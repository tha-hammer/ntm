## Plan Review Report: thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation-REVIEW.md — Adaptive Per-Agent Memory Estimation

### Review Summary

**Verdict: Needs Major Revision.** The percentile estimator is a reasonable pure reduction, and preserving the explicit operator override is correct. The evidence lifecycle is not implementable as written, however. `tmux.Pane.PID` is the pane shell PID, while `memLimitPrefix()` scopes the command launched beneath that shell; the retained monitor snapshot discards the pane/type-to-descendant association before confirmed death. Confirmed death also occurs only after roughly 25 seconds of missing-session observations, by which point an empty transient scope may already have been removed. The result would be missing, survivor-biased, or wrongly attributed samples.

The automatic-feedback safety rationale also needs correction: clamping an estimate to an individual `MemoryMax` ceiling limits overestimation, but does not protect the host from aggregate **underestimation**. Until this is resolved, automatic mode must not lower the already-shipped 2048 MB admission baseline.

| Category | Status | Issues Found |
|----------|--------|--------------|
| Contracts | ❌ | 10 critical, 5 warnings |
| Interfaces | ❌ | 5 critical, 5 warnings |
| Promises | ❌ | 7 critical, 6 warnings |
| Data Models | ❌ | 7 issues |
| APIs | ❌ | 6 issues |
| CodeCleanup Gates | ❌ | 6 issues |

External semantics were checked against primary documentation. The kernel defines `memory.peak` as the cgroup-and-descendants maximum since cgroup creation (or an FD-local reset), but also permits an empty cgroup to be removed. systemd defines a scope's lifecycle by the presence of at least one process, not by one distinguished main process. Those semantics support live scope discovery and bounded finalization, not first discovery after confirmed session death. See [Linux cgroup v2 memory controller documentation](https://www.kernel.org/doc/html/latest/admin-guide/cgroup-v2.html) and [systemd scope documentation](https://man7.org/linux/man-pages/man5/systemd.scope.5.html).

### Contract Review

#### Well-Defined

- ✅ Enforcement and estimation remain separate. `PerAgentMemLimitMB()` is the `MemoryMax` backstop, while `PerAgentExpectedMemMB()` owns normal-usage admission sizing (`internal/config/templates.go:102-136`).
- ✅ `pressure.EvaluateSpawnAdmission` is already a pure decision boundary that accepts aggregate requested memory (`internal/pressure/pressure.go:130-152`, `196-200`).
- ✅ The explicit positive override and zero-as-unset behavior are concrete and tested (`internal/config/templates.go:126-136`, `internal/config/templates_test.go:542-567`).
- ✅ The current confirmed-death order and manifest-deletion promise are explicit: ended event, synchronous stop/join, optional reap/log, panic-isolated summary, then manifest deletion (`internal/cli/monitor.go:591-636`).
- ✅ `ProcessIdentity{PID, CreateTimeMillis}` and fresh equality checks provide an established identity-safe pattern (`internal/process/liveness.go:75-121`).

#### Missing or Unclear

