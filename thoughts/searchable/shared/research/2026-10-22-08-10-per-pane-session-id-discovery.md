---
date: 2026-10-22T08:10:00-04:00
researcher: tha-hammer
git_commit: 305bc656dec0
branch: main
repository: ntm
topic: "How to properly ascertain per-pane provider session IDs for `ntm sessions save`"
tags: [research, codebase, sessions-save, agentsession, tmux, per-pane-identity]
status: complete
last_updated: 2026-10-22
last_updated_by: tha-hammer
---

# Research: per-pane provider session IDs for `ntm sessions save`

**Date**: 2026-10-22T08:10:00-04:00 (EDT)
**Researcher**: tha-hammer
**Git Commit**: 305bc656dec0
**Branch**: main
**Repository**: ntm

## Research Question
Research how to properly ascertain the per-pane IDs for use with the `ntm sessions save` command so we can fix the error that is currently present (save writes one id to every Claude pane; every future save needs the same hand fix).

## Summary
`ntm sessions save` flows through `runSessionsSave` → `session.Capture` → `mapPaneStates` → `agentsession.Discover(type, paneCwd)` → one of four provider finders. `Discover` answers only the question "what is the single most-recent provider session for this cwd?" (`discoverClaude` = newest `*.jsonl` in `~/.claude/projects/<encoded-cwd>/`; codex/gemini/antigravity = newest file whose contents mention the cwd). Every Claude pane that shares a cwd therefore receives the identical newest id. No per-pane signal (tmux pane id, shell PID, scrollback text, process argv) is consulted during discovery, even though ntm already collects several such signals elsewhere. The on-disk session files themselves carry `cwd`, `sessionId`, `gitBranch`, and timestamps, but nothing observed links a file to a specific tmux pane or PID. The remainder of this document maps every relevant piece: the exact save flow, every per-pane signal ntm already has, each provider store layout, and existing pane-attribution patterns that a fix could reuse.

## Detailed Findings

### 1. Save → capture → discover → resume flow (as it exists today)

- `runSessionsSave` in `internal/cli/session_persist.go:134` resolves the session (`ResolveSessionWithOptions`, `:158`), checks `tmux.SessionExists` (`:175`), captures via `state, err := session.Capture(sessionName)` (`:184`), and persists via `session.Save(state, opts)` (`:194`).
- `Capture` in `internal/session/capture.go:17` loads the tmux session (`tmux.GetSession`, `:45`), lists panes (`tmux.GetPanes`, `:50`), counts agents (`countAgents`, `:56`), detects one session-level cwd (`detectWorkDir`, `:59`), maps pane states (`mapPaneStates(panes, cwd)`, `:62`), and attaches git info, layout, and per-window geometry.
- `PaneState` in `internal/session/state.go:68` carries topology (`Title`, `Index`, `WindowIndex`, `AgentType`, `Model`, `Command`, `Active`, `Width`, `Height`, `PaneID`) plus the resume linkage (`SessionID:84`, `SessionProvider:85`, `SessionFile:86`). The struct comment (`:80-:83`) states the id is discovered best-effort as "the most-recent provider session for the pane's cwd+agent".
- `mapPaneStates` in `internal/session/capture.go:187` copies topology per pane, skips panes where `agentsession.ResumeProvider(string(p.Type)) == ""` (`:204`, i.e. user/editor panes), resolves `paneCwd := paneCurrentPath(p.ID)` with fallback to `sessionCwd` (`:207-:210`), then calls `agentsession.Discover(string(p.Type), paneCwd)` (`:211`) and stores `SessionID` / `SessionProvider` / `SessionFile` (`:212-:214`). Each pane makes an independent, identical-shaped query; nothing is shared or deduplicated across panes.
- `paneCurrentPath` (`internal/session/capture.go:222`) runs `display-message -t <paneID> -p #{pane_current_path}` and returns `""` on any failure. `detectWorkDir` (`:231`) tries the active pane's path, then the first pane's path, then `os.Getwd()`, then home. When per-pane lookup fails, all panes collapse to the same session cwd.
- `Discover` in `internal/agentsession/discover.go:71` maps agent type to provider via `ResumeProvider` (`:49`: `cc`→`claude`, `cod`→`codex`, `gmi`→`gemini`, `agy`→`antigravity`, everything else→`""`) and dispatches to exactly one provider finder (`:81-:93`). It returns `nil` (no error) when nothing is found so capture stays best-effort.
- `Resume` in `internal/session/restore.go:346` recreates topology via `Restore`, re-lists panes with `tmux.GetPanes` (`:361`), sorts saved states to creation order (`:367-:374`), and per pane builds `agentsession.ResumeCommand(provider, ps.SessionID, opts.PreferCASR)` (`:399`) when an id exists, else falls back to a fresh launch (`ps.Command` or `getAgentCommand`, `:404-:412`). `ResumeCommand` in `internal/agentsession/resume.go:29` emits `casr -cc/-cod/-gmi '<id>'` when casr is present and preferred, else native `claude --resume '<id>'`, `codex resume '<id>'`, `gemini --resume '<id>'`, or `agy --conversation '<id>' --model 'Gemini 3.1 Pro (High)'`. Duplicate saved ids therefore replay the same conversation into every pane.

