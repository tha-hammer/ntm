# Live-Polled Per-Agent Memory Estimation TDD Implementation Plan

## Overview

`ntm spawn`/`--robot-spawn` size aggregate-memory admission from `config.PerAgentExpectedMemMB(cfg)`, a static per-agent-type-blind constant (2048 MB) shipped as a tactical fix for the "5 agents refused with 29GB free" bug (`internal/config/templates.go`, `defaultPerAgentExpectedMemMB`). It is still a guess, and the same guess for every agent type.

`2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation.md` was the first attempt to replace that guess with something learned. Its review (`...-REVIEW.md`) correctly found the original capture mechanism broken — it read the wrong process's cgroup, and read it too late to reliably see anything — but the accepted rewrite fixed those two bugs by inserting a brand-new process-launch layer (a hidden `syscall.Exec` runner + a detached finalizer subprocess) into every single agent spawn, plus a versioned cross-process-locked store, HMAC host identity, and 9 real-tmux/systemd/binary closure tests. That is disproportionate to the two bugs it fixes: both are fixable by extending machinery ntm already runs, without touching how any agent process gets launched.

This plan is that narrower fix. It keeps every finding from the review that identifies a real defect, and rejects only the *mechanism* chosen to fix them.

## Relationship to the Prior Plan

Explicitly kept, ported into this plan's own Locked Decisions:

- **Finding: wrong process was sampled.** `tmux.Pane.PID` is the pane's login shell, not the agent (`internal/tmux/session.go`). Reading the shell's cgroup measures the shell, not `claude`/`codex`. **Kept.**
- **Finding: capture was too late.** Confirming session death takes ~25s of missing-session polling (`monitorDefaultMaxMisses=5` at `monitorDefaultPollInterval=5s`, `internal/cli/monitor.go:367-371`), and a systemd transient scope is removed the instant it's empty — by then the scope this plan wants to read is very likely already gone. **Kept.**
- **Finding: `--robot-spawn` has no evidence producer at all** — it never saves a manifest or starts `internal-monitor` (`internal/robot/spawn.go`, confirmed no `SaveManifest`/`internal-monitor` references exist there today). **Kept**, fixed by reusing the CLI's existing monitor rather than building new evidence-production infrastructure for robot.
- **Finding: PID reuse can misattribute evidence.** **Kept** — reuses the same `process.ProcessIdentity{PID, CreateTimeMillis}` pattern the orphan-sweep feature already established (`internal/process/liveness.go:86-108`).
- **Finding: automatic learning must never recommend less than the shipped 2048MB baseline without an explicit operator override.** **Kept** as a hard invariant.

Explicitly rejected, with the reasoning:

- **A `syscall.Exec` runner + detached finalizer subprocess pair per agent launch.** The two bugs above are both about *when and what* something already-running reads, not about *how the agent process comes into existence*. Fixing them doesn't require rerouting every agent's exec path through new hidden commands. This is also the single highest-blast-radius part of the rejected design: a bug in it breaks spawning agents at all, on every host running ntm.
- **A versioned, cross-process-locked, byte-bounded store with deterministic SHA-256 sample/generation IDs.** The only concurrent writer in this plan is `internal-monitor` (one per active session — plural sessions are the only real concurrency source), which is the same concurrency shape `internal/history/storage.go` already solves with a plain `flock`-based process lock (`internal/history/lock_unix.go:13-37`). Reusing that pattern is enough; a bespoke transaction/versioning protocol is not needed for a file with a handful of writers appending a few dozen bytes at a time.
- **HMAC-derived host identity.** Solves cross-host sample mixing for a file this plan never syncs anywhere (`$XDG_DATA_HOME/ntm/...`, local-only, exactly like every other store this codebase already keeps there). If ntm ever syncs this file across machines, host-scoping becomes a real problem worth solving then — not before.
- **Full `/proc/self/mountinfo` parsing to locate the cgroup v2 mount.** Modern unified-hierarchy Linux mounts cgroup v2 at `/sys/fs/cgroup`; this plan hardcodes that with a test-only override var and fails open (no evidence, not a crash) if it's missing. Revisit only if a real legacy-hybrid-mount host is reported.
- **9 real-tmux/systemd/built-binary closure tests with zero mocks.** One is enough to prove the real cgroup-read boundary works; the rest of the behaviors are pure functions or fixture-driven, exactly like the rest of this codebase's test suite.

## Current-State Facts That Constrain the Design

