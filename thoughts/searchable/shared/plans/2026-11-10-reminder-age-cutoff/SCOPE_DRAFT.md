# Scope draft: reminder age cutoff

## 1. Goal

ntm reminds an agent about new mail in two ways: the Stop hook at the end of a turn, and the idle nudge.
Both use the same step, `mailToAnnounce` (`internal/cli/idle_nudge.go:90`). A message is reminded once,
and a told record stops it repeating. But told records last only 7 days (`ToldRecordRetention`,
`internal/state/runtime_store.go:2437`). After that, `ClaimToldRecord` lets the message be claimed again
(the test `TestClaimToldRecord_ExpiredClaimWinsAgain` asserts this). Agent Mail never reports a read time,
so `unreadMessages` treats every message as unread. The hook fetches the 50 newest messages
(`stopHookFetchLimit`). So on a quiet agent, a message it read long ago is announced again about
every 7 days, with no end.

Who has it: any agent with fewer than 50 messages over a week or more. Planning agents and quiet
reviewers are the usual cases.

Solved when:
- A plain message created more than 7 days ago is never announced by the hook or the nudge. Shown by
  a closure test that ages a told record past 7 days and finds 0 reminders over 5 polls and 1 turn end.
- A message returning from an ended snooze is still announced exactly once, even when it is just past
  7 days old. Shown by a closure test with a snooze set to the notice's expiry, giving 1 reminder.
- How a `created_ts` with no time zone is read is fixed by a test.

## 2. Deliverables

1. An age cutoff in `mailToAnnounce`. A plain inbox message (`item.ended == nil`) whose `created_ts` is
   more than `ToldRecordRetention` before now is left out. The hook and the nudge both get this,
   because they share the step. The hook also stops recording such messages in the attention feed.
2. An exemption: an ended-snooze item is kept whatever its age.
3. A message with no or zero `created_ts` is announced (fail open).
4. A test that pins `FlexTime` parsing of `created_ts`, using one real Agent Mail inbox response
   captured on rocketman. Today a zone-less time is read as UTC (`internal/agentmail/types.go:33-51`).
5. Closure tests at the real entry points (`ntm agent-hook stop` and the monitor's nudge poll):
   (a) an old, already-told message whose told record has expired gives no reminder;
   (b) a snooze until its notice's expiry returns exactly once, then never again;
   (c) a message 6 days old whose told record was lost is still announced (the cutoff doesn't
   reach too far).
6. A failure check: a `created_ts` in the future is treated as new, not old, with no error.
7. Update the old expired-claim test and the scope note in the attention-feed plan, which currently
   accepts "announced once more after 7 days".

## 3. Out of scope

- Real read-state tracking (Agent Mail would need to report `read_at`).
- Changing the 7-day told retention or the 50-message fetch window.
- The repeated-send-failure retry and a per-message snooze limit. Those are separate follow-ups.
- `ntm mail inbox` display and `ntm mail snooze` validation. Neither changes.

## 4. Open questions

1. What should the cutoff age be?
   a. Exactly `ToldRecordRetention`, 7 days, reusing the one constant (suggested)
   b. Retention plus a margin, e.g. 7 days and 1 hour, for clock skew
   c. A separate config setting
   **Answer:** a. Exactly `ToldRecordRetention` (7 days), the same constant.
2. What happens to a message with no `created_ts`, or a zero one?
   a. Announce it (fail open, as the rest of the reminder code does) (suggested)
   b. Skip it
   **Answer:** a. Announce it (fail open).
3. How should the zone-less `created_ts` question be settled?
   a. Capture one real Agent Mail inbox response on rocketman and pin the test to it (suggested)
   b. Keep today's "assume UTC" and only add a test for it
   **Answer:** a. Capture one real Agent Mail inbox response on rocketman and pin the test to it.
4. Should the age cutoff also apply where the hook records mail notices in the attention feed?
   a. Yes: skip recording old messages too, so the feed doesn't fill with stale notices (suggested)
   b. No: change only what is announced
   **Answer:** a. Yes, skip recording old messages in the attention feed too.
5. How should this be planned?
   a. As one small loop with one or two sprints, added to the existing attention-feed plan's numbering
      (suggested)
   b. As a standalone project in this new folder
   **Answer:** a. One small loop (one or two sprints), numbered after the attention-feed plan's loops (Loop 7).

**Deliverables:** kept as listed. **Next:** write the scope.
