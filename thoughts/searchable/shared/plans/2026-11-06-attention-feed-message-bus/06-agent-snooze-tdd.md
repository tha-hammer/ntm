# Loop 6: Agent snooze — TDD plan

Loop document: [06-agent-snooze.md](06-agent-snooze.md). Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Agent Snooze**, rule **One Telling**.

Epic `bd-atnqa`. Sprint beads `bd-atnqa.1` and `bd-atnqa.2`.

## Overview

An agent told about a message can defer it with `ntm mail snooze <id> --until <time>`. While the snooze is active, the Stop hook and the idle nudge leave the message out. Once it ends, the next reminder includes it once, and **One Telling** applies again.

The behaviors are 15 observable results in 2 sprints:
- **Sprint 1 (Behaviors 1 to 8):** saving a snooze and hiding the message.
- **Sprint 2 (Behaviors 9 to 15):** bringing the message back once when the snooze ends.

## Current State Analysis

- **Snooze state exists for operators only.**
  - `AttentionItemState` has `State` `snoozed` and `SnoozedUntil` (`internal/state/runtime_schema.go:430`, `:448`), stored in `attention_item_states` (migration `014_attention_item_state.sql`, with an index on `snoozed_until`).
  - `ntm serve` sets it through `applyAttentionItemAction`'s `"snooze"` case (`internal/serve/server.go:5659-5676`). That case parses RFC3339, rejects a time that isn't after now, and saves the state plus an audit event and decision in one transaction (`server.go:5800-5860`).
- **Item state is per event.** `ResolveAttentionItemKey` (`runtime_store.go:2028`) keys an item by its dedup key, or `cursor:<n>` without one (`attentionItemKeyForEvent`, `:1879`). Mail events have no dedup key, and each is addressed to one recipient (`AppendAddressed`, `attention_feed.go:905`). So a snooze on BlueLake's event can't touch RedStone's.
- **Mail events are found by project, recipient and message.** `GetAddressedAttentionEvent` (`runtime_store.go:1666`) does the lookup. `RecordMailNotice` (`attention_feed.go:2504`) writes the event, with `from` and `subject` in its details.
- **Told records:** `ClaimToldRecord`, `ReleaseToldRecord` and `GetToldRecord` (`runtime_store.go:2285-2350`), kept for 7 days (`ToldRecordRetention`, `:2257`).
- **The hook and the nudge each claim on their own.**
  - The hook: `claimUnread` (`internal/cli/agent_hook.go:215`) records then claims each unread message.
  - The nudge: `(*idleNudger).nudge` and `claim` (`internal/cli/idle_nudge.go`).
  - Neither knows about snoozes.
- **The reminder shows no message IDs.** `buildReminderText` (`agent_hook.go:257`) lists sender and subject, then says only to run `fetch_inbox`.
- **Agent Mail reports no read state.**
  - The server's `fetch_inbox` payload (`_list_inbox` and `_message_to_dict`, mcp_agent_mail `app.py:4193`, `:1759`) has no `read_ts` or recipients. Neither does `resource://message/{id}` (`app.py:11887`).
  - So `InboxMessage.ReadAt` (`internal/agentmail/types.go:137`) is always empty from the real server.
  - The loop's "read during the snooze isn't announced" rule was dropped at 4c (see **Scope change**).
- **The only per-agent ownership check** is the agent's own `fetch_inbox`, which lists only messages addressed to that agent (`_list_inbox` joins `MessageRecipient` on the agent). Its limit can go up to 1000 (`app.py:8316-8326`).
- **Existing pieces to reuse:**
  - Identity: `agentmail.ResolveHookIdentity` (`internal/agentmail/session.go:464`), the **Agent Identity Order**.
  - Project: `stopHookProject` (`agent_hook.go:119`).
  - Time: `util.ParseDuration` (`internal/util/duration.go:21`).
  - The binary's path: `resilience.CurrentExecutablePath`, which the hook installer uses.
  - JSON output: `IsJSONOutput` (`root.go:5019`) and `encodeJSONResult` (`mail.go:1015`).

### Key Discoveries

- Events last 7 days from when they're recorded (`attentionEventRetention`, `attention_feed.go:119`), not from the snooze. A snooze must end before its own event expires, or the event, and so the snooze, is gone when it ends.
- `claimUnread`'s `msg.ReadAt != nil` skip never fires in production, because the server sends no read time. That's beaded as a separate issue; Loop 6 doesn't depend on it.
- The closure harness from Loops 3 to 5 covers everything here. That's `closureEnv`, `fakeAgentMail`, `screenAgents`, `spawnNudgeAgent`, `startMonitor`, `runStopHook`, `told` and `assertNoNudge`, in `internal/cli/*_test.go`.

## Desired End State

### Observable Behaviors

1. Given `30m`, `1d` or an RFC3339 time, when `--until` is parsed, then it's one UTC instant. Given `soon`, then an error naming both forms.
2. Given a time at or before now, when any snooze is validated, then an error. Serve and the command share the check.
3. Given BlueLake's and RedStone's events for message 42, when BlueLake's is snoozed, then only BlueLake's item is snoozed. Snoozing again replaces the time.
4. Given an agent's snoozed messages, when listed at now, then each is either active or ended, and no other agent's appear.
5. Given an agent and a message, when `ntm mail snooze <id> --until <time>` runs, then the snooze is saved and confirmed, or an error changes nothing.
6. Given an active snooze and new mail, when the turn ends, then the Stop hook names only the new mail.
7. Given an active snooze and new mail, when the monitor polls an idle agent, then the nudge names only the new mail.
8. Given any reminder, then each entry shows its message ID, and the last line gives the full snooze command.
9. Given a time more than 7 days ahead, or after the message's event expires, then an error naming the limit.
10. Given an ended snooze, when two callers claim it, then exactly one wins. Clearing the snooze and replacing the told record happen together.
11. Given ended snoozes and new mail, when the hook or nudge collects what to announce, then one list has both, with no duplicates.
12. Given a snooze that ends while the agent is idle, then one reminder includes it, and nothing more follows.
13. Given a snooze that ends during a turn, then the turn end tells it, and the nudge doesn't repeat it.
14. Given a monitor restart while a snooze is active, then the message stays held and returns on time.
15. Given a snoozed message that Agent Mail lists as read, or no longer lists, then it still returns when the snooze ends.

## What We're NOT Doing

- Any change to the `mcp_agent_mail` server (scope exclusion). So there's no read-state check.
- An `--agent` flag. An agent snoozes only its own mail (decided at 4c).
- Marking returned messages differently in the reminder (loop document assumption).
- Unsnooze or list commands. Snoozing again replaces the time.
- Fixing the dead `ReadAt` skip in `claimUnread`. That's beaded separately.

## Scope change (4c, 2026-11-09)

The loop document's Sprint 2 criterion "A message read or acknowledged during the snooze isn't announced" is replaced, by your choice at 4c. The new criterion is "A snoozed message returns when its snooze ends, whether or not it was read, because Agent Mail reports no read state." The loop document is updated to match. Behavior 15 covers the new criterion.

## Testing Strategy

- **Framework:** Go `testing`, with `pgregory.net/rapid` for properties. Both are already used in the repo.
- **Test types:**
  - Unit: Behaviors 1, 2, 8 and 9.
  - Store integration on real SQLite in temp folders: Behaviors 3, 4 and 10.
  - The poll and the hook with fakes: Behaviors 11 and 15.
  - Closure: Behaviors 5, 6, 7, 12, 13 and 14.
- **Setup:**
  - Closure tests run `Execute` in helper processes, on a private tmux server, with the fake Agent Mail server and stand-in agents. That's the Loop 3 to 5 harness.
  - The real store is under the closure env's `XDG_CONFIG_HOME`.
  - Snoozes in closure tests use `--until 2s`, so the real clock passes them.
