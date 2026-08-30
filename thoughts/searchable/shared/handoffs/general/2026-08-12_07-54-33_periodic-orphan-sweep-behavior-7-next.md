---
date: 2026-08-12T07:54:33-04:00
researcher: Claude Sonnet 5
git_commit: f850ca6ccf56eb686c55d12a8371b2b30a8c39e3
branch: main
repository: ntm
topic: "Periodic Orphan-Process Sweep Implementation Strategy"
tags: [implementation, strategy, orphan-processes, internal-monitor, tmux, resilience, process-identity, tdd]
status: complete
last_updated: 2026-08-12
last_updated_by: Claude Sonnet 5
type: implementation_strategy
---

# Handoff: Periodic orphan-process sweep — Behaviors 2,3,5,4,6,1 all green; Behavior 7 (real-tmux closure) is the only piece left

## Task(s)

Continuing from the prior handoff (`2026-08-11_20-37-48_periodic-orphan-sweep-tdd-plan-reviewed.md`), this session resumed and implemented the v2 TDD plan (`thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md`) in its own specified Implementation Order. Status:

1. **bd-1dtgg** (follow-up plan review): **closed**. Verdict Ready for Implementation, recorded at session start.
2. **Behavior 2** (process identity) — bd-8iefk: **closed, green**.
3. **Behavior 3** (manifest-scoped live snapshot) — bd-op9au: **closed, green**.
4. **Behavior 5** (identity-safe delayed reaper) — bd-gcqgd: **closed, green**.
5. **Behavior 4** (context-aware monitor loop) — bd-1fyfn: **closed, green**. Biggest single piece; extracted `runMonitor`'s inline loop into `runSessionMonitorLoop`.
6. **Behavior 6** (ordered confirmed-death effects) — bd-uee4b: **closed, green**.
7. **Behavior 1** (config+manifest policy surfaces) — bd-vc07s: **closed, green**.
8. **Behavior 7** (real-tmux HUP-survivor Closure Test) — bd-6ykjr: **NOT STARTED. This is the only remaining work.**
9. **bd-cibu4** (parent tracking issue, "Wire periodic orphan-process sweep into internal-monitor"): still OPEN, blocked only on bd-6ykjr. Closing bd-6ykjr does **not** auto-close it — do that explicitly as the last step.

**Nothing has been committed.** All work across Behaviors 2–1 exists only as uncommitted working-tree changes on top of `f850ca6c` (17 files changed, 1526 insertions, 68 deletions, plus one new file `internal/cli/monitor_test.go`, 894 lines). Per beads policy (conservative default), do not commit/push without explicit user authority — flag this to the user before doing so, or wait until Behavior 7 is also green so the whole feature lands as one coherent state.

## Critical References

- **The TDD plan (still fully authoritative, no changes needed)**: `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md` — Behavior 7's exact spec is in its own section (search for "## Behavior 7"), plus the "Workflow Closure" section's "Closure Test A"/"Closure Test B" and "Hermetic cleanup contract" subsections, which give the literal step-by-step for the real-tmux test.
- **Prior handoff** (context on how this resumed): `thoughts/searchable/shared/handoffs/general/2026-08-11_20-37-48_periodic-orphan-sweep-tdd-plan-reviewed.md`.
- **Existing real-tmux test pattern to imitate**: `internal/cli/send_test.go` `TestSendRealSession` (top of file) and `TestCollectPaneDescendants_RecursiveAndExclusion` (near the bottom, added this session) — shows `testutil.RequireTmuxThrottled(t)`, XDG isolation via `t.Setenv`, and spawning real subtrees via `sh -c "... & wait"`.

## Recent changes

All changes this session (nothing committed):