### 2. Per-pane identity signals ntm already collects from tmux

- `Pane` struct in `internal/tmux/session.go:59` holds `ID` (e.g. `%5`), `Index`, `WindowIndex`, `NTMIndex` (parsed from title), `Title`, `Type`, `Variant`, `Tags`, `Command` (`pane_current_command`), `Width`, `Height`, `Active`, and `PID` (shell PID from `#{pane_pid}`).
- `FieldSeparator = "_NTM_SEP_"` (`internal/tmux/session.go:56`) delimits all `list-panes -F` format strings.
- `GetPanes` / `GetPanesContext` (`internal/tmux/session.go:513`, `:518`) and the `list-panes -s` variant (`:490`) request `#{pane_id}`, `#{pane_index}`, `#{pane_title}`, `#{pane_current_command}`, `#{pane_width}`, `#{pane_height}`, `#{pane_active}`, `#{pane_pid}`, `#{window_index}`, `#{@ntm_type}`, `#{@ntm_index}`, `#{@ntm_variant}`, parsed via `parsePaneLine`. `PaneActivity` (`:1641`) adds `#{pane_last_activity}`; `GetPanesWithActivity` / `GetPanesWithActivityContext` (`:1785`, `:1790`) expose it.
- Agent typing comes from the pane title (`parseAgentFromTitle`, `:88`, format `{session}__{type}_{index}[_variant][tags]` via `paneNameRegex`, `:33`), with `detectAgentFromCommand` (`:153`) and `detectAgentFromProcessTree(shellPID, maxDepth)` (`:236`) as fallbacks. The tree walk uses `process.GetCmdline` + `process.GetChildPIDs(top.pid, 8)` (`internal/process/liveness.go:235`, `:266`), pre-order, depth default 4, to catch wrappers (`bun`, `node`, `npx`, `deno`, `python`, `sh`, `bash`, `zsh` in `agentWrapperCommands`, `:209`).
- Per-pane cwd is queryable at any time with `display-message -t <paneID> -p #{pane_current_path}` (used by `paneCurrentPath`, `detectWorkDir`, and `internal/cli/rollback.go:307`).
- Full scrollback is queryable with `CapturePaneOutput(target, lines)` / `CapturePaneOutputContext` / `CapturePaneOutputANSIContext` / `CapturePaneCleanContext` / `CapturePaneVisible` (`internal/tmux/session.go:1529`, `:1534`, `:1551`, `:1556`, `:1561`). Existing callers: `internal/cli/save.go:132` (pane dumps), `internal/context/rotation.go:690,706`, `internal/cli/errors.go:331`, `internal/cli/get_all_session_text.go:115`, `internal/cli/diff.go:81`, `internal/cli/assign.go:3626,4351`, `internal/cli/codex.go:106,403`, `internal/robot/watch_bead.go:133`.
- None of these per-pane signals (PID, pane id, scrollback) are passed to `agentsession.Discover` today; discovery input is only `(agentType, workDir)`.

### 3. Provider session stores and what each file contains

All discovery lives in `internal/agentsession/discover.go`; fixtures live in `internal/agentsession/agentsession_test.go`.

