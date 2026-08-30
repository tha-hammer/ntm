# Adaptive Per-Agent Memory Estimation TDD Implementation Plan

## Overview

`ntm spawn`/`--robot-spawn` refuse a burst spawn when `requestedAgents * <per-agent estimate>` exceeds live `MemAvailable` (`internal/pressure/pressure.go:200`, wired at `internal/cli/spawn.go:1687` and `internal/robot/spawn.go:131`). Until the companion tactical fix landed in this same session, `<per-agent estimate>` was `config.PerAgentMemLimitMB()` — the systemd `MemoryMax` *enforcement ceiling* (25% of host RAM, clamped to 16384MB, `internal/config/templates.go:87-99`) — not a realistic usage figure. That made 5 agents require 80GB of free memory on any host with >=64GB RAM, regardless of what agents actually use. The tactical fix replaced it with `config.PerAgentExpectedMemMB(cfg)`, a static placeholder constant (`internal/config/templates.go`, `defaultPerAgentExpectedMemMB = 2048`), overridable via `spawn_pacing.headroom.per_agent_expected_mem_mb`.

A static constant is still a guess, and it's the same guess for a `claude` agent and a `codex` agent even though the two CLIs have different memory profiles (`internal/config/pacing.go:48-70`'s `AgentPacingConfig` already treats them with different concurrency/rate defaults). This plan replaces the static constant with an estimate learned from what agents on *this host* have actually used, following the same "evidence in, bounded recommendation out" shape `internal/pressure/calibration.go` already established for pressure thresholds — extended to per-agent-type memory sizing, sourced from real systemd-scope cgroup accounting instead of synthetic benchmark evidence.

## Review Disposition

The 2026-08-13 review found nine blocking defects in the original plan: it attributed the pane shell instead of the agent scope, discovered scopes after they could disappear, did not authenticate an expected process identity, omitted robot's producer lifecycle, allowed automatic learning below the 2048 MiB safety baseline, specified neither a process-safe nor globally bounded store, left HostID/statistics/overflow nondeterministic, omitted failure and robot-schema contracts, and stopped closure at a direct reader test.

This revision closes every critical, warning, data-model, API, and cleanup finding. There are no unresolved design questions. The primary correction is that normal evidence is produced by a finalizer that remains *inside* each transient systemd scope. The existing session monitor is a generation-aware safety net, not the authoritative normal-exit producer.

## Current-State Facts That Constrain the Design

- `tmux.Pane.PID` is the pane shell PID (`internal/tmux/session.go:58-73`). The shell launches `systemd-run --user --scope`, so the shell is not the agent-scope identity and its cgroup must never be sampled as agent evidence.
- Only the default Claude, Codex, and Gemini templates include `memLimitPrefix` (`internal/config/templates.go:310-344`). Antigravity, Ollama, Cursor, Windsurf, Aider, Opencode, plugin, and custom templates are unsupported/unscoped unless their parsed template explicitly opts into both the production scope and runner functions.
- `memLimitPrefix()` must remain byte-for-byte behaviorally unchanged: `MemoryHigh` and `MemoryMax` continue to enforce the current per-pane limits. No unit name is known before launch.
- Cgroup v2 `memory.peak` is monotonic for the cgroup lifetime, but a successful transient scope is removed as soon as it is empty. A read-only probe confirmed that even a held `memory.peak` file descriptor returns `ENODEV` after scope removal. Polling or confirmed-session-death discovery therefore cannot be authoritative.
- `ProcessIdentity{PID, CreateTimeMillis}` and `CaptureProcessIdentity` in `internal/process/liveness.go` are the established PID-reuse defense. A cgroup read must receive a previously captured expected identity and validate it before and after the read; capturing a new identity inside a bare-PID API is insufficient.
- `orphanProcessSnapshot` currently retains untyped roots and a flat candidate union (`internal/cli/monitor.go:234-364`). Evidence tracking needs a keyed pane/type/generation map while preserving the flat derived set for the existing reaper.
- Production confirms a missing session only after five five-second misses (`internal/cli/monitor.go:366-380,575-584`). Normal exits and auto-restart generations can vanish long before then.
- CLI spawn saves a manifest and starts `internal-monitor` (`internal/cli/spawn.go:2508-2573`). `robot.GetSpawn` launches panes but does neither (`internal/robot/spawn.go:434-578`), and it renders commands too early with empty per-pane variables (`internal/robot/spawn.go:918-951`). CLI, robot CLI, and REST must share the lower in-scope recorder; the CLI monitor remains an additional safety net.
- Resilience restart replays the saved rendered command (`internal/resilience/monitor.go`). A runner embedded in that command naturally creates a fresh scope generation and sample on every replay.
- `EvaluateSpawnAdmission` is the correct pure decision boundary (`internal/pressure/pressure.go`). CLI and robot currently perform separate live-memory effects and flat multiplication; both must adapt into one typed resolver.
- `internal/history/storage.go` plus `lock_unix.go` provides the process-lock precedent; `util.AtomicWriteFile` provides temp/sync/rename precedent. Neither alone satisfies the required context-bounded, full read-modify-write transaction with parent-directory sync.
- The existing confidence curve is `min(1, log1p(n)/log1p(100))` with a 0.35 gate (`internal/pressure/calibration.go`). Four complete samples are below the gate and five are above it.

## Desired End State and Safety Invariants

Given completed scoped runs on this host, a later CLI or robot spawn sizes each canonical agent type from its own durable evidence and reports the exact derivation. Given no trustworthy evidence, unsupported/unscoped launch configuration, unavailable host identity, or an unreadable store, it uses the shipped 2048 MiB baseline. A positive operator override remains exact and performs no HostID/store I/O.

The implementation is accepted only while all of these invariants hold:

1. Automatic sizing never returns less than `DefaultPerAgentExpectedMemMB == 2048` MiB. Only an explicit positive override may lower it.
2. Enforcement is unchanged: `PerAgentMemLimitMB()`, `MemoryHigh`, `MemoryMax`, and the existing `memLimitPrefix()` protocol do not change.
3. Evidence is keyed by canonical type, application-specific HostID, and one authenticated scope generation. Pane-shell/root cgroups and ambiguous scopes never become samples.
4. Every recording-capable CLI/robot spawn and resilience replay invokes the same recorder. When runner/finalizer initialization and durable append succeed, it produces terminal evidence on normal exit; a failed generation is observable and contributes no fabricated sample. Live polling is only censored backup evidence.
5. Duplicate/fallback producers cannot inflate confidence; a deterministic sample ID merges one generation to one record.
6. Store load/append is synchronous, process-safe, context-bounded, globally bounded, atomic, and leaves corrupt/unknown data untouched.
7. Optional evidence failures fail open to the 2048 MiB baseline and never change agent exit status or confirmed-death cleanup. Invalid demand/bounds/arithmetic fail closed.
8. CLI, robot CLI, and REST use the same in-scope recorder and admission resolver. The existing detached monitor remains a CLI-only safety net; robot does not need it to produce normal terminal evidence.

## Ownership and Dependency Direction

This is genuinely new, coherent functionality, so one new package is justified instead of scattering storage and lifecycle policy through config:

- `internal/process` owns Linux/cgroup-v2 OS primitives only: mount and membership parsing, expected-process validation, cgroup directory identity, peak/limit/event reads, and non-Linux unavailable stubs. It imports no higher NTM package.
- `internal/agentmemory` owns evidence-domain types, scope/sample IDs, HostID, the versioned store, runner/finalizer, and the typed generation tracker. It imports only `internal/agent`, `internal/process`, `internal/util` where appropriate, and the standard library. It does not import config, pressure, resilience, tmux, CLI, or robot.
- `internal/pressure` owns canonical demands, pure estimator/resolver policy, checked aggregation, admission status, and robot-stable estimate DTOs. It may import `internal/agent`, `internal/agentmemory`, and pure config constants; it must not import CLI, robot, or resilience.
- `internal/config` owns only the static automatic floor, explicit override lookup, enforcement ceiling, template parsing/rendering, and the shared headroom-enabled predicate. It performs no HostID, store, percentile, or runtime evidence I/O.
- `internal/resilience` retains CLI's manifest/restart monitor. Its saved rendered command replays the same runner; it may use `agentmemory` but must not import CLI or robot.
- CLI and robot are adapters. They build the same `[]pressure.AgentMemoryDemand`, invoke the same resolver/evaluator, and render the same per-pane recorder metadata. CLI additionally starts the existing monitor. CLI may continue to import robot; robot must never import CLI.

## Locked Contracts

### 1. Configuration and launch eligibility

Keep runtime policy out of config:

~~~go
const DefaultPerAgentExpectedMemMB uint64 = 2048

func PerAgentExpectedMemOverrideMB(cfg *Config) (uint64, bool)
func PerAgentExpectedMemMB(cfg *Config) uint64

type AgentMemoryTemplateEligibility string
const (
    AgentMemoryTemplateCapable      AgentMemoryTemplateEligibility = "recording_capable"
    AgentMemoryTemplateUnsupported  AgentMemoryTemplateEligibility = "unsupported_or_unscoped"
)

type AgentTemplateCapabilities struct {
    MemoryScopeAction bool
    AgentMemoryRunnerAction bool
}

func InspectAgentTemplate(tmpl string) (AgentTemplateCapabilities, error)
func ProspectiveAgentMemoryCapability(
    tmpl string,
    localExecution bool,
) AgentMemoryTemplateEligibility

type AgentCommandRenderResult struct {
    Command string
    MemoryScoped bool
    MemoryRecorderStarted bool
    MemoryRecorderReason string
}