- **How tests run:** `go test -short ./internal/state/ ./internal/serve/ ./internal/cli/`, plus the sprint verification list at the end.

## Workflow Closure

The research has no chain for snoozing, because it doesn't exist yet. Its sources and sinks come from Chain A (Agent Mail to the hook, Loop 3) and from Loop 5's nudge chain.

- **`highest_new_connector`:**
  - Sprint 1: the `ntm mail snooze` command and the snooze filter in the shared announce step.
  - Sprint 2: `ClaimEndedSnooze` in that step.
- **Trigger choice:** `Execute` with `mail snooze`, with `spawn`, with `internal-monitor`, and the written hook command, each in a helper process. The only direct seeds are messages on the fake server.

### Production Operation Chain

`ntm mail snooze <id> --until T -> ResolveHookIdentity + stopHookProject -> GetAddressedAttentionEvent (or fetch_inbox ownership check + RecordMailNotice) -> ValidateSnoozeUntil -> Store.SnoozeAddressedMessage (item state + audit, one tx) -> state.db -> [hook: runAgentHookStop | nudge: runIdleNudgeLoop -> nudge] -> FetchInbox -> mailToAnnounce (Store.SnoozedMessages: drop active, add ended) -> ClaimToldRecord / ClaimEndedSnooze -> buildReminderText -> hook decision | SendKeysForAgentDoubleEnter -> agent`

**Production callers:**

| New code | Production caller |
|---|---|
| `newMailSnoozeCmd`, `runMailSnooze`, `parseSnoozeUntil` | `newMailCmd` (`mail.go:24`) |
| `state.ValidateSnoozeUntil`, `state.SnoozeAttentionItem` | `applyAttentionItemAction` (serve), `Store.SnoozeAddressedMessage` |
| `Store.SnoozeAddressedMessage` | `runMailSnooze` |
| `Store.SnoozedMessages` | `mailToAnnounce` |
| `Store.ClaimEndedSnooze`, `Store.ReleaseEndedSnoozeClaim` | `mailToAnnounce` callers: `claimUnread` (hook) and `(*idleNudger).nudge` |
| `mailToAnnounce` | `claimUnread`, `(*idleNudger).nudge` |
| reminder ID and snooze line | `buildReminderText`, called by the hook and the nudge |

### Closure Test: An agent snoozes its own message, and gets errors that change nothing [BLOCKING]

Behavior 5.
- **SOURCE (seed only)**: messages in the fake server's inboxes for BlueLake and RedStone.
- **TRIGGER (start)**: `Execute` with `mail snooze <id> --until <t>` in a helper process, with `AGENT_NAME` and the work folder set (boundary = highest_new_connector, the command).
- **DRIVERS (async edges)**: none. The command is synchronous.
- **OBSERVE (assert via)**:
  - The command's stdout, and its exit code through `tryRun`.
  - `Store.SnoozedMessages` through a `read-snoozes` helper mode, which is the production read the hook uses.
- **FORBIDDEN SPAN**: `runMailSnooze`'s internals, `SnoozeAddressedMessage`, `UpsertAttentionItemState`, raw SQL.
- **RED-AT-SEAM proof**: the subcommand isn't registered in `newMailCmd`. Expected: "unknown command "snooze"".
- **DRIVABILITY**: the store path comes from the env's `XDG_CONFIG_HOME`, and the Agent Mail URL from the config.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. There's no skip path.

### Closure Test: The Stop hook leaves out a snoozed message [BLOCKING]

Behavior 6.
- **SOURCE (seed only)**: two messages for the spawned agent on the fake server.
- **TRIGGER (start)**:
  - `Execute` with `spawn` (nudge off), then `Execute` with `mail snooze` for the first message.
  - Then the hook command that spawn wrote, run with `runStopHook`.
- **DRIVERS (async edges)**: none. Each step is a process that runs to its end.
- **OBSERVE (assert via)**: the hook's stdout decision.
- **FORBIDDEN SPAN**: `mailToAnnounce`, `SnoozedMessages`, `claimUnread`, `buildReminderText`.
- **RED-AT-SEAM proof**: `claimUnread` doesn't call `mailToAnnounce`. Expected: the reason names the snoozed subject.
- **DRIVABILITY**: as in Loop 3.
- **EXECUTION (must run)**: `go test -short ./internal/cli/`.

### Closure Test: The nudge leaves out a snoozed message [BLOCKING]

Behavior 7.
- **SOURCE (seed only)**: messages on the fake server.
- **TRIGGER (start)**: `Execute` with `spawn`, then `mail snooze`, then `internal-monitor`.
- **DRIVERS (async edges)**: the monitor's real 1-second poll.
- **OBSERVE (assert via)**:
  - The stand-in's `typed` file.
  - The told records, through the `told` helper.
  - The monitor log.
- **FORBIDDEN SPAN**: `idleNudger.nudge`, `mailToAnnounce`, the claims.
- **RED-AT-SEAM proof**: `nudge` doesn't call `mailToAnnounce`. Expected: the reminder names the snoozed subject.
- **DRIVABILITY**: the interval comes from the manifest.
- **EXECUTION (must run)**: `go test -short ./internal/cli/`.

### Closure Test: A snooze that ends while the agent is idle returns once [BLOCKING]

Behavior 12.
- **SOURCE (seed only)**: one message on the fake server.
- **TRIGGER (start)**: `Execute` with `spawn`, then `mail snooze <id> --until 2s`, then `internal-monitor`.
- **DRIVERS (async edges)**: the real clock passing the snooze, and the monitor's poll.
- **OBSERVE (assert via)**:
  - The `typed` file: nothing before the snooze ends, one reminder after, nothing more 3 seconds later.
  - Then `runStopHook`, which prints nothing.
- **FORBIDDEN SPAN**: `SnoozedMessages`, `ClaimEndedSnooze`, the poll.
- **RED-AT-SEAM proof**: `mailToAnnounce` drops ended snoozes as well as active ones. Expected: no reminder within 10 seconds.
- **DRIVABILITY**: wall clock only. The 2-second snooze keeps it fast.
- **EXECUTION (must run)**: `go test -short ./internal/cli/`.

### Closure Test: A snooze that ends mid-turn is told at the turn end, once [BLOCKING]

Behavior 13.
- **SOURCE (seed only)**: one message.
- **TRIGGER (start)**:
  - `spawn`, then `mail snooze --until 2s`, then `internal-monitor`, with the stand-in showing `cc_working.txt`.
  - Then `runStopHook`, then the idle screen.
- **DRIVERS (async edges)**: the clock and the poll.
- **OBSERVE (assert via)**:
  - The hook decision names the message.
  - The `typed` file stays free of a reminder after the screen turns idle.
- **FORBIDDEN SPAN**: as Behavior 12.
- **RED-AT-SEAM proof**: the hook path doesn't use `mailToAnnounce`. Expected: the hook prints nothing, because the message was already told before the snooze.
- **DRIVABILITY / EXECUTION**: as Behavior 12.

### Closure Test: A monitor restart keeps the snooze [BLOCKING]

Behavior 14.
- **SOURCE (seed only)**: one message.
- **TRIGGER (start)**:
  - `spawn` and `mail snooze --until 4s`, then `internal-monitor`.
  - Stop the monitor, then start a new one.
- **DRIVERS (async edges)**: the clock and both monitors' polls.
- **OBSERVE (assert via)**: the `typed` file, with nothing before the time and one reminder after.
- **FORBIDDEN SPAN**: as Behavior 12.
- **RED-AT-SEAM proof**: keep snoozes in the nudger's memory instead of the store. Expected: the second monitor types it at once.
- **DRIVABILITY / EXECUTION**: as Behavior 12.

---

## Sprint 1: Snooze a message

**Bead:** `bd-atnqa.1`.

