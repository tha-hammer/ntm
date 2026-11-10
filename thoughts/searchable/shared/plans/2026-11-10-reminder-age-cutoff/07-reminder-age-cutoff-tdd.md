# Loop 7 — Reminder age cutoff: TDD Implementation Plan

Loop document: [07-reminder-age-cutoff.md](07-reminder-age-cutoff.md). Behavior list and 4c answers:
[07-reminder-age-cutoff-behaviors.md](07-reminder-age-cutoff-behaviors.md). Scope: [SCOPE.md](SCOPE.md).
Repository: `/home/maceo/Dev/ntm`.

## Overview

Agents get reminded about mail by the Stop hook and the idle nudge, which share one step,
`mailToAnnounce`. Told records last 7 days, and Agent Mail never reports read state. So an old
message the agent already saw is announced again about every 7 days. This loop adds an age cutoff to
that shared step, keeps snooze returns exempt, and closes one gap: a snooze that ends exactly at its
notice's expiry never came back.

## Current State Analysis

### Key Discoveries:
- `mailToAnnounce` is at `internal/cli/idle_nudge.go:90`. The hook calls it from `claimUnread`
  (`internal/cli/agent_hook.go`, about line 237), and the nudge calls it from its poll
  (`internal/cli/idle_nudge.go:223`).
- The hook records a feed notice only for items that `mailToAnnounce` returns (`feed.RecordMailNotice`
  in `claimUnread`). The nudge does the same (`n.deps.Record`, `idle_nudge.go:270`). So the feed cutoff
  follows from the main cutoff, with no extra code.
- `ToldRecordRetention = 7 * 24 * time.Hour` is at `internal/state/runtime_store.go:2437`.
  `ClaimToldRecord` treats records older than that as gone, and
  `TestClaimToldRecord_ExpiredClaimWinsAgain` asserts this (`internal/state/state_test.go:2677`).
