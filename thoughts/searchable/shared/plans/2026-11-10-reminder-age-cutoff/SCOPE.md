# Reminder Age Cutoff — Product Scope

Scope version: 1.0 (2026-11-10)
Intended release: Loop 7 of the attention-feed plan
(`thoughts/searchable/shared/plans/2026-11-06-attention-feed-message-bus`), one small loop.

## 0. Overview and Release Boundary

**Goal:** an agent is reminded about a message once. An old message it has already been told about is
never announced again, however quiet the agent is. A message coming back from a snooze is still
announced once.

**Problem:** told records last 7 days (`ToldRecordRetention`, `internal/state/runtime_store.go:2437`).
After that, `ClaimToldRecord` treats the old record as gone, and `TestClaimToldRecord_ExpiredClaimWinsAgain`
asserts this. Agent Mail never reports a read time, so `unreadMessages` (`internal/cli/idle_nudge.go:375`)
treats every message as unread. The Stop hook and the idle nudge look at the agent's 50 newest messages
(`stopHookFetchLimit = 50`). So on an agent with fewer than 50 messages a week, a message told long ago
is announced again about every 7 days, with no end.

**Core Principle:** never announce what the told records can no longer vouch for, but never lose a
returning snooze. When the age can't be known, announce: a repeat is better than a missed message
(the existing Fail Open rule).

**Expected Lifetime:** lasts until Agent Mail reports read state. Then the cutoff stays as a backstop.

**Included Capabilities / Responsibilities:**
- **Age Cutoff:** `mailToAnnounce` leaves out a plain inbox message whose `created_ts` is more than
  `ToldRecordRetention` before now. The Stop hook and the idle nudge both get this, because they
  share the step.
- **Snooze Return Exemption:** an item returning from an ended snooze is kept whatever its age.
- **Unknown Age Rule:** a message with no `created_ts`, or a zero one, is announced.
- **Feed Cutoff:** the hook no longer records an old message in the attention feed. This follows from
  the hook recording only the items `mailToAnnounce` returns.
- **Pinned Time Parsing:** a test pins how `FlexTime` reads Agent Mail's `created_ts`, using one real
  inbox response captured on rocketman.

**Excluded:**
- Real read-state tracking.
- Changing the 7-day told retention or the 50-message window.
- The repeated-send-failure retry and a per-message snooze limit.
- The `ntm mail inbox` display and `ntm mail snooze` validation.

**Existing System Relationship:** changes one shared step, `mailToAnnounce` (`internal/cli/idle_nudge.go:90`).
Callers: `claimUnread` in `internal/cli/agent_hook.go` and the nudge poll in `internal/cli/idle_nudge.go:223`.
Told records, snoozes and claims don't change.

**Compatibility:** no saved-data change and no new config. The reminder text format doesn't change.

## 1. High-Level Pipeline

```yaml
system:
  inputs:
    - name: "InboxMessage list"
      source: "Agent Mail fetch_inbox (50 newest)"
      data_contract: "InboxMessage"
    - name: "SnoozedMessage lists (active, ended)"
      source: "state store SnoozedMessages"
      data_contract: "SnoozedMessage"
  processing:
    - "Choose Mail To Announce"
  outputs:
    - name: "announceItem list"
      destination: "claimAnnounce, then the reminder text (hook) or the pane (nudge)"
      data_contract: "announceItem"
  trigger: "Claude Stop hook (ntm agent-hook stop) and each idle-nudge poll (30s)"
  delivery_form: "Go CLI (ntm)"
```

## 2. Core Processes

### Choose Mail To Announce

**Capability / Objective:** Age Cutoff, Snooze Return Exemption, Unknown Age Rule.
**Actor and Trigger:** the Stop hook at turn end, or the session monitor's idle-nudge poll.
**Preconditions:** the inbox has been fetched, and unread messages have been chosen.
**Dependencies:** `ToldRecordRetention`, `SnoozedMessages`, and `InboxMessage.CreatedTS`.

| Step | Responsibility | Input / From | Action | Output / To |
|---|---|---|---|---|
| 1 | mailToAnnounce | SnoozedMessage lists | drop active snoozes; mark ended ones (as today) | working set |
| 2 | mailToAnnounce | each plain item (`ended == nil`) | if `CreatedTS` is set and `now - CreatedTS > ToldRecordRetention`, drop it | working set |
| 3 | mailToAnnounce | ended-snooze items | keep them all, never age-checked | announceItem list → claimAnnounce |

**Rules:** Age Cutoff Rule, Snooze Exemption Rule, Unknown Age Rule, Future Time Rule (section 5).
**Completion:** the returned list holds only items the caller may claim.
**Effects and Ordering:** pure; it writes nothing. A dropped message gets no told record and no feed notice.
**Failure:** if the snoozes can't be read, plain items are returned as today (Fail Open), and the age
cutoff still applies to them.

**Success Example:**
Given message 900, created 8 days ago, whose told record expired yesterday, when the hook runs,
then no reminder names #900, no told record is written, and no feed notice is recorded.