**Testable function:** "Run `ntm mail snooze <id> --until <time>`, and see no reminder mention that message before the time. See an error for another agent's message or a past time."

**Order of work:** 1, 2, 3, 4, 8, then 5 as the outer test, then 6 and 7.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 5, 6 |
| `ntm mail snooze <message-id> --until <time>` saves the snooze on that agent's event, and confirms the saved time | 3, 5 |
| The Stop hook and the nudge leave the message out until the time passes | 4, 6, 7 |
| A message addressed to another agent, an unknown message id, or a time in the past returns an error and changes nothing | 2, 5 |
| Snoozing the same message again replaces the time | 3, 5 |
| Any message can be snoozed, whatever its importance, and other messages in the same batch are unaffected | 6, 7 |
| The `ReminderText` instruction line tells agents about this command, with the exact syntax (requirement) | 8 |
| `--until` takes RFC3339 or a duration; bad times name the accepted forms (requirement, test scenario "Bad time") | 1 |
| No identity: an error explaining that the agent's name wasn't found (test scenario) | 5 |

### Behavior 1: `--until` takes an RFC3339 time or a duration [LEAF: pure function, one package]

#### Test Specification

**Given** `now` and a raw value. **When** `parseSnoozeUntil(raw, now)` runs. **Then**:

| Raw | Result |
|---|---|
| `30m` | now + 30 minutes, in UTC |
| `1d`, `2h`, `45s` | now plus that, in UTC (`util.ParseDuration`) |
| `2026-11-09T15:30:00-05:00` | 20:30 UTC |
| `soon`, `""`, `30x` | an error: "--until must be an RFC3339 time (2026-11-09T15:30:00Z) or a duration (30m, 2h, 1d)" |

**Edge Cases:** surrounding spaces are trimmed. A negative duration parses, and Behavior 2 rejects it.

**Property:** Roundtrip. For any duration d from 1 second to 7 days, `parseSnoozeUntil(d.String(), now) == now.Add(d)`. For any instant t, `parseSnoozeUntil(t.Format(time.RFC3339), now)` is t truncated to the second.

```go
rapid.Check(t, func(rt *rapid.T) {
	d := time.Duration(rapid.Int64Range(1, 7*24*3600).Draw(rt, "seconds")) * time.Second
	if got, err := parseSnoozeUntil(d.String(), now); err != nil || !got.Equal(now.Add(d)) {
		rt.Fatalf("%s: got %v, %v", d, got, err)
	}
})
```

**Files touched:** `internal/cli/mail.go`, `internal/cli/mail_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestParseSnoozeUntil` (table) and `TestParseSnoozeUntil_RoundTripProperty`. Expected failure: `undefined: parseSnoozeUntil`.

##### 🟢 Green

```go
// parseSnoozeUntil reads --until as an RFC3339 time or a duration from now.
func parseSnoozeUntil(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	if d, err := util.ParseDuration(raw); err == nil {
		return now.Add(d).UTC(), nil
	}
	return time.Time{}, errSnoozeUntilForm
}
```

##### 🔵 Refactor

- [ ] **No duplication:** durations go through `util.ParseDuration` only.
- [ ] **Reveals intent:** the error text lists both forms with an example of each.
- [ ] **Complexity went down:** two attempts, one error.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `util.ParseDuration` callers in `internal/cli`.

#### Success Criteria

**Automated:** `go test -short -run TestParseSnoozeUntil ./internal/cli/` fails first, then passes.

### Behavior 2: Every snooze must end in the future, checked in one place [LEAF: pure function in `state`]

#### Test Specification

**Given** `now`. **When** `state.ValidateSnoozeUntil(until, now)` runs. **Then** it returns an error ("snooze time must be in the future") for `until <= now`, and nil after now. **When** serve's `applyAttentionItemAction` runs with `snooze` and a past `until`. **Then** it returns the same error, and serve's existing snooze tests stay green.

**Edge Cases:** `until == now` is rejected.

**Property:** Invariant: for times from now - 1h to now + 1h in seconds, `ValidateSnoozeUntil(until, now) == nil` exactly when `until.After(now)`.

**Files touched:** `internal/state/runtime_store.go` (next to the item state functions), `internal/state/runtime_store_test.go`, `internal/serve/server.go`.

#### TDD Cycle

##### 🔴 Red

`TestValidateSnoozeUntil` and `TestValidateSnoozeUntil_Property`. Expected failure: `undefined: ValidateSnoozeUntil`.

##### 🟢 Green

```go
// ValidateSnoozeUntil is the snooze time rule for every caller.
func ValidateSnoozeUntil(until, now time.Time) error {
	if !until.After(now) {
		return ErrSnoozeNotInFuture
	}
	return nil
}

// SnoozeAttentionItem sets an item to snoozed until the given time.
func SnoozeAttentionItem(item *AttentionItemState, until, now time.Time) error {
	if err := ValidateSnoozeUntil(until, now); err != nil {
		return err
	}
	u := until.UTC()
	item.State, item.SnoozedUntil, item.DismissedAt, item.DismissedBy = AttentionStateSnoozed, &u, nil, ""
	return nil
}
```

Serve's `"snooze"` case keeps its RFC3339 parse and calls `state.SnoozeAttentionItem`.

##### 🔵 Refactor

- [ ] **No duplication:** serve's own future check is gone.
- [ ] **Reveals intent:** one exported error, `ErrSnoozeNotInFuture`.
- [ ] **Complexity went down:** serve's case shrinks.
- [ ] **No shallow wrappers:** `SnoozeAttentionItem` holds the state change, not just the check.
- [ ] **Fits existing patterns:** the item state helpers in `runtime_store.go:1960-2030`.

#### Success Criteria

**Automated:** `go test -short -run 'Snooze' ./internal/state/ ./internal/serve/` fails first, then passes.

### Behavior 3: A snooze is saved on one agent's event, and replaces an earlier one [LEAF: one package, real SQLite]

#### Test Specification

**Given** a store with addressed events for (project P, BlueLake, 42) and (P, RedStone, 42). **When** `Store.SnoozeAddressedMessage(SnoozeRequest{P, "BlueLake", 42, T1, now})` runs. **Then**:
- BlueLake's item state is `snoozed` until T1;
- RedStone's item has no state;
- an audit event with actor type `agent`, actor `BlueLake` and type `snooze` is written in the same transaction.

**When** it runs again with T2. **Then** the snooze time is T2. **Given** no event for (P, BlueLake, 99). **Then** it returns `ErrNoAddressedEvent` and writes nothing.

**Edge Cases:** the agent name matches without case, as the event lookup does.

**Property:** Idempotent: snoozing twice with the same time leaves the same item state as snoozing once (apart from `updated_at`).

**Files touched:** `internal/state/runtime_store.go`, `internal/state/runtime_store_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestSnoozeAddressedMessage`. Expected failure: `undefined: SnoozeRequest`.

##### 🟢 Green

`SnoozeAddressedMessage` takes the lock and does the following in one `Transaction`:
1. Finds the event (`getAddressedAttentionEventLocked`) and resolves its item key (`attentionItemKeyForEvent`).
2. Loads the prior state, calls `SnoozeAttentionItem`, and saves it with `tx.UpsertAttentionItemState`.
3. Writes `tx.RecordAuditEvent`, with category `attention`, entity `attention_item` and the item key.

It returns the saved time.

##### 🔵 Refactor

- [ ] **No duplication:** the state change is `SnoozeAttentionItem`.
- [ ] **Reveals intent:** `SnoozeRequest` names each field.
- [ ] **Complexity went down:** one transaction, with no partial writes.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** serve's transaction at `server.go:5800`.

#### Success Criteria

**Automated:** `go test -short -run TestSnoozeAddressedMessage ./internal/state/` fails first, then passes.

### Behavior 4: An agent's snoozes are listed as active or ended [LEAF: store query, real SQLite]

