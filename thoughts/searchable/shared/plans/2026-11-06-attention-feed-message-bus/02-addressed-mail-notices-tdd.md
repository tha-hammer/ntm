# Loop 2: Addressed Mail Notices TDD Implementation Plan

Loop document: [02-addressed-mail-notices.md](02-addressed-mail-notices.md). Scope: [SCOPE.md](SCOPE.md), process **Mail Notice Recording**, contract **AddressedAttentionEvent**. Index: [00-master-index.md](00-master-index.md).

**Beads:** epic `bd-pbp98`, with `bd-pbp98.1` (Sprint 1) and `bd-pbp98.2` (Sprint 2). Sprint 1 blocks Sprint 2.

## Overview

After this loop, a mail event names its recipient in columns that can be queried, an agent's events can be listed with `--events-recipient`, and a message has one event per recipient however many processes see it. There are 15 testable behaviors across 2 sprints: 6 BLOCKING and 9 LEAF.

## Current State Analysis

The recipient is only inside an event's `details` JSON (`internal/robot/attention_feed.go:2257`, `:2280`). `--robot-events` filters after reading (`filterEventsForRobot`, `internal/robot/events.go:427`), and it has no recipient filter. The dashboard tracks seen mail in the memory-only `seenMailAttentionIDs` (`internal/tui/dashboard/model.go:270`, `dashboard.go:2933`), and both Publish functions call plain `Append`. Now that the feed is durable, each dashboard restart writes all unread mail again.

### Key Discoveries

- `attention_events` has four read queries that list columns by hand: `GetAttentionEventsSince` (`internal/state/runtime_store.go:1632`), `GetEventsForIncident` (`:1673`), `GetAttentionEventsInTimeRange` (`:1718`) and `GetLatestAttentionEventsInTimeRange` (`:1775`). There are also two inserts (`:1577`, `:1601`). A new column must be added to all six.
- `AttentionStore` has two implementers: `state.Store` and `stubAttentionStore`.
- `replayMatchingEvents` (`internal/robot/events.go:377`) is the single paging loop for filtered reads, so the recipient query fits in there.
- `matched` starts as a nil slice in `replayMatchingEvents`, so a filter with no matches could print `"events": null`. The field rules in `AGENTS.md` require `[]`.
- Other code writes mail events with no message id: `pending_ack` writes `mail.ack_required` and the backlog writes `mail.unread` (`internal/robot/robot.go:8178-8188`), and the tracker writes `mail.received` (`internal/robot/attention_feed.go:2658`). They stay unaddressed.
- Agent Mail matches agent names in any letter case (`func.lower(Agent.name)`, `mcp_agent_mail/app.py:2950`). So both the filter and the unique index use `COLLATE NOCASE`.
- WAL readers aren't blocked by a write lock, so a read in the Publish path works during an outage. It doesn't prove the store can be written.
- The dashboard has no popup for each new mail message. The attention panel reads its section from the feed (`internal/tui/dashboard/commands.go:340`), so the loop document's popup assumption needs no code.

## Desired End State

`ntm --robot-events --events-recipient=RedStone` lists RedStone's mail events, however busy the feed is. Restarting a dashboard with unread mail writes nothing new. Many processes can publish one message at once, and one event results.

### Observable Behaviors

Listed by sprint below, in Given / When / Then form.

## What We're NOT Doing

- The repeating `mail.unread` summaries (bead `bd-at16i`). They have no message id, so this loop's check can't catch them.
- The told record (Loop 3).
- Adding recipients to the unaddressed mail events listed above.
- Delivering other processes' events to subscribers (Loop 4).
- The full `docs/attention-feed-contract.md` update (Loop 9).
- Filling recipient fields on rows that already exist.

**Accepted behavior:** an event expires after 7 days. A message still unread after its row is removed gets one new event. Behavior 9 pins this with a test. Until cleanup removes an expired row, the message still counts as recorded.

**Approved decision:** `seenMailAttentionIDs` is removed, so every poll checks the store. Mail held in memory during an outage reaches `state.db` on the first poll after recovery. This changes the Loop 1 answer about "during lock" events, for mail with a message id only. Each unread message costs one read per poll.

## Testing Strategy

- **Framework:** Go standard `testing`.
- **Database:** real SQLite files in `t.TempDir()` for closure and store tests. `stubAttentionStore` for unit tests that need scripted failures.
- **Closure tests:** re-exec the test binary as helper processes through ntm's real startup, inside `go test -short`. They never call `t.Skip`. A helper that fails, or produces no output to assert on, fails the test.
- **Property tests:** `pgregory.net/rapid`, added in Loop 1.
- **Mocking:** only fault injection around the real store. The Agent Mail server is outside the map, so inbox contents are handed to the dashboard's real message handler.

**Environment on this VPS:**
- Go 1.26.3 is at `/usr/local/go/bin/go`, and isn't on the default PATH, so run `export PATH=$PATH:/usr/local/go/bin` first.
- `golangci-lint`, `goimports` and `rch` aren't installed here. If they're still missing at build time, report those checks as not run, and don't count them as passed.
- Red runs of property tests leave a record in `testdata/rapid/`. Move it out of the repo before committing.

## Workflow Closure

Anchors come from the research's Chain B (`thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`), as extended across processes by Loop 1.

- **`highest_new_connector`:** in Sprint 1, the recipient fields carried from `PublishMailPending` to the store, and `--events-recipient` read through `AttentionFeed`. In Sprint 2, `AttentionFeed.AppendAddressed`.
- **Trigger choice:** `fetchAgentMailInboxes` is unchanged, so the trigger is the handler for its result, `Model.Update(AgentMailInboxSummaryMsg)` (`internal/tui/dashboard/dashboard.go:2884`), as in Loop 1. It stays above every new connector.

### Production Operation Chain

`dashboard startup -> AgentMailInboxSummaryMsg handler -> PublishMailPending -> AttentionFeed.AppendAddressed -> Store.AppendAddressedAttentionEvent -> state.db -> (new process) --robot-events --events-recipient -> BuildEventsOutput -> replayMatchingEvents -> AttentionFeed.ReplayForRecipient -> Store.GetAttentionEventsForRecipient -> JSON`

**Production callers:**

| New code | Production caller |
|---|---|
| Migrations `016`, `017` | `Store.Migrate`, from `openProcessStateStore` |
| `robotEventsRecipient` flag | `rootCmd` `--robot-events` handler (`internal/cli/root.go:301`) |
| `AttentionFeed.ReplayForRecipient` | `replayMatchingEvents` |
| `Store.GetAttentionEventsForRecipient` | `AttentionFeed.ReplayForRecipient` |
| `AttentionFeed.AppendAddressed` | `PublishMailPending`, `PublishMailAckRequired` |
| `Store.AppendAddressedAttentionEvent` | `AttentionFeed.AppendAddressed` |
| `Store.GetAddressedAttentionEvent` | `AttentionFeed.AppendAddressed` |
| `AttentionJournal.FindAddressed` | `AttentionFeed.AppendAddressed` |

### Closure Test: A dashboard's mail can be listed for one agent from another process [BLOCKING]

