---
date: 2026-11-06T09:41:22-05:00
researcher: Claude Opus 5.5
git_commit: 905820535176783780bcf57035f4e9687c43ce24
branch: main
repository: ntm
topic: "The pre-launch system check (spawn admission) — seams and grammar"
tags: [research, codebase, spawn, spawn-admission, pressure, headroom, memory, robot-spawn, config, preflight]
status: complete
last_updated: 2026-11-06
last_updated_by: Claude Opus 5.5
---

# Research: The pre-launch system check (spawn admission) — seams and grammar

**Date**: 2026-11-06T09:41:22-05:00
**Researcher**: Claude Opus 5.5
**Git Commit**: 905820535176783780bcf57035f4e9687c43ce24 (pushed; `main` == `origin/main`)
**Branch**: main
**Repository**: ntm (github.com/tha-hammer/ntm)

Permalink base for every `file:line` below:
`https://github.com/tha-hammer/ntm/blob/905820535176783780bcf57035f4e9687c43ce24/<file>#L<line>`

## Research Question

"We make a system check when a new session is about to launch. Research the seams and grammar so we can plan modifications."

Interpretation used: the system check is the **spawn admission** check — the resource check that decides `admit` / `defer` / `refuse` before ntm creates the tmux session. The research also lists the other pre-launch gates that sit next to it on the same path, because a modification will have to fit between them.

## Summary

- **One pure decision function.** `pressure.EvaluateSpawnAdmission` (`internal/pressure/pressure.go:207`) takes a `SpawnAdmissionInput` and returns a `SpawnAdmission`. It has no I/O. It is a five-rule, first-match-wins `switch` (`pressure.go:264-298`) that yields one of three decision tokens (`admit`, `defer`, `refuse`) and one of six reason strings.
- **Two production callers build the input differently.**
  - `--robot-spawn` (`internal/robot/spawn.go:443`) fills nearly every input field: agent caps, live CPU/memory/load pressure, live memory, the learned memory estimate, and a tmux census.
  - Human `ntm spawn` (`internal/cli/spawn.go:1751`) fills only the session, the counts, and the two memory fields. On this path only the `insufficient_memory` rule can stop a launch.
- **Reach.**
  - The REST endpoint `POST /sessions/{id}/agents/spawn` reaches the robot path with a nil config (`internal/serve/server.go:3669`).
  - `ntm resume` reaches the CLI path (`internal/cli/resume.go:352`).
  - No other launcher runs the check: swarm, `ntm add`, `ntm create`, ensemble, respawn, restart-pane, smart-restart, and context rotation all skip it.
- **The memory side learns.**
  - The per-agent memory estimate comes from `pressure.ResolveSpawnMemoryEstimate` (`internal/pressure/spawn_memory_resolver.go:40`). It reads a JSON sample file (`~/.local/share/ntm/agent-memory-samples.json`).
  - That file is written by the detached `ntm internal-monitor` process (`internal/cli/monitor.go:192`, `:887`).
  - With fewer than about 5 samples for an agent type, the estimate falls back to `config.PerAgentExpectedMemMB` (2048 MB default, `internal/config/templates.go:124`).
- **Config.** Admission reads the `[spawn_pacing]` / `[spawn_pacing.headroom]` TOML tables (`internal/config/pacing.go:11`, `:73`). Several headroom fields (`min_free_mb`, `min_free_disk_mb`, `max_load_average`, `max_open_files`, `check_interval_ms`) are defined and validated but are not read by either spawn path.
- **An unwired guard.** A separate `scheduler.HeadroomGuard` (`internal/scheduler/headroom.go:170`) checks process-slot headroom. It is not imported by any package outside `internal/scheduler`, so it is not on any launch path.

## Detailed Findings

### 1. The grammar: `SpawnAdmissionInput` → `EvaluateSpawnAdmission` → `SpawnAdmission`

**Decision tokens** (`internal/pressure/pressure.go:122-128`). These are robot-stable strings:

| Constant | Token |
|---|---|
| `SpawnAdmissionAdmit` | `admit` |
| `SpawnAdmissionDefer` | `defer` |
| `SpawnAdmissionRefuse` | `refuse` |

**Input** — `SpawnAdmissionInput` (`pressure.go:133-158`). It has no JSON tags, and a zero value means "not configured / skip".

| Field | Meaning | Robot sets it | CLI sets it |
|---|---|---|---|
| `Session` | echoed to output | yes | yes |
| `RequestedAgents` | agents in this spawn | yes | yes |
| `RequestedPanes` | agents + user pane | yes | yes |
| `SessionPanes` | panes already in the target session | yes (tmux census) | no |
| `CurrentPanes` | panes across all sessions | yes | no |
| `RunningAgents` | agent panes already running | yes | no |
| `RunningSessions` | session count | yes | no |
| `MaxAgents` | concurrent-agent cap (0 = none) | yes, if cfg non-nil | no |
| `LargeSpawnThreshold` | "large spawn" boundary (0 = never large) | yes, `DefaultBudget().MaxPipelineFanout` = 16 | no |
| `Pressure` | governor `Snapshot` | yes | no |
| `RequestedMemoryMB` | aggregate expected memory (0 = skip memory rule) | yes | yes |
| `AvailableMemoryMB` | live `MemAvailable` (0 = skip memory rule) | yes | yes |
| `MemoryEstimates` | per-type breakdown, passed through only | yes | yes |

**Derived values** (`pressure.go:208-262`):
- All ints are clamped to at least 0.
- `additional_panes = max(requested_panes - session_panes, 0)`.
- `projected_panes = current_panes + additional_panes`.
- `large_spawn = threshold > 0 && requested_agents >= threshold`.
- `postSpawnAgents = running + requested`.
- `agent_headroom = max(max_agents - postSpawnAgents, 0)`, set only when `max_agents > 0`.

