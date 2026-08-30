---
date: 2026-08-11T20:37:48-04:00
researcher: Claude Sonnet 5
git_commit: f850ca6ccf56eb686c55d12a8371b2b30a8c39e3
branch: main
repository: ntm
topic: "Periodic Orphan-Process Sweep Implementation Strategy"
tags: [implementation, strategy, orphan-processes, internal-monitor, tmux, resilience, process-identity]
status: complete
last_updated: 2026-08-11
last_updated_by: Claude Sonnet 5
type: implementation_strategy
---

# Handoff: Periodic orphan-process sweep — TDD plan reviewed and corrected, no implementation yet

> **Note**: `/home/maceo/Dev` (the filesystem backing this checkout) hit 100%
> capacity partway through writing this handoff, blocking `mkdir`/`bd` outright.
> Space has since been freed (79G available as of this file's move into place)
> and this handoff now lives at its intended path. See **Other Notes** for the
> historical detail, kept for the record.

## Task(s)

Investigating and fixing a real production incident (kernel OOM-kill on 2026-08-10, root-caused this session) led to a chain of ntm spawn/headroom/naming fixes, then to a request to research and plan a **periodic sweep for orphaned agent processes** — an agent process that survives its tmux session dying by something other than `ntm kill` (crash, OOM-kill, manual `tmux kill-session`).

Status of each piece:
1. **Two prior fixes: implemented, committed, pushed.** Not part of the orphan-sweep work but same session/thread — see "Artifacts" below for both.
2. **Research** (`/research_codebase "to wire up a periodic sweep"`): **complete**. Answered where the sweep should live and why.
3. **TDD plan** (`/create_tdd_plan`): **complete, then reviewed and majority-revised**. My first draft was reviewed by a separate review pass and found to have real, fundamental design flaws. The plan file now contains a **corrected v2 design that supersedes my v1 in full** — this is the current, authoritative plan. **No implementation has started.** Per beads (`bd-1dtgg` blocking `bd-cibu4`, discovered while writing this handoff — see Action Items), a **follow-up plan review confirming v2 addresses the original findings is the next step, before the plan's "Implementation Order" section begins.**
4. **Handoff** (`/create_handoff`, this document): in progress — blocked partway through only by the disk-space issue above, not by anything about the actual work.

## Critical References

- **The TDD plan (authoritative, v2/corrected)**: `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md` (636 lines) — **read this file in full before writing any code.** It supersedes everything I originally designed; do not implement my original design from memory of this conversation.
- **The review that drove the correction**: `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep-REVIEW.md` (253 lines) — explains *why* each part of v1 was wrong; useful for understanding the reasoning behind v2's contracts, not just the contracts themselves.
- **The research doc** (still valid, not superseded): `thoughts/searchable/shared/research/2026-08-11-16-42-wire-up-periodic-orphan-sweep.md` — documents *where* the sweep should live (the per-session `internal-monitor` daemon) and why, with verified `file:line` citations across the whole existing orphan/monitor/config surface.

## Recent changes

No code changes for the orphan-sweep feature exist yet — only planning documents. The only code changes made this session (separate, already-shipped work, not part of the sweep):
- `internal/pressure/provider.go`, `internal/pressure/pressure.go`, `internal/robot/spawn.go` — commit `be5f182f` (`fix(spawn): admit/refuse spawns on aggregate memory, not just per-agent cap`), pushed to `origin/main` (`tha-hammer/ntm`).
- `internal/cli/spawn.go`, `internal/robot/spawn.go` (Agent Mail naming on `--robot-spawn`) — commit `57ec0580`, also pushed.

Since `be5f182f`, exactly one commit landed on `main`: `f850ca6c bd: update sync.remote` (a 2-line `.beads/config.yaml` change, unrelated to this work — confirmed via `git diff --stat be5f182f..HEAD -- . ':!thoughts'`).

## Learnings

**My original TDD plan (v1) had two fatal design errors — do not repeat them:**
1. **Discovering descendants *after* confirmed session death is temporally impossible.** `collectPaneDescendants` (`internal/cli/send.go:2300-2347`) only walks *current* parent/child edges. By the time a session is confirmed dead, survivors have already reparented — there's nothing left to walk from the (now-gone) pane shell PID. The existing `ntm kill` reap path avoids this by snapshotting *before* `tmux.KillSession` (`internal/cli/send.go:2228-2253`). Any periodic-sweep design must snapshot *while the session is still alive*, continuously, and retain the last-good snapshot across the miss window.
2. **`resilience.Monitor` is not a valid PID source in production**, contrary to what I assumed and reported to the user. I saw `RegisterAgent`'s signature and a `ShellPID` field and assumed it got refreshed — I did not verify `ScanAndRegisterAgents`'s actual body closely enough. It turns out: `runMonitor` registers every manifest agent with `ShellPID == 0` (`internal/cli/monitor.go:138-144`), `ScanAndRegisterAgents` *skips already-registered pane IDs* so it never fixes this (`internal/resilience/monitor.go:123-142`), and health checks read a live shell PID into a local variable that is *never persisted back* (`internal/resilience/monitor.go:384-398`). So `GetAgentStates()` — which I planned to source PIDs from — always returns zeros in the real path. **Lesson for future research/planning in this codebase: when a "this field looks like it gets refreshed" claim matters to a design, read the *entire* refresh function body, not just the line that sets the field once.**

**Other real correctness issues the review surfaced, now locked into v2's design** (see the plan's own "Locked Decisions and Contracts" section for the full, precise contracts — this is a summary, not a substitute):
- A bare PID captured long before a delayed signal is unsafe — PIDs get reused. v2 uses `{PID, CreateTimeMillis}` identity (via the already-present `gopsutil/v4` dependency), captured fresh and re-validated *immediately* before both SIGTERM and SIGKILL.
- `tmux.SessionExists` collapses every failure type to `false` — an ambiguous/transient tmux error must never be treated the same as confirmed absence, or a transient hiccup could trigger destructive reaping. v2 uses `GetPanesContext` + `tmux.ClassifyCommandError` to distinguish definite-absence from ambiguous-error.
- The destructive on/off toggle can't just live in global config read fresh inside the detached monitor process — the detached child's argv is only `internal-monitor <session>` (no `--config` propagated, `internal/cli/spawn.go:2961-2969`), so a user's explicit `--config custom.toml` at spawn time wouldn't reach the monitor. v2 freezes the *effective* policy into `SpawnManifest` at spawn time (mirroring how `AutoRestart` is already snapshotted there), and the monitor reads only the manifest field.
- My proposed closure test's "orphan" was a bare Go-test subprocess never actually attached to the tmux pane tree, and used a plain `sleep` that a real tmux SIGHUP would kill on its own anyway (false-green risk). v2's closure test launches a deliberately HUP-surviving process *through* `tmux.SendKeys` into the real pane, captures its exact identity via a pidfile, and requires *both* an enabled case (target reaped) and a disabled control case (target still alive) to rule out coincidental cleanup.