#### Test Specification

**Given** BlueLake's events for 41 (snoozed until now + 1h), 42 (snoozed until now - 1m) and 43 (no snooze), and RedStone's event for 44 (snoozed until now + 1h). **When** `Store.SnoozedMessages(P, "BlueLake", now)` runs. **Then**:
- **active** is [41] and **ended** is [42];
- each entry has the message's `from` and `subject` from the event details, and its snooze time;
- 43 and 44 appear in neither list.

**Edge Cases:**
- An item cleared back to `new` appears in neither.
- An expired event's snooze appears in neither.

**Property:** Invariant: for 0 to 8 generated messages per agent across two agents, with random snooze offsets of ±2h, each snoozed message of the agent is in exactly one list, in `active` exactly when `until > now`, and no other agent's message appears.

**Files touched:** `internal/state/runtime_store.go`, `internal/state/runtime_store_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestSnoozedMessages` and `TestSnoozedMessages_PartitionProperty`. Expected failure: `undefined: SnoozedMessage`.

##### 🟢 Green

One query joins `attention_events` (recipient project, recipient agent without case, unexpired) to `attention_item_states` on the event's item key, where the state is `snoozed`. It reads `from` and `subject` from `details`. The rows are split by `snoozed_until` against now.

##### 🔵 Refactor

- [ ] **No duplication:** the item key expression matches `attentionItemKeyForEvent`, with a comment tying them together.
- [ ] **Reveals intent:** two named result lists.
- [ ] **Complexity went down:** one query.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `GetAttentionEventsForRecipient` (`runtime_store.go:1731`).

#### Success Criteria

**Automated:** `go test -short -run TestSnoozedMessages ./internal/state/` fails first, then passes.

### Behavior 8: Reminders show message IDs and the snooze command [LEAF: pure function]

#### Test Specification

**Given** messages 71 (RedStone, "first") and 72. **When** `buildReminderText` runs for BlueLake in project P. **Then**:
- entries read `1) [#71] from RedStone: first`;
- the existing `fetch_inbox` line stays;
- a last line reads: `To defer one, run "<ntm path>" mail snooze <id> --until 30m --project "<P>" (or an RFC3339 time).`

The ntm path is `resilience.CurrentExecutablePath()`, the same one the hook registration uses. The Loop 3 and 5 tests that check the reminder text are updated to the new form.

**Edge Cases:**
- The count line and the 5-entry limit don't change.
- A project with spaces stays quoted.

**Property:** Invariant: for 1 to 8 generated messages with random subjects (any runes), every listed message's `[#id]` appears exactly once, and each entry is one line.

**Files touched:** `internal/cli/agent_hook.go`, `internal/cli/agent_hook_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestBuildReminderText_IDsAndSnoozeLine` and `TestBuildReminderText_IDsProperty`. Expected failure: no `[#71]` in the text.

##### 🟢 Green

Add `[#%d] ` to each entry and a final `fmt.Fprintf` with the path, `%q` project and the example duration.

##### 🔵 Refactor

- [ ] **No duplication:** the ntm path comes from the function the hook installer uses.
- [ ] **Reveals intent:** the snooze line is its own constant format.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the **Reminder Format** rule. Senders and subjects stay cleaned.

#### Success Criteria

**Automated:** `go test -short -run 'TestBuildReminderText' ./internal/cli/` fails first, then passes.

### Behavior 5: `ntm mail snooze` saves an agent's snooze, or errors and changes nothing [BLOCKING]

#### Test Specification

**Given** the fake server with message 81 in BlueLake's inbox and 82 in RedStone's, and a work folder. **When** `Execute` with `mail snooze <id> --until <t>` runs in a helper process, with the environment below. **Then**:

| Environment and args | Result |
|---|---|
| `AGENT_NAME=BlueLake`, 81, `30m` | exit 0. Prints `Snoozed message 81 until <UTC time> (in 30m).` `SnoozedMessages` shows 81 active. The feed event for 81 is recorded first |
| same, again with `2h` | the later time applies |
| same, with `--json` | `{"success":true,"message_id":81,"agent":"BlueLake","project":"<P>","snoozed_until":"<RFC3339>"}` |
| `AGENT_NAME=BlueLake`, 82 | error "message 82 isn't in BlueLake's Agent Mail inbox". Nothing saved |
| `AGENT_NAME=BlueLake`, 999 | the same error, for 999 |
| `AGENT_NAME=BlueLake`, 81, `2020-01-01T00:00:00Z` | error "snooze time must be in the future". The earlier snooze stands |
| `AGENT_NAME=BlueLake`, 81, `soon` | error naming both forms |
| no `AGENT_NAME` or breadcrumb | error "no Agent Mail name found: set AGENT_NAME or run inside an ntm-spawned agent" |
| `AGENT_NAME=SomeoneElse`, 81 | error "message 81 isn't in SomeoneElse's Agent Mail inbox", and 81's snooze unchanged |

**Edge Cases:**
- With a feed event already present, the command needs no Agent Mail call. The test checks the fake server's call log.
- `--project` overrides the project, then `MCP_AGENT_MAIL_PROJECT`, then the folder, as the hook does.

**Property:** no property: fixed command runs.

**Files touched:** `internal/cli/mail.go` (new `newMailSnoozeCmd` and `runMailSnooze`, registered in `newMailCmd`), `internal/cli/mail_snooze_test.go` (new closure test file), `internal/cli/attention_closure_test.go` (`read-snoozes` helper mode).

#### TDD Cycle

##### 🔴 Red

`TestClosure_MailSnoozeCommand`. Expected failure: "unknown command "snooze" for "ntm mail"".

##### 🟢 Green

`runMailSnooze` does the following, in order:
1. Parse the id (a positive int) and `--until` (Behavior 1), then call `ValidateSnoozeUntil` (Behavior 2).
2. Find the identity with `ResolveHookIdentity`, setting its token on the client as the hook does. Find the project with `stopHookProject`.
3. Open the process store, and look up `GetAddressedAttentionEvent`.
4. If there's no event, call `FetchInbox` with a limit of 1000 and look for the id. If it's found, record it with `RecordMailNotice`. If not, return the inbox error.
5. Call `SnoozeAddressedMessage`.
6. Print the confirmation line or the JSON.

##### 🔵 Refactor

