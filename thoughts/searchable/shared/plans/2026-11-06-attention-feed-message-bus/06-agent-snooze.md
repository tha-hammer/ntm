# Loop 6: Agent snooze

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Agent Snooze**, rule **One Telling**.

## Overview

An agent that has been told about a message may act now or defer the work. An agent that defers without doing anything isn't reminded again. This loop gives it a way to defer to a chosen time: `ntm mail snooze`. Reminders skip the message until then, and it comes back afterward.

The feed already has snooze state: `AttentionStateSnoozed` and `SnoozedUntil` (`internal/state/runtime_schema.go:425`, `:443`), with an operator API in `ntm serve` (`internal/serve/server.go:5650-5660`). This loop adds an agent-facing command on top of it, addressed by Agent Mail message id.

Covers deliverable 14. Needs Loop 5.

---

## Sprint 1: Snooze a message

### Testable function

"Run `ntm mail snooze <id> --until <time>`, and see no reminder mention that message before the time. See an error for another agent's message or a past time."

### Acceptance criteria

- [ ] `ntm mail snooze <message-id> --until <time>` saves the snooze on that agent's event, and confirms the saved time.
- [ ] The Stop hook and the nudge leave the message out until the time passes.
- [ ] A message addressed to another agent, an unknown message id, or a time in the past returns an error and changes nothing.
- [ ] Snoozing the same message again replaces the time.
- [ ] Any message can be snoozed, whatever its importance (**Assumption**), and other messages in the same batch are unaffected.

### Requirements

**Data:** Uses the existing `attention_item_states` table. The item is found from the project, recipient and message id on the **AddressedAttentionEvent**, then its cursor, through `ResolveAttentionItemKey` (`internal/state/runtime_store.go:1897`).

**API:**
- New subcommand under `ntm mail` (`internal/cli/mail.go`). The agent is found with **Agent Identity Order**, and the project with the same order as the hook.
- `--until` accepts an RFC3339 time or a duration such as `30m`. **Assumption:** durations use `util.ParseDuration` (`internal/util/duration.go:21`).
- It shares validation with the serve API rather than copying it: a time in the past is rejected there too.
- A message that has an Agent Mail id but no feed event yet, because nothing has announced it, gets its event recorded first. **Assumption:** the command checks with Agent Mail that the message is addressed to this agent before recording it.
- The `ReminderText` instruction line tells agents about this command, with the exact syntax.

**UI:** One line of confirmation, plus the usual JSON output mode for this command family.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Snooze | Valid id and future time | Saved, and confirmed |
| Hook skips it | New mail plus a snoozed message, turn ends | Reminder lists only the other mail |
| Nudge skips it | Same, idle agent | Reminder lists only the other mail |
| Only snoozed mail | Nothing else new | No reminder at all |
| Other agent's message | Id addressed to RedStone | Error, nothing changes |
| Unknown id | Id that doesn't exist | Error |
| Past time | `--until` in the past | Error |
| Bad time | `--until=soon` | Error naming the accepted forms |
| Re-snooze | Snooze twice | The later time applies |
| No identity | No name found | Error explaining that the agent's name wasn't found |

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

---

## Sprint 2: Reminder after the snooze ends

### Testable function

"Leave the agent idle past the snooze time, and see one reminder that includes the message."

### Acceptance criteria

- [ ] The first poll after the snooze ends that finds the agent idle and safe to type into sends one reminder with that message.
- [ ] The reminder covers it once, then **One Telling** applies again, including at the next turn end.
- [ ] If the agent is working when the snooze ends, its next turn end includes the message (**One Telling**).
- [ ] A snoozed message returns when its snooze ends, whether or not it was read. Agent Mail reports no read state, so the earlier rule "a message read or acknowledged during the snooze isn't announced" was dropped (your choice at the TDD step, 2026-11-09).
- [ ] A snooze still ending within retention survives restarts of the monitor and the agent.

### Requirements

**Data:** Re-announcing replaces the `ToldRecord`, and clears the snooze in `attention_item_states`, in one transaction, so two callers can't both announce it.

**API:** The hook and nudge query for snoozes that have ended for this agent, then treat those messages as new. Agent Mail's read state isn't checked, because the server doesn't report it. **Assumption:** a snooze longer than the 7 day retention, or past its event's expiry, is rejected, because the event would be gone by then.

**UI:** A snoozed message returns in the normal `ReminderText`. **Assumption:** it isn't marked differently.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Ends while idle | Snooze passes, idle pane | One reminder with the message |
| Next poll | After that reminder | Nothing more |
| Ends while working | Snooze passes during a turn | Told at turn end, and no second nudge |
| Race | Hook and nudge at the same moment | One announcement |
| Read during snooze | Message read before the time | Still one reminder when the snooze ends (read state isn't reported) |
| With new mail | Snooze ends, and new mail arrives | One reminder listing both |
| Monitor restart | Restart while snoozed | Still honored, and returns on time |
| Over the limit | `--until 8d` | Error stating the 7 day limit |

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
