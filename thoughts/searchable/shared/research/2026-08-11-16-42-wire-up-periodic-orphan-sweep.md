---
date: 2026-08-11T16:42:09-04:00
researcher: Claude Sonnet 5
git_commit: be5f182f00d0903ed7a52494726f036b438fbff3
branch: main
repository: ntm
topic: "Wiring up a periodic sweep for orphaned agent processes"
tags: [research, codebase, orphan-processes, tmux, spawn, kill, monitor, internal-monitor, pt-healthmonitor, serve, headroom, config-patterns]
status: complete
last_updated: 2026-08-11
last_updated_by: Claude Sonnet 5
---

# Research: Wiring up a periodic sweep for orphaned agent processes

**Date**: 2026-08-11T16:42:09-04:00
**Researcher**: Claude Sonnet 5
**Git Commit**: be5f182f00d0903ed7a52494726f036b438fbff3
**Branch**: main
**Repository**: ntm
**GitHub permalink base**: `https://github.com/tha-hammer/ntm/blob/be5f182f00d0903ed7a52494726f036b438fbff3/` — commit is pushed to `origin`; append any `path/to/file.go#L123` from the citations below to browse it on GitHub.

## Research Question

To wire up a periodic sweep [for orphaned agent processes — panes/agent processes that survive a session's death without ever going through `ntm kill`'s reap step], what exists in the codebase today: the current reactive orphan-reap mechanism, existing periodic/background-execution patterns, the only genuinely long-lived ntm process, an existing zombie-detection subsystem, config conventions for toggle+interval features, and the pane/PID enumeration APIs a sweep would need.

## Summary

ntm has exactly one production code path that terminates orphaned agent processes, and it is narrow: `internal/cli/send.go`'s `runKill` snapshots each pane's descendant PIDs before calling `tmux.KillSession`, then SIGTERM→(750ms grace)→SIGKILLs any survivors — but **only** on the plain-text (non-`--json`) branch of `ntm kill`. The JSON/kernel-registered `"sessions.kill"` handler (`buildKillResponse`) calls `tmux.KillSession` too but contains no equivalent reap step anywhere in its body. Neither path is periodic — both fire only when someone explicitly runs `ntm kill`.

There is a completely separate subsystem, `internal/integrations/pt` (`HealthMonitor`), that already runs a real periodic ticker and classifies agent processes including a `ClassZombie` state — but it only alerts (surfaces an `Alert` to the operator/robot attention feed); nothing in that package ever signals or kills a process. It is wired into production, but only inside the optional `ntm serve` HTTP server, gated behind `ProcessTriageConfig.Enabled` and the external `pt` binary being present on `$PATH`.

The most significant finding for "wiring up a periodic sweep": ntm already spawns a **per-session background daemon** on every `ntm spawn` — the hidden `ntm internal-monitor <session>` subcommand (`internal/cli/monitor.go`), started unconditionally (`internal/cli/spawn.go:2491`: "Always started regardless of auto-restart config") and detached so it outlives the spawning process. It already runs its own `select` loop with two tickers (a 5s session-existence poll and a 30s output-snapshot poll, `internal/cli/monitor.go:170-238`) for the lifetime of the tmux session. This is the only place in ntm that is (a) guaranteed to be running whenever a session with orphan-risk exists, and (b) already ticker-driven. By contrast, `internal/serve` (`ntm serve`) — the only *other* long-lived ntm process — is optional/opt-in infrastructure that ordinary `ntm spawn`/`ntm kill` commands never talk to or depend on.

The codebase has an established config convention for toggle+interval background features (`Enabled bool` + `*IntervalMs`/`CheckInterval` fields, a `Default*Config()`, and usually a `Validate*Config()`), used by `HeadroomPacingConfig`, `RanoConfig`, `CautConfig`, `ProcessTriageConfig`, and `FileReservationConfig`. Notably, `ProcessTriageConfig.OnStuck` already validates `"alert"`, `"kill"`, or `"ignore"` as accepted values (`internal/config/config.go:1631-1634`), but nothing outside `internal/config/config.go` itself ever reads `cfg.OnStuck` — the schema already anticipates an auto-kill action that was never implemented.

## Detailed Findings

### 1. The existing reactive orphan-reap mechanism (`internal/cli/send.go`)

Constants and exclusion predicate (`internal/cli/send.go:2280-2298`):
```go
const orphanReapGrace = 750 * time.Millisecond
const orphanReapMaxDepth = 4
const orphanReapFanout = 64

func orphanReapExcluded(pid int) bool {
	return pid <= 1 || pid == os.Getpid() || pid == os.Getppid()
}
```

`collectPaneDescendants(panePIDs []int) []int` (`internal/cli/send.go:2300-2347`): for each pane-shell PID, recursively walks `process.GetChildPIDs(pid, orphanReapFanout)` up to `orphanReapMaxDepth` (4) levels deep, excluding init/self/parent and deduplicating, to build a flat list of descendant PIDs. The pane shell PIDs themselves are never included in the result — only their descendants.