- `FlexTime` reads zone-less times as UTC (`internal/agentmail/types.go:33-51`).
- `SnoozedMessages` (`runtime_store.go:2161`) only reads notices with `expires_at >= now`.
  `SnoozeAddressedMessage` (`runtime_store.go:2083-2097`) allows `until == expires_at`. Such a snooze
  never shows as ended (see the behavior list's finding).
- Existing tests build `InboxMessage` values with no `CreatedTS` (zero). Under the Unknown Age rule
  they keep passing unchanged.
- Test helpers to reuse:
  - `newFakeAgentMail` and `addMessage` (`agent_hook_test.go:60`), `withHookTestEnv` (`:442`);
  - `newClosureEnv`, `env.projectDir` (`:878`), `env.told` (`idle_nudge_test.go:289`);
  - `spawnNudgeAgent` (`:309`), `runStopHook` (`:355`), `waitTypedCount` and `seedEndedSnooze`
    (`mail_snooze_test.go:177`), `snoozeInSession` (`:434`);
  - `parseRobotEvents` (`attention_closure_test.go:237`).

## Desired End State

### Observable Behaviors:
- Given a plain message more than 7 days old, when the hook or the nudge runs, then it isn't announced or recorded.
- Given a message with a zero or future time, when they run, then it's announced.
- Given an ended snooze, whatever its message's age, when they run, then it's announced once.
- Given a notice expiring at E, when `ntm mail snooze` asks for a time after E − 1h, then it's refused.

## What We're NOT Doing

- Read-state tracking, changing the 7-day retention or the 50-message window, a send-retry limit,
  or a re-snooze limit.
- Any change to `ntm mail inbox` output.
- Any new config.

## Testing Strategy

- **Framework**: Go `testing`, plus `pgregory.net/rapid` v1.3.0 (already in go.mod).
- **Test Types**:
  - Unit: B1, B2, B7, B9, and the state part of B11.
  - Closure: B3, B4, B5, B6, B10, B11 and B12, through `ntm agent-hook stop`, `ntm mail snooze`,
    and the real session monitor.
- **Mocking/Setup**: only the existing external fakes: the fake Agent Mail HTTP server, the private
  tmux server, and the `screenAgents` stand-in. Each test gets its own temporary `state.db`. The
  monitor polls every 1s (`idle_nudge_interval_seconds = 1`).
- **Run**:
  - Sprint tests: `go test ./internal/cli/ -run 'AgeCutoff|TooOld|MailToAnnounce|LateSnooze|SnoozeMargin' -count=1`.
  - State tests: `go test ./internal/state/ -run 'Snooze|ToldRecord' -count=1`.
  - Full suite: `go test -short ./...`.

## Beads

The master index sets no beads rule. The VPS agent files and owns this repo's beads, so this plan
proposes issues but doesn't create them:
- an epic, "Reminder age cutoff (Loop 7)";
- one child per sprint.

---

## Sprint 1: Old mail stays quiet

| Acceptance criterion (loop document) | Behaviors |
|---|---|
| Testable function: the hook doesn't announce a message over 7 days old, but announces a younger one | B3, B5 |
| `mailToAnnounce` drops plain items older than `ToldRecordRetention`, for the hook and the nudge | B1, B2, B4 |
| A dropped message gets no told record and no feed notice | B3 |
| Zero and future `CreatedTS` are announced | B1, B2, B6 |
| `created_ts` parsing is pinned to a captured real response | B7 |
| The expired-claim test comment and the attention-feed scope note are updated | B8 |

### Behavior 1: Age check [LEAF: pure function in one package, with no async or registration]

#### Test Specification
**Given** a message time `created` and `now`. **When** `tooOldToAnnounce(created, now)` runs.
**Then** it's true only when `created` is non-zero and `now.Sub(created) > state.ToldRecordRetention`.

**Edge Cases**: exactly 7 days gives false. A zero time gives false. A time 1 hour in the future gives false. 7 days and 1ns gives true.

**Property**: Invariant. For every `age` in [−30d, 30d]:
`tooOldToAnnounce(now.Add(-age), now) == (age > ToldRecordRetention)`. A zero time is always false.
**Domain**: `rapid.Int64Range(-30*24*3600e9, 30*24*3600e9)` as a `time.Duration`.

**Files touched**: `internal/cli/idle_nudge.go`, `internal/cli/reminder_age_test.go` (new).

#### TDD Cycle
##### 🔴 Red
**File**: `internal/cli/reminder_age_test.go`
```go
func TestTooOldToAnnounce(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name    string
		created time.Time
		want    bool
	}{
		{"8 days", now.Add(-8 * 24 * time.Hour), true},
		{"exactly 7 days", now.Add(-state.ToldRecordRetention), false},
		{"7 days and 1ns", now.Add(-state.ToldRecordRetention - 1), true},
		{"zero", time.Time{}, false},
		{"future", now.Add(time.Hour), false},
	} {
		if got := tooOldToAnnounce(c.created, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTooOldToAnnounce_Property(t *testing.T) {
	now := time.Now().UTC()
	rapid.Check(t, func(rt *rapid.T) {
		age := time.Duration(rapid.Int64Range(-30*24*int64(time.Hour), 30*24*int64(time.Hour)).Draw(rt, "age"))
		if got := tooOldToAnnounce(now.Add(-age), now); got != (age > state.ToldRecordRetention) {
			rt.Fatalf("age %s: got %v", age, got)
		}
	})
}
```
It fails to compile: `tooOldToAnnounce` is undefined.

##### 🟢 Green
**File**: `internal/cli/idle_nudge.go`
```go
// tooOldToAnnounce reports whether a message is older than a told record can
// vouch for. A message with no time, or a time in the future, is never too old
// (Fail Open).
func tooOldToAnnounce(created, now time.Time) bool {
	return !created.IsZero() && now.Sub(created) > state.ToldRecordRetention
}
```

##### 🔵 Refactor
- [ ] No duplication: the one constant is reused, with no new 7-day literal.
- [ ] The name reveals intent.
- [ ] Complexity: a single expression.
- [ ] No shallow wrapper: it holds the rule, used from one place.
- [ ] Fits the existing pattern: the comment style matches `mailToAnnounce`'s Fail Open note.

### Behavior 2: Cutoff in mailToAnnounce [LEAF: same-module pure step]

#### Test Specification
**Given** plain fetched messages aged 8d (#61), 6d (#62), zero (#63) and +1h (#64), with no snoozes.
**When** `mailToAnnounce` runs. **Then** it returns #62, #63 and #64, in order. #61 is gone.
On a snooze-store read error, #61 is still dropped.

**Property**: Invariant. For any fetched set with random ages and random snooze kinds, no returned
item with `ended == nil` has `tooOldToAnnounce(item.msg.CreatedTS.Time, now)`. This extends
`TestMailToAnnounce_Property` (`mail_snooze_test.go:249`) with a random `CreatedTS` per message.

**Files touched**: `internal/cli/idle_nudge.go`, `internal/cli/mail_snooze_test.go`.

#### TDD Cycle
##### 🔴 Red
**File**: `internal/cli/mail_snooze_test.go`
```go
func TestMailToAnnounce_AgeCutoff(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) agentmail.FlexTime { return agentmail.FlexTime{Time: now.Add(d)} }
	msgs := []agentmail.InboxMessage{
		{ID: 61, CreatedTS: at(-8 * 24 * time.Hour)},
		{ID: 62, CreatedTS: at(-6 * 24 * time.Hour)},
		{ID: 63},
		{ID: 64, CreatedTS: at(time.Hour)},
	}
	if ids, _ := announcedIDs(mailToAnnounce(fakeSnoozes{}, "/p", "BlueLake", msgs, now)); fmt.Sprint(ids) != "[62 63 64]" {
		t.Fatalf("ids = %v, want [62 63 64]", ids)
	}
	if ids, _ := announcedIDs(mailToAnnounce(fakeSnoozes{err: errors.New("busy")}, "/p", "BlueLake", msgs, now)); fmt.Sprint(ids) != "[62 63 64]" {
		t.Fatalf("on a read error: ids = %v", ids)
	}
}
```
It fails: #61 is returned.

##### 🟢 Green
**File**: `internal/cli/idle_nudge.go`. In `mailToAnnounce`, filter the fetched list once at the
top, so the Fail Open branch gets the cutoff too. Ended snoozes are kept (Sprint 2 adds the
exemption test).
```go
func mailToAnnounce(store snoozeReader, project, agent string, msgs []agentmail.InboxMessage, now time.Time) []announceItem {
	active, ended, err := store.SnoozedMessages(project, agent, now)
	if err != nil {
		slog.Warn("snoozes not checked", "agent", agent, "project", project, "error", err)
		return plainAnnounceItems(withoutTooOld(msgs, nil, now))
	}
	...
	back := ...                      // as today
	msgs = withoutTooOld(msgs, back, now)
	... // rest unchanged
}

// withoutTooOld drops fetched messages too old to announce, except those
// returning from a snooze (keep).
func withoutTooOld(msgs []agentmail.InboxMessage, keep map[int64]*state.SnoozedMessage, now time.Time) []agentmail.InboxMessage {
	out := msgs[:0:0]
	for _, m := range msgs {
		if keep[int64(m.ID)] == nil && tooOldToAnnounce(m.CreatedTS.Time, now) {
			continue
		}
		out = append(out, m)
	}
	return out
}
```

##### 🔵 Refactor
- [ ] No duplication: one filter is used by both branches.
- [ ] Reveals intent: the `keep` argument names the exemption.
- [ ] Complexity: one added loop and no new branches in the main body.
- [ ] No shallow wrapper: `withoutTooOld` owns the rule plus its exemption.
- [ ] Fits existing patterns: it mirrors the `held` and `back` maps in the same function.

### Behavior 3: Closure (a): the hook stays quiet about old mail [BLOCKING]

#### Test Specification
**Given** fake Agent Mail with #900 for BlueLake, created 8 days ago, and a told record for #900
with told_at 7 days and 1 minute ago (left by an earlier hook). **When** `ntm agent-hook stop` runs.
**Then** it prints nothing, #900's told record still has its old told_at, and
`--robot-events --events-recipient=BlueLake` has no notice for #900.

No property: fixed input, nothing to generate over.

**Files touched**: `internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
```go
func TestClosure_AgeCutoffHookQuiet(t *testing.T) {
	mail := newFakeAgentMail(t)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour)
	mail.addMessage("BlueLake", agentmail.InboxMessage{ID: 900, From: "RedStone", Subject: "old news", CreatedTS: agentmail.FlexTime{Time: old}})
	token := mail.issueToken("BlueLake")
	env := newClosureEnv(t)
	env.useAgentMail(mail)
	env.seedRegistry(t, "agecut", "BlueLake", token)
	env.set("AGENT_NAME", "BlueLake")
	toldAt := time.Now().UTC().Add(-state.ToldRecordRetention - time.Minute)
	env.seedToldRecord(t, env.workDir, "BlueLake", 900, state.ToldByStopHook, toldAt) // new small helper over state.ClaimToldRecord

	if out := env.runWithStdin(t, "{}", "execute", "agent-hook", "stop", "--project", env.workDir); out != "" {
		t.Fatalf("hook printed %q, want nothing", out)
	}
	if rec := env.told(t, env.workDir, "BlueLake", 900); rec == nil || !rec.ToldAt.Equal(toldAt.Truncate(time.Second)) {
		t.Fatalf("told record = %+v, want unchanged", rec)
	}
	if ev := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=BlueLake")); len(ev) != 0 {
		t.Fatalf("events = %+v, want none", ev)
	}
}
```
It fails today: the expired record is reclaimed and #900 is announced.

##### 🟢 Green
B2's change. No new code: this test proves the hook path goes through `mailToAnnounce`.
`env.seedToldRecord` is a test helper. It writes through `state.ClaimToldRecord`, the same call an
earlier hook makes, so it seeds SOURCE only.

##### 🔵 Refactor
- [ ] No duplication: the setup lines it shares with `TestClosure_StopHookTellsOnce` go into one
      `newHookClosure(t, agent)` helper if a third test needs them.
- [ ] Reveals intent, with fewer branches, no wrappers, and the existing closure layout.

### Behavior 4: Closure (a): the nudge stays quiet about old mail [BLOCKING]

#### Test Specification
**Given** a spawned, idle stand-in agent, and #903 created 8 days ago with an expired told record.
**When** the monitor runs for 6 seconds, which is at least 5 polls at the 1s interval.
**Then** nothing is typed into the pane, and #903's told record is unchanged.

No property: fixed input.

**Files touched**: `internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
```go
func TestClosure_AgeCutoffNudgeQuiet(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "agecutnudge")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 903, From: "RedStone", Subject: "old news",
		CreatedTS: agentmail.FlexTime{Time: time.Now().UTC().Add(-8 * 24 * time.Hour)}})
	env.seedToldRecord(t, env.projectDir("agecutnudge"), agent, 903, state.ToldByIdleNudge, time.Now().UTC().Add(-state.ToldRecordRetention-time.Minute))
	env.startMonitor(t, "agecutnudge")
	time.Sleep(6 * time.Second)
	if typed := agents.typed(t, pid); strings.Contains(typed, "#903") {
		t.Fatalf("nudge typed %q", typed)
	}
}
```