Behavior 1.
- **SOURCE (seed only)**: `InboxMessage` values for BlueLake and RedStone, handed to the dashboard's message handler.
- **TRIGGER (start)**: `Model.Update(AgentMailInboxSummaryMsg)` : `internal/tui/dashboard/dashboard.go:2884`, in a helper process through real startup. (boundary = highest_new_connector)
- **DRIVERS (async edges)**: none. The parent waits for the helper.
- **OBSERVE (assert via)**: `ntm --robot-events --events-recipient=RedStone` through `Execute`, in a new process.
- **FORBIDDEN SPAN**: `PublishMailPending`, `Store.GetAttentionEventsForRecipient`, raw SQL.
- **RED-AT-SEAM proof**: stop copying the recipient in `attentionEventToStored`. Expected: `events` is empty.
- **DRIVABILITY**: `state.db` location comes from `XDG_CONFIG_HOME`, as in Loop 1. The span is synchronous, so no clock is needed.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/` on a temp config folder. A failed helper fails the test.

### Closure Test: A busy feed can't push an agent's mail out of the result [BLOCKING]

Behavior 4.
- **SOURCE (seed only)**: `InboxMessage` for RedStone, then 500 unaddressed events.
- **TRIGGER (start)**: `Model.Update(AgentMailInboxSummaryMsg)`, then `AttentionFeed.Append` in the `append-events` helper.
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `ntm --robot-events --events-recipient=RedStone --events-limit=10`, in a new process.
- **FORBIDDEN SPAN**: `replayMatchingEvents`, `Store.GetAttentionEventsForRecipient`, raw SQL.
- **RED-AT-SEAM proof**: don't pass `--events-recipient` into `EventsOptions`. Expected: the first page is other events. The SQL query itself is proven by `TestReplayMatchingEvents_RecipientIgnoresOtherEventsInScanLimit`, because the scan page is larger than 500.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

### Closure Test: An unknown agent name returns an empty list, not an error [BLOCKING]

Behavior 5.
- **SOURCE (seed only)**: `InboxMessage` for RedStone.
- **TRIGGER (start)**: `Model.Update(AgentMailInboxSummaryMsg)`.
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `ntm --robot-events --events-recipient=NoSuchAgent`, raw JSON, in a new process.
- **FORBIDDEN SPAN**: `BuildEventsOutput`, `Store.GetAttentionEventsForRecipient`.
- **RED-AT-SEAM proof**: don't initialize `matched` in `replayMatchingEvents`. Expected: `events` is `null`, and the test fails.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

### Closure Test: Restarting a dashboard with recorded mail writes no new events [BLOCKING]

Behavior 11.
- **SOURCE (seed only)**: 10 `InboxMessage` values for RedStone.
- **TRIGGER (start)**: `Model.Update(AgentMailInboxSummaryMsg)` in one helper process, then the same in a second.
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `--robot-events --events-recipient=RedStone`, in a new process.
- **FORBIDDEN SPAN**: `AppendAddressed`, `Store.AppendAddressedAttentionEvent`, raw SQL.
- **RED-AT-SEAM proof**: have the Publish functions call plain `Append`. Expected: the unique index rejects the write, the feed reports an outage, and the helper's durability check fails.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

### Closure Test: Many processes publishing one message produce one event, and none fails [BLOCKING]

Behavior 12.
- **SOURCE (seed only)**: 10 `InboxMessage` values for RedStone.
- **TRIGGER (start)**: `Model.Update(AgentMailInboxSummaryMsg)` in 8 helper processes at once, on a new `state.db`.
- **DRIVERS (async edges)**: none. The parent waits for the helpers.
- **OBSERVE (assert via)**: `--robot-events --events-recipient=RedStone`, in a new process.
- **FORBIDDEN SPAN**: `AppendAddressed`, `Store.AppendAddressedAttentionEvent`, raw SQL.
- **RED-AT-SEAM proof**: remove `ON CONFLICT` from the insert. Expected: a helper reports a store failure, so its durability check fails.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. Run it repeatedly at build time, because Loop 1's first-use bugs only showed up in some runs.

### Closure Test: A new message after a restart produces one new event [BLOCKING]

Behavior 13.
- **SOURCE (seed only)**: 10 `InboxMessage` values, then an 11th.
- **TRIGGER (start)**: `Model.Update(AgentMailInboxSummaryMsg)` in two helper processes.
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `--robot-events --events-recipient=RedStone`, in a new process.
- **FORBIDDEN SPAN**: `AppendAddressed`, `Store.AppendAddressedAttentionEvent`, raw SQL.
- **RED-AT-SEAM proof**: make `AppendAddressed` always report the first recorded message as existing. Expected: the 11th event is missing.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

---

## Sprint 1: Events addressed to an agent

**Bead:** `bd-pbp98.1`.

**Testable function:** "Send mail to two agents, then list only one agent's events with `ntm --robot-events --events-recipient=<name>`."

**Order of work:** write 1 as the outer test, then 2, 3, 4, 5, 6, 7, 8. Behavior 1 goes green with 3 and 4.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 1 |
| A new numbered migration after `015` adds `recipient_project`, `recipient_agent` and `message_id` to `attention_events`, and upgrades an existing `state.db` automatically | 2 |
| `PublishMailPending` and `PublishMailAckRequired` fill in those fields | 1, 3 |
| `--events-recipient=<name>` returns only that agent's events, and a busy feed can't push them out of the result | 1, 4, 7 |
| An unknown name returns `success: true` with `events: []`, following the field rules in `AGENTS.md` | 5 |
| Events that aren't addressed, such as `mail.unread` summaries, have an empty recipient and are unaffected without the flag | 6 |
| Listed in `--robot-capabilities` (loop document, API) | 8 |

**Build notes (2026-11-07):**

**Found during verification:** applying pending migration `016` changed the real `state.db` on the VPS. `internal/robot` tests reached `ensemble.NewStateStore`, which opens the default database when given no path, and that package had no `TestMain`. Loop 1's check compared the file's modified time, which can't see an open that writes nothing. So its claim that robot tests never touch the real file was wrong, though only migrations ran, with no events written. `internal/robot/testmain_test.go` now redirects the config folder, and the verification command is replaced.

**Corrections to this plan:**
- The Behavior 5 proof was wrong. With a recipient filter, `filterEventsForRobot` already builds a non-nil list, so removing the `matched` initialization doesn't make that test fail. The `null` only happens with no filter on an empty feed, so `TestBuildEventsOutput_EmptyFeedReturnsEmptyList` was added to prove the fix. Behavior 5's own proof is the blank name check below.
- `applyMigrationsThrough` takes the store and fails the test itself, rather than returning an error.
- `MessageID` was placed on `AttentionEvent` as `int64`, and `addressMailEvent` treats a blank name after trimming as no recipient.

| Test | Red result | Red setup |
|---|---|---|
| `TestClosure_EventsRecipientListsOneAgent` | `unknown flag: --events-recipient` | Before the flag existed |
| `TestClosure_EventsRecipientListsOneAgent` | `got 0 events` | `attentionEventToStored` not copying `RecipientAgent` |
| `TestClosure_BusyFeedKeepsRecipientEvents` | Returned other events | Flag not passed into `EventsOptions` |
| `TestClosure_UnknownRecipientReturnsEmptyList` | `a blank recipient should mean no filter` | Flag value not trimmed |
| `TestMigration016_KeepsRowsWithEmptyRecipient` | `no such column: recipient_project` | Before migration `016` |
| `TestAttentionRecipient_EveryReadPathKeepsFields` | `table attention_events has no column` | Before migration `016` |
| `TestAttentionEventStored_RecipientRoundtrip` | Fields lost in the round trip | Before the conversion carried them |
| `TestPublishMail_FillsRecipientFields` | Fields empty | Before `addressMailEvent` |
| `TestReplayMatchingEvents_RecipientIgnoresOtherEventsInScanLimit` | Empty result, `hasMore` true | `replayEventsPage` using plain `Replay` |
| `TestBuildEventsOutput_EmptyFeedReturnsEmptyList` | `events` nil | `matched` not initialized |
| `TestRobotEventsCapabilities_ListRecipientFlag` | Flag not listed | Before the entries were added |

### Behavior 1: Mail can be listed for one agent [BLOCKING]

#### Test Specification

**Given** a dashboard that sees mail for BlueLake and RedStone. **When** another process runs `--events-recipient=RedStone`. **Then** only RedStone's event returns, with `recipient_agent`, `recipient_project` and `message_id` set.

**Edge Cases:** the filter's letter case doesn't matter. Other flags such as `--events-category` still apply on top.

**Property:** no property here: a fixed journey. The field round trip is in Behavior 3.

**Files touched:** `internal/cli/attention_closure_test.go`, `internal/cli/root.go`, `internal/robot/events.go`, `internal/robot/attention_feed.go`, `internal/state/runtime_store.go`.

#### TDD Cycle

##### 🔴 Red

```go
const closureInboxArgSep = "|"

