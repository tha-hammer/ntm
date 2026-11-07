# Attention feed message bus: master index

## Overview

Claude and Codex agents spawned by ntm will learn about new Agent Mail messages when a turn ends or while they sit idle, with no dashboard or `ntm serve` running. To support that, the attention feed becomes durable and can address an event to one agent, so any ntm process can read what another wrote.

- Full scope: [SCOPE.md](SCOPE.md)
- Loop plan and its decisions: [LOOP_PLAN.md](LOOP_PLAN.md)

## Sprint rule

Each sprint ends with 1 human-testable function and only 1. A human-testable function is one capability a person can try at its real entry point and see work.

Human tests run on this VPS, except Loop 8, Sprint 3, which runs on your local machine. Each Evidence Needed check from the scope runs at the start of the sprint that needs it. Each loop gets one epic in beads, with one bead per sprint, created at the TDD step.

## Loops

| # | Document | Loop | Sprints | Status |
|---|---|---|---|---|
| 1 | [01-durable-feed.md](01-durable-feed.md) | Durable feed | 3 | Done: Sprints 1, 2 and 3 passed |
| 2 | [02-addressed-mail-notices.md](02-addressed-mail-notices.md) | Addressed mail notices | 2 | Done: Sprints 1 and 2 passed |
| 3 | [03-claude-turn-end-push.md](03-claude-turn-end-push.md) | Claude turn-end push | 7 | In progress: Sprint 1 passed |
| 4 | [04-cross-process-delivery.md](04-cross-process-delivery.md) | Cross-process delivery | 1 | Not started |
| 5 | [05-idle-nudge.md](05-idle-nudge.md) | Idle nudge | 5 | Not started |
| 6 | [06-agent-snooze.md](06-agent-snooze.md) | Agent snooze | 2 | Not started |
| 7 | [07-codex-turn-end-push.md](07-codex-turn-end-push.md) | Codex turn-end push | 2 | Not started |
| 8 | [08-hook-migration.md](08-hook-migration.md) | Hook migration | 3 | Not started |
| 9 | [09-release-journey.md](09-release-journey.md) | Release journey | 1 | Not started |

Total: 9 loops, 26 sprints.

## Loop summaries

**Loop 1: Durable feed.** Every ntm process, including the dashboard, uses the SQLite store the first time it uses the feed. A failed store write no longer leaves a process in memory for the rest of its life, and events are kept for 7 days. Covers deliverables 1, 2 and 5.

**Loop 2: Addressed mail notices.** A mail event names its recipient, so one agent's events can be listed with `--events-recipient`. A dashboard restart no longer writes all unread mail again. Covers deliverables 3 and 6.

**Loop 3: Claude turn-end push.** The new `ntm agent-hook stop` command tells an agent about new mail when its turn ends, once per message. `ntm spawn` (including `--worktrees`), robot spawn and `ntm add` write the hook into the work folder, and a config switch turns it off. This loop reaches the scope's First Useful Result. Covers deliverables 4, 8, 9, 11, 15 and the hook half of 16.

**Loop 4: Cross-process delivery.** Long-running processes such as `ntm serve` pick up events that other processes wrote and hand them to their subscribers once. Covers deliverable 7.

**Loop 5: Idle nudge.** `internal-monitor` types one batched reminder into an idle agent's pane, using ntm's existing idle detection and send functions. It holds back when a person is typing, and a config switch turns it off. Covers deliverables 12, 13 and the nudge half of 16.

**Loop 6: Agent snooze.** An agent can defer a message to a chosen time with `ntm mail snooze`. Reminders skip it until then, and the nudge brings it back afterward. Covers deliverable 14.

**Loop 7: Codex turn-end push.** Spawn writes a Codex turn-end hook and tells you when a project's hook still needs your approval. An approved Codex agent learns about mail when its turn ends, as far as Codex's hooks allow. Covers deliverable 10.

**Loop 8: Hook migration.** `ntm agent-hook migrate` previews and then applies the move from the old hook scripts to the Go command, with a guide for your local machine. Covers deliverable 17.

**Loop 9: Release journey.** Runs the scope's Connected Release Journey by hand, finishes the end-to-end tests, and updates `docs/attention-feed-contract.md`. Covers the end-to-end and doc parts of deliverable 18. The rest of 18, the tests for each item, is done in the sprint that builds that item.

## Order

| Loop | Needs | Why |
|---|---|---|
| 1 Durable feed | Nothing | Everything else relies on durable events |
| 2 Addressed mail notices | Loop 1 | Addressed events are stored in the durable feed |
| 3 Claude turn-end push | Loop 2 | The hook records addressed notices and told records |
| 4 Cross-process delivery | Loop 1 | It reads the store that other processes write to |
| 5 Idle nudge | Loop 3 | It shares the told record and reminder text with the hook |
| 6 Agent snooze | Loop 5 | The nudge is what brings a snoozed message back |
| 7 Codex turn-end push | Loop 3 | It reuses the hook command and the hook file writer |
| 8 Hook migration | Loop 3 | It points old registrations at the finished hook command |
| 9 Release journey | All earlier loops | It tests the whole path |

Build order is the loop number order.