**Decision rules** (`pressure.go:228-298`). The default before the switch is `admit` / `headroom_available` (`:228-230`). Rules are checked in order and the first match wins:

| # | Condition (line) | Decision | Reason | Hint |
|---|---|---|---|---|
| 1 | `requested_agents == 0 \|\| requested_panes == 0` (`:265`) | refuse | `invalid_request` | `specify at least one agent` |
| 2 | `max_agents > 0 && running + requested > max_agents` (`:269`) | refuse | `agent_limit_exceeded` | `running N + requested M exceeds cap C; reduce requested agents or raise spawn_pacing agent caps` |
| 3 | `available > 0 && requested_mem > available - 2048` (`:276`) | refuse | `insufficient_memory` | `N agent(s) could demand up to XMB but only YMB is available; reduce requested agents or free memory` |
| 4 | `large_spawn && level >= critical` (`:290`) | refuse | `pressure_critical` | `reduce spawn size or wait for headroom: <sources>` |
| 5 | `large_spawn && level >= high` (`:294`) | defer | `pressure_high` | same as row 4 |
| — | none matched | admit | `headroom_available` | (empty) |

Notes on the rules:
- **Memory reserve.** `memoryReserveMB = 2048` (`pressure.go:201`) is held back from available memory.
- **Hint builder.** The hint for rows 4-5 comes from `recommendation(ActionSwarmSpawn, limiting)` (`internal/pressure/governor.go:231`, text at `:245-246`). It returns `""` when there are no limiting sources (`:232-234`).
- **No reason constants.** Reason strings are inline literals at `pressure.go:230, 267, 271, 285, 292, 296`.
- **Report-only fields.** The pane counts (`session_panes`, `current_panes`, `projected_panes`) are reported but no rule reads them.
- **Pressure level.** `effectiveSpawnPressure` (`pressure.go:308`) computes the level and limiting list while ignoring the `proc_count` source. `proc_count` stays visible in `sources` as a diagnostic (also stated in `docs/robot-section-model.md:291-301`).

**Output** — `SpawnAdmission` (`pressure.go:162-185`). JSON keys, in order:
- `decision`
- `reason`
- `hint` (omitempty)
- `session` (omitempty)
- `requested_agents`
- `requested_panes`
- `session_panes`
- `additional_panes`
- `current_panes`
- `projected_panes`
- `running_agents`
- `running_sessions`
- `max_agents` (omitempty)
- `agent_headroom` (omitempty)
- `large_spawn`
- `large_spawn_threshold` (omitempty)
- `requested_memory_mb` (omitempty)
- `available_memory_mb` (omitempty)
- `pressure_level`
- `limiting` (omitempty)
- `sources` (omitempty; `SpawnAdmissionSource{source,value,unit,level}`, `pressure.go:189`)
- `memory_estimates` (omitempty; `AgentMemoryEstimate{type,count,estimate_mb,source,confidence,sample_count}`, `spawn_memory_resolver.go:13`)

**Pressure levels and thresholds:**
- Level strings (`pressure.go:45-60`): `low`, `normal`, `elevated`, `high`, `critical`, `unknown`.
- Default thresholds (`pressure.go:81-92`), as elevated / high / critical:
  - cpu: 0.60 / 0.80 / 0.92
  - memory: 0.65 / 0.82 / 0.92
  - load: 0.75 / 1.00 / 1.50
  - proc_count: 0.70 / 0.85 / 0.95

### 2. Where the inputs come from (live readers)

**`AvailableMemoryMB()`** (`internal/pressure/provider.go:238`)
- Reads `MemAvailable` from `/proc/meminfo` only. There is no cgroup read and swap is excluded.
- Returns `(0,false)` when the file or the line is missing. When the result is `false`, both callers skip the memory rule; the CLI caller skips admission entirely.

**`collectSystemPressureSnapshot()`** (`internal/robot/spawn.go:217`)
- Builds a `pressure.New` governor in `ModeEnforce` with one `NewSystemProvider()` (`provider.go:83`) and calls `Refresh` with a 250 ms timeout.
- The provider reads:
  - load: `/proc/loadavg` ÷ NumCPU
  - memory used ratio: from `/proc/meminfo`
  - proc_count: the user's processes from `/proc/*/status`
  - cpu: two `/proc/stat` samples taken 50 ms apart
- Only the snapshot is consumed. `Governor.Gate` and `Mode` have no effect on admission.

**tmux census** (`internal/robot/spawn.go:166-181`)
- Calls `tmux.GetAllPanes()` and sets `RunningSessions`, `SessionPanes`, `CurrentPanes` and `RunningAgents`.
- A pane counts as an agent when its canonical type is neither empty nor `user` (`:227-230`).
- On error the function returns early with these fields at 0.

**Agent cap** — `spawnAdmissionAgentLimit` (`internal/robot/spawn.go:203`)
- Sums `ClaudeMaxConcurrent + CodexMaxConcurrent + GeminiMaxConcurrent`, counting only caps above 0. With defaults this is 3 + 2 + 2 = 7 (`internal/config/pacing.go:144-156`).
- `AgentPacingConfig` has no field for Antigravity or other agent types.
- `RunningAgents`, however, counts every non-user agent pane.

### 3. Memory estimation (the learned part)

