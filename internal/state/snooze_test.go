package state

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Loop 6, Behavior 2: every snooze must end in the future, checked in one
// place.
func TestValidateSnoozeUntil(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for _, until := range []time.Time{now.Add(-time.Hour), now.Add(-time.Second), now} {
		if err := ValidateSnoozeUntil(until, now); !errors.Is(err, ErrSnoozeNotInFuture) {
			t.Errorf("ValidateSnoozeUntil(%v) = %v, want ErrSnoozeNotInFuture", until, err)
		}
	}
	for _, until := range []time.Time{now.Add(time.Second), now.Add(time.Hour)} {
		if err := ValidateSnoozeUntil(until, now); err != nil {
			t.Errorf("ValidateSnoozeUntil(%v) = %v, want nil", until, err)
		}
	}
	if ErrSnoozeNotInFuture.Error() != "snooze time must be in the future" {
		t.Errorf("error text = %q", ErrSnoozeNotInFuture)
	}
}

// Behavior 2 property: a snooze time is accepted exactly when it is after now.
func TestValidateSnoozeUntil_Property(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	rapid.Check(t, func(rt *rapid.T) {
		until := now.Add(time.Duration(rapid.Int64Range(-3600, 3600).Draw(rt, "offset")) * time.Second)
		if got := ValidateSnoozeUntil(until, now) == nil; got != until.After(now) {
			rt.Fatalf("until %v: accepted %v, want %v", until, got, until.After(now))
		}
	})
}

// mailEvent stores an addressed mail notice like RecordMailNotice does, with
// the sender and subject in its details, and returns its cursor.
func mailEvent(t *testing.T, store *Store, ts time.Time, project, agent string, messageID int64, from, subject string) int64 {
	t.Helper()
	event := addressedStoredEvent(ts, project, agent, messageID)
	event.Details = fmt.Sprintf(`{"from":%q,"subject":%q,"message_id":%d}`, from, subject, messageID)
	expires := ts.Add(7 * 24 * time.Hour)
	event.ExpiresAt = &expires
	cursor, created, err := store.AppendAddressedAttentionEvent(event)
	if err != nil || !created {
		t.Fatalf("append mail event %d for %s: created %v, %v", messageID, agent, created, err)
	}
	return cursor
}

func snoozeRequest(agent string, messageID int64, until, now time.Time) SnoozeRequest {
	return SnoozeRequest{ProjectKey: "/p", AgentName: agent, MessageID: messageID, Until: until, Now: now}
}

// Loop 6, Behavior 3: a snooze is saved on one agent's event, with an audit
// record, and replaces an earlier one.
func TestSnoozeAddressedMessage(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	blue := mailEvent(t, store, now, "/p", "BlueLake", 42, "RedStone", "first")
	red := mailEvent(t, store, now, "/p", "RedStone", 42, "GreenCastle", "first")

	t1 := now.Add(30 * time.Minute)
	got, err := store.SnoozeAddressedMessage(snoozeRequest("bluelake", 42, t1, now))
	if err != nil || !got.Equal(t1) {
		t.Fatalf("snooze: %v, %v; want %v", got, err, t1)
	}
	item, err := store.GetAttentionItemStateByCursor(blue)
	if err != nil || item == nil || item.State != AttentionStateSnoozed || !item.SnoozedUntil.Equal(t1) {
		t.Fatalf("BlueLake's item = %+v, %v", item, err)
	}
	if other, err := store.GetAttentionItemStateByCursor(red); err != nil || other != nil {
		t.Fatalf("RedStone's item = %+v, %v; want none", other, err)
	}
	audit, err := store.GetAuditHistory("attention_item", item.ItemKey, 10)
	if err != nil || len(audit) != 1 || audit[0].ActorType != "agent" || audit[0].ActorID != "bluelake" ||
		audit[0].EventType != "snooze" || audit[0].Category != "attention" {
		t.Fatalf("audit = %+v, %v", audit, err)
	}

	t2 := now.Add(2 * time.Hour)
	if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, t2, now)); err != nil {
		t.Fatal(err)
	}
	if item, _ := store.GetAttentionItemStateByCursor(blue); item == nil || !item.SnoozedUntil.Equal(t2) {
		t.Fatalf("after re-snooze: %+v", item)
	}

	if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 99, t1, now)); !errors.Is(err, ErrNoAddressedEvent) {
		t.Fatalf("unknown message error = %v, want ErrNoAddressedEvent", err)
	}
	if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, now, now)); !errors.Is(err, ErrSnoozeNotInFuture) {
		t.Fatalf("past time error = %v", err)
	}
	if item, _ := store.GetAttentionItemStateByCursor(blue); item == nil || !item.SnoozedUntil.Equal(t2) {
		t.Fatalf("a rejected snooze changed the item: %+v", item)
	}
}

