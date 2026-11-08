package robot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ntmevents "github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/integrations/pt"
	"github.com/Dicklesworthstone/ntm/internal/robot/adapters"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/watcher"
	"pgregory.net/rapid"
)

func newTestAttentionFeed(t *testing.T) *AttentionFeed {
	t.Helper()

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	t.Cleanup(feed.Stop)
	return feed
}

func mustLoggedAttentionEvent(t *testing.T, event ntmevents.Event) AttentionEvent {
	t.Helper()

	normalized, ok := NewLoggedAttentionEvent(event)
	if !ok {
		t.Fatalf("expected logged event %q to normalize", event.Type)
	}
	return normalized
}

func mustBusAttentionEvent(t *testing.T, event ntmevents.BusEvent) AttentionEvent {
	t.Helper()

	normalized, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatalf("expected bus event %T to normalize", event)
	}
	return normalized
}

func digestTestEvent(cursor int64, category EventCategory, eventType EventType, actionability Actionability, severity Severity, summary string) AttentionEvent {
	return AttentionEvent{
		Cursor:        cursor,
		Ts:            time.Date(2026, 3, 21, 4, 0, 0, 0, time.UTC).Add(time.Duration(cursor) * time.Second).Format(time.RFC3339Nano),
		Session:       "proj",
		Pane:          2,
		Category:      category,
		Type:          eventType,
		Actionability: actionability,
		Severity:      severity,
		Summary:       summary,
		Details: map[string]any{
			"pane_ref": "2",
		},
	}
}

func TestPtAttentionSessionUsesFinalSeparator(t *testing.T) {

	if got := ptAttentionSession("", "my__project__cc_1"); got != "my__project" {
		t.Fatalf("ptAttentionSession() = %q, want %q", got, "my__project")
	}
}

// =============================================================================
// Cursor Allocator Tests
// =============================================================================

func TestCursorAllocator_Monotonic(t *testing.T) {
	alloc := NewCursorAllocator()

	// Cursors must be strictly increasing
	prev := int64(0)
	for i := 0; i < 1000; i++ {
		cur := alloc.Next()
		if cur <= prev {
			t.Errorf("cursor %d not greater than previous %d", cur, prev)
		}
		prev = cur
	}
}

func TestCursorAllocator_Current(t *testing.T) {
	alloc := NewCursorAllocator()

	// Current returns 0 before any allocations
	if got := alloc.Current(); got != 0 {
		t.Errorf("Current() before allocation = %d, want 0", got)
	}

	// Current returns the last allocated cursor
	c1 := alloc.Next()
	if got := alloc.Current(); got != c1 {
		t.Errorf("Current() after Next() = %d, want %d", got, c1)
	}

	c2 := alloc.Next()
	if got := alloc.Current(); got != c2 {
		t.Errorf("Current() after second Next() = %d, want %d", got, c2)
	}
}

func TestCursorAllocator_Concurrent(t *testing.T) {
	alloc := NewCursorAllocator()
	const goroutines = 100
	const iterations = 100

	seen := make(map[int64]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				c := alloc.Next()
				mu.Lock()
				if seen[c] {
					t.Errorf("duplicate cursor %d", c)
				}
				seen[c] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	expected := goroutines * iterations
	if len(seen) != expected {
		t.Errorf("got %d unique cursors, want %d", len(seen), expected)
	}

	// Verify cursors are monotonic (all unique and starting from 1)
	for i := int64(1); i <= int64(expected); i++ {
		if !seen[i] {
			t.Errorf("cursor %d not allocated", i)
		}
	}
}

// =============================================================================
// Attention Journal Tests
// =============================================================================

func TestAttentionJournal_AppendAndReplay(t *testing.T) {
	journal := NewAttentionJournal(100, time.Hour)

	// Append some events
	events := []AttentionEvent{
		{Cursor: 1, Ts: "2026-03-20T10:00:00Z", Summary: "Event 1"},
		{Cursor: 2, Ts: "2026-03-20T10:00:01Z", Summary: "Event 2"},
		{Cursor: 3, Ts: "2026-03-20T10:00:02Z", Summary: "Event 3"},
	}
	for _, e := range events {
		journal.Append(e)
	}

	// Replay from start (cursor 0)
	got, newest, err := journal.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay(0) error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("Replay(0) returned %d events, want 3", len(got))
	}
	if newest != 3 {
		t.Errorf("newest cursor = %d, want 3", newest)
	}

	// Replay from cursor 1 (should get events 2 and 3)
	got, _, err = journal.Replay(1, 100)
	if err != nil {
		t.Fatalf("Replay(1) error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Replay(1) returned %d events, want 2", len(got))
	}

	// Replay from cursor -1 (start from "now")
	got, newest, err = journal.Replay(-1, 100)
	if err != nil {
		t.Fatalf("Replay(-1) error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Replay(-1) returned %d events, want 0", len(got))
	}
	if newest != 3 {
		t.Errorf("newest cursor from Replay(-1) = %d, want 3", newest)
	}
}

func TestAttentionJournal_Limit(t *testing.T) {
	journal := NewAttentionJournal(100, time.Hour)

	// Append 10 events
	for i := int64(1); i <= 10; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}

	// Replay with limit
	got, _, err := journal.Replay(0, 5)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("got %d events, want 5", len(got))
	}

	// Events should be oldest first
	if got[0].Cursor != 1 {
		t.Errorf("first event cursor = %d, want 1", got[0].Cursor)
	}
}

func TestAttentionJournal_Wraparound(t *testing.T) {
	journal := NewAttentionJournal(5, time.Hour)

	// Append 10 events (will wrap around)
	for i := int64(1); i <= 10; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}

	// Should only have the last 5 events
	stats := journal.Stats()
	if stats.Count != 5 {
		t.Errorf("count = %d, want 5", stats.Count)
	}
	if stats.OldestCursor < 6 {
		t.Errorf("oldest cursor = %d, want >= 6", stats.OldestCursor)
	}
	if stats.NewestCursor != 10 {
		t.Errorf("newest cursor = %d, want 10", stats.NewestCursor)
	}

	// Replay all should return 5 events
	got, _, err := journal.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("got %d events, want 5", len(got))
	}
}

func TestAttentionJournal_CursorExpired(t *testing.T) {
	journal := NewAttentionJournal(5, time.Hour)

	// Append events to cause wraparound
	for i := int64(1); i <= 10; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}

	// Try to replay from expired cursor
	_, _, err := journal.Replay(3, 100)
	if err == nil {
		t.Fatal("expected CursorExpiredError, got nil")
	}

	expErr, ok := err.(*CursorExpiredError)
	if !ok {
		t.Fatalf("expected *CursorExpiredError, got %T", err)
	}

	if expErr.RequestedCursor != 3 {
		t.Errorf("RequestedCursor = %d, want 3", expErr.RequestedCursor)
	}
	if expErr.EarliestCursor < 6 {
		t.Errorf("EarliestCursor = %d, want >= 6", expErr.EarliestCursor)
	}

	// Verify ToDetails
	details := expErr.ToDetails()
	if details.RequestedCursor != 3 {
		t.Errorf("details.RequestedCursor = %d, want 3", details.RequestedCursor)
	}
	if details.ResyncCommand != "ntm --robot-snapshot" {
		t.Errorf("unexpected ResyncCommand: %s", details.ResyncCommand)
	}
}

func TestAttentionJournal_Stats(t *testing.T) {
	journal := NewAttentionJournal(100, time.Hour)

	// Initial stats
	stats := journal.Stats()
	if stats.Size != 100 {
		t.Errorf("Size = %d, want 100", stats.Size)
	}
	if stats.Count != 0 {
		t.Errorf("Count = %d, want 0", stats.Count)
	}

	// After appending
	journal.Append(AttentionEvent{Cursor: 1})
	journal.Append(AttentionEvent{Cursor: 2})

	stats = journal.Stats()
	if stats.Count != 2 {
		t.Errorf("Count = %d, want 2", stats.Count)
	}
	if stats.TotalAppended != 2 {
		t.Errorf("TotalAppended = %d, want 2", stats.TotalAppended)
	}
}

// =============================================================================
// Attention Feed Tests
// =============================================================================

func TestAttentionFeed_Append(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0, // Disable heartbeats for tests
	})
	defer feed.Stop()

	// Append event without cursor (feed assigns it)
	event := AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Summary:       "Test event",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
	}

	result := feed.Append(event)

	// Cursor should be assigned
	if result.Cursor == 0 {
		t.Error("cursor not assigned")
	}

	// Timestamp should be set
	if result.Ts == "" {
		t.Error("timestamp not set")
	}

	// Should be replayable
	events, _, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want 1", len(events))
	}
}

func TestAttentionFeed_Subscribe(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	received := make([]AttentionEvent, 0)
	var mu sync.Mutex

	unsub := feed.Subscribe(func(e AttentionEvent) {
		mu.Lock()
		received = append(received, e)
		mu.Unlock()
	})

	// Append events
	feed.Append(AttentionEvent{Summary: "Event 1"})
	feed.Append(AttentionEvent{Summary: "Event 2"})

	mu.Lock()
	if len(received) != 2 {
		t.Errorf("received %d events, want 2", len(received))
	}
	mu.Unlock()

	// Unsubscribe
	unsub()

	// Further events should not be received
	feed.Append(AttentionEvent{Summary: "Event 3"})

	mu.Lock()
	if len(received) != 2 {
		t.Errorf("received %d events after unsub, want 2", len(received))
	}
	mu.Unlock()
}

func TestAttentionFeed_CurrentCursor(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	if got := feed.CurrentCursor(); got != 0 {
		t.Errorf("CurrentCursor() before any events = %d, want 0", got)
	}

	e1 := feed.Append(AttentionEvent{Summary: "Event 1"})
	if got := feed.CurrentCursor(); got != e1.Cursor {
		t.Errorf("CurrentCursor() = %d, want %d", got, e1.Cursor)
	}

	e2 := feed.Append(AttentionEvent{Summary: "Event 2"})
	if got := feed.CurrentCursor(); got != e2.Cursor {
		t.Errorf("CurrentCursor() = %d, want %d", got, e2.Cursor)
	}
}

func TestAttentionFeed_HeartbeatLoopSkipsWithoutSubscribers(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	defer feed.Stop()

	time.Sleep(30 * time.Millisecond)

	if got := feed.CurrentCursor(); got != 0 {
		t.Fatalf("CurrentCursor() = %d, want 0 with no subscribers", got)
	}

	events, newest, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Replay returned %d events, want 0", len(events))
	}
	if newest != 0 {
		t.Fatalf("Replay newest = %d, want 0", newest)
	}
}

func TestAttentionFeed_HeartbeatLoopPublishesEphemeralHeartbeats(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	defer feed.Stop()

	heartbeats := make(chan AttentionEvent, 4)
	unsub := feed.Subscribe(func(e AttentionEvent) {
		if e.Type != EventType(DefaultTransportLiveness.HeartbeatType) {
			return
		}
		select {
		case heartbeats <- e:
		default:
		}
	})
	defer unsub()

	var heartbeat AttentionEvent
	select {
	case heartbeat = <-heartbeats:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for heartbeat")
	}

	if heartbeat.Cursor <= 0 {
		t.Fatalf("heartbeat cursor = %d, want positive", heartbeat.Cursor)
	}
	switch got := heartbeat.Details["subscriber_count"].(type) {
	case int:
		if got != 1 {
			t.Fatalf("heartbeat subscriber_count = %d, want 1", got)
		}
	case int64:
		if got != 1 {
			t.Fatalf("heartbeat subscriber_count = %d, want 1", got)
		}
	case float64:
		if got != 1 {
			t.Fatalf("heartbeat subscriber_count = %v, want 1", got)
		}
	default:
		t.Fatalf("heartbeat subscriber_count type = %T, value = %v, want numeric 1", got, got)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Replay returned %d events, want 0 for live-only heartbeats", len(events))
	}
	if got := feed.CurrentCursor(); got < heartbeat.Cursor {
		t.Fatalf("CurrentCursor() = %d, want at least %d", got, heartbeat.Cursor)
	}
}

func TestAttentionFeed_HeartbeatLoopDefersHeartbeatUntilQuiet(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 40 * time.Millisecond,
	})
	defer feed.Stop()

	heartbeats := make(chan AttentionEvent, 4)
	unsub := feed.Subscribe(func(e AttentionEvent) {
		if e.Type != EventType(DefaultTransportLiveness.HeartbeatType) {
			return
		}
		select {
		case heartbeats <- e:
		default:
		}
	})
	defer unsub()

	feed.Append(AttentionEvent{
		Category: EventCategoryAlert,
		Type:     EventTypeAlertAttentionRequired,
		Summary:  "real event proves liveness",
	})

	select {
	case heartbeat := <-heartbeats:
		t.Fatalf("heartbeat arrived before quiet period elapsed: %#v", heartbeat)
	case <-time.After(25 * time.Millisecond):
	}

	var heartbeat AttentionEvent
	select {
	case heartbeat = <-heartbeats:
	case <-time.After(120 * time.Millisecond):
		t.Fatal("timed out waiting for deferred heartbeat")
	}

	if got, ok := heartbeat.Details["last_event_time"].(string); !ok || got == "" {
		t.Fatalf("heartbeat last_event_time = %#v, want RFC3339 string", heartbeat.Details["last_event_time"])
	}
	switch got := heartbeat.Details["idle_ms"].(type) {
	case int64:
		if got < 0 {
			t.Fatalf("heartbeat idle_ms = %d, want non-negative", got)
		}
	case float64:
		if got < 0 {
			t.Fatalf("heartbeat idle_ms = %v, want non-negative", got)
		}
	default:
		t.Fatalf("heartbeat idle_ms type = %T, value = %v, want numeric", got, got)
	}
}

func TestAttentionFeed_StopWaitsForHeartbeatDelivery(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 5 * time.Millisecond,
	})

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once

	unsub := feed.Subscribe(func(e AttentionEvent) {
		if e.Type != EventType(DefaultTransportLiveness.HeartbeatType) {
			return
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	})
	defer unsub()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		feed.Stop()
	}()

	select {
	case <-started:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("timed out waiting for heartbeat delivery to start")
	}

	stopped := make(chan struct{})
	go func() {
		feed.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("Stop() returned before in-flight heartbeat delivery completed")
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(release) })

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop() did not wait for heartbeat delivery")
	}
}

func TestAttentionFeed_ConcurrentAppend(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	const goroutines = 10
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				feed.Append(AttentionEvent{Summary: "Concurrent event"})
			}
		}()
	}
	wg.Wait()

	// All events should be in journal
	stats := feed.Stats()
	expected := int64(goroutines * iterations)
	if stats.TotalAppended != expected {
		t.Errorf("TotalAppended = %d, want %d", stats.TotalAppended, expected)
	}
}

func TestAttentionFeed_SubscriberPanicRecovery(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	var received atomic.Int32

	// Subscriber that panics
	feed.Subscribe(func(e AttentionEvent) {
		panic("test panic")
	})

	// Good subscriber
	feed.Subscribe(func(e AttentionEvent) {
		received.Add(1)
	})

	// Append should not crash despite panicking subscriber
	feed.Append(AttentionEvent{Summary: "Event 1"})

	if got := received.Load(); got != 1 {
		t.Errorf("good subscriber received = %d, want 1", got)
	}
}

func TestAttentionFeed_SubscriberCanAppendFollowOnEvent(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	feed.Subscribe(func(e AttentionEvent) {
		if e.Summary == "seed" {
			feed.Append(AttentionEvent{Summary: "follow-up"})
		}
	})

	done := make(chan struct{})
	go func() {
		feed.Append(AttentionEvent{Summary: "seed"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Append deadlocked when subscriber emitted a follow-up event")
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Summary != "seed" || events[1].Summary != "follow-up" {
		t.Fatalf("unexpected replay order: %#v", events)
	}
}

func TestAttentionFeed_SubscriberFollowOnEventPreservesDeliveryOrder(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	feed.Subscribe(func(e AttentionEvent) {
		if e.Summary == "seed" {
			feed.Append(AttentionEvent{Summary: "follow-up"})
		}
	})

	received := make([]string, 0, 2)
	feed.Subscribe(func(e AttentionEvent) {
		received = append(received, e.Summary)
	})

	feed.Append(AttentionEvent{Summary: "seed"})

	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}
	if received[0] != "seed" || received[1] != "follow-up" {
		t.Fatalf("delivery order = %#v, want []string{\"seed\", \"follow-up\"}", received)
	}
}

func TestAttentionFeed_QueuedFollowOnEventUsesStableSnapshot(t *testing.T) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	details := map[string]any{"status": "original"}
	received := make(chan AttentionEvent, 1)

	feed.Subscribe(func(e AttentionEvent) {
		if e.Summary == "seed" {
			followUp := AttentionEvent{
				Summary:       "follow-up",
				Details:       details,
				Actionability: ActionabilityInteresting,
			}
			feed.Append(followUp)
			details["status"] = "mutated"
			return
		}
		if e.Summary == "follow-up" {
			received <- e
		}
	})

	feed.Append(AttentionEvent{Summary: "seed"})

	var delivered AttentionEvent
	select {
	case delivered = <-received:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for follow-up delivery")
	}

	if got := delivered.Details["status"]; got != "original" {
		t.Fatalf("delivered status = %v, want %q", got, "original")
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if got := events[1].Details["status"]; got != "original" {
		t.Fatalf("replayed status = %v, want %q", got, "original")
	}
}

func TestAttentionFeed_PublishTrackerChange(t *testing.T) {
	feed := newTestAttentionFeed(t)

	change := tracker.StateChange{
		Timestamp: time.Date(2026, 3, 21, 3, 5, 0, 0, time.UTC),
		Type:      tracker.ChangeAgentOutput,
		Session:   "proj",
		Pane:      "2",
		Details: map[string]interface{}{
			"line_count": 3,
		},
	}

	published := feed.PublishTrackerChange(change)
	t.Logf("published tracker event cursor=%d type=%s summary=%q", published.Cursor, published.Type, published.Summary)

	if published.Cursor == 0 {
		t.Fatal("tracker change was not assigned a cursor")
	}
	if published.Type != EventTypePaneOutput {
		t.Fatalf("tracker change type = %q, want %q", published.Type, EventTypePaneOutput)
	}
	if published.Pane != 2 {
		t.Fatalf("tracker change pane = %d, want 2", published.Pane)
	}
	if published.Details["pane_ref"] != "2" {
		t.Fatalf("tracker change pane_ref = %#v, want %q", published.Details["pane_ref"], "2")
	}

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d tracker events, want 1", len(replayed))
	}
	if replayed[0].Summary != published.Summary {
		t.Fatalf("replayed summary = %q, want %q", replayed[0].Summary, published.Summary)
	}
}

func TestAttentionFeed_PublishLoggedEvent_Suppressed(t *testing.T) {
	feed := newTestAttentionFeed(t)

	published, ok := feed.PublishLoggedEvent(ntmevents.Event{
		Timestamp: time.Date(2026, 3, 21, 3, 6, 0, 0, time.UTC),
		Type:      ntmevents.EventPromptSend,
		Session:   "proj",
		Data: map[string]interface{}{
			"pane_index": 1,
		},
	})
	t.Logf("suppressed logged event ok=%v published=%+v", ok, published)

	if ok {
		t.Fatal("prompt_send should be suppressed from the attention feed")
	}
	if stats := feed.Stats(); stats.TotalAppended != 0 {
		t.Fatalf("TotalAppended = %d, want 0 for suppressed logged event", stats.TotalAppended)
	}
}

func TestAttentionFeed_PublishActuationOutcome(t *testing.T) {
	feed := newTestAttentionFeed(t)

	published := feed.PublishActuation(ActuationRecord{
		Session:        "proj",
		Action:         "send",
		Stage:          ActuationStageOutcome,
		Source:         "robot.send",
		Method:         "paste_enter",
		RequestID:      "req-123",
		CorrelationID:  "corr-123",
		IdempotencyKey: "idem-123",
		Targets:        []string{"2"},
		Successful:     []string{"2"},
		MessagePreview: "safe preview",
		Result:         "succeeded",
		Summary:        "send completed for 1 target(s)",
		ReasonCode:     "actuation_succeeded",
		Actionability:  ActionabilityInteresting,
		Severity:       SeverityInfo,
	})

	if published.Category != EventCategoryActuation {
		t.Fatalf("Category = %q, want %q", published.Category, EventCategoryActuation)
	}
	if published.Type != EventTypeActuationOutcome {
		t.Fatalf("Type = %q, want %q", published.Type, EventTypeActuationOutcome)
	}
	if published.Pane != 2 {
		t.Fatalf("Pane = %d, want 2", published.Pane)
	}
	if got := published.Details["request_id"]; got != "req-123" {
		t.Fatalf("request_id = %#v, want req-123", got)
	}
	if got := published.Details["correlation_id"]; got != "corr-123" {
		t.Fatalf("correlation_id = %#v, want corr-123", got)
	}
	if got := published.Details["idempotency_key"]; got != "idem-123" {
		t.Fatalf("idempotency_key = %#v, want idem-123", got)
	}
	if got := published.Details["target_ref"]; got != "session:proj:2" {
		t.Fatalf("target_ref = %#v, want session:proj:2", got)
	}
	if got := published.Details["message_preview"]; got != "safe preview" {
		t.Fatalf("message_preview = %#v, want safe preview", got)
	}

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d actuation events, want 1", len(replayed))
	}
	if replayed[0].ReasonCode != "actuation_succeeded" {
		t.Fatalf("ReasonCode = %q, want actuation_succeeded", replayed[0].ReasonCode)
	}
}

func TestAttentionFeed_PublishActuationVerificationTimeout(t *testing.T) {
	feed := newTestAttentionFeed(t)

	published := feed.PublishActuation(ActuationRecord{
		Session:       "proj",
		Action:        "interrupt",
		Stage:         ActuationStageVerification,
		Source:        "robot.interrupt",
		Method:        "ctrl_c",
		RequestID:     "req-verify",
		CorrelationID: "corr-verify",
		Targets:       []string{"1", "3"},
		Successful:    []string{"1"},
		Pending:       []string{"3"},
		Verification:  "timed_out",
		TimedOut:      true,
		Summary:       "interrupt verification timed out for 1 target(s)",
		ReasonCode:    "actuation_verification_timeout",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
	})

	if published.Type != EventTypeActuationVerified {
		t.Fatalf("Type = %q, want %q", published.Type, EventTypeActuationVerified)
	}
	if published.Actionability != ActionabilityActionRequired {
		t.Fatalf("Actionability = %q, want %q", published.Actionability, ActionabilityActionRequired)
	}
	if published.Severity != SeverityWarning {
		t.Fatalf("Severity = %q, want %q", published.Severity, SeverityWarning)
	}
	if got := published.Details["verification_state"]; got != "timed_out" {
		t.Fatalf("verification_state = %#v, want timed_out", got)
	}
	if got := published.Details["pending_count"]; got != 1 {
		t.Fatalf("pending_count = %#v, want 1", got)
	}
	if got := published.Details["timed_out"]; got != true {
		t.Fatalf("timed_out = %#v, want true", got)
	}
}

func TestAttentionFeed_PublishBusHistoryOrdersOldestFirst(t *testing.T) {
	feed := newTestAttentionFeed(t)
	bus := ntmevents.NewEventBus(10)

	first := ntmevents.AlertEvent{
		BaseEvent: ntmevents.BaseEvent{
			Type:      "alert",
			Timestamp: time.Date(2026, 3, 21, 3, 7, 0, 0, time.UTC),
			Session:   "proj",
		},
		AlertID:   "alert-1",
		AlertType: "health",
		Severity:  "warning",
		Message:   "first warning",
	}
	second := ntmevents.AgentStallEvent{
		BaseEvent: ntmevents.BaseEvent{
			Type:      "agent_stall",
			Timestamp: time.Date(2026, 3, 21, 3, 8, 0, 0, time.UTC),
			Session:   "proj",
		},
		AgentID:       "cod-1",
		StallDuration: 45,
		LastActivity:  "waiting",
	}

	bus.PublishSync(first)
	bus.PublishSync(second)

	published := feed.PublishBusHistory(bus, 10)
	if len(published) != 2 {
		t.Fatalf("published %d bus history events, want 2", len(published))
	}
	t.Logf("published bus history summaries=%q then %q", published[0].Summary, published[1].Summary)
	if published[0].Details["alert_id"] != "alert-1" {
		t.Fatalf("first published history event = %#v, want alert-1 first", published[0].Details["alert_id"])
	}
	if published[1].Type != EventTypeAgentStalled {
		t.Fatalf("second published history type = %q, want %q", published[1].Type, EventTypeAgentStalled)
	}
	if published[0].Cursor >= published[1].Cursor {
		t.Fatalf("history cursors not increasing oldest-first: %d then %d", published[0].Cursor, published[1].Cursor)
	}
}

func TestAttentionFeed_SubscribeEventBus(t *testing.T) {
	feed := newTestAttentionFeed(t)
	bus := ntmevents.NewEventBus(10)

	unsubscribeBus := feed.SubscribeEventBus(bus)
	defer unsubscribeBus()

	bus.PublishSync(ntmevents.NewAgentErrorEvent("proj", "cc-1", "auth", "token expired"))

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay after unsubscribe error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d events after unsubscribe, want 1", len(replayed))
	}
	if replayed[0].Type != EventTypeAgentError {
		t.Fatalf("live bus event type = %q, want %q", replayed[0].Type, EventTypeAgentError)
	}

	unsubscribeBus()
	bus.PublishSync(ntmevents.NewAlertEvent("proj", "alert-2", "health", "warning", "after unsubscribe"))

	replayed, _, err = feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay after unsubscribe error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d events after unsubscribe, want 1", len(replayed))
	}
}

// =============================================================================
// Store-backed Persistence Tests (bd-j9jo3.4.1)
// =============================================================================

func TestAttentionFeed_StoreBackedPersistence(t *testing.T) {

	store := newTestAttentionStore(t)

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	// Append event - should persist to store
	event := AttentionEvent{
		Session:       "test-proj",
		Pane:          1,
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "test",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		ReasonCode:    "agent:state:busy",
		Summary:       "agent state changed",
		Details:       map[string]any{"old": "idle", "new": "busy"},
		NextActions: []NextAction{{
			Action: "robot-status",
			Args:   "--robot-status",
			Reason: "Inspect the updated agent state",
		}},
		DedupKey: "agent|test-proj|1|busy",
	}
	appended := feed.Append(event)

	if appended.Cursor <= 0 {
		t.Fatalf("appended event should have positive cursor, got %d", appended.Cursor)
	}

	// Verify replay from store works
	replayed, latestCursor, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("expected 1 replayed event, got %d", len(replayed))
	}
	if replayed[0].Cursor != appended.Cursor {
		t.Fatalf("replayed cursor = %d, want %d", replayed[0].Cursor, appended.Cursor)
	}
	if replayed[0].Summary != "agent state changed" {
		t.Fatalf("replayed summary = %q, want %q", replayed[0].Summary, "agent state changed")
	}
	if replayed[0].ReasonCode != event.ReasonCode {
		t.Fatalf("replayed reason code = %q, want %q", replayed[0].ReasonCode, event.ReasonCode)
	}
	if replayed[0].DedupKey != event.DedupKey {
		t.Fatalf("replayed dedup key = %q, want %q", replayed[0].DedupKey, event.DedupKey)
	}
	if len(replayed[0].NextActions) != 1 || replayed[0].NextActions[0].Action != "robot-status" {
		t.Fatalf("replayed next actions = %+v, want robot-status action", replayed[0].NextActions)
	}
	if latestCursor < appended.Cursor {
		t.Fatalf("latest cursor = %d, should be >= appended cursor %d", latestCursor, appended.Cursor)
	}

	// Verify stats reflect store state
	stats := feed.Stats()
	if stats.Count != 1 {
		t.Fatalf("stats.Count = %d, want 1", stats.Count)
	}
	if stats.NewestCursor != appended.Cursor {
		t.Fatalf("stats.NewestCursor = %d, want %d", stats.NewestCursor, appended.Cursor)
	}
}