// closureHelperPublishInbox goes through ntm's real startup for the dashboard
// command, then hands the real model one poll's inboxes. Each arg is
// "<message id>|<agent>|<subject>". It fails if the feed ended up memory-only.
func closureHelperPublishInbox(args []string) int {
	os.Args = []string{"ntm", "dashboard"}
	if err := rootCmd.PersistentPreRunE(newDashboardCmd(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeRobotPersistence()

	inboxes := map[string][]agentmail.InboxMessage{}
	agents := map[string]string{}
	for _, arg := range args {
		parts := strings.SplitN(arg, closureInboxArgSep, 3)
		id, err := strconv.Atoi(parts[0])
		if err != nil || len(parts) != 3 {
			fmt.Fprintf(os.Stderr, "bad inbox arg %q\n", arg)
			return 2
		}
		paneID := "%" + parts[1]
		agents[paneID] = parts[1]
		inboxes[paneID] = append(inboxes[paneID], agentmail.InboxMessage{ID: id, From: "Sender", Subject: parts[2]})
	}

	project := os.Getenv(closureHelperProjectEnv)
	model := dashboard.New("closure", project)
	model.Update(dashboard.AgentMailInboxSummaryMsg{
		ProjectKey: project, Inboxes: inboxes, AgentMap: agents, Gen: 1,
	})
	if health := robot.GetAttentionFeed().StoreHealth(); !health.Durable {
		fmt.Fprintf(os.Stderr, "feed is memory-only: %+v\n", health)
		return 1
	}
	return 0
}

func inboxArg(id int, agent, subject string) string {
	return strings.Join([]string{strconv.Itoa(id), agent, subject}, closureInboxArgSep)
}

func TestClosure_EventsRecipientListsOneAgent(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", inboxArg(41, "BlueLake", "for blue"), inboxArg(42, "RedStone", "for red"))

	for _, name := range []string{"RedStone", "redstone"} {
		events := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient="+name))
		if len(events) != 1 {
			t.Fatalf("%s: got %d events: %+v", name, len(events), events)
		}
		got := events[0]
		if got.RecipientAgent != "RedStone" || got.RecipientProject != env.workDir || got.MessageID != 42 || !strings.Contains(got.Summary, "for red") {
			t.Fatalf("%s: event = %+v", name, got)
		}
	}

	filtered := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=RedStone", "--events-category=agent"))
	if len(filtered) != 0 {
		t.Fatalf("category filter ignored: %+v", filtered)
	}
}
```

`closureRobotEvent` gains the three JSON fields. `runClosureHelper` gains the `publish-inbox` mode.

Expected failure: `unknown flag: --events-recipient`.

##### 🟢 Green

**File:** `internal/cli/root.go`: add `robotEventsRecipient string` next to `robotEventsCategory` (`:2801`), register `--events-recipient` next to `--events-category` (`:3323`), and pass it as `opts.Recipient` with spaces trimmed.

**File:** `internal/robot/events.go`:

```go
type EventsOptions struct {
	// Recipient limits results to events addressed to this agent name, in any
	// project. The store filters it, so a busy feed can't hide matches.
	Recipient string
	// ...
}

// replayEventsPage reads one page. A recipient filter goes to the store, so the
// page holds only that agent's events.
func replayEventsPage(feed *AttentionFeed, opts EventsOptions, cursor int64, pageSize int) ([]AttentionEvent, int64, error) {
	if opts.Recipient != "" {
		return feed.ReplayForRecipient(opts.Recipient, cursor, pageSize)
	}
	return feed.Replay(cursor, pageSize)
}
```

`replayMatchingEvents` calls it in place of `feed.Replay`. `eventsFiltersActive` includes `Recipient`, and `filterEventsForRobot` checks it with `strings.EqualFold`, so `--events-incident` and `--events-as-of` respect the flag too.

**File:** `internal/robot/attention_feed.go`:

```go
// ReplayForRecipient returns events addressed to one agent, in any project.
func (f *AttentionFeed) ReplayForRecipient(agent string, sinceCursor int64, limit int) ([]AttentionEvent, int64, error) {
	store := f.currentStore()
	if store == nil {
		return f.journalReplayForRecipient(agent, sinceCursor, limit)
	}
	return f.replayFromStorePage(store, sinceCursor, limit,
		func(since int64, n int) ([]state.StoredAttentionEvent, error) {
			return store.GetAttentionEventsForRecipient(agent, since, n)
		},
		func(event AttentionEvent) bool { return strings.EqualFold(event.RecipientAgent, agent) })
}
```

`replayFromStore` becomes `replayFromStorePage` with a fetch function and a memory-only filter, so window checks, cursor expiry and cleanup stay in one place. `Replay` passes `store.GetAttentionEventsSince` and no filter.

**File:** `internal/state/runtime_store.go`:

```go
// GetAttentionEventsForRecipient returns one agent's events after sinceCursor.
func (s *Store) GetAttentionEventsForRecipient(agent string, sinceCursor int64, limit int) ([]StoredAttentionEvent, error)
```

Its query: `WHERE recipient_agent = ? COLLATE NOCASE AND cursor > ? AND (expires_at IS NULL OR expires_at >= datetime('now')) ORDER BY cursor ASC LIMIT ?`.

##### 🔵 Refactor

- [ ] **No duplication:** one replay function handles window checks for both paths.
- [ ] **Reveals intent:** `replayEventsPage` says why the recipient goes to the store.
- [ ] **Complexity went down:** no new paging loop.
- [ ] **No shallow wrappers:** `ReplayForRecipient` adds the fetch and the memory filter.
- [ ] **Fits existing patterns:** the flag follows `--events-category`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_EventsRecipientListsOneAgent ./internal/cli/`, with red-at-seam recorded.

### Behavior 2: Migration keeps existing rows [LEAF: state package only, synchronous]

#### Test Specification

**Given** a `state.db` at version 015 with rows. **When** it migrates. **Then** the rows are kept, their recipient fields are empty, `message_id` is NULL, and migrating again changes nothing.

**Edge Cases:** an older ntm build can still insert into the migrated table, because the new columns have defaults.

**Property:** no property: fixed input, nothing to generate over.

**Files touched:** `internal/state/migrations/016_attention_recipient.sql`, `internal/state/state_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestMigration016_KeepsRowsWithEmptyRecipient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := applyMigrationsThrough(store.DB(), 15); err != nil {
		t.Fatalf("migrate to 015: %v", err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO attention_events (ts, category, event_type, source, actionability, severity, summary)
		VALUES (?, 'system', 'system.health_change', 'old', 'background', 'info', 'old row')`, time.Now().UTC()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Summary != "old row" {
		t.Fatalf("events = %+v", events)
	}
	if got := events[0]; got.RecipientProject != "" || got.RecipientAgent != "" || got.MessageID != nil {
		t.Fatalf("recipient fields should be empty: %+v", got)
	}

	// An older build's insert names none of the new columns.
	if _, err := store.DB().Exec(`
		INSERT INTO attention_events (ts, category, event_type, source, actionability, severity, summary)
		VALUES (?, 'system', 'system.health_change', 'old', 'background', 'info', 'older build')`, time.Now().UTC()); err != nil {
		t.Fatalf("older build's insert: %v", err)
	}
}
```

`applyMigrationsThrough` is a test helper that applies migration files up to a version with the same `applyMigration` function production uses.

##### 🟢 Green

**File:** `internal/state/migrations/016_attention_recipient.sql`

```sql
ALTER TABLE attention_events ADD COLUMN recipient_project TEXT NOT NULL DEFAULT '';
ALTER TABLE attention_events ADD COLUMN recipient_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE attention_events ADD COLUMN message_id INTEGER;

CREATE INDEX IF NOT EXISTS idx_attention_events_recipient
    ON attention_events(recipient_agent COLLATE NOCASE, cursor);
```

**File:** `internal/state/runtime_schema.go`: add to `StoredAttentionEvent`:

```go
// Recipient. Empty for events not addressed to one agent.
RecipientProject string `json:"recipient_project,omitempty"`
RecipientAgent   string `json:"recipient_agent,omitempty"`
MessageID        *int64 `json:"message_id,omitempty"`
```

##### 🔵 Refactor

- [ ] **No duplication:** the test helper reuses `applyMigration`.
- [ ] **Reveals intent:** the file name says what it adds.
- [ ] **Complexity went down:** no data backfill.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** matches `008_attention_reason_code.sql`.

#### Success Criteria

**Automated:** `go test -short -run TestMigration016 ./internal/state/` fails first, then passes.

### Behavior 3: Recipient fields survive every read path [LEAF: conversion and state package queries, synchronous]

#### Test Specification

**Given** any recipient and message id. **When** an event is published or appended, then read by `Replay`, `ReplayForIncident` or `ReconstructAsOf`. **Then** the fields come back unchanged.

**Edge Cases:**
- `PublishMailPending` and `PublishMailAckRequired` both fill the fields from their arguments, and `details` stays as it is today.
- An empty `to` leaves all three fields empty, so a message with no known recipient isn't treated as addressed.
- Output omits the fields when they're empty.

**Property:** **Roundtrip** — for all project, agent and message id, `attentionEventFromStored(attentionEventToStored(e))` keeps all three fields.

**Domain:** project and agent are any non-empty strings up to 64 runes, and message id is 1 to 2^53.

```go
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
```

**Files touched:** `internal/robot/attention_contract.go`, `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`, `internal/state/runtime_store.go`, `internal/state/state_test.go`.

#### TDD Cycle

##### 🔴 Red

The property above, plus:

```go
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
			if empty := publish(feed, "", 43); empty.RecipientProject != "" || empty.RecipientAgent != "" || empty.MessageID != 0 {
				t.Fatalf("event with no recipient = %+v", empty)
			}
		})
	}
}
```

And in `internal/state/state_test.go`, a table test that appends one addressed event and reads it through each of `GetAttentionEventsSince`, `GetEventsForIncident`, `GetAttentionEventsInTimeRange`, `GetLatestAttentionEventsInTimeRange` and the `Tx` insert. A read that misses a column fails its row.

##### 🟢 Green

**File:** `internal/robot/attention_contract.go`: add to `AttentionEvent`:

```go
// RecipientProject and RecipientAgent name the agent a mail event is addressed
// to. Both are empty for other events.
RecipientProject string `json:"recipient_project,omitempty"`
RecipientAgent   string `json:"recipient_agent,omitempty"`