**`ResolveSpawnMemoryEstimate(ctx, counts, overrideMB, floorMB, ceilingMB, store)`** (`internal/pressure/spawn_memory_resolver.go:40-86`)
- **Override path.** When `overrideMB > 0` (config `per_agent_expected_mem_mb` set), every type uses the override with source `override`. The store is not read.
- **Sampled path.** Otherwise it calls `store.Load` once. A load error is swallowed and every type gets zero samples. Each type's estimate then comes from `EstimateAgentMemMB(samples, floor, ceiling)` (`internal/pressure/agent_memory_estimate.go:43`):
  - **Confidence.** `ln(1+n)/ln(101)`; below 0.35 the result is the floor with source `fallback` (`calibration.go:10`, `:385-390`). By the curve, that threshold is first crossed at about n = 5. This is computed, not confirmed by a test.
  - **Estimate.** Otherwise it is p90 of `PeakBytes` × 1.25, clamped to `[floor, ceiling]`, with source `calibrated`.
- **Arguments both callers pass:**
  - floor = `config.PerAgentExpectedMemMB(cfg)` (`internal/config/templates.go:132`; default 2048, `:124`)
  - ceiling = `config.PerAgentMemLimitMB()` (`templates.go:111`; the systemd MemoryMax ceiling)
  - store = `pressure.NewFileMemorySampleStore()`

**Sample store** (`internal/pressure/memory_store.go`)
- **Path.** `$XDG_DATA_HOME/ntm/agent-memory-samples.json`, else `~/.local/share/ntm/agent-memory-samples.json` (`MemorySampleStorePath`, `:43`).
- **Format.** JSON array of `MemorySample{pane_id, agent_type, peak_bytes, observed_at, generation}` (`:24-30`).
- **Append rules.**
  - The dedup key is `(pane_id, agent_type, generation)`.
  - Each type keeps its newest 50 samples (`:19`).
  - Writes go through `util.AtomicWriteFile` with mode 0600.
- **Locking.** A process-wide mutex plus a Unix `flock` on `<path>.lock`: shared for Load, exclusive for Append (`memory_store_lock_unix.go:17-48`).

**Who writes samples** (the producer side of the learning loop)
- The detached `ntm internal-monitor <session>` process (`internal/cli/monitor.go:34`) wires:
  - `OnAgentGenerationEnded: productionOnAgentGenerationEnded(store)` (`:192`, body `:844-850`)
  - `FinalizeMemorySamples: productionFinalizeMemorySamples(store)` (`:887`, body `:855-864`)
- Both convert an `agentScopePeak` (cgroup `memory.peak`) via `agentScopePeakToMemorySample` (`:831-839`). Both are best-effort: store errors are logged and swallowed.
- **CLI spawn** launches the monitor at `internal/cli/spawn.go:2563-2585` whenever `resilience.ShouldStartInternalMonitor()` holds. That function (`internal/resilience/monitor_launch.go:22-30`) is false under `go test` and when `NTM_DISABLE_INTERNAL_MONITOR` is set.
- **Robot spawn** launches it only when `spawn_pacing.headroom.robot_monitor_enabled = true` (`internal/robot/spawn.go:264-266`, `:681-683`). That setting defaults to false.

### 4. Robot path — `--robot-spawn` and REST

**Entry wiring**
- `--robot-spawn` flag: `internal/cli/root.go:3449`.
- Dispatch: `root.go:1668-1698`, which calls `robot.PrintSpawn(opts, cfg)` (`:1693`). A Go error exits 1.
- `PrintSpawn` (`internal/robot/spawn.go:714`) calls `GetSpawn` (`:303`) and then encodes the JSON.

**Order inside `GetSpawn` up to session creation:**
1. Validate the project name and label, then format the session name (`:312-332`).
2. Write audit start and finish events (`:342`, `:354-382`).
3. Validate the tmux session name (`:385`) and check that tmux is installed (`:392`).
4. With `Safety` set, an existing session is an error (`:399-403`).
5. Resolve the working directory: opts → `cfg.GetProjectDir` → cwd (`:406-419`).
6. Load the handoff recovery context (`:423-426`).
7. Count agents, where 0 is an error, and count panes (`:430-441`).
8. **Run admission** (`:443`). The result is always stored in `output.Admission` (`:444`).
9. **Refusal gate** (`:445-453`), when `!DryRun && decision != admit`:
   - `output.Error = "spawn admission <decision>: <reason>"`
   - hint = `admission.Hint`, or the fallback `Reduce requested agents or wait for resource headroom`
   - `NewErrorResponse(..., ErrCodeResourceBusy, hint)`, where `ErrCodeResourceBusy = "RESOURCE_BUSY"` (`internal/robot/types.go:111`)
   - `return output, nil`, so the process does not exit 1
   - Defer and refuse take the same branch and differ only in `admission.decision`, `reason` and `hint`.
10. **Dry run** (`:456-528`) returns before any filesystem or tmux change. A non-admit decision under dry run is `success:true` with `admission` populated.
11. `MkdirAll`, plus `git.InitRepository` when the directory was new (`:531-544`).
12. `tmux.CreateSessionWithHistoryLimit` (`:548-560`).

**Gating inside `collectSpawnAdmissionInput`** (`:114-183`):
- The caps, pressure snapshot and memory block run only when `cfg == nil || cfg.SpawnPacing.Enabled` (`:121`).
- Within that, the memory reads also need `cfg == nil || cfg.SpawnPacing.Headroom.Enabled` (`:147`).
- The tmux census always runs (`:166`).

**REST** (`internal/serve/server.go:1035` route, handler `:3638`)
- Calls `robot.GetSpawn(opts, nil)` (`:3669`).
- With `cfg == nil`: there is no `MaxAgents` cap, the threshold is 16, and the pressure and memory checks are on.
- Admission refusals come back inside an HTTP 200 body with `success:false`.

### 5. CLI path — `ntm spawn` (and `ntm resume`)