- Claude (`discoverClaude`, `:115`): store is `~/.claude/projects/<encodeClaudeProjectDir(cwd)>/*.jsonl`, where the encoder is `cwd.replace(/[^a-zA-Z0-9]/g, "-")` (`claudeNonAlnumPattern`, `:25`; `encodeClaudeProjectDir`, `:108`). Selection is `newestFileWithExt(projDir, ".jsonl")` (`:117`, mtime-based, `:211`), and the id is the filename stem (`:121`). Verified live: cwd `/home/maceo/Dev/ntm` maps to `-home-maceo-Dev-ntm` and currently holds one session file plus a `memory/` dir. File lines embed `sessionId` (= filename stem), `cwd` (`/home/maceo/Dev/ntm` on all sampled lines), `gitBranch`, `timestamp`, `version`, hook attachments. A substring scan for pid-like keys found nothing; `tmux`/`pane` mentions are conversational text (ntm help output, skill docs), not metadata fields. No pane id, PID, or PPID field was observed.
- Codex (`discoverCodex`, `:137`): store is `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (date-sharded; live host has `2026/` with day dirs). Selection is `newestCodexRolloutForCwd` (`:250`), which walks the tree and keeps the newest file whose first 64 KB mentions the cleaned cwd (`fileMentionsCwd`, `:237`). The id is the stem after the `rollout-` prefix (`codexSessionID`, `:281`).
- Gemini (`discoverGemini`, `:159`): store is `~/.gemini/tmp/<hash>/chats/session-*.json` (workspace path is hashed, not reversible). Selection is `newestGeminiSessionForCwd` (`:288`), newest `session-*.json` directly under a `chats/` dir whose first 64 KB mentions the cwd. The id is the stem after `session-` (`:165`).
- Antigravity (`discoverAntigravity`, `:190`): store is `~/.gemini/antigravity-cli/conversations/<uuid>.db` (stock SQLite, one DB per conversation). Selection is `newestAntigravityConversationForCwd` (`:326`), newest `*.db` whose full contents contain the cwd (`fileContainsCwd`, `:358`, whole-file read because the cwd can sit ~16–20 KB deep). The id is the filename stem (`:196`). The gmi and agy paths are explicitly kept disjoint despite sharing the `~/.gemini` parent.
- Shared behavior: every provider selects exactly one newest-by-mtime match per `(agentType, workDir)` call. With N panes sharing one cwd, N identical calls return N copies of the same id. The mtime comparison (`info.ModTime().After(bestMod)`, `newestFileWithExt:226`, codex `:265`, gemini `:306`, agy `:341`) means even a second pane started later loses: only the globally newest file ever wins.

### 4. Existing pane-attribution patterns a fix could reuse

- Shell-PID → pane listing: `Pane.PID` via `#{pane_pid}` in `GetPanes`/`GetPanesContext` (`internal/tmux/session.go:59`, `:490`, `:518`, `:526`) and `PaneActivity` (`:1641`, `:1649`).
- Process-tree helpers in `internal/process/liveness.go`: `GetCmdline(pid)` (`:235`, `/proc/<pid>/cmdline` + `ps` fallback), `GetChildPIDs(parentPID, limit)` (`:266`, `/proc/.../children` + `pgrep -P` fallback), `GetChildPIDsContext` (`:306`), `getChildPIDs` (`:167`, all-thread `/proc/<pid>/task/*/children` walk), `GetChildPID` (`:151`, first child), `HasChildAlive` (`:135`), `CaptureProcessIdentity` (`:101`), `GetProcessState` (`:375`).
- Full-subtree walk pattern: `collectPaneDescendants` in `internal/cli/send.go:2301` recursively walks `process.GetChildPIDs(pid, orphanReapFanout)` (`orphanReapFanout = 64`, `:2301`) bounded by `orphanReapMaxDepth`, excluding self/parent/init via `orphanReapExcluded`. Same one-level-helper-recursive-walk shape as `detectAgentFromProcessTree`.
- PID↔pane map: `PIDMap` in `internal/integrations/rano/pidmap.go:43` maintains `paneToShellPID`, `pidToPane` (shell + children), and `shellToChildren`; `RefreshContext` (`:80`) lists sessions, calls `tmux.GetPanesContext`, records `m.pidToPane[pane.PID] = identity` (`:140`), and maps children per shell PID (`:145`-`:160`). Built for rano network attribution, but it is the only in-repo structure that already binds every descendant PID to a pane identity.
- Scrollback-as-evidence pattern: `collectSummaryAgentOutputs` (`internal/cli/send.go:2835` takes `tmux.CapturePaneOutput` as a parameter), rotation summaries (`internal/context/rotation.go:690`), and scrollback checks before acting (`internal/cli/assign.go:3626,4351`). No code today parses scrollback for a provider session id or UUID.
- Per-pane cwd pattern: `paneCurrentPath` + `detectWorkDir` (`internal/session/capture.go:222`, `:231`) and the `display-message #{pane_current_path}` call in `internal/cli/rollback.go:307`.
- Test seam: `homeDir` var in `internal/agentsession/discover.go:65` is overridable; `internal/agentsession/agentsession_test.go` covers newest-wins (`TestDiscoverClaude:165`), no-match (`TestDiscoverClaudeNoSession:207`), non-resumable types (`TestDiscoverNonResumableAgent:218`), codex/gemini/agy discovery (`:227`, `:261`, `:310`), and gmi/agy disjointness (`TestDiscoverAgyGmiDisambiguation:355`). `TestMapPaneStates` in `internal/session/session_test.go:842` covers empty/single/multi pane topology only, with no discovery assertions (multi-pane same-cwd duplicate behavior is untested).