// MessageID is the Agent Mail message id of an addressed mail event.
MessageID int64 `json:"message_id,omitempty"`
```

**File:** `internal/robot/attention_feed.go`: `attentionEventToStored` and `attentionEventFromStored` carry the fields, turning `MessageID` 0 into NULL and back. Both Publish functions call one helper:

```go
// addressMailEvent names the recipient. With no known recipient the event stays
// unaddressed, since Mail Notice Recording needs both.
func addressMailEvent(event AttentionEvent, projectKey, to string, messageID int) AttentionEvent {
	if strings.TrimSpace(to) == "" {
		return event
	}
	event.RecipientProject = projectKey
	event.RecipientAgent = to
	event.MessageID = int64(messageID)
	return event
}
```

**File:** `internal/state/runtime_store.go`: add the three columns to both inserts and all four read queries.

##### 🔵 Refactor

- [ ] **No duplication:** the four read queries share one column list and one scan function. Replace the copied `rows.Scan` blocks with the `scanRangedAttentionEvents` helper from Loop 1, renamed `scanAttentionEvents`.
- [ ] **Reveals intent:** `addressMailEvent` states the rule for an empty recipient.
- [ ] **Complexity went down:** one place to add a future column.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `COALESCE` for strings, a pointer for nullable values, as `ExpiresAt` does.

#### Success Criteria

**Automated:** `go test -short -run 'RecipientRoundtrip|PublishMail_FillsRecipientFields' ./internal/robot/` and `go test -short -run TestAttentionRecipient ./internal/state/` fail first, then pass.

### Behavior 4: A busy feed can't hide an agent's mail [BLOCKING]

#### Test Specification

**Given** RedStone's event, followed by 500 other events. **When** `--events-recipient=RedStone --events-limit=10` runs. **Then** RedStone's event is returned. Paging through more matches than the limit returns each once.

**Edge Cases:** a scan limit counts only matching rows, because the store filters first. Replaying from a cursor that has expired still returns `CURSOR_EXPIRED`.

**Property:** no property: fixed volumes.

**Files touched:** `internal/cli/attention_closure_test.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_BusyFeedKeepsRecipientEvents(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", inboxArg(42, "RedStone", "for red"))
	env.run(t, "append-events", "500")

	events := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=RedStone", "--events-limit=10"))
	if len(events) != 1 || events[0].MessageID != 42 {
		t.Fatalf("events = %+v", events)
	}
}