`reapOrphanProcesses(pids []int)` (`internal/cli/send.go:2349-2379`): first pass SIGTERMs every still-alive candidate (skipping already-exited ones via `process.IsAlive`); sleeps `orphanReapGrace` (750ms); second pass SIGKILLs any that are still alive. Signal errors are discarded (a process exiting between the liveness check and the signal is the desired outcome, not a failure).

Call site — `runKill`'s non-JSON branch only (`internal/cli/send.go:2215-2255`): `panePIDs` is built from `tmux.GetPanes(session)` *before* `tmux.KillSession`; `collectPaneDescendants(panePIDs)` runs before the kill; `reapOrphanProcesses(orphanCandidates)` runs immediately after `tmux.KillSession` succeeds.

`runKill` short-circuits to a completely different implementation in JSON mode (`internal/cli/send.go:2030-2044`):
```go
func runKill(w io.Writer, session string, force bool, tags []string, noHooks bool, summarize bool) (err error) {
	if IsJSONOutput() {
		result, err := kernel.Run(context.Background(), "sessions.kill", SessionKillInput{...})
		...
		return output.PrintJSON(result)
	}
	// ...non-JSON path with the reap logic below this point...
```

`kernel.Run(ctx, "sessions.kill", ...)` dispatches to `buildKillResponse` (registered at `internal/cli/send.go:204-218`, handler body at `internal/cli/send.go:2434-2663`). Its untagged-kill branch (`internal/cli/send.go:2602-2645`) fetches panes only to feed timeline markers, calls `tmux.KillSession(session)`, and proceeds straight to webhook emission and building the response — no call to `collectPaneDescendants`, `reapOrphanProcesses`, `process.GetChildPIDs`, or `process.IsAlive` anywhere in `buildKillResponse`'s body. The tagged-kill branch (`internal/cli/send.go:2556-2601`) calls `tmux.KillPane` per matched pane with no surrounding descendant collection either.

Process-liveness primitives these reap functions depend on (`internal/process/liveness.go`):
- `GetChildPIDs(parentPID int, limit int) []int` (`liveness.go:210-245`) — Linux fast path reads `/proc/<pid>/task/<pid>/children`; falls back to `pgrep -P`. Returns `nil` on any failure (both paths failing), never an error.
- `IsAlive(pid int) bool` (`liveness.go:13-69`) — Linux fast path reads `/proc/<pid>/status`'s `State:` line, treating `Z`/`X`/`x` as not-alive; POSIX fallback uses `kill(pid, 0)` cross-checked against `ps -o state=` (needed because a bare signal-0 probe reports zombies as "alive" on macOS).

### 2. `internal/integrations/pt` — `HealthMonitor`, a real ticker but alert-only

`NewHealthMonitor(cfg *config.ProcessTriageConfig, opts ...HealthMonitorOption) *HealthMonitor` (`internal/integrations/pt/monitor.go:167-194`) builds a monitor wrapping `tools.NewPTAdapter()` (shells out to the external `pt` binary) and `tools.NewRanoAdapter()`.

`monitorLoop` (`monitor.go:289-308`) is a genuine ticker loop:
```go
func (m *HealthMonitor) monitorLoop() {
	defer close(m.doneCh)
	interval := time.Duration(m.config.CheckInterval) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	m.checkAll()
	for {
		select {
		case <-ticker.C:
			m.checkAll()
		case <-m.stopCh:
			return
		}
	}
}
```
`checkAll` (`monitor.go:310-419`) refreshes a PID map from tmux/`/proc` (via `internal/integrations/rano`), classifies all tracked PIDs in one call to `m.ptAdapter.ClassifyProcesses(ctx, pids)` (which shells out to `pt classify --json --pid ...`), and for each pane compares the returned classification against thresholds to emit `ClassificationStateChange`/`Alert` values.

`Classification` includes `ClassZombie = "zombie"` (`monitor.go:19-29`); it is produced solely by mapping the external `pt` binary's JSON `classification` field — there is no in-process (`/proc`, `syscall`) zombie detection inside `monitor.go` itself. Zombie alerts fire on every cycle a zombie is observed, with no duration threshold (unlike `ClassStuck`/`ClassIdle`, which require `duration >= threshold`) (`monitor.go:509-564`, specifically 528-539).

**No process termination anywhere in this package**: a search of every file in `internal/integrations/pt/` for `syscall.Kill`, `process.Kill`, `SIGKILL`, `SIGTERM`, `os.Process`, `.Kill(` returns zero matches. `Alert`/`AlertZombie` values are only ever sent on a channel and passed to registered callbacks (`sendAlert`/`emitAlert`, `monitor.go:566-615`).

