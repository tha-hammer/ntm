package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewTimelineLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}

	if lifecycle.GetTracker() != tracker {
		t.Error("GetTracker returned wrong tracker")
	}

	if lifecycle.GetPersister() != persister {
		t.Error("GetPersister returned wrong persister")
	}

	t.Log("PASS: NewTimelineLifecycle creates lifecycle manager correctly")
}

func TestTimelineLifecycleStartSession(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{
		BaseDir:            tmpDir,
		CheckpointInterval: 100 * time.Millisecond, // Short interval for testing
	}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer lifecycle.Stop()

	sessionID := "test-session"

	// Start session
	lifecycle.StartSession(sessionID)

	if !lifecycle.IsSessionActive(sessionID) {
		t.Error("Session should be active after StartSession")
	}

	sessions := lifecycle.ActiveSessions()
	if len(sessions) != 1 || sessions[0] != sessionID {
		t.Errorf("Expected active sessions [%s], got %v", sessionID, sessions)
	}

	// Starting same session again should be idempotent
	lifecycle.StartSession(sessionID)
	sessions = lifecycle.ActiveSessions()
	if len(sessions) != 1 {
		t.Errorf("Expected 1 active session after duplicate start, got %d", len(sessions))
	}

	t.Log("PASS: StartSession activates timeline tracking")
}

func TestTimelineLifecycleStartSession_IgnoresInvalidSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer lifecycle.Stop()

	lifecycle.StartSession("")
	lifecycle.StartSession("   ")
	lifecycle.StartSession("../escape")

	if len(lifecycle.ActiveSessions()) != 0 {
		t.Fatalf("expected no active sessions for invalid input, got %v", lifecycle.ActiveSessions())
	}
}

func TestTimelineLifecycleEndSession(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer lifecycle.Stop()

	sessionID := "end-test-session"

	// Record some events
	tracker.RecordEvent(AgentEvent{
		AgentID:   "cc_1",
		SessionID: sessionID,
		State:     TimelineWorking,
		Timestamp: time.Now(),
	})
	tracker.RecordEvent(AgentEvent{
		AgentID:   "cc_1",
		SessionID: sessionID,
		State:     TimelineIdle,
		Timestamp: time.Now().Add(time.Second),
	})

	// Start and then end session
	lifecycle.StartSession(sessionID)
	err = lifecycle.EndSession(sessionID)
	if err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	if lifecycle.IsSessionActive(sessionID) {
		t.Error("Session should not be active after EndSession")
	}

	// Verify timeline was saved
	path := filepath.Join(tmpDir, sessionID+".jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("Timeline file should exist after EndSession")
	}

	// Verify events were persisted
	events, err := persister.LoadTimeline(sessionID)
	if err != nil {
		t.Fatalf("LoadTimeline failed: %v", err)
	}

	if len(events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(events))
	}

	t.Log("PASS: EndSession finalizes and persists timeline")
}

func TestTimelineLifecycleEndSession_RejectsInvalidSessionID(t *testing.T) {
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer lifecycle.Stop()

	if err := lifecycle.EndSession("   "); err == nil {
		t.Fatal("expected EndSession to reject blank session IDs")
	}
	if err := lifecycle.EndSession("../escape"); err == nil {
		t.Fatal("expected EndSession to reject path-like session IDs")
	}
}

func TestTimelineLifecycleMultipleSessions(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}
	defer lifecycle.Stop()

	// Start multiple sessions
	sessions := []string{"session-a", "session-b", "session-c"}
	for _, s := range sessions {
		lifecycle.StartSession(s)
	}

	activeSessions := lifecycle.ActiveSessions()
	if len(activeSessions) != 3 {
		t.Errorf("Expected 3 active sessions, got %d", len(activeSessions))
	}

	// End one session
	err = lifecycle.EndSession("session-b")
	if err != nil {
		t.Fatalf("EndSession failed: %v", err)
	}

	activeSessions = lifecycle.ActiveSessions()
	if len(activeSessions) != 2 {
		t.Errorf("Expected 2 active sessions after ending one, got %d", len(activeSessions))
	}

	t.Log("PASS: Multiple sessions can be tracked concurrently")
}

func TestTimelineLifecycleStop(t *testing.T) {
	tmpDir := t.TempDir()
	config := &TimelinePersistConfig{BaseDir: tmpDir}

	persister, err := NewTimelinePersister(config)
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)

	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}

	// Start sessions
	lifecycle.StartSession("session-1")
	lifecycle.StartSession("session-2")

	// Add events
	for _, s := range []string{"session-1", "session-2"} {
		tracker.RecordEvent(AgentEvent{
			AgentID:   "cc_1",
			SessionID: s,
			State:     TimelineWorking,
			Timestamp: time.Now(),
		})
	}

	// Stop should finalize all sessions
	lifecycle.Stop()

	// Verify all timelines were saved
	for _, s := range []string{"session-1", "session-2"} {
		events, err := persister.LoadTimeline(s)
		if err != nil {
			t.Errorf("LoadTimeline for %s failed: %v", s, err)
		}
		if len(events) == 0 {
			t.Errorf("Expected events for %s to be persisted", s)
		}
	}

	t.Log("PASS: Stop finalizes all active sessions")
}