- `internal/process/liveness.go` / `liveness_test.go` — Behavior 2: `ProcessIdentity{PID, CreateTimeMillis}`, `CaptureProcessIdentity` (fresh gopsutil handle per call, `ErrProcessNotRunning` sentinel, `IsAlive`-gated zombie safety), `GetChildPIDsContext` (context/error-aware, deliberately mirrors the existing `GetChildPIDs`'s main-thread-only `/proc` semantics for consistency with the production kill path — see Learnings).
- `internal/cli/monitor.go` — grew from 353 to ~940 lines across Behaviors 3/4/6/1:
  - Behavior 3: `orphanProcessSnapshot`, `orphanSnapshotDeps`, `productionOrphanSnapshotDeps`, `manifestOwnedPaneRoots`, `captureOrphanProcessSnapshot`.
  - Behavior 4: `monitorDefaultPollInterval`/`OutputSnapshotInterval`/`MaxMisses` constants, `monitorLoopOptions` (+ `validate()`), `monitorLoopDependencies` (incl. test-only `PollTicks`/`OutputTicks` channels), `monitorLoopState`, `copyOrphanProcessSnapshot`, `applyMonitorObservation`, `pollMonitorLoopOnce`, `runSessionMonitorLoop`. `runMonitor`'s old inline `for { select { ticker... } }` loop is gone — it now runs `runSessionMonitorLoop` in a goroutine and selects `{sigChan, loopDone}`.
  - Behavior 6: `confirmedDeathDeps`, `handleConfirmedSessionDeath`, `productionConfirmedDeathDeps`.
  - Behavior 1: `OnConfirmedDeath`'s production closure now reads `manifest.ReapOrphansOnExit` directly (was a hardcoded fail-safe `false` placeholder between Behaviors 6 and 1).
- `internal/cli/monitor_test.go` — **new file**, 894 lines. All Behavior 3/4/6 tests live here (`TestCaptureOrphanProcessSnapshot_*`, `TestProductionOrphanSnapshotDeps_*`, `TestMonitorLoopOptions_*`, `TestRunSessionMonitorLoop_*` ×3, `TestHandleConfirmedSessionDeath_*` ×3, `TestRunMonitor_UsesManifestPolicyAndProductionLoop`).
- `internal/cli/send.go` / `send_test.go` — Behavior 5: `orphanCandidateSkipReason`, `orphanReapDeps`, `productionOrphanReapDeps`, `validateOrphanCandidate`, `orphanReapResult`, `reapIdentifiedOrphanProcesses`, added beside the existing unchanged `collectPaneDescendants`/`reapOrphanProcesses`.
- `internal/config/config.go` — Behavior 1: `ResilienceConfig.ReapOrphansOnExit` (default true) + `Print`/`GetValue`/`Diff` enumeration entries.
- `internal/cli/root.go` — Behavior 1: JSON `config show` gained a `"resilience": {"reap_orphans_on_exit": ...}` object (previously exposed nothing for this section).
- `internal/resilience/manifest.go` — Behavior 1: `SpawnManifest.ReapOrphansOnExit bool` (no `omitempty`).
- `internal/cli/spawn.go` — Behavior 1: new `buildSpawnManifest(opts, cfg, projectDir, agents) *resilience.SpawnManifest` factored out of the inline launch block; `newInternalMonitorCommand`/monitor argv untouched.
- Test files extended to match: `internal/config/config_test.go`, `coverage_boost_test.go`, `diff_test.go`, `project_test.go`; `internal/resilience/manifest_test.go`; `internal/cli/cli_test.go`, `spawn_test.go`.

## Learnings

- **`GetChildPIDs`/`GetChildPIDsContext` only read the *main thread's* `/proc/<pid>/task/<pid>/children`**, not all threads (unlike the older private `getChildPIDs`, which walks every `task/<tid>/children`). This is the function `orphanReapFanout`/`collectPaneDescendants` already use in production, so `GetChildPIDsContext` deliberately matches it for consistency. A test that spawns children directly from the Go test binary can flake because Go's runtime may `ForkExec` on a non-main OS thread — fork through a real shell (`sh -c "... &"`) instead, matching how this is actually used against real pane-shell PIDs. See `internal/process/liveness_test.go`'s `TestGetChildPIDsContext_ReturnsErrorAndHonorsLimit/honors_limit` subtest comment.
- **Implementation Order runs Behavior 6 before Behavior 1**, but Behavior 6's gating logic needs a bool that only becomes real (`manifest.ReapOrphansOnExit`) once Behavior 1 lands. Resolved by having `handleConfirmedSessionDeath` take `enabled bool` as a plain parameter (fully testable in isolation) and having `runMonitor`'s wiring temporarily hardcode `false` (fail-safe) between Behaviors 6 and 1, with a `TODO(bd-vc07s / Behavior 1)` comment — then Behavior 1 replaced that one line with the real field read. This pattern is worth knowing about if you ever see "hardcoded false pending Behavior N" comments elsewhere in a multi-behavior TDD plan.
- **`internal/cli` test `PersistentPreRunE` reloads the package-level `cfg` via `startup.GetConfig()`** for any command that needs config — directly assigning `cfg = config.Default(); cfg.Foo = ...` before `rootCmd.Execute()` gets silently discarded. Tests that need an explicit-false (or any non-default) config value for a `config show`/`config get` roundtrip must write a real TOML file and pass `--config <path>`, plus reset `cfg = nil; cfgFile = ""; startup.ResetConfig()` before and after (see `TestConfigGetUsesProjectMergedConfig` in `cli_test.go` for the original pattern, and `TestConfigShowJSONIncludesResilienceReapOrphansOnExit` for the fix after this bit me once — my first draft passed with a false-green because it silently tested the default `true` value instead of the explicit `false` I intended).
- **Everything this session is uncommitted**, all on top of the same original `HEAD` (`f850ca6c`). This means `git stash push -- <file>` for a "verify Red would have failed" check reverts *all* behaviors' changes to that file, not just the one being verified — fine for proving the test isn't vacuous, but don't expect an isolated single-behavior diff. When stashing `internal/cli/monitor.go` specifically, it reverts *all* of Behaviors 3/4/6/1's changes to that file at once (this surprised me once mid-session; had to redo the Behavior 1 Red-check excluding `monitor.go` and reasoning about it separately).
- **Two pre-existing, unrelated test failures** in `internal/cli`'s full `-short` suite, confirmed to reproduce identically on a genuinely unmodified checkout (verified via full stash/restore in Behavior 3, and reconfirmed after Behavior 1): `TestResolveModel_WithConfig` (stale expected default model string, `"gpt-5.6-sol"` vs test's hardcoded `"gpt-5.5"`) and `TestRespawnWithPaneFilter` (real-tmux agent-relaunch timing flake). Not caused by this work; don't chase them down as part of this plan.
- **`rch` (this repo's remote-build offload wrapper) and `golangci-lint`/`goimports` are not installed in this environment** — `go build`/`go test`/`go vet`/`gofmt` were run directly instead, which is the plan's own documented fallback for missing tools, not a skipped gate.
- Each closed beads issue's `bd show <id>` close-reason contains a detailed, accurate summary of exactly what was implemented and tested for that behavior — worth reading if you need behavior-level detail beyond what's here (all still visible via `bd show bd-8iefk` / `bd-op9au` / `bd-gcqgd` / `bd-1fyfn` / `bd-uee4b` / `bd-vc07s`, even though closed).

## Artifacts

- `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep.md` — the TDD plan (authoritative, unchanged)
- `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep-REVIEW.md` — the v1 review that produced v2 (background only, not needed to implement Behavior 7)
- `thoughts/searchable/shared/research/2026-08-11-16-42-wire-up-periodic-orphan-sweep.md` — original research doc (background only)
- This handoff and the prior one in the same directory
- All code changes listed under "Recent changes" above (uncommitted)
- Beads: bd-8iefk, bd-op9au, bd-gcqgd, bd-1fyfn, bd-uee4b, bd-vc07s (all closed with detailed reasons), bd-6ykjr (open, next), bd-cibu4 (open, parent, blocked only on bd-6ykjr)

## Action Items & Next Steps

1. **Run `bd update bd-6ykjr --claim`**, then implement Behavior 7 per the plan's own "## Behavior 7" section and "Workflow Closure" → "Closure Test A"/"B" subsections in full — read them directly, don't reconstruct from memory. In brief:
   - New test `TestRunSessionMonitorLoop_OrganicDeathHUPSurvivor` in `internal/cli/monitor_test.go`, with `enabled`/`disabled` subtests.
   - Real tmux session + pane owning a manifest; launch a deliberately HUP-surviving process through `tmux.SendKeys` (`trap "" HUP` + a pidfile), capture its exact `{PID, CreateTimeMillis}` via a pidfile-poll.
   - Drive `runSessionMonitorLoop` with **production** dependencies (real `tmux.GetPanesContext`, real `productionOrphanSnapshotDeps()`, real ticker — i.e. do *not* set `PollTicks`/`OutputTicks` this time), wait on `Ready` for the exact target identity, then `tmux.KillSession` directly (never `ntm kill`).
   - Enabled case: assert the identity is gone after the loop joins. Disabled case: assert it's still alive. Both: assert exactly one summary callback and manifest absence before any fallback cleanup.
   - `testutil.RequireTmuxThrottled(t)` + XDG isolation (`t.Setenv("XDG_DATA_HOME", ...)`) — follow `TestCollectPaneDescendants_RecursiveAndExclusion`/`TestSendRealSession`'s conventions already in `send_test.go`.
   - No new production code should be needed — Behaviors 1–6 already built everything Behavior 7 exercises. If you find yourself wanting a new production function reachable only from this test, stop — that violates the plan's own Refactor guidance ("no production function called solely from tests").
2. Verify Red-would-fail the same way prior behaviors did (stash the new test file isn't possible since it's additions to an existing file — instead, temporarily comment out/remove the specific new test function, confirm nothing else breaks, or rely on the fact that the test is new and can't have passed before you wrote it — use judgment).
3. Run the plan's own "Automated Verification Matrix" section for the final full pass, including `go test -race ./internal/cli -run 'RunSessionMonitorLoop|ReapIdentifiedOrphanProcesses' -count=1` and a full `go build ./cmd/ntm` / `go test -short ./...` — expect the same 2 pre-existing unrelated failures and nothing else new.
4. Close bd-6ykjr with a detailed reason (follow the pattern in the other 6 closed issues), then **explicitly close bd-cibu4 too** (it will not auto-close).
5. The plan's "Manual / Detached-Process E2E" section (real `ntm spawn` + kill tmux directly + verify) is explicitly out of scope for the Closure Test and described as a separate manual check — mention it to the user as available but don't attempt to automate it.
6. Ask the user whether/how they want the accumulated uncommitted changes committed (conservative default — do not commit without explicit authority). Given this is one coherent 7-behavior feature, committing as a single commit (or one commit per behavior, replaying the beads close-reasons as commit messages) once Behavior 7 is green is probably what they'll want, but confirm first.

## Other Notes

- Agent Mail / NTM session context: **not applicable**, same as the prior handoff — this is a standalone Claude Code CLI session working on `ntm`'s own source, not a session running inside an ntm-spawned tmux pane. Skip Agent Mail identity/message-number reporting and NTM orchestration boilerplate.
- `bd dolt push`/`bd dolt pull` were run successfully at the top of this handoff-writing step; beads state is synced.
- If significant time passes before Behavior 7 is picked up, the plan's own file:line citations (verified against `be5f182f`/`f850ca6c` when originally written) may have drifted from the *now-modified* `internal/cli/monitor.go`/`send.go` — but since those files were modified *by this session's own work*, the current in-repo state (not the plan's citations) is the source of truth for exact line numbers; the plan's prose/contracts remain authoritative regardless.