## Code References
- `internal/cli/session_persist.go:134` - `runSessionsSave`: save entry; Capture at :184, persist at :194
- `internal/session/capture.go:17` - `Capture`: full session snapshot assembly
- `internal/session/capture.go:187` - `mapPaneStates`: per-pane loop where the duplicate id enters
- `internal/session/capture.go:222` - `paneCurrentPath`: per-pane cwd via `display-message #{pane_current_path}`
- `internal/session/capture.go:231` - `detectWorkDir`: session-level cwd fallback chain
- `internal/session/state.go:68` - `PaneState`: topology + `SessionID:84`, `SessionProvider:85`, `SessionFile:86`
- `internal/agentsession/discover.go:71` - `Discover`: single-newest-per-cwd dispatcher
- `internal/agentsession/discover.go:49` - `ResumeProvider`: agent-type → provider mapping
- `internal/agentsession/discover.go:115` - `discoverClaude`: newest `*.jsonl` in encoded project dir
- `internal/agentsession/discover.go:108` - `encodeClaudeProjectDir`: `replace(/[^a-zA-Z0-9]/g, "-")`
- `internal/agentsession/discover.go:137` - `discoverCodex`: newest cwd-mentioning rollout
- `internal/agentsession/discover.go:159` - `discoverGemini`: newest cwd-mentioning `session-*.json`
- `internal/agentsession/discover.go:190` - `discoverAntigravity`: newest cwd-containing `<uuid>.db`
- `internal/agentsession/discover.go:211` - `newestFileWithExt`: mtime-based newest selection
- `internal/agentsession/discover.go:237` - `fileMentionsCwd`: 64 KB prefix cwd check (codex/gemini)
- `internal/agentsession/discover.go:358` - `fileContainsCwd`: whole-file cwd check (agy)
- `internal/agentsession/resume.go:29` - `ResumeCommand`: casr/native resume command builder
- `internal/session/restore.go:346` - `Resume`: topology rebuild + per-pane id replay at :399
- `internal/tmux/session.go:59` - `Pane`: `ID`, `Index`, `WindowIndex`, `NTMIndex`, `Title`, `Type`, `Variant`, `Tags`, `Command`, `Width`, `Height`, `Active`, `PID`
- `internal/tmux/session.go:490,513,518,526` - `GetPanes*` format strings incl. `#{pane_pid}`, `#{pane_id}`, `#{@ntm_type}`
- `internal/tmux/session.go:1529,1534,1551,1556,1561` - `CapturePane*` scrollback capture family
- `internal/tmux/session.go:1641,1785,1790` - `PaneActivity`, `GetPanesWithActivity*`
- `internal/tmux/session.go:236` - `detectAgentFromProcessTree`: argv + child-PID agent typing walk
- `internal/process/liveness.go:235,266,306` - `GetCmdline`, `GetChildPIDs`, `GetChildPIDsContext`
- `internal/process/liveness.go:135,151,167` - `HasChildAlive`, `GetChildPID`, `getChildPIDs`
- `internal/cli/send.go:2301` - `collectPaneDescendants`: recursive pane-subtree walk for orphan reap
- `internal/integrations/rano/pidmap.go:43,80,140` - `PIDMap`: shell+child PID ↔ pane identity map
- `internal/cli/session_persist.go:740` - save-display `SessionID` rendering
- `internal/session/restore.go:333,394,400` - `ResumePane` id reporting and replay
- `internal/agentsession/agentsession_test.go:165,207,218,227,261,310,355` - discovery tests (newest-wins, no-match, disjointness)
- `internal/session/session_test.go:842` - `TestMapPaneStates` (topology only, no discovery assertions)
- `internal/cli/save.go:132` - `runSave` pane-output dumps via `CapturePaneOutput` (the other `save` command)