**`RunE` gates** (`internal/cli/spawn.go:1130-1404`). These return plain errors with no JSON envelope:
- project name has no `--` (`:1134`)
- `--label` (`:1139`)
- session name and project directory (`:1146`)
- `--interactive` wizard (`:1152`)
- `--persona`, `--recipe`, `--template` (`:1183-1242`)
- Ollama spec flags (`:1245`)
- `--local-fallback` provider (`:1249`)
- default agents from `ProjectDefaults` (`:1264`)
- `--profiles` / `--profile-set` (`:1276-1318`)
- `--marching-orders` (`:1322`)
- profile distribution (`:1337`)
- `--profile` (`:1393`)

It then calls `spawnSessionLogic(opts)` (`:1403`).

**`spawnSessionLogic`** (`spawn.go:1549`) gates, in order:

| Step | Lines | What it checks | On fail | Skip |
|---|---|---|---|---|
| A | 1559 | `tmux.EnsureInstalled()` | `outputError` | — |
| B | 1563 | `tmux.ValidateSessionName` | `outputError` | — |
| D | 1569-1578 | `preflightOllamaSpawn` (`:3080`): model name pattern, `ListModels` with 10 s timeout, pull prompt in human mode | abort, or `handleOllamaPreflightError` (`:638`) falls back to `--local-fallback` provider | no Ollama agents |
| E | 1580 | `--safety`: session must not exist | `outputError` | without `--safety`, an existing session is reused |
| F | 1591 | `preflightCodexAccountSupport` (`:4432`): `codex login status`, 5 s timeout, for `gpt-*-codex` models | warn by default; block only if `NTM_CODEX_PREFLIGHT_STRICT` | `NTM_DISABLE_CODEX_PREFLIGHT`, `go test`, no matching codex model |
| G | 1595-1604 | total agents > 0 | `outputError` | — |
| H | 1606 | `resolveSpawnProjectDir` (override must be absolute) | `outputError` | — |
| J | 1647 | test pacing env vars (`NTM_TEST_MODE` / `NTM_E2E` only) | `outputError` | — |
| K-L | 1653-1697 | hooks config load; `pre-spawn` hooks with 5 min timeout | `pre-spawn hook failed: …` | `--no-hooks` |
| M | 1700-1718 | project directory exists: JSON mode auto-creates it; human mode prompts `Create it?` and decline returns `nil` ("Aborted.", exit 0); `initCreatedProjectGit` for new dirs (`internal/cli/project_git.go:14`) | `outputError` on mkdir failure | — |
| **O** | **1737-1763** | **spawn admission (memory only)** | `spawn admission refused: <reason> (<hint>)` | `Headroom.Enabled=false`, or `AvailableMemoryMB` not ok |
| P | 1767-1780 | `tmux.CreateSessionWithHistoryLimit` — first tmux mutation | `creating session: …` | session already exists |

How step O works:
- It sits entirely inside `if cfg == nil || cfg.SpawnPacing.Headroom.Enabled` and `if avail, ok := pressure.AvailableMemoryMB(); ok` (`:1737-1738`).
- The input passes only `Session`, `RequestedAgents`, `RequestedPanes`, `RequestedMemoryMB`, `AvailableMemoryMB` and `MemoryEstimates` (`:1751-1758`).
- Only `Decision == Refuse` aborts (`:1759`). With `LargeSpawnThreshold` and `MaxAgents` both 0, and step G already guaranteeing at least one agent, `insufficient_memory` is the only rule that can fire here.
- `spawnMemoryEstimateCounts` (`:554`) groups `opts.Agents` by canonical type, or uses the legacy count fields.

**Error output** — `outputError` (`spawn.go:1551-1557`):
- JSON mode: prints `output.NewError(msg)` and returns the error.
- Human mode: returns the error, and `root.Execute` prints `Error: …` to stderr (`internal/cli/root.go:2480-2482`).

**`ntm resume`** (`internal/cli/resume.go:340-353`) builds `SpawnOptions{..., UserPane:true, NoHooks:true}` and calls `spawnSessionLogic`. It therefore passes through gates A-P except the hooks.

### 6. Launch entrypoints that do not run admission

Verified by grep for `pressure`, `headroom`, `admission`, `AvailableMemory`, `MaxConcurrent` in each file:

| Entrypoint | Entry |
|---|---|
| `ntm add` | `internal/cli/add.go:344` (`SplitWindow` `:526`) |
| swarm | `internal/cli/swarm.go:293` → `internal/swarm/orchestrator.go:739` → `pane_launcher.go:373` |
| auto respawner | `internal/swarm/auto_respawner.go:409` |
| context rotation | `internal/context/rotation.go:182` |
| robot restart-pane / smart-restart | `internal/robot/restart_pane.go:75`, `smart_restart.go:168` |
| `ntm respawn` | `internal/cli/respawn.go:56` |
| ensemble spawn | `internal/cli/ensemble_spawn.go:200` → `internal/ensemble/manager.go:77` |
| `ntm create` / REST create session | `internal/cli/create.go:61`; `internal/serve/server.go:3010` |
| session / checkpoint restore | `internal/session/restore.go:117`; `internal/checkpoint/restore.go:216` |

### 7. Config surface (`[spawn_pacing]`)

`SpawnPacing SpawnPacingConfig` lives at `internal/config/config.go:96`. The structs are in `internal/config/pacing.go`. Defaults are at `:135-171` and validation at `:176-230`; validation is skipped entirely when `enabled=false`, and headroom is validated only when `headroom.enabled`.