// Behavior 3 property: snoozing twice with the same time leaves the same state
// as snoozing once.
func TestSnoozeAddressedMessage_IdempotentProperty(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	var next int64
	rapid.Check(t, func(rt *rapid.T) {
		next++
		cursor := mailEvent(t, store, now, "/p", "BlueLake", next, "RedStone", "s")
		// A fresh notice expires in 7 days, so the latest snooze is SnoozeReturnMargin before that (Loop 7).
		until := now.Add(time.Duration(rapid.Int64Range(1, int64((SnoozeLimit-SnoozeReturnMargin)/time.Second)).Draw(rt, "seconds")) * time.Second)
		if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", next, until, now)); err != nil {
			rt.Fatal(err)
		}
		once, _ := store.GetAttentionItemStateByCursor(cursor)
		if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", next, until, now)); err != nil {
			rt.Fatal(err)
		}
		twice, _ := store.GetAttentionItemStateByCursor(cursor)
		if once.State != twice.State || !once.SnoozedUntil.Equal(*twice.SnoozedUntil) || once.ItemKey != twice.ItemKey {
			rt.Fatalf("once %+v, twice %+v", once, twice)
		}
	})
}

// setSnooze writes an item state straight to the store, so a test can seed a
// snooze that has already ended, which SnoozeAddressedMessage won't accept.
func setSnooze(t *testing.T, store *Store, cursor int64, state AttentionState, until *time.Time) {
	t.Helper()
	if err := store.UpsertAttentionItemState(&AttentionItemState{ItemKey: fmt.Sprintf("cursor:%d", cursor), State: state, SnoozedUntil: until}); err != nil {
		t.Fatal(err)
	}
}

func snoozedIDs(list []SnoozedMessage) []int64 {
	ids := []int64{}
	for _, m := range list {
		ids = append(ids, m.MessageID)
	}
	return ids
}

// Loop 6, Behavior 4: an agent's snoozes are listed as active or ended.
func TestSnoozedMessages(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	later, earlier := now.Add(time.Hour), now.Add(-time.Minute)
	setSnooze(t, store, mailEvent(t, store, now, "/p", "BlueLake", 41, "RedStone", "active one"), AttentionStateSnoozed, &later)
	setSnooze(t, store, mailEvent(t, store, now, "/p", "BlueLake", 42, "GreenCastle", "ended one"), AttentionStateSnoozed, &earlier)
	mailEvent(t, store, now, "/p", "BlueLake", 43, "RedStone", "never snoozed")
	setSnooze(t, store, mailEvent(t, store, now, "/p", "RedStone", 44, "BlueLake", "someone else's"), AttentionStateSnoozed, &later)
	setSnooze(t, store, mailEvent(t, store, now, "/p", "BlueLake", 45, "RedStone", "cleared"), AttentionStateNew, nil)
	setSnooze(t, store, mailEvent(t, store, now, "/other", "BlueLake", 46, "RedStone", "other project"), AttentionStateSnoozed, &later)
	expired := now.Add(-8 * 24 * time.Hour)
	old := addressedStoredEvent(expired, "/p", "BlueLake", 47)
	gone := now.Add(-time.Hour)
	old.ExpiresAt = &gone
	oldCursor, _, err := store.AppendAddressedAttentionEvent(old)
	if err != nil {
		t.Fatal(err)
	}
	setSnooze(t, store, oldCursor, AttentionStateSnoozed, &earlier)

	active, ended, err := store.SnoozedMessages("/p", "bluelake", now)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(snoozedIDs(active)) != "[41]" || fmt.Sprint(snoozedIDs(ended)) != "[42]" {
		t.Fatalf("active %v, ended %v; want [41] and [42]", snoozedIDs(active), snoozedIDs(ended))
	}
	if a := active[0]; a.From != "RedStone" || a.Subject != "active one" || !a.Until.Equal(later) || a.Cursor == 0 {
		t.Fatalf("active entry = %+v", a)
	}
	if e := ended[0]; e.From != "GreenCastle" || e.Subject != "ended one" || !e.Until.Equal(earlier) {
		t.Fatalf("ended entry = %+v", e)
	}
}