func TestAttentionFeed_StoreSyncCursorOnStartup(t *testing.T) {

	store := newTestAttentionStore(t)

	// Pre-seed store with events
	for i := 0; i < 5; i++ {
		stored := state.StoredAttentionEvent{
			Ts:            time.Now().UTC(),
			SessionName:   "proj",
			Category:      "agent",
			EventType:     "agent.state_change",
			Source:        "test",
			Actionability: state.ActionabilityInteresting,
			Severity:      state.SeverityInfo,
			Summary:       "seeded event",
			DedupCount:    1,
		}
		if _, err := store.AppendAttentionEvent(&stored); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}

	// Create feed with store - should sync cursor
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	// Cursor should be synced from store (at least 5)
	currentCursor := feed.CurrentCursor()
	if currentCursor < 5 {
		t.Fatalf("cursor should be synced from store (>= 5), got %d", currentCursor)
	}

	// New events should get cursors after the synced value
	appended := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySystem,
		Type:          EventTypeSystemHealthChange,
		Source:        "test",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "new event after sync",
	})

	if appended.Cursor <= currentCursor {
		t.Fatalf("new event cursor %d should be > synced cursor %d", appended.Cursor, currentCursor)
	}
}

func TestAttentionFeed_ReplayForIncidentIncludesBoundedContext(t *testing.T) {

	store := newTestAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	appendStored := func(ts time.Time, summary string) int64 {
		t.Helper()
		cursor, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
			Ts:            ts,
			SessionName:   "proj",
			Category:      "incident",
			EventType:     "incident.replayed",
			Source:        "test",
			Actionability: state.ActionabilityInteresting,
			Severity:      state.SeverityWarning,
			Summary:       summary,
		})
		if err != nil {
			t.Fatalf("AppendAttentionEvent(%q): %v", summary, err)
		}
		return cursor
	}

	contextCursor := appendStored(now.Add(-25*time.Second), "context")
	firstIncidentCursor := appendStored(now.Add(-18*time.Second), "incident-1")
	secondIncidentCursor := appendStored(now.Add(-11*time.Second), "incident-2")
	appendStored(now.Add(-5*time.Second), "after")

	if err := store.CreateIncident(&state.Incident{
		ID:          "inc-replay",
		Title:       "incident replay",
		Fingerprint: "incident.replay:test",
		Family:      "incident.replay",
		Category:    "testing",
		Status:      state.IncidentStatusOpen,
		Severity:    state.SeverityWarning,
		EventCount:  2,
		StartedAt:   now.Add(-18 * time.Second),
		LastEventAt: now.Add(-11 * time.Second),
	}); err != nil {
		t.Fatalf("CreateIncident(): %v", err)
	}
	for _, cursor := range []int64{firstIncidentCursor, secondIncidentCursor} {
		if err := store.LinkEventToIncident("inc-replay", cursor); err != nil {
			t.Fatalf("LinkEventToIncident(%d): %v", cursor, err)
		}
	}

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	events, latestCursor, err := feed.ReplayForIncident("inc-replay", 10*time.Second, 0)
	if err != nil {
		t.Fatalf("ReplayForIncident() error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("ReplayForIncident() returned %d events, want 3", len(events))
	}
	if got := []int64{events[0].Cursor, events[1].Cursor, events[2].Cursor}; got[0] != contextCursor || got[1] != firstIncidentCursor || got[2] != secondIncidentCursor {
		t.Fatalf("ReplayForIncident() cursors = %v", got)
	}
	if latestCursor < secondIncidentCursor {
		t.Fatalf("latestCursor = %d, want >= %d", latestCursor, secondIncidentCursor)
	}
}

func TestAttentionFeed_ReconstructAsOfReturnsRecentWindowAndMetadata(t *testing.T) {

	store := newTestAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	appendStored := func(ts time.Time, summary string) {
		t.Helper()
		if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
			Ts:            ts,
			SessionName:   "proj",
			Category:      "incident",
			EventType:     "incident.replayed",
			Source:        "test",
			Actionability: state.ActionabilityInteresting,
			Severity:      state.SeverityWarning,
			Summary:       summary,
		}); err != nil {
			t.Fatalf("AppendAttentionEvent(%q): %v", summary, err)
		}
	}

	appendStored(now.Add(-40*time.Second), "older")
	appendStored(now.Add(-20*time.Second), "target-1")
	appendStored(now.Add(-15*time.Second), "target-2")
	appendStored(now.Add(-5*time.Second), "future")

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	events, meta, err := feed.ReconstructAsOf(now.Add(-10*time.Second), 2)
	if err != nil {
		t.Fatalf("ReconstructAsOf() error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("ReconstructAsOf() returned %d events, want 2", len(events))
	}
	if events[0].Summary != "target-1" || events[1].Summary != "target-2" {
		t.Fatalf("ReconstructAsOf() summaries = [%q %q], want [target-1 target-2]", events[0].Summary, events[1].Summary)
	}
	if meta == nil {
		t.Fatal("ReconstructAsOf() meta is nil")
	}
	if meta.Method != "event_replay" {
		t.Fatalf("meta.Method = %q, want event_replay", meta.Method)
	}
	if meta.Confidence != ReconstructionConfidenceMedium {
		t.Fatalf("meta.Confidence = %q, want %q", meta.Confidence, ReconstructionConfidenceMedium)
	}
	if meta.EventsReplayed != 2 {
		t.Fatalf("meta.EventsReplayed = %d, want 2", meta.EventsReplayed)
	}
	if meta.ActualEnd != events[1].Ts {
		t.Fatalf("meta.ActualEnd = %q, want %q", meta.ActualEnd, events[1].Ts)
	}

	foundReconstructed := false
	foundPartial := false
	for _, warning := range meta.Warnings {
		if warning == "RECONSTRUCTED" {
			foundReconstructed = true
		}
		if warning == "PARTIAL_DATA" {
			foundPartial = true
		}
	}
	if !foundReconstructed || !foundPartial {
		t.Fatalf("meta.Warnings = %#v, want RECONSTRUCTED and PARTIAL_DATA", meta.Warnings)
	}
}

func TestBuildEventsOutput_IncidentReplayIncludesHistoricalMetadata(t *testing.T) {

	store := newTestAttentionStore(t)
	oldStore := currentProjectionStore()
	SetProjectionStore(store)
	t.Cleanup(func() {
		SetProjectionStore(oldStore)
	})

	now := time.Now().UTC().Truncate(time.Second)
	appendStored := func(ts time.Time, summary string) int64 {
		t.Helper()
		cursor, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
			Ts:            ts,
			SessionName:   "proj",
			Category:      "incident",
			EventType:     "incident.replayed",
			Source:        "test",
			Actionability: state.ActionabilityInteresting,
			Severity:      state.SeverityWarning,
			Summary:       summary,
		})
		if err != nil {
			t.Fatalf("AppendAttentionEvent(%q): %v", summary, err)
		}
		return cursor
	}

	contextCursor := appendStored(now.Add(-25*time.Second), "context")
	firstIncidentCursor := appendStored(now.Add(-18*time.Second), "incident-1")
	secondIncidentCursor := appendStored(now.Add(-11*time.Second), "incident-2")
	appendStored(now.Add(-5*time.Second), "after")

	if err := store.CreateIncident(&state.Incident{
		ID:          "inc-replay",
		Title:       "incident replay",
		Fingerprint: "incident.replay:test",
		Family:      "incident.replay",
		Category:    "testing",
		Status:      state.IncidentStatusOpen,
		Severity:    state.SeverityWarning,
		EventCount:  2,
		StartedAt:   now.Add(-18 * time.Second),
		LastEventAt: now.Add(-11 * time.Second),
	}); err != nil {
		t.Fatalf("CreateIncident(): %v", err)
	}
	for _, cursor := range []int64{firstIncidentCursor, secondIncidentCursor} {
		if err := store.LinkEventToIncident("inc-replay", cursor); err != nil {
			t.Fatalf("LinkEventToIncident(%d): %v", cursor, err)
		}
	}

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	oldFeed := PeekAttentionFeed()
	SetAttentionFeed(feed)
	t.Cleanup(func() {
		SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	output, status := BuildEventsOutput(EventsOptions{
		IncidentID:   "inc-replay",
		Limit:        10,
		WindowBefore: 10 * time.Second,
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if !output.Success {
		t.Fatalf("expected success, got %#v", output)
	}
	if output.ReplayTarget == nil || output.ReplayTarget.Mode != "incident_replay" {
		t.Fatalf("ReplayTarget = %#v, want incident_replay", output.ReplayTarget)
	}
	if output.ReplayTarget.Ref != "incident:inc-replay" {
		t.Fatalf("ReplayTarget.Ref = %q, want incident:inc-replay", output.ReplayTarget.Ref)
	}
	if output.Incident == nil || output.Incident.ID != "inc-replay" {
		t.Fatalf("Incident = %#v, want inc-replay anchor", output.Incident)
	}
	if output.Boundedness == nil || output.Boundedness.RequestedRangeMS != (10*time.Second+DefaultIncidentReplayWindowAfter).Milliseconds() {
		t.Fatalf("Boundedness = %#v, want requested_range_ms=%d", output.Boundedness, (10*time.Second + DefaultIncidentReplayWindowAfter).Milliseconds())
	}
	if len(output.Events) != 4 {
		t.Fatalf("len(output.Events) = %d, want 4", len(output.Events))
	}
	if got := []int64{output.Events[0].Cursor, output.Events[1].Cursor, output.Events[2].Cursor, output.Events[3].Cursor}; got[0] != contextCursor || got[1] != firstIncidentCursor || got[2] != secondIncidentCursor || got[3] <= secondIncidentCursor {
		t.Fatalf("output event cursors = %v", got)
	}
}

func TestBuildEventsOutput_AsOfIncludesHistoricalMetadata(t *testing.T) {

	store := newTestAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	appendStored := func(ts time.Time, summary string) {
		t.Helper()
		if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
			Ts:            ts,
			SessionName:   "proj",
			Category:      "incident",
			EventType:     "incident.replayed",
			Source:        "test",
			Actionability: state.ActionabilityInteresting,
			Severity:      state.SeverityWarning,
			Summary:       summary,
		}); err != nil {
			t.Fatalf("AppendAttentionEvent(%q): %v", summary, err)
		}
	}

	appendStored(now.Add(-40*time.Second), "older")
	appendStored(now.Add(-20*time.Second), "target-1")
	appendStored(now.Add(-15*time.Second), "target-2")
	appendStored(now.Add(-5*time.Second), "future")

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	oldFeed := PeekAttentionFeed()
	SetAttentionFeed(feed)
	t.Cleanup(func() {
		SetAttentionFeed(oldFeed)
		feed.Stop()
	})

	asOf := now.Add(-10 * time.Second)
	output, status := BuildEventsOutput(EventsOptions{
		AsOf:  asOf,
		Limit: 2,
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if !output.Success {
		t.Fatalf("expected success, got %#v", output)
	}
	if output.ReplayTarget == nil || output.ReplayTarget.Mode != "as_of" {
		t.Fatalf("ReplayTarget = %#v, want as_of", output.ReplayTarget)
	}
	if output.ReplayTarget.RequestedAt != asOf.Format(time.RFC3339Nano) {
		t.Fatalf("ReplayTarget.RequestedAt = %q, want %q", output.ReplayTarget.RequestedAt, asOf.Format(time.RFC3339Nano))
	}
	if output.Reconstruction == nil || output.Reconstruction.Method != "event_replay" {
		t.Fatalf("Reconstruction = %#v, want event_replay metadata", output.Reconstruction)
	}
	if !output.Reconstruction.Partial {
		t.Fatalf("Reconstruction.Partial = %v, want true", output.Reconstruction.Partial)
	}
	if output.Boundedness == nil || !output.Boundedness.Truncated {
		t.Fatalf("Boundedness = %#v, want truncated historical metadata", output.Boundedness)
	}
	if len(output.Events) != 2 {
		t.Fatalf("len(output.Events) = %d, want 2", len(output.Events))
	}
}

func TestAttentionFeed_ReplayAnnotatesDurableOperatorState(t *testing.T) {

	store := newTestAttentionStore(t)
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	appended := feed.Append(AttentionEvent{
		Session:       "proj",
		Pane:          2,
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Source:        "test",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		ReasonCode:    "alert:quota",
		Summary:       "quota at 92%",
		DedupKey:      "alert|proj|quota",
	})

	itemKey, err := store.ResolveAttentionItemKey(appended.Cursor)
	if err != nil {
		t.Fatalf("ResolveAttentionItemKey() error: %v", err)
	}
	if itemKey != "dedup:alert|proj|quota" {
		t.Fatalf("item key = %q, want %q", itemKey, "dedup:alert|proj|quota")
	}

	acknowledgedAt := time.Now().UTC().Add(-15 * time.Minute).Round(0)
	snoozedUntil := time.Now().UTC().Add(45 * time.Minute).Round(0)
	overrideExpiresAt := time.Now().UTC().Add(2 * time.Hour).Round(0)
	if err := store.UpsertAttentionItemState(&state.AttentionItemState{
		ItemKey:           itemKey,
		DedupKey:          appended.DedupKey,
		State:             state.AttentionStateSnoozed,
		AcknowledgedAt:    &acknowledgedAt,
		AcknowledgedBy:    "operator",
		SnoozedUntil:      &snoozedUntil,
		Pinned:            true,
		Muted:             true,
		OverridePriority:  "urgent",
		OverrideReason:    "manual-escalation",
		OverrideExpiresAt: &overrideExpiresAt,
		ResurfacingPolicy: "manual_review",
	}); err != nil {
		t.Fatalf("UpsertAttentionItemState() error: %v", err)
	}

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay() error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d events, want 1", len(replayed))
	}

	event := replayed[0]
	if got := attentionStringDetail(event.Details, attentionDetailItemKey); got != itemKey {
		t.Fatalf("attention_item_key = %q, want %q", got, itemKey)
	}
	if got := attentionStringDetail(event.Details, attentionDetailState); got != string(state.AttentionStateSnoozed) {
		t.Fatalf("attention_state = %q, want %q", got, state.AttentionStateSnoozed)
	}
	if got := attentionStringDetail(event.Details, attentionDetailAcknowledgedAt); got != acknowledgedAt.Format(time.RFC3339Nano) {
		t.Fatalf("attention_acknowledged_at = %q, want %q", got, acknowledgedAt.Format(time.RFC3339Nano))
	}
	if got := attentionStringDetail(event.Details, attentionDetailAcknowledgedBy); got != "operator" {
		t.Fatalf("attention_acknowledged_by = %q, want operator", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailSnoozedUntil); got != snoozedUntil.Format(time.RFC3339Nano) {
		t.Fatalf("attention_snoozed_until = %q, want %q", got, snoozedUntil.Format(time.RFC3339Nano))
	}
	if got := event.Details[attentionDetailPinned]; got != true {
		t.Fatalf("attention_pinned = %#v, want true", got)
	}
	if got := event.Details[attentionDetailMuted]; got != true {
		t.Fatalf("attention_muted = %#v, want true", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailOverridePriority); got != "urgent" {
		t.Fatalf("attention_override_priority = %q, want urgent", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailOverrideReason); got != "manual-escalation" {
		t.Fatalf("attention_override_reason = %q, want manual-escalation", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailOverrideExpiresAt); got != overrideExpiresAt.Format(time.RFC3339Nano) {
		t.Fatalf("attention_override_expires_at = %q, want %q", got, overrideExpiresAt.Format(time.RFC3339Nano))
	}
	if got := attentionStringDetail(event.Details, attentionDetailResurfacingPolicy); got != "manual_review" {
		t.Fatalf("attention_resurfacing_policy = %q, want manual_review", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailFingerprint); got == "" {
		t.Fatal("attention_fingerprint should be populated")
	}
	if got := attentionStringDetail(event.Details, attentionDetailExplanationCode); got != attentionExplainPinned {
		t.Fatalf("attention_explanation_code = %q, want %q", got, attentionExplainPinned)
	}
}

func TestAttentionFeed_ReplayResurfacesAcknowledgedItemOnFingerprintChange(t *testing.T) {

	store := newTestAttentionStore(t)
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	appended := feed.Append(AttentionEvent{
		Session:       "proj",
		Pane:          1,
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Source:        "test",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityWarning,
		ReasonCode:    "alert:quota",
		Summary:       "quota at 95%",
		DedupKey:      "alert|proj|quota",
	})

	itemKey, err := store.ResolveAttentionItemKey(appended.Cursor)
	if err != nil {
		t.Fatalf("ResolveAttentionItemKey() error: %v", err)
	}

	if err := store.UpsertAttentionItemState(&state.AttentionItemState{
		ItemKey:           itemKey,
		DedupKey:          appended.DedupKey,
		State:             state.AttentionStateAcknowledged,
		Fingerprint:       "fp-old-occurrence",
		AcknowledgedBy:    "operator",
		ResurfacingPolicy: "on_change",
	}); err != nil {
		t.Fatalf("UpsertAttentionItemState() error: %v", err)
	}

	replayed, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay() error: %v", err)
	}
	if len(replayed) != 1 {
		t.Fatalf("replayed %d events, want 1", len(replayed))
	}

	event := replayed[0]
	if got := attentionStringDetail(event.Details, attentionDetailState); got != string(state.AttentionStateNew) {
		t.Fatalf("attention_state = %q, want %q", got, state.AttentionStateNew)
	}
	if got := attentionStringDetail(event.Details, attentionDetailStoredState); got != string(state.AttentionStateAcknowledged) {
		t.Fatalf("attention_stored_state = %q, want %q", got, state.AttentionStateAcknowledged)
	}
	if got := attentionStringDetail(event.Details, attentionDetailPreviousState); got != string(state.AttentionStateAcknowledged) {
		t.Fatalf("attention_previous_state = %q, want %q", got, state.AttentionStateAcknowledged)
	}
	if got := event.Details[attentionDetailResurfaced]; got != true {
		t.Fatalf("resurfaced = %#v, want true", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailResurfaceReason); got != attentionResurfaceReasonFingerprintChange {
		t.Fatalf("resurface_reason = %q, want %q", got, attentionResurfaceReasonFingerprintChange)
	}
	if got := attentionStringDetail(event.Details, attentionDetailStoredFingerprint); got != "fp-old-occurrence" {
		t.Fatalf("attention_stored_fingerprint = %q, want fp-old-occurrence", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailPreviousFingerprint); got != "fp-old-occurrence" {
		t.Fatalf("attention_previous_fingerprint = %q, want fp-old-occurrence", got)
	}
	if got := attentionStringDetail(event.Details, attentionDetailExplanationCode); got != attentionExplainResurfaced {
		t.Fatalf("attention_explanation_code = %q, want %q", got, attentionExplainResurfaced)
	}
	if got := attentionStringDetail(event.Details, attentionDetailHiddenReason); got != "" {
		t.Fatalf("attention_hidden_reason = %q, want empty", got)
	}
}

func TestBuildAttentionDigest_RespectsOperatorStateVisibility(t *testing.T) {

	base := digestTestEvent(1, EventCategoryAlert, EventTypeAlertWarning, ActionabilityInteresting, SeverityWarning, "operator-managed alert")

	acknowledged := annotateAttentionOperatorState(cloneAttentionEvent(base), "dedup:ack", &state.AttentionItemState{
		ItemKey:     "dedup:ack",
		State:       state.AttentionStateAcknowledged,
		Fingerprint: attentionEventFingerprint(base),
	})
	acknowledged.Cursor = 1

	muted := cloneAttentionEvent(base)
	muted.Cursor = 2
	muted.Summary = "muted alert"
	muted = annotateAttentionOperatorState(muted, "dedup:muted", &state.AttentionItemState{
		ItemKey:     "dedup:muted",
		State:       state.AttentionStateNew,
		Fingerprint: attentionEventFingerprint(muted),
		Muted:       true,
	})
	muted.Cursor = 2

	pinned := cloneAttentionEvent(base)
	pinned.Cursor = 3
	pinned.Severity = SeverityDebug
	pinned.Actionability = ActionabilityBackground
	pinned.Summary = "pinned deployment item"
	pinned = annotateAttentionOperatorState(pinned, "dedup:pinned", &state.AttentionItemState{
		ItemKey:     "dedup:pinned",
		State:       state.AttentionStateSnoozed,
		Fingerprint: attentionEventFingerprint(pinned),
		Pinned:      true,
	})
	pinned.Cursor = 3

	digest := BuildAttentionDigest([]AttentionEvent{acknowledged, muted, pinned}, 0, 3, AttentionDigestOptions{
		MinSeverity:      SeverityWarning,
		MinActionability: ActionabilityInteresting,
	})

	if len(digest.Buckets.ActionRequired) != 0 {
		t.Fatalf("action_required bucket = %d, want 0", len(digest.Buckets.ActionRequired))
	}
	if len(digest.Buckets.Interesting) != 1 {
		t.Fatalf("interesting bucket = %d, want 1", len(digest.Buckets.Interesting))
	}
	if got := digest.Buckets.Interesting[0].Event.Summary; got != "pinned deployment item" {
		t.Fatalf("surfaced summary = %q, want pinned deployment item", got)
	}
	if got := digest.Suppressed.ByReason["attention_state_acknowledged"]; got != 1 {
		t.Fatalf("suppressed acknowledged = %d, want 1", got)
	}
	if got := digest.Suppressed.ByReason["attention_state_muted"]; got != 1 {
		t.Fatalf("suppressed muted = %d, want 1", got)
	}
}

func TestAttentionFeed_AppendDeduplicated_AnnotatesCooldownResurfacing(t *testing.T) {

	feed := newTestAttentionFeed(t)
	window := 2 * time.Minute
	baseTS := time.Date(2026, 3, 22, 4, 0, 0, 0, time.UTC)

	baseEvent := AttentionEvent{
		Session:       "proj",
		Pane:          2,
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Source:        "test",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		ReasonCode:    "alert:quota",
		Summary:       "quota at 92%",
		DedupKey:      "alert|proj|quota",
	}

	first := baseEvent
	first.Ts = baseTS.Format(time.RFC3339Nano)
	appended, ok := feed.AppendDeduplicated(first, window)
	if !ok {
		t.Fatal("expected first event to publish")
	}
	if attentionStringDetail(appended.Details, attentionDetailResurfaceReason) != "" {
		t.Fatalf("first event should not have resurface reason, got %+v", appended.Details)
	}

	duplicate1 := baseEvent
	duplicate1.Ts = baseTS.Add(30 * time.Second).Format(time.RFC3339Nano)
	if _, ok := feed.AppendDeduplicated(duplicate1, window); ok {
		t.Fatal("expected duplicate inside cooldown to be suppressed")
	}

	duplicate2 := baseEvent
	duplicate2.Ts = baseTS.Add(90 * time.Second).Format(time.RFC3339Nano)
	if _, ok := feed.AppendDeduplicated(duplicate2, window); ok {
		t.Fatal("expected second duplicate inside cooldown to be suppressed")
	}

	resurfaced := baseEvent
	resurfaced.Ts = baseTS.Add(window + 15*time.Second).Format(time.RFC3339Nano)
	resurfaced.Summary = "quota at 95%"
	appended, ok = feed.AppendDeduplicated(resurfaced, window)
	if !ok {
		t.Fatal("expected event after cooldown to resurface")
	}

	if appended.Details[attentionDetailResurfaced] != true {
		t.Fatalf("resurfaced detail = %#v, want true", appended.Details[attentionDetailResurfaced])
	}
	if reason := attentionStringDetail(appended.Details, attentionDetailResurfaceReason); reason != attentionResurfaceReasonCooldownExpired {
		t.Fatalf("resurface reason = %q, want %q", reason, attentionResurfaceReasonCooldownExpired)
	}
	if count := attentionFloatDetail(appended.Details, attentionDetailCooldownSuppressedCount); count != 2 {
		t.Fatalf("cooldown_suppressed_count = %v, want 2", count)
	}
	if windowMS := attentionFloatDetail(appended.Details, attentionDetailCooldownWindowMS); windowMS != float64(window.Milliseconds()) {
		t.Fatalf("cooldown_window_ms = %v, want %d", windowMS, window.Milliseconds())
	}
	if got := attentionStringDetail(appended.Details, attentionDetailCooldownSuppressedSince); got != duplicate1.Ts {
		t.Fatalf("cooldown_suppressed_since = %q, want %q", got, duplicate1.Ts)
	}
	if got := attentionStringDetail(appended.Details, attentionDetailCooldownLastSuppressed); got != duplicate2.Ts {
		t.Fatalf("cooldown_last_suppressed_at = %q, want %q", got, duplicate2.Ts)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 stored events after cooldown resurfacing, got %d", len(events))
	}
	if got := attentionStringDetail(events[1].Details, attentionDetailResurfaceReason); got != attentionResurfaceReasonCooldownExpired {
		t.Fatalf("replayed resurface reason = %q, want %q", got, attentionResurfaceReasonCooldownExpired)
	}
}

func TestAttentionFeed_StoreCursorExpiration(t *testing.T) {

	store := newTestAttentionStore(t)

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Minute, // Short retention for test
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	// Add events
	for i := int64(1); i <= 100; i++ {
		feed.Append(AttentionEvent{
			Session:       "proj",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Source:        "test",
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       "test event",
		})
	}

	// Replay from valid cursor should work
	events, _, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay(0) error: %v", err)
	}
	if len(events) != 100 {
		t.Fatalf("expected 100 events, got %d", len(events))
	}

	// Replay from cursor=-1 should return empty with current cursor
	events, latestCursor, err := feed.Replay(-1, 100)
	if err != nil {
		t.Fatalf("Replay(-1) error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("Replay(-1) should return 0 events, got %d", len(events))
	}
	if latestCursor < 100 {
		t.Fatalf("latest cursor should be >= 100, got %d", latestCursor)
	}
}

func TestAttentionFeed_StoreWriteFailureFallsBackConsistently(t *testing.T) {

	store := newStubAttentionStore()
	store.seed("persisted")
	store.failAppend.Store(true)

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	first := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Source:        "test",
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "journal-only alert",
	})
	second := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "test",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "journal-only state change",
	})

	if first.Cursor != 2 {
		t.Fatalf("first fallback cursor = %d, want 2", first.Cursor)
	}
	if second.Cursor != 3 {
		t.Fatalf("second fallback cursor = %d, want 3", second.Cursor)
	}
	if attempts := store.appendAttempts.Load(); attempts != 1 {
		t.Fatalf("append attempts = %d, want 1 after disabling store writes", attempts)
	}

	stats := feed.Stats()
	if stats.Count != 3 {
		t.Fatalf("stats.Count = %d, want 3", stats.Count)
	}
	if stats.OldestCursor != 1 {
		t.Fatalf("stats.OldestCursor = %d, want 1", stats.OldestCursor)
	}
	if stats.NewestCursor != 3 {
		t.Fatalf("stats.NewestCursor = %d, want 3", stats.NewestCursor)
	}

	events, latestCursor, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if latestCursor != 3 {
		t.Fatalf("latest cursor = %d, want 3", latestCursor)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 replayed events, got %d", len(events))
	}
	for i, want := range []int64{1, 2, 3} {
		if events[i].Cursor != want {
			t.Fatalf("events[%d].Cursor = %d, want %d", i, events[i].Cursor, want)
		}
	}
}

func TestAttentionFeed_ConcurrentAppendPreservesCursorOrder(t *testing.T) {

	store := newStubAttentionStore()
	store.blockFirstAppend()

	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	}, WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		feed.Append(AttentionEvent{
			Session:       "proj",
			Category:      EventCategoryAlert,
			Type:          EventTypeAlertWarning,
			Source:        "first",
			Actionability: ActionabilityActionRequired,
			Severity:      SeverityWarning,
			Summary:       "first append",
		})
	}()

	<-store.firstAppendStarted

	go func() {
		defer wg.Done()
		feed.Append(AttentionEvent{
			Session:       "proj",
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Source:        "second",
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       "second append",
		})
	}()

	close(store.releaseFirstAppend)
	wg.Wait()

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 replayed events, got %d", len(events))
	}
	if events[0].Cursor != 1 || events[1].Cursor != 2 {
		t.Fatalf("replay cursors = [%d %d], want [1 2]", events[0].Cursor, events[1].Cursor)
	}
}

func TestGetAttentionFeed_ReinitializesAfterSetNil(t *testing.T) {
	oldFeed := PeekAttentionFeed()
	SetAttentionFeed(nil)
	t.Cleanup(func() {
		SetAttentionFeed(oldFeed)
	})

	feed := GetAttentionFeed()
	if feed == nil {
		t.Fatal("GetAttentionFeed returned nil after SetAttentionFeed(nil)")
	}
}

func TestSetAttentionFeed_ReplacesInitializedDefault(t *testing.T) {
	oldFeed := PeekAttentionFeed()
	defaultFeed := GetAttentionFeed()
	customFeed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       8,
		RetentionPeriod:   time.Minute,
		HeartbeatInterval: 0,
	})
	t.Cleanup(func() {
		SetAttentionFeed(oldFeed)
		if customFeed != oldFeed && customFeed != defaultFeed {
			customFeed.Stop()
		}
	})

	SetAttentionFeed(customFeed)

	if got := GetAttentionFeed(); got != customFeed {
		t.Fatalf("GetAttentionFeed() = %p, want %p", got, customFeed)
	}
}