func GenerateAgentCommandForLaunch(
    tmpl string,
    vars AgentTemplateVars,
) (AgentCommandRenderResult, error)

// Template action; returns empty unless executable and encoded metadata validate.
// Exact token shape:
// <shellQuote(NTMExecutable)> internal-agent-memory-run
// --metadata=<shellQuote(AgentMemoryRunnerMetadata)> --
func agentMemoryRunnerPrefix(vars AgentTemplateVars) string
~~~

- Zero means automatic mode. A positive override is exact, even below 2048 or above `PerAgentMemLimitMB()`, because it is explicit operator policy. Negative remains a config error.
- Override resolution happens after live `MemAvailable` succeeds but before HostID/store access. Tests prove zero HostID/store calls.
- `InspectAgentTemplate` parses without executing or requiring a PaneID. Capability requires both action nodes in the same top-level command sequence with `memLimitPrefix` preceding `agentMemoryRunnerPrefix`; incompatible conditional branches or reversed order are unsupported. It never scans rendered text.
- `ProspectiveAgentMemoryCapability` is the pre-admission/dry-run check: it combines the parsed shape with local execution and a nonempty production systemd prefix. Default Claude/Codex/Gemini are capable on a local supported host; other defaults are not.
- Rename the demand field to `RecordingCapable`. Admission uses this prospective property plus local/systemd availability, so dry-run works before panes exist. A capable type may consume earlier valid samples even though an individual future recorder can still fail.
- Retain `SessionName`/`PaneIndex`/`AgentType` and add `PaneID`, absolute `NTMExecutable`, `LocalExecution`, encoded `AgentMemoryRunnerMetadata`, and optional `AgentMemoryCompletionSocket` to `AgentTemplateVars`. CLI/robot call `agentmemory.EncodeRunnerMetadata`, then config renders without importing agentmemory. This locks the connector direction: adapters -> agentmemory+config; config never imports agentmemory.
- `GenerateAgentCommandForLaunch` is the actual per-pane render. Its per-render action recorder sets `MemoryRecorderStarted` only when the systemd prefix and runner prefix both render nonempty with complete metadata. Missing metadata, remote execution, or unavailable systemd preserves the enforcement/agent command where possible, returns a stable recorder reason, and logs one launch-generation evidence failure.
- For Codex, move `CODEX_SYSTEM_PROMPT` before `memLimitPrefix` so it remains an environment assignment for the eventual agent argv. Preserve `CARGO_BUILD_JOBS` and every systemd property.

### 2. Authoritative in-scope runner and finalizer

Eligible templates render:

~~~text
<existing environment> <unchanged memLimitPrefix> <agentMemoryRunnerPrefix metadata> <agent argv>
~~~

Register two hidden Phase-1-only commands beside `internal-monitor` in `internal/cli/root.go`:

- `internal-agent-memory-run --metadata=<encoded> -- <argv...>`
- `internal-agent-memory-finalize --metadata=<encoded> --ready-fd=N --arm-fd=N`

~~~go
type RunnerMetadataV1 struct {
    Version uint32 `json:"version"`
    Session string `json:"session"`
    PaneID string `json:"pane_id"`
    PaneIndex int `json:"pane_index"`
    AgentType agent.AgentType `json:"agent_type"`
    NTMExecutable string `json:"ntm_executable"`
    CompletionSocket string `json:"completion_socket,omitempty"`
}

type FinalizerMetadataV1 struct {
    Version uint32 `json:"version"`
    Runner RunnerMetadataV1 `json:"runner"`
    ExpectedRunner process.ProcessIdentity `json:"expected_runner"`
    Cgroup process.CgroupRef `json:"cgroup"`
}

type CompletionEventV1 struct {
    Version uint32 `json:"version"`
    SampleID string `json:"sample_id,omitempty"`
    Finalizer process.ProcessIdentity `json:"finalizer"`
    Durable bool `json:"durable"`
    Reason string `json:"reason,omitempty"`
}

func EncodeRunnerMetadata(RunnerMetadataV1) (string, error)
func DecodeRunnerMetadata(string) (RunnerMetadataV1, error)
func EncodeFinalizerMetadata(FinalizerMetadataV1) (string, error)
func DecodeFinalizerMetadata(string) (FinalizerMetadataV1, error)
func WriteCompletionEvent(io.WriteCloser, CompletionEventV1) error
func ReadCompletionEvent(io.Reader) (CompletionEventV1, error)
func RunAgentMemoryRunner(context.Context, RunnerMetadataV1, []string, RunnerDeps) error
func RunAgentMemoryFinalizer(context.Context, FinalizerMetadataV1, FinalizerDeps) error
~~~

Runner and finalizer metadata use the same named bounds `agentMemoryRunnerMetadataVersion = 1`, `agentMemoryRunnerMetadataMaxDecodedBytes = 4096`, `agentMemorySessionMaxBytes = 256`, and `agentMemoryPaneIDMaxBytes = 64`. Each is strict JSON encoded with unpadded base64url; decoded JSON is limited to 4096 bytes and encoded input to `base64.RawURLEncoding.EncodedLen(4096)`. Reject unknown/trailing fields, wrong version, nonzero unknown fields, negative pane index, invalid canonical type, non-absolute executable/socket, and NUL. Metadata may appear in the local process table but is never written to the sample store or logs. `CompletionSocketFromEnv` reads optional internal diagnostic `NTM_AGENT_MEMORY_COMPLETION_SOCKET`, validates an absolute Unix-socket path, and is used by the real CLI/robot metadata builder; empty means no event.

Completion uses one connection per event. `WriteCompletionEvent` owns the passed connection, writes one strict compact JSON object plus newline, and closes it on both success and error; a close error is returned only when no earlier write error exists. `ReadCompletionEvent` reads at most `agentMemoryCompletionEventMaxBytes = 2048` including newline, requires exactly one newline-terminated JSON value followed by EOF, rejects unknown/trailing fields/invalid version/identity/reason, and returns only after the peer has closed. Listener/client deadlines are 100ms.

Runner contract:

1. Validate bounded metadata/argv and capture its own expected `ProcessIdentity` after systemd has placed it in the scope.
2. Resolve a finite, distinct cgroup v2 `CgroupRef` and derive identity inputs without persisting raw session/cgroup data.
3. Start the same binary's finalizer with inherited scope membership, `SysProcAttr.Setsid=true`, a new process group, `stdin/stdout/stderr=/dev/null`, readiness and arm/abort pipes, close-on-exec on every unrelated descriptor, and no shell. It therefore does not receive foreground Ctrl-C/HUP.
4. Wait at most `agentMemoryFinalizerReadyLimit = 1 * time.Second`. On failure, close pipes, send TERM to the captured finalizer identity/process group, wait `agentMemoryFinalizerStopGrace = 250 * time.Millisecond`, send KILL if still the same identity, then join under `agentMemoryFinalizerJoinLimit = 1 * time.Second` using `exec.Cmd.WaitDelay`. Every path closes inherited descriptors. Failure logs once and continues agent launch.
5. Mark the arm pipe close-on-exec and call `syscall.Exec` with exact argv and inherited environment/stdin/stdout/stderr. EOF is only a request to attest, not proof of successful exec. If exec returns, send abort and call the same identity-safe bounded stop routine from step 4 (TERM -> grace -> KILL -> bounded Wait plus descriptor close), even when the finalizer ignores abort/TERM; then return the normal exec failure without a sample.

Finalizer contract:

1. Capture its expected identity, prove membership in the supplied cgroup device/inode, signal ready, and wait for arm. Optional HostID/store work is not on the runner readiness path.
2. After arm EOF, attest `ExpectedRunner` before HostID/drain: capture the exact same ProcessIdentity and cgroup ref, then inspect bounded `/proc/<pid>/exe` plus NUL-delimited cmdline and require that it no longer names the hidden `internal-agent-memory-run` command. Missing, still-runner, malformed, identity/ref change, or ambiguity emits a nondurable abort event and no sample. This deliberately loses an ultra-short unverifiable run rather than fabricating evidence when the runner died before exec.
3. Acquire HostID and construct the store under their named deadlines. Failure exits the finalizer without affecting the already exec'd agent.
4. Poll production `cgroup.procs` every `agentMemoryFinalizerPollInterval = 100 * time.Millisecond` until every member except itself is gone. It intentionally pins the scope and its small overhead remains included in `memory.peak`.
5. Long-lived valid descendants have no arbitrary wall-clock cap. Repeated unreadable/stale-ref state is bounded by `agentMemoryFinalizerFailureLimit = 5 * time.Second`.
6. While pinned, validate finalizer identity/membership and device/inode around terminal `memory.peak`, `memory.max`, and `memory.events.local` (fallback `memory.events`) reads.
7. Synchronously bounded-append one terminal sample. If `CompletionSocket` is nonempty, connect with a `100ms` deadline and emit exactly one strict `CompletionEventV1` only after append success/failure is known; the event includes the finalizer's expected identity. A durable event plus identity-safe `WaitForProcessExit` is the production completion/drain driver. Completion-sink failure is log-only. Evidence never affects the exec'd agent or pane.
8. Every resilience replay reruns the saved command and therefore creates a new authenticated generation automatically.

### 3. Identity-safe cgroup API and scope selection

~~~go
type CgroupRef struct {
    RelativePath string
    Device       uint64
    Inode        uint64
}

type CgroupFSRoots struct {
    ProcRoot string
    CgroupMountOverride string // fixtures only; production parses mountinfo
}

type CgroupMemoryPeak struct {
    PeakBytes     uint64
    LimitBytes    uint64
    LimitFinite   bool
    MaxEvents     uint64
    OOMEvents     uint64
    OOMKillEvents uint64
    OOMGroupKillEvents uint64
}