- ❌ **The universal cgroup premise is false.** Only the default Claude, Codex, and Gemini templates include `memLimitPrefix`; Antigravity, Ollama, Cursor, Windsurf, Aider, Opencode, plugins, and customized templates may be unscoped (`internal/config/templates.go:310-340`). The plan must define evidence eligibility and fallback for unscoped agents.
- ❌ **Pane ownership and scope ownership are conflated.** `tmux.Pane.PID` is explicitly the shell PID (`internal/tmux/session.go:58-73`). The rendered `systemd-run --scope` command is sent into that shell (`internal/config/templates.go:187-199`, `internal/cli/spawn.go:2243-2318`), so reading the shell PID's cgroup does not identify the agent scope.
- ❌ **The live mapping is discarded.** `orphanProcessSnapshot` retains unkeyed roots plus one flat descendant-identity set (`internal/cli/monitor.go:244-250`). `manifestOwnedPaneRoots` removes `PaneID` and type association (`internal/cli/monitor.go:269-295`), and pane-shell roots are excluded from candidates (`internal/cli/monitor.go:297-303`).
- ❌ **Confirmed death is too late for first discovery.** Production confirms death after five misses at five-second intervals (`internal/cli/monitor.go:366-380`, `575-584`). An agent scope can disappear when the agent exits back to its still-live pane shell, or during that miss window.
- ❌ **A newly captured identity is not an expected identity.** A bare-PID reader cannot tell whether the PID was reused before the call. It must receive a previously captured expected `ProcessIdentity`, compare a fresh identity before the read, and revalidate after the read.
- ❌ **Cgroup parsing is under-specified.** The plan must select only `0::/path`, define the unified mount source, reject traversal/malformed/overflow content, decide whether zero is valid evidence, and retain a structured skip reason for logs.
- ❌ **The JSON ring buffer lacks a cross-process transaction.** Every CLI session can run a detached monitor (`internal/cli/spawn.go:2508-2573`); two monitors can lose each other's read-modify-write updates. `TimelinePersister` is only instance-locked (`internal/state/timeline_persist.go:90-99`), whereas history uses a process-safe lock (`internal/history/storage.go:48-104`).
- ❌ **Host identity is not owned.** `SanitizeMachineFingerprint` hashes caller input but does not acquire a stable ID (`internal/pressure/profile_trend.go:127-147`). The plan must define source, hashing, unavailable behavior, and container/non-Linux semantics.
- ❌ **Estimator math is incomplete.** The floor, p90 convention, MiB rounding, invalid-bound behavior, accepted-sample filter, exact confidence gate, and overflow behavior are all unspecified.
- ❌ **Best-effort behavior lacks an effect contract.** Memory capture is absent from `confirmedDeathDeps`; its order, timeout, panic boundary, independence from `reap_orphans_on_exit`, and returned-error behavior are undefined (`internal/cli/monitor.go:591-636`).

#### Recommendations

- Discover the actual scoped descendant while tmux is live and retain a canonical `PaneID -> AgentType -> ProcessIdentity -> CgroupRef` association. Fail closed on ambiguous scope selection.
- Capture a last-good monotonic peak while the scope exists; at lifecycle finalization, attempt a last identity-safe read and retain the maximum. Do not make confirmed session death the first discovery point.
- Make unscoped/unsupported agents normal fallback cases with an observable reason.
- Specify a process-safe, bounded, atomic, versioned store transaction and an explicit fail-open policy for every optional evidence failure.

---

### Interface Review

#### Well-Defined

- ✅ CLI normalization supplies a type for every concrete `FlatAgent` (`internal/cli/spawn.go:391-428`, `700-718`).
- ✅ The repository already has a central canonical agent type and alias normalizer in `internal/agent`, and `internal/tmux` aliases it (`internal/agent/types.go:11-61`, `internal/tmux/session.go:38-52`).
- ✅ `confirmedDeathDeps` is the correct dependency-injection style for ordered side-effect tests (`internal/cli/monitor.go:591-603`).
- ✅ `SpawnOutput.Admission` already carries the admission decision in robot JSON (`internal/robot/spawn.go:53-71`).

#### Missing or Unclear

- ❌ The proposed `(pid) -> (bytes, ok)` interface cannot enforce PID-reuse safety and hard-codes live `/proc`/cgroup roots, contradicting the fixture-test strategy.
- ❌ `handleConfirmedSessionDeath` receives neither the manifest nor typed live scope state, and `confirmedDeathDeps` has no capture/store callback (`internal/cli/monitor.go:591-614`).
- ❌ `--robot-spawn` has a separate lifecycle: it launches panes and returns without saving a manifest or starting the internal monitor (`internal/robot/spawn.go:434-578`). The only current evidence producer exists in CLI spawn (`internal/cli/spawn.go:2508-2573`).
- ❌ Current admission interfaces carry only aggregate integers; there is no per-type input/output DTO for count, estimate, source, confidence, sample count, or fallback reason (`internal/pressure/pressure.go:130-178`).
- ❌ The store constructor, path injection, `Load`/`AppendBatch` methods, lock behavior, and flush-before-process-exit semantics are not defined.
- ⚠️ Runtime estimation does not belong inside `internal/config`. Config should expose override/default values; evidence filtering, confidence gating, and estimate resolution should remain in a shared lower package such as `internal/pressure`.
- ⚠️ `EstimateAgentMemMB` returns too little for its downstream observability requirement. A result struct should include estimate, confidence, accepted count, percentile, and source-selection metadata.
- ⚠️ `memLimitPrefix()` is unexported, so a closure test in `internal/process` cannot reuse it. Put the test in package `config` or define a narrow production seam (`internal/config/templates.go:187-200`).