Downstream consumers only report, never remediate:
- `internal/cli/serve.go:99-131` (`runServe`) wires `pt.InitGlobalMonitor(&ptCfg, WithStateChangeCallback(...), WithAlertCallback(...))`, forwarding every alert/state-change straight to `feed.PublishPTStateChange`/`feed.PublishPTAlert` (`internal/robot/attention_feed.go`).
- `PublishPTAlert` → `NewPTAlertAttentionEvent` (`attention_feed.go:1947-2016`); the `AlertZombie` case (`attention_feed.go:1993-2002`) produces an `AttentionEvent` with `Severity: SeverityError`, reason code `"pt:alert:zombie"`, next action `"Inspect the zombie agent output"` — an operator-facing notification, deduplicated over a 15-minute window, not an automated action.

**Correction to an earlier point made in this conversation before this research pass**: `pt.NewHealthMonitor` *is* called in production, via the package-level singleton accessors `pt.GetGlobalMonitor()`/`pt.InitGlobalMonitor()` (`monitor.go:672-694`), consumed from `internal/cli/serve.go:111`, `internal/tui/dashboard/commands.go:777`, and `internal/robot/agent_health.go:263`. It is gated on `ProcessTriageConfig.Enabled` (default `true`) and — inside `HealthMonitor.Start()` — on `m.ptAdapter.IsAvailable(ctx)` returning true (external `pt` binary present on `$PATH` and version-compatible; `monitor.go:210-213`). Whether it is *actually running* on any given machine therefore depends on both `ntm serve` being started and the `pt` binary being installed.

### 3. `ProcessTriageConfig` — a schema that already anticipates auto-kill, unimplemented

`internal/config/config.go:1574-1582`:
```go
type ProcessTriageConfig struct {
	Enabled        bool   `toml:"enabled"`
	BinaryPath     string `toml:"binary_path"`
	CheckInterval  int    `toml:"check_interval"`  // seconds, default 30
	IdleThreshold  int    `toml:"idle_threshold"`  // seconds, default 300
	StuckThreshold int    `toml:"stuck_threshold"` // seconds, default 600
	OnStuck        string `toml:"on_stuck"`        // "alert", "kill", or "ignore" — default "alert"
	UseRanoData    bool   `toml:"use_rano_data"`
}
```
`ValidateProcessTriageConfig` (`config.go:1598-1637`) enforces `OnStuck ∈ {"alert","kill","ignore"}` (`config.go:1631-1634`) alongside interval floors (`CheckInterval >= 5`, `IdleThreshold >= 30`, `StuckThreshold >= IdleThreshold`). A repo-wide search found `cfg.OnStuck`/`ProcessTriage.OnStuck` referenced only inside `internal/config/config.go` itself (validation, TOML export at `config.go:3267`, a `config get` accessor at `config.go:4428`, and a config-diff helper at `config.go:5447`) — never inside `internal/integrations/pt/monitor.go` or anywhere else that could act on it.

### 4. `internal/scheduler.HeadroomGuard` — unrelated dead code

`internal/scheduler/headroom.go` defines a `HeadroomGuard` with its own ticker-based recheck loop (`startRecheck`, `headroom.go:288-314`, `time.NewTicker` at `headroom.go:297`), gating on process-count limits (`ulimit -u`, cgroup `pids.max`, kernel `pid_max`) — not memory, not zombie/orphan detection. `NewHeadroomGuard` is only constructed inside `internal/scheduler/scheduler.go:282`, and no production code anywhere in the repository constructs a `scheduler.Scheduler` (`grep` for `scheduler.New(` across the whole module returns nothing outside `internal/scheduler/` itself). This subsystem is unrelated to orphan cleanup and was previously confirmed, in this same research thread's prior investigation this session, to be fully disconnected from the running system.

### 5. The only genuinely long-lived ntm process: `internal/serve` (`ntm serve`) — optional

`internal/cli/serve.go:22-69` defines `newServeCmd()` (`Use: "serve"`); `runServe` (`serve.go:86-225`) opens state, optionally starts the `pt.HealthMonitor`, builds `serve.Config`, constructs `srv := serve.New(serverCfg)` (`serve.go:164`), and blocks on `srv.Start(ctx)` (`serve.go:224`).

`(*Server).Start(ctx)` (`internal/serve/server.go:1100-1198`) is explicitly documented as blocking ("starts the HTTP server and blocks until shutdown"): it starts the WebSocket hub once (`sync.Once`-guarded), subscribes to the event bus/attention feed for broadcast, builds an `http.Server` with `WriteTimeout: 0` (to support long-lived SSE), runs `ListenAndServe` in a goroutine, then blocks on `select { case <-ctx.Done(): ...Shutdown(5s timeout)...; case err := <-errCh: return err }`.

