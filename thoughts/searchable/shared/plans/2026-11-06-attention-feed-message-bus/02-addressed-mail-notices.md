# Loop 2: Addressed mail notices

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Mail Notice Recording**, contract **AddressedAttentionEvent**.

## Overview

Mail recipients are only inside an event's `details` JSON today, so an agent can't efficiently ask for its own events. The dashboard also tracks seen mail in memory (`seenMailAttentionIDs`, `internal/tui/dashboard/dashboard.go:2933`), and `PublishMailPending` calls plain `Append` (`internal/robot/attention_feed.go:2059`). Once the feed is durable, each dashboard restart would write all unread mail again.

After this loop, a mail event names its recipient in columns that can be queried, and a message has only one event per recipient, however many processes see it.

Covers deliverables 3 and 6. Needs Loop 1.

---

## Sprint 1: Events addressed to an agent

### Testable function

"Send mail to two agents, then list only one agent's events with `ntm --robot-events --events-recipient=<name>`."

### Acceptance criteria

- [ ] A new numbered migration after `015` adds `recipient_project`, `recipient_agent` and `message_id` to `attention_events`, and upgrades an existing `state.db` automatically.
- [ ] `PublishMailPending` and `PublishMailAckRequired` fill in those fields.
- [ ] `--events-recipient=<name>` returns only that agent's events, and a busy feed can't push them out of the result.
- [ ] An unknown name returns `success: true` with `events: []`, following the field rules in `AGENTS.md`.
- [ ] Events that aren't addressed, such as `mail.unread` summaries, have an empty recipient and are unaffected without the flag.

### Requirements

**Data:**
- Add the three columns to `attention_events`, with an index on recipient and cursor. Existing rows stay empty.
- Add the same fields to `StoredAttentionEvent` and `AttentionEvent`, and carry them through `attentionEventToStored` and replay.

**API:**
- `--events-recipient` on `--robot-events`, next to `--events-category` and `--events-session` (`internal/cli/root.go:3274`), and listed in `--robot-capabilities`.
- The store filters by recipient in SQL. Filtering after reading a page (`filterEventsForRobot`, `internal/robot/events.go:347`) could return an empty page while matching events exist further along.
- Agent names can repeat across projects. **Assumption:** the flag matches the name in every project, and each event's output includes `recipient_project`.

**UI:** None. The output is JSON.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Migration on existing data | Open a `state.db` at version 015 with rows, then migrate | Rows are kept, with empty recipient fields |
| Recipient saved | Publish mail to BlueLake, then replay | Recipient fields are set |
| Filter | Mail to BlueLake and RedStone, filter on RedStone | Only RedStone's events |
| Busy feed | 500 other events after RedStone's, with `--events-limit=10` | RedStone's event is still returned |
| Unknown name | Filter on a name with no events | `success: true`, `events: []` |
| No flag | Replay without the flag | Same as before, plus the new fields where set |

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

### Testable function

"Restart a dashboard with unread mail already recorded, and see no new events written for those messages."

### Acceptance criteria

- [ ] `state.db` allows one mail event per project, recipient and message.
- [ ] A dashboard restart with 10 unread messages writes no new events for them.
- [ ] Two processes publishing the same message at once produce one event, and neither fails.
- [ ] A new message after the restart still produces one new event.
- [ ] The dedup is durable, and doesn't depend on the in-memory `seenMailAttentionIDs` map or a time window.

### Requirements

**Data:** A unique index on `recipient_project`, `recipient_agent` and `message_id`, applying only where `message_id` is set. It goes in a new migration, `017`, which first removes any duplicates a Sprint 1 build wrote. In Sprint 1's migration it would turn a repeat publish into a store failure.

**API:**
- A store insert that reports "already exists" rather than failing. The feed's existing `AppendDeduplicated` is window-based, so it doesn't meet this need.
- When a message already exists, the feed writes nothing, hands nothing to subscribers, and returns the existing event.
- The Publish functions use this path whenever `message_id` is set.
- A message that changes from pending to ack-required still counts as one message. **Assumption:** the first event stands.

**UI:** None. The dashboard has no popup for each new mail message, so a restart has none to replay. Its in-memory `seenMailAttentionIDs` map is removed, so every poll checks the store (approved at the TDD step).

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Repeat publish | Publish the same message twice | One event, second call reports "already exists" |
| Race | Many goroutines and two processes publish one message | One event, no errors |
| Same message, two recipients | Message 42 to BlueLake and to RedStone | Two events |
| Same id, two projects | Message 42 in two projects, same agent name | Two events |
| New message after restart | Reopen the feed and publish a new id | One new event |
| Subscribers | Subscribe, then publish a repeat | Handler isn't called for the repeat |

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