#### Recommended Interfaces

```go
type AgentMemoryDemand struct {
    AgentType agent.AgentType
    Count     int
}

type AgentMemoryEstimate struct {
    AgentType         agent.AgentType `json:"agent_type"`
    Count             int             `json:"count"`
    EstimateMB        uint64          `json:"estimate_mb"`
    RequestedMemoryMB uint64          `json:"requested_memory_mb"`
    Source            string          `json:"source"` // override|calibrated|fallback
    FallbackReason    string          `json:"fallback_reason,omitempty"`
    Confidence        float64         `json:"confidence"`
    SampleCount       int             `json:"sample_count"`
}

type AgentScopeSnapshot struct {
    PaneID      string
    AgentType   agent.AgentType
    Identity    process.ProcessIdentity
    CgroupPath  string
    LastPeak    uint64
    LastReadAt  time.Time
}
```

- Define `ReadCgroupMemoryPeak(ctx context.Context, expected ProcessIdentity) (uint64, bool)` plus private injectable filesystem/identity dependencies. Revalidate identity after the peak read.
- Define an injectable `MemorySampleStore` with constructor/path/retention options, `Load`, and synchronous bounded `AppendBatch`; hold a process-safe lock across read-prune-atomic-write.
- Extend `SpawnAdmissionInput` and `SpawnAdmission` with a defensively copied, deterministically sorted `[]AgentMemoryEstimate`. Derive or validate the aggregate in one shared reducer.
- Use `.Canonical()` before persistence, grouping, lookup, and output.

---

### Promise Review

#### Well-Defined

- ✅ The monitor loop invokes confirmed death once and returns; context cancellation exits without death effects (`internal/cli/monitor.go:425-428`, `537-584`).
- ✅ `Monitor.Stop` cancels and joins its loop and tracked goroutines (`internal/resilience/monitor.go:240-260`).
- ✅ The explicit override can short-circuit learning without changing the current config contract.
- ✅ The direct real-cgroup reader test is useful as one boundary test, provided it targets the actual process inside the scope.

#### Missing or Unclear

- ❌ **Evidence is missing-not-at-random.** Normal exits and earlier auto-restart generations disappear before session death, while only long-lived survivor scopes remain observable. That biases p90 rather than merely reducing sample count.
- ❌ **Robot workloads never produce evidence.** A robot-heavy host can consume fallback/learned estimates but cannot contribute samples.
- ❌ **The automatic-safety argument is invalid.** An individual upper clamp does not prevent aggregate underestimation. A low p90 can admit a burst the static 2048 MB baseline would have refused.
- ❌ **Concurrent writers can lose samples.** Atomic rename prevents torn files but not lost read-modify-write updates.
- ❌ **Optional telemetry can become a control-plane dependency.** Host-ID, lock, parse, corrupt file, oversized file, version, cancellation, and timeout failures have no defined fail-open path.
- ❌ **The closure test stops at the reader.** It does not prove pane-to-scope selection, production retention, persistence, robot lifecycle parity, or subsequent admission consumption.
- ❌ **No bounded completion promise exists.** “Non-blocking persistence” conflicts with a short-lived monitor process: an unjoined goroutine may be lost, while an unbounded synchronous lock can delay manifest deletion.
- ⚠️ Samples lack an idempotency key, so retries or duplicate monitors can inflate confidence.
- ⚠️ Per-key N does not bound total keys or total file size.
- ⚠️ The real helper needs readiness, context deadline, exact in-scope PID handoff, unconditional termination, and `Wait`/join—never a fixed sleep.

#### Recommendations

- Define automatic mode as never below the tactical baseline unless an operator explicitly opts into lowering it. Consider `max(defaultPerAgentExpectedMemMB, ceil(p90 * namedSafetyFactor))` while evidence quality is being established.
- Treat OOM-killed/censored scope runs explicitly; do not silently discard the very runs that establish unsafe demand.
- Load one immutable store snapshot per admission and reuse it for every type. Check override before HostID or store I/O so override truly performs zero calibration I/O.
- Bound capture/load/store operations with context deadlines. Errors, panics, timeouts, corruption, and lock contention must log and fall back without changing reap/summary/delete completion.
- Add a blocking real-tmux production closure: render the production template, discover the true scoped child, persist a typed sample, then verify a subsequent robot dry-run exposes and consumes it.

