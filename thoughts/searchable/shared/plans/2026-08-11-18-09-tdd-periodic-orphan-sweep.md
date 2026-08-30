# Periodic Orphan-Process Sweep TDD Implementation Plan

## Review Disposition

This revision supersedes the reviewed design in full. The rejected design tried to discover descendants only after tmux had destroyed the pane-shell ancestry and tried to source shell PIDs from `resilience.Monitor`, whose production registrations contain `ShellPID == 0`. Neither path can satisfy the feature. The corrected slice captures manifest-scoped descendant identities while the session is live, retains the last valid snapshot across confirmed-missing observations, and revalidates immutable process creation identity immediately before every signal.

Review input: `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep-REVIEW.md`.

## Overview

Every real `ntm spawn` starts a detached `ntm internal-monitor <session>` process (`internal/cli/spawn.go:2490-2558`, `2961-2969`). Its current loop (`internal/cli/monitor.go:167-238`) polls tmux every five seconds, confirms death after five consecutive misses, emits the session-ended event, joins the resilience monitor, attempts a summary, deletes the spawn manifest, and returns. The missing behavior is safe cleanup of agent descendants that survive an organic tmux teardown.

The corrected implementation adds an identity-bearing live snapshot to a small context-aware inner loop. `runMonitor` remains the production assembler for signals, supervisor daemons, plugins, webhook bridge, resilience monitoring, archiving, and summary state; the extracted loop owns only session observations, snapshot state transitions, readiness, and confirmed-death dispatch. The destructive policy is resolved once by the spawning process and persisted in `SpawnManifest`, so a custom `--config` cannot silently turn an explicit `false` back into the default `true` in the detached child.

## Current-State Facts That Constrain the Design

- `runKill` snapshots descendants before `tmux.KillSession` because surviving agents reparent after the pane shell dies (`internal/cli/send.go:2228-2253`). `collectPaneDescendants` follows only current parent/child edges (`internal/cli/send.go:2300-2347`), so post-death discovery is invalid.
- `runMonitor` registers every manifest agent with PID zero (`internal/cli/monitor.go:138-144`). `ScanAndRegisterAgents` skips registered pane IDs (`internal/resilience/monitor.go:123-142`), health checks use a live shell PID only in a local variable (`internal/resilience/monitor.go:384-398`), and `GetAgentStates` only copies stored state (`internal/resilience/monitor.go:273-283`). `resilience.Monitor` is not the orphan-snapshot owner.
- `tmux.SessionExists` collapses every tmux failure into `false` (`internal/tmux/session.go:316-323`). `GetPanesContext` plus `tmux.ClassifyCommandError` distinguish definite absence from ambiguous infrastructure failure only once the session target is unambiguous. **Corrected during Behavior 7** (this was an overclaim in the original review input): a bare session name is subject to tmux's own prefix/glob target matching, so once a session is dead, `list-panes -s -t <name>` can report an *unclassified* `"can't find window: <name>"` on tmux 3.5a (never recognized as `CommandErrorSessionNotFound`, so `applyMonitorObservation` treats it as ambiguous and never advances the miss streak) — or, worse, silently resolve to a still-live prefix-sibling session (e.g. querying dead `"foo"` returns live `"foobar"`'s real panes) instead of erroring at all, which would make a caller believe the dead session is still alive, permanently masking its death. `exactSessionTarget` (`internal/tmux/session.go:480-483`) forces an exact, session-only target (`"="+session+":"`) so `GetPanesContext` (`internal/tmux/session.go:485-509`) and `GetPanesWithActivityContext` (`internal/tmux/session.go:1647-...`) both return a definite, correctly classified `CommandErrorSessionNotFound` for a genuinely dead session, verified by a real-tmux regression test (`internal/tmux/session_test.go`, `TestGetPanesContext_ExactSessionTarget_PrefixSiblingSurvives`) that creates a prefix-sibling session, kills only the base session, and proves the sibling is unaffected. See `internal/tmux/client.go:217-276` for `ClassifyCommandError` itself, which was intentionally left unchanged — broadening it to also treat `"can't find window"` as session-absence would be unsafe, since that phrase also legitimately means "the session exists but this particular window index doesn't," which must stay ambiguous rather than falsely confirm death.
- `Monitor.Stop` cancels and joins its goroutines (`internal/resilience/monitor.go:240-260`); it must happen before the potentially blocking TERM/grace/KILL phase.
- `github.com/shirou/gopsutil/v4` is already a direct dependency (`go.mod:26`). A freshly constructed gopsutil process exposes cross-platform creation time in milliseconds; the implementation must retain only scalar PID/create-time values because a retained gopsutil process caches creation time.
- `SpawnManifest` already snapshots `AutoRestart` (`internal/resilience/manifest.go:30-45`, `internal/cli/spawn.go:2494-2510`). The detached monitor argv intentionally contains only `internal-monitor <session>`, so the spawning command's selected config path is not propagated (`internal/cli/spawn.go:2961-2969`).
- Global config is decoded over `config.Default()`, preserving absent-as-default-true and explicit false (`internal/config/config.go:2711-2748`). Project config has no resilience section and rejects unknown fields (`internal/config/project.go:17-27`, `104-120`).
- Config `Print`, `GetValue`, `Diff`, and JSON `config show` are manually enumerated (`internal/config/config.go:3586-3596`, `4597-4628`, `5501-5510`; `internal/cli/root.go:4628-4759`). Every promised config surface must be updated deliberately.

## Desired End State

Given a session spawned with orphan reaping enabled, the internal monitor captures the identity of each current descendant of manifest-owned pane shells while tmux is live. If the session then becomes definitely absent for the configured number of consecutive observations, the monitor emits the ended event, joins resilience activity, revalidates each cached identity, sends TERM, waits the bounded grace period, revalidates again before KILL, attempts the existing summary, deletes the manifest, and exits. A PID that has exited or been reused is never signaled.

Given the policy was disabled at spawn, the monitor may retain the same read-only live identity snapshot needed for deterministic readiness and diagnosis, but it never invokes the delayed reaper or sends an orphan-reap signal. The ended event, stop/join, summary attempt, manifest deletion, and exit behavior remain intact.