func TestReplayMatchingEvents_RecipientIgnoresOtherEventsInScanLimit(t *testing.T) {
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))))
	t.Cleanup(feed.Stop)
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
	// Publishes 25 messages to RedStone between other events, pages with Limit 10,
	// and checks 25 distinct cursors in order, with HasMore false on the last page.
}
```

##### 🟢 Green

No production code beyond Behavior 1. If `TestReplayMatchingEvents_RecipientIgnoresOtherEventsInScanLimit` passes before `replayEventsPage` exists, the test is wrong, because a read that filters after fetching must stop at the scan limit.

##### 🔵 Refactor

- [ ] **No duplication:** tests share `inboxArg` and `openStoreFile`.
- [ ] **Reveals intent:** a comment explains the small scan limit.
- [ ] **Complexity went down:** no production branch.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** matches `TestReplayMatchingEvents_StopsAtScanLimit`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_BusyFeedKeepsRecipientEvents ./internal/cli/` and `go test -short -run 'RecipientIgnoresOtherEvents|RecipientPagesEachEventOnce' ./internal/robot/`, with red-at-seam recorded.

### Behavior 5: Unknown name returns an empty list [BLOCKING]

#### Test Specification

**Given** a feed with RedStone's event. **When** `--events-recipient=NoSuchAgent` runs. **Then** the exit code is 0, `success` is `true`, and `events` is `[]`, not `null`.

**Edge Cases:**
- An empty feed behaves the same.
- `--events-recipient=` or spaces only means no filter.
- `next_cursor` is the newest cursor, so a poller doesn't rescan.

**Property:** no property: fixed input.

**Files touched:** `internal/cli/attention_closure_test.go`, `internal/robot/events.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_UnknownRecipientReturnsEmptyList(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", inboxArg(42, "RedStone", "for red"))

	out := env.run(t, "execute", "--robot-events", "--events-recipient=NoSuchAgent")
	var response struct {
		Success bool            `json:"success"`
		Events  json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !response.Success || string(response.Events) != "[]" {
		t.Fatalf("response = %s", out)
	}

	all := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient= "))
	if len(all) == 0 {
		t.Fatal("a blank recipient should mean no filter")
	}
}
```

Expected failure: `events` is `null`.

##### 🟢 Green

In `replayMatchingEvents`: `matched := []AttentionEvent{}`.

##### 🔵 Refactor

- [ ] **No duplication:** one initialization.
- [ ] **Reveals intent:** a comment cites the `AGENTS.md` field rule.
- [ ] **Complexity went down:** a one-line change.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** other robot outputs return `[]`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_UnknownRecipientReturnsEmptyList ./internal/cli/`, with red-at-seam recorded.

### Behavior 6: Unaddressed events are unaffected [LEAF: feed only, synchronous]

#### Test Specification

**Given** a `mail.unread` summary and other unaddressed events. **When** replayed without the flag. **Then** they're returned as before with empty recipient fields, and their JSON has none of the three keys.

**Edge Cases:** the coordination problem path (`internal/robot/robot.go:8178`) and the tracker path (`attention_feed.go:2658`) leave the fields empty. A recipient filter doesn't return them.

**Property:** no property: fixed event kinds.

**Files touched:** `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestUnaddressedEvents_HaveNoRecipient(t *testing.T) {
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))))
	t.Cleanup(feed.Stop)
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
	matches, _, err := feed.ReplayForRecipient("", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("an empty name matched unaddressed events: %+v", matches)
	}
}
```

Expected failure: the last check, since an empty name would match every unaddressed row.

##### 🟢 Green

`ReplayForRecipient` returns no events for a blank name, without a store read. `EventsOptions.Recipient` is already blank-free, so this guards other callers.

##### 🔵 Refactor

- [ ] **No duplication:** one guard, in `ReplayForRecipient`.
- [ ] **Reveals intent:** the guard says that an empty recipient means "not addressed".
- [ ] **Complexity went down:** one early return.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `omitempty` for optional fields, as `AGENTS.md` describes.

#### Success Criteria

**Automated:** `go test -short -run TestUnaddressedEvents_HaveNoRecipient ./internal/robot/` fails first, then passes.

### Behavior 7: The filter also finds memory-only events [LEAF: feed only, using an injected clock]

#### Test Specification

**Given** a feed with no store, or one in an outage. **When** the filter runs. **Then** matching memory-only events are returned, merged by cursor with stored ones.

**Edge Cases:**
- An agent name used in two projects returns both, each naming its `recipient_project`.
- A limit applies across stored and memory-only events.

**Property:** no property: scripted steps.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
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
		t.Fatalf("cursors = %v", got)
	}
}

func TestReplayForRecipient_SameNameInTwoProjects(t *testing.T) {
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))))
	t.Cleanup(feed.Stop)
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
```

##### 🟢 Green

`journalReplayForRecipient` reads the journal with the journal size as its limit, then filters and applies the limit. The store path gets memory-only events from `replayFromStorePage`'s filter in Behavior 1.

##### 🔵 Refactor

- [ ] **No duplication:** one name comparison, `strings.EqualFold`.
- [ ] **Reveals intent:** the journal path says it's for feeds without a store.
- [ ] **Complexity went down:** merging reuses `mergeByCursor`.
- [ ] **No shallow wrappers:** the journal function holds the filter.
- [ ] **Fits existing patterns:** Loop 1's `memoryOnlySince`.

#### Success Criteria

**Automated:** `go test -short -run TestReplayForRecipient ./internal/robot/` fails first, then passes.

### Behavior 8: The flag is listed [LEAF: static data]

#### Test Specification

**Given** the capabilities output. **When** `--robot-capabilities` is read. **Then** the `robot-events` entry lists `--events-recipient`, and the attention contract lists it too.

**Edge Cases:** the example list includes a recipient example.

**Property:** no property: static data.

**Files touched:** `internal/robot/capabilities.go`, `internal/robot/attention_contract.go`, and their tests. Update a golden with `-update-goldens` only if a golden test names these flags, and read its diff first.

#### TDD Cycle

##### 🔴 Red

```go
func TestRobotEventsCapabilities_ListRecipientFlag(t *testing.T) {
	// Finds the robot-events entry in GetCapabilities() and in the attention
	// contract's command list, and requires --events-recipient in each.
}
```

##### 🟢 Green

Add the entry to `capabilities.go:594` and `attention_contract.go:446`, with the description "Only events addressed to this agent, in any project (any letter case)".

##### 🔵 Refactor

- [ ] **No duplication:** one description, in a named constant shared by both entries.
- [ ] **Reveals intent:** the description states the any-project rule.
- [ ] **Complexity went down:** data only.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the neighboring `events-category` entries.

#### Success Criteria