##### 🟢 Green
B2's change.

##### 🔵 Refactor
The checklist as B3. Use one 6s quiet window, not several short sleeps.

### Behavior 5: Closure (c): a 6-day-old message is still told [BLOCKING]

#### Test Specification
**Given** #901, created 6 days ago, with no told record. **When** the hook runs twice.
**Then** the first run's reminder names `[#901] from RedStone: still fresh`, the told record says
stop_hook, and the second run prints nothing.

No property: fixed input.

**Files touched**: `internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
This is the same layout as B3 with `CreatedTS` −6d and no seeded record. It asserts
`strings.Contains(reason, "[#901] from RedStone: still fresh")` and `env.told(...).ToldBy == state.ToldByStopHook`.
It passes before B2 and must stay green after. Red-first: run it against a deliberately wrong
cutoff (`> 5*24h`) to see it fail, then remove that.

##### 🟢 Green
No new code.

##### 🔵 Refactor
The shared hook-closure helper from B3.

### Behavior 6: Failure check: a future time is still announced [BLOCKING]

#### Test Specification
**Given** #902 with `created_ts` 1 hour in the future. **When** the hook runs.
**Then** the reminder names `[#902]`, the told record says stop_hook, the hook log
(`ntm` log file in the closure env) has no line at WARN or above, and the told records and notices
of other messages are unchanged.

No property: one fixed bad input, as the sprint's failure check.

**Files touched**: `internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
This is the B5 layout with `CreatedTS` +1h. It also reads the hook log and asserts there's no
`level=WARN`. It fails if the cutoff uses `now.Sub(created)` with an absolute value or similar.

##### 🟢 Green
B1's rule already gives false for future times.

##### 🔵 Refactor
The checklist as B3.

### Behavior 7: Pinned created_ts parsing [LEAF: decoding only, with no async]

#### Test Specification
**Given** `internal/agentmail/testdata/fetch_inbox_live_2026-11-10.json`, one raw `fetch_inbox` JSON-RPC
result captured from the live Agent Mail container on rocketman, with tokens redacted.
**When** it's decoded the way `FetchInbox` decodes results.
**Then** each message's `CreatedTS` equals the UTC instant written next to the fixture in
`fetch_inbox_live_2026-11-10.expected.txt`. The test also asserts the raw `created_ts` string matches
the captured layout (with or without a zone suffix).

**Capture step (build agent, before Red)**: with the HTTP tool URL from `~/.config/ntm/config.toml`,
POST a `tools/call` for `fetch_inbox` (limit 2, a test agent) and save the body. Run
`sed -E 's/("(registration_)?token"[: ]*")[^"]+/\1REDACTED/g'`. Record the raw `created_ts` strings
and their meaning in UTC.

**Property**: Roundtrip. For any time `t`, `FlexTime{t}` marshalled then unmarshalled equals `t` in
UTC, to the microsecond, and a zone-less string `t.UTC().Format("2006-01-02T15:04:05.999999")`
decodes to `t.UTC()`.

**Files touched**: `internal/agentmail/flextime_live_test.go` (new) and the two testdata files.

#### TDD Cycle
##### 🔴 Red
Write the test against the fixture first, with an expected instant computed by hand from the
captured string. If the live format turns out to be zone-less local time, it fails, and that finding
goes to the user before Green.

##### 🟢 Green
No change if the format is UTC or carries a zone (expected). Otherwise make the fix in `FlexTime`,
and only for the captured layout.

##### 🔵 Refactor
Keep the fixture minimal: two messages, with no bodies.

### Behavior 8: Doc updates [LEAF: documentation only, with no behavior]

#### Test Specification
**Given** this sprint's change. **When** it lands. **Then**:
- `TestClaimToldRecord_ExpiredClaimWinsAgain` has a comment: "Reminders don't repeat after expiry:
  mailToAnnounce skips messages older than ToldRecordRetention (Loop 7)."
- Line 623 of `../2026-11-06-attention-feed-message-bus/SCOPE.md` reads: "A message older than 7
  days is never announced again (Loop 7 age cutoff); a snooze still returns once."

No property: there's no input.

**Files touched**: `internal/state/state_test.go`, `thoughts/.../2026-11-06-attention-feed-message-bus/SCOPE.md`.

#### TDD Cycle
##### 🔴 Red
`grep -q 'Loop 7' internal/state/state_test.go` fails.
##### 🟢 Green
Add the comment and the note.
##### 🔵 Refactor
None needed. The text is final.

### Failure check

- Command: the closure test (B6) sends a message whose `created_ts` is 1 hour in the future, and runs
  `ntm agent-hook stop` as its recipient.
- Expected: the reminder names it, there's no error and no warning in the hook log, and the told
  record says stop_hook.
- Unchanged: other messages' told records and the attention feed's other notices.

### Human test script

(rocketman, after building the new commit to `/tmp/ntm-loop7`)
1. Pick an agent that isn't running and has an old message among its 50 newest:
   `cd ~/ntm_Dev/zigBrowser && /tmp/ntm-loop7 mail inbox zigBrowser --agent <agent> --json --limit 50`.
   Note one message `M` with a `created_ts` more than 7 days ago, and one message `Y` from the last 6
   days. If no agent has a young message, send one: `/tmp/ntm-loop7 mail send zigBrowser --to <agent> "age check"`.
2. Back up the state database to `/mnt/agent-tmp`, then delete any told records for `M` and `Y`:
   `sqlite3 ~/.config/ntm/state.db "delete from told_records where agent_name='<agent>' and message_id in (<M>,<Y>);"`.
3. Run the hook as that agent:
   `echo '{}' | AGENT_NAME=<agent> /tmp/ntm-loop7 agent-hook stop --project <project path>`.
4. Pass: the reminder names `[#<Y>]` and doesn't name `[#<M>]`.
5. Pass: `told_records` has a row for `Y` and none for `M`, and `attention_events` has no new notice for `M`.
6. Run step 3 again. Pass: no output, because `Y` is now told and `M` is still skipped.

Note: step 3 marks every young untold message for that agent as told. Use an agent that isn't running.

---

## Sprint 2: A late snooze still returns

| Acceptance criterion (loop document) | Behaviors |
|---|---|
| Testable function: a message over 7 days old whose snooze just ended is announced once, and never again | B12 |
| `mailToAnnounce` keeps every ended item, whatever its age | B9 |
| Closure (b): a snooze at its latest allowed time returns exactly once | B12 |
| An ended snooze outside the 50 newest is still announced | B10 |
| The told record says idle_nudge after the return, and the snooze is cleared | B12 |
| Sprint 1's tests still pass | full-suite command |
| (4c decision) A snooze must end 1h before its notice expires | B11 |

### Behavior 9: Exemption in mailToAnnounce [LEAF: same-module pure step]

#### Test Specification
**Given** an ended snooze for #910, and #910 also fetched with `CreatedTS` 8 days ago.
**When** `mailToAnnounce` runs. **Then** it returns #910 once, marked ended.

**Property**: Invariant, extending B2's property. Every id in `ended` is returned exactly once,
whatever its `CreatedTS`.

**Files touched**: `internal/cli/mail_snooze_test.go` (the code is already in B2's `keep` map).

#### TDD Cycle
##### 🔴 Red
```go
func TestMailToAnnounce_EndedSnoozeIgnoresAge(t *testing.T) {
	now := time.Now().UTC()
	snoozes := fakeSnoozes{ended: []state.SnoozedMessage{{MessageID: 910, From: "GreenCastle", Subject: "late"}}}
	msgs := []agentmail.InboxMessage{{ID: 910, CreatedTS: agentmail.FlexTime{Time: now.Add(-8 * 24 * time.Hour)}}}
	ids, ended := announcedIDs(mailToAnnounce(snoozes, "/p", "BlueLake", msgs, now))
	if fmt.Sprint(ids) != "[910]" || fmt.Sprint(ended) != "[910]" {
		t.Fatalf("ids %v ended %v", ids, ended)
	}
}
```
Red-first: run it with `keep` passed as nil to see it fail. With B2's code, it passes.

##### 🟢 Green
B2's `withoutTooOld(msgs, back, now)`.

##### 🔵 Refactor
Confirm that `back` is built before the filter, so there's one source for the exemption.

### Behavior 10: Ended snooze outside the fetch [BLOCKING]

#### Test Specification
**Given** an ended snooze for #911, which isn't among the fetched messages, seeded with
`seedEndedSnooze` (the notice and snooze an earlier hook and `ntm mail snooze` leave).
**When** `ntm agent-hook stop` runs. **Then** the reminder names `[#911] from GreenCastle: late`
once, and a second run prints nothing.

No property: fixed input. This extends `TestClaimUnread_EndedSnooze` (`mail_snooze_test.go:295`) through
the real command.

**Files touched**: `internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
The B3 layout, with `seedEndedSnooze(t, store, env.workDir, "BlueLake", 911, "GreenCastle", "late", now)`
and no fetched mail. It must stay green. Red-first: comment out the `ended` append loop in
`mailToAnnounce` to see it fail.
##### 🟢 Green
No new code.
##### 🔵 Refactor
The shared helper.

### Behavior 11: Snooze limit leaves a return margin [BLOCKING]

#### Test Specification
**Given** a notice for #F expiring at E.
**When** `ntm mail snooze F --until <E − 1h + 1m>` runs. **Then** it exits non-zero with
`snooze would end after its notice expires: message F's notice expires at <E>; snooze until <E − 1h> at the latest`,
and F's item state and told record are unchanged.
**When** `--until <E − 1h>` runs. **Then** it succeeds.

**Property**: Invariant. For a random `until` in (now, E], it's accepted only if
`until <= E − SnoozeReturnMargin`. Driven through `SnoozeAddressedMessage`.

**Files touched**: `internal/state/runtime_store.go`, `internal/state/state_test.go`,
`internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
- State test: a snooze `until = expires − 30m` must return `ErrSnoozePastNotice`. Today it succeeds.
- Closure: `env.snoozeAs(agent, F, "--until", <E−1h+1m RFC3339>, "--project", dir)`. It expects an error
  with the exact text, and `GetAttentionItemState` unchanged.

##### 🟢 Green
**File**: `internal/state/runtime_store.go`
```go
// SnoozeReturnMargin is how long before its notice expires a snooze must end,
// so the return is seen while the notice still exists (SnoozedMessages reads
// only live notices).
const SnoozeReturnMargin = time.Hour

	if event.ExpiresAt != nil {
		latest := event.ExpiresAt.Add(-SnoozeReturnMargin)
		if req.Until.After(latest) {
			return time.Time{}, fmt.Errorf("%w: message %d's notice expires at %s; snooze until %s at the latest",
				ErrSnoozePastNotice, req.MessageID, event.ExpiresAt.UTC().Format(time.RFC3339), latest.UTC().Format(time.RFC3339))
		}
	}
```

##### 🔵 Refactor
- [ ] One constant, next to `SnoozeLimit`.
- [ ] Update the existing past-notice test's expected text.
- [ ] No new branches beyond the one comparison.

### Behavior 12: Closure (b): a late snooze returns once [BLOCKING]

#### Test Specification
**Given** a spawned, idle stand-in agent, and #912 in fake Agent Mail created 7 days and 10 seconds ago.
Its notice is seeded through `AppendAddressedAttentionEvent`, with `expires_at = now + 1h + 6s`. That's
what the hook left about 7 days ago, with ntm's notice time slightly after Agent Mail's created time.
It's snoozed through the real command `ntm mail snooze 912 --until <now + 5s RFC3339>`, the latest time
allowed by B11 at that point.
**When** the monitor runs. **Then** exactly 1 reminder names `[#912]` within 12s. After 6 more
seconds (5 polls) there's still exactly 1, and `runStopHook` prints nothing. The told record says
idle_nudge, and `SnoozedMessages` has no ended entry for #912.

No property: fixed timeline.

**Files touched**: `internal/cli/reminder_age_test.go`.

#### TDD Cycle
##### 🔴 Red
```go
func TestClosure_LateSnoozeReturnsOnce(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "latesnooze")
	now := time.Now().UTC()
	mail.addMessage(agent, agentmail.InboxMessage{ID: 912, From: "RedStone", Subject: "late snooze",
		CreatedTS: agentmail.FlexTime{Time: now.Add(-state.ToldRecordRetention - 10*time.Second)}})
	project := env.projectDir("latesnooze")
	env.seedNotice(t, project, agent, 912, "RedStone", "late snooze", now.Add(time.Hour+6*time.Second)) // small helper over AppendAddressedAttentionEvent
	snoozeInSession(t, env, "latesnooze", agent, 912, now.Add(5*time.Second).Format(time.RFC3339))
	env.startMonitor(t, "latesnooze")
	const entry = "[#912] from RedStone: late snooze"
	waitTypedCount(t, agents, pid, entry, 1, 12*time.Second)
	agents.setScreen(t, pid, "cc_idle.txt")
	time.Sleep(6 * time.Second)
	if n := strings.Count(agents.typed(t, pid), entry); n != 1 {
		t.Fatalf("reminded %d times, want 1", n)
	}
	if out := runStopHook(t, env, "latesnooze", agent); out != "" {
		t.Fatalf("hook repeated it: %q", out)
	}
	if rec := env.told(t, project, agent, 912); rec == nil || rec.ToldBy != state.ToldByIdleNudge {
		t.Fatalf("told record = %+v", rec)
	}
}
```
Red-first: with the exemption removed (`keep` nil), the return is dropped and the test fails at
`waitTypedCount`.

##### 🟢 Green
B2's exemption and B11's margin. No new product code.

##### 🔵 Refactor
`env.seedNotice` shares its body with `seedEndedSnooze`. Extract one `seedMailNotice` that both use.

### Failure check

- Command: `AGENT_NAME=A /tmp/ntm-loop7 mail snooze <F> --until <E − 1h + 1 minute, RFC3339>`, where `E`
  is F's notice expiry.
- Expected error:
  `snooze would end after its notice expires: message <F>'s notice expires at <E>; snooze until <E − 1h> at the latest`.
- Unchanged: `F`'s snooze state and its told record.

### Human test script

(rocketman, nudge on, `/tmp/ntm-loop7`)
1. Run `/tmp/ntm-loop7 spawn latesnooze --cc=1`, and wait for the agent `A` to be idle with an empty input box.
2. Find an old message `M` for `A`'s project. If there isn't one, use a message from Sprint 1's agent,
   and run this test in that project instead.
3. Send a fresh mail `F` to `A` (`/tmp/ntm-loop7 mail send latesnooze --to A "late snooze"`), wait for
   its reminder, then snooze it: `AGENT_NAME=A /tmp/ntm-loop7 mail snooze <F> --until 30m`.
4. Back up `state.db` to `/mnt/agent-tmp`. Then point `F`'s notice at the old message and end the snooze now:
   - Find the notice and its item key:
     `sqlite3 ~/.config/ntm/state.db "select cursor, case when trim(coalesce(dedup_key,''))!='' then 'dedup:'||trim(dedup_key) else 'cursor:'||cursor end from attention_events where message_id=<F> and recipient_agent='A';"`
   - See the stored time format:
     `sqlite3 ~/.config/ntm/state.db "select snoozed_until from attention_item_states where item_key='<key>';"`
   - Run `sqlite3 ~/.config/ntm/state.db "update attention_events set message_id=<M> where cursor=<cursor>;"`.
   - Run `sqlite3 ~/.config/ntm/state.db "update attention_item_states set snoozed_until='<one minute ago, same format>' where item_key='<key>';"`.
5. Wait for the next poll, about 30s. Pass: one reminder names `[#<M>]`, with `F`'s saved subject
   "late snooze", and the log shows `idle nudge: reminder sent … messages=[<M>]`.
6. Wait 2 more minutes. Pass: no further reminder for `[#<M>]`.
7. Restore `state.db` from the backup if you want, and kill the session.

---

## Hardening targets

| Target | Why it is high risk |
|---|---|
| `internal/cli/idle_nudge.go:tooOldToAnnounce` | an off-by-one or sign error either hides new mail or brings back the weekly repeat |
| `internal/cli/idle_nudge.go:mailToAnnounce` (with `withoutTooOld`) | a dropped exemption silently loses snooze returns, and a dropped filter repeats old mail |
| `internal/state/runtime_store.go:SnoozeAddressedMessage` (margin check) | a silent pass lets a snooze end after its notice, and the message never returns |

## Workflow Closure

### Production Operation Chain
`Claude turn end | monitor poll -> fetch_inbox (Agent Mail) -> unreadMessages -> mailToAnnounce (SnoozedMessages, age cutoff) -> claimAnnounce (ClaimToldRecord | ClaimEndedSnooze) -> feed notice + reminder text -> hook stdout | tmux pane`

### Closure Test: an old, already-told message stays silent at turn end (B3)   [BLOCKING]
- **SOURCE (seed only)**: fake Agent Mail inbox (#900, created −8d); told record via `ClaimToldRecord` (told_at −7d−1m).
- **TRIGGER (start)**: `ntm agent-hook stop` (`runAgentHookStop`, `internal/cli/agent_hook.go`), boundary = outermost.
- **DRIVERS (async edges)**: none (synchronous).
- **OBSERVE (assert via)**: hook stdout; `env.told` (GetToldRecord); `ntm --robot-events`.
- **FORBIDDEN SPAN**: `unreadMessages`, `mailToAnnounce`, `claimAnnounce`, `RecordMailNotice` must not be called or stubbed.
- **RED-AT-SEAM proof**: remove the `withoutTooOld` call, and the hook prints #900.
- **DRIVABILITY**: the store seam is present (temporary `state.db` via `NTM` env). No clock is needed (real now).
- **EXECUTION (must run)**: the fake Agent Mail HTTP server runs in-process. With no server, the test fails rather than skipping.

### Closure Test: an old, already-told message is never typed by the nudge (B4)   [BLOCKING]
- **SOURCE**: fake inbox #903 (−8d), with an expired told record.
- **TRIGGER**: `ntm spawn` + session monitor (`startMonitor`), boundary = outermost.
- **DRIVERS**: the monitor's 1s poll (`idle_nudge_interval_seconds = 1`), with a 6s window.
- **OBSERVE**: the stand-in's typed file (`agents.typed`).
- **FORBIDDEN SPAN**: the nudge's `poll`, `mailToAnnounce`, `claim`, `Send`.
- **RED-AT-SEAM proof**: remove the filter, and #903 is typed within 2s.
- **DRIVABILITY**: the private tmux server and the stand-in agent, already in the harness.
- **EXECUTION**: needs tmux on PATH. Without it the harness fails, as the Loop 5 tests do.

### Closure Test: a 6-day-old message is still told once (B5)   [BLOCKING]
- **SOURCE**: fake inbox #901 (−6d), with no told record.
- **TRIGGER**: `ntm agent-hook stop`.
- **DRIVERS**: none.
- **OBSERVE**: hook stdout, twice; `env.told`.
- **FORBIDDEN SPAN**: as B3.
- **RED-AT-SEAM proof**: a cutoff of 5d instead of 7d drops #901 and turns the test red.
- **DRIVABILITY / EXECUTION**: as B3.

### Closure Test: a future-dated message is announced without error (B6, failure check)   [BLOCKING]
- **SOURCE**: fake inbox #902 (+1h).
- **TRIGGER**: `ntm agent-hook stop`.
- **DRIVERS**: none.
- **OBSERVE**: hook stdout, the told record, and the hook log file.
- **FORBIDDEN SPAN**: as B3.
- **RED-AT-SEAM proof**: `now.Sub(created)` taken as an absolute value drops #902.
- **DRIVABILITY / EXECUTION**: as B3.

### Closure Test: an ended snooze outside the fetch is told once (B10)   [BLOCKING]
- **SOURCE**: the notice and ended snooze (`seedEndedSnooze`), with an empty fake inbox.
- **TRIGGER**: `ntm agent-hook stop`.
- **DRIVERS**: none.
- **OBSERVE**: hook stdout, twice.
- **FORBIDDEN SPAN**: `SnoozedMessages`, `ClaimEndedSnooze`.
- **RED-AT-SEAM proof**: drop the `ended` append loop in `mailToAnnounce`, and there's no reminder.
- **DRIVABILITY / EXECUTION**: as B3.

### Closure Test: a snooze past the return margin is refused (B11)   [BLOCKING]
- **SOURCE**: a notice for F with a known `expires_at`.
- **TRIGGER**: `ntm mail snooze` (`runMailSnooze`, `internal/cli/mail.go:1170`).
- **DRIVERS**: none.
- **OBSERVE**: exit status and stderr text; `GetAttentionItemState` via `ntm --robot-attention` or the env's store read helper.
- **FORBIDDEN SPAN**: `SnoozeAddressedMessage`.
- **RED-AT-SEAM proof**: without the margin, `--until E−30m` succeeds.
- **DRIVABILITY / EXECUTION**: the fake Agent Mail inbox check (`snoozeInboxLimit`) and the temporary store.

### Closure Test: a message over 7 days old returns once from a late snooze (B12)   [BLOCKING]
- **SOURCE**: fake inbox #912 (−7d−10s); the notice (`expires_at` now+1h+6s), written as the hook would have.
- **TRIGGER**: `ntm mail snooze`, then the session monitor. Boundary = outermost for both.
- **DRIVERS**: the monitor's 1s poll.
- **OBSERVE**: the stand-in's typed file, `runStopHook` output, and `env.told`.
- **FORBIDDEN SPAN**: `SnoozedMessages`, `mailToAnnounce`, `ClaimEndedSnooze`, `Send`.
- **RED-AT-SEAM proof**: remove the exemption (`keep` nil), and there's no reminder within 12s.
- **DRIVABILITY**: the existing harness. The real clock is used, with a 5s snooze.
- **EXECUTION**: needs tmux. If it's absent, the test fails closed.

## Verification commands

```bash
cd /home/maceo/Dev/ntm
go build ./... && go vet ./internal/cli/ ./internal/state/ ./internal/agentmail/
go test ./internal/cli/ -run 'AgeCutoff|TooOld|MailToAnnounce|LateSnooze|SnoozeMargin|EndedSnooze' -count=1
go test ./internal/state/ -run 'Snooze|ToldRecord' -count=1 -race
go test ./internal/agentmail/ -run 'FlexTime' -count=1
for i in $(seq 5); do go test ./internal/cli/ -run 'Closure_(AgeCutoff|LateSnooze|Snooze)' -count=1 || break; done
go test -short ./...    # only the known failures (bd-5zbcm, bd-dwpes, bd-cxa1s) may fail
grep -q 'Loop 7' internal/state/state_test.go
gofmt -l internal/cli internal/state internal/agentmail
```

## References

- Scope: [SCOPE.md](SCOPE.md). Loop doc: [07-reminder-age-cutoff.md](07-reminder-age-cutoff.md).
- Prior plan: `../2026-11-06-attention-feed-message-bus` (Loops 3, 5 and 6).
- Evidence: `/home/debian/Dev/ntm-test-evidence/loop6-sprint2-local-2026-11-10/` on the VPS.
- Closure framework: `~/.claude/commands/references/closure-test-framework.md`.