Signal handling lives in `internal/cli/serve.go:194-207` (`signal.Notify(sigCh, SIGINT, SIGTERM)` → `cancel()` on receipt), not inside `internal/serve` itself.

A background goroutine already exists in `runServe` as direct precedent for "periodic work tied to server lifetime" (`internal/cli/serve.go:176-192`):
```go
go func() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, refreshCancel := context.WithTimeout(ctx, 10*time.Second)
			if err := robot.RefreshNormalizedProjection(refreshCtx, stateStore, "", ""); err != nil {
				slog.Warn("normalized projection refresh failed", "err", err)
			}
			refreshCancel()
		}
	}
}()
```
Started unconditionally, tied to the same cancellable `ctx` the signal handler cancels.

Other tickers exist inside `internal/serve` package code (`IdempotencyStore.cleanup`, 1 minute, lazily started on first cached response; `WSClient.writePump` ping, 54s, per-connection; adaptive SSE heartbeat, per-connection) but two others — `AuditStore.cleanupLoop` (24h default) and `WSEventStore.cleanupLoop` (5m default) — are defined with their own ticker loops but their constructors (`NewAuditStore`, `NewWSEventStore`) are **only called from test files**; `Server` has no field for either, and neither is wired into `Server.New`/`Start` as currently constructed.