| TOML key | Default | Read by admission? |
|---|---|---|
| `spawn_pacing.enabled` | true | robot: gates caps, pressure and memory (`robot/spawn.go:121`) |
| `spawn_pacing.agent_caps.{claude,codex,gemini}_max_concurrent` | 3 / 2 / 2 | robot: summed into `MaxAgents` |
| `spawn_pacing.headroom.enabled` | true | both: gates the memory reads |
| `spawn_pacing.headroom.per_agent_expected_mem_mb` | 0 → 2048 | both: override, and also the floor via `PerAgentExpectedMemMB` |
| `spawn_pacing.headroom.robot_monitor_enabled` | false | robot: launches the sample-producing monitor |
| `headroom.min_free_mb` / `min_free_disk_mb` / `max_load_average` / `max_open_files` / `check_interval_ms` | 512 / 1024 / 8.0 / 50000 / 5000 | no (defined and validated only) |

Not configurable today (hardcoded):
- `memoryReserveMB` = 2048 (`pressure.go:201`)
- large-spawn threshold = `DefaultBudget().MaxPipelineFanout` = 16 (`budget.go:45`)
- pressure thresholds (`pressure.go:81-92`)
- confidence floor 0.35 (`calibration.go:10`)
- p90 × 1.25 (`agent_memory_estimate.go:20-22`)
- 50 samples per type (`memory_store.go:19`)
- 250 ms snapshot timeout (`robot/spawn.go:218`)

### 8. The separate, unwired `scheduler.HeadroomGuard`

- **What it checks.** `internal/scheduler/headroom.go` checks process-slot headroom: the smallest of `ulimit -u`, cgroup `pids.max`, systemd `TasksMax` and `pid_max`, compared against current usage. `CheckHeadroom` is at `:170` and `evaluate` at `:200-253`.
- **Defaults** (`:43-52`): threshold 0.75, warn 0.70, min 50 free slots.
- **Where it is used.** It is constructed only in `Scheduler.rebuildHeadroomGuard` (`internal/scheduler/scheduler.go:277-282`) and called at `scheduler.go:687`.
- **Not imported.** Grep for `ntm/internal/scheduler"` finds no importers. The only reference outside the package is a comment at `internal/pressure/provider.go:131`.
- **Separate config.** `scheduler.HeadroomConfig` is a different struct from `config.HeadroomPacingConfig`, and nothing maps one to the other.

### 9. Tests that exercise the check

- **Pure rules** (`internal/pressure/pressure_test.go`):
  - `TestEvaluateSpawnAdmission_AdmitsWithHeadroom` (`:105`)
  - `_DefersLargeSpawnUnderHighPressure` (`:141`)
  - `_RefusesCriticalPressure` (`:167`)
  - `_AdmitsWhenOnlyProcessCountIsCritical` (`:187`)
  - `_DefersAtRealHostPressureDespiteCriticalProcessCount` (`:225`)
  - `_RefusesAgentLimit` (`:256`)
  - `_RefusesWhenRunningPlusRequestedExceedsCap` (`:280`)
  - `_AdmitsWhenRunningPlusRequestedFitsCap` (`:311`)
  - `_AdmitsAtExactCap` (`:330`)
  - No test is named for the `insufficient_memory` or `invalid_request` rows. Their strings were not grepped across test files, so indirect coverage is not ruled out.
- **Estimator and store:**
  - `internal/pressure/spawn_memory_resolver_test.go` (6 tests, `:44-163`)
  - `agent_memory_estimate_test.go` (`TestEstimateAgentMemMB` `:23`)
  - `memory_store_test.go`, including the cross-process concurrency test at `:252`
- **Config:** `TestPerAgentExpectedMemMB_DefaultsAndOverride` (`internal/config/templates_test.go:546`).
- **Robot path** (`internal/robot/spawn_test.go`):
  - `TestSpawnOptions_DryRunIncludesAdmission` (`:599`)
  - `TestSpawnOptions_DryRunAdmissionRefusesAgentCap` (`:642`)
  - Most robot tests set `Headroom.Enabled=false` (`:274`, `:305`, `:622`, `:768`) and hoist caps to 1024.
  - The pressure snapshot and tmux census are not injectable. No test sets `SpawnPacing.Enabled=false`.
- **CLI path:**
  - No `internal/cli` test exercises the step O block.
  - `internal/cli/testmain_test.go:30-34` sets `Headroom.Enabled=false` for the whole package.
  - Gate tests that do exist cover Ollama (`spawn_test.go:488-739`), Codex preflight (`codex_preflight_test.go`), counts, and the project dir.
- **Monitor sample writers:** `internal/cli/monitor_test.go`.
- **E2E:** `tests/e2e/robot_mode_test.go:398` `TestRobotSpawn`.

## Code References

Seams, meaning the symbols a modification would touch:

- `internal/pressure/pressure.go:122-128` — decision tokens
- `internal/pressure/pressure.go:133` — `SpawnAdmissionInput`
- `internal/pressure/pressure.go:162` — `SpawnAdmission` (JSON contract)
- `internal/pressure/pressure.go:189` — `SpawnAdmissionSource`
- `internal/pressure/pressure.go:201` — `memoryReserveMB`
- `internal/pressure/pressure.go:207` — `EvaluateSpawnAdmission`
- `internal/pressure/pressure.go:264-298` — the rule switch
- `internal/pressure/pressure.go:308` — `effectiveSpawnPressure`
- `internal/pressure/pressure.go:338` — `spawnAdmissionSources`
- `internal/pressure/governor.go:231` — `recommendation` (hint text)
- `internal/pressure/spawn_memory_resolver.go:40` — `ResolveSpawnMemoryEstimate`
- `internal/pressure/agent_memory_estimate.go:43` — `EstimateAgentMemMB`
- `internal/pressure/provider.go:238` — `AvailableMemoryMB`
- `internal/pressure/provider.go:83` — `NewSystemProvider`
- `internal/pressure/memory_store.go:43` — `MemorySampleStorePath`
- `internal/pressure/memory_store.go:73` — `NewFileMemorySampleStore`
- `internal/pressure/budget.go:42` — `DefaultBudget`
- `internal/robot/spawn.go:114` — `collectSpawnAdmissionInput`
- `internal/robot/spawn.go:203` — `spawnAdmissionAgentLimit`
- `internal/robot/spawn.go:217` — `collectSystemPressureSnapshot`
- `internal/robot/spawn.go:303` — `GetSpawn`
- `internal/robot/spawn.go:443-453` — robot refusal gate
- `internal/robot/spawn.go:714` — `PrintSpawn`
- `internal/cli/spawn.go:1549` — `spawnSessionLogic`
- `internal/cli/spawn.go:1737-1763` — CLI admission block
- `internal/cli/spawn.go:554` — `spawnMemoryEstimateCounts` (CLI)
- `internal/cli/spawn.go:3080` — `preflightOllamaSpawn`
- `internal/cli/spawn.go:638` — `handleOllamaPreflightError`
- `internal/cli/spawn.go:4432` — `preflightCodexAccountSupport`
- `internal/cli/resume.go:352` — resume enters `spawnSessionLogic`
- `internal/serve/server.go:3638` — `handleAgentSpawnV1` (nil cfg at `:3669`)
- `internal/config/pacing.go:11` — `SpawnPacingConfig`
- `internal/config/pacing.go:73` — `HeadroomPacingConfig`
- `internal/config/pacing.go:176` — `ValidateSpawnPacingConfig`
- `internal/config/templates.go:111` — `PerAgentMemLimitMB` (ceiling)
- `internal/config/templates.go:132` — `PerAgentExpectedMemMB` (estimate / floor)
- `internal/cli/monitor.go:192`, `:887` — memory-sample producers
- `internal/scheduler/headroom.go:170` — `CheckHeadroom` (not on any launch path)

All 32 symbol citations above that name a function or type were verified with the Semgrep citation verifier (32/32 `verified`). The admission switch, both call sites, the monitor writers, the resume call and the config defaults were also confirmed with targeted reads.

## Architecture Documentation

- **Pure core, impure shells.**
  - `EvaluateSpawnAdmission` is documented as pure "so robot, session, and future scheduler callers can share the same threshold behavior" (`pressure.go:203-206`).
  - Each caller gathers its own live readings and decides what to do with a non-admit result.
  - The robot path treats any non-admit as a failure. The CLI path aborts only on `refuse`.
- **Zero means "not configured".** Every optional input uses 0 to switch its rule off. Rules 2-5 are activated by the caller choosing to fill a field.
- **Config-flag opt-outs, not dependency injection.** Live reads (`/proc`, tmux, the sample file) are skipped in tests through `Headroom.Enabled=false` and high caps, not through injected functions. The monitor launch is the exception: it uses an injected deps struct (`robotMonitorLaunchDeps`, `robot/spawn.go:238`).
- **Ceiling vs expected.** `PerAgentMemLimitMB` (the systemd MemoryMax ceiling) and `PerAgentExpectedMemMB` (the typical-use estimate) are kept as separate values. The estimator clamps between them (see Historical Context).
- **Best-effort learning loop.** Sample writes in the monitor log and swallow errors. Store read errors in the resolver fall back to the static floor. Admission never fails because of the sample store.
- **Robot contract.** The admission result is a nested, `omitempty` `admission` object on `SpawnOutput` (`robot/spawn.go:75`). Refusal is signalled by `success:false` + `error_code:"RESOURCE_BUSY"` + `hint`, with exit code 0.

## Workflow Closure Map

**Behavior:** when a spawn is requested, ntm decides `admit` / `defer` / `refuse` from live host readings and the learned per-agent memory samples, and that decision becomes visible to the caller before any tmux session exists.

There are two sibling chains: robot (A) and CLI (B). They share the source store and the pure core.

**Upstream producer (outside both chains, seeded in tests).** The `ntm internal-monitor` process polls cgroup `memory.peak` and calls `store.Append` (`internal/cli/monitor.go:844-864`).
- This is an asynchronous, cross-process writer.
- It is launched by CLI spawn (`spawn.go:2563`), and by robot spawn only when `robot_monitor_enabled`.
- Its errors are logged and swallowed.
- Tests: `internal/cli/monitor_test.go`.

### Chain A — robot spawn (`--robot-spawn`, REST agents/spawn)

| Depth | Node | Location | Label | Adds/changes |
|---|---|---|---|---|
| 0 | memory sample store (SOURCE) | `internal/pressure/memory_store.go:73` (`NewFileMemorySampleStore`), file `~/.local/share/ntm/agent-memory-samples.json` | production-called | not yet specified |
| 1 | `ResolveSpawnMemoryEstimate` | `internal/pressure/spawn_memory_resolver.go:40` | production-called (robot `:154`, cli `:1743`) | not yet specified |
| 2 | `collectSpawnAdmissionInput` | `internal/robot/spawn.go:114` | production-called (`:443`) | not yet specified |
| 3 | `EvaluateSpawnAdmission` | `internal/pressure/pressure.go:207` | production-called (robot `:443`, cli `:1751`) | not yet specified |
| 4 | `GetSpawn` refusal gate (ENTRYPOINT) | `internal/robot/spawn.go:303`, gate `:445-453` | production-called (`PrintSpawn` `:714`; REST `server.go:3669`) | not yet specified |
| 5 | `PrintSpawn` JSON `admission` + `error_code` (OBSERVABLE) | `internal/robot/spawn.go:714` | production-called (`root.go:1693`) | not yet specified |

Edge evidence:

- **0→1.**
  - Producer `memory_store.go:105-122` (Load); consumer `spawn_memory_resolver.go:63-85`.
  - Contract: the `[]MemorySample` JSON array, grouped by `agent_type`.
  - Runtime context: shared `flock` plus a process mutex.
  - Errors: a load error is swallowed, giving every type the `fallback` floor.
  - Tests: `spawn_memory_resolver_test.go` `_LoadFailureFallsBackForEveryType` (`:135`). Synchronous.