**Failure / Boundary Example:**
Given message 901, created 7 days and 10 seconds ago, snoozed until its notice's expiry (notice time
plus 7 days), when the snooze ends and the nudge polls, then one reminder names [#901] and the told
record says idle_nudge. Over 5 more polls there's no further reminder.

## 3. Users and Callers

| Actor / Role | Objective | Allowed Actions | Data or Access Boundary |
|---|---|---|---|
| Coding agent (Claude, Codex) | hear about new mail once | reads reminders, runs fetch_inbox and snooze | its own inbox |
| Stop hook (`ntm agent-hook stop`) | tell the agent at turn end | claims and announces | one agent, one project |
| Idle nudge (session monitor) | tell an idle agent | claims and types reminders | the session's agents |

## 4. Shared Data Contracts

```yaml
data_contract:
  name: "InboxMessage"
  meaning: "one Agent Mail message as fetch_inbox returns it"
  identity: "ID (Agent Mail message id)"
  fields:
    - name: "CreatedTS"
      type: "FlexTime (time.Time)"
      required_and_default: "optional; zero when missing"
      constraints: "RFC3339 or zone-less ISO8601; zone-less is read as UTC today (internal/agentmail/types.go:33-51)"
  created_by: "Agent Mail"
  read_by: ["mailToAnnounce", "buildReminderText"]
  changed_by: "immutable"
  lifetime: "in memory only"
  invariant: "a told record for a message is always written after that message's CreatedTS"
  example: '{"id": 12525, "subject": "come back", "created_ts": "<captured format>"}'
```

`announceItem` and `SnoozedMessage` are unchanged (`internal/cli/idle_nudge.go`, `internal/state/runtime_store.go`).

## 5. Shared Domain and Conditional Rules

| Rule Name | If / Condition | Then / Required Result | Applies To | Reason / Evidence |
|---|---|---|---|---|
| Age Cutoff Rule | plain item and `now - CreatedTS > ToldRecordRetention` | left out | hook, nudge | told records can't vouch past 7 days; answer to Q1 |
| Snooze Exemption Rule | item has `ended != nil` | kept whatever its age | hook, nudge | a snooze may end at notice time plus 7 days, past the message's 7-day mark |
| Unknown Age Rule | `CreatedTS` is zero | kept | hook, nudge | Fail Open; answer to Q2 |
| Future Time Rule | `CreatedTS` is after now | kept, with no error | hook, nudge | clock skew must not hide new mail |
| One Constant Rule | the cutoff age | equals `ToldRecordRetention`, with no copy | code | so the two can't drift apart; answer to Q1 |

## 6. Release Acceptance and Quality

**Required Process Acceptance:** both examples in section 2.

**Connected Release Journey:** a real Agent Mail message reaches an agent through the hook, is told,
and the told record ages past 7 days (set in the test store). Then the next turn end and 5 nudge polls
give 0 reminders for it.

**Journey Example:** closure tests at the real entry points (`ntm agent-hook stop` and the monitor poll):
(a) old and told, with the record expired: 0 reminders;
(b) a snooze until the notice's expiry: exactly 1 return, then 0 over 5 polls;
(c) a message 6 days old whose told record was lost: still announced once.

**Cross-Process Failure:** the store can't be read. Plain items are still age-checked, and young ones
are announced.

**First Useful Result:** the hook path alone stops the weekly repeat.

**Required Deliverables:**
1. The cutoff and exemption in `mailToAnnounce`.
2. The Unknown Age and Future Time rules.
3. A `FlexTime` test pinned to a captured real response.
4. Closure tests (a), (b) and (c).
5. A failure check: a future `created_ts` is kept, with no error.
6. `TestClaimToldRecord_ExpiredClaimWinsAgain` kept, with a comment saying the cutoff now stops the
   repeat upstream. The attention-feed scope's "announced once more after 7 days" note updated.

## 7. Technical Constraints and Decisions

| Choice / Constraint | Status | Applies To | Reason / Source | Decision Owner |
|---|---|---|---|---|
| Cutoff equals `ToldRecordRetention` | Confirmed | mailToAnnounce | Q1 answer | Maceo |
| Missing time is announced | Confirmed | mailToAnnounce | Q2 answer | Maceo |
| Pin parsing to a captured real response | Confirmed | agentmail tests | Q3 answer | Maceo |
| The feed skips old messages | Confirmed | hook | Q4 answer | Maceo |
| One loop (Loop 7) | Confirmed | plan | Q5 answer | Maceo |
| The cutoff lives in `mailToAnnounce`, not in `unreadMessages` | **Assumption** | code | `mailToAnnounce` is the only step that knows which items are ended snoozes | planning agent |
| The comparison is strict (`>`), so a message exactly 7 days old is kept | **Assumption** | Age Cutoff Rule | lines up with `ClaimToldRecord`'s expiry boundary | planning agent |
| The nudge's own feed recording (`Record` dep) follows the same list | **Assumption** | nudge | same reason as the hook | planning agent |

**High-Risk Assumption:** that the told-record invariant (a record is always written after its message's
CreatedTS) holds across clocks. If Agent Mail's clock runs ahead of ntm's, the cutoff falls up to that
skew late. Some reminders could repeat once, but none would be missed.

**Evidence Needed:** one captured `fetch_inbox` response from Agent Mail on rocketman, to see the
`created_ts` format and zone.

## 8. Terminology

| Canonical Term | Meaning |
|---|---|
| Plain item | an inbox message announced for the first time, not a snooze return (`ended == nil`) |
| Ended-snooze item | a message whose snooze is over, returning once (`ended != nil`) |
| Told record | a store row saying the agent was told about a message (7-day life) |
| Notice | the attention-feed event for a message; it expires 7 days after ntm records it |
| Cutoff | `ToldRecordRetention` (7 days) measured from `CreatedTS` |

## 9. Summary for LLM Use

- Change only `mailToAnnounce`, and add tests. Don't add config.
- Check only plain items for age. Never check ended-snooze items.
- Keep messages whose time is missing or in the future.
- Prove it with closure tests (a), (b) and (c) through the real hook and nudge entry points.
- Pin `created_ts` parsing to a captured real Agent Mail response.