Relationship to `ntm spawn`/`ntm kill`: confirmed these commands never import `net/http` or `internal/serve`, and never talk to a running server. `runKill` calls `kernel.Run(...)` directly (in-process dispatch to the same handler function `internal/serve`'s HTTP layer also calls, not an HTTP round-trip). Each CLI invocation opens its own local SQLite connection independently. Across `internal/cli`, only `serve.go` (the command itself), `openapi.go` (statically generates an OpenAPI doc from the router, never listens), and one test file import `internal/serve`. `internal/serve` is optional, opt-in infrastructure for external consumers (dashboards, robot/automation clients) — not something `ntm spawn`/`ntm kill` depend on or start.

### 6. The per-session daemon ntm already runs on every spawn: `ntm internal-monitor`

This is the one component in the codebase that is already (a) started automatically by `ntm spawn` with no config flag required, and (b) already running a ticker-driven loop for the entire lifetime of a session with orphan risk.

Command definition (`internal/cli/monitor.go:28-38`):
```go
func newMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-monitor <session>",
		Short:  "Run the resilience monitor for a session (internal use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMonitor(args[0])
		},
	}
}
```

`runMonitor(session string)` (`internal/cli/monitor.go:40-239`) — verified in full by direct read:
1. Loads the session's `resilience.SpawnManifest` (`monitor.go:42`).
2. Registers `SIGINT`/`SIGTERM` (`monitor.go:53-54`).
3. Retries session-existence up to 3 times before giving up (`monitor.go:58-71`).
4. Starts a `supervisor.Supervisor` and its `DefaultSpecs()` daemons — `cm serve` always, `am serve-http` only if `[agent_mail].supervisor_enabled = true` (`monitor.go:101-125`; `internal/supervisor/supervisor.go:673-713`).
5. Constructs a `resilience.Monitor` and calls `monitor.Start(ctx)` (`monitor.go:139-150`) — this itself launches a *further* ticker-driven goroutine (`internal/resilience/monitor.go:237`, `go m.monitorLoop(childCtx, done)`) for agent health/crash/restart detection.
6. Starts a CASS output archiver in the background (`monitor.go:152-165`, `go func() { archiver.Run(ctx) }()`).
7. Its own `select` loop (`monitor.go:180-238`), with two tickers:
   - `ticker := time.NewTicker(5 * time.Second)` (`monitor.go:170`) — polls `tmux.SessionExists(session)`; after `maxMisses = 5` consecutive misses (~25s), treats the session as permanently gone, stops the resilience monitor, generates an end-of-session summary, deletes the manifest, and returns (exiting the monitor process itself).
   - `snapshotTicker := time.NewTicker(30 * time.Second)` (`monitor.go:174`) — calls `captureSessionOutputs(session, lastOutputs)` (`monitor.go:277-294`) to snapshot pane output for the eventual end-of-session summary.
   - `case <-sigChan:` — graceful stop on signal, also generating a summary.

Spawn call site (`internal/cli/spawn.go:2490-2554`) — verified by direct read, comment quoted verbatim:
```go
// Start session monitor (handles resilience and daemons)
// Always started regardless of auto-restart config
// Note: Started BEFORE waiting for staggered prompts so that resilience is active
// even if the user interrupts the wait.
if shouldStartInternalMonitor() {
	...
	cmd, err := newInternalMonitorCommand(opts.Session)
	...
	setDetachedProcess(cmd)   // detach from terminal so it survives when ntm spawn exits
	if err := cmd.Start(); err != nil { ... }
```
`shouldStartInternalMonitor()` (`internal/cli/spawn.go:687-698`, verified by direct read) returns `true` in every real invocation — it only returns `false` under `go test` (`flag.Lookup("test.v") != nil`, to avoid recursively re-running the test binary) or when the escape-hatch env var `NTM_DISABLE_INTERNAL_MONITOR` is set. There is no ordinary config toggle for it.

`newInternalMonitorCommand(session string) (*exec.Cmd, error)` (`internal/cli/spawn.go:2961-2970`) builds `exec.Command(exe, "internal-monitor", session)` where `exe` is the currently-running `ntm` binary's own path (`currentExecutablePath()`, `spawn.go:2942`) — i.e. the monitor re-invokes the same `ntm` executable with the hidden subcommand. `killExistingMonitorProcess` (`spawn.go:2972-2980`) is called first if a stale monitor for the same session is already alive, to avoid duplicates.

Process-matching (used to find/kill this daemon later, e.g. from `ntm kill`) — `internal/cli/session.go:1783-1804`:
- `monitorProcessPatternForExecutable(executablePath, session string) string` (`session.go:1783`) builds a `pgrep -f`-style regex matching `<exe-basename> internal-monitor <session>`.
- `monitorProcessPattern(session string) string` (`session.go:1791`) wraps it with the current executable's own path.
- `isMonitorAlive(session string) bool` (`session.go:1799`) uses `pgrep -f <pattern>`.
- Both `runKill`'s non-JSON path (`internal/cli/send.go:2242`) and `buildKillResponse`'s JSON path (`internal/cli/send.go:2612`) call `pkill -f <monitorProcessPattern(session)>` before `tmux.KillSession` — so the per-session monitor *is* killed symmetrically on both kill paths, unlike the agent-subtree reap which only happens on one of them.

### 7. Established config convention for toggle+interval background features

Pattern shared across `HeadroomPacingConfig`, `RanoConfig`, `CautConfig`, `ProcessTriageConfig`, `FileReservationConfig`: an `Enabled bool` field plus one or more interval fields, a `Default*Config()` setting concrete defaults, and (for most of them) a `Validate*Config`/`validate*Config` enforcing an interval floor.

Example — `HeadroomPacingConfig` (`internal/config/pacing.go:72-91`):
```go
type HeadroomPacingConfig struct {
	Enabled         bool    `toml:"enabled"`
	MinFreeMB       int     `toml:"min_free_mb"`
	MinFreeDiskMB   int     `toml:"min_free_disk_mb"`
	MaxLoadAverage  float64 `toml:"max_load_average"`
	MaxOpenFiles    int     `toml:"max_open_files"`
	CheckIntervalMs int     `toml:"check_interval_ms"`
}
```
Default at `pacing.go:134-141` (`Enabled: true, CheckIntervalMs: 5000, ...`); validated conditionally on `.Enabled` at `pacing.go:194-199`, with the interval floor (`>= 100`) enforced in `validateHeadroomPacingConfig` (`pacing.go:256-273`). Real production consumers gating on `.Enabled` (added in this session's prior work): `internal/robot/spawn.go:141` and `internal/cli/spawn.go:1680`.

`ProcessTriageConfig` (`internal/config/config.go:1574-1595`) follows the same shape (`Enabled` + `CheckInterval` seconds, default 30, floor 5) — see §3 above. `RanoConfig` (`config.go:1641-1660`, `PollIntervalMs`, default 1000, floor 100) and `CautConfig` (`config.go:1549-1570`, `PollInterval` seconds, default 60, floor enforced in the consumer `internal/integrations/caut/poller.go:36-39` rather than a dedicated validator) follow the same convention with minor variation in where the floor is enforced.

Every one of these configs also gets: a TOML-export `fmt.Fprintf(w, "enabled = %t\n", ...)` line, a `config get <key>` accessor switch arm, and a diff-from-defaults `addDiff(...)` call, all in `internal/config/config.go`.

### 8. Pane/session enumeration API a sweep would iterate over

`GetAllPanes()` (`internal/tmux/session.go:556-558` → `Client.GetAllPanesContext`, `session.go:507-553`) issues a single `tmux list-panes -a -F "<session_name>...<pane_pid>...<window_index>...<@ntm_type>..."` call and returns `map[string][]Pane` keyed by session name — the system-wide, batched alternative to calling `GetPanes(session)` once per session. It swallows "no server running"/"no sessions" errors into an empty map rather than propagating them.

`Pane.PID` (`internal/tmux/session.go:72`, doc-commented `// Shell PID`) is populated directly from tmux's `#{pane_pid}` — codebase-wide, this is documented and treated as the pane's *shell* PID, not the agent binary's PID (e.g. `internal/resilience/monitor.go:38`: `ShellPID int // Shell PID from tmux #{pane_pid} — used for PID-based liveness checks`; `internal/robot/restart_pane.go:222-224` notes a pane respawn assigns a *new* shell PID). This is exactly why `collectPaneDescendants` (§1) walks the process tree from `p.PID` rather than treating it as the agent's own PID.

`Pane.Type` classification (`parsePaneFromParts`, `internal/tmux/session.go:1724-1741`) is a fallback chain: (1) title regex parse (`session__type_index[_variant][tags]`), (2) `#{pane_current_command}` matched against known agent binary names, (3) a bounded process-tree walk (`detectAgentFromProcessTree`, depth 4) when the immediate command is a wrapper (`bun`/`node`/`npx`/`deno`/`python`/shells) that could itself be launching an agent, (4) a final override from the pane's `@ntm_type`/`@ntm_index`/`@ntm_variant` tmux user options if set. `isSpawnAdmissionAgentPane` (`internal/robot/spawn.go:192-195`) treats a pane as an "agent" pane for counting purposes whenever its canonicalized `Type` is non-empty and not `AgentUser`.

Production (non-test) call sites of `tmux.GetAllPanes()` — exactly three, all using it as a batching optimization over per-session `list-panes` calls: `internal/robot/spawn.go:149` (spawn-admission input collection — added/extended in this session's prior work), `internal/robot/robot.go:7100` (building a normalized tmux projection), `internal/cli/session.go:529` (`ntm sessions list` response, tag filtering).

## Code References

- `internal/cli/send.go:2280-2298` — `orphanReapGrace`/`orphanReapMaxDepth`/`orphanReapFanout`/`orphanReapExcluded`
- `internal/cli/send.go:2300-2347` — `collectPaneDescendants`
- `internal/cli/send.go:2349-2379` — `reapOrphanProcesses`
- `internal/cli/send.go:2030-2044` — `runKill`'s JSON short-circuit to `kernel.Run("sessions.kill", ...)`
- `internal/cli/send.go:2215-2255` — non-JSON `runKill` flow: snapshot → `tmux.KillSession` → reap
- `internal/cli/send.go:204-218` — kernel handler registration for `"sessions.kill"` → `buildKillResponse`
- `internal/cli/send.go:2434-2663` — `buildKillResponse` (no reap call anywhere in this range)
- `internal/process/liveness.go:13-69` — `IsAlive`
- `internal/process/liveness.go:210-245` — `GetChildPIDs`
- `internal/integrations/pt/monitor.go:167-194` — `NewHealthMonitor`
- `internal/integrations/pt/monitor.go:289-308` — `monitorLoop` (ticker)
- `internal/integrations/pt/monitor.go:310-419` — `checkAll`
- `internal/integrations/pt/monitor.go:509-564` — `checkAlerts` (`AlertZombie` fires with no threshold)
- `internal/integrations/pt/monitor.go:672-694` — `GetGlobalMonitor`/`InitGlobalMonitor` singleton accessors
- `internal/cli/serve.go:99-131` — `runServe` wiring `pt.InitGlobalMonitor` callbacks into the attention feed
- `internal/robot/attention_feed.go:1993-2002` — `AlertZombie` → `AttentionEvent` (reporting only)
- `internal/config/config.go:1574-1595` — `ProcessTriageConfig` + `DefaultProcessTriageConfig`
- `internal/config/config.go:1598-1637` — `ValidateProcessTriageConfig` (`OnStuck ∈ {alert,kill,ignore}`, never consumed elsewhere)
- `internal/config/pacing.go:72-91` — `HeadroomPacingConfig` (toggle+interval pattern reference)
- `internal/config/pacing.go:134-148` — `DefaultSpawnPacingConfig`'s `Headroom`/`Backoff` defaults
- `internal/scheduler/headroom.go` — unrelated, unwired `HeadroomGuard` (process-count only, dead code)
- `internal/cli/serve.go:22-225` — `newServeCmd`/`runServe`
- `internal/cli/serve.go:176-192` — 15s normalized-projection refresh ticker (precedent for a server-lifetime periodic task)
- `internal/serve/server.go:1100-1198` — `(*Server).Start`, blocking main loop + graceful shutdown
- `internal/serve/audit.go:197-199,402-439` — `AuditStore.cleanupLoop` (defined, only constructed from tests)
- `internal/serve/ws_events.go:105-139` — `WSEventStore.cleanupLoop` (defined, only constructed from tests)
- `internal/cli/monitor.go:28-38` — `newMonitorCmd` (`internal-monitor`, hidden)
- `internal/cli/monitor.go:40-239` — `runMonitor` (full per-session daemon body, 5s/30s tickers)
- `internal/cli/spawn.go:687-698` — `shouldStartInternalMonitor`
- `internal/cli/spawn.go:2490-2554` — spawn call site launching the detached monitor process
- `internal/cli/spawn.go:2942,2961-2980` — `currentExecutablePath`/`newInternalMonitorCommand`/`killExistingMonitorProcess`
- `internal/cli/session.go:1783-1804` — `monitorProcessPattern*`/`isMonitorAlive`
- `internal/tmux/session.go:507-558` — `GetAllPanes`/`GetAllPanesContext`
- `internal/tmux/session.go:59-73` — `Pane` struct (`PID // Shell PID`)
- `internal/tmux/session.go:1724-1741` — `Pane.Type` fallback classification chain
- `internal/robot/spawn.go:109-163` — `collectSpawnAdmissionInput` (existing production consumer of `GetAllPanes`)
- `internal/robot/spawn.go:192-195` — `isSpawnAdmissionAgentPane`

## Architecture Documentation

- ntm has no cron library and no `--daemon` CLI flag anywhere in the module.
- Exactly two commands represent genuinely long-running processes: `ntm serve` (opt-in HTTP/SSE/WebSocket API server) and the hidden `ntm internal-monitor <session>` (per-session daemon, started automatically and unconditionally by every `ntm spawn`, detached so it survives the spawning process).
- Every other `ntm` command (`spawn`, `kill`, `send`, `status`, etc.) is short-lived: it runs, does its work directly against tmux/local SQLite state, and exits. Several commands (`ntm watch`, `ntm status --watch`, `ntm logs --follow`, `ntm assign --watch`, `ntm robot monitor`) block in a foreground polling loop until interrupted, but these are user-attended, not background daemons.
- The established shape for background/periodic feature config in `internal/config` is `Enabled bool` + one or more `*IntervalMs`/`*IntervalSec`/`PollInterval`/`CheckInterval` fields, a `Default*Config()`, and usually a validator enforcing an interval floor — five existing structs (`HeadroomPacingConfig`, `RanoConfig`, `CautConfig`, `ProcessTriageConfig`, `FileReservationConfig`) all follow this shape.
- Two ticker-driven cleanup loops already exist in `internal/serve` package source (`AuditStore.cleanupLoop`, `WSEventStore.cleanupLoop`) but their constructors are currently called only from test code, not from the running `ntm serve` process.

## Workflow Closure Map

This section maps the one behavior in the codebase that actually reaches the observable outcome "an orphaned agent process is terminated": the reactive reap triggered by `ntm kill` (non-JSON path). A second, separate behavior — zombie *detection* via `pt.HealthMonitor` — is also mapped for contrast; its chain is real and production-called, but its observable end is an operator-facing alert, not a terminated process. No chain in the current codebase connects a periodic tick to a terminated orphan process — that connection does not exist today, which is the premise of the research question ("wire up a periodic sweep").

### Chain A — `ntm kill` (non-JSON) reaps a surviving agent subtree

| Step | Node | Evidence |
|---|---|---|
| 1 (entrypoint) | `runKill` non-JSON branch invoked | `internal/cli/send.go:2030-2057` — `IsJSONOutput()` false falls through past the JSON short-circuit |
| 2 | Snapshot pane-shell descendant PIDs | `internal/cli/send.go:2235-2239` (`panePIDs` from `tmux.GetPanes`, then `collectPaneDescendants`) |
| 3 | Kill the tmux session (external `tmux` binary) | `internal/cli/send.go:2247` (`tmux.KillSession(session)`) — cross-process boundary into the `tmux` server |
| 4 | SIGTERM → 750ms grace → SIGKILL survivors | `internal/cli/send.go:2253` (`reapOrphanProcesses`) → `internal/cli/send.go:2360-2378`, using `process.IsAlive`/`syscall.Kill` |
| 5 (observable) | Process no longer alive | `process.IsAlive(pid)` returns `false` (`internal/process/liveness.go:13-43`); externally observable via `ps`/`/proc/<pid>` |

Labels: node 1 is `production-called` (it is `ntm kill`'s own default code path). All nodes are pre-existing (`adds_or_changes: false` — nothing in this research changed any of them). Depth 1→5 left to right. Registration point: this is a cobra `RunE` entrypoint (`internal/cli/send.go:2017`, the `kill` command), not a scheduler/queue registration — there is nothing async in this chain.

Negative evidence: `buildKillResponse` — the JSON/kernel-registered twin of this same operation — was confirmed (§1) to call `tmux.KillSession` without any of steps 2 or 4. It is therefore `production-called` (reachable via `ntm kill --json` and via `kernel.Run("sessions.kill", ...)`) but does **not** reach the same observable outcome; its chain ends at "tmux session killed," not "orphan process terminated."

### Chain B — `pt.HealthMonitor` detects a zombie and surfaces an alert (no termination)

| Step | Node | Evidence |
|---|---|---|
| 1 (entrypoint) | `ntm serve` started with `ProcessTriage.Enabled` and `pt` binary available | `internal/cli/serve.go:99-124` (`pt.InitGlobalMonitor` + `ptMonitor.Start()`) |
| 2 | Ticker fires every `CheckInterval` (default 30s) | `internal/integrations/pt/monitor.go:289-308` (`monitorLoop`) |
| 3 | Classify tracked PIDs via external `pt` binary | `internal/integrations/pt/monitor.go:335` (`m.ptAdapter.ClassifyProcesses`) — cross-process boundary into the external `pt` CLI |
| 4 | Zombie classification → immediate `AlertZombie` | `internal/integrations/pt/monitor.go:509,528-539` (`checkAlerts`, no threshold gate) |
| 5 (observable) | Alert surfaced to the attention feed (operator-visible, not process state) | `internal/cli/serve.go:117-123` (`WithAlertCallback` → `feed.PublishPTAlert`) → `internal/robot/attention_feed.go:1993-2002` (`AttentionEvent`, `ReasonCode: "pt:alert:zombie"`) |

Labels: node 1 is `production-called` but conditionally reachable (requires `ntm serve` running, `Enabled: true`, and the external `pt` binary installed and version-compatible). Node 3 crosses into an external binary the same way Chain A's node 3 crosses into `tmux`. This chain never reaches a "process terminated" observable — it terminates at an `AttentionEvent`, a fundamentally different node type than Chain A's `process.IsAlive() == false`.

### ClosureMap (structured — derive() input)

> This repository does not contain an `apps/closure-oracle` (confirmed: no directory or file matching `*closure-oracle*` exists anywhere under the repo root). The block below is produced per the research protocol's instructions regardless, mechanically derived from Chain A above (the chain whose observable is "process terminated," matching the research question's premise). It is speculative with respect to any downstream `derive()`/closure-test tooling unless such tooling is later added to this repository.

```json
{
  "behavior": "A survivor of ntm kill's tmux session teardown (an agent process still running under a now-dead pane shell) is terminated.",
  "git_commit": "be5f182f00d0903ed7a52494726f036b438fbff3",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "orphan_candidate_process", "module": "internal/process", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": "OS process table (/proc) — a live child process of a tmux pane shell, enumerable via process.GetChildPIDs" },
    { "id": "runkill_nonjson_entry", "module": "internal/cli/send.go:2030", "is_entrypoint": true, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "collect_pane_descendants", "module": "internal/cli/send.go:2312", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "tmux_kill_session", "module": "internal/tmux (external tmux binary)", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "reap_orphan_processes", "module": "internal/cli/send.go:2354", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "process_is_alive_false", "module": "internal/process", "is_entrypoint": false, "adds_or_changes": false, "read_path": "process.IsAlive (internal/process/liveness.go:13)", "seedable_store": null }
  ],
  "edges": [
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null }
  ]
}
```

No `highest_new_connector` — every node in this map is pre-existing (`adds_or_changes: false` throughout); this research pass introduced no code.

### Closure adapter (staged proposal)

> No closure adapter staged. `apps/closure-oracle` and its 7-op contract (`/seed`, `/trigger`, `/drive`, `/observe`, `/reset`, `/set_connector`, `/seed_sink`) are not present anywhere in this repository, and this chain's "store" (a live OS process under a tmux pane) does not fit the seed/read-path database paradigm the adapter scaffold assumes — seeding it would mean actually spawning a real child process under a real tmux pane, not writing a row. Producing a fabricated scaffold against nonexistent infrastructure would not be useful; if `apps/closure-oracle` is added to this repository later, Chain A's five nodes above give the exact symbols (`collectPaneDescendants`, `tmux.KillSession`, `reapOrphanProcesses`, `process.IsAlive`) an adapter would need to wire.

## Historical Context (from thoughts/)

None — the `thoughts/` directory did not exist in this repository before this research pass (`thoughts/searchable/shared/research/` was created to hold this document).

## Related Research

None found — this is the first document in `thoughts/searchable/shared/research/`.

## Open Questions

- Whether a periodic sweep, if built, should live inside the existing per-session `ntm internal-monitor` daemon's `select` loop (§6 — guaranteed running whenever a session with orphan risk exists, but one instance per session, so it would only ever see its own session's descendants) or as a new goroutine inside `internal/serve`'s `runServe` (§5 — a single instance could see all sessions via `tmux.GetAllPanes()`, but only runs while the optional `ntm serve` process is up) — this research documents both candidate locations and their tradeoffs as they exist today; it does not recommend between them, per the scope of this research pass.
- Whether `buildKillResponse`'s missing reap step (Chain A's negative-evidence finding) is considered a bug to fix independently of any periodic-sweep work, or whether a periodic sweep is intended to be the sole safety net going forward.
- Whether `ProcessTriageConfig.OnStuck`'s already-validated `"kill"` value is intended to eventually gate an actual termination action inside `pt.HealthMonitor`, or whether that value predates a design change and is now vestigial.