// Behavior 4 property: every snoozed message of the agent lands in exactly one
// list, active exactly when its time is after now, and no other agent's
// message appears.
func TestSnoozedMessages_PartitionProperty(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rapid.Check(t, func(rt *rapid.T) {
		store := testRuntimeAttentionStore(t)
		wantActive, wantEnded := map[int64]bool{}, map[int64]bool{}
		count := rapid.IntRange(0, 8).Draw(rt, "messages")
		for i := range count {
			id := int64(i + 1)
			agent := rapid.SampledFrom([]string{"BlueLake", "RedStone"}).Draw(rt, "agent")
			cursor := mailEvent(t, store, now, "/p", agent, id, "X", "s")
			if !rapid.Bool().Draw(rt, "snoozed") {
				continue
			}
			until := now.Add(time.Duration(rapid.Int64Range(-7200, 7200).Draw(rt, "offset")) * time.Second)
			setSnooze(t, store, cursor, AttentionStateSnoozed, &until)
			if agent != "BlueLake" {
				continue
			}
			if until.After(now) {
				wantActive[id] = true
			} else {
				wantEnded[id] = true
			}
		}
		active, ended, err := store.SnoozedMessages("/p", "BlueLake", now)
		if err != nil {
			rt.Fatal(err)
		}
		check := func(name string, got []SnoozedMessage, want map[int64]bool) {
			if len(got) != len(want) {
				rt.Fatalf("%s = %v, want %v", name, snoozedIDs(got), want)
			}
			for _, m := range got {
				if !want[m.MessageID] {
					rt.Fatalf("%s has %d, want %v", name, m.MessageID, want)
				}
			}
		}
		check("active", active, wantActive)
		check("ended", ended, wantEnded)
	})
}

// Loop 6, Behavior 9: a snooze can't outlast the 7 day retention.
func TestValidateSnoozeUntil_SevenDayLimit(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	if err := ValidateSnoozeUntil(now.Add(7*24*time.Hour), now); err != nil {
		t.Fatalf("exactly 7 days: %v", err)
	}
	err := ValidateSnoozeUntil(now.Add(7*24*time.Hour+time.Second), now)
	if !errors.Is(err, ErrSnoozeTooLong) || !strings.Contains(err.Error(), "at most 7 days ahead (until 2026-11-16T12:00:00Z)") {
		t.Fatalf("7 days and 1 second: %v", err)
	}
}

// Behavior 9 property: a snooze time is accepted exactly when it is after now
// and no more than 7 days ahead.
func TestValidateSnoozeUntil_LimitProperty(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	rapid.Check(t, func(rt *rapid.T) {
		until := now.Add(time.Duration(rapid.Int64Range(-24*3600, 9*24*3600).Draw(rt, "offset")) * time.Second)
		want := until.After(now) && !until.After(now.Add(7*24*time.Hour))
		if got := ValidateSnoozeUntil(until, now) == nil; got != want {
			rt.Fatalf("until %v: accepted %v, want %v", until, got, want)
		}
	})
}

// Behavior 9: a snooze can't end after its own notice expires, or the notice,
// and the snooze with it, would be gone when it ends.
func TestSnoozeAddressedMessage_EventExpiry(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	cursor := mailEvent(t, store, now.Add(-5*24*time.Hour), "/p", "BlueLake", 42, "RedStone", "old")
	expires := now.Add(2 * 24 * time.Hour)

	_, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, now.Add(3*24*time.Hour), now))
	if !errors.Is(err, ErrSnoozePastNotice) || !strings.Contains(err.Error(), "message 42's notice expires at "+expires.Format(time.RFC3339)+
		"; snooze until "+expires.Add(-SnoozeReturnMargin).Format(time.RFC3339)+" at the latest") {
		t.Fatalf("past the notice: %v", err)
	}
	if item, _ := store.GetAttentionItemStateByCursor(cursor); item != nil {
		t.Fatalf("a rejected snooze wrote state: %+v", item)
	}
	if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, now.Add(24*time.Hour), now)); err != nil {
		t.Fatalf("within the notice: %v", err)
	}
}