type CgroupReadReason string

const (
    CgroupReadOK CgroupReadReason = "ok"
    CgroupReadUnsupported CgroupReadReason = "unsupported"
    CgroupReadCanceled CgroupReadReason = "canceled"
    CgroupReadInvalidIdentity CgroupReadReason = "invalid_identity"
    CgroupReadIdentityMismatch CgroupReadReason = "identity_mismatch"
    CgroupReadUnifiedEntryMissing CgroupReadReason = "unified_entry_missing"
    CgroupReadUnifiedEntryConflicting CgroupReadReason = "unified_entry_conflicting"
    CgroupReadMountUnavailable CgroupReadReason = "mount_unavailable"
    CgroupReadInvalidPath CgroupReadReason = "invalid_path"
    CgroupReadRootScope CgroupReadReason = "root_scope"
    CgroupReadGroupReplaced CgroupReadReason = "group_replaced"
    CgroupReadMalformedCounter CgroupReadReason = "malformed_counter"
    CgroupReadUnavailable CgroupReadReason = "cgroup_unavailable"
)

type ScopeResolutionReason string
const (
    ScopeResolutionOK ScopeResolutionReason = "ok"
    ScopeResolutionUnsupportedOrUnscoped ScopeResolutionReason = "unsupported_or_unscoped"
    ScopeResolutionAmbiguous ScopeResolutionReason = "ambiguous_scope"
    ScopeResolutionTraversalBound ScopeResolutionReason = "traversal_bound"
)

func ResolveCgroupV2(ctx context.Context, expected ProcessIdentity, roots CgroupFSRoots) (CgroupRef, CgroupReadReason)
func ReadCgroupMemoryPeak(ctx context.Context, expected ProcessIdentity, expectedGroup CgroupRef, roots CgroupFSRoots) (CgroupMemoryPeak, CgroupReadReason)
~~~

`CgroupReadReason` lives in `internal/process`; `ScopeResolutionReason` lives in `internal/agentmemory`.

- Parse the cgroup2 mount from `/proc/self/mountinfo` and exact target membership from `/proc/<pid>/cgroup`; fixtures inject roots/stat/read/identity.
- Accept exactly one `0::/path`. Reject missing/conflicting entries, NUL, absolute escape, `..`, deleted markers, root-only evidence, non-cgroup2 mounts, and paths outside the mount.
- Require the caller's previously captured identity before and after. Stat and retain cgroup device/inode; revalidate identity, membership, device, and inode after reads.
- Strictly parse unsigned counters; reject sign, fraction, junk, and overflow. `memory.max=max` is unbounded. Zero peak parses at the OS boundary but sample construction rejects it.
- Scope discovery starts from a typed pane owner, traverses descendants with `agentMemoryScopeMaxDepth = 4` and `agentMemoryScopeMaxFanout = 64`, excludes shell/root, groups by validated ref, and accepts exactly one outermost scope. None is `unsupported_or_unscoped`; multiple disjoint scopes are `ambiguous_scope`.
- Read `memory.events.local` when present; fall back to `memory.events` only on not-exist. Malformed, permission-denied, or unstable local data is a read failure, not a fallback.
- Linux/non-Linux behavior uses build tags. Non-Linux returns a stable unavailable reason.

### 4. Typed generations and evidence

~~~go
const AgentMemoryStoreSchemaVersion uint32 = 1

type AgentMemorySampleOutcome string
const (
    AgentMemorySampleComplete         AgentMemorySampleOutcome = "complete"
    AgentMemorySampleLastGoodCensored AgentMemorySampleOutcome = "last_good_censored"
    AgentMemorySampleLimitCensored    AgentMemorySampleOutcome = "limit_censored"
)

type AgentMemorySample struct {
    SampleID          string                   `json:"sample_id"`
    ScopeGenerationID string                   `json:"scope_generation_id"`
    Sequence          uint64                   `json:"sequence"`
    HostID            string                   `json:"host_id"`
    AgentType         agent.AgentType          `json:"agent_type"`
    PeakBytes         uint64                   `json:"peak_bytes"`
    LimitBytes        uint64                   `json:"limit_bytes,omitempty"`
    Outcome           AgentMemorySampleOutcome `json:"outcome"`
    ObservedAt        time.Time                `json:"observed_at"`
}

type AgentScopeSnapshot struct {
    PaneID            string
    PaneIndex         int
    AgentType         agent.AgentType
    AgentIdentity     process.ProcessIdentity
    Cgroup            process.CgroupRef
    ScopeGenerationID string
    SampleID          string
    LastGoodPeak      process.CgroupMemoryPeak
    LastReadAt        time.Time
}
~~~

- Canonicalize before eligibility, identity, persistence, grouping, lookup, and output. Empty/user/unknown/unverified types never create buckets.
- The tracker accepts a scope only when descendant inspection finds exactly one live `internal-agent-memory-finalize` whose strict metadata validates against its captured finalizer identity and cgroup ref. `AgentIdentity` is `FinalizerMetadataV1.ExpectedRunner`, which is also the exec'd agent identity; this is the identity the authoritative finalizer hashed. Multiple/malformed/mismatched finalizers are ambiguous. Generic one-scope discovery may support diagnostics/reaping but cannot produce evidence because it cannot guarantee the same SampleID.
- `ScopeGenerationID` is domain-separated SHA-256 over schema, hashed HostID, session/pane metadata, canonical type, expected PID/create time, and cgroup device/inode. `SampleID` hashes schema, HostID, type, and generation ID. Only hashes persist.
- A clean pinned terminal read is `complete`. Any positive `max`/`oom`/`oom_kill`/`oom_group_kill` is `limit_censored`. Tracker-only evidence after a lost terminal read is `last_good_censored`.
- Require positive peak, nonzero UTC time, and input sequence zero. Never persist raw session, pane command/title, hostname, machine ID, or cgroup path.
- Same-ID equivalent writes are no-ops. Same-ID upgrades require identical immutable identity, retain sequence, take maximum peak/limit/time, and use precedence `limit_censored > complete > last_good_censored`. Identity mismatch is a hard collision. Dual producers never double confidence.

### 5. Host identity

~~~go
var ErrHostIDUnavailable = errors.New("agent memory host identity unavailable")
type HostIDProvider interface { HostID(context.Context) (string, error) }
~~~

- Linux reads injectable `/etc/machine-id`, trims ASCII whitespace, and accepts exactly 32 hex characters. Missing, empty, all-zero, `uninitialized`, malformed, permission-denied, canceled, and non-Linux inputs are unavailable. Do not use hostname, boot ID, randomness, or `unknown`.
- Decode 16 bytes and derive `ntm-host-v1:<32 lowercase hex>` from the first 16 bytes of `HMAC-SHA256(key=machine-id bytes, message="ntm/adaptive-agent-memory/v1")`. Raw input is never returned/logged/persisted.
- The container's valid machine ID is its partition. The caller bounds acquisition with `agentMemoryHostLoadLimit = 100 * time.Millisecond`.

### 6. Versioned process-safe store

~~~go
type AgentMemorySampleSnapshot struct {
    HostID  string
    Samples []AgentMemorySample
}

type MemorySampleStore interface {
    Load(context.Context, string) (AgentMemorySampleSnapshot, error)
    AppendBatch(context.Context, []AgentMemorySample) error
}

type MemorySampleStoreOptions struct {
    Path string
    SamplesPerKey, MaxKeys, MaxRecords, MaxAppendBatch int
    MaxFileBytes int64
    LockRetry time.Duration
}

func DefaultMemorySampleStorePath() (string, error)
func NewFileMemorySampleStore(MemorySampleStoreOptions) (*FileMemorySampleStore, error)
~~~

Named defaults are single-sourced:

~~~go
const (
    defaultAgentMemorySamplesPerKey   = 50
    defaultAgentMemoryMaxKeys         = 64
    defaultAgentMemoryMaxRecords      = 3200
    defaultAgentMemoryMaxFileBytes    = 2 << 20
    defaultAgentMemoryMaxAppendBatch  = 256
    defaultAgentMemoryLockRetry       = 10 * time.Millisecond
    defaultAgentMemoryStoreLoadLimit  = 100 * time.Millisecond
    defaultAgentMemoryStoreWriteLimit = 250 * time.Millisecond
)
~~~

- Path is `$XDG_DATA_HOME/ntm/agent-memory-samples-v1.json`, else `$HOME/.local/share/ntm/...`. No cwd/shared-temp fallback. Directory is 0700; data/lock/temp are 0600.
- Zero options select every named default; any positive field overrides only that default. Reject negative fields, `MaxRecords < SamplesPerKey`, impossible key/record relationships, and a nonempty unresolved/non-absolute path before I/O.
- Envelope is exactly `{schema_version,next_sequence,samples}`. Missing means empty v1 with next sequence 1; a nonempty file requires `next_sequence == max(sequence)+1`. V1 has no predecessor migration.
- `Load` takes a shared process lock; append takes an exclusive process lock. Both the same-process arbitration (channel semaphore, not an unbounded `sync.Mutex` wait) and Unix nonblocking `flock` retry honor the same caller context. Unsupported platforms report unavailable.
- `Load(ctx, hostID)` rejects an invalid/empty derived HostID, validates the entire envelope, filters only the exact requested host, and returns a nonnil defensive-copy slice sorted by sequence. Mutating the result never changes store state. Missing file returns that HostID with `[]`.
- Exclusive lock covers bounded stat/read -> strict decode -> dedupe/merge -> sequence assignment -> per-key/global prune -> deterministic sort -> serialize/size -> same-dir temp -> file sync/close -> rename -> parent-dir sync -> unlock.
- Reject trailing JSON, unknown fields/schema, excessive keys/records, invalid or duplicate sequences/IDs/types/outcomes/times, inconsistent next sequence, and sequence overflow.
- Normalize and merge duplicate IDs within an append batch before assigning sequence; result bytes and retained strength are independent of batch order.
- Retain highest 50 sequences per host/type, then 64 keys by newest sequence with lexical ties, finally sequence-sort all records. Wall clock never evicts.
- Corrupt/oversized/unsupported/unreadable files remain byte-for-byte untouched. Never quarantine, rename, truncate, overwrite, or delete automatically.
- Append is all-or-nothing and synchronous; success is durable. No unjoined goroutine. A started fsync/rename syscall is the explicit irreducible kernel window.