// newTestAttentionStore creates a real in-memory SQLite store for testing.
func newTestAttentionStore(t *testing.T) *state.Store {
	t.Helper()

	store, err := state.Open(":memory:")
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Apply migrations to create the attention_events table
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate test store: %v", err)
	}

	return store
}

func TestPersistNormalizedProjection_ReplacesRows(t *testing.T) {
	store := newTestAttentionStore(t)
	firstCollectedAt := time.Now().UTC()
	staleWindow := 10 * time.Minute
	firstTmuxSnapshot := &NormalizedSnapshot{
		Sessions: []state.RuntimeSession{
			{
				Name:         "ntm--proj",
				Label:        "proj",
				ProjectPath:  "/tmp/proj",
				Attached:     true,
				WindowCount:  1,
				PaneCount:    2,
				AgentCount:   2,
				ActiveAgents: 1,
				IdleAgents:   1,
				ErrorAgents:  0,
				HealthStatus: state.HealthStatusHealthy,
			},
		},
		Agents: []state.RuntimeAgent{
			{
				ID:             "ntm--proj:%1",
				SessionName:    "ntm--proj",
				Pane:           "%1",
				AgentType:      "claude",
				TypeConfidence: 0.95,
				TypeMethod:     "process",
				State:          state.AgentStateBusy,
				PendingMail:    2,
				AgentMailName:  "BlueLake",
				HealthStatus:   state.HealthStatusHealthy,
			},
			{
				ID:             "ntm--proj:%2",
				SessionName:    "ntm--proj",
				Pane:           "%2",
				AgentType:      "codex",
				TypeConfidence: 0.95,
				TypeMethod:     "process",
				State:          state.AgentStateIdle,
				HealthStatus:   state.HealthStatusHealthy,
			},
		},
	}

	first := &adapters.AggregatedSignals{
		CollectedAt: firstCollectedAt,
		Work: &adapters.WorkSection{
			Ready: []adapters.WorkItem{
				{
					ID:              "bd-1",
					Title:           "Top ready bead",
					TitleDisclosure: &adapters.DisclosureMetadata{DisclosureState: "visible"},
					Priority:        1,
					Type:            "task",
					Labels:          []string{"robot-redesign"},
					Unblocks:        2,
					Score:           float64Pointer(8.5),
				},
			},
			Blocked: []adapters.WorkItem{
				{ID: "bd-2", Title: "Blocked bead", Priority: 2, Type: "task", BlockedBy: []string{"bd-9"}},
			},
			InProgress: []adapters.WorkItem{
				{ID: "bd-3", Title: "In progress bead", Priority: 1, Type: "task", Assignee: "BlueLake", UpdatedAt: firstCollectedAt.Format(time.RFC3339)},
			},
			Triage: &adapters.WorkTriage{
				TopRecommendation: &adapters.WorkRecommendation{
					ID:       "bd-1",
					Title:    "Top ready bead",
					Priority: 1,
					Score:    8.5,
					Reasons:  []string{"high impact", "unblocks downstream"},
					Unblocks: 2,
				},
			},
		},
		Coordination: &adapters.CoordinationSection{
			Mail: &adapters.MailSummary{
				LatestMessage: firstCollectedAt.Format(time.RFC3339),
				ByAgent: map[string]adapters.AgentMailStats{
					"BlueLake": {
						Unread:                  2,
						Pending:                 1,
						Urgent:                  1,
						LatestMessage:           firstCollectedAt.Format(time.RFC3339),
						LatestSubject:           "Claim update",
						LatestSubjectDisclosure: &adapters.DisclosureMetadata{DisclosureState: "visible"},
						LatestPreview:           "Need restart token [REDACTED:generic-secret] before merge",
						LatestPreviewDisclosure: &adapters.DisclosureMetadata{DisclosureState: "redacted", RedactionMode: "detector", Findings: 1},
					},
					"RedHill": {
						Unread:                  1,
						LatestMessage:           firstCollectedAt.Add(-30 * time.Minute).Format(time.RFC3339),
						LatestSubject:           "Earlier coordination",
						LatestSubjectDisclosure: &adapters.DisclosureMetadata{DisclosureState: "visible"},
						LatestPreview:           "Earlier coordination",
						LatestPreviewDisclosure: &adapters.DisclosureMetadata{DisclosureState: "redact"},
					},
				},
			},
			Handoff: &adapters.HandoffSummary{
				Session:            "ntm--proj",
				Status:             "blocked",
				Goal:               "Ship [REDACTED:generic-secret] safely",
				GoalDisclosure:     &adapters.DisclosureMetadata{DisclosureState: "redacted", Findings: 1, Preview: "Ship [REDACTED:generic-secret] safely"},
				Now:                "Need safe preview before merge",
				NowDisclosure:      &adapters.DisclosureMetadata{DisclosureState: "visible", Preview: "Need safe preview before merge"},
				UpdatedAt:          firstCollectedAt.Format(time.RFC3339),
				ActiveBeads:        []string{"bd-3"},
				AgentMailThreads:   []string{"bd-j9jo3.3.5"},
				Blockers:           []string{"waiting on [REDACTED:generic-secret] confirmation"},
				BlockerDisclosures: []adapters.DisclosureMetadata{{DisclosureState: "redacted", Findings: 1, Preview: "waiting on [REDACTED:generic-secret] confirmation"}},
				Files:              []string{"internal/robot/robot.go"},
			},
		},
		Quota: &adapters.QuotaSection{
			Accounts: []adapters.AccountQuota{
				{
					ID:           "acct-1",
					Provider:     "anthropic",
					UsagePercent: func() *float64 { v := 82.5; return &v }(),
					TokensUsed:   10,
					TokensLimit:  100,
					Status:       "warning",
					ReasonCode:   adapters.ReasonQuotaWarningTokens,
					IsActive:     true,
				},
			},
		},
		Health: &adapters.SourceHealthSection{
			Sources: map[string]adapters.SourceInfo{
				"work_coordination": {
					Name:           "work_coordination",
					Available:      false,
					Fresh:          false,
					ReasonCode:     adapters.ReasonHealthSourceUnavailable,
					Degraded:       true,
					DegradedSince:  firstCollectedAt.Format(time.RFC3339),
					DegradedReason: "agent mail unavailable",
					LastError:      "agent mail unavailable",
				},
			},
			Degraded: []string{"work_coordination"},
			AllFresh: false,
		},
	}

	if err := persistNormalizedProjection(store, first, firstTmuxSnapshot, staleWindow); err != nil {
		t.Fatalf("persistNormalizedProjection(first) error: %v", err)
	}

	workRows, err := store.ListFreshRuntimeWork("", 0)
	if err != nil {
		t.Fatalf("ListFreshRuntimeWork() error: %v", err)
	}
	if len(workRows) != 3 {
		t.Fatalf("runtime work row count = %d, want 3", len(workRows))
	}
	workByID := make(map[string]state.RuntimeWork, len(workRows))
	for _, row := range workRows {
		workByID[row.BeadID] = row
	}
	if workByID["bd-1"].ScoreReason != "high impact; unblocks downstream" {
		t.Fatalf("bd-1 score reason = %q", workByID["bd-1"].ScoreReason)
	}
	var titleDisclosure adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(workByID["bd-1"].TitleDisclosure), &titleDisclosure); err != nil {
		t.Fatalf("unmarshal title disclosure: %v", err)
	}
	if titleDisclosure.DisclosureState != "visible" {
		t.Fatalf("title disclosure = %+v", titleDisclosure)
	}
	if workByID["bd-3"].Status != normalizedProjectionInProgStatus {
		t.Fatalf("bd-3 status = %q, want %q", workByID["bd-3"].Status, normalizedProjectionInProgStatus)
	}

	coordinationRows, err := store.ListFreshRuntimeCoordination("")
	if err != nil {
		t.Fatalf("ListFreshRuntimeCoordination() error: %v", err)
	}
	if len(coordinationRows) != 2 {
		t.Fatalf("unexpected coordination rows: %+v", coordinationRows)
	}
	coordinationByAgent := make(map[string]state.RuntimeCoordination, len(coordinationRows))
	for _, row := range coordinationRows {
		coordinationByAgent[row.AgentName] = row
	}
	blueCoordination, ok := coordinationByAgent["BlueLake"]
	if !ok {
		t.Fatalf("missing BlueLake coordination row: %+v", coordinationByAgent)
	}
	if blueCoordination.LastMessageSubject != "Claim update" {
		t.Fatalf("coordination subject = %q", blueCoordination.LastMessageSubject)
	}
	if blueCoordination.LastMessagePreview != "Need restart token [REDACTED:generic-secret] before merge" {
		t.Fatalf("coordination preview = %q", blueCoordination.LastMessagePreview)
	}
	expectedBlueTime := firstCollectedAt.UTC().Truncate(time.Second)
	if blueCoordination.LastMessageAt == nil || !blueCoordination.LastMessageAt.Equal(expectedBlueTime) {
		t.Fatalf("blue LastMessageAt = %v, want %v", blueCoordination.LastMessageAt, expectedBlueTime)
	}
	var subjectDisclosure adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(blueCoordination.LastMessageSubjectDisclosure), &subjectDisclosure); err != nil {
		t.Fatalf("unmarshal subject disclosure: %v", err)
	}
	if subjectDisclosure.DisclosureState != "visible" {
		t.Fatalf("subject disclosure = %+v", subjectDisclosure)
	}
	var previewDisclosure adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(blueCoordination.LastMessagePreviewDisclosure), &previewDisclosure); err != nil {
		t.Fatalf("unmarshal preview disclosure: %v", err)
	}
	if previewDisclosure.DisclosureState != "redacted" || previewDisclosure.Findings != 1 {
		t.Fatalf("preview disclosure = %+v", previewDisclosure)
	}
	redCoordination, ok := coordinationByAgent["RedHill"]
	if !ok {
		t.Fatalf("missing RedHill coordination row: %+v", coordinationByAgent)
	}
	expectedRedTime := firstCollectedAt.Add(-30 * time.Minute).UTC().Truncate(time.Second)
	if redCoordination.LastMessageAt == nil || !redCoordination.LastMessageAt.Equal(expectedRedTime) {
		t.Fatalf("red LastMessageAt = %v, want %v", redCoordination.LastMessageAt, expectedRedTime)
	}
	handoffRow, err := store.GetRuntimeHandoff()
	if err != nil {
		t.Fatalf("GetRuntimeHandoff(first) error: %v", err)
	}
	if handoffRow == nil || handoffRow.SessionName != "ntm--proj" || handoffRow.Status != "blocked" {
		t.Fatalf("unexpected handoff row: %+v", handoffRow)
	}
	var goalDisclosure adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(handoffRow.GoalDisclosure), &goalDisclosure); err != nil {
		t.Fatalf("unmarshal handoff goal disclosure: %v", err)
	}
	if goalDisclosure.DisclosureState != "redacted" || goalDisclosure.Findings != 1 {
		t.Fatalf("handoff goal disclosure = %+v", goalDisclosure)
	}
	var blockerDisclosures []adapters.DisclosureMetadata
	if err := json.Unmarshal([]byte(handoffRow.BlockerDisclosures), &blockerDisclosures); err != nil {
		t.Fatalf("unmarshal handoff blocker disclosures: %v", err)
	}
	if len(blockerDisclosures) != 1 || blockerDisclosures[0].DisclosureState != "redacted" {
		t.Fatalf("handoff blocker disclosures = %+v", blockerDisclosures)
	}

	sessionRows, err := store.GetFreshRuntimeSessions()
	if err != nil {
		t.Fatalf("GetFreshRuntimeSessions() error: %v", err)
	}
	if len(sessionRows) != 1 || sessionRows[0].Name != "ntm--proj" || sessionRows[0].AgentCount != 2 {
		t.Fatalf("unexpected session rows: %+v", sessionRows)
	}

	agentRows, err := store.GetRuntimeAgentsBySession("ntm--proj")
	if err != nil {
		t.Fatalf("GetRuntimeAgentsBySession(first) error: %v", err)
	}
	if len(agentRows) != 2 {
		t.Fatalf("runtime agent row count = %d, want 2", len(agentRows))
	}
	agentByID := make(map[string]state.RuntimeAgent, len(agentRows))
	for _, row := range agentRows {
		agentByID[row.ID] = row
	}
	if agentByID["ntm--proj:%1"].PendingMail != 2 || agentByID["ntm--proj:%1"].AgentMailName != "BlueLake" {
		t.Fatalf("unexpected first agent row: %+v", agentByID["ntm--proj:%1"])
	}

	quotaRows, err := store.ListFreshRuntimeQuota("")
	if err != nil {
		t.Fatalf("ListFreshRuntimeQuota() error: %v", err)
	}
	if len(quotaRows) != 1 || quotaRows[0].Provider != "anthropic" {
		t.Fatalf("unexpected quota rows: %+v", quotaRows)
	}
	if quotaRows[0].UsedPct != 82.5 || !quotaRows[0].UsedPctKnown || quotaRows[0].UsedPctSource != state.RuntimeQuotaUsedPctSourceProvider {
		t.Fatalf("unexpected quota provenance: %+v", quotaRows[0])
	}

	healthRows, err := store.GetAllSourceHealth()
	if err != nil {
		t.Fatalf("GetAllSourceHealth() error: %v", err)
	}
	if len(healthRows) != 1 || healthRows[0].SourceName != "work_coordination" {
		t.Fatalf("unexpected health rows: %+v", healthRows)
	}

	secondCollectedAt := firstCollectedAt.Add(2 * time.Minute)
	secondTmuxSnapshot := &NormalizedSnapshot{
		Sessions: []state.RuntimeSession{
			{
				Name:         "ntm--next",
				Label:        "next",
				ProjectPath:  "/tmp/next",
				Attached:     false,
				WindowCount:  1,
				PaneCount:    1,
				AgentCount:   1,
				ActiveAgents: 0,
				IdleAgents:   1,
				ErrorAgents:  0,
				HealthStatus: state.HealthStatusWarning,
				HealthReason: "one agent compacting",
			},
		},
		Agents: []state.RuntimeAgent{
			{
				ID:             "ntm--next:%4",
				SessionName:    "ntm--next",
				Pane:           "%4",
				AgentType:      "claude",
				TypeConfidence: 0.90,
				TypeMethod:     "title",
				State:          state.AgentStateCompacting,
				PendingMail:    1,
				AgentMailName:  "GreenStone",
				HealthStatus:   state.HealthStatusWarning,
				HealthReason:   "context compacting",
			},
		},
	}
	second := &adapters.AggregatedSignals{
		CollectedAt: secondCollectedAt,
		Work: &adapters.WorkSection{
			Ready: []adapters.WorkItem{
				{ID: "bd-4", Title: "Fresh replacement bead", Priority: 1, Type: "task"},
			},
		},
		Coordination: &adapters.CoordinationSection{},
		Health: &adapters.SourceHealthSection{
			Sources: map[string]adapters.SourceInfo{
				"mail": {
					Name:       "mail",
					Available:  true,
					Fresh:      true,
					ReasonCode: adapters.ReasonHealthOK,
				},
			},
			AllFresh: true,
		},
	}

	if err := persistNormalizedProjection(store, second, secondTmuxSnapshot, staleWindow); err != nil {
		t.Fatalf("persistNormalizedProjection(second) error: %v", err)
	}

	workRows, err = store.ListFreshRuntimeWork("", 0)
	if err != nil {
		t.Fatalf("ListFreshRuntimeWork(second) error: %v", err)
	}
	if len(workRows) != 1 || workRows[0].BeadID != "bd-4" {
		t.Fatalf("unexpected runtime work rows after replacement: %+v", workRows)
	}

	coordinationRows, err = store.ListFreshRuntimeCoordination("")
	if err != nil {
		t.Fatalf("ListFreshRuntimeCoordination(second) error: %v", err)
	}
	if len(coordinationRows) != 0 {
		t.Fatalf("expected coordination rows to be removed, got %+v", coordinationRows)
	}
	handoffRow, err = store.GetRuntimeHandoff()
	if err != nil {
		t.Fatalf("GetRuntimeHandoff(second) error: %v", err)
	}
	if handoffRow != nil {
		t.Fatalf("expected handoff row to be removed, got %+v", handoffRow)
	}

	sessionRows, err = store.GetFreshRuntimeSessions()
	if err != nil {
		t.Fatalf("GetFreshRuntimeSessions(second) error: %v", err)
	}
	if len(sessionRows) != 1 || sessionRows[0].Name != "ntm--next" || sessionRows[0].HealthReason != "one agent compacting" {
		t.Fatalf("unexpected session rows after replacement: %+v", sessionRows)
	}

	agentRows, err = store.GetRuntimeAgentsBySession("ntm--proj")
	if err != nil {
		t.Fatalf("GetRuntimeAgentsBySession(old session) error: %v", err)
	}
	if len(agentRows) != 0 {
		t.Fatalf("expected old session agents to cascade away, got %+v", agentRows)
	}

	agentRows, err = store.GetRuntimeAgentsBySession("ntm--next")
	if err != nil {
		t.Fatalf("GetRuntimeAgentsBySession(second) error: %v", err)
	}
	if len(agentRows) != 1 || agentRows[0].ID != "ntm--next:%4" || agentRows[0].PendingMail != 1 {
		t.Fatalf("unexpected runtime agent rows after replacement: %+v", agentRows)
	}

	quotaRows, err = store.ListFreshRuntimeQuota("")
	if err != nil {
		t.Fatalf("ListFreshRuntimeQuota(second) error: %v", err)
	}
	if len(quotaRows) != 0 {
		t.Fatalf("expected quota rows to be removed, got %+v", quotaRows)
	}

	healthRows, err = store.GetAllSourceHealth()
	if err != nil {
		t.Fatalf("GetAllSourceHealth(second) error: %v", err)
	}
	if len(healthRows) != 1 || healthRows[0].SourceName != "mail" || !healthRows[0].Healthy {
		t.Fatalf("unexpected health rows after replacement: %+v", healthRows)
	}
}

func TestPublishNormalizedAttentionSignals_DeduplicatesRefreshOutput(t *testing.T) {
	feed := newTestAttentionFeed(t)
	collectedAt := time.Date(2026, 3, 22, 19, 45, 0, 0, time.UTC)

	signals := &adapters.AggregatedSignals{
		CollectedAt: collectedAt,
		Work: &adapters.WorkSection{
			Triage: &adapters.WorkTriage{
				TopRecommendation: &adapters.WorkRecommendation{
					ID:       "bd-42",
					Title:    "Ship projection refresh",
					Priority: 1,
					Score:    9.1,
					Reasons:  []string{"high leverage"},
				},
			},
		},
		Coordination: &adapters.CoordinationSection{
			Problems: []adapters.CoordinationProblem{
				{
					Kind:     "reservation_conflict",
					Severity: "warning",
					Summary:  "internal/robot/*.go <-> internal/robot/attention_feed.go",
					Agents:   []string{"BlueLake", "GreenStone"},
					Paths:    []string{"internal/robot/attention_feed.go"},
				},
			},
		},
		Health: &adapters.SourceHealthSection{
			Sources: map[string]adapters.SourceInfo{
				"work_coordination": {
					Name:           "work_coordination",
					Available:      false,
					Fresh:          false,
					ReasonCode:     adapters.ReasonHealthSourceUnavailable,
					Degraded:       true,
					DegradedSince:  collectedAt.Format(time.RFC3339),
					DegradedReason: "agent mail unavailable",
					LastError:      "agent mail unavailable",
				},
			},
			Degraded: []string{"work_coordination"},
			AllFresh: false,
		},
	}

	publishNormalizedAttentionSignals(feed, nil, "ntm--proj", signals)
	publishNormalizedAttentionSignals(feed, nil, "ntm--proj", signals)

	events, _, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay() error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3 after dedup", len(events))
	}

	var (
		sawTopRecommendation bool
		sawHealthUnavailable bool
		sawReservation       bool
	)
	for _, event := range events {
		switch event.ReasonCode {
		case string(adapters.ReasonWorkReadyTopRecommendation):
			sawTopRecommendation = true
			if len(event.NextActions) == 0 || event.NextActions[0].Action != "robot-bead-show" {
				t.Fatalf("top recommendation next actions = %+v", event.NextActions)
			}
		case string(adapters.ReasonHealthSourceUnavailable):
			sawHealthUnavailable = true
			if event.Actionability != ActionabilityActionRequired {
				t.Fatalf("health actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
			}
		case string(adapters.ReasonCoordinationReservationConflict):
			sawReservation = true
			if event.Actionability != ActionabilityActionRequired {
				t.Fatalf("reservation conflict actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
			}
			var sawInspectCoordination bool
			for _, action := range event.NextActions {
				if action.Action == "robot-inspect-coordination" {
					sawInspectCoordination = true
				}
			}
			if !sawInspectCoordination {
				t.Fatalf("reservation conflict next actions = %+v, want robot-inspect-coordination guidance", event.NextActions)
			}
		}
	}

	if !sawTopRecommendation || !sawHealthUnavailable || !sawReservation {
		t.Fatalf("missing normalized attention events: top=%v health=%v reservation=%v events=%+v", sawTopRecommendation, sawHealthUnavailable, sawReservation, events)
	}
}

func float64Pointer(v float64) *float64 {
	return &v
}

func TestPublishMailPending_UsesRobotMailCheckAction(t *testing.T) {
	feed := newTestAttentionFeed(t)

	event := feed.PublishMailPending("/data/projects/ntm", "GreenStone", "BlueLake", "Need review", 42, "br-42")

	if len(event.NextActions) != 1 {
		t.Fatalf("NextActions len = %d, want 1", len(event.NextActions))
	}
	action := event.NextActions[0]
	if action.Action != "robot-mail-check" {
		t.Fatalf("action = %q, want %q", action.Action, "robot-mail-check")
	}
	if strings.Contains(action.Args, "robot-mail-read") || strings.Contains(action.Args, "robot-mail-ack") {
		t.Fatalf("args = %q, want canonical robot-mail-check command", action.Args)
	}
	for _, want := range []string{
		"--robot-mail-check",
		"--mail-project=/data/projects/ntm",
		"--mail-agent=BlueLake",
		"--thread=br-42",
		"--mail-status=unread",
	} {
		if !strings.Contains(action.Args, want) {
			t.Fatalf("args = %q, want substring %q", action.Args, want)
		}
	}
}

func TestPublishMailAckRequired_UsesRobotMailCheckAction(t *testing.T) {
	feed := newTestAttentionFeed(t)

	event := feed.PublishMailAckRequired("/data/projects/ntm", "GreenStone", "BlueLake", "Need ack", 77, "br-77")

	if len(event.NextActions) != 1 {
		t.Fatalf("NextActions len = %d, want 1", len(event.NextActions))
	}
	action := event.NextActions[0]
	if action.Action != "robot-mail-check" {
		t.Fatalf("action = %q, want %q", action.Action, "robot-mail-check")
	}
	if strings.Contains(action.Args, "robot-mail-read") || strings.Contains(action.Args, "robot-mail-ack") {
		t.Fatalf("args = %q, want canonical robot-mail-check command", action.Args)
	}
	for _, want := range []string{
		"--robot-mail-check",
		"--mail-project=/data/projects/ntm",
		"--mail-agent=BlueLake",
		"--thread=br-77",
	} {
		if !strings.Contains(action.Args, want) {
			t.Fatalf("args = %q, want substring %q", action.Args, want)
		}
	}
	if strings.Contains(action.Args, "--mail-status=unread") {
		t.Fatalf("args = %q, want ack-required follow-up to avoid unread-only filtering", action.Args)
	}
}

type stubAttentionStore struct {
	mu                 sync.Mutex
	events             []state.StoredAttentionEvent
	seq                int64 // Next cursor comes after this, like AUTOINCREMENT.
	appendAttempts     atomic.Int32
	failAppend         atomic.Bool
	failReserve        atomic.Bool
	blockFirst         atomic.Bool
	firstAppendStarted chan struct{}
	releaseFirstAppend chan struct{}
}

func newStubAttentionStore() *stubAttentionStore {
	return &stubAttentionStore{
		firstAppendStarted: make(chan struct{}),
		releaseFirstAppend: make(chan struct{}),
	}
}

func (s *stubAttentionStore) seed(summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	cursor := s.seq
	s.events = append(s.events, state.StoredAttentionEvent{
		Cursor:        cursor,
		Ts:            time.Now().UTC(),
		SessionName:   "proj",
		Category:      string(EventCategorySystem),
		EventType:     string(EventTypeSystemHealthChange),
		Source:        "seed",
		Actionability: state.ActionabilityBackground,
		Severity:      state.SeverityInfo,
		Summary:       summary,
		DedupCount:    1,
	})
}

func (s *stubAttentionStore) blockFirstAppend() {
	s.blockFirst.Store(true)
}

func (s *stubAttentionStore) AppendAttentionEvent(event *state.StoredAttentionEvent) (int64, error) {
	attempt := s.appendAttempts.Add(1)
	if s.failAppend.Load() {
		return 0, fmt.Errorf("append failed")
	}
	if s.blockFirst.Load() && attempt == 1 {
		close(s.firstAppendStarted)
		<-s.releaseFirstAppend
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	cursor := s.seq
	stored := *event
	stored.Cursor = cursor
	s.events = append(s.events, stored)
	return cursor, nil
}

func (s *stubAttentionStore) ReserveAttentionCursor(minCursor int64) error {
	if s.failReserve.Load() {
		return errors.New("reserve failed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if minCursor > s.seq {
		s.seq = minCursor
	}
	return nil
}

func (s *stubAttentionStore) GetAttentionEventsSince(sinceCursor int64, limit int) ([]state.StoredAttentionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}

	events := make([]state.StoredAttentionEvent, 0, len(s.events))
	for _, event := range s.events {
		if event.Cursor <= sinceCursor {
			continue
		}
		events = append(events, event)
		if len(events) == limit {
			break
		}
	}
	return events, nil
}

func (s *stubAttentionStore) AppendAddressedAttentionEvent(event *state.StoredAttentionEvent) (int64, bool, error) {
	if existing, err := s.GetAddressedAttentionEvent(event.RecipientProject, event.RecipientAgent, *event.MessageID); err != nil || existing != nil {
		if existing == nil {
			return 0, false, err
		}
		return existing.Cursor, false, nil
	}
	cursor, err := s.AppendAttentionEvent(event)
	return cursor, err == nil, err
}

func (s *stubAttentionStore) GetAddressedAttentionEvent(project, agent string, messageID int64) (*state.StoredAttentionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event.MessageID != nil && *event.MessageID == messageID &&
			event.RecipientProject == project && strings.EqualFold(event.RecipientAgent, agent) {
			found := event
			return &found, nil
		}
	}
	return nil, nil
}

func (s *stubAttentionStore) GetAttentionEventsForRecipient(agent string, sinceCursor int64, limit int) ([]state.StoredAttentionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 100
	}
	events := []state.StoredAttentionEvent{}
	for _, event := range s.events {
		if event.Cursor <= sinceCursor || !strings.EqualFold(event.RecipientAgent, agent) {
			continue
		}
		events = append(events, event)
		if len(events) == limit {
			break
		}
	}
	return events, nil
}