// Loop 7, Behavior 11: a snooze must end SnoozeReturnMargin before its notice
// expires, so the notice is still there to show the snooze ended.
func TestSnoozeAddressedMessage_ReturnMargin(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	cursor := mailEvent(t, store, now.Add(-5*24*time.Hour), "/p", "BlueLake", 42, "RedStone", "old")
	expires := now.Add(2 * 24 * time.Hour)

	_, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, expires.Add(-30*time.Minute), now))
	want := "snooze would end after its notice expires: message 42's notice expires at " + expires.Format(time.RFC3339) +
		"; snooze until " + expires.Add(-time.Hour).Format(time.RFC3339) + " at the latest"
	if !errors.Is(err, ErrSnoozePastNotice) || err.Error() != want {
		t.Fatalf("30 minutes before the notice expires: %v; want %q", err, want)
	}
	if item, _ := store.GetAttentionItemStateByCursor(cursor); item != nil {
		t.Fatalf("a rejected snooze wrote state: %+v", item)
	}
	if _, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, expires.Add(-SnoozeReturnMargin), now)); err != nil {
		t.Fatalf("at the latest allowed time: %v", err)
	}
}

// Behavior 11 property: a snooze until a time in (now, E] is accepted exactly
// when it ends at least SnoozeReturnMargin before the notice expires at E.
func TestSnoozeAddressedMessage_ReturnMarginProperty(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	mailEvent(t, store, now.Add(-5*24*time.Hour), "/p", "BlueLake", 42, "RedStone", "old")
	expires := now.Add(2 * 24 * time.Hour)
	rapid.Check(t, func(rt *rapid.T) {
		until := now.Add(time.Duration(rapid.Int64Range(1, int64(expires.Sub(now)/time.Second)).Draw(rt, "seconds")) * time.Second)
		_, err := store.SnoozeAddressedMessage(snoozeRequest("BlueLake", 42, until, now))
		want := !until.After(expires.Add(-SnoozeReturnMargin))
		if (err == nil) != want || (err != nil && !errors.Is(err, ErrSnoozePastNotice)) {
			rt.Fatalf("until %s (notice expires %s): err %v, want accepted %v", until, expires, err, want)
		}
	})
}

// endedSnoozeOf returns the agent's one ended snooze, failing otherwise.
func endedSnoozeOf(t testing.TB, store *Store, agent string, now time.Time) SnoozedMessage {
	t.Helper()
	_, ended, err := store.SnoozedMessages("/p", agent, now)
	if err != nil || len(ended) != 1 {
		t.Fatalf("ended snoozes = %+v, %v; want one", ended, err)
	}
	return ended[0]
}

func nudgeTold(agent string, id int64, at time.Time) ToldRecord {
	return ToldRecord{ProjectKey: "/p", AgentName: agent, MessageID: id, ToldBy: ToldByIdleNudge, ToldAt: at}
}

// Loop 6, Behavior 10: an ended snooze is claimed by exactly one caller,
// which clears the snooze and replaces the told record together, and a release
// puts both back.
func TestClaimEndedSnooze(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	earlier, later := now.Add(-time.Minute), now.Add(time.Hour)
	cursor := mailEvent(t, store, now.Add(-time.Hour), "/p", "BlueLake", 42, "RedStone", "come back")
	setSnooze(t, store, cursor, AttentionStateSnoozed, &earlier)
	toldEarlier := toldRecord("/p", "BlueLake", 42, now.Add(-time.Hour))
	if won, err := store.ClaimToldRecord(toldEarlier); err != nil || !won {
		t.Fatalf("seed told record: %v %v", won, err)
	}

	snooze := endedSnoozeOf(t, store, "BlueLake", now)
	claim, err := store.ClaimEndedSnooze(snooze, nudgeTold("BlueLake", 42, now))
	if err != nil || claim == nil {
		t.Fatalf("claim = %+v, %v; want a win", claim, err)
	}
	if item, _ := store.GetAttentionItemStateByCursor(cursor); item.State != AttentionStateNew || item.SnoozedUntil != nil {
		t.Fatalf("item after claim = %+v", item)
	}
	if rec, _ := store.GetToldRecord("/p", "BlueLake", 42); rec == nil || rec.ToldBy != ToldByIdleNudge || !rec.ToldAt.Equal(now) {
		t.Fatalf("told record after claim = %+v", rec)
	}
	if again, err := store.ClaimEndedSnooze(snooze, nudgeTold("BlueLake", 42, now)); err != nil || again != nil {
		t.Fatalf("second claim = %+v, %v; want none", again, err)
	}

	if err := store.ReleaseEndedSnoozeClaim(claim); err != nil {
		t.Fatal(err)
	}
	if item, _ := store.GetAttentionItemStateByCursor(cursor); item.State != AttentionStateSnoozed || !item.SnoozedUntil.Equal(earlier) {
		t.Fatalf("item after release = %+v", item)
	}
	if rec, _ := store.GetToldRecord("/p", "BlueLake", 42); rec == nil || rec.ToldBy != ToldByStopHook || !rec.ToldAt.Equal(toldEarlier.ToldAt) {
		t.Fatalf("told record after release = %+v", rec)
	}

	// An active snooze can't be claimed, and stays as it is.
	active := mailEvent(t, store, now, "/p", "BlueLake", 43, "RedStone", "not yet")
	setSnooze(t, store, active, AttentionStateSnoozed, &later)
	held, _, _ := store.SnoozedMessages("/p", "BlueLake", now)
	for _, s := range held {
		if s.MessageID != 43 {
			continue
		}
		if c, err := store.ClaimEndedSnooze(s, nudgeTold("BlueLake", 43, now)); err != nil || c != nil {
			t.Fatalf("active claim = %+v, %v", c, err)
		}
	}
	if item, _ := store.GetAttentionItemStateByCursor(active); item.State != AttentionStateSnoozed || !item.SnoozedUntil.Equal(later) {
		t.Fatalf("active item changed: %+v", item)
	}

	// With no earlier told record, a release removes the claim's record.
	fresh := mailEvent(t, store, now, "/p", "BlueLake", 44, "RedStone", "never told")
	setSnooze(t, store, fresh, AttentionStateSnoozed, &earlier)
	var freshSnooze SnoozedMessage
	_, ended, _ := store.SnoozedMessages("/p", "BlueLake", now)
	for _, s := range ended {
		if s.MessageID == 44 {
			freshSnooze = s
		}
	}
	c, err := store.ClaimEndedSnooze(freshSnooze, nudgeTold("BlueLake", 44, now))
	if err != nil || c == nil || c.Previous != nil {
		t.Fatalf("fresh claim = %+v, %v", c, err)
	}
	if err := store.ReleaseEndedSnoozeClaim(c); err != nil {
		t.Fatal(err)
	}
	if rec, _ := store.GetToldRecord("/p", "BlueLake", 44); rec != nil {
		t.Fatalf("release left a told record: %+v", rec)
	}
}