**Automated:** `go test -short -run TestRobotEventsCapabilities ./internal/robot/` fails first, then passes.

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop2 ./cmd/ntm`.
2. In a scratch project folder, create a session: `/tmp/ntm-loop2 spawn addrtest --cc=2`.
3. Find the two agent names: `/tmp/ntm-loop2 --robot-status`.
4. Send one message to each: `/tmp/ntm-loop2 mail send addrtest --to <first> "for first"`, then the same for `<second>`.
5. Open `/tmp/ntm-loop2 dashboard addrtest`, wait until the bottom ticker shows `Mail: 2 unread`, then quit.
6. Run `/tmp/ntm-loop2 --robot-events --events-recipient=<first> | jq '.events[] | {recipient_agent, summary}'`.
7. Pass: only the first agent's message is listed.
8. Run `/tmp/ntm-loop2 --robot-events --events-recipient=NoSuchAgent | jq '{success, events}'`.
9. Pass: `success` is `true` and `events` is `[]`.

---

## Sprint 2: No duplicates after restart

**Bead:** `bd-pbp98.2`.

**Testable function:** "Restart a dashboard with unread mail already recorded, and see no new events written for those messages."

**Order of work:** 9, 10, then 11 as the outer test, 13, 12, 14, 15.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 11 |
| `state.db` allows one mail event per project, recipient and message | 9 |
| A dashboard restart with 10 unread messages writes no new events for them | 11 |
| Two processes publishing the same message at once produce one event, and neither fails | 9, 12 |
| A new message after the restart still produces one new event | 13 |
| The dedup is durable, and doesn't depend on the in-memory `seenMailAttentionIDs` map or a time window | 10, 11, 14, 15 |

### Behavior 9: The store keeps one row per message [LEAF: state package only, synchronous]

#### Test Specification

**Given** any mix of project, agent and message id. **When** each is inserted. **Then** there's one row per combination, the first insert reports created, and repeats report the existing cursor.

**Edge Cases:**
- The same message for two recipients gives two rows. The same id in two projects gives two rows.
- Agent names differing only in letter case are one recipient.
- A NULL `message_id` never conflicts.
- Migration `017` removes duplicates a Sprint 1 build wrote, and keeps the oldest.
- A row past its expiry, not yet removed, still counts as existing. Once `GCExpiredEvents` removes it, an insert creates a new row.
- `GetAddressedAttentionEvent` returns no row and no error when none exists.

**Property:** **Invariant** — for any sequence of inserts, the row count equals the number of distinct (project, lowercase agent, message id) keys, and `created` is true exactly once per key.

**Domain:** up to 50 inserts, from 3 projects, 3 agent names in mixed letter case, and message ids 1 to 5, so repeats are common.

```go
func TestAppendAddressed_OneRowPerKeyProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		store := openRapidStore(rt)
		defer store.Close()
		seen := map[string]int64{}
		steps := rapid.IntRange(1, 50).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			project := rapid.SampledFrom([]string{"/one", "/two", "/three"}).Draw(rt, "project")
			agent := rapid.SampledFrom([]string{"RedStone", "redstone", "BlueLake"}).Draw(rt, "agent")
			id := int64(rapid.IntRange(1, 5).Draw(rt, "id"))
			key := fmt.Sprintf("%s|%s|%d", project, strings.ToLower(agent), id)

			cursor, created, err := store.AppendAddressedAttentionEvent(addressedEvent(project, agent, id))
			if err != nil {
				rt.Fatal(err)
			}
			first, repeat := seen[key]
			if created == repeat {
				rt.Fatalf("created = %v for key %s (seen before: %v)", created, key, repeat)
			}
			if repeat && cursor != first {
				rt.Fatalf("repeat cursor = %d, want %d", cursor, first)
			}
			seen[key] = cursor
		}
		var rows int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM attention_events`).Scan(&rows); err != nil {
			rt.Fatal(err)
		}
		if rows != len(seen) {
			rt.Fatalf("rows = %d, keys = %d", rows, len(seen))
		}
	})
}
```

**Files touched:** `internal/state/migrations/017_attention_recipient_unique.sql`, `internal/state/runtime_store.go`, `internal/state/state_test.go`.

#### TDD Cycle

##### 🔴 Red

The property above, plus named tests for each edge case, including:

```go
func TestMigration017_RemovesSprintOneDuplicates(t *testing.T) {
	// Migrates to 016, inserts message 42 for RedStone three times and message 42
	// for BlueLake once, migrates, and expects two rows: RedStone's oldest cursor
	// and BlueLake's. An unaddressed row with a NULL message_id is untouched.
}
```

##### 🟢 Green

**File:** `internal/state/migrations/017_attention_recipient_unique.sql`

```sql
-- A build with only migration 016 wrote one event per dashboard restart.
DELETE FROM attention_events
WHERE message_id IS NOT NULL
  AND cursor NOT IN (
    SELECT MIN(cursor) FROM attention_events
    WHERE message_id IS NOT NULL
    GROUP BY recipient_project, recipient_agent COLLATE NOCASE, message_id
  );

CREATE UNIQUE INDEX IF NOT EXISTS idx_attention_events_addressed
    ON attention_events(recipient_project, recipient_agent COLLATE NOCASE, message_id)
    WHERE message_id IS NOT NULL;
```

**File:** `internal/state/runtime_store.go`

```go
// AppendAddressedAttentionEvent saves a mail event unless one exists for its
// project, recipient and message. It returns the cursor and whether it created
// the row. Another process inserting the same message isn't an error.
func (s *Store) AppendAddressedAttentionEvent(event *StoredAttentionEvent) (int64, bool, error)

// GetAddressedAttentionEvent returns the event for a project, recipient and
// message, or nil when there is none.
func (s *Store) GetAddressedAttentionEvent(project, agent string, messageID int64) (*StoredAttentionEvent, error)
```

The insert ends with `ON CONFLICT (recipient_project, recipient_agent COLLATE NOCASE, message_id) WHERE message_id IS NOT NULL DO NOTHING`. If it affects no rows, a `SELECT` returns the existing cursor.

##### 🔵 Refactor

- [ ] **No duplication:** the insert shares its column list and values with `AppendAttentionEvent`.
- [ ] **Reveals intent:** the name says "addressed", and the comment says a conflict is normal.
- [ ] **Complexity went down:** no retry loop, because the database settles the race.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `fmt.Errorf` wrapping and `s.mu` use, as in `AppendAttentionEvent`.

#### Success Criteria

**Automated:** `go test -short -run 'OneRowPerKey|TestMigration017|TestAddressed' ./internal/state/` fails first, then passes.

### Behavior 10: A repeat writes and delivers nothing [LEAF: feed only, synchronous]

#### Test Specification

**Given** a message already recorded. **When** it's published again. **Then** nothing is written, the journal doesn't grow, subscribers aren't called, the cursor counter doesn't move, and the first event is returned.

**Edge Cases:**
- A message that changes from pending to ack-required keeps its first event.
- An existing row found during an outage doesn't clear the outage, because a read doesn't prove writes work.