- **1→2.**
  - Caller `robot/spawn.go:154-161`.
  - Additional live inputs on this edge: `AvailableMemoryMB` (`:148`) and the config floor and ceiling.
  - Registration: none (direct call).
  - Errors: if `AvailableMemoryMB` is not ok, the memory fields stay 0 and the rule is skipped.
  - Tests: none that run this edge with live memory (tests set `Headroom.Enabled=false`). Synchronous, cross-package.
- **2→3.**
  - Caller `robot/spawn.go:443`.
  - Contract: `SpawnAdmissionInput`, where zero values disable rules.
  - Additional live inputs on this edge: the governor snapshot (`:134`, 250 ms) and the tmux census (`:166`).
  - Errors: a census error returns a partially filled input.
  - Tests: `TestSpawnOptions_DryRunAdmissionRefusesAgentCap` (`spawn_test.go:642`). Synchronous, cross-package.
- **3→4.**
  - Caller `robot/spawn.go:444-453`.
  - Contract: `SpawnAdmission.Decision != admit` and `!DryRun` lead to `RESOURCE_BUSY`.
  - Tests: `TestSpawnOptions_DryRunIncludesAdmission` (`:599`). No test was found for the non-dry-run refusal branch. Synchronous.
- **4→5.**
  - `PrintSpawn` encodes `SpawnOutput` (`:714-720`). The REST handler instead wraps it in an HTTP 200 body (`server.go:3681`).
  - Tests: `TestPrintSpawn` (`spawn_test.go:348`) and the e2e test `tests/e2e/robot_mode_test.go:398`. Synchronous.

`highest_new_connector`: **not yet determined.** The research request names no specific modification, so every node is tagged "not yet specified". The planning step sets `adds_or_changes` once the change is chosen.

### Chain B — human CLI spawn (`ntm spawn`, `ntm resume`)

| Depth | Node | Location | Label |
|---|---|---|---|
| 0 | memory sample store (SOURCE) | `internal/pressure/memory_store.go:73` | production-called |
| 1 | `ResolveSpawnMemoryEstimate` | `internal/pressure/spawn_memory_resolver.go:40` | production-called (`cli/spawn.go:1743`) |
| 2 | `EvaluateSpawnAdmission` | `internal/pressure/pressure.go:207` | production-called (`cli/spawn.go:1751`) |
| 3 | `spawnSessionLogic` refusal → `outputError` (ENTRYPOINT + OBSERVABLE) | `internal/cli/spawn.go:1549`, gate `:1759-1761` | production-called (`spawn.go:1403`, `resume.go:352`) |

Edge evidence:

- **1→2** and **2→3** are synchronous direct calls with no registration.
  - The whole chain is skipped when `Headroom.Enabled=false` or `AvailableMemoryMB` is not ok.
  - The observable is the returned error `spawn admission refused: <reason> (<hint>)`: a JSON error envelope in `--json` mode, or `Error: …` on stderr.
  - Tests that exercise edge 2→3 in `internal/cli`: none found. `testmain_test.go:30-34` disables it package-wide.

Negative evidence:
- `scheduler.CheckHeadroom` is labeled `production-called` only within `internal/scheduler` (`scheduler.go:687`). The scheduler has no importers, so it is effectively unmounted relative to any launch path.
- The Semgrep closure mapper returned `llm-fallback` (it reported no supported-language targets for directory targets). The caller labels above come from a repo-wide grep (`grep -rnE <symbol> --include='*.go' internal cmd`, with test files split out) plus targeted reads.

### ClosureMap (structured — derive() input) — Chain A, robot spawn

```json
{
  "behavior": "When an operator or automation requests --robot-spawn, ntm returns an admission decision (admit/defer/refuse with reason and hint) computed from live host readings and learned per-agent memory samples, before any tmux session is created.",
  "git_commit": "905820535176783780bcf57035f4e9687c43ce24",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "memory_sample_store", "module": "internal/pressure", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": "pressure.NewFileMemorySampleStore" },
    { "id": "resolve_spawn_memory_estimate", "module": "internal/pressure", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "collect_spawn_admission_input", "module": "internal/robot", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "evaluate_spawn_admission", "module": "internal/pressure", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "robot_get_spawn_gate", "module": "internal/robot", "is_entrypoint": true, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "robot_spawn_admission_json", "module": "internal/robot", "is_entrypoint": false, "adds_or_changes": false, "read_path": "robot.PrintSpawn", "seedable_store": null }
  ],
  "edges": [
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null }
  ]
}
```

### ClosureMap (structured — derive() input) — Chain B, CLI spawn

```json
{
  "behavior": "When an operator runs ntm spawn (or ntm resume), ntm refuses the launch with 'spawn admission refused: <reason> (<hint>)' if the learned or estimated memory demand exceeds live available memory minus a 2048MB reserve, before any tmux session is created.",
  "git_commit": "905820535176783780bcf57035f4e9687c43ce24",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "memory_sample_store", "module": "internal/pressure", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": "pressure.NewFileMemorySampleStore" },
    { "id": "resolve_spawn_memory_estimate", "module": "internal/pressure", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "evaluate_spawn_admission", "module": "internal/pressure", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "cli_spawn_session_logic_gate", "module": "internal/cli", "is_entrypoint": true, "adds_or_changes": false, "read_path": "cli.spawnSessionLogic", "seedable_store": null }
  ],
  "edges": [
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null }
  ]
}
```

All `adds_or_changes` values are `false` because no modification has been chosen yet. That under-specifies the maps on purpose: `derive()` fails safe to BLOCKING until planning marks the changed nodes.