func (s *stubAttentionStore) GetLatestAttentionEventsInTimeRange(start, end time.Time, limit int) ([]state.StoredAttentionEvent, error) {
	events, err := s.GetAttentionEventsInTimeRange(start, end, math.MaxInt)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func (s *stubAttentionStore) GetAttentionEventsInTimeRange(start, end time.Time, limit int) ([]state.StoredAttentionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if end.Before(start) {
		return []state.StoredAttentionEvent{}, nil
	}
	if limit <= 0 {
		limit = 100
	}

	events := make([]state.StoredAttentionEvent, 0, len(s.events))
	for _, event := range s.events {
		if event.Ts.Before(start) || event.Ts.After(end) {
			continue
		}
		events = append(events, event)
		if len(events) == limit {
			break
		}
	}
	return events, nil
}

func (s *stubAttentionStore) GetAttentionItemStatesForCursors(cursors []int64) (map[int64]state.AttentionItemState, error) {
	return map[int64]state.AttentionItemState{}, nil
}

func (s *stubAttentionStore) GetAttentionReplayWindow() (state.AttentionReplayWindow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	window := state.AttentionReplayWindow{
		EventCount: len(s.events),
	}
	if len(s.events) > 0 {
		window.OldestCursor = s.events[0].Cursor
		window.NewestCursor = s.events[len(s.events)-1].Cursor
	}
	return window, nil
}

func (s *stubAttentionStore) GetLatestEventCursor() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.events) == 0 {
		return 0, nil
	}
	return s.events[len(s.events)-1].Cursor, nil
}

func (s *stubAttentionStore) GetEventsForIncident(incidentID string, limit int) ([]state.StoredAttentionEvent, error) {
	return []state.StoredAttentionEvent{}, nil
}

func (s *stubAttentionStore) GetIncident(id string) (*state.Incident, error) {
	return nil, nil
}

func (s *stubAttentionStore) GCExpiredEvents() (int64, error) {
	return 0, nil
}

// =============================================================================
// Event Builder Tests
// =============================================================================

func TestNewTrackerEvent_MailReceived(t *testing.T) {
	change := tracker.StateChange{
		Timestamp: time.Date(2026, 3, 21, 3, 9, 0, 0, time.UTC),
		Type:      tracker.ChangeMailReceived,
		Session:   "proj",
		Pane:      "1",
		Details: map[string]interface{}{
			"subject": "Need ack",
		},
	}

	event := NewTrackerEvent(change)
	if event.Category != EventCategoryMail {
		t.Fatalf("tracker mail category = %q, want %q", event.Category, EventCategoryMail)
	}
	if event.Type != EventTypeMailReceived {
		t.Fatalf("tracker mail type = %q, want %q", event.Type, EventTypeMailReceived)
	}
	if event.Pane != 1 {
		t.Fatalf("tracker mail pane = %d, want 1", event.Pane)
	}
}

func TestNewLoggedAttentionEvent_SessionCreate(t *testing.T) {
	event, ok := NewLoggedAttentionEvent(ntmevents.Event{
		Timestamp: time.Date(2026, 3, 21, 3, 10, 0, 0, time.UTC),
		Type:      ntmevents.EventSessionCreate,
		Session:   "proj",
	})
	if !ok {
		t.Fatal("session_create should map into the attention feed")
	}
	if event.Category != EventCategorySession {
		t.Fatalf("logged event category = %q, want %q", event.Category, EventCategorySession)
	}
	if event.Type != EventTypeSessionCreated {
		t.Fatalf("logged event type = %q, want %q", event.Type, EventTypeSessionCreated)
	}
	if len(event.NextActions) != 1 || event.NextActions[0].Action != "robot-status" {
		t.Fatalf("logged event next actions = %+v, want robot-status follow-up", event.NextActions)
	}
}

func TestNewBusAttentionEvent_AlertCritical(t *testing.T) {
	event, ok := NewBusAttentionEvent(ntmevents.NewAlertEvent("proj", "alert-3", "health", "critical", "disk full"))
	if !ok {
		t.Fatal("alert bus event should map into the attention feed")
	}
	if event.Category != EventCategoryAlert {
		t.Fatalf("bus alert category = %q, want %q", event.Category, EventCategoryAlert)
	}
	if event.Type != EventTypeAlertAttentionRequired {
		t.Fatalf("bus alert type = %q, want %q", event.Type, EventTypeAlertAttentionRequired)
	}
	if event.Severity != SeverityCritical {
		t.Fatalf("bus alert severity = %q, want %q", event.Severity, SeverityCritical)
	}
	if event.Actionability != ActionabilityActionRequired {
		t.Fatalf("bus alert actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
	}
}

func TestAttentionSignalAnnotations_Table(t *testing.T) {
	timeline := time.Date(2026, 3, 21, 3, 20, 0, 0, time.UTC)

	tests := []struct {
		name               string
		event              AttentionEvent
		wantSignal         string
		wantReasonContains string
		wantMetadataKey    string
		wantActionability  Actionability
	}{
		{
			name: "session changed",
			event: mustLoggedAttentionEvent(t, ntmevents.Event{
				Timestamp: timeline,
				Type:      ntmevents.EventSessionCreate,
				Session:   "proj",
			}),
			wantSignal:         attentionSignalSessionChanged,
			wantReasonContains: "session lifecycle",
			wantActionability:  ActionabilityInteresting,
		},
		{
			name: "pane changed",
			event: NewTrackerEvent(tracker.StateChange{
				Timestamp: timeline,
				Type:      tracker.ChangePaneCreated,
				Session:   "proj",
				Pane:      "4",
			}),
			wantSignal:         attentionSignalPaneChanged,
			wantReasonContains: "pane lifecycle",
			wantActionability:  ActionabilityInteresting,
		},
		{
			name:               "agent state changed",
			event:              NewAgentStateChangeEvent("proj", 2, "cc-2", "working", "idle", "activity_tracker"),
			wantSignal:         attentionSignalAgentStateChanged,
			wantReasonContains: "agent lifecycle",
			wantActionability:  ActionabilityInteresting,
		},
		{
			name:               "stalled",
			event:              mustBusAttentionEvent(t, ntmevents.NewAgentStallEvent("proj", "cod-1", 45, "waiting")),
			wantSignal:         attentionSignalStalled,
			wantReasonContains: "stall heuristic",
			wantMetadataKey:    "signal_threshold_seconds",
			wantActionability:  ActionabilityActionRequired,
		},
		{
			name:               "context hot",
			event:              mustBusAttentionEvent(t, ntmevents.NewContextWarningEvent("proj", "cc-1", 92.5, 1200)),
			wantSignal:         attentionSignalContextHot,
			wantReasonContains: "90%",
			wantMetadataKey:    "signal_threshold_percent",
			wantActionability:  ActionabilityActionRequired,
		},
		{
			name:               "rate limited",
			event:              mustBusAttentionEvent(t, ntmevents.NewWebhookEvent(ntmevents.WebhookAgentRateLimit, "proj", "2", "cc-1", "429 Too Many Requests", nil)),
			wantSignal:         attentionSignalRateLimited,
			wantReasonContains: "rate-limit",
			wantMetadataKey:    "signal_threshold_rationale",
			wantActionability:  ActionabilityActionRequired,
		},
		{
			name:               "alert raised",
			event:              mustBusAttentionEvent(t, ntmevents.NewAlertEvent("proj", "alert-4", "health", "warning", "disk warm")),
			wantSignal:         attentionSignalAlertRaised,
			wantReasonContains: "alert emitted",
			wantActionability:  ActionabilityActionRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			signal, _ := tt.event.Details["signal"].(string)
			reason, _ := tt.event.Details["signal_reason"].(string)
			t.Logf("signal=%q reason=%q summary=%q actionability=%q details=%v", signal, reason, tt.event.Summary, tt.event.Actionability, tt.event.Details)

			if signal != tt.wantSignal {
				t.Fatalf("signal = %q, want %q", signal, tt.wantSignal)
			}
			if !strings.Contains(reason, tt.wantReasonContains) {
				t.Fatalf("signal_reason = %q, want substring %q", reason, tt.wantReasonContains)
			}
			if tt.wantMetadataKey != "" {
				if _, ok := tt.event.Details[tt.wantMetadataKey]; !ok {
					t.Fatalf("missing metadata key %q in details=%v", tt.wantMetadataKey, tt.event.Details)
				}
			}
			if tt.wantActionability != "" && tt.event.Actionability != tt.wantActionability {
				t.Fatalf("actionability = %q, want %q", tt.event.Actionability, tt.wantActionability)
			}
		})
	}
}

func TestAttentionSignal_NormalizesLifecycleActionabilityAndActions(t *testing.T) {
	tests := []struct {
		name       string
		event      AttentionEvent
		wantSignal string
	}{
		{
			name: "session created from tracker",
			event: NewTrackerEvent(tracker.StateChange{
				Timestamp: time.Date(2026, 3, 21, 3, 23, 0, 0, time.UTC),
				Type:      tracker.ChangeSessionCreated,
				Session:   "proj",
			}),
			wantSignal: attentionSignalSessionChanged,
		},
		{
			name: "pane created from tracker",
			event: NewTrackerEvent(tracker.StateChange{
				Timestamp: time.Date(2026, 3, 21, 3, 23, 30, 0, time.UTC),
				Type:      tracker.ChangePaneCreated,
				Session:   "proj",
				Pane:      "7",
			}),
			wantSignal: attentionSignalPaneChanged,
		},
		{
			name:       "agent state change from helper",
			event:      NewAgentStateChangeEvent("proj", 3, "cod-3", "idle", "working", "activity_tracker"),
			wantSignal: attentionSignalAgentStateChanged,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Details["signal"]; got != tt.wantSignal {
				t.Fatalf("signal = %#v, want %q", got, tt.wantSignal)
			}
			if tt.event.Actionability != ActionabilityInteresting {
				t.Fatalf("actionability = %q, want %q", tt.event.Actionability, ActionabilityInteresting)
			}
			if tt.event.Severity != SeverityInfo {
				t.Fatalf("severity = %q, want %q", tt.event.Severity, SeverityInfo)
			}
			if len(tt.event.NextActions) == 0 {
				t.Error("NextActions must suggest follow-up commands")
			}
			if tt.event.NextActions[0].Action != "robot-status" {
				t.Fatalf("next action = %q, want robot-status", tt.event.NextActions[0].Action)
			}
		})
	}
}

func TestAttentionSignal_ContextHotThresholdBoundary(t *testing.T) {
	tests := []struct {
		name              string
		usagePercent      float64
		wantActionability Actionability
	}{
		{
			name:              "below threshold stays interesting",
			usagePercent:      89.9,
			wantActionability: ActionabilityInteresting,
		},
		{
			name:              "at threshold becomes action required",
			usagePercent:      90.0,
			wantActionability: ActionabilityActionRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := mustBusAttentionEvent(t, ntmevents.NewContextWarningEvent("proj", "cc-1", tt.usagePercent, 1200))
			t.Logf("usage=%.1f signal=%v reason=%v actionability=%q next_actions=%v", tt.usagePercent, event.Details["signal"], event.Details["signal_reason"], event.Actionability, event.NextActions)

			if event.Details["signal"] != attentionSignalContextHot {
				t.Fatalf("signal = %#v, want %q", event.Details["signal"], attentionSignalContextHot)
			}
			if event.Actionability != tt.wantActionability {
				t.Fatalf("actionability = %q, want %q", event.Actionability, tt.wantActionability)
			}
			if len(event.NextActions) != 1 || event.NextActions[0].Action != "robot-context" {
				t.Fatalf("next_actions = %v, want single robot-context action", event.NextActions)
			}
		})
	}
}

func TestAttentionSignal_DoesNotPromotePaneOutput(t *testing.T) {
	event := NewTrackerEvent(tracker.StateChange{
		Timestamp: time.Date(2026, 3, 21, 3, 21, 0, 0, time.UTC),
		Type:      tracker.ChangeAgentOutput,
		Session:   "proj",
		Pane:      "5",
		Details: map[string]interface{}{
			"line_count": 12,
		},
	})
	t.Logf("pane output summary=%q actionability=%q details=%v", event.Summary, event.Actionability, event.Details)

	if _, ok := event.Details["signal"]; ok {
		t.Fatalf("pane output should not derive a first-class signal: %v", event.Details)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Fatalf("pane output actionability = %q, want %q", event.Actionability, ActionabilityInteresting)
	}
}

func TestAttentionSignal_RateLimitedFromLoggedError(t *testing.T) {
	event := mustLoggedAttentionEvent(t, ntmevents.Event{
		Timestamp: time.Date(2026, 3, 21, 3, 22, 0, 0, time.UTC),
		Type:      ntmevents.EventError,
		Session:   "proj",
		Data: map[string]interface{}{
			"error_type": "rate_limit",
			"message":    "429 Too Many Requests",
		},
	})
	t.Logf("logged error signal=%v details=%v", event.Details["signal"], event.Details)

	if event.Details["signal"] != attentionSignalRateLimited {
		t.Fatalf("logged error signal = %#v, want %q", event.Details["signal"], attentionSignalRateLimited)
	}
	if event.Actionability != ActionabilityActionRequired {
		t.Fatalf("logged error actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
	}
}

func TestAttentionSignal_AlertInfoPreservesSeverity(t *testing.T) {
	event := mustBusAttentionEvent(t, ntmevents.NewAlertEvent("proj", "alert-5", "health", "info", "background sync complete"))
	t.Logf("alert info signal=%v severity=%q actionability=%q next_actions=%v", event.Details["signal"], event.Severity, event.Actionability, event.NextActions)

	if event.Details["signal"] != attentionSignalAlertRaised {
		t.Fatalf("signal = %#v, want %q", event.Details["signal"], attentionSignalAlertRaised)
	}
	if event.Severity != SeverityInfo {
		t.Fatalf("severity = %q, want %q", event.Severity, SeverityInfo)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Fatalf("actionability = %q, want %q", event.Actionability, ActionabilityInteresting)
	}
	if len(event.NextActions) != 1 || event.NextActions[0].Action != "robot-status" {
		t.Fatalf("next_actions = %v, want single robot-status action", event.NextActions)
	}
}

func TestNewAgentStateChangeEvent(t *testing.T) {
	event := NewAgentStateChangeEvent("myproject", 2, "cc_1", "generating", "idle", "activity_tracker")

	if event.Session != "myproject" {
		t.Errorf("Session = %q, want %q", event.Session, "myproject")
	}
	if event.Pane != 2 {
		t.Errorf("Pane = %d, want 2", event.Pane)
	}
	if event.Category != EventCategoryAgent {
		t.Errorf("Category = %q, want %q", event.Category, EventCategoryAgent)
	}
	if event.Type != EventTypeAgentStateChange {
		t.Errorf("Type = %q, want %q", event.Type, EventTypeAgentStateChange)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Errorf("Actionability = %q, want %q (idle state)", event.Actionability, ActionabilityInteresting)
	}

	// Check details
	if event.Details["agent_id"] != "cc_1" {
		t.Errorf("details.agent_id = %v, want cc_1", event.Details["agent_id"])
	}
}

func TestNewFileConflictEvent(t *testing.T) {
	event := NewFileConflictEvent("myproject", "internal/robot/types.go", []string{"cc_1", "cc_2"})

	if event.Actionability != ActionabilityActionRequired {
		t.Errorf("Actionability = %q, want %q", event.Actionability, ActionabilityActionRequired)
	}
	if event.Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", event.Severity, SeverityError)
	}
	if len(event.NextActions) == 0 {
		t.Error("NextActions should not be empty for file conflicts")
	}
}