## Locked Decisions and Contracts

### 1. Snapshot owner and scope

- The CLI session-observation loop owns the orphan snapshot. Remove `collectSessionOrphanCandidates(*resilience.Monitor)` from the design; do not add a PID upsert API to `resilience.Monitor` for this slice.
- Roots come only from nonzero `tmux.Pane.PID` values whose `Pane.ID` appears in `manifest.Agents[].PaneID`. User panes, foreign panes, supervisors, the archiver, the monitor itself, init, and the monitor's parent are outside the candidate set.
- Pane-shell roots are traversal roots only and are excluded from candidates, matching the existing direct-kill contract.
- Candidate semantics are an unordered unique set of `process.ProcessIdentity{PID, CreateTimeMillis}`. Duplicate roots and overlapping descendants produce one identity. Any sorting is only for deterministic tests/logs.

### 2. Identity and signal safety

- Add `ProcessIdentity`, `CaptureProcessIdentity`, and an error-returning/context-aware child enumeration boundary in `internal/process/liveness.go`; use fresh gopsutil process objects for every capture.
- The delayed reaper stores scalar identities, never bare PIDs and never gopsutil process objects.
- Immediately before TERM and again immediately before KILL: reject excluded PIDs, check liveness, capture a fresh identity, require exact equality, then call `syscall.Kill` adjacent to that check. An exit, mismatch, or lookup error fails closed for that signal.
- Keep the current fast `collectPaneDescendants`/`reapOrphanProcesses` direct-kill path behavior unchanged. The periodic path gets a distinct identity-safe reaper because it holds candidates across a much longer window.

### 3. Snapshot state and transitions

`orphanProcessSnapshot` contains `Valid`, `Generation`, `CapturedAt`, manifest-pane shell roots, and the unique identity set.

- Perform one synchronous observation before signaling readiness, then refresh on every successful live session poll; do not tie process refresh to the 30-second output-snapshot ticker.
- Build a complete candidate snapshot locally, then replace state atomically. A valid owned-pane observation with zero descendants is a valid empty snapshot and replaces the prior generation.
- A missing session retains the last valid snapshot and advances the consecutive-miss count.
- An ambiguous tmux error retains the snapshot and breaks the consecutive-definite-missing streak; it cannot cause destructive confirmation.
- A successful but unusable pane result (empty parsed pane list, or a manifest with agents but no owned pane present) retains the snapshot and resets the missing streak. A manifest with zero agents permits a valid empty snapshot.
- A live recovery resets misses; its next usable capture replaces the snapshot. A pane respawn under the same PaneID produces a new shell root and snapshot generation, replacing the old generation.
- A descendant that vanishes during capture is omitted. Any other child-enumeration or identity lookup failure rejects the whole refresh and retains the previous generation.

### 4. Loop and lifecycle contracts

- Introduce named `monitorLoopOptions` with production constants for poll interval, output-snapshot interval, maximum misses, and reap grace. Validate both intervals `> 0` and `MaxMisses >= 1` before creating a ticker or invoking a dependency.
- Introduce `monitorLoopDependencies` and `runSessionMonitorLoop(ctx, manifest, options, deps)`. Dependencies cover observation, output capture, process snapshot, readiness, ended event, stop/join, reaping, summary, and manifest deletion. Production wiring remains in `runMonitor`.
- Readiness fires exactly once after the first usable live observation and carries a defensive copy of the current snapshot. Production supplies no readiness observer; tests wait on it instead of sleeping.
- Cancellation returns promptly and is joined by every test. The existing signal path remains `stop -> summary -> return` and does not reap or delete a potentially live session manifest.
- Confirmed death is flat and ordered: `emit ended -> stop/join resilience -> reap/log if policy enabled (including an empty snapshot) -> attempt summary -> delete manifest -> return`.
- Summary failure or panic must not prevent manifest deletion. Manifest absence is asserted before any test fallback cleanup.

### 5. Policy ownership and config surfaces

- Add global-only `ResilienceConfig.ReapOrphansOnExit bool` with TOML key `reap_orphans_on_exit`; default it to `true` only after identity-safe capture/reaping exists.
- Add `SpawnManifest.ReapOrphansOnExit bool` without `omitempty`. Factor manifest construction so tests can prove both effective values are snapshotted even though monitor launch is disabled under `go test`.
- `runMonitor` reads only `manifest.ReapOrphansOnExit` for this destructive policy. Global config changes affect future spawns, not an already-running monitor. A legacy manifest without the field decodes `false` as a fail-safe; no compatibility shim is added.
- Project config does not gain a resilience override. Add a contract test that a project-level `[resilience]` section is rejected.
- Update text/default config output, `GetValue`/`config get`, `Diff`, and the curated JSON `config show` projection. JSON `config show` remains explicitly curated rather than a complete serialization, but it must expose this destructive setting under `resilience.reap_orphans_on_exit` and retain explicit `false`.

### 6. Observability

The identity-safe reaper returns `orphanReapResult` with exact counters: `Captured`, `MatchedBeforeTERM`, `TERMSignaled`, `MatchedBeforeKILL`, `KILLSignaled`, `SkippedStale`, `SkippedExited`, `SkippedExcluded`, `LookupErrors`, and `SignalErrors`. The confirmed-death handler emits one structured `slog` record with the enabled flag, snapshot generation/age, and every counter; it does not log raw PID lists.

## Scope Boundaries

- Do not change the JSON/kernel `sessions.kill` gap in `buildKillResponse`; it remains separately scoped.
- Do not build a system-wide sweep in `ntm serve`.
- Do not add a project-scoped resilience override, a new robot flag, or a public command.
- Do not claim that the in-process real-tmux Closure Test exercises Cobra startup, process detachment, or OS signal registration. Those remain explicit manual/subprocess E2E checks.
- Do not change existing direct `ntm kill` reap behavior while adding the identity-safe delayed path.

## Smallest Testable Behaviors