### Closure adapter (staged proposal — `2026-11-06-09-41-spawn-admission-system-check-seams-and-grammar.closure-adapter.py`)

Two staged adapters, one per ClosureMap, live next to this document. Neither is wired into the repo.
- `…seams-and-grammar.closure-adapter.py` — Chain A (robot)
- `…seams-and-grammar.cli.closure-adapter.py` — Chain B (CLI)

Chain A adapter:

```python
"""Closure adapter (STAGED PROPOSAL — not wired into the repo).
Derived from the ClosureMap for: robot spawn admission decision.  Pin: 905820535176783780bcf57035f4e9687c43ce24.
Promote into /home/maceo/Dev/ntm and complete each TODO(promote) before use.
Speaks the 7-op contract apps/closure-oracle already talks to (mock_adapter.py).
"""
import http.server, json, sys
ASYNC_EDGES = []                                   # chain A is fully synchronous; no drivers
CONNECTOR = {e: True for e in ASYNC_EDGES}
SINK = []                                          # Phase-0 /seed_sink target

def handle(op, p):
    if op == "/reset":        SINK.clear(); CONNECTOR.update({e: True for e in ASYNC_EDGES}); return {"ok": True}
    if op == "/set_connector": CONNECTOR[p["edge"]] = p["enabled"]; return {"ok": True}
    if op == "/seed_sink":     SINK.append(p["value"]); return {"ok": True}
    if op == "/seed":
        # TODO(promote): write p["data"] (list of pressure.MemorySample) via
        #   pressure.NewFileMemorySampleStore().Append  (internal/pressure/memory_store.go:73)
        #   with XDG_DATA_HOME pointed at a temp dir   (MemorySampleStorePath, memory_store.go:43)
        return {"ok": True}
    if op == "/trigger":
        # TODO(promote): run `ntm --robot-spawn=<s> --spawn-cc=N --dry-run` -> robot.PrintSpawn
        #   (internal/robot/spawn.go:714 -> GetSpawn :303, admission at :443)
        return {"ok": True}
    if op == "/drive":
        if not CONNECTOR.get(p["edge"], True): return {"ok": True}  # oracle disabled = red-at-seam
        # no async edges in this chain
        return {"ok": True}
    if op == "/observe":
        # TODO(promote): return the "admission" object from robot.PrintSpawn stdout JSON
        #   (SpawnOutput.Admission, internal/robot/spawn.go:75; shape pressure.SpawnAdmission pressure.go:162)
        return {"ok": True, "value": json.dumps(SINK)}
    return {"ok": False, "error": "unknown op"}

class Hn(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        out = json.dumps(handle(self.path, json.loads(self.rfile.read(n) or "{}"))).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Hn).serve_forever()
```

The Chain B adapter is the same skeleton with `/trigger` → `spawnSessionLogic` (`internal/cli/spawn.go:1549`, via `ntm spawn <s> --cc=N --json`) and `/observe` → the JSON error envelope produced by `outputError` (`spawn.go:1551-1557`) at the refusal gate (`:1759-1761`).

## Historical Context (from thoughts/)

These files exist only under `thoughts/searchable/` in this repo (there is no `thoughts/shared/`), so the paths below are the real on-disk paths.

- `thoughts/searchable/shared/handoffs/general/2026-08-13_11-53-52_memory-admission-tactical-fix-and-adaptive-plan.md` — the original bug.
  - `ntm spawn` refused 5 agents with 29 GB free, because admission multiplied agents by `PerAgentMemLimitMB()` (16 GB systemd ceiling).
  - The fix added `PerAgentExpectedMemMB` (2048 default) and the config override.
  - Recorded learnings: never conflate ceiling and estimate; `tmux.Pane.PID` is the pane shell, not the agent; systemd scopes vanish when empty, so samples must be read while the agent is alive; robot spawn did not start the monitor.
- `thoughts/searchable/shared/plans/2026-08-13-11-42-tdd-live-polled-per-agent-memory-estimation.md` — the accepted plan that produced the live-polled estimator and the `RobotMonitorEnabled` flag (landed as commit `c1c96c72`).
- `thoughts/searchable/shared/plans/2026-08-13-08-36-tdd-adaptive-per-agent-memory-estimation.md` and `-REVIEW.md` — the rejected heavier design (runner/finalizer subprocess pair), kept for reference only.
- `docs/robot-section-model.md:291-301` — states that spawn admission uses the pressure projection and that `proc_count` is advisory.
- `docs/verification-matrix-swarm-scale-vnext.md` — swarm-scale verification matrix referencing the pressure beads (`bd-2mb03`, `bd-1oenb`, `bd-mr59k`, `bd-mmzvs`, `bd-qjt3s`).
- Relevant commit trail: `7a3d732d` (robot spawn admission), `1bad81ac` (running + requested cap, bd-1oenb), `6866832d` (ignore proc_count), `be5f182f` (aggregate memory), `9acad2ad` (stop using the ceiling), `c1c96c72` (live-polled estimates).

## Related Research

- `thoughts/searchable/shared/research/2026-08-11-16-42-wire-up-periodic-orphan-sweep.md` — the monitor loop that also hosts the memory-sample producers.

## Open Questions

- Which modification is intended? That choice sets `adds_or_changes` and `highest_new_connector` in the closure maps.
- Is the CLI path meant to stay memory-only? Today it does not pass the pressure snapshot, the agent caps or the tmux census that the robot path passes.
- Should the launch paths without admission (swarm, `add`, `create`, ensemble, restarts) be in scope?
- Should the unused headroom fields and the unwired `scheduler.HeadroomGuard` be in scope?
- No open beads issue tracks spawn admission. The `bd list --status=open` output on 2026-11-06 showed only test, E2E and workflow epics.
