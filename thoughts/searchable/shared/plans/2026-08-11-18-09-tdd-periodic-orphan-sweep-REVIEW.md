## Plan Review Report: thoughts/searchable/shared/plans/2026-08-11-18-09-tdd-periodic-orphan-sweep-REVIEW.md — TDD Periodic Orphan Sweep

### Review Summary

**Verdict: Needs Major Revision.** The proposed confirmed-death path cannot discover the intended survivors, because it walks the pane-shell process tree only after tmux has destroyed that tree. The alternative PID source named by the plan is also empty in production: `runMonitor` registers every manifest agent with `ShellPID == 0` and never persists the live PID observed by health checks. A straightforward pre-death snapshot is still unsafe unless it retains and revalidates immutable process identity across the roughly 25-second miss window.

| Category | Status | Issues Found |
|----------|--------|--------------|
| Contracts | ❌ | 5 issues |
| Interfaces | ❌ | 6 issues |
| Promises | ❌ | 10 issues |
| Data Models | ❌ | 5 issues |
| APIs | ⚠️ | 4 issues |
| CodeCleanup Gates | ❌ | 6 issues |

### Contract Review

#### Well-Defined:

- ✅ The external command remains `internal-monitor <session>`; `newMonitorCmd` already delegates to `runMonitor(args[0])` (`internal/cli/monitor.go:28-38`).
- ✅ The existing direct-kill collector and reaper have concrete boundaries: recursively collect current descendants, exclude the roots, then signal surviving candidates (`internal/cli/send.go:2292-2379`).
- ✅ Global TOML loading decodes over `config.Default()`, so an absent boolean can preserve `true` while an explicit `false` overrides it (`internal/config/config.go:2711-2748`).
- ✅ Manifest deletion is idempotent, making absence after confirmed death a testable outcome (`internal/resilience/manifest.go:115-126`).

#### Missing or Unclear:

- ❌ **The process-tree discovery contract is temporally impossible.** The plan collects descendants only after the session reaches confirmed death (`plan:46-48`, `395-400`). `collectPaneDescendants` walks only current parent/child edges (`internal/cli/send.go:2300-2347`), while the existing `runKill` path deliberately snapshots before `tmux.KillSession` because survivors reparent after the pane shell dies (`internal/cli/send.go:2228-2253`).
- ❌ **The ownership invariant for delayed signaling is absent.** `reapOrphanProcesses` accepts bare integer PIDs and checks only current liveness (`internal/cli/send.go:2349-2379`). Across five 5-second misses (`internal/cli/monitor.go:167-175`, `197-228`), a PID can be reused by an unrelated process.
- ❌ **The stated shell-PID source does not satisfy its contract.** `runMonitor` registers manifest agents with PID 0 (`internal/cli/monitor.go:138-144`). `ScanAndRegisterAgents` is not called and skips already-registered pane IDs (`internal/resilience/monitor.go:123-142`); health checks read a live shell PID into a local variable but do not persist it (`internal/resilience/monitor.go:384-398`). `GetAgentStates` therefore returns the stored zeros (`internal/resilience/monitor.go:273-283`).
- ❌ **Snapshot refresh semantics are unspecified.** The plan does not say whether an empty result or a transient `GetPanes` error replaces the last usable snapshot, whether a recovered session resets it, or how pane respawns update ownership.
- ⚠️ **Timing preconditions are missing.** `time.NewTicker` panics for non-positive durations, and `maxMisses <= 0` has no documented meaning, despite the proposed helper returning `error`.

#### Recommendations:

- Capture an initial process snapshot while tmux is live and refresh it on each successful session poll, filtering panes to `manifest.Agents[].PaneID`.
- Define last-good semantics: a successful observation atomically replaces the snapshot; a missing session or ambiguous query error retains it; recovery resets misses and resumes refresh.
- Replace bare candidates with an identity-bearing type containing at least PID and immutable process start/creation identity. Revalidate identity immediately before both SIGTERM and SIGKILL.
- Define the result as an unordered, deduplicated candidate set. Do not promise that every captured candidate will still be live at reap time.
- Validate named loop options before creating tickers: poll interval and snapshot interval must be positive, and maximum misses must be at least one.