---

### Data Model Review

#### Well-Defined

- ✅ A per-agent observation needs agent type, peak bytes, observation time, and host partitioning.
- ✅ Bounded per-key retention is the right starting shape for a frequently read store.
- ✅ Per-type admission detail belongs alongside the existing aggregate admission response.

#### Missing or Unclear

- ❌ `MemorySample` has no schema version or deterministic `SampleID`. Add a key derived from host, session, pane, and scope identity/generation so append is idempotent.
- ❌ `AgentType string` permits parallel buckets (`claude`, `cc`, whitespace, empty). Persist canonical `agent.AgentType` tokens; define empty/unknown/plugin behavior.
- ❌ `PeakBytes int64` does not match the non-negative kernel counter. Use `uint64` and reject malformed/overflowing text before construction.
- ❌ `HostID` acquisition/redaction/unavailable semantics are missing. Never merge failures under an `unknown` shared bucket.
- ❌ “Most recent N” lacks an exact eviction order. Define append sequence or normalized UTC time, clock-skew behavior, and duplicate handling.
- ❌ Per-key retention is not a global bound. Add maximum records, keys, bytes, and accepted input file size.
- ❌ The `SpawnAdmission` detail schema is not locked. Define an always-present, type-sorted `memory_estimates` array when admission memory sizing is checked; use `[]`, not absent/null, when checked and no rows exist.
- ⚠️ Define store schema migration and corruption behavior. Unsupported/corrupt data must be quarantined only with explicit deletion permission; otherwise leave it untouched and fall back.

#### Recommendations

- Version the store and use file mode `0600` with atomic replacement.
- Store a stable run/scope identifier even though admission remains aggregated only by host and type.
- Define deterministic JSON ordering for reproducible tests and reviews.

---

### API Review

#### Well-Defined

- ✅ No new public command or endpoint is necessary; the feature can extend existing spawn admission output.
- ✅ The global override key can retain its current spelling and positive-value precedence.
- ✅ `--robot-spawn --dry-run` is the correct external inspection surface.

#### Missing or Unclear

- ❌ The robot-stable JSON names, types, enum values, ordering, and presence semantics for per-type estimates are absent.
- ❌ Robot count fields use a different input shape from CLI `FlatAgent` values. Both must adapt into the same shared `[]AgentMemoryDemand` reducer.
- ❌ The plan names only `calibration|fallback`, but observable source must include `override`; fallback reasons need stable tokens such as `cold_start`, `low_confidence`, `store_unavailable`, `host_id_unavailable`, and `unsupported_or_unscoped`.
- ❌ Behavior is undefined when headroom is disabled or `MemAvailable` is unavailable. State whether `memory_estimates` is absent because sizing was not checked or present as `[]`; follow repository absent-vs-empty semantics consistently.
- ❌ Checked arithmetic is missing. Per-type multiplication, aggregate addition, and conversion to the current `int` field must fail closed on overflow.
- ⚠️ CLI and robot parity needs a contract test: identical demands/evidence/config must yield identical aggregate estimates and source rows.

#### Recommendations

- Lock the JSON DTO above and include it in both dry-run admits and dry-run refusals.
- Keep `EvaluateSpawnAdmission` pure. Store and host reads happen before it; it receives typed, already-resolved details and validates/derives the aggregate.
- Add JSON golden/round-trip tests for override, calibrated, cold-start, unscoped, store-error, disabled, and refusal cases.

---

### CodeCleanup Plan-Hygiene Review

#### Well-Defined

- ✅ The pure percentile reducer can keep conditions as questions and mutation out of control expressions.
- ✅ The existing confirmed-death path is flat and has explicit relative effect order (`internal/cli/monitor.go:614-636`).
- ✅ The plan correctly protects `PerAgentMemLimitMB`/systemd values from accidental behavior changes.

#### Missing or Unclear