func TestBuildAttentionDigest_CoalescesPaneOutputBursts(t *testing.T) {
	events := []AttentionEvent{
		digestTestEvent(1, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 1"),
		digestTestEvent(2, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 2"),
		digestTestEvent(3, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 3"),
		digestTestEvent(4, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, "agent became idle"),
	}

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	digest := BuildAttentionDigest(events, 0, 4, opts)
	t.Logf("digest summary=%q suppressed=%+v trace=%+v", digest.Summary, digest.Suppressed, digest.Trace)

	if len(digest.Buckets.Interesting) != 2 {
		t.Fatalf("interesting bucket len = %d, want 2", len(digest.Buckets.Interesting))
	}

	var burst *AttentionDigestItem
	for i := range digest.Buckets.Interesting {
		if digest.Buckets.Interesting[i].Event.Type == EventTypePaneOutput {
			burst = &digest.Buckets.Interesting[i]
			break
		}
	}
	if burst == nil {
		t.Fatal("expected pane output burst item in interesting bucket")
	}
	if burst.SourceEventCount != 3 {
		t.Fatalf("burst.SourceEventCount = %d, want 3", burst.SourceEventCount)
	}
	if burst.SuppressedCount != 2 {
		t.Fatalf("burst.SuppressedCount = %d, want 2", burst.SuppressedCount)
	}
	if burst.SuppressionReason != attentionDigestSuppressionPaneOutputBurst {
		t.Fatalf("burst.SuppressionReason = %q, want %q", burst.SuppressionReason, attentionDigestSuppressionPaneOutputBurst)
	}
	if burst.Event.Summary != "3 output updates in proj pane 2" {
		t.Fatalf("burst summary = %q, want %q", burst.Event.Summary, "3 output updates in proj pane 2")
	}
	if digest.Suppressed.Total != 2 {
		t.Fatalf("suppressed total = %d, want 2", digest.Suppressed.Total)
	}
	if digest.Suppressed.ByReason[attentionDigestSuppressionPaneOutputBurst] != 2 {
		t.Fatalf("pane output suppression count = %d, want 2", digest.Suppressed.ByReason[attentionDigestSuppressionPaneOutputBurst])
	}

	hasCoalesced := false
	hasSuppressed := false
	for _, decision := range digest.Trace {
		if decision.Decision == "coalesced" {
			hasCoalesced = true
		}
		if decision.Decision == "suppressed" && decision.Reason == attentionDigestSuppressionPaneOutputBurst {
			hasSuppressed = true
		}
	}
	if !hasCoalesced || !hasSuppressed {
		t.Fatalf("trace missing coalesced/suppressed pane output decisions: %+v", digest.Trace)
	}
}

func TestBuildAttentionDigest_SuppressesLifecycleNoiseAndDuplicateAlerts(t *testing.T) {
	attached := digestTestEvent(1, EventCategorySession, EventTypeSessionAttached, ActionabilityInteresting, SeverityInfo, "session attached")
	attached.Pane = 0
	delete(attached.Details, "pane_ref")

	resized := digestTestEvent(2, EventCategoryPane, EventTypePaneResized, ActionabilityBackground, SeverityInfo, "pane resized")
	dupAlert1 := digestTestEvent(3, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 85%")
	dupAlert2 := digestTestEvent(4, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 85%")
	background := digestTestEvent(5, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, "checkpoint created")
	background.Pane = 0
	delete(background.Details, "pane_ref")

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	digest := BuildAttentionDigest([]AttentionEvent{attached, resized, dupAlert1, dupAlert2, background}, 0, 5, opts)
	t.Logf("digest summary=%q suppressed=%+v trace=%+v", digest.Summary, digest.Suppressed, digest.Trace)

	if len(digest.Buckets.ActionRequired) != 1 {
		t.Fatalf("action_required bucket len = %d, want 1", len(digest.Buckets.ActionRequired))
	}
	alert := digest.Buckets.ActionRequired[0]
	if alert.SourceEventCount != 2 || alert.SuppressedCount != 1 {
		t.Fatalf("duplicate alert item = %+v, want source=2 suppressed=1", alert)
	}
	if alert.SuppressionReason != attentionDigestSuppressionDuplicateAlert {
		t.Fatalf("duplicate alert reason = %q, want %q", alert.SuppressionReason, attentionDigestSuppressionDuplicateAlert)
	}
	if len(digest.Buckets.Background) != 1 {
		t.Fatalf("background bucket len = %d, want 1", len(digest.Buckets.Background))
	}
	if digest.Suppressed.Total != 3 {
		t.Fatalf("suppressed total = %d, want 3", digest.Suppressed.Total)
	}
	if digest.Suppressed.ByReason[attentionDigestSuppressionLifecycleNoise] != 2 {
		t.Fatalf("lifecycle suppression count = %d, want 2", digest.Suppressed.ByReason[attentionDigestSuppressionLifecycleNoise])
	}
	if digest.Suppressed.ByReason[attentionDigestSuppressionDuplicateAlert] != 1 {
		t.Fatalf("duplicate alert suppression count = %d, want 1", digest.Suppressed.ByReason[attentionDigestSuppressionDuplicateAlert])
	}
}

func TestBuildAttentionDigest_PrefersRecurringResurfacedItemsWhenBucketLimited(t *testing.T) {
	recurring := digestTestEvent(1, EventCategoryAlert, EventTypeAlertWarning, ActionabilityInteresting, SeverityInfo, "quota still high")
	recurring.Details[attentionDetailResurfaced] = true
	recurring.Details[attentionDetailResurfaceReason] = attentionResurfaceReasonCooldownExpired
	recurring.Details[attentionDetailCooldownSuppressedCount] = 3
	recurring.Details[attentionDetailCooldownWindowMS] = (2 * time.Minute).Milliseconds()

	oneOff := digestTestEvent(2, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, "agent became idle")

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 1
	opts.BackgroundLimit = 5

	digest := BuildAttentionDigest([]AttentionEvent{recurring, oneOff}, 0, 2, opts)
	if len(digest.Buckets.Interesting) != 1 {
		t.Fatalf("interesting bucket len = %d, want 1", len(digest.Buckets.Interesting))
	}
	if got := digest.Buckets.Interesting[0].Event.Summary; got != recurring.Summary {
		t.Fatalf("top interesting summary = %q, want %q", got, recurring.Summary)
	}
}

func TestAttentionFeed_DigestPreservesCursorBoundariesAndImportantSignals(t *testing.T) {
	feed := newTestAttentionFeed(t)

	feed.Append(digestTestEvent(0, EventCategorySystem, EventType(DefaultTransportLiveness.HeartbeatType), ActionabilityBackground, SeverityDebug, "Heartbeat"))
	feed.Append(digestTestEvent(0, EventCategoryPane, EventTypePaneOutput, ActionabilityBackground, SeverityInfo, "output update 1"))
	feed.Append(digestTestEvent(0, EventCategoryPane, EventTypePaneOutput, ActionabilityBackground, SeverityInfo, "output update 2"))
	feed.Append(digestTestEvent(0, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 92%"))
	feed.Append(digestTestEvent(0, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, "agent became idle"))

	opts := DefaultAttentionDigestOptions()
	opts.IncludeTrace = true
	opts.MinSeverity = SeverityDebug
	opts.ActionRequiredLimit = 1
	opts.InterestingLimit = 1
	opts.BackgroundLimit = 1

	digest, err := feed.Digest(0, opts)
	if err != nil {
		t.Fatalf("Digest error: %v", err)
	}
	t.Logf("digest summary=%q cursor_start=%d cursor_end=%d suppressed=%+v trace=%+v", digest.Summary, digest.CursorStart, digest.CursorEnd, digest.Suppressed, digest.Trace)

	if digest.CursorStart != 1 {
		t.Fatalf("CursorStart = %d, want 1", digest.CursorStart)
	}
	if digest.CursorEnd != 5 {
		t.Fatalf("CursorEnd = %d, want 5", digest.CursorEnd)
	}
	if len(digest.Buckets.ActionRequired) != 1 {
		t.Fatalf("action_required bucket len = %d, want 1", len(digest.Buckets.ActionRequired))
	}
	if digest.Buckets.ActionRequired[0].Event.Summary != "quota at 92%" {
		t.Fatalf("action_required summary = %q, want %q", digest.Buckets.ActionRequired[0].Event.Summary, "quota at 92%")
	}
	if !strings.HasPrefix(digest.Summary, "quota at 92%") {
		t.Fatalf("summary = %q, want prefix %q", digest.Summary, "quota at 92%")
	}
	if digest.Suppressed.Total < 2 {
		t.Fatalf("suppressed total = %d, want at least 2", digest.Suppressed.Total)
	}
}

func TestBuildAttentionDigest_SummaryStableAndBucketAssignment(t *testing.T) {
	heartbeat := digestTestEvent(1, EventCategorySystem, EventType(DefaultTransportLiveness.HeartbeatType), ActionabilityBackground, SeverityDebug, "Heartbeat")
	heartbeat.Pane = 0
	delete(heartbeat.Details, "pane_ref")

	alert := digestTestEvent(2, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 92%")
	bead := digestTestEvent(3, EventCategoryBead, EventTypeBeadUpdated, ActionabilityInteresting, SeverityInfo, "bead ready")
	output1 := digestTestEvent(4, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 1")
	output2 := digestTestEvent(5, EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update 2")
	background := digestTestEvent(6, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, "checkpoint created")
	background.Pane = 0
	delete(background.Details, "pane_ref")

	opts := DefaultAttentionDigestOptions()
	opts.MinSeverity = SeverityDebug
	opts.ActionRequiredLimit = 15
	opts.InterestingLimit = 15
	opts.BackgroundLimit = 15

	digest := BuildAttentionDigest([]AttentionEvent{heartbeat, alert, bead, output1, output2, background}, 0, 6, opts)
	t.Logf("digest summary=%q buckets=%+v suppressed=%+v", digest.Summary, digest.Buckets, digest.Suppressed)

	if digest.CursorStart != 1 {
		t.Fatalf("CursorStart = %d, want 1", digest.CursorStart)
	}
	if digest.CursorEnd != 6 {
		t.Fatalf("CursorEnd = %d, want 6", digest.CursorEnd)
	}
	if digest.PeriodStart != heartbeat.Ts {
		t.Fatalf("PeriodStart = %q, want %q", digest.PeriodStart, heartbeat.Ts)
	}
	if digest.PeriodEnd != background.Ts {
		t.Fatalf("PeriodEnd = %q, want %q", digest.PeriodEnd, background.Ts)
	}
	if len(digest.Buckets.ActionRequired) != 1 || len(digest.Buckets.Interesting) != 2 || len(digest.Buckets.Background) != 1 {
		t.Fatalf("bucket sizes = action_required:%d interesting:%d background:%d, want 1/2/1",
			len(digest.Buckets.ActionRequired), len(digest.Buckets.Interesting), len(digest.Buckets.Background))
	}

	wantSummary := "quota at 92%; 1 action_required, 2 interesting, 1 background; 2 suppressed from 6 events"
	if digest.Summary != wantSummary {
		t.Fatalf("summary = %q, want %q", digest.Summary, wantSummary)
	}
}

func TestBuildAttentionDigest_PrioritizedQueueMergesBuckets(t *testing.T) {
	// Create events across all three actionability levels
	action1 := digestTestEvent(1, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityError, "critical alert 1")
	action2 := digestTestEvent(2, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "critical alert 2")
	interesting1 := digestTestEvent(3, EventCategoryBead, EventTypeBeadUpdated, ActionabilityInteresting, SeverityInfo, "bead ready")
	interesting2 := digestTestEvent(4, EventCategoryAgent, EventTypeAgentStateChange, ActionabilityInteresting, SeverityInfo, "agent idle")
	background1 := digestTestEvent(5, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityInfo, "health check")
	background2 := digestTestEvent(6, EventCategorySystem, EventTypeSystemHealthChange, ActionabilityBackground, SeverityDebug, "metric update")

	opts := DefaultAttentionDigestOptions()
	opts.ActionRequiredLimit = 15
	opts.InterestingLimit = 15
	opts.BackgroundLimit = 15
	opts.MinSeverity = SeverityDebug

	digest := BuildAttentionDigest([]AttentionEvent{action1, action2, interesting1, interesting2, background1, background2}, 0, 6, opts)

	// Verify buckets are populated as expected
	if len(digest.Buckets.ActionRequired) != 2 {
		t.Fatalf("ActionRequired = %d, want 2", len(digest.Buckets.ActionRequired))
	}
	if len(digest.Buckets.Interesting) != 2 {
		t.Fatalf("Interesting = %d, want 2", len(digest.Buckets.Interesting))
	}
	if len(digest.Buckets.Background) != 2 {
		t.Fatalf("Background = %d, want 2", len(digest.Buckets.Background))
	}

	// Verify PrioritizedQueue merges all buckets in order
	if len(digest.PrioritizedQueue) != 6 {
		t.Fatalf("PrioritizedQueue = %d, want 6", len(digest.PrioritizedQueue))
	}

	// First two should be action_required
	if digest.PrioritizedQueue[0].Event.Actionability != ActionabilityActionRequired {
		t.Errorf("PrioritizedQueue[0] actionability = %s, want action_required", digest.PrioritizedQueue[0].Event.Actionability)
	}
	if digest.PrioritizedQueue[1].Event.Actionability != ActionabilityActionRequired {
		t.Errorf("PrioritizedQueue[1] actionability = %s, want action_required", digest.PrioritizedQueue[1].Event.Actionability)
	}

	// Next two should be interesting
	if digest.PrioritizedQueue[2].Event.Actionability != ActionabilityInteresting {
		t.Errorf("PrioritizedQueue[2] actionability = %s, want interesting", digest.PrioritizedQueue[2].Event.Actionability)
	}
	if digest.PrioritizedQueue[3].Event.Actionability != ActionabilityInteresting {
		t.Errorf("PrioritizedQueue[3] actionability = %s, want interesting", digest.PrioritizedQueue[3].Event.Actionability)
	}

	// Last two should be background
	if digest.PrioritizedQueue[4].Event.Actionability != ActionabilityBackground {
		t.Errorf("PrioritizedQueue[4] actionability = %s, want background", digest.PrioritizedQueue[4].Event.Actionability)
	}
	if digest.PrioritizedQueue[5].Event.Actionability != ActionabilityBackground {
		t.Errorf("PrioritizedQueue[5] actionability = %s, want background", digest.PrioritizedQueue[5].Event.Actionability)
	}
}

func TestBuildAttentionDigest_PrioritizedQueueCapsAtLimit(t *testing.T) {
	// Create more events than the cap (10)
	events := make([]AttentionEvent, 15)
	for i := 0; i < 15; i++ {
		events[i] = digestTestEvent(int64(i+1), EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, fmt.Sprintf("alert %d", i+2))
	}

	opts := DefaultAttentionDigestOptions()
	opts.ActionRequiredLimit = 15 // Allow all in bucket

	digest := BuildAttentionDigest(events, 0, 15, opts)

	// Buckets should have all 15
	if len(digest.Buckets.ActionRequired) != 15 {
		t.Fatalf("ActionRequired = %d, want 15", len(digest.Buckets.ActionRequired))
	}

	// PrioritizedQueue should be capped at 10
	if len(digest.PrioritizedQueue) != 10 {
		t.Fatalf("PrioritizedQueue = %d, want 10 (capped)", len(digest.PrioritizedQueue))
	}
}

func TestBuildAttentionDigest_PrioritizedQueueEmptyWhenNoBuckets(t *testing.T) {
	opts := DefaultAttentionDigestOptions()
	digest := BuildAttentionDigest([]AttentionEvent{}, 0, 0, opts)

	if digest.PrioritizedQueue != nil {
		t.Fatalf("PrioritizedQueue = %v, want nil for empty digest", digest.PrioritizedQueue)
	}
}

// =============================================================================
// JSON Serialization Tests
// =============================================================================

func TestAttentionEvent_JSONSerialization(t *testing.T) {
	event := AttentionEvent{
		Cursor:        42,
		Ts:            "2026-03-20T10:00:00Z",
		Session:       "myproject",
		Pane:          2,
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "test",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Test event",
		Details: map[string]any{
			"key": "value",
		},
		NextActions: []NextAction{
			{Action: "robot-tail", Args: "--robot-tail=myproject --panes=2 --lines=50", Reason: "Check output"},
		},
	}

	// Serialize
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	// Deserialize
	var decoded AttentionEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	// Verify fields
	if decoded.Cursor != 42 {
		t.Errorf("Cursor = %d, want 42", decoded.Cursor)
	}
	if decoded.Session != "myproject" {
		t.Errorf("Session = %q, want %q", decoded.Session, "myproject")
	}
	if decoded.Category != EventCategoryAgent {
		t.Errorf("Category = %q, want %q", decoded.Category, EventCategoryAgent)
	}
}

func TestCursorExpiredDetails_JSONSerialization(t *testing.T) {
	details := CursorExpiredDetails{
		RequestedCursor: 42,
		EarliestCursor:  100,
		RetentionPeriod: "1h",
		ResyncCommand:   "ntm --robot-snapshot",
	}

	data, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var decoded CursorExpiredDetails
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.RequestedCursor != 42 {
		t.Errorf("RequestedCursor = %d, want 42", decoded.RequestedCursor)
	}
	if decoded.ResyncCommand != "ntm --robot-snapshot" {
		t.Errorf("ResyncCommand = %q, want %q", decoded.ResyncCommand, "ntm --robot-snapshot")
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCursorAllocator_Next(b *testing.B) {
	alloc := NewCursorAllocator()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		alloc.Next()
	}
}

func BenchmarkCursorAllocator_NextParallel(b *testing.B) {
	alloc := NewCursorAllocator()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			alloc.Next()
		}
	})
}

func BenchmarkAttentionJournal_Append(b *testing.B) {
	journal := NewAttentionJournal(10000, time.Hour)
	event := AttentionEvent{Summary: "Benchmark event"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event.Cursor = int64(i + 1)
		journal.Append(event)
	}
}

func BenchmarkAttentionJournal_Replay(b *testing.B) {
	journal := NewAttentionJournal(10000, time.Hour)
	// Pre-fill journal
	for i := int64(1); i <= 1000; i++ {
		journal.Append(AttentionEvent{Cursor: i, Summary: "Event"})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		journal.Replay(500, 100)
	}
}

func BenchmarkAttentionFeed_Append(b *testing.B) {
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:       100000,
		RetentionPeriod:   time.Hour,
		HeartbeatInterval: 0,
	})
	defer feed.Stop()

	event := AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Source:        "test",
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Benchmark event",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		feed.Append(event)
	}
}

// =============================================================================
// Conflict Event Tests (br-vdfjr)
// =============================================================================

func TestNewBusAttentionEvent_ReservationConflict(t *testing.T) {

	event := ntmevents.NewReservationConflictEvent(
		"myproject", "internal/auth/handler.go", "BlueLake", "cc_1",
		[]string{"GreenCastle", "RedMountain"},
	)

	att, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatal("expected ReservationConflictEvent to normalize")
	}

	if att.Category != EventCategoryFile {
		t.Errorf("Category = %q, want %q", att.Category, EventCategoryFile)
	}
	if att.Type != EventTypeFileConflict {
		t.Errorf("Type = %q, want %q", att.Type, EventTypeFileConflict)
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Errorf("Actionability = %q, want %q", att.Actionability, ActionabilityActionRequired)
	}
	if att.Severity != SeverityWarning {
		t.Errorf("Severity = %q, want %q", att.Severity, SeverityWarning)
	}
	if !strings.Contains(att.Summary, "reservation conflict") {
		t.Errorf("Summary should contain 'reservation conflict', got %q", att.Summary)
	}
	if !strings.Contains(att.Summary, "internal/auth/handler.go") {
		t.Errorf("Summary should contain file path, got %q", att.Summary)
	}
	if att.Details == nil {
		t.Fatal("Details must not be nil")
	}
	if att.Details["conflict_kind"] != "reservation" {
		t.Errorf("conflict_kind = %v, want 'reservation'", att.Details["conflict_kind"])
	}
	if att.Details["path"] != "internal/auth/handler.go" {
		t.Errorf("path = %v, want 'internal/auth/handler.go'", att.Details["path"])
	}
	if len(att.NextActions) == 0 {
		t.Error("NextActions must suggest follow-up commands")
	}
	foundInspectCoordination := false
	for _, action := range att.NextActions {
		if strings.Contains(action.Args, "--session=") {
			t.Errorf("NextAction args should not use stale --session form: %q", action.Args)
		}
		if strings.Contains(action.Args, "--file=") {
			t.Errorf("NextAction args should not use unsupported --file form: %q", action.Args)
		}
		if action.Action == "robot-locks" {
			t.Errorf("NextActions should not reference nonexistent robot-locks action: %+v", action)
		}
		if action.Action == "robot-inspect-coordination" {
			foundInspectCoordination = true
			if action.Args != "--robot-inspect-coordination=BlueLake" {
				t.Errorf("inspect-coordination args = %q, want %q", action.Args, "--robot-inspect-coordination=BlueLake")
			}
		}
	}
	if !foundInspectCoordination {
		t.Error("NextActions should include robot-inspect-coordination guidance")
	}
}

func TestNewBusAttentionEvent_FileConflict(t *testing.T) {

	event := ntmevents.NewFileConflictEvent(
		"myproject", "cmd/main.go",
		[]string{"BlueLake", "GreenCastle"},
	)

	att, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatal("expected FileConflictEvent to normalize")
	}

	if att.Category != EventCategoryFile {
		t.Errorf("Category = %q, want %q", att.Category, EventCategoryFile)
	}
	if att.Type != EventTypeFileConflict {
		t.Errorf("Type = %q, want %q", att.Type, EventTypeFileConflict)
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Errorf("Actionability = %q, want %q", att.Actionability, ActionabilityActionRequired)
	}
	if !strings.Contains(att.Summary, "file conflict") {
		t.Errorf("Summary should contain 'file conflict', got %q", att.Summary)
	}
	if !strings.Contains(att.Summary, "cmd/main.go") {
		t.Errorf("Summary should contain file path, got %q", att.Summary)
	}
	if att.Details == nil {
		t.Fatal("Details must not be nil")
	}
	if att.Details["conflict_kind"] != "file" {
		t.Errorf("conflict_kind = %v, want 'file'", att.Details["conflict_kind"])
	}
	if len(att.NextActions) == 0 {
		t.Error("NextActions must suggest follow-up commands")
	}
	foundDiff := false
	for _, action := range att.NextActions {
		if strings.Contains(action.Args, "--session=") {
			t.Errorf("NextAction args should not use stale --session form: %q", action.Args)
		}
		if strings.Contains(action.Args, "--file=") {
			t.Errorf("NextAction args should not use unsupported --file form: %q", action.Args)
		}
		if action.Action == "robot-diff" {
			foundDiff = true
			if action.Args != "--robot-diff=myproject" {
				t.Errorf("robot-diff args = %q, want %q", action.Args, "--robot-diff=myproject")
			}
		}
	}
	if !foundDiff {
		t.Error("NextActions should include robot-diff guidance")
	}
}

func TestNewReservationConflictEvent_UsesCanonicalNextActions(t *testing.T) {

	conflict := watcher.FileConflict{
		Path:           "internal/auth/handler.go",
		RequestorAgent: "BlueLake",
		RequestorPane:  "cc_1",
		SessionName:    "myproject",
		Holders:        []string{"GreenCastle", "RedMountain"},
		DetectedAt:     time.Date(2026, 3, 24, 5, 0, 0, 0, time.UTC),
	}

	att, ok := NewReservationConflictEvent(conflict)
	if !ok {
		t.Fatal("expected watcher reservation conflict to normalize")
	}

	foundInspectCoordination := false
	for _, action := range att.NextActions {
		if strings.Contains(action.Args, "--session=") {
			t.Errorf("NextAction args should not use stale --session form: %q", action.Args)
		}
		if strings.Contains(action.Args, "--file=") {
			t.Errorf("NextAction args should not use unsupported --file form: %q", action.Args)
		}
		if action.Action == "robot-locks" {
			t.Errorf("NextActions should not reference nonexistent robot-locks action: %+v", action)
		}
		if action.Action == "robot-inspect-coordination" {
			foundInspectCoordination = true
			if action.Args != "--robot-inspect-coordination=BlueLake" {
				t.Errorf("inspect-coordination args = %q, want %q", action.Args, "--robot-inspect-coordination=BlueLake")
			}
		}
	}
	if !foundInspectCoordination {
		t.Error("NextActions should include robot-inspect-coordination guidance")
	}
}

func TestConflictEvents_FeedToJournal(t *testing.T) {

	feed := newTestAttentionFeed(t)

	// Publish a reservation conflict
	reservationEvt := ntmevents.NewReservationConflictEvent(
		"proj", "src/auth.go", "Agent1", "cc_1", []string{"Agent2"},
	)
	published, ok := feed.PublishBusEvent(reservationEvt)
	if !ok {
		t.Fatal("reservation conflict should be publishable")
	}
	if published.Cursor == 0 {
		t.Error("published event should have a cursor")
	}

	// Publish a file conflict
	fileEvt := ntmevents.NewFileConflictEvent("proj", "src/main.go", []string{"A", "B"})
	published2, ok := feed.PublishBusEvent(fileEvt)
	if !ok {
		t.Fatal("file conflict should be publishable")
	}
	if published2.Cursor <= published.Cursor {
		t.Errorf("cursor should be monotonically increasing: %d <= %d", published2.Cursor, published.Cursor)
	}

	// Verify replay returns both
	events, _, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("Replay error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events in journal, got %d", len(events))
	}
}

func TestNewBusAttentionEvent_RotationStarted(t *testing.T) {

	att := mustBusAttentionEvent(t, ntmevents.NewRotationStartedEvent("proj", "cod-1", 91.0, "architect"))

	if att.Type != EventTypeAgentCompacted {
		t.Fatalf("Type = %q, want %q", att.Type, EventTypeAgentCompacted)
	}
	if att.Severity != SeverityInfo {
		t.Fatalf("Severity = %q, want %q", att.Severity, SeverityInfo)
	}
	if !strings.Contains(att.Summary, "rotation started for cod-1") {
		t.Fatalf("Summary = %q, want started agent id", att.Summary)
	}
}

func TestNewBusAttentionEvent_RotationFailedFallsBackToNewAgent(t *testing.T) {

	att := mustBusAttentionEvent(t, ntmevents.NewRotationCompletedEvent("proj", "", "cod-2", 0, false, "handoff timeout"))

	if att.Type != EventTypeAgentError {
		t.Fatalf("Type = %q, want %q", att.Type, EventTypeAgentError)
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Fatalf("Actionability = %q, want %q", att.Actionability, ActionabilityActionRequired)
	}
	if att.Severity != SeverityError {
		t.Fatalf("Severity = %q, want %q", att.Severity, SeverityError)
	}
	if !strings.Contains(att.Summary, "rotation failed for cod-2") {
		t.Fatalf("Summary = %q, want fallback agent id", att.Summary)
	}
}

func TestConflictEvents_PartialObservability(t *testing.T) {

	// Reservation conflict with empty holders — should still work
	event := ntmevents.NewReservationConflictEvent(
		"proj", "file.go", "Agent1", "cc_1", nil,
	)
	att, ok := NewBusAttentionEvent(event)
	if !ok {
		t.Fatal("reservation conflict with nil holders should normalize")
	}
	if att.Actionability != ActionabilityActionRequired {
		t.Errorf("even with no holders, actionability should be action_required")
	}

	// File conflict with single agent (edge case)
	event2 := ntmevents.NewFileConflictEvent("proj", "f.go", []string{"OnlyAgent"})
	att2, ok := NewBusAttentionEvent(event2)
	if !ok {
		t.Fatal("file conflict with one agent should normalize")
	}
	if att2.Details["conflict_kind"] != "file" {
		t.Errorf("conflict_kind should be 'file'")
	}
}

func BenchmarkBuildAttentionDigest_Burst(b *testing.B) {
	events := make([]AttentionEvent, 0, 4096)
	for i := 0; i < 4000; i++ {
		event := digestTestEvent(int64(i+1), EventCategoryPane, EventTypePaneOutput, ActionabilityInteresting, SeverityInfo, "output update")
		events = append(events, event)
	}
	events = append(events, digestTestEvent(4001, EventCategoryAlert, EventTypeAlertWarning, ActionabilityActionRequired, SeverityWarning, "quota at 92%"))

	opts := DefaultAttentionDigestOptions()
	opts.ActionRequiredLimit = 5
	opts.InterestingLimit = 5
	opts.BackgroundLimit = 5

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildAttentionDigest(events, 0, 4001, opts)
	}
}

// =============================================================================
// EventsOptions / filterEventsForRobot Tests (br-kpvhy)
// =============================================================================

func TestFilterEventsForRobot_NoFilters(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityInteresting, Severity: SeverityInfo},
		{Cursor: 2, Session: "other", Category: EventCategoryAlert, Actionability: ActionabilityActionRequired, Severity: SeverityWarning},
	}

	opts := EventsOptions{} // No filters
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 events with no filters, got %d", len(result))
	}
}

func TestFilterEventsForRobot_SessionFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane},
		{Cursor: 2, Session: "other", Category: EventCategoryAlert},
		{Cursor: 3, Session: "proj", Category: EventCategorySystem},
	}

	opts := EventsOptions{Session: "proj"}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 events for session 'proj', got %d", len(result))
	}
	for _, ev := range result {
		if ev.Session != "proj" {
			t.Errorf("expected session 'proj', got %q", ev.Session)
		}
	}
}

func TestFilterEventsForRobot_CategoryFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane},
		{Cursor: 2, Session: "proj", Category: EventCategoryAlert},
		{Cursor: 3, Session: "other", Category: EventCategoryPane},
		{Cursor: 4, Session: "proj", Category: EventCategoryPane},
	}

	opts := EventsOptions{CategoryFilter: []string{string(EventCategoryPane)}}
	result := filterEventsForRobot(events, opts)

	if len(result) != 3 {
		t.Errorf("expected 3 pane events, got %d", len(result))
	}
	for _, ev := range result {
		if ev.Category != EventCategoryPane {
			t.Errorf("expected category 'pane', got %q", ev.Category)
		}
	}
}

func TestFilterEventsForRobot_ActionabilityFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityActionRequired},
		{Cursor: 2, Session: "proj", Category: EventCategoryAlert, Actionability: ActionabilityActionRequired},
		{Cursor: 3, Session: "other", Category: EventCategoryPane, Actionability: ActionabilityActionRequired},
		{Cursor: 4, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityActionRequired},
	}

	opts := EventsOptions{
		Session:             "proj",
		CategoryFilter:      []string{string(EventCategoryPane)},
		ActionabilityFilter: []string{string(ActionabilityActionRequired)},
	}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 action_required events, got %d", len(result))
	}
	for _, ev := range result {
		if ev.Actionability != ActionabilityActionRequired {
			t.Errorf("expected actionability 'action_required', got %q", ev.Actionability)
		}
	}
}

func TestFilterEventsForRobot_SeverityFilter(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityActionRequired, Severity: SeverityInfo},
		{Cursor: 2, Session: "proj", Category: EventCategoryAlert, Actionability: ActionabilityActionRequired, Severity: SeverityWarning},
		{Cursor: 3, Session: "other", Category: EventCategoryPane, Actionability: ActionabilityActionRequired, Severity: SeverityInfo},
		{Cursor: 4, Session: "proj", Category: EventCategoryPane, Actionability: ActionabilityActionRequired, Severity: SeverityWarning},
	}

	opts := EventsOptions{
		Session:             "proj",
		CategoryFilter:      []string{string(EventCategoryPane)},
		ActionabilityFilter: []string{string(ActionabilityActionRequired)},
		SeverityFilter:      []string{string(SeverityWarning), string(SeverityError)},
	}
	result := filterEventsForRobot(events, opts)

	if len(result) != 1 {
		t.Errorf("expected 1 event matching all filters, got %d", len(result))
	}
	if len(result) > 0 {
		if result[0].Cursor != 4 {
			t.Errorf("expected cursor 4, got %d", result[0].Cursor)
		}
	}
}

func TestFilterEventsForRobot_MultipleCategoryValues(t *testing.T) {
	events := []AttentionEvent{
		{Cursor: 1, Session: "proj", Category: EventCategoryPane},
		{Cursor: 2, Session: "proj", Category: EventCategoryAlert},
		{Cursor: 3, Session: "proj", Category: EventCategorySystem},
	}

	opts := EventsOptions{CategoryFilter: []string{string(EventCategoryPane), string(EventCategoryAlert)}}
	result := filterEventsForRobot(events, opts)

	if len(result) != 2 {
		t.Errorf("expected 2 events (pane or alert), got %d", len(result))
	}
}

func TestToStringSetForEvents_Empty(t *testing.T) {
	result := toStringSetForEvents(nil)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}

	result = toStringSetForEvents([]string{})
	if result != nil {
		t.Errorf("expected nil for empty slice, got %v", result)
	}
}

func TestToStringSetForEvents_Values(t *testing.T) {
	input := []string{"a", "b", "c"}
	result := toStringSetForEvents(input)

	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}
	for _, v := range input {
		if !result[v] {
			t.Errorf("expected %q in set", v)
		}
	}
}

func TestEventsOptions_Fields(t *testing.T) {
	// Verify the EventsOptions struct has expected fields
	opts := EventsOptions{
		SinceCursor:         100,
		Limit:               50,
		Session:             "test",
		CategoryFilter:      []string{"pane"},
		ActionabilityFilter: []string{"action_required"},
		SeverityFilter:      []string{"warning"},
	}

	if opts.SinceCursor != 100 {
		t.Errorf("expected SinceCursor 100, got %d", opts.SinceCursor)
	}
	if opts.Limit != 50 {
		t.Errorf("expected Limit 50, got %d", opts.Limit)
	}
	if opts.Session != "test" {
		t.Errorf("expected Session 'test', got %q", opts.Session)
	}
}

// =============================================================================
// DigestOptions / DigestResponse Tests (br-6tzh9)
// =============================================================================

func TestDigestOptions_Fields(t *testing.T) {

	opts := DigestOptions{
		SinceCursor:         100,
		Session:             "test",
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
		IncludeTrace:        true,
	}

	if opts.SinceCursor != 100 {
		t.Errorf("expected SinceCursor 100, got %d", opts.SinceCursor)
	}
	if opts.Session != "test" {
		t.Errorf("expected Session 'test', got %q", opts.Session)
	}
	if opts.ActionRequiredLimit != 5 {
		t.Errorf("expected ActionRequiredLimit 5, got %d", opts.ActionRequiredLimit)
	}
	if opts.InterestingLimit != 4 {
		t.Errorf("expected InterestingLimit 4, got %d", opts.InterestingLimit)
	}
	if opts.BackgroundLimit != 3 {
		t.Errorf("expected BackgroundLimit 3, got %d", opts.BackgroundLimit)
	}
	if !opts.IncludeTrace {
		t.Error("expected IncludeTrace to be true")
	}
}