---

### Interface Review

#### Well-Defined:

- ✅ `tmux.CreateSession`, `GetPanes`, `SendKeys`, and `KillSession` provide the operations needed by a corrected real-tmux closure test (`internal/tmux/session.go:452-499`, `997-1000`, `1383-1390`).
- ✅ `tmux.Pane.PID` is the pane shell PID (`internal/tmux/session.go:58-73`).
- ✅ `Monitor.Stop` waits for its goroutines and leaves agent state readable (`internal/resilience/monitor.go:240-283`).

#### Missing or Unclear:

- ❌ `collectSessionOrphanCandidates(*resilience.Monitor)` is built on the wrong owner. The production monitor does not contain current shell PIDs, and the plan adds no update/upsert interface.
- ❌ The proposed closure process is not attached to the tmux boundary. `exec.Command("sh", ...)` is a child of the Go test, not of `panes[0].PID`; the local `paneShellPID` is unused and would itself prevent compilation (`plan:320-337`).
- ❌ The proposed unit tests call `resilience.NewMonitor(..., nil, false)` (`plan:96`, `138`), but the constructor immediately dereferences `cfg.Notifications` and `cfg.Resilience` (`internal/resilience/monitor.go:72-85`).
- ❌ `runMonitorWithIntervals` only changes ticker parameters while retaining unrelated global/process side effects: signal registration, supervisor daemons, CASS archiving, singleton-store closure, global `cfg`, and real manifest paths (`internal/cli/monitor.go:40-165`).
- ⚠️ Positional arguments of the same duration type can be silently swapped, and the plan does not validate them.
- ⚠️ Map iteration from `GetAgentStates` makes candidate ordering nondeterministic; duplicate shell roots and overlapping descendants are not specified.

#### Recommendations:

- Keep live pane/process-snapshot ownership in the CLI monitor loop, or add a real `UpdateAgentShellPID`/upsert API with pane-respawn tests. Do not claim current `resilience.Monitor` state already provides this data.
- Extract a small context-aware session-observation loop with explicit dependencies/config snapshot. Let `runMonitor` assemble supervisor, archive, webhook, signal, and resilience services around it.
- Use a named options struct with production defaults instead of positional timing values.
- Launch the closure target through `tmux.SendKeys`, capture its exact identity through a pidfile or observation hook, and wait for the production loop's first usable snapshot.
- Pass `config.Default()` to monitor unit tests unless changing the constructor's nil contract is explicitly added as a separate behavior.

---

### Promise Review

#### Well-Defined:

- ✅ The session must be absent for consecutive misses before cleanup; the current loop resets `missCount` on recovery (`internal/cli/monitor.go:197-205`).
- ✅ The existing death path emits the session-ended webhook, stops the resilience monitor, attempts summary generation, deletes the manifest, and returns (`internal/cli/monitor.go:206-228`).
- ✅ The toggle intends to leave existing session-end behavior intact when cleanup is disabled.

#### Missing or Unclear:

- ❌ **Functional closure is broken.** Post-death discovery normally returns no descendants, and the named monitor state contains only zero PIDs.
- ❌ **The closure test cannot seed the private production monitor.** It creates no injected monitor/state store, and its subprocess is outside the tmux tree.
- ❌ **The test does not prove reaping.** A normal in-pane `sleep` may die from tmux SIGHUP and create a false green; the current external process survives only because it is unrelated to tmux.
- ❌ **Readiness is a race.** A fixed 50ms sleep does not prove that startup, the loop, or a candidate snapshot completed (`plan:353-360`).
- ❌ **Cancellation and join behavior are absent.** A test timeout can leave the monitor goroutine running while global config and resources are restored.
- ❌ **Effect order regresses.** The plan inserts the blocking TERM/grace/KILL operation before `monitor.Stop()` (`plan:395-401`), leaving health/restart activity concurrent during the 750ms reap grace.
- ❌ **The toggle promise lacks a concrete gate test.** The only fully described test checks the config default; the disabled collector/reaper path is deferred to prose.
- ❌ **Cleanup leaks processes.** Killing and waiting for only `sh -c "sleep 30 & wait"` can leave the background `sleep` reparented, especially in the required toggle-off case.
- ❌ **Summary and manifest regressions are not observed.** The closure test checks only process liveness, and unconditional fallback manifest deletion can mask a production failure.
- ⚠️ The plan calls the in-process goroutine test a detached monitor-process test; it does not exercise Cobra startup, detachment, or OS-level monitor signals.

