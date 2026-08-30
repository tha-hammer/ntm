---
date: 2026-08-13T11:53:52-04:00
researcher: Claude Sonnet 5
git_commit: 1fd5e5768e3e88ffba3e04c4e51df9c0723962c0
branch: main
repository: ntm
topic: "Spawn-Admission Memory Estimation: Tactical Fix + Adaptive Follow-Up Plan"
tags: [implementation, strategy, spawn-admission, memory, pressure, config, cli, robot, plan-review]
status: complete
last_updated: 2026-08-13
last_updated_by: Claude Sonnet 5
type: implementation_strategy
---

# Handoff: general — Memory-admission tactical fix (shipped, uncommitted) + adaptive follow-up plan (rewritten after user rejected an over-scoped review)

## Task(s)

1. **Diagnose why `ntm spawn` refused 5 agents with 29GB free.** Root cause: `internal/pressure`'s spawn-admission check sized aggregate demand as `requestedAgents * PerAgentMemLimitMB()`, reusing the systemd `MemoryMax` *enforcement ceiling* (16GB on any host ≥64GB RAM) as if it were a realistic per-agent estimate. **Status: complete, root-caused, fixed.**
2. **Ship a tactical fix.** Added `config.PerAgentExpectedMemMB(cfg)` (static 2048MB default, operator-overridable), wired into both `internal/cli/spawn.go` and `internal/robot/spawn.go` admission call sites, leaving the enforcement ceiling untouched. **Status: complete, tested, verified live against real host memory. Uncommitted** — sitting as working-tree changes per this repo's conservative commit policy.
3. **Plan an adaptive follow-up** (learn real per-agent-type memory usage instead of a flat guess). First draft plan + review round produced a plan that ballooned into a `syscall.Exec` runner/finalizer subprocess architecture touching every agent's launch path. **The user rejected this as disproportionate.** I then wrote a narrower replacement plan that keeps the review's three legitimate findings but fixes them by extending machinery ntm already runs. **Status: plan written, reviewed by the user in conversation (not re-run through automated plan-review), not yet implemented.**

## Critical References

- `thoughts/searchable/shared/plans/2026-08-13-11-42-tdd-live-polled-per-agent-memory-estimation.md` — **the plan to implement next.** Self-contained; read it in full before starting.
- `thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation.md` + its `-REVIEW.md` — **rejected direction, kept on disk for reference only per explicit user instruction.** Do not resume implementation from this one. Its "Relationship to the Prior Plan" section in the new plan explains exactly which findings were kept vs. rejected and why.
- `bd-n1iva` (open) — tracks the plan to implement. Supersedes `bd-wsgpf` (closed).

## Recent changes

Tactical fix (uncommitted, `git status` shows these 8 files modified):

- `internal/config/templates.go` — new `defaultPerAgentExpectedMemMB = 2048` const and `PerAgentExpectedMemMB(cfg *Config) uint64`; `PerAgentMemLimitMB()`'s doc comment corrected to say it's the enforcement ceiling, not an admission estimate.
- `internal/config/pacing.go` — `HeadroomPacingConfig.PerAgentExpectedMemMB int` field (0 = use default) + validation (must be non-negative).
- `internal/config/config.go` — `Print`/`GetValue` enumeration for the new field (Diff is automatic, it compares the whole struct).
- `internal/config/pacing_test.go`, `internal/config/templates_test.go` — new tests, including `TestPerAgentExpectedMemMB_FarBelowLimit` which guards against ever regressing back to using the enforcement ceiling for admission math.
- `internal/cli/spawn.go:~1699`, `internal/robot/spawn.go:~144` — admission math changed from `totalAgents * config.PerAgentMemLimitMB()` to `totalAgents * config.PerAgentExpectedMemMB(cfg)`.
- `internal/pressure/pressure.go` — doc comments on `SpawnAdmissionInput.RequestedMemoryMB` and the admission-refusal branch corrected to reference the new estimate, not the old ceiling.