- `manifestOwnedPaneRoots(manifest, panes)` already resolves every manifest-owned agent pane to its shell PID (`internal/cli/monitor.go:273-295`).
- `captureOrphanProcessSnapshot` already walks every root's descendant subtree via `process.GetChildPIDsContext`, bounded by `orphanReapMaxDepth=4`/`orphanReapFanout=64` (`internal/cli/send.go:2286-2296`), capturing a fresh `process.ProcessIdentity` for each descendant on every successful poll tick — but discards the walk's own pane/type attribution into one flat `map[ProcessIdentity]struct{}` (`internal/cli/monitor.go:244-250, 311-364`). This plan's core mechanism is: **keep that attribution instead of discarding it**, reusing the exact same walk.
- `memLimitPrefix()` wraps eligible templates (default Claude/Codex/Gemini only) in `systemd-run --user --scope -q -p MemoryMax=... -p MemoryHigh=...` (`internal/config/templates.go:159-172`, `310-344`). `systemd-run --scope` places the invoking process directly into a new transient scope and then execs the target command in place (no fork) — so the pane shell's direct child, once running, *is* the agent process, inside a cgroup distinct from the shell's own. Custom/unsupported templates that skip `memLimitPrefix` leave the child in the same cgroup as the shell; this plan treats "descendant's cgroup path equals the shell's own" as the unscoped case and produces no evidence for that pane, no different from cold-start behavior today.
- `pollMonitorLoopOnce`/`applyMonitorObservation` rebuild the snapshot wholesale on every usable poll and only ever compare/act on the miss-streak and confirmed-death boundary (`internal/cli/monitor.go:488-524`) — there is currently no per-pane "this specific agent's generation just ended" signal, even though the whole session is still alive. This plan adds that comparison as a new step inside the existing loop, not a new loop.
- `handleConfirmedSessionDeath(ctx, enabled, snap, deps)` already runs the ordered `emit ended -> stop/join -> reap (if enabled) -> summary -> delete manifest` chain with a dependency-injection struct (`confirmedDeathDeps`, `internal/cli/monitor.go:596-644`) built for exactly this kind of best-effort side effect to be added safely.
- No code anywhere reads `memory.peak`/`memory.current` from a live cgroup today (`grep -rn "memory.peak\|MemoryCurrent" internal/` matches only an unrelated synthetic-benchmark struct in `internal/swarm/synthetic.go`) — the cgroup reader itself is genuinely new, unavoidably so.
- `internal/history/storage.go` + `internal/history/lock_unix.go:13-37` is this codebase's proven process-safe-store pattern (`flock`, `LOCK_EX`); `internal/state/timeline_persist.go` is only instance-locked and is *not* the pattern to imitate here, since this store has genuine multi-process writers.
- `agent.AgentType.Canonical()` (`internal/agent/types.go:12-40`) already normalizes every alias (`"claude"`, `"cc"`, `"claude-code"`, ...) to one of three canonical tokens — reuse it; do not invent a second normalizer.
- `robot.GetSpawn` creates real tmux panes and sends commands but never calls `resilience.SaveManifest` or launches `internal-monitor` (confirmed: zero matches for either in `internal/robot/spawn.go`) — today it leaves no background process running at all. CLI spawn does both (`internal/cli/spawn.go:2525, 2539, 2976-2984`).

## Desired End State and Safety Invariants

Given a host that has run CLI-spawned agents before, a new spawn sizes each requested agent type's demand from that type's own recently-observed peak memory, not one flat number shared by every type. Given no evidence for a type yet (cold start, a fresh install, or a host that has only ever used unscoped/custom templates), that type falls back to exactly `defaultPerAgentExpectedMemMB`, byte-identical to today's shipped behavior.

1. Automatic sizing never returns less than `defaultPerAgentExpectedMemMB` (2048 MB). Only an explicit positive `per_agent_expected_mem_mb` override may go lower.
2. `PerAgentMemLimitMB()`/`memLimitPrefix()` enforcement is untouched. The estimator's ceiling is always `PerAgentMemLimitMB()`.
3. No agent's exec path changes. Nothing about how `claude`/`codex`/`gemini` gets launched is different after this plan than before it.
4. Evidence capture is best-effort everywhere: any read/lock/parse failure logs and is silently skipped. It can never delay `handleConfirmedSessionDeath`'s manifest deletion, never change an agent's exit status, and never block a poll tick.
5. Evidence is captured only for panes whose descendant is running in a cgroup distinct from its own pane shell (i.e., `memLimitPrefix`-eligible templates); every other pane is a normal, observable, non-error fallback case.
6. A given agent generation (one pane, one process identity) can be recorded at most once, whether it's caught by the per-tick "generation ended" path or the whole-session confirmed-death path.

## Locked Decisions

### 1. Cgroup memory reader (new, minimal)

```go
// internal/process/cgroup.go (+ cgroup_linux.go, cgroup_other.go — build-tagged)

var cgroupRoot = "/sys/fs/cgroup" // test-only override

// CgroupPathForPID resolves the single unified (0::) cgroup v2 path for pid
// from /proc/<pid>/cgroup. ok=false on missing/malformed/non-unified entries,
// a PID that has exited, or a non-Linux platform.
func CgroupPathForPID(pid int) (path string, ok bool)

// ReadCgroupMemoryPeakBytes reads memory.peak under cgroupRoot+path, falling
// back to memory.current if memory.peak doesn't exist (older kernels). ok is
// false on any read/parse failure or an unsigned-overflow value.
func ReadCgroupMemoryPeakBytes(path string) (bytes uint64, ok bool)
```