func TestTimelineLifecycle_StartSessionDoesNotRacePastStop(t *testing.T) {
	tmpDir := t.TempDir()
	persister, err := NewTimelinePersister(&TimelinePersistConfig{BaseDir: tmpDir})
	if err != nil {
		t.Fatalf("NewTimelinePersister failed: %v", err)
	}

	tracker := NewTimelineTracker(nil)
	lifecycle, err := NewTimelineLifecycle(tracker, persister)
	if err != nil {
		t.Fatalf("NewTimelineLifecycle failed: %v", err)
	}

	oldRunner := &checkpointRunner{
		ticker: time.NewTicker(time.Hour),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	t.Cleanup(func() {
		oldRunner.ticker.Stop()
		select {
		case <-oldRunner.done:
		default:
			close(oldRunner.done)
		}
	})

	lifecycle.mu.Lock()
	lifecycle.activeSessions["old-session"] = struct{}{}
	lifecycle.mu.Unlock()
	persister.mu.Lock()
	persister.checkpoints["old-session"] = oldRunner
	persister.mu.Unlock()

	stopped := make(chan struct{})
	go func() {
		lifecycle.Stop()
		close(stopped)
	}()

	select {
	case <-oldRunner.stop:
	case <-time.After(time.Second):
		t.Fatal("Stop did not begin draining the old checkpoint runner")
	}

	started := make(chan struct{})
	go func() {
		lifecycle.StartSession("new-session")
		close(started)
	}()

	select {
	case <-started:
		t.Fatal("StartSession returned before Stop completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(oldRunner.done)

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("StartSession did not unblock after Stop finished")
	}

	if lifecycle.IsSessionActive("new-session") {
		t.Fatal("new-session should not become active after lifecycle stop")
	}
	if len(lifecycle.ActiveSessions()) != 0 {
		t.Fatalf("expected no active sessions after Stop, got %v", lifecycle.ActiveSessions())
	}
}

func TestStartSessionTimeline(t *testing.T) {
	// This tests the convenience function
	// Note: This uses the global lifecycle, so we need to be careful about state.
	// bd-ev740: scope HOME to a temp dir and clear ambient
	// XDG_CONFIG_HOME / NTM_CONFIG so the global lifecycle's lazy
	// initialization cannot route through an outer-shell-injected
	// invalid config path (e.g. /nonexistent/config.toml).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetGlobalTimelineLifecycleForTest()

	sessionID := "convenience-test-" + time.Now().Format("20060102150405")

	err := StartSessionTimeline(sessionID)
	if err != nil {
		t.Fatalf("StartSessionTimeline failed: %v", err)
	}

	lifecycle, err := GetGlobalTimelineLifecycle()
	if err != nil {
		t.Fatalf("GetGlobalTimelineLifecycle failed: %v", err)
	}

	if !lifecycle.IsSessionActive(sessionID) {
		t.Error("Session should be active after StartSessionTimeline")
	}

	// Clean up
	_ = EndSessionTimeline(sessionID)

	t.Log("PASS: StartSessionTimeline convenience function works")
}

func TestStartSessionTimeline_RejectsInvalidSessionID(t *testing.T) {
	if err := StartSessionTimeline("   "); err == nil {
		t.Fatal("expected StartSessionTimeline to reject blank session IDs")
	}
	if err := StartSessionTimeline("../escape"); err == nil {
		t.Fatal("expected StartSessionTimeline to reject path-like session IDs")
	}
}

func TestEndSessionTimeline(t *testing.T) {
	// bd-ev740: hermetic HOME/XDG/NTM_CONFIG isolation (see
	// TestStartSessionTimeline comment).
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("NTM_CONFIG", "")
	resetGlobalTimelineLifecycleForTest()

	sessionID := "end-convenience-test-" + time.Now().Format("20060102150405")

	// Start first
	err := StartSessionTimeline(sessionID)
	if err != nil {
		t.Fatalf("StartSessionTimeline failed: %v", err)
	}

	// End session
	err = EndSessionTimeline(sessionID)
	if err != nil {
		t.Fatalf("EndSessionTimeline failed: %v", err)
	}

	lifecycle, err := GetGlobalTimelineLifecycle()
	if err != nil {
		t.Fatalf("GetGlobalTimelineLifecycle failed: %v", err)
	}

	if lifecycle.IsSessionActive(sessionID) {
		t.Error("Session should not be active after EndSessionTimeline")
	}

	t.Log("PASS: EndSessionTimeline convenience function works")
}

func TestEndSessionTimeline_RejectsInvalidSessionID(t *testing.T) {
	if err := EndSessionTimeline("   "); err == nil {
		t.Fatal("expected EndSessionTimeline to reject blank session IDs")
	}
	if err := EndSessionTimeline("../escape"); err == nil {
		t.Fatal("expected EndSessionTimeline to reject path-like session IDs")
	}
}

func TestGetGlobalTimelineTracker(t *testing.T) {
	tracker := GetGlobalTimelineTracker()
	if tracker == nil {
		t.Fatal("GetGlobalTimelineTracker returned nil")
	}

	// Should return the same instance
	tracker2 := GetGlobalTimelineTracker()
	if tracker != tracker2 {
		t.Error("GetGlobalTimelineTracker should return singleton")
	}

	t.Log("PASS: GetGlobalTimelineTracker returns singleton")
}