**Beads was briefly unusable mid-session** due to the disk-space issue (see Other Notes) — `bd` commands failed with a disk-space error, not a beads bug. Confirmed working again once space was freed (see Artifacts for what that revealed).

## Artifacts

- `thoughts/searchable/shared/research/2026-08-11-16-42-wire-up-periodic-orphan-sweep.md` — research doc (valid, unchanged)
- `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md` — **the TDD plan, v2/corrected, authoritative** (read this fully before coding)
- `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep-REVIEW.md` — the review that produced v2
- This handoff (`thoughts/searchable/shared/handoffs/general/2026-08-11_20-37-48_periodic-orphan-sweep-tdd-plan-reviewed.md`)
- **Beads** (verified working after disk space was freed): the actual dependency structure is `bd-1dtgg` ("Plan Review: Periodic orphan-process sweep", P1, OPEN) **blocks** `bd-cibu4` ("Wire periodic orphan-process sweep into internal-monitor", P1, OPEN, which I created). `bd-1dtgg`'s acceptance criteria calls for exactly what the v2 plan now contains (identity-safe pre-death snapshots, ordered stop/reap/summary/delete effects, deterministic cancellation/readiness/cleanup, manifest/config ownership, full config-surface tests, real in-pane HUP-survivor closure matrix) *plus* "a follow-up plan review is Ready for Implementation or Needs Minor Revision with no critical process-safety issue." **That follow-up review has not happened yet** — I read the v2 plan myself and it appears to address every point in the original review's traceability table, but I did not run a formal review pass, and I am not closing `bd-1dtgg` or updating `bd-cibu4`'s stale (pre-review) acceptance criteria myself — that's a judgment call for whoever runs the follow-up review, not something to fold into a handoff. `bd-cibu4`'s ACCEPTANCE CRITERIA field is currently stale (still describes the rejected v1 design) and its NOTES field already documents the review outcome and the `bd-1dtgg` block.
- Two unrelated, already-shipped commits from earlier in this session: `be5f182f`, `57ec0580` (see "Recent changes").