**Property:** no property here. Sequences are covered by Behavior 9.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAppendAddressed_RepeatDoesNothing(t *testing.T) {
	store := openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(store))
	t.Cleanup(feed.Stop)
	var delivered atomic.Int32
	unsubscribe := feed.Subscribe(func(AttentionEvent) { delivered.Add(1) })
	defer unsubscribe()

	first := feed.PublishMailPending("/proj", "Sender", "RedStone", "subject", 42, "")
	second := feed.PublishMailAckRequired("/proj", "Sender", "RedStone", "subject", 42, "")

	if second.Cursor != first.Cursor || second.Type != EventTypeMailReceived {
		t.Fatalf("second = %+v, want the first event", second)
	}
	if got := delivered.Load(); got != 1 {
		t.Fatalf("delivered %d times, want 1", got)
	}
	event := first
	event.Cursor = 0
	if _, created := feed.AppendAddressed(event); created {
		t.Fatal("a repeat reported created")
	}
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
}
```

##### 🟢 Green

**File:** `internal/robot/attention_feed.go`

```go
// AppendAddressed appends a mail event unless one exists for its project,
// recipient and message. It returns that event and whether this call created it.
func (f *AttentionFeed) AppendAddressed(event AttentionEvent) (AttentionEvent, bool) {
	// Normalizes the event as Append does, then under appendMu:
	//   store available and found by GetAddressedAttentionEvent -> return it, false.
	//   store available -> reserve after an outage, then AppendAddressedAttentionEvent.
	//     created  -> journal, enqueue for delivery, mark recovered, return event, true.
	//     conflict -> mark recovered, read the winner, return it, false.
	//     error    -> mark down, then treat as no store.
	//   no store, or waiting to retry -> Behaviors 14 and 15.
}
```

`PublishMailPending` and `PublishMailAckRequired` call it when `MessageID` is set, and plain `Append` otherwise. Their return type stays `AttentionEvent`, so their existing tests don't change.

##### 🔵 Refactor

- [ ] **No duplication:** normalization, delivery and the failure path are shared with `Append` through small helpers, and the outage rules stay in Loop 1's functions.
- [ ] **Reveals intent:** `AppendAddressed` reads as found, created, conflict or failed.
- [ ] **Complexity went down:** both Publish functions share one send step.
- [ ] **No shallow wrappers:** `AppendAddressed` owns the rule.
- [ ] **Fits existing patterns:** `AppendDeduplicated` returns `(AttentionEvent, bool)`.

#### Success Criteria

**Automated:** `go test -short -run TestAppendAddressed ./internal/robot/` fails first, then passes.

### Behavior 11: Restart writes no new events [BLOCKING]

#### Test Specification

**Given** a dashboard process that recorded 10 unread messages. **When** a new dashboard process sees the same inbox. **Then** the same 10 cursors are all that's listed, and the second process's feed stays durable.

**Edge Cases:** two polls in one process also write nothing, now that `seenMailAttentionIDs` is gone. A pane with no known agent name publishes nothing, because the scope's precondition needs a recipient.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/attention_closure_test.go`, `internal/tui/dashboard/dashboard.go`, `internal/tui/dashboard/model.go`, `internal/tui/dashboard/dashboard_layout_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func closureInboxOfTen() []string {
	args := make([]string, 0, 10)
	for id := 1; id <= 10; id++ {
		args = append(args, inboxArg(id, "RedStone", fmt.Sprintf("message %d", id)))
	}
	return args
}

func TestClosure_DashboardRestartWritesNoNewEvents(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", closureInboxOfTen()...)
	before := readRecipientCursors(t, env, "RedStone")
	if len(before) != 10 {
		t.Fatalf("first process recorded %d events, want 10", len(before))
	}

	env.run(t, "publish-inbox", closureInboxOfTen()...)
	after := readRecipientCursors(t, env, "RedStone")
	if !slices.Equal(after, before) {
		t.Fatalf("cursors changed after restart: %v -> %v", before, after)
	}
}
```

`readRecipientCursors` pages `--events-recipient` with the same loop as `readAllCursors`. Expected failure: 20 cursors.

##### 🟢 Green

Publish path from Behavior 10. In `internal/tui/dashboard/dashboard.go:2932`:

```go
// Record new unread messages. The feed checks the store, so this is safe on
// every poll and across restarts.
if toAgent != "" {
	threadID := ""
	if mm.ThreadID != nil {
		threadID = *mm.ThreadID
	}
	if mm.AckRequired {
		attentionFeed.PublishMailAckRequired(msg.ProjectKey, mm.From, toAgent, mm.Subject, mm.ID, threadID)
	} else {
		attentionFeed.PublishMailPending(msg.ProjectKey, mm.From, toAgent, mm.Subject, mm.ID, threadID)
	}
}
```

Remove `seenMailAttentionIDs` from `internal/tui/dashboard/model.go:270` and `:575`.

##### 🔵 Refactor