Verified live on this host: `--robot-spawn=X --spawn-cc=5 --dry-run` now computes `requested_memory_mb: 10240` against `available_memory_mb: 26265` → **admit** (previously computed 81920 → refuse). `go build ./...`, `go vet`, `gofmt -l`, and targeted `go test -race` on the touched packages all pass.

Plan documents written this session:

- `thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation.md` — my first draft (small: static ring-buffer + read at confirmed-death). Got run through an automated plan-review (tracked as `bd-qlut2`) that found the design *actually broken* (see Learnings), then — instead of just reporting findings — **rewrote the plan document in place** into a 986-line, 18-behavior, new-package design. The user's reaction: "That's not a review that's a fucking new plan."
- `thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation-REVIEW.md` — the review report itself. Its **findings** are sound (see Learnings); its **prescribed fix** (runner/finalizer subprocess pair) is what got rejected.
- `thoughts/searchable/shared/plans/2026-08-13-11-42-tdd-live-polled-per-agent-memory-estimation.md` — **the accepted replacement**, written by me after the rejection, as a *new* file (old ones untouched, per explicit instruction). Ports the review's three real findings into fixes that extend `internal/cli/monitor.go`'s existing poll loop instead of adding a new process-launch layer. See its own "Relationship to the Prior Plan" section for the itemized keep/reject list.

Beads: created `bd-wsgpf` (tactical-fix follow-up tracking, first draft), then `bd-n1iva` (lean replacement) and ran `bd supersede bd-wsgpf --with=bd-n1iva`. `bd dolt push`/`bd dolt pull` both run clean as of this handoff.

## Learnings