func TestDigestResponse_EmptyFeed(t *testing.T) {

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	opts := DigestOptions{
		SinceCursor: 0,
	}

	// Build digest directly using feed.Digest
	digestOpts := AttentionDigestOptions{
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
	digest, err := feed.Digest(opts.SinceCursor, digestOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest.EventCount != 0 {
		t.Errorf("expected 0 events in empty feed, got %d", digest.EventCount)
	}
	if digest.CursorStart != 0 {
		t.Errorf("expected CursorStart 0, got %d", digest.CursorStart)
	}
	if digest.CursorEnd != 0 {
		t.Errorf("expected CursorEnd 0, got %d", digest.CursorEnd)
	}
	if len(digest.Buckets.ActionRequired) != 0 {
		t.Errorf("expected 0 action_required items, got %d", len(digest.Buckets.ActionRequired))
	}
	if len(digest.Buckets.Interesting) != 0 {
		t.Errorf("expected 0 interesting items, got %d", len(digest.Buckets.Interesting))
	}
}

func TestDigestResponse_WithEvents(t *testing.T) {

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	// Add some events
	now := time.Now()
	events := []AttentionEvent{
		{
			Ts:            now.Add(-3 * time.Minute).Format(time.RFC3339),
			Session:       "proj",
			Category:      EventCategoryPane,
			Type:          EventTypePaneOutput,
			Actionability: ActionabilityBackground,
			Severity:      SeverityInfo,
			Summary:       "Background output",
		},
		{
			Ts:            now.Add(-2 * time.Minute).Format(time.RFC3339),
			Session:       "proj",
			Pane:          1,
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentError,
			Actionability: ActionabilityActionRequired,
			Severity:      SeverityError,
			Summary:       "Agent error detected",
		},
		{
			Ts:            now.Add(-1 * time.Minute).Format(time.RFC3339),
			Session:       "proj",
			Pane:          2,
			Category:      EventCategoryAgent,
			Type:          EventTypeAgentStateChange,
			Actionability: ActionabilityInteresting,
			Severity:      SeverityInfo,
			Summary:       "Agent waiting for prompt",
		},
	}
	for _, ev := range events {
		feed.Append(ev)
	}

	// Build digest
	digestOpts := AttentionDigestOptions{
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
	digest, err := feed.Digest(0, digestOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if digest.EventCount != 3 {
		t.Errorf("expected 3 events, got %d", digest.EventCount)
	}
	if digest.CursorStart < 1 {
		t.Errorf("expected CursorStart >= 1, got %d", digest.CursorStart)
	}
	if digest.CursorEnd < digest.CursorStart {
		t.Errorf("expected CursorEnd >= CursorStart, got start=%d end=%d",
			digest.CursorStart, digest.CursorEnd)
	}

	// Verify buckets contain expected items
	if len(digest.Buckets.ActionRequired) == 0 {
		t.Error("expected at least one action_required item")
	}
	if len(digest.Buckets.Interesting) == 0 {
		t.Error("expected at least one interesting item")
	}
}

func TestDigestResponse_CursorChaining(t *testing.T) {

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	now := time.Now()

	// First batch
	feed.Append(AttentionEvent{
		Ts:            now.Add(-10 * time.Minute).Format(time.RFC3339),
		Session:       "proj",
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "First batch event",
	})

	digestOpts := AttentionDigestOptions{
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}

	// First digest
	digest1, err := feed.Digest(0, digestOpts)
	if err != nil {
		t.Fatalf("first digest error: %v", err)
	}
	if digest1.EventCount != 1 {
		t.Errorf("expected 1 event in first digest, got %d", digest1.EventCount)
	}

	// Add second batch after first digest
	feed.Append(AttentionEvent{
		Ts:            now.Add(-5 * time.Minute).Format(time.RFC3339),
		Session:       "proj",
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "Second batch event",
	})

	// Second digest from cursor_end of first
	digest2, err := feed.Digest(digest1.CursorEnd, digestOpts)
	if err != nil {
		t.Fatalf("second digest error: %v", err)
	}

	// Second digest should only have the new event
	if digest2.EventCount != 1 {
		t.Errorf("expected 1 event in second digest (cursor chaining), got %d", digest2.EventCount)
	}
	if digest2.CursorStart <= digest1.CursorEnd {
		t.Logf("cursor chaining: first.CursorEnd=%d second.CursorStart=%d",
			digest1.CursorEnd, digest2.CursorStart)
	}
}

func TestDigestResponse_SessionFilter(t *testing.T) {

	feed := newTestAttentionFeed(t)
	SetAttentionFeed(feed)
	defer SetAttentionFeed(nil)

	now := time.Now()

	// Add events from different sessions
	feed.Append(AttentionEvent{
		Ts:            now.Add(-2 * time.Minute).Format(time.RFC3339),
		Session:       "proj-a",
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "Event from proj-a",
	})
	feed.Append(AttentionEvent{
		Ts:            now.Add(-1 * time.Minute).Format(time.RFC3339),
		Session:       "proj-b",
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "Event from proj-b",
	})

	// Digest with session filter
	digestOpts := AttentionDigestOptions{
		Session:             "proj-a",
		ActionRequiredLimit: 5,
		InterestingLimit:    4,
		BackgroundLimit:     3,
	}
	digest, err := feed.Digest(0, digestOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should only include events from proj-a
	if digest.EventCount != 1 {
		t.Errorf("expected 1 event for proj-a, got %d", digest.EventCount)
	}
}

// =============================================================================
// AttentionOptions / AttentionResponse Tests (br-t540i)
// =============================================================================

func TestAttentionOptions_Fields(t *testing.T) {
	// Verify AttentionOptions has all expected fields
	opts := AttentionOptions{
		SinceCursor:         100,
		Session:             "test-session",
		Timeout:             30 * time.Second,
		PollInterval:        500 * time.Millisecond,
		Condition:           "action_required",
		ActionRequiredLimit: 10,
		InterestingLimit:    5,
		BackgroundLimit:     2,
		IncludeTrace:        true,
	}

	if opts.SinceCursor != 100 {
		t.Errorf("SinceCursor: expected 100, got %d", opts.SinceCursor)
	}
	if opts.Session != "test-session" {
		t.Errorf("Session: expected test-session, got %s", opts.Session)
	}
	if opts.Timeout != 30*time.Second {
		t.Errorf("Timeout: expected 30s, got %v", opts.Timeout)
	}
	if opts.PollInterval != 500*time.Millisecond {
		t.Errorf("PollInterval: expected 500ms, got %v", opts.PollInterval)
	}
	if opts.Condition != "action_required" {
		t.Errorf("Condition: expected action_required, got %s", opts.Condition)
	}
}

func TestAttentionResponse_Fields(t *testing.T) {
	// Verify AttentionResponse has all expected fields
	resp := AttentionResponse{
		RobotResponse: RobotResponse{
			Success:      true,
			Timestamp:    "2026-03-21T00:00:00Z",
			Version:      "1.0.0",
			OutputFormat: "json",
		},
		WakeReason:       "attention",
		MatchedCondition: "action_required",
		TriggerEvent: &AttentionEvent{
			Cursor:        42,
			Type:          EventTypeMailReceived,
			Category:      EventCategoryMail,
			Actionability: ActionabilityActionRequired,
		},
		WaitedSeconds: 2.5,
		Digest: &AttentionDigest{
			CursorStart: 10,
			CursorEnd:   42,
			EventCount:  5,
		},
		CursorInfo: AttentionCursorInfo{
			StartCursor:  10,
			EndCursor:    42,
			OldestCursor: 1,
			NextCommand:  "ntm --robot-attention --attention-cursor=42",
		},
	}

	if resp.WakeReason != "attention" {
		t.Errorf("WakeReason: expected attention, got %s", resp.WakeReason)
	}
	if resp.MatchedCondition != "action_required" {
		t.Errorf("MatchedCondition: expected action_required, got %s", resp.MatchedCondition)
	}
	if resp.WaitedSeconds != 2.5 {
		t.Errorf("WaitedSeconds: expected 2.5, got %f", resp.WaitedSeconds)
	}
	if resp.CursorInfo.EndCursor != 42 {
		t.Errorf("CursorInfo.EndCursor: expected 42, got %d", resp.CursorInfo.EndCursor)
	}
	if resp.Digest.EventCount != 5 {
		t.Errorf("Digest.EventCount: expected 5, got %d", resp.Digest.EventCount)
	}
}

func TestAttentionCursorInfo_NextCommand(t *testing.T) {
	// Verify NextCommand format is correct for cursor chaining
	info := AttentionCursorInfo{
		StartCursor:  10,
		EndCursor:    50,
		OldestCursor: 1,
		NextCommand:  "ntm --robot-attention --attention-cursor=50 --attention-session=proj",
	}

	if info.NextCommand == "" {
		t.Error("NextCommand should not be empty")
	}
	if info.EndCursor <= info.StartCursor {
		t.Error("EndCursor should be greater than StartCursor in normal flow")
	}
}

func TestAttentionResponse_TimeoutWake(t *testing.T) {
	// Verify timeout response has correct wake reason
	resp := AttentionResponse{
		RobotResponse: RobotResponse{Success: false},
		WakeReason:    "timeout",
		WaitedSeconds: 300.0,
		Digest:        &AttentionDigest{EventCount: 0},
		CursorInfo: AttentionCursorInfo{
			StartCursor:  50,
			EndCursor:    50,
			OldestCursor: 50,
		},
	}

	if resp.WakeReason != "timeout" {
		t.Errorf("WakeReason: expected timeout, got %s", resp.WakeReason)
	}
	if resp.RobotResponse.Success {
		t.Error("Success should be false on timeout")
	}
	if resp.CursorInfo.OldestCursor != 50 {
		t.Errorf("OldestCursor should be 50, got %d", resp.CursorInfo.OldestCursor)
	}
}

func TestAttentionResponse_CursorExpired(t *testing.T) {
	// Verify cursor expired response has correct structure
	resp := AttentionResponse{
		RobotResponse: NewErrorResponse(
			&CursorExpiredError{RequestedCursor: 5, EarliestCursor: 100},
			ErrCodeCursorExpired,
			"ntm --robot-snapshot",
		),
		WakeReason: "cursor_expired",
		Digest:     &AttentionDigest{},
		CursorInfo: AttentionCursorInfo{
			StartCursor:  5,
			OldestCursor: 100,
			NextCommand:  "ntm --robot-snapshot",
		},
	}

	if resp.WakeReason != "cursor_expired" {
		t.Errorf("WakeReason: expected cursor_expired, got %s", resp.WakeReason)
	}
	if resp.RobotResponse.Success {
		t.Error("Success should be false on cursor expiry")
	}
	if resp.CursorInfo.OldestCursor != 100 {
		t.Errorf("OldestCursor should be 100, got %d", resp.CursorInfo.OldestCursor)
	}
}

func TestPrintDigest_ProfileFiltersBackgroundNoise(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySession,
		Type:          EventTypeSessionCreated,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "session created",
	})
	actionRequired := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "operator action required",
	})

	output, err := captureStdout(t, func() error {
		return PrintDigest(DigestOptions{
			SinceCursor:         0,
			Session:             "proj",
			Profile:             "operator",
			ActionRequiredLimit: 5,
			InterestingLimit:    4,
			BackgroundLimit:     3,
		})
	})
	if err != nil {
		t.Fatalf("PrintDigest returned error: %v", err)
	}

	var resp DigestResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("failed to decode digest response: %v\noutput=%s", err, output)
	}
	if !resp.Success {
		t.Fatalf("expected successful digest response, got %#v", resp)
	}
	if resp.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1 after operator profile filtering", resp.EventCount)
	}
	if len(resp.Buckets.ActionRequired) != 1 {
		t.Fatalf("ActionRequired bucket len = %d, want 1", len(resp.Buckets.ActionRequired))
	}
	if len(resp.Buckets.Background) != 0 {
		t.Fatalf("Background bucket len = %d, want 0 after filtering", len(resp.Buckets.Background))
	}
	if resp.Buckets.ActionRequired[0].Event.Cursor != actionRequired.Cursor {
		t.Fatalf("action bucket cursor = %d, want %d", resp.Buckets.ActionRequired[0].Event.Cursor, actionRequired.Cursor)
	}
}

func TestPrintAttention_ProfileShapesDigestAndNextCommand(t *testing.T) {
	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySession,
		Type:          EventTypeSessionCreated,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "session created",
	})
	actionRequired := feed.Append(AttentionEvent{
		Session:       "proj",
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "operator action required",
	})

	output, err := captureStdout(t, func() error {
		if exitCode := PrintAttention(AttentionOptions{
			SinceCursor:         0,
			Session:             "proj",
			Timeout:             20 * time.Millisecond,
			PollInterval:        time.Millisecond,
			Condition:           WaitConditionActionRequired,
			Profile:             "operator",
			ActionRequiredLimit: 5,
			InterestingLimit:    4,
			BackgroundLimit:     3,
		}); exitCode != 0 {
			t.Fatalf("PrintAttention exit code = %d, want 0", exitCode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PrintAttention returned error: %v", err)
	}

	var resp AttentionResponse
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		t.Fatalf("failed to decode attention response: %v\noutput=%s", err, output)
	}
	if !resp.Success {
		t.Fatalf("expected successful attention response, got %#v", resp)
	}
	if resp.WakeReason != "attention" {
		t.Fatalf("WakeReason = %q, want %q", resp.WakeReason, "attention")
	}
	if resp.MatchedCondition != WaitConditionActionRequired {
		t.Fatalf("MatchedCondition = %q, want %q", resp.MatchedCondition, WaitConditionActionRequired)
	}
	if resp.TriggerEvent == nil || resp.TriggerEvent.Cursor != actionRequired.Cursor {
		t.Fatalf("TriggerEvent = %#v, want cursor %d", resp.TriggerEvent, actionRequired.Cursor)
	}
	if resp.Digest == nil {
		t.Fatal("expected digest payload")
	}
	if resp.Digest.EventCount != 1 {
		t.Fatalf("Digest.EventCount = %d, want 1 after operator profile filtering", resp.Digest.EventCount)
	}
	if len(resp.Digest.Buckets.Background) != 0 {
		t.Fatalf("Digest background bucket len = %d, want 0", len(resp.Digest.Buckets.Background))
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--attention-cursor=") {
		t.Fatalf("NextCommand = %q, want --attention-cursor", resp.CursorInfo.NextCommand)
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--attention-session=proj") {
		t.Fatalf("NextCommand = %q, want attention-session handoff", resp.CursorInfo.NextCommand)
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--profile=operator") {
		t.Fatalf("NextCommand = %q, want profile handoff", resp.CursorInfo.NextCommand)
	}
	if !strings.Contains(resp.CursorInfo.NextCommand, "--attention-condition=action_required") {
		t.Fatalf("NextCommand = %q, want condition handoff", resp.CursorInfo.NextCommand)
	}
}

// =============================================================================
// Operator Loop Integration Tests (br-9bmtl)
// =============================================================================

func TestOperatorLoop_CursorChaining(t *testing.T) {

	feed := newTestAttentionFeed(t)
	oldFeed := GetAttentionFeed()
	SetAttentionFeed(feed)
	defer SetAttentionFeed(oldFeed)

	// Step 1: Bootstrap — empty feed returns cursor 0
	events, latestCursor, err := feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("initial replay error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events on empty feed, got %d", len(events))
	}
	if latestCursor != 0 {
		t.Errorf("expected cursor 0 on empty feed, got %d", latestCursor)
	}

	// Step 2: Add events and replay from cursor 0
	ev1 := feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStalled,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "agent stalled",
		Session:       "proj",
	})
	ev2 := feed.Append(AttentionEvent{
		Category:      EventCategoryPane,
		Type:          EventTypePaneOutput,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "pane output",
		Session:       "proj",
	})

	events, latestCursor, err = feed.Replay(0, 100)
	if err != nil {
		t.Fatalf("replay error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
	if latestCursor != ev2.Cursor {
		t.Errorf("latest cursor = %d, want %d", latestCursor, ev2.Cursor)
	}

	// Step 3: Replay from ev1's cursor — should get only ev2
	events, latestCursor, err = feed.Replay(ev1.Cursor, 100)
	if err != nil {
		t.Fatalf("chained replay error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event from cursor %d, got %d", ev1.Cursor, len(events))
	}
	if len(events) > 0 && events[0].Cursor != ev2.Cursor {
		t.Errorf("expected event cursor %d, got %d", ev2.Cursor, events[0].Cursor)
	}

	// Step 4: Replay from latest — should get 0 events
	events, _, err = feed.Replay(latestCursor, 100)
	if err != nil {
		t.Fatalf("replay from latest error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events from latest cursor, got %d", len(events))
	}
}

func TestOperatorLoop_ProfileFilteredEvents(t *testing.T) {

	feed := newTestAttentionFeed(t)

	// Add events at different actionability levels
	feed.Append(AttentionEvent{
		Category:      EventCategorySystem,
		Type:          EventTypeSystemHealthChange,
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "system healthy",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Type:          EventTypeAgentStateChange,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent state changed",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Type:          EventTypeAlertWarning,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "context hot",
	})

	events, _, _ := feed.Replay(0, 100)
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Operator profile: min_actionability=interesting → should exclude background
	operatorProfile := GetProfile("operator")
	if operatorProfile == nil {
		t.Fatal("operator profile not found")
	}
	filters := ResolveEffectiveFilters("operator", ProfileFilters{})
	if filters.MinActionability != ActionabilityInteresting {
		t.Errorf("operator min_actionability = %q, want %q", filters.MinActionability, ActionabilityInteresting)
	}

	// Apply filters manually to verify
	var filtered []AttentionEvent
	for _, ev := range events {
		if filters.MatchesFilters(&ev) {
			filtered = append(filtered, ev)
		}
	}
	if len(filtered) != 2 {
		t.Errorf("operator profile should show 2 events (interesting+action_required), got %d", len(filtered))
	}

	// Debug profile: min_actionability=background → should include all
	debugFilters := ResolveEffectiveFilters("debug", ProfileFilters{})
	var debugFiltered []AttentionEvent
	for _, ev := range events {
		if debugFilters.MatchesFilters(&ev) {
			debugFiltered = append(debugFiltered, ev)
		}
	}
	if len(debugFiltered) != 3 {
		t.Errorf("debug profile should show all 3 events, got %d", len(debugFiltered))
	}

	// Minimal profile: min_severity=error AND min_actionability=action_required
	// The alert (severity=warning, actionability=action_required) does NOT meet min_severity=error.
	// So minimal profile should exclude all our test events (none have severity >= error).
	minFilters := ResolveEffectiveFilters("minimal", ProfileFilters{})
	var minFiltered []AttentionEvent
	for _, ev := range events {
		if minFilters.MatchesFilters(&ev) {
			minFiltered = append(minFiltered, ev)
		}
	}
	// None of our 3 events have severity >= error (warning < error), so 0 pass minimal.
	if len(minFiltered) != 0 {
		t.Errorf("minimal profile should show 0 events (none have severity>=error), got %d", len(minFiltered))
	}
}

func TestNewPTStateChangeAttentionEvent_SuppressesBenignInitialClassification(t *testing.T) {

	event, ok := NewPTStateChangeAttentionEvent(pt.ClassificationStateChange{
		Session:  "proj",
		Pane:     "proj__cc_1",
		PID:      12345,
		Previous: pt.ClassUnknown,
		Current:  pt.ClassUseful,
		Event: pt.ClassificationEvent{
			Classification: pt.ClassUseful,
			Confidence:     0.95,
			Timestamp:      time.Date(2026, 3, 22, 20, 0, 0, 0, time.UTC),
			Reason:         "working normally",
		},
		Initial:          true,
		ConsecutiveCount: 1,
	})
	if ok {
		t.Fatalf("expected benign initial PT classification to be suppressed, got %#v", event)
	}
}

func TestNewPTStateChangeAttentionEvent_RecoverySemantics(t *testing.T) {

	event, ok := NewPTStateChangeAttentionEvent(pt.ClassificationStateChange{
		Session:  "proj",
		Pane:     "proj__cc_1",
		PID:      12345,
		Previous: pt.ClassStuck,
		Current:  pt.ClassWaiting,
		Event: pt.ClassificationEvent{
			Classification: pt.ClassWaiting,
			Confidence:     0.88,
			Timestamp:      time.Date(2026, 3, 22, 20, 1, 0, 0, time.UTC),
			Reason:         "network activity detected",
			NetworkActive:  true,
		},
		ConsecutiveCount: 1,
	})
	if !ok {
		t.Fatal("expected recovery PT classification to normalize")
	}
	if event.Type != EventTypeAgentRecovered {
		t.Fatalf("event.Type = %q, want %q", event.Type, EventTypeAgentRecovered)
	}
	if event.ReasonCode != "pt:state:recovered" {
		t.Fatalf("event.ReasonCode = %q, want pt:state:recovered", event.ReasonCode)
	}
	if event.Actionability != ActionabilityInteresting {
		t.Fatalf("event.Actionability = %q, want %q", event.Actionability, ActionabilityInteresting)
	}
	if got := fmt.Sprint(event.Details["current_classification"]); got != string(pt.ClassWaiting) {
		t.Fatalf("current_classification = %q, want %q", got, pt.ClassWaiting)
	}
}

func TestPublishPTAlert_DeduplicatesRepeatedThresholdAlerts(t *testing.T) {

	feed := newTestAttentionFeed(t)
	base := time.Date(2026, 3, 22, 20, 2, 0, 0, time.UTC)
	alert := pt.Alert{
		Session:   "proj",
		Type:      pt.AlertStuck,
		Pane:      "proj__cc_1",
		PID:       12345,
		State:     pt.ClassStuck,
		Duration:  10 * time.Minute,
		Timestamp: base,
		Message:   "Agent proj__cc_1 has been stuck for 10m0s",
	}

	first, ok := feed.PublishPTAlert(alert)
	if !ok {
		t.Fatal("expected first PT alert to publish")
	}
	second, ok := feed.PublishPTAlert(alert)
	if ok {
		t.Fatalf("expected duplicate PT alert to be suppressed, got %#v", second)
	}

	events, newest, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatalf("Replay failed: %v", err)
	}
	if newest != first.Cursor {
		t.Fatalf("newest cursor = %d, want %d", newest, first.Cursor)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 PT alert event after dedup, got %d", len(events))
	}
	if events[0].ReasonCode != "pt:alert:stuck" {
		t.Fatalf("stored PT alert reason_code = %q, want pt:alert:stuck", events[0].ReasonCode)
	}
}

func TestOperatorLoop_SnapshotSummaryConsistency(t *testing.T) {

	feed := newTestAttentionFeed(t)

	// Add known events
	feed.Append(AttentionEvent{
		Category:      EventCategoryAlert,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityInfo,
		Summary:       "critical alert",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Actionability: ActionabilityInteresting,
		Severity:      SeverityInfo,
		Summary:       "agent info",
	})
	feed.Append(AttentionEvent{
		Category:      EventCategoryAgent,
		Actionability: ActionabilityActionRequired,
		Severity:      SeverityWarning,
		Summary:       "agent warning",
	})

	summary := buildSnapshotAttentionSummary(feed)
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}

	// Verify counts match what we added
	if summary.TotalEvents != 3 {
		t.Errorf("TotalEvents = %d, want 3", summary.TotalEvents)
	}
	if summary.ActionRequiredCount != 2 {
		t.Errorf("ActionRequiredCount = %d, want 2", summary.ActionRequiredCount)
	}
	if summary.InterestingCount != 1 {
		t.Errorf("InterestingCount = %d, want 1", summary.InterestingCount)
	}

	// Category breakdown
	if summary.ByCategoryCount["alert"] != 1 {
		t.Errorf("ByCategoryCount[alert] = %d, want 1", summary.ByCategoryCount["alert"])
	}
	if summary.ByCategoryCount["agent"] != 2 {
		t.Errorf("ByCategoryCount[agent] = %d, want 2", summary.ByCategoryCount["agent"])
	}

	// TopItems should include both action_required events (capped at 3)
	if len(summary.TopItems) != 2 {
		t.Errorf("TopItems = %d, want 2", len(summary.TopItems))
	}

	// Unsupported signals should always be present
	if len(summary.UnsupportedSignals) == 0 {
		t.Error("expected unsupported signals for honest representation")
	}

	// Next steps should reference action_required events
	if len(summary.NextSteps) == 0 {
		t.Error("expected next-step hints")
	}
}

// =============================================================================
// Durable-by-default feed (bd-bdjf4.1)
// =============================================================================

// resetGlobalFeedForTest clears the process-wide feed and opener, and restores
// them when the test ends.
func resetGlobalFeedForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		SetAttentionFeed(nil)
		SetAttentionStoreOpener(nil)
	}
	reset()
	t.Cleanup(reset)
}

func testAttentionEvent(summary string) AttentionEvent {
	return AttentionEvent{
		Session:       "proj",
		Category:      EventCategorySystem,
		Type:          EventTypeSystemHealthChange,
		Source:        "test",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       summary,
	}
}

func TestGetAttentionFeed_NoOpenerStaysInMemory(t *testing.T) {
	resetGlobalFeedForTest(t)
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("NTM_CONFIG", "")

	feed := GetAttentionFeed()
	t.Cleanup(feed.Stop)
	if feed.StoreHealth().Configured {
		t.Fatal("feed reports a store with no opener registered")
	}
	feed.Append(testAttentionEvent("memory only"))

	if _, err := os.Stat(filepath.Join(configDir, "ntm", "state.db")); !os.IsNotExist(err) {
		t.Fatalf("state.db was created with no opener: %v", err)
	}
}

func TestGetAttentionFeed_OpensStoreOnce(t *testing.T) {
	resetGlobalFeedForTest(t)
	var calls atomic.Int32
	SetAttentionStoreOpener(func() (AttentionStore, error) {
		calls.Add(1)
		return newStubAttentionStore(), nil
	})

	if PeekAttentionFeed() != nil {
		t.Fatal("PeekAttentionFeed should stay nil before first use")
	}

	var wg sync.WaitGroup
	feeds := make([]*AttentionFeed, 50)
	for i := range feeds {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			feeds[i] = GetAttentionFeed()
		}(i)
	}
	wg.Wait()
	t.Cleanup(feeds[0].Stop)

	if got := calls.Load(); got != 1 {
		t.Fatalf("opener ran %d times, want 1", got)
	}
	for _, feed := range feeds {
		if feed != feeds[0] {
			t.Fatal("callers got different feeds")
		}
	}
	health := feeds[0].StoreHealth()
	if !health.Configured || !health.Durable || health.LastError != "" {
		t.Fatalf("health = %+v, want configured and durable", health)
	}
}

func TestGetAttentionFeed_OpenFailureFallsBackToMemory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opener  func() (AttentionStore, error)
		wantErr string
	}{
		{
			name:    "opener returns an error",
			opener:  func() (AttentionStore, error) { return nil, errors.New("disk full") },
			wantErr: "disk full",
		},
		{
			name:    "opener returns no store and no error",
			opener:  func() (AttentionStore, error) { return nil, nil },
			wantErr: "no store",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetGlobalFeedForTest(t)
			SetAttentionStoreOpener(tc.opener)

			feed := GetAttentionFeed()
			t.Cleanup(feed.Stop)
			appended := feed.Append(testAttentionEvent("still works"))

			events, _, err := feed.Replay(appended.Cursor-1, 10)
			if err != nil {
				t.Fatalf("Replay error: %v", err)
			}
			if len(events) != 1 || events[0].Summary != "still works" {
				t.Fatalf("replayed %+v, want the appended event", events)
			}

			health := feed.StoreHealth()
			if !health.Configured || health.Durable || !strings.Contains(health.LastError, tc.wantErr) {
				t.Fatalf("health = %+v, want configured, not durable, error containing %q", health, tc.wantErr)
			}
			if health.Since.IsZero() {
				t.Fatalf("health = %+v, want a failure time", health)
			}
		})
	}
}

// newEventsFilterFeed returns a global store-backed feed holding system events,
// with mail events at the given 1-based positions.
func newEventsFilterFeed(t *testing.T, total int, mailAt ...int) []int64 {
	t.Helper()
	resetGlobalFeedForTest(t)
	feed := NewAttentionFeed(AttentionFeedConfig{
		JournalSize:     100,
		RetentionPeriod: time.Hour,
	}, WithAttentionStore(newTestAttentionStore(t)))
	t.Cleanup(feed.Stop)
	SetAttentionFeed(feed)

	isMail := make(map[int]bool, len(mailAt))
	for _, position := range mailAt {
		isMail[position] = true
	}
	var mailCursors []int64
	for position := 1; position <= total; position++ {
		event := testAttentionEvent(fmt.Sprintf("event %d", position))
		if isMail[position] {
			event.Category = EventCategoryMail
			event.Type = EventTypeMailReceived
		}
		appended := feed.Append(event)
		if isMail[position] {
			mailCursors = append(mailCursors, appended.Cursor)
		}
	}
	return mailCursors
}

// A filter must apply to the whole replay, not to the first page. Found by the
// Sprint 1 human test: on a busy feed, mail sat beyond the first 100 events.
func TestBuildEventsOutput_FilterFindsEventsBeyondLimit(t *testing.T) {
	mail := newEventsFilterFeed(t, 150, 150)

	output, status := BuildEventsOutput(EventsOptions{Limit: 100, CategoryFilter: []string{"mail"}})
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if len(output.Events) != 1 || output.Events[0].Cursor != mail[0] {
		t.Fatalf("events = %+v, want the one mail event at cursor %d", output.Events, mail[0])
	}
	if output.HasMore {
		t.Fatal("HasMore = true with nothing left to read")
	}
	if output.NextCursor != mail[0] {
		t.Fatalf("NextCursor = %d, want %d", output.NextCursor, mail[0])
	}
}

// With no more matches, the cursor moves past non-matching events, so the reader
// doesn't scan them again, and it reaches the feed's latest cursor.
func TestBuildEventsOutput_NextCursorPassesNonMatchingTail(t *testing.T) {
	newEventsFilterFeed(t, 150, 10)

	output, _ := BuildEventsOutput(EventsOptions{Limit: 100, CategoryFilter: []string{"mail"}})
	if len(output.Events) != 1 || output.HasMore {
		t.Fatalf("events = %d, HasMore = %v", len(output.Events), output.HasMore)
	}
	if output.NextCursor != output.ReplayWindow.LatestCursor {
		t.Fatalf("NextCursor = %d, want latest cursor %d", output.NextCursor, output.ReplayWindow.LatestCursor)
	}
}