#### Recommendations:

- Specify the lifecycle as: start services → take a live scoped identity snapshot → signal ready → count missing observations → emit ended event → stop resilience monitor → identity-validate/reap cached candidates if enabled → attempt summary from last outputs → delete manifest → return/join.
- Add an explicit ready signal after the first usable candidate snapshot and accept `context.Context` for cancellation. Every test must cancel and join on all exits.
- Use an in-pane, deliberately HUP-surviving target (`nohup` or explicit HUP handling). Prove it is alive before tmux death, remains alive in the disabled control, and dies only in the enabled path.
- Add an injected callback-order test for `stop -> [reap] -> summary -> delete`, including enabled, disabled, and enabled-empty cases.
- Assert manifest absence before fallback cleanup. Treat real summary creation as conditional on a successful output snapshot; otherwise test summary invocation at the handler seam.
- Keep a separate manual or subprocess E2E if detached-process behavior is a required promise.

---

### Data Model Review

#### Well-Defined:

- ✅ `ReapOrphansOnExit bool` with TOML key `reap_orphans_on_exit` fits the existing global resilience config shape (`internal/config/config.go:487-497`).
- ✅ A global non-pointer boolean decoded over defaults supports absent-as-true and explicit-false semantics.
- ✅ `SpawnManifest` already demonstrates snapshotting effective spawn-time policy through `AutoRestart` (`internal/resilience/manifest.go:30-45`, `internal/cli/spawn.go:2495-2508`).

#### Missing or Unclear:

- ❌ Bare `[]int` cannot encode the process identity needed for safe delayed signaling.
- ❌ Toggle ownership is ambiguous. The detached monitor is launched as `internal-monitor <session>` without the spawning command's custom `--config` path (`internal/cli/spawn.go:2961-2969`), so a custom explicit false can reload as the default true.
- ❌ If the preferred manifest-snapshot fix is used, the plan omits the new manifest field and its round-trip test (`internal/resilience/manifest_test.go:49-107`).
- ⚠️ Project config has no resilience section and rejects unknown fields (`internal/config/project.go:17-27`, `104-120`); the plan does not state whether the toggle is global-only.
- ⚠️ Snapshot state has no definition for pane ID, shell PID, descendant identity, replacement generation, deduplication, or empty/error status.

#### Recommendations:

- Introduce an internal identity-bearing candidate model, for example `{PID, StartIdentity}`, plus an identity lookup/revalidation contract.
- Prefer snapshotting the effective toggle into `SpawnManifest`, mirroring `AutoRestart`, so the detached monitor honors the spawning command's selected config. Add manifest JSON round-trip coverage.
- State that the option is global-only, or deliberately add a pointer-backed project override and merge rule so unset differs from explicit false.
- Define last-known snapshot state and transition rules in the plan before naming concrete helpers.

---

### API Review

#### Well-Defined:

- ✅ No new public robot flag or external command is needed; the behavior belongs behind the existing internal monitor.
- ✅ The intended TOML spelling follows existing snake_case config conventions.
- ✅ Existing direct-kill behavior can remain unchanged while the slower periodic path gains a distinct identity-safe reaper.

#### Missing or Unclear:

