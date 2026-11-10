# Loop 7 — Reminder age cutoff

## Overview

The Stop hook (`ntm agent-hook stop`) and the idle nudge both choose what to announce in one shared
step, `mailToAnnounce` (`internal/cli/idle_nudge.go:90`). This loop adds an age cutoff there. A plain
message whose `created_ts` is more than `ToldRecordRetention` (7 days) before now is left out. An
ended-snooze item is always kept. Messages with a zero or future time are kept.

The loop has two sprints. Sprint 1 adds the cutoff, and Sprint 2 proves the snooze exemption at its
hardest edge. Scope: [SCOPE.md](SCOPE.md). Index: [00-master-index.md](00-master-index.md).

Facts checked on rocketman (2026-11-10):
- Agent Mail has real old messages. For example, in project zigBrowser, #2003 was created
  2026-05-30T17:47:22Z, to SapphirePeak.
- The live server is the Docker container `mcp-agent-mail-rust`. Its database isn't readable from
  the host, so tests go through ntm and Agent Mail's tools, not raw SQL against Agent Mail.

---

## Sprint 1 — Old mail stays quiet

**Testable function:** "At turn end, the Stop hook doesn't announce a message more than 7 days old,
even when its told record has expired, but it still announces a younger message."

**Acceptance criteria**
- [ ] `mailToAnnounce` drops a plain item when `now - CreatedTS > ToldRecordRetention`. The hook and
      the nudge both get this.
- [ ] A dropped message gets no told record and no attention-feed notice.
- [ ] A message with a zero `CreatedTS`, or one after now, is announced (the Unknown Age and Future
      Time rules).
- [ ] A test pins `FlexTime` parsing to one raw `fetch_inbox` response captured from the live Agent
      Mail on rocketman. It records the exact `created_ts` text, its zone, and the parsed UTC time.
- [ ] `TestClaimToldRecord_ExpiredClaimWinsAgain` stays, with a comment that the cutoff now stops the
      repeat upstream. The attention-feed SCOPE note "announced once more after 7 days" is updated.

**Requirements**
- Data: `InboxMessage.CreatedTS` (FlexTime) and `state.ToldRecordRetention`, with no copy of the constant.
- API: no new command, flag or config. The reminder text format doesn't change.
- UI: none. The visible effect is the absence of a reminder line.

**Test scenarios**

| Scenario | Steps | Expected result |
|---|---|---|
| (a) Old and told, record expired | message created 8 days ago; its told record's told_at is 7 days and 1 minute ago; run the hook, then 5 nudge polls | 0 reminders name it; no told record is rewritten |
| (c) Young message, record lost | message created 6 days ago; no told record; run the hook | 1 reminder names it; told record says stop_hook |
| Zero time | message with no `created_ts`; run the hook | announced once |
| Exactly 7 days | `now - CreatedTS == ToldRecordRetention` | announced (the comparison is strict `>`) |
| Pinned parse | the captured raw response, decoded | `CreatedTS` equals the expected UTC instant |

**Human test script** (rocketman, after building the new commit to `/tmp/ntm-loop7`)
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

**Failure check**
- Command: the closure test sends a message whose `created_ts` is 1 hour in the future, and runs
  `ntm agent-hook stop` as its recipient.
- Expected: the reminder names it, there's no error and no warning in the hook log, and the told
  record says stop_hook.
- Unchanged: other messages' told records and the attention feed's other notices.

---

## Sprint 2 — A late snooze still returns

**Testable function:** "A message more than 7 days old whose snooze has just ended is announced once
by the idle nudge, and never again."

**Acceptance criteria**
- [ ] `mailToAnnounce` keeps every item with `ended != nil`, whatever its `CreatedTS`.
- [ ] Closure test (b): a message created 7 days and 10 seconds ago, snoozed until its notice's
      expiry. After the snooze ends, exactly 1 reminder names it, and 0 more over the next 5 polls
      and 1 turn end.
- [ ] An ended-snooze item that isn't in the 50 newest is still announced (it comes from the store).
- [ ] The told record after the return says idle_nudge, and the snooze is cleared.
- [ ] The Sprint 1 tests still pass.

**Requirements**
- Data: `SnoozedMessage` and `announceItem.ended`, unchanged. No schema change.
- API: none new.
- UI: the return reminder shows `[#id]`, as today.

**Test scenarios**

| Scenario | Steps | Expected result |
|---|---|---|
| (b) Snooze at notice expiry | old message; notice expiry = notice time + 7d; snooze until expiry; advance the clock past it; 6 polls | exactly 1 reminder |
| Ended, outside the newest 50 | ended snooze for a message not in the fetch | announced once, using the saved sender and subject |
| Hook path | same as (b), via `ntm agent-hook stop` | exactly 1 reminder; the nudge doesn't repeat it |

**Human test script** (rocketman, nudge on, `/tmp/ntm-loop7`)
1. Run `/tmp/ntm-loop7 spawn latesnooze --cc=1`, and wait for the agent `A` to be idle with an empty input box.
2. Find an old message `M` for `A`'s project. If there isn't one, use a message from Sprint 1's agent,
   and run this test in that project instead.
3. Send a fresh mail `F` to `A` (`/tmp/ntm-loop7 mail send latesnooze --to A "late snooze"`), wait for
   its reminder, then snooze it: `AGENT_NAME=A /tmp/ntm-loop7 mail snooze <F> --until 30m`.
4. Back up `state.db` to `/mnt/agent-tmp`. Then point `F`'s notice at the old message and end the snooze now:
   `sqlite3 ~/.config/ntm/state.db "update attention_events set message_id=<M> where message_id=<F> and recipient_agent='A';"`
   and
   `sqlite3 ~/.config/ntm/state.db "update attention_item_states set snoozed_until=<one minute ago, in the stored format> where state='snoozed' and item_key in (<F's item key>);"`.
   The TDD plan (`07-reminder-age-cutoff-tdd.md`, Sprint 2 human test) gives the exact item-key lookup and time format.
5. Wait for the next poll, about 30s. Pass: one reminder names `[#<M>]`, with `F`'s saved subject
   "late snooze", and the log shows `idle nudge: reminder sent … messages=[<M>]`.
6. Wait 2 more minutes. Pass: no further reminder for `[#<M>]`.
7. Restore `state.db` from the backup if you want, and kill the session.

**Failure check**
- Command: `AGENT_NAME=A /tmp/ntm-loop7 mail snooze <F> --until <E − 1h + 1 minute, RFC3339>`, where `E`
  is F's notice expiry (4c decision: a snooze must end at least 1h before its notice expires).
- Expected error:
  `snooze would end after its notice expires: message <F>'s notice expires at <E>; snooze until <E − 1h> at the latest`.
- Unchanged: `F`'s snooze state and its told record.
