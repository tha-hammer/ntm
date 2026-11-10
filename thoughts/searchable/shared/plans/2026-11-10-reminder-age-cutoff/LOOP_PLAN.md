# Loop plan: reminder age cutoff

Scope: `SCOPE.md` (this folder). The deliverable numbers refer to SCOPE.md section 6, "Required Deliverables".
Numbering continues the attention-feed plan (`2026-11-06-attention-feed-message-bus`), which has Loops 1 to 9 (its Loop 7 is the Codex turn-end push). This plan is Loop 10; it was written as Loop 7 and renumbered when merged (2026-11-10).

## 1. Loops

| # | Loop | SCOPE.md deliverables | Sprints |
|---|---|---|---|
| 7 | Reminder age cutoff | 1, 2, 3, 4, 5, 6 | 2 |

There is one loop. Sprint 2 depends on Sprint 1's cutoff, because the exemption only matters once
the cutoff exists.

## 2. Sprints

### Loop 10 — Reminder age cutoff

**Sprint 1 — Old mail stays quiet.** An agent with a message more than 7 days old, whose told record
has expired, gets no reminder for it at turn end or from the idle nudge. A message 6 days old with
no told record is still announced.
- Covers:
  - the age cutoff in `mailToAnnounce` (deliverable 1);
  - the Unknown Age and Future Time rules (2);
  - the `FlexTime` test pinned to a captured real `fetch_inbox` response (3);
  - closure tests (a) and (c) (4);
  - the comment on `TestClaimToldRecord_ExpiredClaimWinsAgain` and the attention-feed scope note (6).
- Failure check: a message whose `created_ts` is 1 hour in the future is announced. There's no
  error, and its told record is written as usual (5).
- Human test: on rocketman, run the Stop hook as an agent that has a message more than 7 days old in
  Agent Mail, after deleting that message's told record. The reminder doesn't name it.

**Sprint 2 — A late snooze still returns.** A message just over 7 days old, whose snooze ends at its
notice's expiry, comes back exactly once, and then never again.
- Covers: the Snooze Exemption in `mailToAnnounce` (deliverable 1, exemption part), and closure test (b) (4).
- Failure check: snoozing past the notice's expiry is still refused with the existing
  `ErrSnoozePastNotice` error, and nothing is saved.
- Human test: on rocketman, set up a snoozed message whose message time is 7 days and 1 minute old,
  with a snooze ending now. The next nudge poll announces it once, and the following polls don't.

## 3. Open questions

1. How should the human test get a message older than 7 days?
   a. Use a real old message already in Agent Mail on rocketman (there are messages from earlier
      test sessions), and delete its told record by hand (suggested)
   b. Add a hidden test-only flag to make the cutoff shorter (for example `NTM_TOLD_RETENTION=2m`)
   c. Write a fake old message straight into Agent Mail's database
   **Answer:** a. Use a real old message already in Agent Mail on rocketman, and delete its told record by hand.
2. How should the Sprint 2 human test get a snooze just past 7 days?
   a. Edit the state database directly (the notice and snooze times), with steps given in the test (suggested)
   b. Use the same test-only retention flag as 1b
   c. Closure test only, with no human test for Sprint 2
   **Answer:** a. Edit the state database directly, with exact sqlite steps in the test.
3. Should the two sprints be merged into one?
   a. No: keep 2, so the exemption gets its own human test (suggested)
   b. Yes: one sprint with both closure tests and one human test
   **Answer:** a. Keep 2 sprints.

**Loops:** kept as listed. **Next:** write the documents.