- ⚠️ `config.Print` manually emits resilience keys, so generated/default config will omit the new option unless updated (`internal/config/config.go:3586-3596`).
- ⚠️ `config.GetValue` manually switches on paths; `ntm config get resilience.reap_orphans_on_exit` is not automatic and would return an unknown-path error (`internal/config/config.go:4597-4628`, `5311`). The plan incorrectly treats this as optional (`plan:286-287`).
- ⚠️ `config.Diff` manually enumerates resilience fields and would omit explicit false (`internal/config/config.go:5500-5510`).
- ⚠️ `ntm config show --json` manually constructs its response and currently omits resilience entirely (`internal/cli/root.go:4646-4759`); the plan does not define whether that command promises the full effective config.

#### Recommendations:

- Include the field in `Print`/CreateDefault, `GetValue`, and `Diff`, with explicit-false load and CLI `config get` tests.
- Decide and document whether JSON `config show` represents the complete effective config. If yes, add the resilience object and contract test; if not, avoid claiming that surface exposes the toggle.
- Add an observable reap result or structured log containing captured, identity-matched, TERM, KILL, and skipped-stale counts; the plan's manual log-evidence step cannot currently be performed because the reaper returns and logs nothing.

---

### CodeCleanup Plan-Hygiene Review

#### Well-Defined:

- ✅ The proposed boolean gate itself is a pure nil-protecting short circuit.
- ✅ `missCount++` remains a visible serial mutation rather than being embedded in a control expression.
- ✅ Reusing established tmux/process helpers is appropriate where their timing and identity preconditions still hold.

#### Missing or Unclear:

- ❌ **Maintainability Recovery fails:** the design patches the confirmed-death branch without addressing its lost process-tree boundary or the longer PID-identity lifetime.
- ❌ **Never Nesting fails:** another effect branch is added inside the existing `for -> select -> ticker -> missing -> threshold` cold path, even though the prose promises an extracted handler.
- ❌ **Named Constants Over Literals fails:** `5*time.Second, 30*time.Second, 5` and the test's `20ms`, `100ms`, `50ms`, and `3s` are positional/unnamed control values.
- ⚠️ **No Side Effects in Conditionals warns:** the load-bearing `tmux.SessionExists(session)` query remains embedded directly in a condition instead of becoming a named observation/seam.
- ⚠️ **Control Expressions Discipline warns:** snapshot refresh, miss recovery, candidate retention, and death handling are not modeled as explicit transitions.
- ⚠️ Candidate tests under-specify exact set behavior: prose promises two real roots, code registers one real root plus zero, and assertions only require non-empty output.

#### Recommendations:

- Extract the confirmed-death handler and express the observation loop with guard clauses: live path refreshes/reset; unconfirmed missing path continues; confirmed missing path performs ordered effects.
- Query external state in named statements (`sessionExists := ...`); keep snapshot mutations and effect calls out of boolean expressions.
- Use `monitorLoopOptions` with named production/test values and flat validation guards.
- Define unordered unique candidate semantics, deduplicate roots, and test two real subtrees, zero PID, duplicate root, overlapping descendants, and stale identity with exact set assertions.
- Preserve short-circuit and effect order explicitly; do not hoist a gate above state on which it depends.

---

### Critical Issues (Must Address Before Implementation)

1. **Lost ancestry boundary**: descendants are discovered only after the pane shell has died and survivors have reparented.
   - Impact: the feature normally reaps nothing; a reused shell PID can point discovery at an unrelated tree.
   - Recommendation: snapshot manifest-scoped descendant identities while tmux is live and retain the last successful snapshot.

2. **No immutable PID ownership check**: the proposed automatic daemon signals bare PIDs after a long confirmation delay.
   - Impact: PID reuse can cause SIGTERM/SIGKILL to target an unrelated process.
   - Recommendation: capture PID plus immutable start identity and revalidate before each signal; do not default the feature on without this invariant.