- [ ] **No duplication:** one duplicate check, in the feed.
- [ ] **Reveals intent:** the comment says why this runs on every poll.
- [ ] **Complexity went down:** a field and a map check are gone.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the existing tests at `dashboard_layout_test.go` and `dashboard_popup_test.go` keep passing. Any change to them needs an explanation in the commit.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_DashboardRestartWritesNoNewEvents ./internal/cli/`, with red-at-seam recorded.

### Behavior 12: Many processes produce one event [BLOCKING]

#### Test Specification

**Given** 8 processes on a new `state.db`. **When** each publishes the same 10 messages at once. **Then** there are 10 events with distinct cursors, every process exits 0, and no process reports an outage.

**Edge Cases:** first-use migration runs in all 8, covering `016` and `017`. Many goroutines in one process publish the same message too.

**Property:** no property: a fixed load.

**Files touched:** `internal/cli/attention_closure_test.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_ManyProcessesPublishOneMessage(t *testing.T) {
	const writers = 8
	env := newClosureEnv(t)
	var wg sync.WaitGroup
	errs := make(chan string, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := env.tryRun("publish-inbox", closureInboxOfTen()...); err != nil {
				errs <- fmt.Sprintf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for failure := range errs {
		t.Errorf("writer failed: %s", failure)
	}
	cursors := readRecipientCursors(t, env, "RedStone")
	if len(cursors) != 10 || !allDistinct(cursors) {
		t.Fatalf("cursors = %v, want 10 distinct", cursors)
	}
}

func TestAppendAddressed_ManyGoroutines(t *testing.T) {
	// 20 goroutines publish message 42 on one feed under -race. One created is
	// reported, one subscriber call happens, and the store holds one row.
}
```

##### 🟢 Green

No production code beyond Behaviors 9 and 10. If a process reports an outage, the conflict isn't being treated as normal. Fix that in `AppendAddressed`.

##### 🔵 Refactor

- [ ] **No duplication:** shares the 10 message inbox helper with Behavior 11.
- [ ] **Reveals intent:** the test names its load.
- [ ] **Complexity went down:** no production branch.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** matches `TestClosure_ManyWritersOneStateDB`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_ManyProcessesPublishOneMessage ./internal/cli/`, run at least 40 times from a built test binary, with red-at-seam recorded. Copy the pass count to the sprint notes.

### Behavior 13: A new message after a restart gets one event [BLOCKING]

#### Test Specification

**Given** 10 recorded messages. **When** a new process sees those 10 and an 11th. **Then** there are 11 events: the first 10 cursors are unchanged, and the 11th is higher.

**Edge Cases:** an event removed after 7 days allows a new event, pinned in Behavior 9.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/attention_closure_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_NewMessageAfterRestartGetsOneEvent(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", closureInboxOfTen()...)
	before := readRecipientCursors(t, env, "RedStone")

	env.run(t, "publish-inbox", append(closureInboxOfTen(), inboxArg(11, "RedStone", "new one"))...)
	after := readRecipientCursors(t, env, "RedStone")
	if len(after) != 11 || !slices.Equal(after[:10], before) || after[10] <= before[9] {
		t.Fatalf("cursors %v -> %v", before, after)
	}
}
```

##### 🟢 Green

No production code beyond Behavior 10.

##### 🔵 Refactor

- [ ] **No duplication:** shares helpers with Behavior 11.
- [ ] **Reveals intent:** the test says what's new.
- [ ] **Complexity went down:** no production change.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** same helper design.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_NewMessageAfterRestartGetsOneEvent ./internal/cli/`, with red-at-seam recorded.

### Behavior 14: Mail held during an outage is saved once [LEAF: feed only, driven by an injected clock]

#### Test Specification

**Given** a message held in memory during an outage. **When** it's published again during the outage. **Then** there's still one memory-only copy. **When** the store recovers and the next poll publishes it. **Then** it's saved once, its memory-only copy is dropped, it isn't delivered again, and replay shows it once.

**Edge Cases:**
- If another process saved it first, the memory-only copy is dropped the same way.
- A store write failing partway through a poll leaves later messages memory-only, with one failure report.
- Only one write attempt per retry interval, however many messages a poll holds.

**Property:** no property here. Ordering across recovery is covered by Loop 1's `TestAttentionFeed_ReplayOrderProperty`, which keeps passing.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAppendAddressed_OutageThenRecovery(t *testing.T) {
	captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)
	var delivered atomic.Int32
	unsubscribe := feed.Subscribe(func(AttentionEvent) { delivered.Add(1) })
	defer unsubscribe()

	store.fail.Store(true)
	for i := 0; i < 3; i++ {
		feed.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")
	}
	events, _, err := feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("during outage: %+v", events)
	}

	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	feed.PublishMailPending("/proj", "Sender", "RedStone", "held", 42, "")

	events, _, err = feed.ReplayForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("after recovery: %+v", events)
	}
	if got := delivered.Load(); got != 1 {
		t.Fatalf("delivered %d times, want 1", got)
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
	if len(saved) != 1 {
		t.Fatalf("another process sees %+v, want the held message", saved)
	}
}
```

`faultInjectingStore` overrides `AppendAddressedAttentionEvent` as well as `AppendAttentionEvent`.

##### 🟢 Green

**File:** `internal/robot/attention_feed.go`

```go
// forgetMemoryOnlyLocked drops the memory-only copy of an addressed event, now
// that the store holds it. It reports whether there was one, which means
// subscribers have already seen this message.
func (f *AttentionFeed) forgetMemoryOnlyLocked(project, agent string, messageID int64) bool
```

`AppendAddressed` calls it after a created or conflict result. A created event with a dropped copy isn't delivered again, and it reports `created` false. While waiting to retry, `AttentionJournal.FindAddressed` finds an earlier copy, so a repeat adds nothing.

`AttentionJournal.FindAddressed(project, agent, messageID)` scans newest first. Its size is capped, so the scan is bounded. It runs only with no store or during a retry wait.

##### 🔵 Refactor

- [ ] **No duplication:** one key comparison, `sameAddressedKey`, used by the journal and memory-only functions.
- [ ] **Reveals intent:** the comment says what a dropped copy means.
- [ ] **Complexity went down:** the outage rules stay in Loop 1's functions.
- [ ] **No shallow wrappers:** `FindAddressed` holds the scan under the journal's lock.
- [ ] **Fits existing patterns:** Loop 1's `memoryOnlySince`.

#### Success Criteria

**Automated:** `go test -short -race -run TestAppendAddressed_OutageThenRecovery ./internal/robot/` fails first, then passes.

### Behavior 15: A feed with no store still records a message once [LEAF: feed and dashboard model, in one process]

#### Test Specification

**Given** a dashboard with a feed that has no store. **When** two polls see one message. **Then** there's one event.

**Edge Cases:** a pane with no known agent name publishes nothing. An unread message that drops out of the inbox and returns isn't recorded again while it's in the journal.

**Property:** no property: scripted steps.

**Files touched:** `internal/tui/dashboard/dashboard_layout_test.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestDashboardMailPoll_NoStoreRecordsOnce(t *testing.T) {
	lockAttentionFeedForTest(t)
	feed := robot.NewAttentionFeed(robot.AttentionFeedConfig{JournalSize: 16, RetentionPeriod: time.Hour})
	t.Cleanup(feed.Stop)
	installFeedForWarningTest(t, feed)

	poll := func(m Model, gen uint64) Model {
		updated, _ := m.Update(AgentMailInboxSummaryMsg{
			ProjectKey: "proj",
			Inboxes: map[string][]agentmail.InboxMessage{
				"1": {{ID: 42, Subject: "unread"}},
				"2": {{ID: 43, Subject: "no known agent"}},
			},
			AgentMap: map[string]string{"1": "RedStone"},
			Gen:      gen,
		})
		return updated.(Model)
	}
	m := newTestModel(200)
	m = poll(m, 1)
	m = poll(m, 2)

	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].MessageID != 42 {
		t.Fatalf("events = %+v", events)
	}
}
```

##### 🟢 Green

`AppendAddressed` with no store configured uses `AttentionJournal.FindAddressed`, then appends through the normal path. No separate code beyond Behavior 14.

##### 🔵 Refactor

- [ ] **No duplication:** the same lookup as during an outage.
- [ ] **Reveals intent:** the test name says "no store".
- [ ] **Complexity went down:** one lookup covers both cases.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the dashboard tests' `lockAttentionFeedForTest`.

#### Success Criteria

**Automated:** `go test -short -run TestDashboardMailPoll_NoStoreRecordsOnce ./internal/tui/dashboard/` fails first, then passes.

### Human test script

1. Keep the session and unread mail from Sprint 1, or set them up the same way.
2. Count mail events that name a message, because other code paths write unaddressed mail events: `sqlite3 -readonly ~/.config/ntm/state.db "select count(*) from attention_events where event_type in ('mail.received','mail.ack_required') and message_id is not null"`.
3. Open `/tmp/ntm-loop2 dashboard addrtest`, wait for the ticker's `Mail:` unread count and at least one more 30 second poll, then quit.
4. Run the same count.
5. Pass: the number hasn't changed.
6. Send one more message: `/tmp/ntm-loop2 mail send addrtest --to <first> "new one"`.
7. Open the dashboard, wait for the ticker's `Mail:` unread count to rise, then quit.
8. Run the same count.
9. Pass: the number went up by exactly 1.

---

## Integration & E2E Testing

- **Integration:** all 6 closure tests run in `go test -short ./internal/cli/`.
- **E2E:** the `e2e/` tests for this project come in Loop 9.

## Verification Commands

Run after each sprint, with Go on the PATH:

```bash
export PATH=$PATH:/usr/local/go/bin
go build ./cmd/ntm
go test -short ./...
go test -short -race ./internal/robot/ ./internal/cli/ ./internal/state/ ./internal/tui/dashboard/
gofmt -l .
go vet ./internal/robot/ ./internal/cli/ ./internal/state/ ./internal/tui/dashboard/
ubs <changed-files>
bun ~/.claude/SAI/Tools/TestTamperCheck.ts --base <commit before the sprint>
# Tests must not open the real state.db. Comparing its modified time isn't enough, because
# an open with no pending migration writes nothing. Run each package with HOME and
# XDG_CONFIG_HOME set to a throwaway folder, and check that no state.db appears there.
```

Don't edit code files while these run, because a test build reads them. Run `golangci-lint run` and `goimports -l .` if they're installed. Report them as not run if they aren't. The tests known to fail before this loop are listed in bead `bd-5zbcm`.

## References

- Research: `thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`
- Closure framework: `~/.claude/commands/references/closure-test-framework.md`
- Patterns: Loop 1 plan `01-durable-feed-tdd.md`; `internal/cli/attention_closure_test.go` (helper processes); `internal/robot/attention_feed_test.go` (`openStoreFile`, `faultInjectingStore`, `newTestClock`); `internal/state/migrations/008_attention_reason_code.sql` (adding a column)