1. **Config and manifest policy** — Given no global key, explicit false, and a spawned session, when config/manifest surfaces are read, then defaults are true, explicit false is preserved, and the effective value is frozen in the manifest.
2. **Immutable process identity** — Given a live PID, when identity is captured repeatedly, then PID and creation time are stable; after exit or with a different creation time, it does not match.
3. **Manifest-scoped snapshot** — Given panes and two overlapping process trees, when a snapshot is captured, then only descendants of manifest-owned roots appear as an exact deduplicated identity set.
4. **Observation-loop transitions** — Given live, missing, ambiguous, recovered, empty, and respawned observations, when ticks are driven, then misses and last-good generations follow the locked transition table and readiness fires once.
5. **Identity-safe delayed reap** — Given identities that match, exit, or change generation between phases, when reap runs, then only current owners receive TERM/KILL and every result counter is exact.
6. **Confirmed-death effects** — Given enabled/disabled and populated/empty snapshots, when death is confirmed, then callbacks occur in the required order and manifest deletion survives summary failure/panic.
7. **Real-tmux workflow closure** — Given a HUP-surviving in-pane target, when tmux is killed and the real loop confirms death, then enabled reaping removes the exact identity while the disabled control leaves it alive.

## Testing Strategy

- **Unit tests**: process identity, exact candidate-set semantics, loop option validation and transitions, reaper phase safety/counters, callback order, toggle gating, config surfaces, project-scope rejection, manifest policy round trip, and spawn-time policy freezing.
- **Integration/Closure tests**: real tmux, real `SendKeys`, real in-pane HUP-surviving process, real `GetPanesContext`, production snapshotter, production ticker, real identity-safe reaper, XDG-isolated manifest, and bounded cancellation/join.
- **No sleep-based readiness**: unit tests use an explicit tick driver; Closure tests use named short production intervals but wait on readiness and done channels with named deadlines.
- **No construction of `resilience.NewMonitor(..., nil, ...)`**: its constructor dereferences config (`internal/resilience/monitor.go:72-85`). Tests that need it use `config.Default()`; most new loop tests use the extracted callback seam and do not construct it.
- **Exact assertions**: compare identity sets and result structs, not `len > 0`; assert manifest absence before fallback cleanup; assert the disabled target remains the same live identity before cleanup.

## Workflow Closure

### Corrected Production Operation Chain

`runMonitor starts services -> runSessionMonitorLoop performs live GetPanesContext observation -> filter manifest pane IDs -> capture descendant {PID, CreateTimeMillis} snapshot -> signal ready/continue polling -> definite session absence reaches MaxMisses -> emit ended -> Monitor.Stop joins -> identity-safe TERM/grace/KILL -> structured result log -> summary attempt -> DeleteManifest -> return`

`runMonitor` is the production caller and registration point for the new loop; `newMonitorCmd` continues to delegate the existing `internal-monitor <session>` command to `runMonitor` (`internal/cli/monitor.go:28-38`). No new unregistered worker/handler is introduced.

### Closure Test A: enabled organic-death cleanup — BLOCKING

- **Classification**: BLOCKING because the behavior crosses the tmux process boundary, a ticker/async boundary, the new loop connector, the OS process table, and signal delivery.
- **SOURCE**: A real tmux session whose manifest owns the first pane. Through `tmux.SendKeys`, launch `nohup sh -c` with explicit HUP handling, write `$$` to an XDG/temp pidfile, and keep a child alive. Capture the exact `{PID, CreateTimeMillis}` after the pidfile appears.
- **TRIGGER**: `runSessionMonitorLoop` with production observation/snapshot/reap dependencies and named short valid options. This is the highest new connector in the slice; ancillary supervisor/archive/webhook/global setup stays outside the seam.
- **DRIVERS**: A real production ticker drives observations. The test waits for `ready(snapshot)` containing the exact target identity, then calls `tmux.KillSession`. Context cancellation and a bounded join channel drive cleanup; there are no fixed startup sleeps.
- **OBSERVABLE**: Fresh identity lookup reports that every identity in the ready snapshot is absent after the loop joins; the XDG-isolated manifest is absent and the summary callback was invoked once.
- **FORBIDDEN SPAN**: The test does not call snapshot collection, identity matching, either reaper, signal functions, or the death handler directly. It only seeds through tmux/manifest, starts the loop, kills tmux, and observes through fresh process identity plus manifest APIs.
- **RED-AT-SEAM**: Before the confirmed-death handler invokes the identity-safe reaper, the HUP-surviving identity remains current after loop completion; this makes the test red for the missing behavior.
- **DRIVABILITY**: The new loop accepts context, named options, dependencies, and readiness. The process store is the real OS process table; the async driver is the real ticker. No mock exists inside the forbidden span.

### Closure Test B: disabled control — BLOCKING

Use the same SOURCE, TRIGGER, driver, and observable path with `manifest.ReapOrphansOnExit == false`. After the loop joins, a fresh identity lookup must still match the exact HUP-surviving target, while ended/stop/summary/delete still complete. This prevents a false green caused by ordinary tmux SIGHUP behavior.

### Hermetic cleanup contract

Every Closure subtest registers cleanup immediately: cancel the context; merge the ready snapshot with a fresh target subtree snapshot while possible; kill the tmux session if it still exists; join the loop with a named deadline; identity-safely terminate the entire captured subtree; wait until no captured identity matches; then perform fallback manifest deletion. Test-body assertions for production deletion occur before fallback cleanup.

## Behavior 1: Global config is complete and spawn freezes the destructive policy

### Test Specification

**Given** a default global config, a config omitting the key, and a config explicitly setting `reap_orphans_on_exit = false`, **when** config is loaded and exposed, **then** default/absent are true and explicit false remains false across `Print`/CreateDefault, `GetValue`/`config get`, `Diff`, and curated JSON `config show`.

**Given** an effective global config at spawn, **when** the spawn manifest is built and round-tripped, **then** `SpawnManifest.ReapOrphansOnExit` exactly equals the spawn-time value, including explicit false, and the detached monitor uses that field only. A raw legacy manifest without the field decodes false. A project `[resilience]` section is rejected.

### Files Touched