## Action Items & Next Steps

1. Disk space is resolved (79G free as of this handoff's move into place) — no longer a blocker.
2. **Read the v2 plan in full** (`thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md`) — it is long and dense by design (every locked decision traces to a specific review finding via its "Review-Finding Traceability" table at the end). Do not skim it and reconstruct from this handoff's summary.
3. Get the follow-up plan review `bd-1dtgg` is waiting on (its acceptance criteria requires "Ready for Implementation or Needs Minor Revision with no critical process-safety issue" before `bd-cibu4` unblocks). Whether that's a fresh `/review_plan` pass or the user's own read-through is a judgment call, not mine to make here.
4. Once `bd-1dtgg` closes and `bd-cibu4` unblocks, update `bd-cibu4`'s ACCEPTANCE CRITERIA field (currently stale, still describes the rejected v1 design) to match the v2 plan's actual behaviors, then follow the plan's own **"Implementation Order"** section exactly — it is deliberately *not* Behavior-1-then-2-then-3 in file order: identity primitives (Behavior 2) and snapshot capture (Behavior 3) land before the config/manifest plumbing (Behavior 1), and the destructive default isn't flipped to `true` until Behaviors 1–5 are green.
5. Run `silmari-oracle sync` to register this handoff.

## Other Notes

- **Resolved, unrelated-to-the-feature disk issue (kept for the record)**: mid-handoff, `/home/maceo/Dev` (`/dev/mapper/vg_work-work_dev`, 393G) hit **100% capacity, 0 bytes available**, confirmed *actively blocking* writes (`mkdir` failed twice with `No space left on device`, `bd` failed on every command with the same root cause). I did not attempt any deletion/cleanup myself (had no inventory of what was safe to remove among 373GB; a `du -sh --max-depth=1` I attempted timed out after 60s without results). The user freed space directly; as of this file's move into place, `df -h` shows 79G available and `bd` is confirmed working again. If disk pressure recurs, `/tmp` (tmpfs) and `/`/`/home` are separate filesystems and were never affected.
- **Agent Mail / NTM session context**: not applicable this session. This is a standalone Claude Code CLI session working *on* the `ntm` project's source code, not a session running *inside* an ntm-spawned tmux pane as part of a multi-agent swarm. No `AGENT_NAME` env var or other evidence of being an ntm-orchestrated agent was observed. Skip Agent Mail identity/message-number reporting and the "you are {ORCHESTRATING, PART OF} an ntm session" boilerplate for this handoff — they don't apply.
- The research and plan docs both cross-reference exact `file:line` citations that were independently verified (not just agent-reported) at the time they were written, against commit `be5f182f`/`f850ca6c` — if significant time passes before implementation, spot-check a few citations against current `HEAD` since surrounding code may have shifted.
- `git_commit` in this handoff's frontmatter (`f850ca6c`) is a **trivial** `.beads/config.yaml`-only commit on top of the two real fixes (`be5f182f`, `57ec0580`) — it is not itself relevant to the orphan-sweep work, just the current `HEAD` at time of writing.