// Behavior 10 property: callers in separate store connections, as the hook and
// the monitor are, race to claim one ended snooze, and exactly one wins.
func TestClaimEndedSnooze_OneWinnerProperty(t *testing.T) {
	dir := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		path := filepath.Join(dir, fmt.Sprintf("state-%d.db", run))
		seed, err := Open(path)
		if err != nil {
			rt.Fatal(err)
		}
		defer seed.Close()
		if err := seed.Migrate(); err != nil {
			rt.Fatal(err)
		}
		now := time.Now().UTC().Truncate(time.Second)
		earlier := now.Add(-time.Minute)
		setSnooze(t, seed, mailEvent(t, seed, now, "/p", "BlueLake", 42, "RedStone", "race"), AttentionStateSnoozed, &earlier)
		snooze := endedSnoozeOf(t, seed, "BlueLake", now)

		n := rapid.IntRange(2, 6).Draw(rt, "callers")
		stores := make([]*Store, n)
		for i := range stores {
			if stores[i], err = Open(path); err != nil {
				rt.Fatal(err)
			}
			defer stores[i].Close()
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				claim, err := stores[i].ClaimEndedSnooze(snooze, nudgeTold("BlueLake", 42, now))
				if err != nil {
					t.Errorf("caller %d: %v", i, err)
				}
				if claim != nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			rt.Fatalf("%d winners of %d callers", wins.Load(), n)
		}
	})
}

func TestSnoozeAttentionItem(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	dismissed := now.Add(-time.Hour)
	item := AttentionItemState{ItemKey: "cursor:1", State: AttentionStateDismissed, DismissedAt: &dismissed, DismissedBy: "op"}
	until := now.Add(30 * time.Minute).In(time.FixedZone("EST", -5*3600))
	if err := SnoozeAttentionItem(&item, until, now); err != nil {
		t.Fatal(err)
	}
	if item.State != AttentionStateSnoozed || item.SnoozedUntil == nil || !item.SnoozedUntil.Equal(until) ||
		item.SnoozedUntil.Location() != time.UTC || item.DismissedAt != nil || item.DismissedBy != "" {
		t.Fatalf("item after snooze = %+v", item)
	}
	before := item
	if err := SnoozeAttentionItem(&item, now, now); !errors.Is(err, ErrSnoozeNotInFuture) {
		t.Fatalf("past snooze error = %v", err)
	}
	if item.SnoozedUntil != before.SnoozedUntil || item.State != before.State {
		t.Fatalf("a rejected snooze changed the item: %+v", item)
	}
}