- No mount-table parsing; `cgroupRoot` is a package var fixtures/tests override.
- No device/inode identity capture. PID-reuse defense is `process.ProcessIdentity` (PID + create time), captured immediately before and immediately after the cgroup read; a mismatch discards that single reading, not the whole poll.
- `ok=false` is always a silent skip for that one pane/tick, never an error surfaced to the loop.

### 2. Typed per-pane scope tracking (extends the existing walk, doesn't replace it)

```go
// internal/cli/monitor.go

type agentScopePeak struct {
    PaneID     string
    AgentType  agent.AgentType // agent.AgentType(manifest entry).Canonical()
    Identity   process.ProcessIdentity
    PeakBytes  uint64
    ObservedAt time.Time
}

// orphanProcessSnapshot gains one field:
//   ScopePeaks map[string]agentScopePeak // keyed by PaneID
```

- `captureOrphanProcessSnapshot`'s existing walk, when visiting a root's *direct* child (depth==1) whose resolved cgroup path differs from the root's own, additionally: read its `memory.peak`, and — if this is the first time this generation (same `PaneID`+`Identity`) has been observed OR the newly-read value is larger — store/update `ScopePeaks[paneID]`. This is a small addition inside the loop that already runs; it does not add a second traversal.
- A pane whose depth-1 child's cgroup matches the shell's own (unscoped template, or systemd unavailable) simply never gets a `ScopePeaks` entry — same code path as "read failed," no special-casing.

### 3. Per-tick "generation ended" detection (new step in the existing loop)

- `applyMonitorObservation`, immediately before it overwrites `state.snapshot` with a freshly captured one, diffs `state.snapshot.ScopePeaks` (previous) against the new capture's `ScopePeaks`: any `PaneID` present in the old set but absent from the new one — while the pane itself is still a manifest-owned root this tick, i.e., the session is still alive — means that specific agent's generation just ended (normal exit, crash, or resilience restart into a fresh process).
- For each such pane, call a new `deps.OnAgentGenerationEnded(ctx, agentScopePeak)` (added to `monitorLoopDependencies`, production default persists via the store from Locked Decision 5; test default is a no-op or recorder). This directly answers the "normal exits and restarts vanish before confirmed death" finding without a dedicated finalizer process — the monitor already polls every 5s, so the gap between "true peak" and "last observed peak" is bounded by the poll interval, not by however long an agent happened to run.
- This is strictly additive to the locked observation transition table in the orphan-sweep plan; it does not change miss-streak, generation, or confirmed-death semantics.

### 4. Confirmed-death integration

- `confirmedDeathDeps` gains `FinalizeMemorySamples func(ctx context.Context, peaks map[string]agentScopePeak)`, called after `Stop`/join and before/alongside the existing reap step (independent of `reap_orphans_on_exit` — memory evidence is not a destructive action and must not share that gate).
- Production default persists every still-present `ScopePeaks` entry from the final retained snapshot. Combined with Locked Decision 3, this covers both "individual agent exited mid-session" and "whole session died at once" without double-counting: a pane already finalized via `OnAgentGenerationEnded` is removed from `ScopePeaks` at that point, so it can't also be finalized here.
- Every error/panic/timeout is logged and swallowed. `DeleteManifest`'s error remains the only one `handleConfirmedSessionDeath` returns, unchanged from today.

### 5. Store

```go
// internal/pressure/memory_store.go (or a small internal/agentmemory
// package if the estimator + store outgrow internal/pressure — decide
// during Behavior 6, not up front)

type MemorySample struct {
    PaneID     string          `json:"pane_id"`
    AgentType  agent.AgentType `json:"agent_type"`
    PeakBytes  uint64          `json:"peak_bytes"`
    ObservedAt time.Time       `json:"observed_at"`
    Generation int64           `json:"generation"` // ProcessIdentity.CreateTimeMillis — dedup key
}

type MemorySampleStore interface {
    Load(ctx context.Context) ([]MemorySample, error)
    Append(ctx context.Context, sample MemorySample) error
}
```

- File-based, `flock`-locked exactly like `internal/history/storage.go` (`Load` takes a shared lock, `Append` takes an exclusive lock spanning read-modify-write-rename).
- Path: `$XDG_DATA_HOME/ntm/agent-memory-samples.json`, else `$HOME/.local/share/ntm/...` — same resolution order as this codebase's other XDG-scoped stores.
- Dedup key is `(PaneID, AgentType, Generation)` — a plain composite, not a cryptographic hash; a duplicate append (e.g. both `OnAgentGenerationEnded` and `FinalizeMemorySamples` somehow racing) is a no-op, not an error.
- Bounded retention: keep the most recent `maxSamplesPerType = 50` per `AgentType`, oldest evicted first by `ObservedAt`. No separate global-byte-size ceremony — 50 samples × 3 types × ~80 bytes of JSON is trivially small.
- `Append` failure (lock contention, disk full, corrupt existing file) logs and returns an error the caller (Locked Decisions 3/4) already treats as best-effort/swallowed. A corrupt file is left untouched, never auto-deleted; `Load` on a corrupt file returns an error and callers fall back to cold-start behavior for every type.

