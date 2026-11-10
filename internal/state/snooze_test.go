package state

import (
	"errors"
	"fmt"
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
		until := now.Add(time.Duration(rapid.Int64Range(1, 7*24*3600).Draw(rt, "seconds")) * time.Second)
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