- `internal/config/config.go`: field/default plus Print, GetValue, and Diff enumeration.
- `internal/config/config_test.go`: default, absent, explicit-false load, Print/CreateDefault round trip.
- `internal/config/coverage_boost_test.go`: exact `GetValue` false assertion.
- `internal/config/diff_test.go`: true-default to false-current diff.
- `internal/config/project_test.go`: global-only rejection contract.
- `internal/cli/root.go`, `internal/cli/cli_test.go`: curated JSON `config show` resilience entry and custom `config get` false.
- `internal/resilience/manifest.go`, `internal/resilience/manifest_test.go`: manifest field, true/false/raw-key/missing-key cases.
- `internal/cli/spawn.go`, `internal/cli/spawn_test.go`: factor `buildSpawnManifest` (or equivalently named real builder) from the test-disabled launch block and prove policy snapshotting. Existing monitor argv remains unchanged.

### TDD Cycle

#### Red

1. Add `TestResilienceReapOrphansOnExit_DefaultAbsentAndExplicitFalse`.
2. Extend Print/CreateDefault, GetValue, Diff, `config get`, and JSON `config show` tests with exact values, not merely presence/no-error.
3. Extend manifest round-trip tests for true and false; inspect raw JSON to require the false key; add a missing-field fixture expecting false.
4. Add `TestBuildSpawnManifestSnapshotsEffectiveResiliencePolicy` and `TestLoadProjectConfig_RejectsResilienceSection`.

Run:

```bash
rch exec -- go test ./internal/config ./internal/resilience ./internal/cli -run 'ReapOrphansOnExit|SpawnManifestSnapshots|RejectsResilienceSection' -count=1
```

The Red failures are missing fields/switch cases/builder behavior, never a nil-config panic.

#### Green

- Add the global bool with default true and all manual config enumerations.
- Add the manifest bool without `omitempty`.
- Build the manifest from the already-selected effective `cfg` in the spawning process; do not add `--config` to `newInternalMonitorCommand`.
- Read the manifest field into immutable loop options in `runMonitor`; do not consult the detached child's global config for the gate.

#### Refactor

- Keep JSON `config show` documented/tested as curated while exposing this safety-relevant field.
- Keep manifest construction in one production helper used by the real spawn path and tests.
- Do not add pointer-backed project merge logic or legacy-manifest compatibility behavior.

### Success Criteria

- [ ] Generated/default config includes `reap_orphans_on_exit = true` and reloads true.
- [ ] Explicit false survives TOML load, config get/show, manifest save/load, and spawn construction.
- [ ] Diff reports path `resilience.reap_orphans_on_exit`, default true, current false.
- [ ] Legacy/missing manifest field is false fail-safe.
- [ ] Project-level resilience is rejected as unknown.

## Behavior 2: Process creation identity is stable, fresh, and distinguishable from PID reuse

### Test Specification

**Given** the current live process, **when** `CaptureProcessIdentity` is called twice, **then** the returned PID/create-time values are equal and nonzero.

**Given** a captured identity, **when** the same PID is paired with another creation time or the process exits, **then** a fresh capture does not validate the stored owner. Each validation creates a new gopsutil process; no cached process object crosses calls.

### Files Touched

- `internal/process/liveness.go`: `ProcessIdentity`, `CaptureProcessIdentity`, and context/error-returning immediate-child enumeration used by live snapshots. Preserve `IsAlive` and existing `GetChildPIDs` callers.
- `internal/process/liveness_test.go`: stable identity, mismatched creation time, exited process, invalid PID, cancellation/enumeration error.

### TDD Cycle

#### Red

Add:

- `TestCaptureProcessIdentity_CurrentProcessStable`
- `TestCaptureProcessIdentity_ExitedProcess`
- `TestProcessIdentity_RejectsDifferentCreateTime`
- `TestGetChildPIDsContext_ReturnsErrorAndHonorsLimit`

Run `rch exec -- go test ./internal/process -run 'ProcessIdentity|GetChildPIDsContext' -count=1`.

#### Green

Store only:

```go
type ProcessIdentity struct {
    PID              int
    CreateTimeMillis int64
}
```

Use a fresh `gopsutil/process.NewProcessWithContext` plus `CreateTimeWithContext` on every capture. Return errors rather than manufacturing an identity. The new child boundary returns an error so the monitor can distinguish a valid empty tree from a failed refresh; existing direct-kill helpers retain their current behavior.

#### Refactor

- Keep OS/process-library details in `internal/process`, not in monitor state transitions.
- Do not expose a retained gopsutil object or treat `IsAlive(pid)` alone as ownership.
- Reuse the existing depth/fanout constants from the CLI orphan domain for snapshot traversal.

### Success Criteria

- [ ] A stable process produces the same nonzero creation identity.
- [ ] Exit, invalid PID, cancellation, and lookup failure are explicit.
- [ ] No bare-PID delayed ownership API is introduced.

## Behavior 3: Live snapshot capture is manifest-scoped, exact, and generation-aware

### Test Specification

**Given** panes containing two manifest-owned shell roots, a foreign/user root, a zero root, a duplicate root, and overlapping descendant graphs, **when** `captureOrphanProcessSnapshot` runs with deterministic process dependencies, **then** it excludes shell roots and foreign/excluded PIDs and returns the exact unordered unique set of descendant identities.

**Given** a child that exits during capture, **when** identity capture reports not-running, **then** that child is omitted. **Given** any other enumeration/identity error, **then** capture fails without exposing a partial replacement.

### Files Touched

- `internal/cli/monitor.go`: `orphanProcessSnapshot`, manifest-pane filtering, complete-local-then-assign capture helper.
- `internal/cli/monitor_test.go` (new, justified because monitor.go has no tests): exact-set and error semantics.

### TDD Cycle

#### Red

Add table-driven `TestCaptureOrphanProcessSnapshot_FiltersManifestPanesAndDeduplicates` covering:

- two real roots and exact descendants;
- duplicate root and overlapping descendant;
- zero/excluded/foreign root;
- manifest with zero agents;
- vanished child;
- enumeration/identity error with no partial snapshot.