func TestBuildEventsOutput_FilteredPagingReturnsEveryMatchOnce(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 7, 100} {
		t.Run(fmt.Sprintf("limit %d", limit), func(t *testing.T) {
			want := newEventsFilterFeed(t, 300, 5, 6, 7, 120, 121, 250, 300)

			var got []int64
			cursor := int64(0)
			for page := 0; ; page++ {
				if page > len(want)+1 {
					t.Fatalf("paging didn't finish; got %v", got)
				}
				output, status := BuildEventsOutput(EventsOptions{
					SinceCursor:    cursor,
					Limit:          limit,
					CategoryFilter: []string{"mail"},
				})
				if status != 200 {
					t.Fatalf("status = %d", status)
				}
				if len(output.Events) > limit {
					t.Fatalf("page has %d events, over the limit of %d", len(output.Events), limit)
				}
				for _, event := range output.Events {
					got = append(got, event.Cursor)
				}
				cursor = output.NextCursor
				if !output.HasMore {
					break
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

func TestBuildEventsOutput_NoFilterKeepsLimitBehavior(t *testing.T) {
	newEventsFilterFeed(t, 5)

	output, _ := BuildEventsOutput(EventsOptions{Limit: 2})
	if len(output.Events) != 2 || !output.HasMore {
		t.Fatalf("events = %d, HasMore = %v, want 2 and true", len(output.Events), output.HasMore)
	}
	if output.NextCursor != output.Events[1].Cursor {
		t.Fatalf("NextCursor = %d, want %d", output.NextCursor, output.Events[1].Cursor)
	}
}

// A rare filter on a huge feed must not scan without limit. It stops, reports
// more, and hands back a cursor to continue from.
func TestReplayMatchingEvents_StopsAtScanLimit(t *testing.T) {
	mail := newEventsFilterFeed(t, 35, 35)
	opts := EventsOptions{Limit: 5, CategoryFilter: []string{"mail"}}
	feed := GetAttentionFeed()

	first, err := replayMatchingEvents(feed, opts, 5, 10, 20)
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if len(first.events) != 0 || !first.hasMore || first.resumeCursor != 20 {
		t.Fatalf("first scan = %+v, want no events, more, resume at 20", first)
	}

	opts.SinceCursor = first.resumeCursor
	second, err := replayMatchingEvents(feed, opts, 5, 10, 20)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if len(second.events) != 1 || second.events[0].Cursor != mail[0] || second.hasMore {
		t.Fatalf("second scan = %+v, want the mail event and no more", second)
	}
}

// openStoreFile opens and migrates its own connection to a store file, so two
// feeds on one path behave like two processes.
func openStoreFile(t *testing.T, path string) *state.Store {
	t.Helper()
	store, err := state.Open(path)
	if err != nil {
		t.Fatalf("open store %s: %v", path, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate store %s: %v", path, err)
	}
	return store
}

// appendAged appends an event whose timestamp is age in the past. Expiry is
// computed from the timestamp, so no clock needs faking.
func appendAged(feed *AttentionFeed, summary string, age time.Duration) AttentionEvent {
	event := testAttentionEvent(summary)
	event.Ts = time.Now().Add(-age).UTC().Format(time.RFC3339Nano)
	return feed.Append(event)
}

func assertSummaries(t *testing.T, events []AttentionEvent, want ...string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		got = append(got, event.Summary)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("summaries = %q, want %q", got, want)
	}
}

func TestClosure_TwoHourOldEventStillReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	appendAged(writer, "two hours old", 2*time.Hour)

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "two hours old")
}

func TestClosure_PastRetentionRemovedAndCursorExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	eightDays := appendAged(writer, "eight days", 8*24*time.Hour)
	// A cursor only counts as expired below OldestCursor-1, so the stale cursor
	// needs a second removed event after it.
	appendAged(writer, "padding", 8*24*time.Hour)
	appendAged(writer, "six days", 6*24*time.Hour)
	appendAged(writer, "new", 0)

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	// Replay reads the window before cleanup runs, so the first call removes the
	// old rows and the second one sees the new window.
	first, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, first, "six days", "new")

	_, _, err = reader.Replay(eightDays.Cursor, 10)
	var expired *CursorExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("err = %v, want CursorExpiredError", err)
	}
}

func TestClosure_RowSavedWithOneHourExpiryKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store := openStoreFile(t, path)
	ts := time.Now().Add(-2 * time.Hour).UTC()
	oldExpiry := ts.Add(time.Hour)
	if _, err := store.AppendAttentionEvent(&state.StoredAttentionEvent{
		Ts:            ts,
		Category:      string(EventCategorySystem),
		EventType:     string(EventTypeSystemHealthChange),
		Source:        "test",
		Actionability: state.Actionability(ActionabilityBackground),
		Severity:      state.Severity(SeverityInfo),
		Summary:       "saved before the change",
		ExpiresAt:     &oldExpiry,
	}); err != nil {
		t.Fatalf("seed old row: %v", err)
	}

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events)
}

// TestClosure_AsOfReturnsNewestEventsAcrossRetention guards the lookback, which
// is the retention period. A week of events must not crowd out the recent ones.
func TestClosure_AsOfReturnsNewestEventsAcrossRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	for i := 0; i < 50; i++ {
		appendAged(writer, fmt.Sprintf("old-%d", i), 6*24*time.Hour)
	}
	for i := 0; i < 3; i++ {
		appendAged(writer, fmt.Sprintf("recent-%d", i), 10*time.Minute)
	}

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, meta, err := reader.ReconstructAsOf(time.Now(), 3)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "recent-0", "recent-1", "recent-2")
	if meta == nil || !meta.Partial {
		t.Fatalf("meta = %+v, want a partial result, since older events were left out", meta)
	}
}

func TestAttentionEventToStored_ExpiryProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		ts := time.Unix(rapid.Int64Range(946684800, 4102444800).Draw(rt, "sec"),
			rapid.Int64Range(0, 999999999).Draw(rt, "nsec")).UTC()
		stored := attentionEventToStored(AttentionEvent{Ts: ts.Format(time.RFC3339Nano)},
			DefaultAttentionFeedConfig().RetentionPeriod)
		if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(ts.Add(7*24*time.Hour)) {
			rt.Fatalf("expiry = %v for ts %v", stored.ExpiresAt, ts)
		}
	})
}

func TestDefaultAttentionFeedConfig_JournalSizeUnchanged(t *testing.T) {
	if got := DefaultAttentionFeedConfig().JournalSize; got != 10000 {
		t.Fatalf("JournalSize = %d, want 10000", got)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// faultInjectingStore is a real store whose appends, or event reads, fail on demand.
type faultInjectingStore struct {
	*state.Store
	fail              atomic.Bool
	failReads         atomic.Bool
	addressedAttempts atomic.Int32
}

func (s *faultInjectingStore) GetAttentionEventsSince(since int64, limit int) ([]state.StoredAttentionEvent, error) {
	if s.failReads.Load() {
		return nil, errors.New("injected read failure")
	}
	return s.Store.GetAttentionEventsSince(since, limit)
}

func (s *faultInjectingStore) AppendAttentionEvent(event *state.StoredAttentionEvent) (int64, error) {
	if s.fail.Load() {
		return 0, errors.New("injected append failure")
	}
	return s.Store.AppendAttentionEvent(event)
}

func (s *faultInjectingStore) AppendAddressedAttentionEvent(event *state.StoredAttentionEvent) (int64, bool, error) {
	s.addressedAttempts.Add(1)
	if s.fail.Load() {
		return 0, false, errors.New("injected append failure")
	}
	return s.Store.AppendAddressedAttentionEvent(event)
}

// holdExclusiveLock takes SQLite's write lock on a separate connection and
// returns the function that releases it.
func holdExclusiveLock(t *testing.T, path string) func() {
	t.Helper()
	store := openStoreFile(t, path)
	conn, err := store.DB().Conn(context.Background())
	if err != nil {
		t.Fatalf("get lock connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("take lock: %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if _, err := conn.ExecContext(context.Background(), "COMMIT"); err != nil {
				t.Errorf("release lock: %v", err)
			}
			_ = conn.Close()
		})
	}
	t.Cleanup(release)
	return release
}

// slogCapture records structured log lines for the length of a test.
type slogCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *slogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

func (c *slogCapture) count(message string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Count(c.buf.String(), message)
}

func (c *slogCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func captureSlog(t *testing.T) *slogCapture {
	t.Helper()
	capture := &slogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(capture, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture
}

func cursorsOf(events []AttentionEvent) []int64 {
	cursors := make([]int64, 0, len(events))
	for _, event := range events {
		cursors = append(cursors, event.Cursor)
	}
	return cursors
}

func TestClosure_RecoversAfterStoreLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	clock := newTestClock()
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(openStoreFile(t, path)), WithAttentionClock(clock.Now))
	t.Cleanup(writer.Stop)

	release := holdExclusiveLock(t, path)
	writer.Append(testAttentionEvent("during lock")) // Waits out the real busy timeout.
	if health := writer.StoreHealth(); health.Durable || health.LastError == "" {
		t.Fatalf("health during the lock = %+v, want not durable with an error", health)
	}
	release()

	clock.Advance(AttentionStoreRetryInterval + time.Second)
	writer.Append(testAttentionEvent("after lock"))

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "after lock")
	if health := writer.StoreHealth(); !health.Durable || health.LastError != "" || !health.Since.IsZero() {
		t.Fatalf("health after recovery = %+v, want durable with no error", health)
	}
}

func TestAttentionFeed_RetryLimitedDuringOutage(t *testing.T) {
	captureSlog(t)
	store := newStubAttentionStore()
	store.failAppend.Store(true)
	clock := newTestClock()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	for i := 0; i < 100; i++ {
		feed.Append(testAttentionEvent("x"))
	}
	if got := store.appendAttempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 inside the retry interval", got)
	}

	// Just short of the interval is still too soon.
	clock.Advance(AttentionStoreRetryInterval - time.Nanosecond)
	feed.Append(testAttentionEvent("y"))
	if got := store.appendAttempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 just before the interval", got)
	}

	// Exactly at the interval counts as allowed.
	clock.Advance(time.Nanosecond)
	feed.Append(testAttentionEvent("z"))
	if got := store.appendAttempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 at the interval", got)
	}

	// A retry that fails starts the wait again.
	clock.Advance(AttentionStoreRetryInterval - time.Nanosecond)
	feed.Append(testAttentionEvent("still too soon"))
	if got := store.appendAttempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 after a failed retry moved the next try", got)
	}
}

func TestAttentionFeed_OneReportPerStateChange(t *testing.T) {
	logs := captureSlog(t)
	store := newStubAttentionStore()
	clock := newTestClock()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	outage := func() {
		store.failAppend.Store(true)
		for i := 0; i < 5; i++ {
			clock.Advance(AttentionStoreRetryInterval)
			feed.Append(testAttentionEvent("down"))
		}
		store.failAppend.Store(false)
		clock.Advance(AttentionStoreRetryInterval)
		feed.Append(testAttentionEvent("back"))
		feed.Append(testAttentionEvent("still back"))
	}

	outage()
	if got := logs.count(attentionStoreDownLogMessage); got != 1 {
		t.Fatalf("failure reports = %d, want 1", got)
	}
	if got := logs.count(attentionStoreRecoveredLogMessage); got != 1 {
		t.Fatalf("recovery reports = %d, want 1", got)
	}
	if got := logs.count("append failed"); got != 1 {
		t.Fatalf("reports naming the error = %d, want 1", got)
	}
	if got := logs.count("memory_only_count=5"); got != 1 {
		t.Fatalf("recovery report should count 5 memory-only events: %q", logs.buf.String())
	}

	outage()
	if got := logs.count(attentionStoreDownLogMessage); got != 2 {
		t.Fatalf("failure reports after a second outage = %d, want 2", got)
	}
	if got := logs.count(attentionStoreRecoveredLogMessage); got != 2 {
		t.Fatalf("recovery reports after a second outage = %d, want 2", got)
	}
}

func TestAttentionFeed_ReplayOrderProperty(t *testing.T) {
	captureSlog(t)
	rapid.Check(t, func(rt *rapid.T) {
		store := newStubAttentionStore()
		clock := newTestClock()
		feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
			WithAttentionStore(store), WithAttentionClock(clock.Now))
		defer feed.Stop()

		var appended []int64
		steps := rapid.IntRange(1, 200).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				appended = append(appended, feed.Append(testAttentionEvent("e")).Cursor)
			case 1:
				store.failAppend.Store(rapid.Bool().Draw(rt, "fail"))
			case 2:
				clock.Advance(time.Duration(rapid.IntRange(0, 60).Draw(rt, "secs")) * time.Second)
			}
		}

		events, _, err := feed.Replay(0, len(appended)+1)
		if err != nil {
			rt.Fatal(err)
		}
		if got := cursorsOf(events); !slices.Equal(got, appended) {
			rt.Fatalf("replay %v, appended %v", got, appended)
		}
		for i := 1; i < len(appended); i++ {
			if appended[i] <= appended[i-1] {
				rt.Fatalf("cursors not strictly increasing: %v", appended)
			}
		}
	})
}

func TestAttentionFeed_MemoryOnlyEvents(t *testing.T) {
	newOutageFeed := func(t *testing.T, journalSize int) (*AttentionFeed, *stubAttentionStore, *testClock) {
		t.Helper()
		captureSlog(t)
		store := newStubAttentionStore()
		clock := newTestClock()
		feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: journalSize, RetentionPeriod: time.Hour},
			WithAttentionStore(store), WithAttentionClock(clock.Now))
		t.Cleanup(feed.Stop)
		return feed, store, clock
	}

	t.Run("dropped oldest first past the journal size", func(t *testing.T) {
		feed, store, _ := newOutageFeed(t, 3)
		store.failAppend.Store(true)
		for i := 0; i < 5; i++ {
			feed.Append(testAttentionEvent(fmt.Sprintf("memory-%d", i)))
		}
		events, _, err := feed.Replay(0, 10)
		if err != nil {
			t.Fatal(err)
		}
		assertSummaries(t, events, "memory-2", "memory-3", "memory-4")
		if stats := feed.Stats(); stats.Count != 3 || stats.OldestCursor != 3 || stats.NewestCursor != 5 {
			t.Fatalf("stats = %+v, want 3 events from cursor 3 to 5", stats)
		}
	})

	t.Run("still readable after recovery, with a limit and a resume cursor", func(t *testing.T) {
		feed, store, clock := newOutageFeed(t, 100)
		feed.Append(testAttentionEvent("stored-1"))
		store.failAppend.Store(true)
		feed.Append(testAttentionEvent("memory-2"))
		store.failAppend.Store(false)
		clock.Advance(AttentionStoreRetryInterval)
		feed.Append(testAttentionEvent("stored-3"))

		page, _, err := feed.Replay(0, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertSummaries(t, page, "stored-1", "memory-2")
		rest, _, err := feed.Replay(page[1].Cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		assertSummaries(t, rest, "stored-3")
		if stats := feed.Stats(); stats.Count != 3 {
			t.Fatalf("stats.Count = %d, want 3 after recovery", stats.Count)
		}
	})

	t.Run("a failed reservation leaves the feed down", func(t *testing.T) {
		feed, store, clock := newOutageFeed(t, 100)
		store.failAppend.Store(true)
		feed.Append(testAttentionEvent("memory"))
		store.failAppend.Store(false)
		store.failReserve.Store(true)
		clock.Advance(AttentionStoreRetryInterval)
		feed.Append(testAttentionEvent("reserve fails"))
		if feed.StoreHealth().Durable {
			t.Fatal("feed reported durable after its cursor reservation failed")
		}
		if got := len(store.events); got != 0 {
			t.Fatalf("store has %d events, want none", got)
		}
	})
}

// A cursor another process used can match a memory-only one. Neither event may be
// lost from this process's replay. See the accepted limit in the Loop 1 plan.
func TestClosure_CursorClashKeepsBothEvents(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	store.fail.Store(true)
	memory := feed.Append(testAttentionEvent("memory"))
	other := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(other.Stop)
	durable := other.Append(testAttentionEvent("other process"))
	if durable.Cursor != memory.Cursor {
		t.Fatalf("setup: cursors %d and %d should clash", durable.Cursor, memory.Cursor)
	}

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "other process", "memory")
}

func TestClosure_RecoveredCursorAboveMemoryOnly(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	store.fail.Store(true)
	first := feed.Append(testAttentionEvent("memory one"))
	second := feed.Append(testAttentionEvent("memory two"))
	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	third := feed.Append(testAttentionEvent("durable"))

	if !(first.Cursor < second.Cursor && second.Cursor < third.Cursor) {
		t.Fatalf("cursors %d, %d, %d", first.Cursor, second.Cursor, third.Cursor)
	}
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "memory one", "memory two", "durable")
}

func TestAttentionFeed_FailedOpenIsRetried(t *testing.T) {
	logs := captureSlog(t)
	store := newStubAttentionStore()
	var works atomic.Bool
	clock := newTestClock()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionClock(clock.Now),
		WithAttentionStoreOpener(func() (AttentionStore, error) {
			if !works.Load() {
				return nil, errors.New("disk full")
			}
			return store, nil
		}))
	t.Cleanup(feed.Stop)

	memory := feed.Append(testAttentionEvent("memory"))
	if health := feed.StoreHealth(); !health.Configured || health.Durable || !strings.Contains(health.LastError, "disk full") {
		t.Fatalf("health while the open fails = %+v", health)
	}

	works.Store(true)
	feed.Append(testAttentionEvent("too soon"))
	if got := store.appendAttempts.Load(); got != 0 {
		t.Fatalf("attempts = %d, want 0 inside the retry interval", got)
	}

	// Replay runs at the same time as the open, for the race detector.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_, _, _ = feed.Replay(0, 10)
			_ = feed.StoreHealth()
		}
	}()
	clock.Advance(AttentionStoreRetryInterval)
	durable := feed.Append(testAttentionEvent("durable"))
	<-done

	if store.appendAttempts.Load() != 1 || !feed.StoreHealth().Durable {
		t.Fatalf("attempts %d, health %+v", store.appendAttempts.Load(), feed.StoreHealth())
	}
	if durable.Cursor <= memory.Cursor {
		t.Fatalf("durable cursor %d should sort above memory cursor %d", durable.Cursor, memory.Cursor)
	}
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "memory", "too soon", "durable")
	if got := logs.count(attentionStoreRecoveredLogMessage); got != 1 {
		t.Fatalf("recovery reports = %d, want 1", got)
	}
}

func newRecipientFeed(t *testing.T) *AttentionFeed {
	t.Helper()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))))
	t.Cleanup(feed.Stop)
	return feed
}

func TestAttentionEventStored_RecipientRoundtrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		event := testAttentionEvent("x")
		event.RecipientProject = rapid.StringN(1, 64, -1).Draw(rt, "project")
		event.RecipientAgent = rapid.StringN(1, 64, -1).Draw(rt, "agent")
		event.MessageID = rapid.Int64Range(1, 1<<53).Draw(rt, "message")

		back := attentionEventFromStored(attentionEventToStored(event, time.Hour))
		if back.RecipientProject != event.RecipientProject || back.RecipientAgent != event.RecipientAgent || back.MessageID != event.MessageID {
			rt.Fatalf("round trip %+v -> %+v", event, back)
		}
	})
}

func TestAttentionEventStored_NoMessageIDStaysNull(t *testing.T) {
	stored := attentionEventToStored(testAttentionEvent("x"), time.Hour)
	if stored.MessageID != nil {
		t.Fatalf("MessageID = %d, want nil for an unaddressed event", *stored.MessageID)
	}
}

func TestPublishMail_FillsRecipientFields(t *testing.T) {
	publishers := map[string]func(*AttentionFeed, string, int) AttentionEvent{
		"pending": func(f *AttentionFeed, to string, id int) AttentionEvent {
			return f.PublishMailPending("/proj", "Sender", to, "subject", id, "thread")
		},
		"ack required": func(f *AttentionFeed, to string, id int) AttentionEvent {
			return f.PublishMailAckRequired("/proj", "Sender", to, "subject", id, "thread")
		},
	}
	for name, publish := range publishers {
		t.Run(name, func(t *testing.T) {
			feed := newTestAttentionFeed(t)
			got := publish(feed, "RedStone", 42)
			if got.RecipientProject != "/proj" || got.RecipientAgent != "RedStone" || got.MessageID != 42 {
				t.Fatalf("event = %+v", got)
			}
			if got.Details["to"] != "RedStone" || got.Details["message_id"] != 42 {
				t.Fatalf("details changed: %+v", got.Details)
			}
			if empty := publish(feed, " ", 43); empty.RecipientProject != "" || empty.RecipientAgent != "" || empty.MessageID != 0 {
				t.Fatalf("event with no recipient = %+v", empty)
			}
		})
	}
}

func TestReplayMatchingEvents_RecipientIgnoresOtherEventsInScanLimit(t *testing.T) {
	feed := newRecipientFeed(t)
	for i := 0; i < 50; i++ {
		feed.Append(testAttentionEvent("other"))
	}
	wanted := feed.PublishMailPending("/proj", "Sender", "RedStone", "for red", 42, "")

	// A scan limit smaller than the other events would stop a read that filters
	// after fetching, before it reached RedStone's event.
	result, err := replayMatchingEvents(feed, EventsOptions{Recipient: "RedStone"}, 10, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.events) != 1 || result.events[0].Cursor != wanted.Cursor || result.hasMore {
		t.Fatalf("result = %+v", result)
	}
}

func TestBuildEventsOutput_RecipientPagesEachEventOnce(t *testing.T) {
	resetGlobalFeedForTest(t)
	feed := newRecipientFeed(t)
	SetAttentionFeed(feed)

	var want []int64
	for i := 0; i < 25; i++ {
		feed.Append(testAttentionEvent("other"))
		want = append(want, feed.PublishMailPending("/proj", "Sender", "RedStone", "for red", i+1, "").Cursor)
	}

	var got []int64
	since := int64(0)
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatalf("still more after %d pages: %v", page, got)
		}
		output, code := BuildEventsOutput(EventsOptions{Recipient: "RedStone", SinceCursor: since, Limit: 10})
		if code != 200 || !output.Success {
			t.Fatalf("code %d, output %+v", code, output)
		}
		got = append(got, cursorsOf(output.Events)...)
		if !output.HasMore {
			break
		}
		since = output.NextCursor
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged cursors %v, want %v", got, want)
	}
}

func TestBuildEventsOutput_EmptyFeedReturnsEmptyList(t *testing.T) {
	for name, opts := range map[string]EventsOptions{
		"no filter":      {},
		"recipient only": {Recipient: "NoSuchAgent"},
	} {
		t.Run(name, func(t *testing.T) {
			resetGlobalFeedForTest(t)
			SetAttentionFeed(newRecipientFeed(t))
			output, code := BuildEventsOutput(opts)
			if code != 200 || output.Events == nil || len(output.Events) != 0 {
				t.Fatalf("code %d, events %#v, want an empty non-nil list", code, output.Events)
			}
		})
	}
}

func TestUnaddressedEvents_HaveNoRecipient(t *testing.T) {
	feed := newRecipientFeed(t)
	event := testAttentionEvent("9 unread message(s) across the swarm")
	event.Category, event.Type = EventCategoryMail, EventTypeMailUnread
	feed.Append(event)

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "9 unread message(s) across the swarm")
	raw, err := json.Marshal(events[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"recipient_project", "recipient_agent", "message_id"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("JSON has %q: %s", key, raw)
		}
	}

	matches, _, err := feed.ReplayForRecipient(" ", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if matches == nil || len(matches) != 0 {
		t.Fatalf("a blank name matched unaddressed events: %+v", matches)
	}
}

func TestReplayForRecipient_NoStore(t *testing.T) {
	feed := newTestAttentionFeed(t)
	feed.PublishMailPending("/proj", "Sender", "BlueLake", "for blue", 41, "")
	feed.PublishMailPending("/proj", "Sender", "RedStone", "for red", 42, "")

	events, _, err := feed.ReplayForRecipient("redstone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].MessageID != 42 {
		t.Fatalf("events = %+v", events)
	}
}

func TestReplayForRecipient_MergesMemoryOnlyEvents(t *testing.T) {
	captureSlog(t)
	store := &faultInjectingStore{Store: openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))}
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(store))
	t.Cleanup(feed.Stop)

	stored := feed.PublishMailPending("/proj", "Sender", "RedStone", "stored", 41, "")
	store.fail.Store(true)
	memory := feed.PublishMailPending("/proj", "Sender", "RedStone", "memory", 42, "")
	feed.PublishMailPending("/proj", "Sender", "BlueLake", "not this one", 43, "")

	events, _, err := feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := cursorsOf(events); !slices.Equal(got, []int64{stored.Cursor, memory.Cursor}) {
		t.Fatalf("cursors = %v, want %d and %d", got, stored.Cursor, memory.Cursor)
	}

	limited, _, err := feed.ReplayForRecipient("RedStone", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cursorsOf(limited); !slices.Equal(got, []int64{stored.Cursor}) {
		t.Fatalf("limit across both sources: cursors = %v", got)
	}
}

func TestReplayForRecipient_SameNameInTwoProjects(t *testing.T) {
	feed := newRecipientFeed(t)
	feed.PublishMailPending("/one", "Sender", "RedStone", "in one", 42, "")
	feed.PublishMailPending("/two", "Sender", "RedStone", "in two", 42, "")

	events, _, err := feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].RecipientProject != "/one" || events[1].RecipientProject != "/two" {
		t.Fatalf("events = %+v", events)
	}
}

func TestRobotEventsCapabilities_ListRecipientFlag(t *testing.T) {
	capabilities, err := GetCapabilities()
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, command := range capabilities.Commands {
		if command.Flag != "--robot-events" {
			continue
		}
		for _, parameter := range command.Parameters {
			listed = listed || parameter.Flag == "--events-recipient"
		}
	}
	if !listed {
		t.Fatal("--robot-capabilities doesn't list --events-recipient")
	}

	inContract := false
	for _, command := range AttentionCommands {
		if command.Name != "--robot-events" {
			continue
		}
		for _, arg := range command.Args {
			inContract = inContract || arg.Name == "--events-recipient"
		}
	}
	if !inContract {
		t.Fatal("the attention contract doesn't list --events-recipient")
	}
}

func TestAppendAddressed_RepeatDoesNothing(t *testing.T) {
	// A store-backed feed delivers through its poll, so the test ticks it by
	// hand before counting deliveries.
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, filepath.Join(t.TempDir(), "state.db")), ticker)
	var delivered atomic.Int32
	unsubscribe := feed.Subscribe(func(AttentionEvent) { delivered.Add(1) })
	defer unsubscribe()

	first := feed.PublishMailPending("/proj", "Sender", "RedStone", "subject", 42, "")
	cursorAfterFirst := feed.cursor.Current()
	second := feed.PublishMailAckRequired("/proj", "Sender", "RedStone", "subject", 42, "")

	if second.Cursor != first.Cursor || second.Type != EventTypeMailReceived {
		t.Fatalf("second = %+v, want the first event", second)
	}
	ticker.pass(t)
	if got := delivered.Load(); got != 1 {
		t.Fatalf("delivered %d times, want 1", got)
	}
	if got := feed.cursor.Current(); got != cursorAfterFirst {
		t.Fatalf("cursor counter moved from %d to %d", cursorAfterFirst, got)
	}

	repeat := first
	repeat.Cursor = 0
	if _, created := feed.AppendAddressed(repeat); created {
		t.Fatal("a repeat reported created")
	}
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if got := feed.Stats(); got.Count != 1 {
		t.Fatalf("stats.Count = %d, want 1", got.Count)
	}
}