- [ ] **No duplication:** identity, project and token handling are shared with `runAgentHookStop`. Extract `resolveAgentHookScope` if both need the same three steps.
- [ ] **Reveals intent:** each error says what to do.
- [ ] **Complexity went down:** the lookup and record-first paths share one snooze call.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `newMailMarkCmd` (`mail.go:516`), `encodeJSONResult`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_MailSnoozeCommand ./internal/cli/` fails first, then passes.

### Behavior 6: The Stop hook leaves out an actively snoozed message [BLOCKING]

#### Test Specification

**Given** a spawned stand-in (nudge off, no monitor), with mail 91 "snooze me" (normal importance) and 92 "second" (high importance) on the fake server. 91 is snoozed with `Execute mail snooze 91 --until 30m` as that agent. **When** the written hook command runs. **Then** the reason names `second` and `[#92]`, and doesn't name `snooze me`. **Given** only 91. **Then** the hook prints nothing.

**Edge Cases:**
- 91 was never told before it was snoozed, so the told record alone wouldn't hide it.
- When the store can't be opened, the hook tells everything unread (**Fail Open**), and a log line says snoozes weren't checked.

**Property:** covered by Behavior 11's invariant through the shared step.

**Files touched:** `internal/cli/agent_hook.go` (`claimUnread` uses `mailToAnnounce`), `internal/cli/idle_nudge.go` (`mailToAnnounce` lives next to the nudge's claim), `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestClosure_HookSkipsSnoozed`. Expected failure: the reason names `snooze me`.

##### 🟢 Green

```go
// mailToAnnounce is the step the hook and the nudge share: it drops messages
// whose snooze is active, and (Sprint 2) adds those whose snooze has ended.
func mailToAnnounce(store snoozeReader, project, agent string, msgs []agentmail.InboxMessage, now time.Time) ([]announceItem, error)
```

`claimUnread` filters through it before claiming.

##### 🔵 Refactor

- [ ] **No duplication:** one step for both callers.
- [ ] **Reveals intent:** named after the scope's "messages to announce" step.
- [ ] **Complexity went down:** `claimUnread` loses no branches, and gains one call.
- [ ] **No shallow wrappers:** `snoozeReader` is the one-method store slice.
- [ ] **Fits existing patterns:** `idleNudgeStore`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_HookSkipsSnoozed ./internal/cli/` fails first, then passes.

### Behavior 7: The nudge leaves out an actively snoozed message [BLOCKING]

#### Test Specification

**Given** a spawned idle stand-in, with mail 93 snoozed for 30 minutes and 94 new. **When** the monitor runs. **Then** one reminder names `[#94]` and not 93, and the told record exists for 94 only. **Given** only 93. **Then** after 3 seconds nothing is typed, there's no told record for 93, and the fake server saw `fetch_inbox` calls (the poll ran).

**Edge Cases:** the pane check is skipped when all mail is snoozed, as it is when there's no mail.

**Property:** covered by Behavior 11.

**Files touched:** `internal/cli/idle_nudge.go` (`nudge` uses `mailToAnnounce`), `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestClosure_NudgeSkipsSnoozed`. Expected failure: the reminder names 93.

##### 🟢 Green

`nudge` calls `mailToAnnounce` after `unreadMessages`, and returns before the pane check when nothing is left. `productionIdleNudgeDeps.Store` returns a store that also satisfies `snoozeReader`.

##### 🔵 Refactor

As Behavior 6.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_NudgeSkipsSnoozed ./internal/cli/` fails first, then passes.

### Build record (2026-11-09)

Base commit `e3ccd63c`. Red, then green, for each behavior:
- **1:** red was "undefined: parseSnoozeUntil". The round-trip property passed.
- **2:** red was "undefined: ValidateSnoozeUntil" in `state`. Serve's new `TestApplyAttentionItemAction_SnoozeUsesSharedRule` was red with "past snooze error = until must be in the future, want state.ErrSnoozeNotInFuture", then green once serve called `state.SnoozeAttentionItem`. Serve's existing snooze tests stayed green.
- **3:** red was "undefined: SnoozeRequest". The idempotent property passed.
- **4:** red was "undefined: SnoozedMessage". The partition property passed.
- **8:** red was "entry "1) from (unknown sender): (no subject)" lacks "1) [#100] from "". After green, the four Loop 3 tests that pin the exact reminder text failed on the new format. They were updated to it with every assertion kept, and the tamper check is clean.
- **5 (red-at-seam):** with no `snooze` subcommand, cobra failed on the flag: "unknown flag: --until".
- **6 (red-at-seam):** before `claimUnread` called `mailToAnnounce`, the reason listed "[#91] from RedStone: snooze me".
- **7 (red-at-seam):** before `nudge` called `mailToAnnounce`, both runs typed the snoozed message.

Where the build differs from the plan:
- **Behavior 1's tests are in `mail_snooze_test.go`,** with the other snooze tests, not in `mail_test.go`. The `state` tests are in a new `internal/state/snooze_test.go`, because `state_test.go` is already very large.
- **`SnoozeAddressedMessage` reads before its transaction.** `Store.Transaction` holds the store's write lock, so it looks up the notice and the prior item state first, then writes the item state and the audit event in one transaction. A snooze replaces the time either way, so a change between the read and the write doesn't matter.
- **The hook and the snooze command share two new helpers,** `agentHookIdentity` and `agentMailClientFor`, from `runAgentHookStop`. `runMailMark`'s JSON check became `mailJSONEnabled`, which `snooze` uses too.
- **The nudge now opens the store before the pane check,** so a poll with only snoozed mail stops before reading the pane. A store that won't open still means no typing, as before.
- **`mailToAnnounce` returns messages in Sprint 1.** Sprint 2 changes it to return items that can carry an ended snooze.
- **Behavior 5's closure test reads snoozes in the test process** through `Store.SnoozedMessages`, the read the hook and nudge use, as Loop 5's `told` helper does, instead of a `read-snoozes` helper mode.
- **Extra error case:** message id 0 or a non-number fails with "message id must be a positive number".

Verification:
- `go test -short -race ./internal/state/`: ok, no data race. `go test -short ./internal/serve/`: ok.
- The closure tests (Behaviors 5, 6 and 7, Loop 5's nudge closure tests, and Loop 3's Stop hook closure tests) passed 10 times in a row: `ok internal/cli 1311s`.
- `go test -short ./...` shows the 7 known failures from bd-5zbcm, plus one new one, `TestAttentionFeed_OutageEventsDeliveredOnce` in `internal/robot`.
  - Sprint 1 didn't change `internal/robot`, and the test passed 20 times in a row when run alone.
  - It waits 2 seconds for events from a feed that polls once a second, so a loaded machine can miss the window. It's beaded as `bd-dwpes` (P3), and Loop 4's test is not changed here.
- The build, `go vet` on cli, state and serve, and the tamper check against `e3ccd63c` are clean. `gofmt -l` flags only the 3 files that were already flagged.
- golangci-lint, goimports, ubs and rch aren't installed here, so they weren't run.

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop6 ./cmd/ntm`.
2. Create a session: `/tmp/ntm-loop6 spawn snoozetest --cc=1`. Before any mail arrives, set the nudge switch off for this test, as in Loop 5, Sprint 5, so you control when the agent is told.
3. Send mail: `/tmp/ntm-loop6 mail send snoozetest --to <agent> "snooze me"`, then find its id with `/tmp/ntm-loop6 mail inbox snoozetest`.
4. In a shell acting as that agent, run `AGENT_NAME=<agent> /tmp/ntm-loop6 mail snooze <id> --until 30m`.
5. Pass: it confirms a time 30 minutes from now.
6. Send a second message: `/tmp/ntm-loop6 mail send snoozetest --to <agent> "second"`.
7. Run the hook by hand, as in Loop 3, Sprint 1.
8. Pass: the reminder names `second`, and doesn't name `snooze me`.
9. Run `AGENT_NAME=<agent> /tmp/ntm-loop6 mail snooze <id> --until 2020-01-01T00:00:00Z`.
10. Pass: an error, and the original snooze time still stands.
11. Run `AGENT_NAME=SomeoneElse /tmp/ntm-loop6 mail snooze <id> --until 30m`.
12. Pass: an error, and the message's snooze is unchanged.

Notes for the run:
- Step 2: set `idle_nudge_enabled = false` under `[agent_mail]` before `spawn`, because spawn freezes it.
- Step 4: run it from the session's folder (`<projects_base>/snoozetest`), or add `--project <that folder>`.
- Step 7: run the command from `.claude/settings.local.json` in that folder, with `AGENT_NAME=<agent>` and `echo '{}' |` piped in.
- Steps 10 and 12: to see that the snooze still stands without changing it, send one fresh message first, then run step 7's hook command again. Check that the reminder lists only the fresh message, not `snooze me`. Without fresh mail, the hook prints nothing because `second` was already told, and the check passes trivially (found in the first run).
- Step 3: `ntm mail inbox` shows IDs only with `--json`.

**Result (2026-11-09), passed:** run on rocketman at `1790b2b5`, with agent FoggyGorge and the nudge off (`"idle_nudge_disabled": true` in the manifest).
- **Step 4:** "Snoozed message 12520 until 2026-11-10T02:44:56Z (in 30m)", at 21:14:56 EST.
- **Steps 7 and 8:** the hook gave "1) [#12521] from HumanOverseer: second" and the snooze command line, with no "snooze me".
- **Steps 9 and 10:** "Error: snooze time must be in the future".
- **Steps 11 and 12:**
  - `AGENT_NAME=SomeoneElse` failed with Agent Mail's own "Agent 'SomeoneElse' not found in project …", because that name isn't registered. The command wraps it as "can't check message … with Agent Mail".
  - With a registered other agent, BlueForest, it gave "Error: message 12520 isn't in BlueForest's Agent Mail inbox".
- **The snooze held through every rerun.** The tester sent fresh mail before each rerun (#12522 to #12524), and each reminder listed only the new mail, never #12520.
- **Notes from the run:**
  1. The script's reruns needed fresh mail, which is now added to the notes above.
  2. The text `ntm mail inbox` shows no message IDs.
  3. The snooze running out wasn't tested, because that's Sprint 2. #12520 has no told record.
- Evidence: `/home/debian/Dev/ntm-test-evidence/loop6-sprint1-local-2026-11-09/`.

---

## Sprint 2: Reminder after the snooze ends

**Bead:** `bd-atnqa.2`.

**Testable function:** "Leave the agent idle past the snooze time, and see one reminder that includes the message."

**Order of work:** 9, 10, 11, then 12 as the outer test, then 13, 14, 15 and 16.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 12 |
| The first poll after the snooze ends that finds the agent idle and safe to type into sends one reminder with that message | 11, 12 |
| The reminder covers it once, then **One Telling** applies again, including at the next turn end | 10, 12 |
| If the agent is working when the snooze ends, its next turn end includes the message (**One Telling**) | 13 |
| A snoozed message returns when its snooze ends, whether or not it was read (Agent Mail reports no read state; scope change at 4c) | 15 |
| A snooze still ending within retention survives restarts of the monitor and the agent | 14 |
| Re-announcing replaces the told record and clears the snooze in one transaction, so two callers can't both announce it (requirement, test scenario "Race") | 10 |
| A snooze longer than the 7 day retention is rejected (test scenario "Over the limit") | 9 |
| Snooze ends and new mail arrives: one reminder listing both (test scenario "With new mail") | 11 |
| The text `ntm mail inbox` shows each message's id, so a person or agent can snooze it (added after Sprint 1's human test, 2026-11-10) | 16 |

### Behavior 9: A snooze can't outlast the 7 day retention or its event [LEAF: pure function plus one store check]

#### Test Specification

**Given** `now`. **When** `ValidateSnoozeUntil(now + 7d + 1s, now)` runs. **Then** it errors with "snooze time can be at most 7 days ahead (until <time>)". `now + 7d` passes. **Given** an event recorded 5 days ago. **When** `SnoozeAddressedMessage` runs with `now + 3d`. **Then** it errors with "message 42's notice expires at <time>; snooze until then at the latest". **When** `Execute mail snooze 81 --until 8d` runs. **Then** the 7-day error. Serve rejects `8d` too.

**Edge Cases:** an event with no expiry is bounded by the 7-day rule only.

**Property:** Invariant (extends Behavior 2): `ValidateSnoozeUntil(until, now) == nil` exactly when `now < until <= now + 7d`, for times from now - 1d to now + 9d.

**Files touched:** `internal/state/runtime_store.go`, `internal/state/runtime_store_test.go`, `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

Extend `TestValidateSnoozeUntil` and its property, and add `TestSnoozeAddressedMessage_EventExpiry`. Expected failure: `8d` is accepted.

##### 🟢 Green

`ValidateSnoozeUntil` adds `until.After(now.Add(SnoozeLimit))`, with `SnoozeLimit = ToldRecordRetention`. `SnoozeAddressedMessage` compares against the event's `expires_at`.

##### 🔵 Refactor

- [ ] **No duplication:** one limit constant, tied to the retention it follows.
- [ ] **Reveals intent:** both errors name the latest allowed time.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Behavior 2.

#### Success Criteria

**Automated:** `go test -short -run 'Snooze' ./internal/state/ ./internal/serve/` fails first, then passes.

### Behavior 10: An ended snooze is claimed by exactly one caller [LEAF: one package, real SQLite]

#### Test Specification

**Given** BlueLake's message 42, told earlier (`told_by stop_hook`), with a snooze that ended. **When** `Store.ClaimEndedSnooze(ToldRecord{P, BlueLake, 42, idle_nudge, now})` runs. **Then**:
- it returns true;
- the item state is `new` with no snooze time;
- the told record is replaced (`told_by idle_nudge`, `told_at` now);
- all of it happens in one transaction.

**When** it runs again. **Then** false. **Given** an active snooze. **Then** false, and nothing changes. **When** `Store.ReleaseEndedSnoozeClaim(rec, until)` runs after a won claim. **Then** the snooze and the earlier told record come back.

**Edge Cases:** a message with an ended snooze and no told record is claimed the same way.

**Property:** Invariant (concurrency): for 2 to 8 goroutines claiming the same ended snooze at once, on one store file, exactly one gets true.

```go
rapid.Check(t, func(rt *rapid.T) {
	n := rapid.IntRange(2, 8).Draw(rt, "callers")
	store := seededEndedSnooze(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if ok, err := store.ClaimEndedSnooze(rec); err == nil && ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()
	if wins.Load() != 1 {
		rt.Fatalf("%d winners of %d", wins.Load(), n)
	}
})
```

**Files touched:** `internal/state/runtime_store.go`, `internal/state/runtime_store_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestClaimEndedSnooze` and `TestClaimEndedSnooze_OneWinnerProperty`. Expected failure: `undefined: ClaimEndedSnooze`.

##### 🟢 Green

One transaction:
1. `UPDATE attention_item_states SET state='new', snoozed_until=NULL WHERE item_key=? AND state='snoozed' AND snoozed_until <= ?`.
2. If no row changed, return false.
3. Otherwise, upsert the told record unconditionally, keeping the replaced one in the return for release.

##### 🔵 Refactor

- [ ] **No duplication:** the told record upsert shares `toldAtText`.
- [ ] **Reveals intent:** the claim and its release are named as a pair.
- [ ] **Complexity went down:** the race is decided by one `UPDATE`.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `ClaimToldRecord` (`runtime_store.go:2285`).

#### Success Criteria

**Automated:** `go test -short -race -run TestClaimEndedSnooze ./internal/state/` fails first, then passes.

### Behavior 11: The shared step merges ended snoozes with new mail [LEAF: `mailToAnnounce` and the poll with fakes]

#### Test Specification

**Given** fetched unread mail 51 (new) and 52 (actively snoozed), and the store's ended snooze 53 (not in the fetch, older than the 50-message window). **When** `mailToAnnounce` runs. **Then**:
- the result is [51, 53];
- 53 carries its sender and subject from the event, and is marked as an ended snooze;
- 52 is left out.

**When** 53 is also in the fetch. **Then** it appears once.

**When** the nudge's poll (fakes) and the hook's `claimUnread` claim the result. **Then**:
- 51 goes through `ClaimToldRecord` and 53 through `ClaimEndedSnooze`;
- one reminder lists both;
- a failed send releases both: the told claim for 51, and `ReleaseEndedSnoozeClaim` for 53.

**Edge Cases:** a store read error leaves the fetched mail unfiltered and logs it (**Fail Open**).

**Property:** Invariant: for generated sets of fetched IDs, active snooze IDs and ended snooze IDs (from 1 to 20), the result's IDs are exactly (fetched minus active) plus ended, each once.

**Files touched:** `internal/cli/idle_nudge.go`, `internal/cli/agent_hook.go`, `internal/cli/idle_nudge_test.go`, `internal/cli/agent_hook_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestMailToAnnounce`, `TestMailToAnnounce_Property`, `TestIdleNudgePoll_EndedSnoozeClaimAndRelease` and `TestClaimUnread_EndedSnooze`. Expected failure: 53 is missing.

##### 🟢 Green

`mailToAnnounce` reads `SnoozedMessages`, drops the active IDs, and appends the ended ones not already present. `announceItem` carries `ended bool`, and the claim step picks the store call by it.

##### 🔵 Refactor

- [ ] **No duplication:** one claim helper serves the hook and the nudge, each with its own `told_by` and failure rule.
- [ ] **Reveals intent:** `announceItem.ended`.
- [ ] **Complexity went down:** the two claim loops become one.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 5's `nudgeClaim`.

#### Success Criteria

**Automated:** `go test -short -run 'TestMailToAnnounce|EndedSnooze' ./internal/cli/` fails first, then passes.

### Behavior 12: A snooze that ends while the agent is idle returns in one reminder [BLOCKING]

#### Test Specification

**Given** a spawned idle stand-in, with mail 95 "come back" told by an earlier nudge, then snoozed with `--until 2s`. **When** the monitor keeps polling. **Then**:
- nothing is typed while the snooze is active;
- after it ends, one reminder names `[#95] … come back`, and the told record says `idle_nudge` with a later `told_at`;
- 3 seconds later nothing more is typed;
- the written hook command prints nothing.

**Edge Cases:** the human typing guard still applies, because the return goes through the same `nudge`.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestClosure_SnoozeReturnsOnce`. Expected failure before 11's Green: no reminder within 10 seconds.

##### 🟢 Green

Behaviors 10 and 11.

##### 🔵 Refactor

As Behavior 11.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_SnoozeReturnsOnce ./internal/cli/` fails first, then passes.

### Behavior 13: A snooze that ends during a turn is told at the turn end, and not nudged again [BLOCKING]

#### Test Specification

**Given** a stand-in showing `cc_working.txt`, with mail 96 told, then snoozed `--until 2s`, and the monitor running. **When** 4 seconds pass. **Then** nothing is typed. **When** the hook command runs. **Then** its reason names `[#96]`. **When** the screen turns idle and 3 seconds pass. **Then** no reminder is typed.

**Edge Cases:** none beyond Behavior 12.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestClosure_SnoozeEndsMidTurn`. Expected failure before 11's hook wiring: the hook prints nothing.

##### 🟢 Green

Behavior 11's hook path.

##### 🔵 Refactor

As Behavior 11.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_SnoozeEndsMidTurn ./internal/cli/` fails first, then passes.

### Behavior 14: A monitor restart keeps the snooze and returns it on time [BLOCKING]

#### Test Specification

**Given** mail 97 told and snoozed with `--until 4s`, and a monitor. **When** the monitor is stopped after 1 second and a new one starts. **Then** nothing is typed before the snooze ends, and one reminder after.

**Edge Cases:** the agent's restart is the same case, because the snooze lives in `state.db`, not in any process.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestClosure_SnoozeSurvivesMonitorRestart`. Red-at-seam: keep the active snooze in nudger memory. Expected: the second monitor types it at once.

##### 🟢 Green

Behaviors 4 and 11, which read only the store.

##### 🔵 Refactor

As Behavior 11.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_SnoozeSurvivesMonitorRestart ./internal/cli/` passes, with red-at-seam recorded.

### Behavior 15: An ended snooze returns even if Agent Mail lists the message as read, or no longer lists it [LEAF: `mailToAnnounce` with fakes]

#### Test Specification

**Given** an ended snooze for 53. **When** the fetch returns 53 with `ReadAt` set. **Then** `mailToAnnounce` still returns 53. **When** the fetch doesn't return 53 at all. **Then** it still returns 53, with the event's sender and subject.

**Edge Cases:** a fetched message with `ReadAt` set and no snooze stays out, which is today's `unreadMessages` rule.

**Property:** covered by Behavior 11's invariant. Generated `ReadAt` values don't change the ended set.

**Files touched:** `internal/cli/idle_nudge_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestMailToAnnounce_EndedSnoozeIgnoresReadState`. Expected failure, if `unreadMessages` runs after the merge: 53 is dropped.

##### 🟢 Green

`mailToAnnounce` merges ended snoozes after the read filter.

##### 🔵 Refactor

As Behavior 11.

#### Success Criteria

**Automated:** `go test -short -run TestMailToAnnounce_EndedSnoozeIgnoresReadState ./internal/cli/` fails first, then passes.

### Behavior 16: The text inbox shows message IDs [LEAF: one command's text output, with the existing mock client]

Added after Sprint 1's human test, where finding an id needed `--json` (your choice, 2026-11-10).

#### Test Specification

**Given** BlueLake's inbox with message 1 "Test Message" (normal) and 2 "Urgent Msg" (urgent). **When** `ntm mail inbox` runs without `--json`. **Then** the subject lines read `[#1] Test Message` and `[#2] [URGENT] Urgent Msg`, and the sender line is unchanged.

**Edge Cases:** the JSON output doesn't change.

**Property:** no property: fixed rendering, nothing to generate over.

**Files touched:** `internal/cli/mail.go` (`runMailInbox`), `internal/cli/mail_inbox_test.go`.

#### TDD Cycle

##### 🔴 Red

Extend `TestRunMailInbox`'s "successful list with messages" and "urgent filter" cases. Expected failure: no `[#1]` in the output.

##### 🟢 Green

`fmt.Fprintf(out, "[#%d] %s%s\n", m.ID, prefix, subject)`.

##### 🔵 Refactor

- [ ] **No duplication:** the `[#id]` form matches the reminder's.
- [ ] **Reveals intent:** n/a, one format string.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `runMailInbox`'s rendering.

#### Success Criteria

**Automated:** `go test -short -run TestRunMailInbox ./internal/cli/` fails first, then passes.

### Build record (2026-11-10)

Base commit `1790b2b5`. Red, then green, for each behavior:
- **9:** red was "undefined: ErrSnoozeTooLong" and "ErrSnoozePastNotice" in `state` and serve, and the command accepted `8d` ("Snoozed message 81 until … (in 192h0m)"). Green in all three. The limit property passed.
- **10:** red was "store.ClaimEndedSnooze undefined". The one-winner property opens a separate `Store` per caller on one file, as the hook and the monitor are separate processes. It passed 100 checks under `-race`.
- **11 and 15:** red was "undefined: announceItem" (compile).
- **12, 13, 14 (red-at-seam):** with `mailToAnnounce` ignoring ended snoozes (`ended = nil`), five tests went red:
  - `TestClosure_SnoozeReturnsOnce`: "typed 1 times within 12s, want 2".
  - `TestClosure_SnoozeEndsMidTurn`: the hook printed nothing.
  - `TestClosure_SnoozeSurvivesMonitorRestart`: nothing typed in 15s.
  - `TestClaimUnread_EndedSnooze` and `TestMailToAnnounce_EndedSnoozeIgnoresReadState`.

  All went green again once the seam check was undone.
- **The first seam check proved nothing, and shows what the closure tests miss.** Turning off only the store-only path (an ended snooze that the fetch doesn't list) left the closure tests green. The fake server keeps listing a message as unread, so the closure tests always get it through the fetch. That path is covered by the unit tests instead: Behavior 15's "no longer listed" case, `TestClaimUnread_EndedSnooze` and `TestMailToAnnounce`.
- **16:** red was the two inbox cases missing `[#1] Test Message` and `[#2] [URGENT] Urgent Msg`.

Where the build differs from the plan:
- **`ClaimEndedSnooze` takes the ended `SnoozedMessage` as well as the told record,** and returns a `*EndedSnoozeClaim` (nil when another caller won) instead of a bool. The claim keeps the told record it replaced, so `ReleaseEndedSnoozeClaim` can put back exactly that. `SnoozedMessage` gained `ItemKey`.
- **The claim's transaction starts with the conditional `UPDATE`,** so it holds SQLite's write lock before it reads. A plain deferred transaction that reads first can fail in a second process with a stale-snapshot busy error, which the hook would treat as Fail Open and tell twice. The release puts each part back only if it's still as the claim left it.
- **`GetToldRecord`'s query is shared** with the claim's in-transaction read, through a small `rowQuerier` interface.
- **The nudge opens the store on every poll,** because an ended snooze can return with no new mail. A store that won't open is logged only when there is new mail, so a broken store doesn't log on every poll for every agent.
- **The hook's read filter now uses `unreadMessages`,** the nudge's helper, and runs before the snooze step, so ended snoozes are added after it (Behavior 15).
- **`claimAnnounce` is the one claim helper.** The hook and the nudge each keep their own `told_by` and failure rule. The nudge's `release` restores an ended snooze through `ReleaseEndedSnoozeClaim`.

Verification:
- `go test -short -race ./internal/state/`: ok, no data race. `go test -short ./internal/serve/`: ok. `internal/cli` alone shows only its 2 known failures.
- The closure tests (Behaviors 5, 6, 7, 12, 13 and 14, Loop 5's nudge closure tests, and Loop 3's Stop hook closure tests) passed 10 times in a row: `ok internal/cli 1646s`.
- `go test -short ./...` shows the 7 known failures from bd-5zbcm, plus 2 palette tests that failed only in that parallel run: `TestPaletteConfigContextDirPrefersExplicitSessionProject` and `TestLoadPaletteRuntimeConfigPrefersExplicitSessionProject`.
  - They passed 3 times in a row alone, and in the same verification's separate `internal/cli` run. Sprint 2 didn't touch palette code.
  - Their isolation doesn't cover the default tmux server, which they ask for a session named `ntm`, so a live session of that name on this shared VPS can fail them. They're beaded as `bd-cxa1s` (P3).
  - Sprint 1's flaky `TestAttentionFeed_OutageEventsDeliveredOnce` (`bd-dwpes`) passed this time.
- The build, `go vet` on cli, state and serve, and the tamper check against `1790b2b5` are clean. `gofmt -l` flags only the 3 files that were already flagged. No rapid failure files were left.
- golangci-lint, goimports, ubs and rch aren't installed here, so they weren't run.

### Human test script

1. Create a session: `/tmp/ntm-loop6 spawn returntest --cc=1`, and wait for the agent to be idle.
2. Send mail: `/tmp/ntm-loop6 mail send returntest --to <agent> "come back"`, and wait for the nudge.
3. Find the id with `/tmp/ntm-loop6 mail inbox returntest`, then run `AGENT_NAME=<agent> /tmp/ntm-loop6 mail snooze <id> --until 3m`. Tell the agent not to read the message.
4. Wait 2 minutes, then run `/tmp/ntm-loop6 --robot-tail=returntest --lines=30`.
5. Pass: no new reminder yet.
6. Wait about 2 more minutes, and run the same tail.
7. Pass: one reminder names `come back`.
8. Wait 2 more minutes.
9. Pass: no further reminder.

Notes for the run:
- The nudge's reminder in step 2 now shows the message ID (`[#<id>]`), so step 3's inbox lookup is optional.
- Make sure `idle_nudge_enabled` is back on, or removed, before step 1.
- Step 3's note to the agent no longer matters for the result. The message returns whether or not it was read.

**Result (2026-11-10), passed:** run on rocketman at `d1a6cb6d`, nudge on, with agent GrayFinch in pane `%73`.
- **The first reminder:**
  - At 07:37:28, mail 12525 was sent.
  - At 07:37:51, the reminder was sent for [12525], showing "[#12525] from HumanOverseer: come back".
- **The snooze:**
  - At 07:38:01: "Snoozed message 12525 until 2026-11-10T12:41:01Z (in 3m)".
  - At 07:40:07, the tail showed no new reminder.
- **The return:**
  - At 07:41:21, the second reminder was sent for [12525], 20 seconds after the snooze ended.
  - Through 07:44:24 there were no more reminders. A pane watcher checking every 10 seconds counted 1, then 2, never 3. There were no Stop hook repeats.
- **The text inbox** shows "[#12525] [URGENT] come back" (Behavior 16).
- `told_records` has one row, `12525|idle_nudge|12:41:20Z`, updated by the return.
- **The tester's note:** the pane title is now `returntest__cc_1`, not the agent's name as in earlier builds, and older test steps used `#{pane_title}` to find the name. Loop 6 didn't change pane titles (no commit since `e3ccd63c` touches `internal/tmux` or `spawn.go`). Claude Code sets its own terminal title, so this varies with Claude. Use the spawn output, or `AGENT_NAME` in the pane's environment, to find the name.
- Evidence: `/home/debian/Dev/ntm-test-evidence/loop6-sprint2-local-2026-11-10/`.

---

## Sprint verification (every sprint)

```bash
export PATH=$PATH:/usr/local/go/bin
go build ./cmd/ntm
gofmt -l internal/ cmd/
go vet ./internal/cli/ ./internal/state/ ./internal/serve/
go test -short -race ./internal/state/
go test -short ./internal/serve/ ./internal/cli/
go test -short -count=10 -run 'TestClosure_.*Snooze|TestClosure_HookSkipsSnoozed|TestClosure_NudgeSkipsSnoozed|TestClosure_MailSnoozeCommand|TestClosure_.*Nudge' ./internal/cli/
go test -short ./...           # compare failures with the known list in bd-5zbcm
bun ~/.claude/SAI/Tools/TestTamperCheck.ts --base <sprint start commit>
```

`internal/serve` under `-race` takes longer than 10 minutes. It runs without `-race` here, as in Loop 4. `golangci-lint`, `goimports` and `ubs` are reported as not run if they're still missing.

## Integration & E2E Testing

- **Integration:** `internal/state` snooze save, lookup, claim and release on real SQLite, with a concurrency property.
- **Closure:** Behaviors 5, 6, 7, 12, 13 and 14 in `internal/cli`, with the real command, spawn, monitor, hook, store and the fake Agent Mail server.
- **E2E:** none new. Loop 9's release journey covers snoozing with real agents.

## Beads

The master index sets the rule: one epic per loop, and one bead per sprint, created at the TDD step.
- Epic: `bd-atnqa`, "Loop 6: Agent snooze".
- Sprint 1: `bd-atnqa.1`, "Loop 6 Sprint 1: Snooze a message".
- Sprint 2: `bd-atnqa.2`, "Loop 6 Sprint 2: Reminder after the snooze ends".
- Separate bug: `bd-1scum` (P3), "Stop hook's ReadAt skip never fires: Agent Mail's fetch_inbox returns no read time".

## References

- Loop document: [06-agent-snooze.md](06-agent-snooze.md)
- Scope: [SCOPE.md](SCOPE.md), process **Agent Snooze**, rule **One Telling**, contract **ToldRecord** ("Replaced when a snooze ends")
- Research: `thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`, Chain A
- Loop 5 TDD plan: [05-idle-nudge-tdd.md](05-idle-nudge-tdd.md) (closure harness, `nudge`, claims and release)
- Patterns:
  - `applyAttentionItemAction` (`internal/serve/server.go:5641`)
  - `ResolveAttentionItemKey` (`internal/state/runtime_store.go:2028`)
  - `GetAddressedAttentionEvent` (`:1666`)
  - `ClaimToldRecord` (`:2285`)
  - `claimUnread` and `buildReminderText` (`internal/cli/agent_hook.go:215`, `:257`)
  - `newMailMarkCmd` (`internal/cli/mail.go:516`)
- Agent Mail server read-state evidence: mcp_agent_mail `app.py:4193` (`_list_inbox`), `:1759` (`_message_to_dict`), `:11887` (message resource)