- ❌ **Maintainability Recovery:** putting runtime sample loading/filtering/fallback inside a new `config.PerAgentExpectedMemMBForType` stacks persistence and statistical policy onto the wrong abstraction. Config should expose configuration; a shared estimator service should own runtime evidence.
- ❌ **No Side Effects in Conditionals:** existing admission embeds live I/O in `if avail, ok := pressure.AvailableMemoryMB(); ok` (`internal/cli/spawn.go:1698-1707`, `internal/robot/spawn.go:142-146`). The amended plan must explicitly hoist host/store reads into named statements before pure checks; do not add more file/lock calls inside `if` initializers.
- ❌ **Control Expression Discipline:** evidence eligibility, scope ambiguity, identity mismatch, final-read fallback, confidence fallback, and override precedence need a bounded state/result type, not a growing open-ended conditional chain.
- ❌ **Named Constants:** p90, retention N, confidence gate, baseline floor, safety factor, MiB divisor, lock/read/write deadlines, maximum keys/records/file bytes, and source/reason strings must be named and single-sourced.
- ⚠️ **Never Nesting:** the live observation and confirmed-death paths should use flat guards for unsupported, ambiguous, stale, and unavailable evidence while preserving the existing ended/stop/reap/summary/delete relative order.
- ⚠️ **No Mutation in Control Expressions:** checked sums and counters must be explicit statements; no assignment or increment belongs inside admission/capture predicates.

#### Recommendations

- Introduce small result types such as `ScopeResolution`, `EstimateResult`, and `StoreLoadResult` with stable reason enums; keep callers as flat orchestration.
- Perform effecting operations serially, store their results, then use pure predicates.
- Preserve short-circuit semantics deliberately: an explicit override returns before HostID/store access; failed optional reads return fallback before aggregate evaluation.

---

### Critical Issues (Must Address Before Implementation)

1. **Wrong and lost scope identity**: the plan treats the pane shell PID as the agent scope and discards pane/type-to-descendant mapping.
   - Impact: wrong-type evidence or no evidence.
   - Recommendation: discover and retain the actual scoped descendant identity while live, keyed by canonical pane/type.

2. **Capture is too late and survivor-biased**: transient scopes can disappear before confirmed session death, especially across normal exit and auto-restart.
   - Impact: non-converging or statistically biased estimates.
   - Recommendation: retain live last-good peaks or add an in-scope finalizer; use confirmed death only to finalize/persist retained evidence.

3. **PID-reuse safety is incomplete**: a bare PID with a fresh self-generated identity does not authenticate the original agent.
   - Impact: unrelated process memory can be attributed to an agent type.
   - Recommendation: accept a previously captured expected identity and revalidate around the read.

4. **Robot spawn has no evidence-producer lifecycle**.
   - Impact: robot-first hosts remain permanently cold.
   - Recommendation: extract shared manifest/monitor lifecycle below CLI and robot, or explicitly narrow the feature and plan.

5. **Automatic mode can lower aggregate safety**: the ceiling clamp does not bound underestimation.
   - Impact: more agents may be admitted than the static baseline allowed, risking host OOM.
   - Recommendation: use a named floor at least equal to the 2048 MB baseline unless lowering is explicitly operator-approved.

6. **Persistence is not process-safe or bounded**.
   - Impact: lost concurrent samples, inflated/incorrect confidence, or blocking control-plane behavior.
   - Recommendation: cross-process lock the full transaction, atomic replace, version, globally bound, and apply a context deadline.

7. **Estimator and HostID contracts are nondeterministic**.
   - Impact: CLI/robot divergence and accidental host/type mixing.
   - Recommendation: lock HostID provider/redaction, exact p90/rounding/filter/gate rules, canonical types, and checked arithmetic.

8. **Failure isolation and robot schema are missing**.
   - Impact: optional telemetry can block manifest deletion, and operators cannot diagnose fallback/calibration decisions.
   - Recommendation: add panic/error/timeout-safe effect seams and a stable, sorted `memory_estimates` response.

9. **The blocking closure test does not exercise the production chain**.
   - Impact: `/proc` parsing can pass while real pane-to-scope attribution and learned admission remain broken.
   - Recommendation: add real tmux + production template + persistence + subsequent robot dry-run closure.

### Suggested Plan Amendments