### 7. Exact estimator and resolver

~~~go
type MemoryEstimateSource string
const (
    MemoryEstimateOverride MemoryEstimateSource = "override"
    MemoryEstimateCalibrated MemoryEstimateSource = "calibrated"
    MemoryEstimateFallback MemoryEstimateSource = "fallback"
)

type MemoryFallbackReason string
const (
    MemoryFallbackNone MemoryFallbackReason = ""
    MemoryFallbackColdStart MemoryFallbackReason = "cold_start"
    MemoryFallbackLowConfidence MemoryFallbackReason = "low_confidence"
    MemoryFallbackStoreUnavailable MemoryFallbackReason = "store_unavailable"
    MemoryFallbackHostIDUnavailable MemoryFallbackReason = "host_id_unavailable"
    MemoryFallbackUnsupportedOrUnscoped MemoryFallbackReason = "unsupported_or_unscoped"
    MemoryFallbackCensoredEvidence MemoryFallbackReason = "censored_evidence"
)
~~~

Named math is single-sourced:

~~~go
const (
    agentMemoryPercentileNumerator   uint64  = 9
    agentMemoryPercentileDenominator uint64  = 10
    agentMemoryMiB                   uint64  = 1 << 20
    agentMemorySafetyNumerator       uint64  = 5
    agentMemorySafetyDenominator     uint64  = 4
    agentMemoryMinConfidence         float64 = 0.35
    agentMemoryConfidenceFullSamples int     = 100
    agentMemoryCgroupReadLimit               = 100 * time.Millisecond
    agentMemoryTrackerFinalizeLimit          = 500 * time.Millisecond
)
~~~

1. The automatic floor is not caller-supplied: the estimator uses `config.DefaultPerAgentExpectedMemMB`. Reject automatic `CeilingMB < 2048`. The explicit-override resolver branch returns before this automatic-bound validation because it does not use the ceiling.
2. Require nonempty valid derived HostID and canonical supported AgentType. Defensively copy, then reject (do not silently filter) any invalid, duplicate-ID, host-mismatched, or type-mismatched direct input.
3. Complete peaks use ascending nearest-rank p90 rank `n-n/10`: n=1 ->1, n=5 ->5, n=10 ->9, n=11 ->10.
4. Convert bytes with ceiling division and calculate `ceil(p90MiB*5/4)` with checked multiplication.
5. Confidence counts complete samples only: `min(1,log1p(n)/log1p(100))`; gate unrounded at 0.35, publish rounded to three decimals.
6. Limit-censored evidence forces the enforcement ceiling, `source=fallback`, and `censored_evidence`. For last-good evidence, choose the maximum PeakBytes, ceiling-convert it to MiB, apply checked `ceil(MiB*5/4)`, then clamp to the ceiling; this exact value is `censoredLowerBound`. Overflow is a typed estimator error. Last-good evidence never raises confidence.
7. No records is `cold_start` at exactly 2048. Only last-good records is `censored_evidence` at `max(floor,censoredLowerBound)`. One-to-four complete samples, with or without last-good records, is `low_confidence`. Five or more complete samples is calibrated.
8. Automatic result is `min(ceiling,max(floor,p90Safety,censoredLowerBound))`. Explicit override bypasses estimation and clamping.
9. `SampleCount` counts complete samples; separate fields count last-good and limit-censored samples; `CensoredSampleCount` is their checked sum. `ObservedP90MB` is nonnil iff at least one complete sample exists. Override rows have zero diagnostics and nil p90. `SafetyFloorApplied` is true exactly when both pre-floor candidates are **strictly less than** 2048; a p90 or last-good candidate equal to 2048 wins the tie and makes it false. Limit-censored ceiling forcing always makes it false.

~~~go
type AgentMemoryDemand struct {
    AgentType agent.AgentType
    Count int
    RecordingCapable bool
}

type AgentMemoryEstimate struct {
    AgentType agent.AgentType `json:"agent_type"`
    Count int `json:"count"`
    EstimateMB uint64 `json:"estimate_mb"`
    RequestedMemoryMB uint64 `json:"requested_memory_mb"`
    Source MemoryEstimateSource `json:"source"`
    FallbackReason MemoryFallbackReason `json:"fallback_reason,omitempty"`
    Confidence float64 `json:"confidence"`
    SampleCount int `json:"sample_count"`
    ObservedP90MB *uint64 `json:"observed_p90_mb,omitempty"`
    CensoredSampleCount int `json:"censored_sample_count"`
    LastGoodCensoredSampleCount int `json:"last_good_censored_sample_count"`
    LimitCensoredSampleCount int `json:"limit_censored_sample_count"`
    SafetyFloorApplied bool `json:"safety_floor_applied"`
}

type AgentMemoryEstimatorInput struct {
    HostID string
    AgentType agent.AgentType
    Samples []agentmemory.AgentMemorySample
    CeilingMB uint64
}

type AgentMemoryEstimatorResult struct {
    EstimateMB uint64
    ObservedP90MB *uint64
    Confidence float64
    SampleCount int
    LastGoodCensoredSampleCount int
    LimitCensoredSampleCount int
    CensoredSampleCount int
    SafetyFloorApplied bool
    Source MemoryEstimateSource
    FallbackReason MemoryFallbackReason
}

func EstimateAgentMemory(AgentMemoryEstimatorInput) (AgentMemoryEstimatorResult, error)

type AgentMemoryPolicy struct {
    OverrideMB uint64
    CeilingMB uint64
}

type AgentMemoryResolution struct {
    Estimates []AgentMemoryEstimate
    RequestedMemoryMB uint64
}

type AgentMemoryResolver struct {
    HostID agentmemory.HostIDProvider
    Store agentmemory.MemorySampleStore
}

func (r AgentMemoryResolver) Resolve(
    context.Context,
    []AgentMemoryDemand,
    AgentMemoryPolicy,
) (AgentMemoryResolution, error)

type AgentMemoryErrorKind string
const (
    AgentMemoryErrorInvalid AgentMemoryErrorKind = "invalid"
    AgentMemoryErrorOverflow AgentMemoryErrorKind = "overflow"
)

type AgentMemoryError struct {
    Kind AgentMemoryErrorKind
    Op string
    Err error
}

func (e *AgentMemoryError) Error() string
func (e *AgentMemoryError) Unwrap() error
func ClassifyAgentMemoryError(error) SpawnMemoryFailureReason
~~~

- CLI adapts each `FlatAgent`; robot adapts counts. One normalizer canonicalizes/groups, rejects invalid/nonpositive/conflicting `RecordingCapable`/overflow input, and sorts. User panes are excluded.
- Resolver precedence is validate demands -> override/no evidence I/O -> validate automatic ceiling >= `config.DefaultPerAgentExpectedMemMB` -> incapable baseline rows -> if no capable rows return with no HostID/store I/O -> HostID once -> store load once -> reuse snapshot -> select exact type rows -> estimator -> checked row products/sum.
- Optional provider errors/panics/timeouts become fallbacks; raw errors are logs only. Invalid demand/bounds/rows return `AgentMemoryErrorInvalid`; any checked count/product/sum/percentile/safety/sequence conversion overflow returns `AgentMemoryErrorOverflow`. `ClassifyAgentMemoryError` uses `errors.As`, maps those kinds exactly to `invalid_memory_estimate`/`memory_estimate_overflow`, and maps unknown nonnil errors conservatively to invalid.
- DTO diagnostics are exact:
  - `override`: reason omitted, confidence/counts zero, p90 nil, floor false.
  - `unsupported_or_unscoped`, `host_id_unavailable`, `store_unavailable`, and `cold_start`: estimate 2048, confidence/counts zero, p90 nil, floor true.
  - `low_confidence`: complete/last-good/limit/combined counts and p90 from the estimator; floor flag follows the determining candidate.
  - `censored_evidence`: last-good or limit count is nonzero; complete diagnostics remain visible; estimate follows the censor rules.
  - `calibrated`: reason omitted, complete/censored diagnostics visible, and floor flag exact.
  - Estimator itself returns source/reason plus separate last-good/limit counts, so the resolver copies rather than reclassifies samples. Combined censored count must equal their checked sum.
  - `fallback_reason` is present if and only if `source=fallback`.

### 8. Admission and robot schema

~~~go
type SpawnMemoryCheckStatus string
const (
    SpawnMemoryCheckDisabled SpawnMemoryCheckStatus = "disabled"
    SpawnMemoryCheckUnavailable SpawnMemoryCheckStatus = "mem_available_unavailable"
    SpawnMemoryCheckChecked SpawnMemoryCheckStatus = "checked"
    SpawnMemoryCheckInvalid SpawnMemoryCheckStatus = "invalid"
)

type SpawnMemoryFailureReason string
const (
    SpawnMemoryFailureNone SpawnMemoryFailureReason = ""
    SpawnMemoryFailureInvalid SpawnMemoryFailureReason = "invalid_memory_estimate"
    SpawnMemoryFailureOverflow SpawnMemoryFailureReason = "memory_estimate_overflow"
)