All comparisons normalize to a set; no case accepts only “non-empty.”

#### Green

- Precompute owned pane IDs from the manifest.
- Deduplicate roots before a bounded depth/fanout walk.
- Capture identity as each child is discovered; exclude roots and `orphanReapExcluded` candidates.
- Assemble `Valid`, roots, candidates, and capture time locally. Generation is assigned only when the loop commits a successful replacement.

#### Refactor

- Keep capture side effects out of condition expressions.
- Keep manifest filtering, traversal, and state replacement at separate named boundaries.
- Return defensive copies to readiness/tests so callers cannot mutate loop state.

### Success Criteria

- [ ] Exact set semantics cover every root/overlap/zero/error case from the review.
- [ ] A valid empty snapshot differs from “no valid snapshot yet.”
- [ ] `resilience.Monitor.GetAgentStates` is absent from the new path.

---

## Behavior 4: The context-aware loop validates options and preserves last-good state

### Test Specification

**Given** nonpositive poll/output intervals or `MaxMisses < 1`, **when** the loop is constructed, **then** it returns a validation error before creating tickers or invoking observation/effect callbacks.

**Given** a driven sequence of observations, **when** the loop advances, **then** it follows this exact transition table:

| Observation | Miss streak | Candidate snapshot | Readiness |
|---|---:|---|---|
| First usable live capture | reset to 0 | atomically replace, generation +1 | fire once with copy |
| Later usable live capture | reset to 0 | atomically replace, generation +1 | no second fire |
| Valid live capture, zero descendants | reset to 0 | replace with valid empty | no second fire |
| Definite session missing/no server | +1 | retain | unchanged |
| Ambiguous tmux error | reset/break to 0 | retain | unchanged |
| Empty/unusable pane parse or no owned pane while manifest has agents | reset to 0 | retain | unchanged |
| Live pane respawn under same PaneID | reset to 0 | replace roots/candidates, generation +1 | unchanged |
| Context canceled | return promptly | no mutation after cancel | no death effects |

At the maximum definite-missing streak, the loop passes the retained snapshot to the confirmed-death handler exactly once and returns.

### Files Touched

- `internal/cli/monitor.go`: production constants, options validation, dependency struct, observation classification, state, readiness, flat poll handler, context-aware loop, and `runMonitor` wiring.
- `internal/cli/monitor_test.go`: manual tick driver/state tables, cancellation/join, readiness copy/once semantics.

### TDD Cycle

#### Red

Add:

- `TestMonitorLoopOptions_ValidateBeforeDependencies`
- `TestRunSessionMonitorLoop_LastGoodSnapshotTransitions`
- `TestRunSessionMonitorLoop_ReadinessOnceAndDefensiveCopy`
- `TestRunSessionMonitorLoop_CancellationJoinsWithoutDeathEffects`

Use named local constants such as `testPollInterval`, `testOutputInterval`, and `testJoinDeadline`; do not scatter duration literals.

#### Green

- Replace positional `runMonitorWithIntervals` with `monitorLoopOptions` and explicit dependencies.
- Production observation calls `GetPanesContext`, stores the result/error in named variables, and classifies errors before branching. Only `CommandErrorSessionNotFound` and `CommandErrorNoServer` advance the destructive miss streak; cancellation exits; all other errors are ambiguous.
- Split transition/effect work into named helpers and guard clauses so the ticker branch does not become another deeply nested confirmed-death block.
- Build production tickers only after validation. Unit tests drive the same loop through an explicit clock/tick dependency; the Closure Test uses real tickers.

#### Refactor

- Keep `runMonitor` as assembler and `runSessionMonitorLoop` as the narrow state machine.
- Keep output capture separate from process-snapshot refresh even though both share the select loop.
- Keep external calls out of boolean/control expressions (`observation, err := ...` before `switch`/guards).
- Name production defaults instead of embedding `5*time.Second`, `30*time.Second`, and `5` at call sites.

### Success Criteria

- [ ] All invalid options fail without ticker panic or callback activity.
- [ ] Missing, ambiguous, recovery, empty, and respawn transitions match the table exactly.
- [ ] Ready fires once only after a usable capture and is immutable to the recipient.
- [ ] Cancellation always joins; no goroutine survives test cleanup.

---

## Behavior 5: Delayed reaping validates identity before TERM and KILL

### Test Specification

**Given** an identity-safe reaper with deterministic liveness/identity/signal/grace dependencies, **when** candidates change across phases, **then**:

| Case | TERM | KILL | Required accounting |
|---|---:|---:|---|
| Same identity through both phases | yes | yes if still live | both matches/signals |
| PID stale before TERM | no | no | one stale skip |
| Identity lookup error before TERM | no | no | one lookup error |
| Same identity at TERM, exits during grace | yes | no | TERM + exited skip |
| Same identity at TERM, new creation time before KILL | yes | no | TERM + stale skip |
| Excluded PID | no | no | excluded skip |
| Signal returns error | attempt only | phase rules continue safely | signal error, no success count |
| Empty input | no | no | zero-valued result with Captured 0 |

### Files Touched

- `internal/cli/send.go`: add `reapIdentifiedOrphanProcesses` and result/dependency types beside existing orphan helpers; do not alter the existing direct-kill reaper.
- `internal/cli/send_test.go`: deterministic phase/revalidation/result tests.

### TDD Cycle

#### Red

Add `TestReapIdentifiedOrphanProcesses_RevalidatesBeforeTERMAndKILL` as a table with exact signal transcripts and exact result structs. Add a real-process smoke case only after deterministic branch coverage; it must clean the whole subtree and wait every started process.

#### Green

For each unique candidate:

1. Reject exclusions.
2. Determine exited vs live.
3. Fresh-capture and compare identity.
4. Send TERM immediately adjacent to a match and record success/error.
5. Wait using a context-aware grace driver only if at least one candidate reached TERM validation.
6. Repeat exclusion, liveness, fresh capture, equality, and signal accounting before KILL.

Candidates failing pre-TERM ownership do not enter phase two. A TERM syscall error may enter phase two because the same owned process may still require KILL.