```diff
# Locked Decision §1 — evidence lifecycle

- At confirmed session death, derive each agent's pane shell PID and read its memory.peak.
- Do not capture evidence anywhere else.
+ While tmux is live, resolve each manifest-owned pane to the actual distinct
+ systemd-scoped descendant and retain {PaneID, canonical AgentType,
+ expected ProcessIdentity, validated CgroupRef, last-good PeakBytes}.
+ Treat unscoped/unsupported/ambiguous panes as explicit fallback reasons.
+ Update the last-good monotonic peak while the scope exists and capture every
+ restart scope generation. At lifecycle finalization, attempt one final
+ identity-safe read and persist max(last-good, final).

# Locked Decision §2 — store

+ Define a versioned MemorySample with SampleID/run identity and uint64 PeakBytes.
+ Define a 0600 XDG store whose AppendBatch holds a cross-process lock across
+ read -> validate/migrate -> deduplicate -> sort/prune -> atomic replace.
+ Name per-key and global record/key/byte limits and a maximum input size.
+ Make load/append synchronous-but-bounded, best-effort, and panic/error-safe.
+ On HostID/store/parse/version/lock/timeout failure, log and use fallback.

# Locked Decision §3 — estimator

+ Lock nearest-rank p90, ascending sort, ceil(bytes/1MiB), valid-sample rules,
+ exact confidence curve/gate, invalid-bound behavior, and checked arithmetic.
+ Automatic mode's named floor is at least defaultPerAgentExpectedMemMB (2048 MB).
+ Config owns override/default lookup only; pressure owns evidence reduction and
+ returns AgentMemoryEstimate with source, reason, confidence, and sample count.

# Locked Decision §4/§5 — shared admission and API

+ Adapt CLI FlatAgent and robot counts into one []AgentMemoryDemand helper.
+ Extract a shared evidence-producing monitor lifecycle callable by both CLI
+ and robot without an import cycle.
+ Extend SpawnAdmission with a deterministic []AgentMemoryEstimate JSON field
+ and stable override|calibrated|fallback plus fallback-reason tokens.

# Confirmed-death order

+ Evidence finalization is independent of reap_orphans_on_exit and cannot alter
+ the existing ended -> stop -> optional reap/log -> summary -> delete relative order.
+ Tests inject read/store error, panic, timeout, cancellation, and lock contention
+ and prove summary/delete still run and DeleteManifest remains the returned error.

# Blocking closure

- Test only a direct reader against a hand-launched real scope.
+ Keep the direct reader test, and add a real-tmux production closure that renders
+ the production template, identifies the actual scoped child, records a typed
+ sample, and proves a subsequent mixed-type robot dry-run consumes and explains it.
```

### Review Checklist

#### Contracts

- [ ] Component boundaries are clearly defined
- [ ] Input/output contracts are specified
- [ ] Error contracts enumerate all failure modes
- [ ] Preconditions and postconditions are documented
- [ ] Invariants are identified

#### Interfaces

- [ ] All public/internal cross-package methods have locked signatures
- [x] Naming can follow existing canonical agent conventions
- [ ] CLI and robot share one reducer and evidence lifecycle
- [ ] Store and OS test seams are explicit
- [x] Visibility can remain internal

#### Promises

- [ ] Automatic mode has a valid aggregate-safety floor
- [ ] Capture covers normal exit and restart generations
- [ ] Timeout/cancellation behavior is bounded
- [ ] Resource cleanup and flush/join behavior are specified
- [ ] Idempotency and ordering guarantees are documented

#### Data Models

- [ ] All fields, types, enum values, and required/optional semantics are locked
- [ ] Host/type/scope relationships are documented
- [ ] Versioning and migration/corruption behavior are defined
- [ ] Serialization and global size bounds are specified
- [ ] Sample identity/deduplication is defined

#### APIs

- [ ] Robot response schema and presence semantics are specified
- [ ] Error/fallback reasons are documented
- [x] No new authentication surface is introduced
- [ ] CLI/robot parity is tested
- [ ] Overflow and disabled/unavailable behavior are defined

#### CodeCleanup Gates

- [ ] Conditionals are pure questions; effecting operations are serialized outside them
- [ ] Mutation is absent from control expressions
- [ ] Guard clauses flatten bad paths without changing effect/error order
- [ ] Statistical, retention, timeout, and schema literals have named constants
- [x] Existing systemd/config protocol values remain unchanged
- [ ] Runtime estimation is owned by the correct abstraction

### Approval Status

- [ ] **Ready for Implementation** - No critical issues
- [ ] **Needs Minor Revision** - Address warnings before proceeding
- [x] **Needs Major Revision** - Critical lifecycle, attribution, safety, persistence, parity, and API contracts must be resolved first