type SpawnMemoryAdmissionInput struct {
    Status SpawnMemoryCheckStatus
    FailureReason SpawnMemoryFailureReason
    AvailableMemoryMB uint64
    Estimates []AgentMemoryEstimate
}

type SpawnAdmissionInput struct {
    // existing non-memory fields remain
    Memory SpawnMemoryAdmissionInput
}

type SpawnAdmission struct {
    // existing fields
    MemoryCheckStatus SpawnMemoryCheckStatus `json:"memory_check_status"`
    RequestedMemoryMB uint64 `json:"requested_memory_mb"`
    AvailableMemoryMB uint64 `json:"available_memory_mb"`
    MemoryEstimates *[]AgentMemoryEstimate `json:"memory_estimates,omitempty"`
}
~~~

- Status and numeric fields are always present. Estimates are absent only for disabled/unavailable; present and non-null for checked/invalid in dry-run admits/refusals and real refusals.
- For every syntactically valid robot spawn evaluation, `SpawnOutput.Admission` is nonnil on dry-run/real admit and refusal, including disabled/unavailable status. Early option/config/session errors may return the standard robot error envelope without admission.
- Resolver errors construct `invalid` with the typed `FailureReason` preserved, successfully read availability, requested memory zero, and a nonnil empty estimates slice; partially trusted rows are never exposed. `EvaluateSpawnAdmission` validates status/reason combinations and maps that reason to the distinct stable refusal token.
- One config predicate gives CLI/robot identical enablement. Disabled performs no live/evidence I/O.
- Parse `MemAvailable` as uint64. Missing/malformed is unavailable/fail-open; legitimate zero is checked.
- Pure admission defensively sorts/copies, rejects invalid/duplicate/count/product/aggregate rows, and recomputes checked uint64 aggregate.
- Usable memory clamps to zero when available does not exceed reserve. Decision order is invalid request -> agent cap -> invalid/overflow memory -> insufficient memory -> existing large-spawn rules.
- Stable fail-closed reasons are `invalid_memory_estimate` and `memory_estimate_overflow`; insufficient remains `insufficient_memory`.

### 9. Effect order and failure isolation

Admission serializes named effects: gates -> demands -> `MemAvailable` -> override -> HostID -> one store load -> pure resolution -> pure admission. No I/O, assignment, increment, or checked sum occurs inside a conditional expression.

Confirmed death is:

~~~text
emit ended -> stop/join resilience -> bounded tracker Finalize
-> memory-finalize log -> optional reap/log -> panic-isolated summary
-> delete manifest
~~~

Memory finalization is independent of reaping. Every error/panic/timeout/cancel/lock/identity failure is logged and swallowed; `DeleteManifest` remains the only returned error. Tracker `Step` runs synchronously at startup and every poll, then `Finalize` after resilience joins and before reap.

| Failure | External result | Mutation/completion |
|---|---|---|
| Override | Exact rows | No HostID/store read |
| Disabled/unavailable | Explicit status; estimates absent | No evidence I/O |
| HostID failure | 2048 `host_id_unavailable` | No store I/O |
| Missing store | 2048 `cold_start` | No load mutation |
| Bad/locked store | 2048 `store_unavailable` | Original untouched |
| 1-4 complete | Safe `low_confidence` | Diagnostics retained |
| >=5 complete | Calibrated | Read-only admission |
| Limit/OOM censored | Enforcement ceiling | Never teaches low |
| Unsupported scope | 2048 explicit fallback | No evidence |
| Bounds/overflow | Fail-closed refusal | No wrap/saturation |
| Runner/finalizer failure | Agent still runs | Warning/no sample |
| Append/death-finalize failure | Agent/cleanup unchanged | Transaction/order preserved |

## Scope Boundaries

- Do not add PSI-based backpressure; it is a separate real-time contention feature.
- Do not change `PerAgentMemLimitMB`, `MemoryHigh`, `MemoryMax`, or the existing systemd property protocol.
- Do not learn per named agent instance or persist raw session/pane/host/cgroup data; aggregate only by hashed host and canonical type.
- Do not sync evidence across hosts or write learned values into `config.toml`.
- Do not infer custom-template eligibility from rendered shell substrings.
- Do not use shell-PID evidence, an `unknown` HostID, a fire-and-forget store goroutine, fixed sleeps in closure tests, or confirmed session death as the first discovery point.

## Residual Risks and Locked Responses

- **Interactive job control:** runner must exec; finalizer detaches from the TTY. Behavior 11/C2 covers normal exit, Ctrl-C, HUP, stdin/stdout/stderr, and status.
- **Whole-cgroup kill/OOM group:** the finalizer can be killed before append. The live tracker supplies conservative last-good evidence when available; otherwise the generation is absent and admission falls back. Never fabricate completion.
- **Surviving descendants:** finalizer drains all cgroup members except itself, not only the exec'd PID.
- **Executable lifetime/quoting:** saved commands contain the absolute NTM path. Tests cover spaces, atomic binary replacement, and missing executable; failure is an ordinary launch/restart failure and cannot create a false sample.
- **Remote tmux:** local `/proc`/cgroup data must not be associated with a remote pane. Remote panes are ineligible unless the runner itself executes remotely with the remote host's local store.
- **Mount namespaces/containers:** process and observer must resolve the same cgroup2 mount and inode. A namespace mismatch is `cgroup_unavailable`.
- **Concurrent producers:** finalizer/tracker races are reconciled only through the process-locked deterministic upsert and strength merge.
- **Validation renders:** callers that render with empty vars for config validation/init/reconstruction receive no runner prefix and no eligibility; only an actual per-pane launch supplies metadata.
- **Other launch surfaces:** `add`, restore/resume, auth restart, controller restart, and `robot-restart-pane` are not admission producers in this slice and remain `missing_metadata` unless separately moved to per-pane rendering. They must not emit false evidence. Spawn and its resilience replay are the required complete producer surfaces.
- **Initial monitor gap:** agents launch before the manifest/monitor can observe them. The in-scope finalizer owns normal completeness; the plan does not claim the tracker observed a generation until `Step` succeeds.

## Smallest Testable Behaviors

Any real async edge, cross-package handoff, process/OS boundary, persistence transaction, or production registration is BLOCKING. Pure same-package reductions and deterministic fixture-injected parsers/providers are LEAF.

### Behavior 1 — Config floor/override and prospective template capability (LEAF)

- **Given/When/Then:** nil/zero/positive config and parsed default/custom templates before pane allocation -> resolve config/capability -> default is 2048, positive override is exact, only ASTs containing both actions are capable without needing a PaneID.
- **RED:** table tests for below-floor/above-ceiling override, negative validation, default templates, custom opt-in, missing one action, and proof that capability is identical with empty/real pane metadata.
- **GREEN:** export the floor/override getter and parsed capability using named constants; preserve existing enforcement output.
- **REFACTOR:** keep all runtime evidence imports/I/O out of config and eliminate duplicate CLI/robot eligibility logic.

### Behavior 2 — Application-specific HostID (LEAF)

- **Given/When/Then:** injected machine-id fixtures -> request HostID -> valid values derive the stable redacted ID; invalid/non-Linux/canceled inputs return unavailable without raw leakage.
- **RED:** upper/lower hex, whitespace, missing, empty, zero, uninitialized, bad length/chars, permission, cancellation, stable HMAC, and serialization/log redaction.
- **GREEN:** implement the bounded provider with flat guards.
- **REFACTOR:** single-source source path/domain tag/timeout; no hostname or shared sentinel.

### Behavior 3 — Identity-safe cgroup parser/reader (LEAF)

- **Given/When/Then:** expected identity plus injected proc/mount/cgroup fixtures -> resolve/read -> exact unified ref and counters return only when identity/membership/inode are stable.
- **RED:** missing/conflicting `0::`, traversal, root, deleted path, wrong mount, malformed/overflow/zero/max/event values, before/after PID reuse, inode replacement, and cancellation.
- **GREEN:** structured result/reason enum and build-tagged production roots.
- **REFACTOR:** keep parsing pure, effects named, and failure paths flat.

### Behavior 4 — Typed pane-to-scope resolution (LEAF)

- **Given/When/Then:** typed pane shell plus descendant identity/ref graph -> resolve -> exclude shell/root, accept one outermost distinct scope, reject none/ambiguous/bounds.
- **RED:** one scope, nested descendants, many processes same ref, disjoint refs, depth/fanout, canonical type, and replacement generation.
- **GREEN:** bounded traversal and deterministic grouping.
- **REFACTOR:** derive the old flat candidate union from the typed map; never discard attribution.

### Behavior 5 — Scope/sample identity and evidence strength (LEAF)

- **Given/When/Then:** host/session/pane/type/process/ref plus observations -> construct/merge -> retries share one ID, restart differs, max peak/strongest outcome wins.
- **RED:** canonical aliases, stable/restart IDs, UTC, zero rejection, complete/last-good/limit classification, merge upgrade, and collision error.
- **GREEN:** domain-separated length-delimited hashes and explicit outcome precedence.
- **REFACTOR:** central validation shared by tracker/finalizer/store.

### Behavior 6 — Single-process store contract (BLOCKING)

- **Given/When/Then:** temp XDG store and valid/invalid batches -> load/append -> durable deterministic v1 data, exact retention, and untouched invalid bytes.
- **RED:** path/modes, absent store, round-trip/restart, dedupe/upgrade, sequence/clock rollback, key/record/file/batch bounds, unknown fields/schema, corruption, write failure, cancel/deadline, and deterministic JSON.
- **GREEN:** strict bounded transaction and atomic replace including directory sync.
- **REFACTOR:** isolate decode/validate/prune/serialize as pure substeps; the behavior stays BLOCKING because it crosses filesystem/durability boundaries. C4 exercises its production transaction.