#### Refactor

- Keep result counting in one place and log only from the confirmed-death handler.
- Reapply `orphanReapExcluded` in both phases, not only `pid <= 1`.
- Keep the grace duration named/injected; no unbounded sleep.

### Success Criteria

- [ ] No stale/exited/lookup-error candidate is signaled.
- [ ] A process changing generation after TERM is never KILLed.
- [ ] Every review-requested counter is exact and observable.
- [ ] Existing `ntm kill` collector/reaper tests remain unchanged and passing.

## Behavior 6: Confirmed death preserves effect order, gating, summary, and deletion

### Test Specification

**Given** callback-recording dependencies, **when** confirmed death is handled, **then** exact transcripts are:

- enabled + populated: `ended, stop, reap, log, summary, delete`;
- enabled + valid empty: `ended, stop, reap, log, summary, delete` with zero reap counts;
- disabled + populated: `ended, stop, summary, delete`, with reaper/log callbacks never invoked;
- summary error or panic: deletion still occurs after the attempted summary;
- delete error: returned/logged after all preceding required effects, never hidden by fallback cleanup.

### Files Touched

- `internal/cli/monitor.go`: `handleConfirmedSessionDeath`, panic-safe summary boundary, structured reap log, and production callback assembly.
- `internal/cli/monitor_test.go`: order/gate/error tables and production-wiring assertions.

### TDD Cycle

#### Red

Add:

- `TestHandleConfirmedSessionDeath_EffectOrder`
- `TestHandleConfirmedSessionDeath_DisabledNeverReaps`
- `TestHandleConfirmedSessionDeath_SummaryFailureStillDeletesManifest`
- `TestRunMonitor_UsesManifestPolicyAndProductionLoop`

The disabled handler test uses callbacks that fail the test if reap/log is invoked. Snapshot capture is a read-only loop responsibility and is covered separately. The enabled-empty case must still call the reaper so a zero-count structured record exists.

#### Green

- Read the immutable manifest bool once into loop/handler inputs.
- Emit ended first, synchronously call `monitor.Stop`, then conditionally call the identity-safe reaper, log its result, invoke the existing summary boundary, delete the manifest, and return.
- Preserve the separate signal/cancel path and do not move the destructive gate above state on which it depends.

#### Refactor

- Use guard clauses and named effect calls rather than extending the existing nested ticker branch.
- Ensure `runMonitor` is the sole production assembler/caller of the new loop and handler dependencies.
- Keep summary invocation observable at the seam; real summary file creation remains conditional on captured output.

### Success Criteria

- [ ] Stop/join precedes every possible signal.
- [ ] Disabled means no reaper invocation or signal, not merely a reaper receiving an empty list.
- [ ] Summary remains attempted once and manifest deletion remains idempotent/observable.
- [ ] Structured logs contain enabled, generation/age, and all result counters, with no raw PID list.

## Behavior 7: Real tmux organic death closes the workflow for enabled and disabled policy

### Test Specification

**Given** an XDG-isolated manifest and a real tmux pane, **when** a deliberately HUP-surviving target is launched through `tmux.SendKeys`, writes its PID, and the loop's ready snapshot contains its exact creation identity, **then** direct `tmux.KillSession` plus confirmed death removes every ready-snapshot identity when enabled.

With the same setup and policy disabled, the exact target identity must still match after the loop joins. Both cases must observe one summary callback and manifest absence before fallback cleanup.

### Files Touched

