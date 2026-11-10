# Loop 10 — behavior list (step 4b)

Loop document: [07-reminder-age-cutoff.md](07-reminder-age-cutoff.md). Repository: `/home/maceo/Dev/ntm`.
Test framework: Go `testing`, with `pgregory.net/rapid` (already in go.mod) for property tests, and the
existing closure harness in `internal/cli` (`newClosureEnv`, `newFakeAgentMail`, `env.startMonitor`
with `idle_nudge_interval_seconds = 1`, and the `screenAgents` stand-in).

## Finding from research: a snooze ending at its notice's expiry never returns

`SnoozedMessages` (`internal/state/runtime_store.go:2161`) only reads notices with
`expires_at >= now`. `SnoozeAddressedMessage` allows `until == expires_at`. So a snooze set to its limit
ends at the same instant its notice expires, and no poll ever sees it as "ended". `GCExpiredEvents`
then deletes the notice.

Today the message still comes back, but only by luck. Its first told record, written about 7 days
earlier, has expired, so it returns as plain mail. With the age cutoff, that plain path is gone,
so the message would be lost silently. Sprint 2 has to close this, and Behavior 11 is the
proposed fix.

## Order of work

B1 → B2 → B3 → B4 → B5 → B6 → B7 → B8 (Sprint 1), then B9 → B10 → B11 → B12 (Sprint 2).

## Sprint 1 — Old mail stays quiet

| # | Behavior | Given / When / Then | Test type | Class |
|---|---|---|---|---|
| B1 | Age check | Given `CreatedTS` and now, when `tooOldToAnnounce(created, now)` runs, then it's true only for a non-zero `CreatedTS` with `now - CreatedTS > ToldRecordRetention`. It's false at exactly 7d, for zero, and for future times. | unit + rapid property | LEAF: a pure function in one package, with no async or registration |
| B2 | Cutoff in `mailToAnnounce` | Given plain items aged 8d, 6d, zero and +1h, when `mailToAnnounce` runs, then the 8d item is gone and the others stay, in order. The existing property test extends this: no returned plain item is too old. | unit + rapid property | LEAF: same-module pure step |
| B3 | Closure (a), hook | Given fake Agent Mail with #900 (created 8d ago) and its told record aged 7d and 1m, when `ntm agent-hook stop` runs, then the output doesn't name #900, the told record is unchanged, and `--robot-events` shows no new notice for #900 | closure | BLOCKING: crosses hook → store → feed |
| B4 | Closure (a), nudge | Given the same #900 for an idle stand-in agent, when the monitor runs 5 or more polls, then nothing is typed into the pane | closure | BLOCKING: async poll edge (monitor) |
| B5 | Closure (c), young message | Given #901 (created 6d ago) with no told record, when the hook runs, then the reminder names `[#901]` once, the told record says stop_hook, and a second run prints nothing | closure | BLOCKING: hook → store |
| B6 | Failure check | Given #902 with `created_ts` 1h in the future, when the hook runs, then the reminder names it, the told record says stop_hook, and the hook log has no warning. Other told records and notices are unchanged. | closure | BLOCKING: the sprint's failure check at the real entry point |
| B7 | Pinned `created_ts` parse | Given `testdata/agentmail_fetch_inbox_live.json` (one raw `fetch_inbox` response captured on rocketman, with the token redacted), when it's decoded into `[]InboxMessage`, then `CreatedTS` equals the expected UTC instant, and the test records the raw text and zone | unit (fixture) | LEAF: decoding only |
| B8 | Doc updates | Given the old expired-claim test and the attention-feed SCOPE note, when this sprint lands, then the test has a comment pointing to the cutoff, and the note says "a message older than 7 days is never announced again" | doc check (grep in the verification commands) | LEAF: documentation, with no behavior |

Sprint 1 coverage: testable function → B3, B5. Failure check → B6. Criteria: cutoff → B1, B2, B4;
no told record or notice → B3; zero and future times → B1, B2, B6; pinned parse → B7; doc updates → B8.

## Sprint 2 — A late snooze still returns

| # | Behavior | Given / When / Then | Test type | Class |
|---|---|---|---|---|
| B9 | Exemption in `mailToAnnounce` | Given an ended snooze for #910 that is also in the fetch with `CreatedTS` 8d ago, when `mailToAnnounce` runs, then #910 is returned once, marked ended. The property test extends this: an ended item is always kept, whatever its age. | unit + rapid property | LEAF: same-module pure step |
| B10 | Ended snooze outside the fetch | Given an ended snooze for #911 that isn't among the 50 fetched, and is old, when the hook runs, then the reminder names `[#911]` with the saved sender and subject, once | closure | BLOCKING: store → hook |
| B11 | Snooze limit leaves a return margin (fix for the finding) | Given a notice expiring at E, when `ntm mail snooze` asks for `until > E - snoozeReturnMargin` (proposed: 1h, one named constant), then it's refused with `ErrSnoozePastNotice`. A snooze until `E - margin` is accepted. | unit (state) + closure through `ntm mail snooze` | BLOCKING: changes the refusal at the real entry point |
| B12 | Closure (b), late snooze returns once | Given #912 (created 7d and 10s ago), its notice expiring now + 1h + 30s, and a snooze until the latest allowed time, when the snooze ends and the monitor polls, then exactly 1 reminder names `[#912]`. Over 5 more polls and 1 turn end there's no repeat, and the told record says idle_nudge. | closure | BLOCKING: async poll edge plus store claims |

Sprint 2 coverage: testable function → B12. Failure check (a snooze past the limit is refused,
and nothing changes) → B11. Criteria: exemption → B9; outside the 50 → B10; told record and snooze
cleared → B12; Sprint 1 still green → the full suite in the verification commands.

The Sprint 2 failure-check text in the loop document needs one update if B11 is accepted. The limit
becomes "E minus the margin", so the expected error reads
`snooze would end after its notice expires: message <F>'s notice expires at <E>; snooze until <E - 1h> at the latest`.

## 4c answers (2026-11-10)

- Test setup: the existing Go setup. That's `go test`, rapid property tests, and the closure harness
  in `internal/cli`, with no new dependencies.
- Snooze fix: a 1-hour margin. A snooze must end at least `snoozeReturnMargin` (1h) before its
  notice expires (B11).
- Behavior list: approved as written.
