# Reminder age cutoff — master index

## Overview

ntm's Stop hook and idle nudge remind an agent about new mail once, and a told record stops repeats.
But told records last 7 days, and Agent Mail never reports read state. So a message the agent read long
ago, still among its 50 newest, is announced again about every 7 days.

This project adds an age cutoff to the shared step `mailToAnnounce`. A plain message more than 7 days
old is never announced. A message returning from an ended snooze is always kept, whatever its age.
Messages with a missing or future time are announced.

Full scope: [SCOPE.md](SCOPE.md). Loop plan: [LOOP_PLAN.md](LOOP_PLAN.md).
Numbering continues the attention-feed plan (`../2026-11-06-attention-feed-message-bus`), which has Loops 1 to 9 (its Loop 7 is the Codex turn-end push). This plan is Loop 10; it was written as Loop 7 and renumbered when merged (2026-11-10).

## Sprint rule

Each sprint ends with 1 human-testable function, and only 1. It is used at its real entry point
(`ntm agent-hook stop` or the session monitor's nudge poll). Each sprint also has exactly one failure check.

## Loops

| # | Document | Loop | Sprints | Status |
|---|---|---|---|---|
| 7 | [07-reminder-age-cutoff.md](07-reminder-age-cutoff.md) | Reminder age cutoff | 2 | Not started |

## Loop summaries

**Loop 10 — Reminder age cutoff.**
- Sprint 1, "Old mail stays quiet": the cutoff, the rules for missing and future times, the pinned
  `created_ts` parsing, and closure tests (a) and (c).
- Sprint 2, "A late snooze still returns": the snooze exemption, and closure test (b), a snooze that
  ends at its notice's expiry and returns once.

## Order

Loop 10 needs only what Loops 3 to 6 of the attention-feed plan already built: told records (Loop 3),
the idle nudge (Loop 5), and snooze and return (Loop 6). Inside the loop, Sprint 2 needs Sprint 1's
cutoff, because the exemption only matters once the cutoff exists.