### Behavior 7 — Cross-process store transaction (BLOCKING)

- **Given/When/Then:** multiple helper processes released together -> each production `AppendBatch` -> loaded union has no lost IDs/duplicates and contention times out unchanged.
- **RED:** a barrier-based subprocess test fails with unlocked RMW.
- **GREEN:** context-aware process lock spans the entire transaction.
- **REFACTOR:** one lock implementation/timeout policy; join all helpers, no sleeps.

### Behavior 8 — Exact estimator (LEAF)

- **Given/When/Then:** exact host/type complete/censored samples and ceiling -> estimate -> exact nearest-rank, rounding, fixed 2048 floor, ceiling, and censor policy.
- **RED:** n=0/1/4/5/10/11/100, sub-MiB, 5/4 ceiling, last-good-only/mixed lower bound and separate counts, limit-censored ceiling, floor-candidate ties exactly at 2048, ceiling below 2048, mismatched/invalid/duplicate sample, invalid-vs-overflow classification, immutable input.
- **GREEN:** checked integer algorithm plus existing confidence curve.
- **REFACTOR:** named constants and typed result/reasons only.

### Behavior 9 — Demand normalization and resolver precedence (BLOCKING)

- **Given/When/Then:** mixed aliases/counts/recording capability, policy, providers -> resolve -> canonical sorted rows, exact override short-circuit, one snapshot, stable fallbacks, checked aggregate.
- **RED:** invalid/user/unknown, conflicting capability, count overflow, override call counts, automatic ceiling below 2048, provider panic/timeout, missing/corrupt store, every exact fallback diagnostic, cold/last-good-only/low/censored/calibrated rows, defensive copy.
- **GREEN:** flat precedence with panic isolation.
- **REFACTOR:** share one normalizer/resolver; config contains no runtime evidence policy. C1/C7 exercise the cross-package HostID/store/admission handoff.

### Behavior 10 — Pure admission status and arithmetic (LEAF)

- **Given/When/Then:** status, availability, typed rows, existing pressure inputs -> evaluate -> exact decision order, presence state, reserve clamp, and recomputed aggregate.
- **RED:** disabled/unavailable/checked/invalid, zero availability, reserve boundary/equality, duplicate/count/product mismatch, multiply/add overflow, insufficient memory, copy/sort determinism.
- **GREEN:** convert memory fields to uint64 and use checked helpers.
- **REFACTOR:** no mutations or effects in control expressions.

### Behavior 11 — Template runner transparency (BLOCKING)

- **Given/When/Then:** production rendered eligible command and helper argv/stdin/out/err -> invoke hidden runner -> PID/TTY/env/stdio/status semantics match direct exec; initialization failure still runs agent.
- **RED:** quoting, argv boundaries, env assignment, readiness timeout, ready-then-runner-killed-before-exec yields no sample, abort-on-exec-failure, finalizer ignoring abort/TERM still reaches bounded KILL/Wait, and descriptor/join subprocess tests.
- **GREEN:** register hidden Phase-1 command, readiness/arm protocol, and `syscall.Exec`.
- **REFACTOR:** one renderer used by CLI/robot; no shell in runner.

### Behavior 12 — In-scope terminal finalizer (BLOCKING)

- **Given/When/Then:** real scoped helper allocates/blocks/exits -> finalizer pins, drains, reads, appends -> one terminal sample survives scope removal.
- **RED:** normal exit, descendant outliving parent, event censoring, store error/panic/timeout, duplicate tracker write, bounded unreadable ref.
- **GREEN:** detached in-scope finalizer and synchronous durable append.
- **REFACTOR:** injected ticker/reader/store for unit cases; real boundary remains unmocked in closure.

### Behavior 13 — Live generation tracker (BLOCKING)

- **Given/When/Then:** manifest ownership plus exact finalizer metadata and startup/poll/replacement/death observations -> production monitor drives `Step/Finalize` -> embedded runner identity yields typed monotonic last-good samples with the authoritative SampleID.
- **RED:** exact finalizer match, malformed/multiple/mismatched metadata, generic scope without finalizer produces no evidence, immediate discovery, peak rise, replacement, disappearance, restart, death, cancellation, and duplicate producer.
- **GREEN:** keyed tracker plus derived orphan candidate union.
- **REFACTOR:** synchronous driver, no independent unjoined polling goroutine.

### Behavior 14 — Confirmed-death failure isolation (BLOCKING)

- **Given/When/Then:** real manifest/tracker/store and injected memory failures -> confirmed-death handler -> exact ordered effects continue and only delete error returns.
- **RED:** success, read/store error, panic, timeout, cancellation, lock contention, reap on/off, summary panic, delete error, once-only loop drive.
- **GREEN:** add `FinalizeMemory`/`LogMemoryFinalize` deps after stop/join and before reap.
- **REFACTOR:** guard clauses while retaining every existing relative effect.

### Behavior 15 — CLI/robot/REST recorder registration (BLOCKING)

- **Given/When/Then:** capable/incapable, successful/failed/dry-run/refused entrypoints -> spawn -> CLI/robot real panes use equivalent per-pane runner metadata; dry-run uses prospective capability; REST inherits robot; robot never starts the CLI monitor.
- **RED:** CLI/robot command parity per pane, missing/remote/systemd-unavailable render reasons, robot CLI and REST delegation, CLI manifest command replay, dry-run before pane allocation, no CLI import from robot.
- **GREEN:** move robot rendering into `launchAgent`, use shared metadata encoder/config renderer, and leave CLI's existing manifest/monitor ownership intact.
- **REFACTOR:** delete eager empty-var robot rendering only; no new detached-monitor ownership protocol.

### Behavior 16 — Robot JSON/schema and CLI parity (BLOCKING)

- **Given/When/Then:** identical config/demands/evidence/availability through both adapters -> admission/JSON/schema -> byte-equivalent sorted rows/aggregate/decision and locked presence/enums.
- **RED:** override, calibrated, cold, low, host/store/unscoped/censored, disabled, unavailable, invalid, overflow and insufficient-memory admit/refuse round trips.
- **GREEN:** extend `SpawnAdmission`, schema registration, docs/capabilities, and both adapters.
- **REFACTOR:** compare complete memory projections, not only final decisions.

### Behavior 17 — Direct real cgroup boundary (BLOCKING)

- **Given/When/Then:** production template launches the current Go test helper in a real systemd user scope -> reader receives the helper's expected identity -> ref is distinct and peak is sane within named accounting slack.
- **RED:** environment-gated test proves a hand-rolled/bare-PID path is insufficient.
- **GREEN:** use production renderer, real `systemd-run`, socket readiness/release, and joined cleanup.
- **REFACTOR:** central `RequireSystemdUserCgroupV2` gate; no exact kernel-byte assertion.

### Behavior 18 — Full learned-admission workflow (BLOCKING)

- **Given/When/Then:** empty temp XDG/config plus real binary/tmux/systemd and five scoped Codex helpers -> real robot spawn/exit/finalize then mixed robot dry-run -> Codex is calibrated with five samples, Claude is cold baseline, and aggregate is checked row sum.
- **RED:** no store seeding/direct estimator calls; with the runner action removed but systemd scope retained, no durable completion events arrive and the final dry-run remains cold. This seam-red proves the production connector is necessary.
- **GREEN:** close every production registration/handoff from rendering through persistence and later admission.
- **REFACTOR:** socket/barrier/deadline drivers and complete cleanup; no fixed sleep or raw-store assertion.

## Ordered TDD Implementation Sequence

Do not write broad production wiring before its smallest contract is red. At every step, run the narrow package tests before proceeding.

1. Lock config names, sample/demand/result enums, schema/bound/statistical constants, and JSON tags (Behaviors 1 and 5).
2. Implement HostID fixtures and redaction (Behavior 2).
3. Implement fixture-driven cgroup mount/membership/counter reader (Behavior 3).
4. Implement typed pane descendant/scope resolution (Behavior 4).
5. Implement sample construction, deterministic IDs, and merge strength (Behavior 5).
6. Implement single-process schema/store/retention/atomic replacement (Behavior 6).
7. Add process-safe locking and close the concurrent subprocess transaction before any producer wiring (Behavior 7).
8. Implement the pure estimator and censor policy (Behavior 8).
9. Implement demand normalization/resolver, including override zero-I/O and one immutable load (Behavior 9).
10. Extend pure admission status, uint64 checked arithmetic, and presence semantics (Behavior 10).
11. Add per-pane template metadata, hidden runner registration, exec/readiness/abort semantics (Behavior 11).
12. Add the pinned finalizer and terminal durable sample path; run the real systemd boundary (Behavior 12/C2/C8).
13. Add the keyed live tracker and synchronous monitor startup/poll/finalize drivers (Behavior 13).
14. Insert bounded finalization into confirmed-death dependencies/order (Behavior 14).
15. Move robot rendering per pane and close shared CLI/robot/REST recorder registration without moving the CLI monitor (Behavior 15).
16. Finish CLI/robot parity, JSON/schema/help/capabilities (Behavior 16).
17. Run the direct real cgroup closure, then the full learned-admission closure (Behaviors 17-18).
18. Update documentation, scan every touched file for dead imports/wrong-level calls/LLM residue, and run the full verification matrix.

## Workflow Closure

### Production operation chains

Producer:

~~~text
CLI/robot/REST spawn
-> shared admission and real pane allocation
-> per-pane production template render
-> unchanged systemd scope
-> hidden runner starts ready finalizer and execs agent
-> finalizer pins generation until all other scope members exit
-> terminal identity/ref/event read
-> process-safe durable sample upsert
~~~

Safety net:

~~~text
CLI manifest/monitor lifecycle
-> internal-monitor startup/poll
-> typed pane-to-scope Step retains last-good peak
-> generation replacement/disappearance/death Finalize
-> same SampleID upgrades/deduplicates with finalizer
~~~

Consumer:

~~~text
spawn request
-> shared enablement and MemAvailable
-> canonical demands/capability
-> override OR HostID + one immutable store snapshot
-> exact per-type estimates and checked sum
-> pure admission
-> CLI decision / robot admission JSON
~~~

### C1 — CLI/robot demand parity (Behavior 16, BLOCKING)

- **SOURCE:** one temp XDG store populated through production append, mixed canonical evidence, one config, CLI `[]FlatAgent`, and equivalent robot counts.
- **TRIGGER:** each real adapter's highest connector into the shared resolver, not the pure normalizer.
- **FORBIDDEN SPAN:** capability, canonicalization, HostID, load, filtering, estimation, multiplication, sorting, and aggregate.
- **DRIVERS:** synchronous resolver/admission calls with the same injected `MemAvailable` statement.
- **OBSERVABLE:** complete memory status/rows/aggregate/decision are byte-equal after JSON round trip.

### C2 — Runner/finalizer terminal capture (Behaviors 11-12, BLOCKING)

- **SOURCE:** real built `ntm`, production rendered template, real systemd user scope, current Go test binary helper, temp XDG, canonical metadata.
- **TRIGGER:** `internal-agent-memory-run` through the rendered command, never direct finalizer/store calls.
- **FORBIDDEN SPAN:** hidden registration, argv/env/stdio, ready/arm protocol, exec, finalizer detach/pin/drain, identity/cgroup reads, HostID, ID construction, lock/append.
- **DRIVERS:** Unix-socket helper readiness/release, one completion event after durable append, identity-safe finalizer-exit wait, and child process `Wait` under context; signal cases use real signals. No pidfile poll, repeated resolver call, or sleep.
- **OBSERVABLE:** helper status/stdio semantics are preserved; after one durable event/drain, one subsequent production resolver sees exactly one sample. Injected telemetry failure emits a nondurable event when the sink remains available, joins, and preserves status.

### C3 — Monitor generation safety net (Behavior 13, BLOCKING)

- **SOURCE:** typed production manifest, injected/real descendant identities, and temp store; no pre-seeded tracker state.
- **TRIGGER:** `runSessionMonitorLoop` startup/poll/death path.
- **FORBIDDEN SPAN:** manifest mapping, descendant resolution, ref selection, identity read, generation transitions, last-good construction, append/merge.
- **DRIVERS:** existing injectable tick/ready/death channels synchronously drive old scope -> rise -> replacement -> disappearance -> death.
- **OBSERVABLE:** production load/resolver shows one row/generation, correct type/max/strength, and existing later monitor effects run.

### C4 — Concurrent store transaction (Behavior 7, BLOCKING)

- **SOURCE:** empty temp XDG plus N current-test-binary helpers with distinct valid batches.
- **TRIGGER:** every helper calls production `AppendBatch`.
- **FORBIDDEN SPAN:** lock, bounded load/validate, merge, sequence, pruning, serialization, modes, sync, rename.
- **DRIVERS:** Unix-domain barrier releases helpers together; all acknowledge and are joined. A separate process holds the actual lock for timeout.
- **OBSERVABLE:** production `Load`/resolver sees exactly the bounded union, no duplicates/lost updates; contention leaves prior bytes unchanged.

### C5 — Recorder registration (Behavior 15, BLOCKING)

- **SOURCE:** capable/incapable and successful/failing/dry-run/refused CLI/robot inputs, production config templates, per-pane metadata recorder, and REST request.
- **TRIGGER:** outer CLI spawn handler, `robot.GetSpawn`, robot CLI flag route, and REST spawn handler.
- **FORBIDDEN SPAN:** prospective capability, demand row, pane allocation, actual metadata encoding/render, send, CLI saved replay command, and REST delegation.
- **DRIVERS:** synchronous renderer/send dependency recorders; no detached monitor or sleep.
- **OBSERVABLE:** successful CLI/robot pane commands carry equivalent exact per-pane runner metadata; dry-run exposes capability without a PaneID; missing recorder logs a stable reason; REST reaches the same robot renderer; robot starts no `internal-monitor`.

### C6 — Confirmed-death failure isolation (Behavior 14, BLOCKING)

- **SOURCE:** real temp manifest, typed retained snapshot, real temp store, existing ordered effect recorder.
- **TRIGGER:** `handleConfirmedSessionDeath`, not the callback directly.
- **FORBIDDEN SPAN:** ended, stop/join, finalize/log, reap/log, summary panic boundary, delete.
- **DRIVERS:** tick-driven once-only death plus success/error/panic/cancel/timeout/lock cases.
- **OBSERVABLE:** exact order and manifest deletion; only production delete error returns.

### C7 — Robot JSON contract (Behavior 16, BLOCKING)

- **SOURCE:** configs/demands and production-appended evidence for every source/reason/status.
- **TRIGGER:** `robot.GetSpawn(DryRun=true)` admit/refuse followed by production encoder and schema generation.
- **FORBIDDEN SPAN:** adapter, resolver/load, admission, output assembly, tags, schema, sorting/presence.
- **DRIVERS:** synchronous injected `MemAvailable`; no host-live assumption.
- **OBSERVABLE:** decoded JSON/schema has exact enums, uint64 values, absent-vs-nonnull estimates, sorting, and refusal detail.

### C8 — Direct real cgroup boundary (Behavior 17, BLOCKING)

- **SOURCE:** production `GenerateAgentCommand`/template actions around current Go helper in real systemd scope.
- **TRIGGER:** public identity-safe reader with previously captured helper identity.
- **FORBIDDEN SPAN:** renderer, systemd, proc membership, mount, path, stat, peak/max/events, revalidation.
- **DRIVERS:** socket readiness/release and `Wait` under deadlines; cleanup registered before first failing operation.
- **OBSERVABLE:** distinct ref and peak at least known allocation but below MemoryMax plus named accounting slack; exact bytes are not asserted.

### C9 — Full learned-admission workflow (Behavior 18, BLOCKING)

- **SOURCE:** empty temp XDG/config, real built `ntm`, real tmux/systemd, production custom Codex template with unchanged scope+runner actions, five helper panes, and completion-socket listener. Bootstrap headroom is disabled only so CI need not reserve five baselines. `RequireNTMBinary` deliberately keeps `NTM_DISABLE_INTERNAL_MONITOR=1`: C9 proves the finalizer path, while C3/C6 separately close the CLI safety net. No sample bytes are seeded/read for assertions.
- **TRIGGER:** highest connector: invoke the real built binary's `--robot-spawn=SESSION --spawn-cod=5 --spawn-no-user --config=...` flag route, which reaches `robot.PrintSpawn`, production encoding, `GetSpawn`, pane creation, render, and send.
- **FORBIDDEN SPAN:** robot option/admission/creation, prospective capability, per-pane metadata/render, tmux send, runner/finalizer, identity/ref/event reads, HostID, store transaction/dedupe, completion event, later load/estimate/admission/JSON. No new seam is mocked.
- **DRIVERS:** `RequireTmuxThrottled`, `RequireSystemdUserCgroupV2`, a narrow gate requiring production `MemAvailable` to parse, five helper readiness/release connections, and the optional completion socket set through the real diagnostic env. Release once; await exactly five durable `CompletionEventV1` values under one context; identity-safely wait for those finalizers to exit; then invoke the consumer exactly once. The consumer may admit or refuse based on the real host; both expose rows. No replaced OS input, repeated dry-run, store poll, or sleep.
- **OBSERVABLE:** capture stdout bytes from one real built-binary mixed Codex+Claude `--robot-spawn --dry-run` and decode the production JSON. Assert sorted estimates: Codex `source=calibrated`, `sample_count=5`, confidence >=0.35 and floor flag; Claude `fallback/cold_start` at 2048; aggregate equals checked row sum. Never assert the returned Go struct, raw store, or estimator.
- **SEAM-RED:** rerun the producer from a fresh XDG root with `memLimitPrefix` retained but runner action removed. Helpers run and no durable events arrive during the bounded negative window. Before the single consumer invocation, restore the normal capable scope+runner Codex template while keeping the same fresh XDG root; its encoded JSON is Codex `cold_start`. This proves no durable evidence crossed the removed runner connector rather than merely proving the consumer refused an incapable template.
- **CLEANUP:** install before spawn. Close/release helper sockets; kill the tmux session/scope if still present; terminate only still-matching expected process identities (never bare PIDs); wait helpers/agents to exit; consume any already-issued completion events to their deadline and identity-safely wait finalizers; verify no finalizer or monitor remains. Cleanup timeout fails the test rather than hanging.

## Production Registration and File Inventory

Prefer revising existing files where ownership already fits. The new `agentmemory` package and platform-specific OS/lock files meet the repository's high bar because no existing package owns this domain.

### Existing files to revise