- `internal/cli/monitor_test.go`: table-driven real-tmux Closure Test and cleanup helper. Both `enabled`/`disabled` subtests keep a prefix-distinct sentinel tmux session alive for the subtest's duration so killing the target session is never the tmux server's last session — otherwise the observation error would be the coarser `CommandErrorNoServer` rather than the session-specific `CommandErrorSessionNotFound`, and the test could false-green without ever exercising the fix below.
- `internal/tmux/session.go` / `session_test.go` (**blocking amendment, discovered while implementing this behavior — not anticipated by the original review input**): `GetPanesContext` and `GetPanesWithActivityContext` built their tmux target from the bare session name. Real tmux does prefix/glob matching on session-name targets, so once a session is dead this either reports an unclassified `"can't find window: <name>"` (tmux 3.5a; never recognized as `CommandErrorSessionNotFound`, so the miss streak never advances) or — if a live sibling session's name happens to have the dead session's name as a prefix — silently returns that sibling's real pane data instead of erroring at all, permanently masking the death to any caller, including this feature's own confirmed-death path. Fixed with an `exactSessionTarget` helper (`"="+session+":"`, disabling prefix/glob matching and forcing session-target parsing) applied at both call sites; `ClassifyCommandError` (`internal/tmux/client.go`) is deliberately left unchanged, since broadening it to treat `"can't find window"` as session-absence would misclassify a live session's missing window index as confirmed death. Proven by a new real-tmux regression test creating a prefix-sibling session pair. This crosses outside the plan's original file inventory, but is a correctness prerequisite for Behavior 7 (and, retroactively, for Behavior 4's reliance on `ClassifyCommandError`) to actually detect an externally-killed session in production — not a new production function reachable only from a test.
- Production code otherwise is already listed in Behaviors 3–6; no test-only production function is permitted.

### TDD Cycle

#### Red

Add `TestRunSessionMonitorLoop_OrganicDeathHUPSurvivor` with `enabled` and `disabled` subtests:

1. Call `testutil.RequireTmuxThrottled(t)` and set `XDG_DATA_HOME`/project dir to temp directories.
2. Create a unique session; fetch its real pane and save a manifest owning that pane.
3. Send a shell-quoted command shaped as `nohup sh -c 'trap "" HUP; write $$ to pidfile; keep a child alive' ... &` through `tmux.SendKeys`.
4. Poll the pidfile with a named deadline; capture the exact identity and prove it is live/HUP-surviving.
5. Start `runSessionMonitorLoop` with real observer, snapshotter, real ticker, real reaper, ready channel, callback summary, and real manifest deletion.
6. Wait until ready contains the exact target and its descendant; do not sleep a guessed startup duration.
7. Kill only through `tmux.KillSession`, join the loop with a named deadline, then assert enabled absence vs disabled identity match.
8. Assert summary callback count and production manifest absence before cleanup.

Before Green wiring, enabled is red because the HUP-surviving identity remains current. Disabled must already demonstrate the control is capable of surviving tmux death.

#### Green

Wire the production observer, snapshot replacement, retained confirmed-death snapshot, manifest policy, and identity-safe reaper through the extracted loop. Do not call `runMonitor` from this test because supervisor/archive/webhook/signal/global setup is outside the promised closure span.

#### Refactor

- Factor only test setup/cleanup helpers; never add a production function called solely from tests.
- Cleanup cancels and joins first, then identity-safely cleans every captured process; never kill a bare PID after a failed ownership check.
- Name every interval/deadline constant and keep tests serial where environment variables/global tmux resources require it.

### Success Criteria

- [ ] Enabled test proves the exact in-pane identity survives tmux teardown and is then removed by the monitor path.
- [ ] Disabled test proves the same identity survives after loop completion until explicit test cleanup.
- [ ] No fixed readiness sleep, leaked goroutine, leaked child, mutable global cfg, real user manifest, supervisor daemon, or fallback deletion can make the test green.
- [ ] The test is described accurately as an in-process real-tmux Closure Test, not a detached-monitor-process E2E.

## File Inventory and Production Registration

| File | Planned change | Production caller/registration |
|---|---|---|
| `internal/process/liveness.go` | Process identity and error-returning child enumeration | Called by CLI snapshot/reaper; no background registration |
| `internal/process/liveness_test.go` | Identity/enumeration tests | Test only |
| `internal/cli/send.go` | Identity-safe delayed reaper/result beside unchanged direct reaper | Called by confirmed-death handler |
| `internal/cli/send_test.go` | Phase safety and exact counter tests | Test only |
| `internal/cli/monitor.go` | Snapshot model/capture, options/deps, context loop, handler, production wiring | `runMonitor` calls the loop; existing `newMonitorCmd` calls `runMonitor` |
| `internal/cli/monitor_test.go` | New unit and real-tmux Closure tests | Test only; no production-only-for-tests helper |
| `internal/config/config.go` | Global toggle and all manual config surfaces | Existing config load/show/get/diff commands |
| `internal/config/config_test.go` | Default/load/Print/CreateDefault contracts | Test only |
| `internal/config/coverage_boost_test.go` | Exact GetValue contract | Test only |
| `internal/config/diff_test.go` | Diff contract | Test only |
| `internal/config/project_test.go` | Global-only rejection | Test only |
| `internal/cli/root.go` | Curated JSON config-show field | Existing `config show` Cobra command |
| `internal/cli/cli_test.go` | CLI config get/show false contracts | Test only |
| `internal/resilience/manifest.go` | Frozen policy field | Existing Save/LoadManifest |
| `internal/resilience/manifest_test.go` | True/false/missing/raw-key cases | Test only |
| `internal/cli/spawn.go` | Manifest builder snapshots effective policy | Existing `runSpawn` manifest-save/monitor-launch path |
| `internal/cli/spawn_test.go` | Builder snapshot tests; monitor argv unchanged | Test only |
| `internal/tmux/session.go` | `exactSessionTarget` helper; `GetPanesContext`/`GetPanesWithActivityContext` target strings corrected to use it | Called by `runSessionMonitorLoop`'s `Observe` (Behavior 4/7) and other existing pane-listing call sites; blocking fix discovered while implementing Behavior 7 (see "Current-State Facts" correction above) |
| `internal/tmux/session_test.go` | Real-tmux prefix-sibling disambiguation regression test | Test only |

No new production file is required. `internal/cli/monitor_test.go` is a genuinely new test surface for previously untested monitor lifecycle code.

## Review-Finding Traceability

| Review finding | Locked amendment / proving behavior |
|---|---|
| Descendants discovered after ancestry is gone | Live capture before death; Behaviors 3, 4, 7 |
| Bare PID reuse can signal unrelated process | `{PID, CreateTimeMillis}` plus two fresh validations; Behaviors 2, 5 |
| `resilience.Monitor` production PID source is zero | Remove it from design; CLI loop owns manifest-scoped snapshot; Behaviors 3–4 |
| Empty/error/recovery/respawn semantics unspecified | Explicit transition table and generation model; Behavior 4 |
| Nonpositive ticker/max-miss inputs | Named validated options before dependencies/tickers; Behavior 4 |
| Wrong snapshot-owner interface | No monitor upsert/collector; production loop dependencies and manifest pane IDs; Behaviors 3–4 |
| Closure subprocess was outside tmux tree/unused pane PID | Launch through `tmux.SendKeys`; exact pidfile identity; Behavior 7 |
| Nil config passed to `NewMonitor` | New tests do not do so; any constructor use passes `config.Default()` |
| `runMonitorWithIntervals` retained unrelated global side effects | Context-aware inner loop; `runMonitor` remains assembler; Behaviors 4, 6–7 |
| Positional same-type timing arguments | `monitorLoopOptions` plus named constants; Behavior 4 |
| Nondeterministic/duplicate candidates | Unordered unique identity-set contract with exact assertions; Behavior 3 |
| Functional closure broken/private monitor unseedable | Explicit loop deps/readiness plus real production span; Workflow Closure, Behavior 7 |
| Ordinary in-pane sleep could false-green on SIGHUP | HUP-surviving enabled/disabled pair; Behavior 7 |
| Fixed 50ms readiness race | `ready(snapshot)` containing exact target; Behavior 4/7 |
| No cancellation/join | Context-aware loop and mandatory cleanup/join; Behaviors 4/7 |
| Reap before `Monitor.Stop` | Required effect transcript puts stop first; Behavior 6 |
| Toggle had no call-site gate proof | Disabled callbacks fail on reap plus disabled real closure; Behaviors 6–7 |
| Root-only cleanup leaked child | Cleanup merges/reaps the entire identity snapshot; Workflow Closure/Behavior 7 |
| Summary/manifest regressions hidden | Summary callback count; manifest absence before fallback; Behaviors 6–7 |
| In-process test mislabeled detached monitor | Explicitly named in-process real-tmux Closure Test; detached E2E remains manual |
| `[]int` lacks identity | `ProcessIdentity` data model; Behaviors 2/5 |
| Custom `--config` not propagated to child | Freeze effective bool in manifest; Behavior 1 |
| Manifest field/round trip omitted | Field without omitempty; true/false/raw/missing tests; Behavior 1 |
| Project override ambiguous | Global-only rejection contract; Behavior 1 |
| Snapshot model undefined | Valid/generation/time/roots/identities model; Locked Decisions/Behaviors 3–4 |
| Config Print/CreateDefault omitted | Required manual surface tests; Behavior 1 |
| Config GetValue/get treated optional | Required switch + CLI test; Behavior 1 |
| Config Diff omitted explicit false | Required diff; Behavior 1 |
| JSON config-show contract undecided | Curated projection, explicitly includes this destructive setting; Behavior 1 |
| No observable reap evidence | Exact result plus structured log counters; Behaviors 5–6 |
| Maintainability/lost boundary | Live snapshot + identity-safe delayed boundary; Behaviors 2–5 |
| Added deep branch | Extracted flat handler/loop with guard clauses; Behaviors 4/6 |
| Unnamed duration/count literals | Production/test named constants and options; Behaviors 4/7 |
| Side-effectful/control-dense conditionals | Named observation/effect statements and explicit transition helpers; Behavior 4/6 |
| Candidate test prose/code mismatch | Exact two-tree/zero/duplicate/overlap/stale tables; Behaviors 3/5 |

## Implementation Order

1. Behavior 2: land identity primitives and tests.
2. Behavior 3: land exact manifest-scoped snapshot capture.
3. Behavior 5: land identity-safe delayed reaper and counters.
4. Behavior 4: extract/validate the loop and lock transitions/readiness/cancellation.
5. Behavior 6: wire ordered confirmed-death effects behind an explicit manifest policy.
6. Behavior 1: expose config, persist/freeze policy, and wire spawn/monitor ownership. Do not enable the default in production before steps 1–5 are green.
7. Behavior 7: run the real-tmux enabled/disabled closure and then the full verification matrix.

Each behavior must be committed only after its Red fails for the intended missing contract, Green passes, and Refactor gates remain green.

## Automated Verification Matrix

```bash
# Focused Red/Green suites
rch exec -- go test ./internal/process -run 'ProcessIdentity|GetChildPIDsContext' -count=1
rch exec -- go test ./internal/cli -run 'CaptureOrphanProcessSnapshot|RunSessionMonitorLoop|ReapIdentifiedOrphanProcesses|HandleConfirmedSessionDeath' -count=1
rch exec -- go test ./internal/config ./internal/resilience ./internal/cli -run 'ReapOrphansOnExit|SpawnManifestSnapshots|RejectsResilienceSection' -count=1

# Blocking real-tmux closure; must not be silently replaced by a unit test
rch exec -- go test ./internal/cli -run TestRunSessionMonitorLoop_OrganicDeathHUPSurvivor -count=1 -v

# Race-sensitive loop/reaper tests
rch exec -- go test -race ./internal/cli -run 'RunSessionMonitorLoop|ReapIdentifiedOrphanProcesses' -count=1

# Repository-required checks
rch exec -- go build ./cmd/ntm
rch exec -- go test -short ./...
rch exec -- golangci-lint run
gofmt -l .
goimports -l .

# Required changed-file bug scan before commit
ubs internal/process/liveness.go internal/process/liveness_test.go \
  internal/cli/send.go internal/cli/send_test.go \
  internal/cli/monitor.go internal/cli/monitor_test.go \
  internal/config/config.go internal/config/config_test.go \
  internal/config/coverage_boost_test.go internal/config/diff_test.go \
  internal/config/project_test.go internal/cli/root.go internal/cli/cli_test.go \
  internal/resilience/manifest.go internal/resilience/manifest_test.go \
  internal/cli/spawn.go internal/cli/spawn_test.go
```

`gofmt -l .` and `goimports -l .` must produce no changed-file output. If `golangci-lint` or `goimports` is unavailable, record that exact environment limitation; do not silently omit the gate.

## Manual / Detached-Process E2E

1. Use a temporary global config with reaping enabled and `ntm spawn` a disposable session containing a long-lived agent/test command.
2. Verify the detached `internal-monitor <session>` exists and its manifest records `reap_orphans_on_exit: true`.
3. Kill the tmux session directly, never through `ntm kill`.
4. After the real confirmation window, verify the captured identity is gone, the manifest is absent, the summary attempt remains present, and the monitor log contains structured reap counts.
5. Repeat with explicit false. Verify the manifest records false, the same HUP-surviving identity remains after monitor exit, then identity-check and clean it explicitly.

This manual check covers Cobra startup, detached execution, OS signal registration, supervisor/archive assembly, and real log routing that the in-process Closure Test intentionally does not claim.

## References

- Review: `thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep-REVIEW.md`
- Research and original closure map: `thoughts/searchable/shared/research/2026-08-11-16-42-wire-up-periodic-orphan-sweep.md`
- Existing live-before-kill snapshot/reaper: `internal/cli/send.go:2228-2379`
- Existing real process-tree test: `internal/cli/send_test.go:1663-1719`
- Monitor lifecycle/effects: `internal/cli/monitor.go:40-239`
- Resilience PID/stop contracts: `internal/resilience/monitor.go:72-85,123-142,240-283,384-398`
- Tmux observation/error/real operations: `internal/tmux/client.go:217-276`, `internal/tmux/session.go:315-323,452-503,997-1000,1383-1390`
- Manifest/XDG contracts: `internal/resilience/manifest.go:30-126`
- Config surfaces: `internal/config/config.go:487-531,2711-2748,3586-3596,4597-4628,5501-5510`
- Spawn policy/monitor argv: `internal/cli/spawn.go:2490-2558,2961-2969`
- Tmux test throttling: `tests/testutil/throttle.go:66-82`