func TestAppendAddressed_UnaddressedAppendsNormally(t *testing.T) {
	feed := newRecipientFeed(t)
	for i := 0; i < 2; i++ {
		if _, created := feed.AppendAddressed(testAttentionEvent("plain")); !created {
			t.Fatal("an unaddressed event should always be appended")
		}
	}
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "plain", "plain")
}

func TestAppendAddressed_FoundDuringOutageKeepsOutage(t *testing.T) {
	captureSlog(t)
	store := &faultInjectingStore{Store: openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))}
	clock := newTestClock()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	feed.PublishMailPending("/proj", "Sender", "RedStone", "recorded", 42, "")
	store.fail.Store(true)
	feed.Append(testAttentionEvent("fails"))
	clock.Advance(AttentionStoreRetryInterval)

	// The store can still be read, and the message is already there.
	feed.PublishMailPending("/proj", "Sender", "RedStone", "recorded", 42, "")
	if feed.StoreHealth().Durable {
		t.Fatal("finding an existing row cleared the outage, but no write succeeded")
	}
}

func TestAppendAddressed_ManyGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	// A store-backed feed delivers through its poll, so the test ticks it by
	// hand before counting deliveries.
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	var delivered atomic.Int32
	unsubscribe := feed.Subscribe(func(AttentionEvent) { delivered.Add(1) })
	defer unsubscribe()

	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := addressMailEvent(testAttentionEvent("race"), "/proj", "RedStone", 42)
			if _, ok := feed.AppendAddressed(event); ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	ticker.pass(t)

	if created.Load() != 1 || delivered.Load() != 1 {
		t.Fatalf("created %d, delivered %d, want 1 and 1", created.Load(), delivered.Load())
	}
	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, _, err := reader.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("stored events = %+v", events)
	}
}

func TestAppendAddressed_OutageThenRecovery(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	// A store-backed feed delivers through its poll, so the test ticks it by
	// hand before counting deliveries.
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker, WithAttentionClock(clock.Now))
	var delivered atomic.Int32
	unsubscribe := feed.Subscribe(func(AttentionEvent) { delivered.Add(1) })
	defer unsubscribe()

	store.fail.Store(true)
	for i := 0; i < 3; i++ {
		feed.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")
		feed.PublishMailPending("/proj", "Sender", "RedStone", "also held", 43, "")
	}
	if got := store.addressedAttempts.Load(); got != 1 {
		t.Fatalf("write attempts during the outage = %d, want 1 per retry interval", got)
	}
	events, _, err := feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("during the outage: %+v", events)
	}

	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	feed.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")
	feed.PublishMailPending("/proj", "Sender", "RedStone", "also held", 43, "")

	events, _, err = feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("after recovery: %+v", events)
	}
	ticker.pass(t)
	if got := delivered.Load(); got != 2 {
		t.Fatalf("delivered %d times, want 2, one per message", got)
	}
	if !feed.StoreHealth().Durable {
		t.Fatalf("health = %+v", feed.StoreHealth())
	}

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	saved, _, err := reader.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("another process sees %+v, want both held messages", saved)
	}
}

func TestAppendAddressed_OtherProcessSavedItFirst(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	store.fail.Store(true)
	feed.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")
	other := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(other.Stop)
	saved := other.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")

	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	got := feed.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")
	if got.Cursor != saved.Cursor {
		t.Fatalf("got cursor %d, want the other process's %d", got.Cursor, saved.Cursor)
	}
	events, _, err := feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := cursorsOf(events); !slices.Equal(got, []int64{saved.Cursor}) {
		t.Fatalf("replay cursors = %v, want only %d", got, saved.Cursor)
	}
}

func TestAppendAddressed_NoStoreRecordsOnce(t *testing.T) {
	feed := newTestAttentionFeed(t)
	feed.PublishMailPending("/proj", "Sender", "RedStone", "once", 42, "")
	feed.PublishMailPending("/proj", "Sender", "RedStone", "once", 42, "")
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
}

func TestSetAttentionFeed_ReplacesOpenerFeed(t *testing.T) {
	resetGlobalFeedForTest(t)
	var calls atomic.Int32
	SetAttentionStoreOpener(func() (AttentionStore, error) {
		calls.Add(1)
		return newStubAttentionStore(), nil
	})

	custom := newTestAttentionFeed(t)
	SetAttentionFeed(custom)

	if GetAttentionFeed() != custom {
		t.Fatal("GetAttentionFeed should return the feed set explicitly")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("opener ran %d times, want 0 once a feed is set", got)
	}
}

// fakePollTicker is a poll ticker the test ticks by hand. It records how many
// tickers were made, their interval, and how many were stopped.
type fakePollTicker struct {
	ch       chan time.Time
	interval atomic.Int64
	made     atomic.Int32
	stopped  atomic.Int32
}

func newFakePollTicker() *fakePollTicker {
	return &fakePollTicker{ch: make(chan time.Time)}
}

func (f *fakePollTicker) factory() func(time.Duration) AttentionPollTicker {
	return func(d time.Duration) AttentionPollTicker {
		f.made.Add(1)
		f.interval.Store(int64(d))
		return f
	}
}

func (f *fakePollTicker) C() <-chan time.Time { return f.ch }
func (f *fakePollTicker) Stop()               { f.stopped.Add(1) }

// tick hands the poll loop one tick, and fails the test if the loop doesn't take
// it within 2 seconds.
func (f *fakePollTicker) tick(t *testing.T) {
	t.Helper()
	select {
	case f.ch <- time.Now():
	case <-time.After(2 * time.Second):
		t.Fatal("the poll loop didn't take a tick within 2s")
	}
}

// tryTick hands the poll loop a tick only if it is waiting for one.
func (f *fakePollTicker) tryTick() {
	select {
	case f.ch <- time.Now():
	default:
	}
}

// pass ticks twice. The loop takes the second tick only after the pass the first
// one started has finished, so every event written before the call has been read
// and delivered when pass returns.
func (f *fakePollTicker) pass(t *testing.T) {
	t.Helper()
	f.tick(t)
	f.tick(t)
}

// newPollTestFeed builds a store-backed feed with heartbeats off and a fake ticker.
func newPollTestFeed(t *testing.T, store AttentionStore, ticker *fakePollTicker, opts ...AttentionFeedOption) *AttentionFeed {
	t.Helper()
	opts = append([]AttentionFeedOption{WithAttentionStore(store), WithAttentionPollTicker(ticker.factory())}, opts...)
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour}, opts...)
	t.Cleanup(feed.Stop)
	return feed
}

// newWriterFeed is a feed on its own connection to path, standing in for
// another process.
func newWriterFeed(t *testing.T, path string) *AttentionFeed {
	t.Helper()
	writer := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
		WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	return writer
}

// eventSink collects the events one subscription receives.
type eventSink struct {
	ch          chan AttentionEvent
	unsubscribe func()
}

func collect(feed *AttentionFeed) *eventSink {
	sink := &eventSink{ch: make(chan AttentionEvent, 2000)}
	sink.unsubscribe = feed.Subscribe(sink.handler)
	return sink
}

func (s *eventSink) handler(event AttentionEvent) { s.ch <- event }

// waitFor returns the next n events, and fails the test at the deadline.
func (s *eventSink) waitFor(t *testing.T, n int, within time.Duration) []AttentionEvent {
	t.Helper()
	got := make([]AttentionEvent, 0, n)
	timeout := time.After(within)
	for len(got) < n {
		select {
		case event := <-s.ch:
			got = append(got, event)
		case <-timeout:
			t.Fatalf("got %d events within %s, want %d: %+v", len(got), within, n, got)
		}
	}
	return got
}

// drain returns the events already received.
func (s *eventSink) drain() []AttentionEvent {
	got := []AttentionEvent{}
	for {
		select {
		case event := <-s.ch:
			got = append(got, event)
		default:
			return got
		}
	}
}

// countingReadStore is a real store that counts the reads a poll makes.
type countingReadStore struct {
	*state.Store
	windowReads atomic.Int32
	sinceReads  atomic.Int32
}

func (s *countingReadStore) GetAttentionReplayWindow() (state.AttentionReplayWindow, error) {
	s.windowReads.Add(1)
	return s.Store.GetAttentionReplayWindow()
}

func (s *countingReadStore) GetAttentionEventsSince(since int64, limit int) ([]state.StoredAttentionEvent, error) {
	s.sinceReads.Add(1)
	return s.Store.GetAttentionEventsSince(since, limit)
}

func (s *countingReadStore) reads() int32 {
	return s.windowReads.Load() + s.sinceReads.Load()
}

func assertIncreasing(t *testing.T, events []AttentionEvent) {
	t.Helper()
	for i := 1; i < len(events); i++ {
		if events[i].Cursor <= events[i-1].Cursor {
			t.Fatalf("cursors not increasing: %v", cursorsOf(events))
		}
	}
}

// Behavior 6: a store-backed feed reads the store only while it has subscribers.
func TestAttentionFeed_PollsOnlyWithSubscribers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store := &countingReadStore{Store: openStoreFile(t, path)}
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker)
	writer := newWriterFeed(t, path)

	writer.Append(testAttentionEvent("before anyone listens"))
	if made, reads := ticker.made.Load(), store.reads(); made != 0 || reads != 0 {
		t.Fatalf("polled with no subscribers: tickers %d, reads %d", made, reads)
	}

	sink := collect(feed)
	second := collect(feed)
	if AttentionPollInterval != time.Second {
		t.Fatalf("AttentionPollInterval = %s, want 1s, the --attention-poll default", AttentionPollInterval)
	}
	if got := time.Duration(ticker.interval.Load()); got != AttentionPollInterval {
		t.Fatalf("ticker interval = %s, want %s", got, AttentionPollInterval)
	}
	writer.Append(testAttentionEvent("after"))
	ticker.pass(t)
	got := sink.waitFor(t, 1, 2*time.Second)
	assertSummaries(t, got, "after")
	if extra := sink.drain(); len(extra) != 0 {
		t.Fatalf("extra events: %+v", extra)
	}

	// One subscriber leaving keeps the poll going for the other.
	second.unsubscribe()
	writer.Append(testAttentionEvent("one left"))
	ticker.pass(t)
	assertSummaries(t, sink.waitFor(t, 1, 2*time.Second), "one left")

	sink.unsubscribe()
	ticker.pass(t)
	before := store.reads()
	ticker.pass(t)
	if store.reads() != before {
		t.Fatal("read the store with no subscribers")
	}

	// A new subscriber starts at "now" and doesn't get what was written meanwhile.
	writer.Append(testAttentionEvent("while nobody listened"))
	late := collect(feed)
	writer.Append(testAttentionEvent("for the late subscriber"))
	ticker.pass(t)
	assertSummaries(t, late.waitFor(t, 1, 2*time.Second), "for the late subscriber")
	if ticker.made.Load() != 1 {
		t.Fatalf("tickers made = %d, want 1", ticker.made.Load())
	}
}

// Behavior 3: the feed's own appends reach its subscriber once, woken by the
// append itself, with no tick.
func TestAttentionFeed_OwnAppendDeliveredOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	sink := collect(feed)

	for i := 0; i < 3; i++ {
		feed.Append(testAttentionEvent(fmt.Sprintf("own %d", i)))
	}
	feed.PublishMailPending("/proj", "RedStone", "BlueLake", "hello", 42, "")
	feed.PublishMailPending("/proj", "RedStone", "BlueLake", "hello", 42, "")

	got := sink.waitFor(t, 4, 2*time.Second)
	ticker.pass(t)
	if extra := sink.drain(); len(extra) != 0 {
		t.Fatalf("extra deliveries: %+v", extra)
	}
	assertIncreasing(t, got)

	reader := newWriterFeed(t, path)
	replayed, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cursorsOf(got), cursorsOf(replayed)) {
		t.Fatalf("delivered %v, replayed %v", cursorsOf(got), cursorsOf(replayed))
	}
}

// Behavior 3 edge case: a handler's follow-on append arrives after the event
// that caused it.
func TestAttentionFeed_StoreBackedFollowOnKeepsOrder(t *testing.T) {
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, filepath.Join(t.TempDir(), "state.db")), ticker)
	got := make(chan AttentionEvent, 10)
	feed.Subscribe(func(event AttentionEvent) {
		got <- event
		if event.Summary == "first" {
			feed.Append(testAttentionEvent("follow-on"))
		}
	})
	feed.Append(testAttentionEvent("first"))
	feed.Append(testAttentionEvent("second"))
	var summaries []string
	for len(summaries) < 3 {
		select {
		case event := <-got:
			summaries = append(summaries, event.Summary)
		case <-time.After(2 * time.Second):
			t.Fatalf("got %v, want 3 events", summaries)
		}
	}
	if !slices.Equal(summaries, []string{"first", "second", "follow-on"}) {
		t.Fatalf("order = %v, want first, second, follow-on (cursor order)", summaries)
	}
}

// Behavior 14: a feed with no store delivers before Append returns, and never polls.
func TestAttentionFeed_MemoryFeedDeliversDirectly(t *testing.T) {
	ticker := newFakePollTicker()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 10, RetentionPeriod: time.Hour},
		WithAttentionPollTicker(ticker.factory()))
	t.Cleanup(feed.Stop)
	var got atomic.Int32
	feed.Subscribe(func(AttentionEvent) { got.Add(1) })
	feed.Append(testAttentionEvent("direct"))
	if got.Load() != 1 || ticker.made.Load() != 0 {
		t.Fatalf("delivered %d, tickers %d; want 1 and 0", got.Load(), ticker.made.Load())
	}
}

// Behavior 13: in a store-backed feed, a heartbeat carries the newest cursor
// instead of taking the one the next stored event will get.
func TestAttentionFeed_HeartbeatKeepsStoredCursorFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	writer := newWriterFeed(t, path)
	for i := 0; i < 3; i++ {
		writer.Append(testAttentionEvent("seed"))
	}
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	sink := collect(feed)

	heartbeat := feed.PublishEphemeral(AttentionEvent{
		Category: EventCategorySystem,
		Type:     EventType(DefaultTransportLiveness.HeartbeatType),
	})
	if newest := feed.Stats().NewestCursor; heartbeat.Cursor != 3 || newest != 3 {
		t.Fatalf("heartbeat cursor %d, newest %d; want 3 and 3", heartbeat.Cursor, newest)
	}
	stored := writer.Append(testAttentionEvent("after heartbeat"))
	if stored.Cursor != 4 {
		t.Fatalf("stored cursor = %d, want 4", stored.Cursor)
	}
	ticker.pass(t)
	var delivered []AttentionEvent
	for _, event := range sink.drain() {
		if event.Type == EventTypeSystemHealthChange {
			delivered = append(delivered, event)
		}
	}
	if len(delivered) != 1 || delivered[0].Cursor != stored.Cursor {
		t.Fatalf("delivered %+v, want cursor %d once", delivered, stored.Cursor)
	}
}

// Behavior 12: during a store write outage, this process's events reach the
// subscriber in replay order, and their stored copies aren't delivered again
// after the store recovers.
func TestAttentionFeed_OutageEventsDeliveredOnce(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker, WithAttentionClock(clock.Now))
	sink := collect(feed)
	writer := newWriterFeed(t, path)

	store.fail.Store(true)
	feed.Append(testAttentionEvent("held 1"))
	feed.PublishMailPending("/proj", "S", "RedStone", "m42", 42, "")
	writer.Append(testAttentionEvent("other process"))
	feed.Append(testAttentionEvent("held 2"))
	feed.PublishMailPending("/proj", "S", "RedStone", "m43", 43, "")
	ticker.pass(t)
	got := sink.waitFor(t, 5, 2*time.Second)
	want, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cursorsOf(got), cursorsOf(want)) {
		t.Fatalf("delivered %v, replay order %v", cursorsOf(got), cursorsOf(want))
	}

	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	feed.PublishMailPending("/proj", "S", "RedStone", "m42", 42, "")
	feed.PublishMailPending("/proj", "S", "RedStone", "m43", 43, "")
	ticker.pass(t)
	if extra := sink.drain(); len(extra) != 0 {
		t.Fatalf("redelivered after recovery: %+v", extra)
	}
}

// Behavior 12 edge case: after the poll has delivered other processes' events,
// an event this process keeps in memory during an outage gets a cursor above
// them, so the poll still delivers it.
func TestAttentionFeed_OutageEventAfterOtherWritersDelivered(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker)
	sink := collect(feed)
	writer := newWriterFeed(t, path)
	for i := 0; i < 5; i++ {
		writer.Append(testAttentionEvent("other process"))
	}
	ticker.pass(t)
	sink.waitFor(t, 5, 2*time.Second)

	store.fail.Store(true)
	held := feed.Append(testAttentionEvent("held"))
	ticker.pass(t)
	if held.Cursor <= 5 {
		t.Fatalf("held cursor = %d, want above the delivered 5", held.Cursor)
	}
	assertSummaries(t, sink.waitFor(t, 1, 2*time.Second), "held")
}

// Behavior 12 edge case: a memory-only notice the poll never delivered before
// the store recovered is delivered once, from its stored copy.
func TestAttentionFeed_OutageNoticeNotYetDeliveredArrivesOnce(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker, WithAttentionClock(clock.Now))
	got := make(chan AttentionEvent, 10)
	release := make(chan struct{})
	var once sync.Once
	feed.Subscribe(func(event AttentionEvent) {
		once.Do(func() { <-release }) // hold the poll so it can't see the memory copy
		got <- event
	})

	feed.Append(testAttentionEvent("blocks the poll")) // stored; the poll blocks delivering it
	time.Sleep(50 * time.Millisecond)
	store.fail.Store(true)
	feed.PublishMailPending("/proj", "S", "RedStone", "m42", 42, "")
	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	feed.PublishMailPending("/proj", "S", "RedStone", "m42", 42, "") // saves the stored copy
	close(release)
	ticker.pass(t)

	var mail []AttentionEvent
	for len(got) > 0 {
		if event := <-got; event.MessageID == 42 {
			mail = append(mail, event)
		}
	}
	if len(mail) != 1 {
		t.Fatalf("message 42 delivered %d times, want 1: %+v", len(mail), mail)
	}
}

// Behavior 9: a failed read is logged once, and the next good read delivers
// from where the poll stopped.
func TestAttentionFeed_PollReadFailureRetries(t *testing.T) {
	logs := captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker)
	sink := collect(feed)
	writer := newWriterFeed(t, path)

	store.failReads.Store(true)
	writer.Append(testAttentionEvent("written during the failure"))
	ticker.pass(t)
	ticker.pass(t)
	if got := sink.drain(); len(got) != 0 {
		t.Fatalf("delivered during the failure: %+v", got)
	}
	if n := logs.count(attentionPollFailedLogMessage); n != 1 {
		t.Fatalf("failure logged %d times, want 1\n%s", n, logs.text())
	}

	store.failReads.Store(false)
	ticker.pass(t)
	assertSummaries(t, sink.waitFor(t, 1, 2*time.Second), "written during the failure")
	if n := logs.count(attentionPollRecoveredLogMessage); n != 1 {
		t.Fatalf("recovery logged %d times, want 1", n)
	}
}

// Behavior 10: when the poll's cursor has left retention, it goes on from the
// oldest kept event and logs both cursors.
func TestAttentionFeed_PollContinuesAfterExpiredCursor(t *testing.T) {
	logs := captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	writer := newWriterFeed(t, path)
	// The window starts at the oldest event that hasn't expired, so the poll's
	// start event must expire too for its cursor to leave the window.
	appendAged(writer, "start", 2*time.Hour)
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	sink := collect(feed)

	for i := 0; i < 3; i++ {
		appendAged(writer, fmt.Sprintf("old %d", i), 2*time.Hour)
	}
	if _, err := openStoreFile(t, path).GCExpiredEvents(); err != nil {
		t.Fatal(err)
	}
	writer.Append(testAttentionEvent("new 1"))
	writer.Append(testAttentionEvent("new 2"))

	ticker.pass(t)
	got := sink.waitFor(t, 2, 2*time.Second)
	if got[0].Cursor != 5 || got[1].Cursor != 6 {
		t.Fatalf("got cursors %v, want 5 and 6", cursorsOf(got))
	}
	line := logs.text()
	if logs.count(attentionPollExpiredLogMessage) != 1 ||
		!strings.Contains(line, "since_cursor=1") || !strings.Contains(line, "oldest_cursor=5") {
		t.Fatalf("expiry not reported with both cursors:\n%s", line)
	}
}

// Behavior 11: a handler that blocks loses nothing, and more than one batch
// arrives in full.
func TestAttentionFeed_SlowHandlerLosesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	writer := newWriterFeed(t, path)
	release := make(chan struct{})
	var once sync.Once
	got := make(chan AttentionEvent, 1000)
	feed.Subscribe(func(event AttentionEvent) {
		once.Do(func() { <-release })
		got <- event
	})

	writer.Append(testAttentionEvent("first"))
	ticker.tick(t)
	for i := 0; i < 5; i++ {
		writer.Append(testAttentionEvent(fmt.Sprintf("while blocked %d", i)))
		ticker.tryTick()
	}
	close(release)
	ticker.pass(t)
	if len(got) != 6 {
		t.Fatalf("got %d events, want 6", len(got))
	}

	for i := 0; i < attentionPollBatch+100; i++ {
		writer.Append(testAttentionEvent("bulk"))
	}
	ticker.pass(t)
	all := make([]AttentionEvent, 0, 6+attentionPollBatch+100)
	for len(got) > 0 {
		all = append(all, <-got)
	}
	if len(all) != 6+attentionPollBatch+100 {
		t.Fatalf("got %d events, want %d", len(all), 6+attentionPollBatch+100)
	}
	assertIncreasing(t, all)
}

// Behavior 8: Stop ends the poll, stops its ticker and leaves no goroutine.
func TestAttentionFeed_StopEndsPoll(t *testing.T) {
	store := openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))
	baseline := runtime.NumGoroutine() // after the store, which has goroutines of its own
	ticker := newFakePollTicker()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(store), WithAttentionPollTicker(ticker.factory()))
	collect(feed)

	assertReturnsWithin(t, time.Second, feed.Stop)
	feed.Stop() // A second Stop is safe.
	if n := ticker.stopped.Load(); n != 1 {
		t.Fatalf("ticker stopped %d times, want 1", n)
	}
	waitForGoroutines(t, baseline, time.Second)
	collect(feed)
	if ticker.made.Load() != 1 {
		t.Fatal("Subscribe after Stop started a poll")
	}
}

// Behavior 7: a short command's calls never start the poll, whether the store
// opens or not, and Stop returns at once.
func TestAttentionFeed_ShortCommandNeverPolls(t *testing.T) {
	for _, openerFails := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("state-%v.db", openerFails))
		ticker := newFakePollTicker()
		feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
			WithAttentionStoreOpener(func() (AttentionStore, error) {
				if openerFails {
					return nil, errors.New("injected open failure")
				}
				return openStoreFile(t, path), nil
			}),
			WithAttentionPollTicker(ticker.factory()))

		feed.Append(testAttentionEvent("short"))
		feed.PublishMailPending("/proj", "RedStone", "BlueLake", "hi", 7, "")
		_, _, _ = feed.Replay(0, 10)
		_, _, _ = feed.ReplayForRecipient("BlueLake", 0, 10)
		_ = feed.Stats()

		if ticker.made.Load() != 0 {
			t.Fatalf("opener fails %v: the poll started without a subscriber", openerFails)
		}
		assertReturnsWithin(t, 100*time.Millisecond, feed.Stop)
	}
}

// Behavior 4 property: while two other writers append at once, the subscriber
// gets every stored cursor once, in increasing order, however the poll's passes
// fall between commits. This is also the check that SQLite commits cursors in
// order: a cursor seen after a higher one would break the order.
func TestAttentionFeed_ParallelWritersDeliveredInOrder(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("state-%d.db", time.Now().UnixNano()))
		ticker := newFakePollTicker()
		feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
			WithAttentionStore(openStoreFile(t, path)), WithAttentionPollTicker(ticker.factory()))
		defer feed.Stop()
		sink := collect(feed)
		counts := []int{rapid.IntRange(1, 50).Draw(rt, "a"), rapid.IntRange(1, 50).Draw(rt, "b")}
		pauses := rapid.SliceOfN(rapid.IntRange(0, 2000), 1, 20).Draw(rt, "pauses_us")

		var wg sync.WaitGroup
		for w, count := range counts {
			writer := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
				WithAttentionStore(openStoreFile(t, path)))
			defer writer.Stop()
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < count; i++ {
					writer.Append(testAttentionEvent(fmt.Sprintf("writer %d event %d", w, i)))
				}
			}()
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		for i := 0; ; i++ {
			select {
			case <-done:
			default:
				ticker.tryTick()
				time.Sleep(time.Duration(pauses[i%len(pauses)]) * time.Microsecond)
				continue
			}
			break
		}
		ticker.pass(t)

		got := sink.drain()
		stored, _, err := feed.Replay(0, 1000)
		if err != nil {
			rt.Fatal(err)
		}
		for i := 1; i < len(got); i++ {
			if got[i].Cursor <= got[i-1].Cursor {
				rt.Fatalf("delivered out of order: %v", cursorsOf(got))
			}
		}
		if !slices.Equal(cursorsOf(got), cursorsOf(stored)) {
			rt.Fatalf("delivered %v, stored %v", cursorsOf(got), cursorsOf(stored))
		}
	})
}

func assertReturnsWithin(t *testing.T, limit time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("didn't return within %s", limit)
	}
}

// waitForGoroutines waits for the goroutine count to fall back to baseline.
func waitForGoroutines(t *testing.T, baseline int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, want at most %d", runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// appendStep is one generated append: by the subscribed feed or the other
// writer, a plain event or a mail notice.
type appendStep struct {
	other     bool
	addressed bool
	messageID int
}

// Behavior 3 property: whatever mix of own and other appends, the subscriber
// gets every stored event after it subscribed, once, in cursor order.
func TestAttentionFeed_OwnAppendProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("state-%d.db", time.Now().UnixNano()))
		ticker := newFakePollTicker()
		feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
			WithAttentionStore(openStoreFile(t, path)), WithAttentionPollTicker(ticker.factory()))
		defer feed.Stop()
		other := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
			WithAttentionStore(openStoreFile(t, path)))
		defer other.Stop()
		other.Append(testAttentionEvent("before subscribe"))
		sink := collect(feed)

		steps := rapid.SliceOfN(rapid.Custom(func(rt *rapid.T) appendStep {
			return appendStep{
				other:     rapid.Bool().Draw(rt, "other"),
				addressed: rapid.Bool().Draw(rt, "addressed"),
				messageID: rapid.IntRange(1, 5).Draw(rt, "message"),
			}
		}), 1, 40).Draw(rt, "steps")
		for i, step := range steps {
			writer := feed
			if step.other {
				writer = other
			}
			if step.addressed {
				writer.PublishMailPending("/proj", "S", "BlueLake", "m", step.messageID, "")
			} else {
				writer.Append(testAttentionEvent(fmt.Sprintf("step %d", i)))
			}
		}
		ticker.pass(t)

		stored, _, err := other.Replay(1, 1000)
		if err != nil {
			rt.Fatal(err)
		}
		got := sink.drain()
		if !slices.Equal(cursorsOf(got), cursorsOf(stored)) {
			rt.Fatalf("delivered %v, stored after subscribe %v", cursorsOf(got), cursorsOf(stored))
		}
		seen := map[int64]bool{}
		for _, event := range got {
			if event.MessageID != 0 && seen[event.MessageID] {
				rt.Fatalf("message %d delivered twice", event.MessageID)
			}
			seen[event.MessageID] = event.MessageID != 0
		}
	})
}