### 6. Estimator

```go
// internal/pressure/agent_memory_estimate.go

type MemoryEstimateSource string
const (
    MemoryEstimateOverride   MemoryEstimateSource = "override"
    MemoryEstimateCalibrated MemoryEstimateSource = "calibrated"
    MemoryEstimateFallback   MemoryEstimateSource = "fallback"
)

func EstimateAgentMemMB(samples []MemorySample, floorMB, ceilingMB uint64) (mb uint64, source MemoryEstimateSource, confidence float64)
```

- p90 of `PeakBytes` (nearest-rank, same convention `internal/pressure/calibration.go` already uses elsewhere in this file), converted to MB with ceiling division, multiplied by a `5/4` safety margin, then `clamp(floorMB, ceilingMB)`.
- Confidence reuses the existing curve already in this package, `min(1, log1p(n)/log1p(100))`, gated at `0.35` (`internal/pressure/calibration.go`'s existing constant) — below the gate, `source=fallback` and the returned value is exactly `floorMB` (2048), not a low-confidence computed number. This is what makes Invariant 1 (never below 2048 automatically) hold structurally, not just by convention.
- Zero samples is `fallback`/`floorMB`, identical to today's shipped constant — proving this plan is additive, not a regression, is Behavior 5 below.

### 7. Wiring into admission

- `config.PerAgentExpectedMemMB(cfg)` gains a per-type sibling used by both `internal/cli/spawn.go` and `internal/robot/spawn.go`'s admission call sites, replacing `totalAgents * flatEstimate` with `Σ count(type) * EstimateAgentMemMB(loadedSamples[type], floor, PerAgentMemLimitMB())` — `opts.Agents []FlatAgent` (CLI) already carries `.Type` per requested agent, so the per-type breakdown is already available at the call site without new plumbing.
- An explicit positive `per_agent_expected_mem_mb` override, exactly as today, short-circuits this entirely — no store load, no estimator call.
- `pressure.SpawnAdmission`'s existing JSON gains a `memory_estimates []AgentMemoryEstimate` field (type, count, estimate_mb, source, confidence, sample_count) so `--robot-spawn --dry-run` stays inspectable, matching this session's already-verified live check.

### 8. Robot spawn evidence gap

- `robot.GetSpawn`, on a real (non-dry-run) spawn, optionally also calls `resilience.SaveManifest` + launches `internal-monitor` — the exact same two calls `internal/cli/spawn.go:2525,2539` already makes, not new machinery.
- Gated behind a new `SpawnPacing.Headroom.RobotMonitorEnabled bool`, **default `false`**. Today robot spawns leave no background process running; flipping that default silently would be a real, surprising behavior change for existing automation (CI, scripts) that may run in ephemeral/constrained environments. An operator who wants robot-spawned agents to also contribute evidence opts in explicitly.
- Robot-spawned agents still *benefit* from evidence gathered via CLI spawns on the same host even with this flag off (the store isn't scoped by spawn path) — they just don't contribute samples themselves unless opted in. This is not a regression: robot spawns contribute zero evidence today too, under the static constant.

## Scope Boundaries

- No `syscall.Exec` runner, no finalizer subprocess, no hidden hijacking of any agent's launch command. Confirmed by code review: nothing in Locked Decisions 1-8 touches `internal/config/templates.go`'s command-rendering output.
- No HMAC/cryptographic host identity. No cross-host sync of the sample store.
- No `/proc/self/mountinfo` parsing. `cgroupRoot` is a hardcoded path with a test override.
- No versioned store schema, no schema migration story. One JSON shape, no `schema_version` field — add one later if a breaking change is ever needed.
- No PSI-based backpressure (unchanged from the original plan's scope cut — still a separate, complementary idea).
- Do not change `PerAgentMemLimitMB`, `MemoryHigh`, `MemoryMax`, or any systemd property.
- Do not track per-individual-agent-instance history, only per `AgentType`.
- REST (`internal/serve`) is out of scope for this pass; it already delegates to `robot.GetSpawn`, so it inherits Locked Decision 8's behavior automatically once that lands — no separate REST-specific work is needed.

## Smallest Testable Behaviors

### Behavior 1 — Cgroup memory reader (LEAF) ✅ complete

Implemented: `internal/process/cgroup.go` (`cgroupRoot`, `parseCgroupProcFileContent`, `ReadCgroupMemoryPeakBytes`), `internal/process/cgroup_linux.go` (`CgroupPathForPID`), `internal/process/cgroup_other.go` (stub). Tests: `internal/process/cgroup_test.go`, `internal/process/cgroup_linux_test.go`. `go build`/`go vet` verified on linux, darwin, windows; `go test -race -run Cgroup` passes (15/15).

- **Given/When/Then:** a real or fixture PID with a known cgroup path -> `CgroupPathForPID`/`ReadCgroupMemoryPeakBytes` -> exact path/bytes on success; `ok=false` on missing/malformed/exited/non-Linux, never an error/panic.
- **RED:** missing `/proc/<pid>/cgroup`, malformed `0::` line, missing `memory.peak` falling back to `memory.current`, missing both, permission denied, non-numeric content.
- **GREEN:** build-tagged Linux implementation + `cgroup_other.go` stub returning `ok=false` unconditionally.
- **REFACTOR:** `cgroupRoot` injectable, no other package-level mutable state.

### Behavior 2 — Typed scope-peak capture inside the existing walk (LEAF, extends existing tests) ✅ complete

Implemented in `internal/cli/monitor.go`: `agentScopePeak` type, `orphanProcessSnapshot.ScopePeaks`, `orphanSnapshotDeps.cgroupPathForPID`/`readCgroupMemoryPeakBytes` (nil-safe, backward compatible), `manifestOwnedPaneRootIDs`, `captureScopePeak`, and `copyOrphanProcessSnapshot` deep-copies `ScopePeaks`. Tests: `TestCaptureOrphanProcessSnapshot_ScopePeaks` (4 subtests: scoped-vs-unscoped, peak-update-not-regress, identity-mismatch-discard, nil-deps-no-op) in `internal/cli/monitor_test.go`. All prior monitor/orphan tests still pass unmodified.

- **Given/When/Then:** a manifest with scoped and unscoped panes -> `captureOrphanProcessSnapshot` -> `ScopePeaks` has an entry only for panes whose depth-1 descendant's cgroup differs from its shell's; the existing flat `Candidates` set is unchanged.
- **RED:** scoped pane produces an entry with correct `AgentType`/`Identity`/`PeakBytes`; unscoped pane produces none; a larger peak on a later poll updates, a smaller one does not regress it; identity mismatch between before/after read discards that one reading only.
- **GREEN:** extend the existing walk's depth==1 branch.
- **REFACTOR:** keep the addition inside the existing function; do not fork a parallel traversal.

### Behavior 3 — Per-tick generation-ended detection (LEAF) ✅ complete

Implemented in `internal/cli/monitor.go`: `monitorLoopDependencies.OnAgentGenerationEnded` (nil-safe, optional) and the ScopePeaks diff step in `applyMonitorObservation`, immediately before the snapshot replacement. Test: `TestApplyMonitorObservation_OnAgentGenerationEnded` (4-tick sequence: no-baseline, unchanged, disappear-while-alive fires once, session-gone does not fire) in `internal/cli/monitor_test.go`. Note: production wiring of this callback to the real store is deferred until Behavior 5 (store) exists — `runMonitor`'s `loopDeps` does not set it yet, which is a safe no-op per the field's nil-safety.

- **Given/When/Then:** two successive `applyMonitorObservation` calls where one pane's `ScopePeaks` entry disappears while the session stays alive -> `OnAgentGenerationEnded` fires exactly once with that pane's last reading; a pane present in both ticks does not fire; a pane absent because the *whole session* is gone does not fire here (that's Behavior 4's path).
- **RED:** disappear-while-alive fires once; unchanged does not fire; session-gone does not double-fire against Behavior 4.
- **GREEN:** diff old vs. new `ScopePeaks` before replacing `state.snapshot`.
- **REFACTOR:** table-driven against the existing locked transition table so this addition's cases sit beside the already-tested ones, not in a separate file.

### Behavior 4 — Confirmed-death memory finalization (BLOCKING — crosses the existing effect-order boundary) ✅ complete

Implemented in `internal/cli/monitor.go`: `confirmedDeathDeps.FinalizeMemorySamples` (nil-safe, optional), called unconditionally (independent of `enabled`) right after `StopResilience` and before the reap gate, wrapped in the same panic-recovery pattern as `Summary`. Test: `TestHandleConfirmedSessionDeath_FinalizeMemorySamples` (5 subtests: unconditional+ordering+exact-payload, ordering-when-enabled, empty-map-still-finalizes, panic-recovered-chain-continues, nil-is-safe-no-op) in `internal/cli/monitor_test.go`. All prior confirmed-death tests still pass unmodified. Production wiring (`productionConfirmedDeathDeps`) is deferred until Behavior 5's store exists, same as Behavior 3's `OnAgentGenerationEnded` — both will be wired together once there's a real destination to persist to.

- **Given/When/Then:** confirmed death with a nonempty final `ScopePeaks` -> `handleConfirmedSessionDeath` -> `FinalizeMemorySamples` runs after stop/join, before/alongside reap; store/panic/timeout failures are logged and swallowed; `DeleteManifest` remains the only returned error; a pane already finalized via Behavior 3 is not finalized again.
- **RED:** success, store error, panic, timeout, empty snapshot, already-finalized pane excluded, ordering relative to existing reap/summary/delete steps.
- **GREEN:** add `FinalizeMemorySamples` to `confirmedDeathDeps`, wired independent of `reap_orphans_on_exit`.
- **REFACTOR:** guard clauses, no new nesting in the existing ordered chain.

### Behavior 5 — Store (BLOCKING — filesystem/durability boundary) ✅ complete

Implemented: `internal/pressure/memory_store.go` (`MemorySample`, `MemorySampleStore` interface, `MemorySampleStorePath`, `fileMemorySampleStore`, `loadMemorySamplesLocked`/`saveMemorySamplesLocked`/`appendMemorySample`), `internal/pressure/memory_store_lock_unix.go` (flock, shared for Load/exclusive for Append), `internal/pressure/memory_store_lock_windows.go` (mutex-only stub, matching `internal/session/lock_windows.go`'s precedent). Location: kept in `internal/pressure` (no need surfaced to split into a new package). Tests in `internal/pressure/memory_store_test.go`: round-trip, duplicate-no-op (store-level and pure-function), eviction order, per-type-independent eviction, corrupt-file-untouched-on-both-Load-and-Append, and a **real 5-subprocess concurrent-Append test** (`TestFileMemorySampleStore_ConcurrentAppendAcrossProcesses` + `TestMemoryStoreHelperProcess` self-exec helper, mirroring `internal/process/liveness_test.go`'s pattern) proving no sample is lost to cross-process flock contention — stronger than `internal/history`'s own goroutine-only concurrency test, since flock's actual purpose is cross-*process* arbitration. All builds verified on linux/darwin/windows.

- **Given/When/Then:** temp XDG dir, valid/duplicate/malformed appends -> `Load`/`Append` -> durable JSON, exact dedup on `(PaneID, AgentType, Generation)`, bounded retention at 50/type, corrupt file left untouched and reported as an error.
- **RED:** round-trip, duplicate no-op, eviction order, concurrent `Append` from two real processes (subprocess test, matching `internal/history`'s own concurrency test style) losing no samples, corrupt-file passthrough.
- **GREEN:** `flock`-based `Load`/`Append` mirroring `internal/history/storage.go`.
- **REFACTOR:** isolate decode/dedupe/prune/encode as pure steps under the lock.

### Behavior 6 — Exact estimator (LEAF) ✅ complete

Implemented `internal/pressure/agent_memory_estimate.go`: `MemoryEstimateSource` + constants, `EstimateAgentMemMB` (p90 nearest-rank → ceil-to-MB → 5:4 margin → clamp, gated by the reused `confidenceFromSamples`/`defaultMinCalibrationConfidence` from `calibration.go`), `nearestRankPercentile`, `ceilBytesToMB`, `clampUint64` (hardens the "ceiling below floor" case so the floor invariant holds structurally). Tests in `internal/pressure/agent_memory_estimate_test.go`: the n=0/1/4/5/11/100 confidence-gate boundary sweep (4 is fallback, 5 is calibrated — computed, not guessed), exact p90/margin arithmetic at two non-trivial indices, calibrated-but-clamped-to-floor (proves source classification is independent of the numeric clamp outcome), low-ceiling clamp, and invalid-ceiling-below-floor. Location decision: stayed in `internal/pressure` alongside the store — no need surfaced to split into a new package.

- **Given/When/Then:** n=0/1/4/5/10/11/100 samples at known peaks, plus a low ceiling -> `EstimateAgentMemMB` -> exact p90/5:4-margin/clamp math, confidence curve, and the `fallback`-at-floor behavior below the confidence gate.
- **RED:** cold start returns exactly `defaultPerAgentExpectedMemMB` byte-for-byte (proves non-regression against the tactical fix already shipped); ceiling below floor is rejected; every source/confidence boundary.
- **GREEN:** p90 + margin + clamp, reusing `calibration.go`'s existing confidence curve.
- **REFACTOR:** named constants for percentile rank, margin fraction, and confidence gate — no magic numbers.

### Behavior 7 — Admission wiring, CLI + robot (LEAF/pure resolver, BLOCKING at the two call sites) ✅ complete

Implemented `internal/pressure/spawn_memory_resolver.go` (`AgentMemoryEstimate`, `ResolveSpawnMemoryEstimate` — the one shared resolver), `MemoryEstimates` field threaded through `SpawnAdmissionInput`→`SpawnAdmission` in `pressure.go`. Wired into both call sites: `internal/cli/spawn.go` (`spawnMemoryEstimateCounts` — prefers `opts.Agents`' per-agent canonical type, falls back to legacy `--cc`/`--cod`/etc counts) and `internal/robot/spawn.go` (`spawnMemoryEstimateCounts` — always from `CCCount`/`CodCount`/`GmiCount`/`AgyCount`). Tests: resolver-level (`spawn_memory_resolver_test.go`: override zero-store-I/O proof, mixed-type sum, load-failure-falls-back, zero-count exclusion, sort order) plus per-package count-grouping tests (`TestSpawnMemoryEstimateCounts*` in both `internal/cli/spawn_test.go` and `internal/robot/spawn_test.go`). Live-verified via a real `--robot-spawn --dry-run`: `memory_estimates` now breaks down per type (3×cc + 2×cod, both cold-start `fallback` @ 2048MB) and `requested_memory_mb: 10240` is byte-identical to the already-shipped flat tactical fix's cold-start output. No regressions in existing spawn/admission test suites.

- **Given/When/Then:** mixed-type demand, policy override, loaded samples -> resolved per-type rows -> `Σ count*estimate`, override short-circuits with zero store I/O, `memory_estimates` present and sorted in the JSON admission response.
- **RED:** override zero-I/O proof, mixed-type sum, cold + calibrated mix, disabled headroom skips entirely.
- **GREEN:** shared resolver called from both `internal/cli/spawn.go` and `internal/robot/spawn.go`.
- **REFACTOR:** one resolver, not two near-duplicate implementations.

### Behavior 8 — Robot monitor opt-in (BLOCKING — new process lifecycle for robot) ✅ complete

Discovered mid-implementation: `newInternalMonitorCommand`/`currentExecutablePath`/`setDetachedProcess`/`shouldStartInternalMonitor` were all private to `internal/cli`, and `internal/robot` cannot import `internal/cli` (would create a cycle — `cli` already imports `robot`). Moved all four to `internal/resilience` (already imported by both) as exported `NewInternalMonitorCommand`, `CurrentExecutablePath`, `SetDetachedProcess` (unix/other build-tagged), `ShouldStartInternalMonitor` — CLI's call sites now delegate to these instead of owning private copies (zero duplication, exactly one implementation each). `RobotMonitorEnabled bool` added to `HeadroomPacingConfig` (default false) + Print/GetValue wiring. `internal/robot/spawn.go` gains `spawnMemoryEstimateCounts`... already done in Behavior 7; this behavior added `shouldStartRobotSessionMonitor` (pure gating predicate) and `startRobotSessionMonitor`/`startRobotSessionMonitorWithDeps` (DI'd for testability — real launches are blocked under `go test` by `ShouldStartInternalMonitor`'s fork-bomb guard, so the launch sequence itself is tested via injected fakes, not a real process). Tests: `TestShouldStartRobotSessionMonitor` (gating truth table), `TestStartRobotSessionMonitorWithDeps` (exact manifest fields + call order + error-swallowing, 4 subtests), plus two real-tmux end-to-end tests (`TestGetSpawn_RobotMonitorDefaultFalseLeavesNoManifest`, `TestGetSpawn_RobotMonitorEnabledDryRunNeverLaunches`) proving no manifest appears in either case. `internal/resilience/monitor_launch_test.go` covers the moved functions directly. Full-repo `go build ./...` and `go vet ./...` both clean. Two pre-existing, unrelated test failures found during regression sweep (`TestResolveModel_WithConfig` — stale hardcoded model string in `agent_spec_test.go`, unrelated file; `TestRespawnWithPaneFilter` — real-agent-readiness timing flake under host load, zero code coupling to this plan) — confirmed via `grep` that neither touches anything this plan modified; not introduced by this work.

- **Given/When/Then:** `RobotMonitorEnabled` false (default) vs. true -> real `robot.GetSpawn` -> false leaves no manifest/monitor (today's behavior, unchanged); true saves a manifest and launches `internal-monitor`, identical to CLI spawn's own call.
- **RED:** default-false parity with today; true launches exactly the same command CLI spawn does; dry-run never launches either way.
- **GREEN:** reuse `resilience.SaveManifest`/`newInternalMonitorCommand` from robot's spawn path behind the flag.
- **REFACTOR:** no duplicated manifest-building logic between CLI and robot.

### Behavior 9 — Real cgroup + live-poll closure (BLOCKING, the one real-infrastructure test) ✅ complete

`TestCaptureOrphanProcessSnapshot_RealCgroupMemoryPeak` in `internal/cli/monitor_test.go`: a real process launched via the actual `memLimitPrefix()`-wrapped `systemd-run --user --scope` command (rendered through `config.GenerateAgentCommand`, no reimplementation) inside a real tmux pane, allocating and holding 64MB (portable `dd|tr`-into-a-shell-variable technique), driven by `runSessionMonitorLoop` with a 300ms real poll interval and full production dependencies (`productionOrphanSnapshotDeps`, `productionOnAgentGenerationEnded`, `productionConfirmedDeathDeps` — no mocks). Asserts a `ScopePeaks` entry appears with a sane peak (32-512MB range) and matching identity/type while the process is alive, then a persisted sample appears in the real file-backed store after `tmux.KillSession` drives confirmed death. Skips with a named reason on hosts without a systemd `--user` session, mirroring `TestMemLimitPrefix_MemoryHighBelowMemoryMax`'s exact gating. **Red-at-seam verified**: temporarily disabled the scope-peak capture call site, reran — failed for the right reason (empty `ScopePeaks`) — then restored and confirmed green again (fresh, uncached run).

Also closed out, as part of finishing this behavior: production wiring of `OnAgentGenerationEnded`/`FinalizeMemorySamples` (deferred since Behaviors 3-4, now that Behaviors 5-7 provide a real store to persist to) — `internal/cli/monitor.go` gained `agentScopePeakToMemorySample`, `productionOnAgentGenerationEnded`, `productionFinalizeMemorySamples`, wired into `runMonitor`'s `loopDeps` and `productionConfirmedDeathDeps` respectively, both backed by `pressure.NewFileMemorySampleStore()`. Unit tests for these conversions/wiring in `monitor_test.go` using a fake `pressure.MemorySampleStore`.

Full verification matrix run clean: `go build ./cmd/ntm`, `go vet` (targeted + whole-repo), `gofmt -l` (clean), all four targeted race-test commands, and `go test -short ./...` — 5 pre-existing failures found (`TestClaudeActivelyWorking_RealCaptures` — missing fixture file; `TestResolveModel_WithConfig` — stale hardcoded model string; `TestRespawnWithPaneFilter` — real-agent-readiness timing flake under host load; `TestGetPaneTarget`; `TestAllAdaptersHaveConsistentInterface`/`TestSLBHealthWhenInstalled`), all confirmed via `git status`/`grep` to be in files this plan never touched, in unrelated subsystems (agent output parsing, swarm pane targeting, tools adapter registry, model-alias config).

- **Given/When/Then:** a real process launched through the actual `memLimitPrefix()`-wrapped command, allocating and holding a known amount of memory -> `runSessionMonitorLoop` driven with a short real poll interval against it -> a `ScopePeaks` entry appears with a sane peak, then a persisted sample appears after the process exits.
- **Classification:** BLOCKING — real systemd scope, real cgroup filesystem, real monitor loop, no mocks, mirroring the orphan-sweep plan's Behavior 7 real-tmux closure pattern and its environment gating (skip with a named reason on hosts without a systemd user session / cgroup v2, never silently pass).
- **Cleanup:** kill the helper and its scope unconditionally via `t.Cleanup`.

## Testing Strategy

- Unit/fixture tests for Behaviors 1, 2, 3, 6 — pure or fixture-injected, no real OS state.
- Behaviors 4, 5, 7, 8 are BLOCKING in the sense of crossing an existing effect-order or process-lifecycle boundary, but stay within this codebase's normal subprocess-test patterns already used elsewhere (`internal/history`'s concurrent-append test, `internal/cli`'s `confirmedDeathDeps` injection tests) — no new test infrastructure needed.
- Behavior 9 is the only test requiring a real systemd user session and real cgroup v2, gated the same way `TestMemLimitPrefix_MemoryHighBelowMemoryMax` already is (`internal/config/templates_test.go`).

## Automated Verification Matrix

Using tools confirmed available in this environment (per the prior orphan-sweep handoff, `rch`/`golangci-lint`/`goimports`/`ubs` are not installed here — fall back to direct commands, do not block on them):

```text
go build ./cmd/ntm
go vet ./internal/process/... ./internal/pressure/... ./internal/cli/... ./internal/robot/...
gofmt -l <every changed file>
go test -race ./internal/process/... -run 'Cgroup'
go test -race ./internal/cli/... -run 'ScopePeak|GenerationEnded|ConfirmedDeath|Monitor'
go test -race ./internal/pressure/... -run 'EstimateAgentMemMB|AgentMemory'
go test -race ./internal/robot/... -run 'Spawn|Admission|RobotMonitor'
go test -short ./...  # expect no new failures beyond the already-documented pre-existing ones
```

## Definition of Done

1. ✅ Behaviors 1-9 green, including the real-cgroup closure test (or a named, honest skip reason on an environment without a systemd user session). This session's host has a systemd `--user` session, so the closure test ran for real (not skipped) and was verified red-at-seam.
2. ✅ A fresh host with zero samples produces byte-identical admission behavior to the already-shipped tactical fix (Behavior 6's cold-start case) — verified both by unit test and live `--robot-spawn --dry-run` (`requested_memory_mb: 10240` for 5 agents × 2048MB, matching the tactical fix exactly).
3. ✅ No agent's launch command changed. `git diff` touches no line of `internal/config/templates.go`'s command-rendering output for `Claude`/`Codex`/`Gemini`/custom templates — confirmed: the only edit to `internal/config/` in this plan's work is the new `RobotMonitorEnabled` field in `pacing.go` and its `Print`/`GetValue` wiring in `config.go`; `templates.go` is untouched by this plan (it was only touched by the earlier, separately-committed tactical fix).
4. ✅ `go build`/`go vet`/`gofmt`/targeted `go test -race`/`go test -short ./...` all pass with no new failures (5 pre-existing, unrelated failures identified and confirmed out-of-scope — see Behavior 9's notes).
5. Nothing beyond the already-committed tactical fix (`9acad2ad`) is committed yet — Behaviors 1-9's implementation is uncommitted working-tree changes, per this repo's conservative default. Ready to commit on request.