3. **Production PID source is empty**: `runMonitor` stores `ShellPID == 0` and never updates it.
   - Impact: `collectSessionOrphanCandidates` returns no candidates in the real path.
   - Recommendation: own live snapshots in the CLI loop or add and exercise an explicit monitor upsert API.

4. **Closure test does not cross the real boundary**: its subprocess is not a tmux descendant, cannot seed the private monitor, asserts an excluded root, and includes an unused local.
   - Impact: the test cannot become green through the proposed implementation or may pass for an unrelated reason.
   - Recommendation: launch a HUP-surviving process through the pane, await a real captured identity, test both toggle states, and assert the exact target.

5. **Constructor/test contract is invalid**: proposed tests pass nil to a constructor that dereferences config immediately.
   - Impact: Red fails by panic for the wrong reason.
   - Recommendation: use `config.Default()` or explicitly expand and test the nil contract.

6. **Asynchronous lifecycle is not controlled**: fixed sleep, mutable global config, ancillary daemons, no cancellation, and root-only cleanup make the closure test non-hermetic.
   - Impact: races, leaked goroutines/processes, user-state side effects, and flaky timeouts.
   - Recommendation: extract a context-aware loop with readiness and injectable ancillary boundaries; isolate XDG data and join every resource.

7. **Destructive-toggle ownership is ambiguous**: custom `--config` is not propagated to the detached monitor.
   - Impact: a user can explicitly disable reaping but still get default-on killing.
   - Recommendation: persist the effective toggle in the spawn manifest or propagate the exact config path with defined snapshot/live semantics.

### Suggested Plan Amendments

```diff
# Replace the proposed Behaviors 1–3 and production chain

- After confirmed session death, read ShellPID values from resilience.Monitor,
- traverse descendants, and pass []int to reapOrphanProcesses.
+ While the session is live, resolve only manifest-owned pane IDs and capture an
+ identity-bearing, deduplicated descendant snapshot.
+ Take an initial snapshot before readiness and refresh it on every successful
+ session poll. Retain the last good snapshot across misses/query errors.
+ On confirmed death: emit ended event -> Stop monitor -> if enabled, revalidate
+ candidate identity before TERM and again before KILL -> attempt summary ->
+ delete manifest -> return.

- Add runMonitorWithIntervals(session, poll, snapshot, maxMisses).
+ Extract a context-aware session loop with a named options/dependency struct,
+ positive timing validation, immutable effective config, and a ready signal.
+ Keep supervisor/archive/signal/global setup in runMonitor, outside the test seam.

- Spawn exec.Command("sh", "-c", "sleep 30 & wait") in the Go test.
- Sleep 50ms, kill tmux, and assert cmd.Process.Pid is dead.
+ Launch a deliberately HUP-surviving target through tmux.SendKeys and capture its
+ exact PID + start identity through a pidfile/observation.
+ Wait for loop readiness after the first usable candidate snapshot, kill tmux,
+ join with a deadline, and assert the exact identity is dead only when enabled.
+ Add a disabled control, stale-identity rejection, ordered callback test, full
+ subtree cleanup, manifest-before-fallback assertion, and XDG isolation.

- Add only ReapOrphansOnExit to ResilienceConfig and Default().
+ Define policy ownership. Prefer adding the effective value to SpawnManifest,
+ setting it from the spawning process, and round-trip testing it.
+ Complete config Print/CreateDefault, GetValue/config-get, Diff, explicit-false
+ load coverage, and decide config-show JSON and project-scope semantics.

- Reuse reapOrphanProcesses unchanged for delayed automatic cleanup.
+ Add an identity-safe delayed-reap boundary and observable result/log counts.
+ Keep the existing fast direct-kill path unchanged unless shared identity-safe
+ primitives can be introduced without changing its behavior.
```

### Approval Status

- [ ] **Ready for Implementation** - No critical issues
- [ ] **Needs Minor Revision** - Address warnings before proceeding
- [x] **Needs Major Revision** - Critical correctness, process-safety, configuration-ownership, and closure-test issues must be resolved first