- **The core conflation to never reintroduce:** `PerAgentMemLimitMB()` (systemd `MemoryMax`, a hard *ceiling* meant to catch one runaway process) and a realistic *expected* per-agent footprint are different numbers with different purposes. The original bug was using the ceiling for both. Any future work here must keep them structurally separate (the new estimator's `ceiling` parameter should always trace back to `PerAgentMemLimitMB()`, never the other way around).
- **`tmux.Pane.PID` is the pane's login shell, not the agent.** Any code that wants to read an agent's own resource usage must not read the pane-shell PID's cgroup/stats directly.
- **`systemd-run --user --scope <cmd>` does not fork** — it registers the invoking process into a new transient scope and then execs `<cmd>` in place (no new PID). So the pane shell's *direct child*, once running, generally *is* the agent process, inside a cgroup distinct from the shell's own. This is what makes the new plan's "walk one level, compare cgroup path to the shell's own" approach sufficient, without needing full scope-discovery/ambiguity-resolution machinery.
- **A systemd transient scope is deleted the instant it's empty.** Reading `memory.peak` only after "confirmed session death" (~25s of polling via `internal/cli/monitor.go`'s `monitorDefaultMaxMisses=5` × `monitorDefaultPollInterval=5s`) is too late — the scope is very likely already gone. Any cgroup-based evidence capture needs to poll *while the agent is alive*, not reconstruct after the fact.
- **`internal/history/storage.go` + `internal/history/lock_unix.go:13-37`** is this codebase's proven process-safe (`flock`, `LOCK_EX`) store pattern — the right one to imitate for a new multi-writer store. **`internal/state/timeline_persist.go` is only instance-locked** and is explicitly *not* sufficient precedent when there can be genuinely concurrent OS-process writers (e.g. two `internal-monitor` processes for two active sessions).
- **`internal/cli/monitor.go`'s `monitorLoopDependencies` and `confirmedDeathDeps` are dependency-injected structs specifically built to have new best-effort side effects added safely** (this is literally why the orphan-sweep feature's own TDD plan was implementable in small behaviors). New work should add fields to these, not restructure the loop.
- **`internal/robot/spawn.go`'s `GetSpawn` never calls `resilience.SaveManifest` or launches `internal-monitor`** — confirmed via grep, zero matches. Any evidence-gathering design that assumes a background monitor exists needs an explicit opt-in fix for the robot path (the new plan makes this a `RobotMonitorEnabled` flag, default `false`, reusing the CLI's exact same two calls rather than building separate robot-side machinery).
- **Tooling reality check:** `rch`, `golangci-lint`, `goimports`, and `ubs` are **not installed in this environment** (confirmed via the prior orphan-sweep handoff and by this session's own attempts). Fall back to `go build`/`go vet`/`go test`/`gofmt` directly — this is the documented, accepted fallback in this repo, not a shortcut.
- **Automated "plan review" in this repo's tooling can silently rewrite the reviewed document itself**, not just produce a separate report — worth flagging to the user explicitly when that happens rather than treating the rewritten version as the new ground truth by default.
- Unrelated to this task but discovered en route: earlier in the session, two orphaned `cass timeline` processes (~12GB combined RSS) were found and killed — root-caused to a *different*, already-dead Claude Code agent session, combined with two bugs in ntm's own then-in-development orphan-reap sweep feature (`thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md`) that landed and got fixed in commits `57577191`/`fdaec91b` that same morning — both bugs are already fixed and committed on `main` (current HEAD `1fd5e576`). No further action needed there; purely contextual/already resolved.

## Artifacts

- `thoughts/searchable/shared/plans/2026-08-13-11-42-tdd-live-polled-per-agent-memory-estimation.md` — **read this first**, it's the implementation target.
- `thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation.md` — rejected, reference only.
- `thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation-REVIEW.md` — reference only; its findings are cross-referenced by name in the new plan's "Relationship to the Prior Plan" section.
- Modified (uncommitted) tactical-fix files: `internal/config/templates.go`, `internal/config/pacing.go`, `internal/config/config.go`, `internal/config/pacing_test.go`, `internal/config/templates_test.go`, `internal/cli/spawn.go`, `internal/robot/spawn.go`, `internal/pressure/pressure.go`.
- Beads: `bd-n1iva` (open, ready), `bd-wsgpf` (closed, superseded by `bd-n1iva`), `bd-qlut2` (the automated review task referenced by the rejected plan).

## Action Items & Next Steps

1. **Ask the user whether to commit the tactical fix now.** It's complete, tested, and verified live, but sitting uncommitted per this repo's conservative-commit default — don't assume authority to commit without asking.
2. **When ready to implement the adaptive estimator:** `bd update bd-n1iva --claim`, then run `/implement_plan` against `thoughts/searchable/shared/plans/2026-08-13-11-42-tdd-live-polled-per-agent-memory-estimation.md`. It has 9 Red-Green-Refactor behaviors; only Behavior 9 needs real systemd/cgroup v2 infrastructure (environment-gated, matching `TestMemLimitPrefix_MemoryHighBelowMemoryMax`'s existing gating pattern). The plan's own "Ordered TDD Implementation Sequence"-equivalent is just its behavior numbering — implement 1→9 in order, running the narrow package tests after each.
3. **Do not resume from the rejected plan** (`2026-08-13-08-36-...`) even though it's more detailed/longer — that detail is exactly what got rejected (new package, `syscall.Exec` runner+finalizer subprocess pair inserted into every agent's launch, HMAC host ID, 9 real-tmux/systemd/binary closure tests). If in doubt, re-read the new plan's "Relationship to the Prior Plan" section, which explains the keep/reject reasoning point by point.
4. No code from the adaptive plan (lean or rejected) has been written yet — Behaviors 1-9 are all still TODO.

## Other Notes

- This session is a direct interactive Claude Code CLI session (the user typing directly), **not** running inside an ntm-spawned tmux pane — Agent Mail / NTM orchestration boilerplate is not applicable, same convention as the prior orphan-sweep handoffs in this same directory.
- `bd list --status=in_progress` currently shows 9 issues (`bd-ovgsh`, `ntm-9mt8`, `ntm-ho9e`, `ntm-j8t0`, `ntm-jv8p`, `ntm-qhcz`, `bd-lbur8`, `ntm-dwuv`, `ntm-m5y3`) that appear to belong to other concurrent work/agents in this repo — none were touched this session, listed here only so the next agent doesn't mistake them for part of this handoff's scope.
- `.beads` directory permissions warning (`0775`, recommended `0700`) appears on every `bd` command in this repo — pre-existing, not introduced this session, not addressed (out of scope).