## Architecture Documentation
- Save owns topology; resume of provider conversations is delegated to casr or native `--resume <id>` (`internal/agentsession/resume.go`, `internal/session/restore.go:341`). The agentsession package intentionally stores only ids, never conversation contents (`internal/agentsession/discover.go:1`).
- Discovery is keyed `(agentType, workDir) → newest session`. Its granularity is one id per directory, not one id per pane. `mapPaneStates` calls it once per pane with the same key, so duplication is structural, not a race.
- Pane identity in ntm is `(session, pane_id, index, title, shell PID)` from tmux plus the `__{type}_{n}` title convention (`internal/tmux/session.go:33`, `:88`). Provider identity is `(provider, sessionId)` from on-disk stores. No join key between the two exists today.
- The `PIDMap` (`internal/integrations/rano/pidmap.go`) is the closest thing to that join: it binds shell and descendant PIDs to pane identities. It is owned by the rano integration, not the session package.
- Per-window geometry, zoom, active window/pane, and model/variant already round-trip through save/restore; only the provider session id lacks a per-pane source.

## Historical Context (from thoughts/)
- No prior research documents were found in `thoughts/searchable/shared/research/` on per-pane session discovery (only one unrelated orphan-sweep note). No further historical context was available.
- Beads (`bd list --status=open --type=bug`) shows one open bug, `bd-2ix5l` (cass status `IndexSize=0`), unrelated to this topic. No tracked issue exists for the duplicate save id; the schema-warnings note applies: writes are blocked until the shared remote schema is reconciled, so no issue was created from this research.

## Related Research
- None in `thoughts/searchable/shared/research/` as of this writing.

## Open Questions
- Do Claude Code session files reliably record anything process-linked (e.g. a shell PID, `entrypoint`, or `userType`) on every session start, or only on hook attachments? The sampled live file showed hook attachments with command names but no PID field in the first 2 KB; a full-schema pass over several session files would settle this.
- Do Codex rollout files embed a PID or `session_id` distinct from the filename stem that could disambiguate same-cwd panes? Only the cwd-mention prefix is read today (`fileMentionsCwd`, 64 KB cap); the rest of the rollout schema was not surveyed.
- Is per-pane scrollback (`CapturePaneOutput`) a viable id source, i.e. does the Claude/Codex/Gemini TUI reliably print its session UUID anywhere visible? No parser for this exists in the repo; a live multi-pane check (save, then grep each pane's scrollback for its on-disk ids) would answer it.
- Would assigning N newest same-cwd sessions to N same-type panes by recency be correct often enough, or does pane start order vs. session-file mtime order diverge (e.g. a pane idles while another creates a second session)? `mapPaneStates` test coverage (`internal/session/session_test.go:842`) has no discovery assertions, so any ranking approach needs new tests.
- Should the PID→pane join (`internal/integrations/rano/pidmap.go`) move into shared code, or should session capture grow its own lightweight descendant walk on top of `internal/process/liveness.go` (`GetChildPIDs`, `GetCmdline`)? Both primitives exist; ownership is undecided.