- `internal/config/templates.go` / `templates_test.go`: export 2048 floor, explicit override getter, template capability/action, metadata vars, eligible defaults, Codex env ordering; prove existing MemoryHigh/Max rendering unchanged.
- `internal/config/pacing.go` / `pacing_test.go`: retain zero/positive/negative configuration semantics and document automatic floor/exact override.
- `internal/process/liveness.go` / `liveness_test.go`: reuse expected-identity capture/equality and bounded descendants; do not duplicate PID semantics.
- `internal/pressure/pressure.go` / `pressure_test.go`: status/uint64/typed rows, pure validation/recomputed aggregate, reserve underflow, decision order.
- `internal/pressure/provider.go` / tests: uint64 `MemAvailable` parser and live seam; cover zero/missing/malformed/negative/fraction/overflow.
- `internal/pressure/calibration.go` / tests: reuse, do not fork, the confidence curve; export/narrow helper only if needed without changing existing calibration output.
- `internal/resilience/manifest.go` / tests: canonical typed agent ownership for CLI monitor/tracker; robot does not create this manifest.
- `internal/resilience/monitor.go` / tests: command replay remains runner-enabled; Stop/join precedes final tracker drain.
- `internal/cli/spawn.go` / `spawn_test.go`: shared demand/resolver, per-pane metadata, and existing CLI manifest/monitor lifecycle; remove only the flat formula.
- `internal/cli/monitor.go` / `monitor_test.go`: keyed tracker startup/poll/finalize, retained flat reaper union, confirmed-death deps/order/failure matrix.
- `internal/cli/root.go` / root tests: register both hidden memory commands beside `internal-monitor`.
- `internal/startup/commands.go` / tests: classify runner/finalizer as Phase 1 only so startup never enters project/tool initialization first.
- `internal/robot/spawn.go` / `spawn_test.go`: shared demand/resolver, per-pane recorder render, actual recorder outcome logging, admission output; no detached monitor.
- `internal/robot/schema.go` / schema and `types_test.go`: stable enum/type/pointer-to-slice presence schema.
- `internal/robot/docs.go` and `capabilities.go` / tests: source/reason/status fields and examples.
- `internal/serve/server.go` / tests: REST spawn delegates to robot lifecycle exactly once.
- `tests/testutil/skip.go` / tests: `RequireSystemdUserCgroupV2` and `RequireMemAvailable`; reuse `RequireTmuxThrottled` and real-binary builder.
- `docs/robot-section-model.md` and `docs/robot-command-registry.md`: per-type sizing, evidence eligibility, safety floor, source/reasons, presence, local store recovery.

### Genuinely new files

- `internal/process/cgroup_memory.go`, `cgroup_memory_linux.go`, `cgroup_memory_other.go` and tests: shared types/reasons plus platform implementations.
- `internal/agentmemory/agentmemory.go` and tests: sample/generation IDs, validation, merge strength, HostID provider.
- `internal/agentmemory/store.go`, `store_lock_unix.go`, `store_lock_other.go` and tests: strict v1 store and process transaction.
- `internal/agentmemory/runner.go`, `finalizer.go`, `runner_unix.go`, `runner_other.go`, `tracker.go` and tests: runner/finalizer protocol, platform exec/detach boundary, and typed synchronous generation tracker.
- `internal/pressure/agent_memory.go` and tests: demand normalizer, exact estimator, resolver, DTOs, checked helpers.
- `internal/cli/agent_memory.go` and tests: narrow hidden Cobra adapters calling `agentmemory`; no policy in CLI.
- `e2e/adaptive_agent_memory_test.go`: C9 real binary/tmux/systemd producer-to-encoded-robot-consumer closure plus runner-action seam-red.

Before adding imports, run `go list` inspection and enforce: process -> none higher; agentmemory -> agent/process/util only; config remains agentmemory-free; pressure -> agent/agentmemory/config constants; resilience -> agentmemory/process/config/tmux as currently needed; CLI/robot -> agentmemory/config/pressure, with CLI also using resilience. No reverse edge to CLI/robot.

## Review Finding Traceability

### Critical findings

- [x] Wrong/lost scope identity -> typed pane ownership, actual descendant, expected identity/ref, runner in real scope, Behaviors 3-5/13, C3/C8/C9.
- [x] Late/survivor-biased capture -> pinned terminal finalizer for every normal/restart generation plus censored live backup, Behaviors 11-13, C2/C9.
- [x] PID reuse -> caller-supplied expected identity and before/after identity/membership/inode validation, Behavior 3/C8.
- [x] Robot has no producer -> per-pane in-scope runner/finalizer from `GetSpawn` and REST; CLI monitor remains optional safety net, Behavior 15/C5/C9.
- [x] Automatic mode lowers safety -> exact 2048 automatic floor; only explicit positive override may lower, Behaviors 1/8/9.
- [x] Store unsafe/unbounded -> full process lock, atomic durable RMW, deadlines, schema/key/record/file/batch bounds, Behaviors 6-7/C4.
- [x] Estimator/HostID nondeterministic -> exact source/HMAC/unavailable rules, p90/rounding/confidence/censor/bounds/overflow, Behaviors 2/8.
- [x] Failure isolation/schema missing -> exact runner/death failure policies and stable admission JSON/status/presence, Behaviors 10-16/C6-C7.
- [x] Closure too shallow -> direct boundary plus real tmux/systemd producer-to-later-robot-consumer closure, C8-C9.

### Contract and interface findings

- [x] Universal cgroup premise replaced by parsed eligibility and `unsupported_or_unscoped`.
- [x] Pane shell and agent identity/ref are distinct types.
- [x] Flat orphan candidates remain only a derived reaper view.
- [x] Normal exit and restart generate terminal samples; death is fallback drain.
- [x] Reader roots/stat/read/identity are injectable; expected identity is input.
- [x] Cgroup mount/path/counter/zero/ambiguity rules and reason enums are exact.
- [x] Confirmed-death deps include bounded finalize/log in a locked position.
- [x] Robot and CLI share recorder metadata/rendering without robot importing CLI; robot does not copy CLI monitor ownership.
- [x] Store path/options/load/append/lock/durability and corruption behavior are exact.
- [x] Runtime estimation is in pressure, never config.
- [x] Result contains estimate/source/reason/confidence/count/p90/censored/floor metadata.
- [x] Unexported enforcement logic is exercised through production template rendering.

### Promise and data-model findings

- [x] Missing-not-at-random normal exits are captured in-scope; last-good is explicitly censored.
- [x] Duplicate observer/monitor/retry writes merge one deterministic sample.
- [x] Optional host/store/cgroup failures are bounded and fail open; invalid math fails closed.
- [x] Writes are synchronous/durable and joined, not fire-and-forget.
- [x] Canonical `agent.AgentType`, uint64 counters, hashed HostID, UTC diagnostics, lock-assigned sequence, and exact outcomes are locked.
- [x] Per-key/global/key-count/record-count/batch/input/output bounds are named.
- [x] Corrupt/unknown data remains untouched; no unauthorized deletion/quarantine.
- [x] Censored/OOM data cannot teach a low estimate.
- [x] Helpers use explicit readiness/release/deadline/join and unconditional cleanup.

### API and cleanup findings

- [x] JSON field names/types/enums/order/presence are locked.
- [x] CLI/robot adapt to one demand resolver and compare full parity.
- [x] Disabled, unavailable, zero-available, invalid, overflow, admit, and refusal semantics are exact.
- [x] Checked uint64 multiplication/addition replaces int wrap risk.
- [x] Host/store/process effects are named statements before pure conditionals.
- [x] Result enums and guard clauses replace open-ended nesting.
- [x] Statistical/retention/schema/timeout/source/reason values are named and single-sourced.
- [x] Existing MemoryHigh/MemoryMax/config protocol is preserved.
- [x] Production registrations/callers, docs/schema/help/capabilities, static cleanup, and verification gates are inventoried.

## Automated Verification Matrix

Capture a fresh baseline before implementation; do not assume historical failures are still expected. Use RCH for compile-heavy work:

~~~text
rch exec -- go test -race ./internal/process/... -run 'Cgroup|MemoryPeak|ProcessIdentity'
rch exec -- go test -race ./internal/agentmemory/... -run 'HostID|Sample|Store|Runner|Finalizer|Tracker|Concurrent'
rch exec -- go test -race ./internal/pressure/... -run 'AgentMemory|SpawnAdmission|AvailableMemory'
rch exec -- go test -race ./internal/cli/... -run 'Monitor|ConfirmedDeath|AgentMemory|Spawn'
rch exec -- go test -race ./internal/robot/... -run 'Spawn|Admission|Memory|Schema'
rch exec -- go test -race ./internal/resilience/... -run 'Lifecycle|Monitor|Restart|Manifest'
rch exec -- go test -race ./internal/serve/... -run 'Spawn'
rch exec -- go test -v ./e2e/... -run 'TestAdaptiveAgentMemoryLearnedAdmission'
rch exec -- go test -short ./...
rch exec -- go build ./cmd/ntm
rch exec -- golangci-lint run
gofmt -l .
goimports -l .
ubs <every changed file>
~~~

Run C2/C8/C9 explicitly on Linux with tmux, unified cgroup v2, and a systemd user session. A skip is valid only when the shared gate names the missing prerequisite; an eligible host must not silently skip. Test cleanup must be installed before the first operation that can fail and all processes/goroutines must be joined.

## Definition of Ready and Done

The plan is ready for implementation because every review item maps to a locked contract, smallest behavior, file owner, or blocking closure and there is no open design question.

Implementation is not done until:

1. All 18 behaviors are green, including cross-process store closure and the exact failure matrix.
2. C9 observes calibrated Codex plus cold Claude only through the later production robot dry-run.
3. CLI/robot/REST parity, schema, docs/help/capabilities, formatting, lint, UBS, build, and short suite pass against a recorded fresh baseline.
4. No scope/sample file is deleted or quarantined automatically and no unrelated working-tree change is modified.
