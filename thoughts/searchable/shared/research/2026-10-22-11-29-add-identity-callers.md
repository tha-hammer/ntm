---
date: 2026-10-22T11:29:00-04:00
researcher: tha-hammer
git_commit: 305bc656dec0
branch: main
repository: ntm
topic: "add command identity flow and other pane creators that impact saved state"
tags: [research, codebase, add, pane-identity, sessions-save, tmux]
status: complete
last_updated: 2026-10-22
last_updated_by: tha-hammer
---

# Research: `add` identity flow and other pane creators that impact saved state

**Date**: 2026-10-22T11:29:00-04:00 (EDT)
**Researcher**: tha-hammer
**Git Commit**: 305bc656dec0
**Branch**: main
**Repository**: ntm

## Research Question
Research the `add` command defect (add titles the new pane `<session>__cc_1` and never sets tmux identity vars, while name-titled panes hide their NTM indices from `maxIndices`, so the new pane claims key `(cc,1)` already held by pane 1) and search for other callers that also impact the save state.

## Summary
The record key is `(WindowIndex, AgentType, NTMIndex)` resolved from `@ntm_*` vars when present, else from title parsing. Only two production code paths ever write the `@ntm_*` vars (`spawn` unconditionally, robot spawn only when an Agent Mail name resolves). Every other pane-creating path — `add`, `scale` (via `add`), controller, adopt, create, restore/resume, checkpoint restore, ensemble/swarm, context rotation — sets the title at most. `add` computes its next indices by re-parsing only pane titles, ignoring the `Type`/`NTMIndex` fields `GetPanes` already resolved, so any existing pane whose title is not in NTM grammar (e.g. an Agent Mail name like `RubyDog`) contributes nothing and its index gets reused. Save-time reconcile then sees colliding keys: the first colliding pane claims the recorded id, the loser saves empty, and the refreshed record persists two entries with the same key and id on every save thereafter.

## Detailed Findings

### 1. The `add` command as it exists today

- Entry: `newAddCmd` in `internal/cli/add.go:126` (cobra command, flags `--cc/--cod/--gmi/--agy/--ollama/--cursor/--windsurf/--aider/--oc`, `--persona`, `--label`). `runAdd` is defined at `internal/cli/add.go:301` (lines 301–806) with exactly two production callers: the cobra `RunE` (`internal/cli/add.go:238`) and `runScale` scale-up (`internal/cli/scale.go:310`).
- Index allocation: `runAdd` snapshots panes once (`tmux.GetPanes`, `add.go:413`), seeds `maxIndices` via `parseIndex` (`add.go:421`) applied to each pane (lines 428–430), then pre-increments per agent (`maxIndices[agentTypeStr]++`, `add.go:503`). The list is never re-read between panes.
- Only `p.Title` is read for seeding. `paneTitleTypeAndIndex` (`add.go:273`) takes the substring after the final `__` (`tmux.PaneTitleSuffix`, `internal/tmux/session.go:892`), strips a trailing `[tags]` block, splits on `_`, and takes the first part parsing to an int `> 0` as the index; the type is the joined preceding parts, canonicalized via `AgentType.Canonical()` when valid. Titles that fail this parse contribute nothing. Examples pinned by `TestPaneTitleTypeAndIndex` (`internal/cli/cli_helpers_extra_test.go:135`): `project__cc_2` → `(cc, 2)`; `custom-title` → not ok.
- The fields `GetPanes` already resolved (`p.Type`, `p.NTMIndex`, `p.Variant`) are ignored for index seeding, even though `applyPaneIdentityVars` (`internal/tmux/session.go:704`) lets `@ntm_*` override title parsing in `parsePaneLine` (`internal/tmux/session.go:1706`). Seeded map keys may also differ in form from loop keys: seeds use canonicalized title types, the loop uses raw `string(agent.Type)`.
- Pane creation: `tmux.SplitWindow(session, dir)` (`add.go:492`; wrapper `internal/tmux/session.go:643`, first-window `split-window ... -P -F #{pane_id}` then `select-layout tiled`), sleep `paneInitDelay`, then `title := tmux.FormatPaneName(session, agentTypeStr, num, agent.Model)` (`add.go:506`; `internal/tmux/session.go:979`, canonical type, `<session>__<type>_<index>[_<variant>]`) and `tmux.SetPaneTitle(paneID, title)` (`add.go:507`; `select-pane -T` with retries plus `allow-set-title off`).
- Identity vars: `runAdd` never calls `tmux.SetPaneIdentity`. The string does not appear in `internal/cli/add.go`. The only per-pane tmux writes are `SplitWindow`, `SetPaneTitle`, `SendKeys`. Panes from `add` carry identity only in their titles.
- Record hook: `paneRecordAppendHook` var (`add.go:46`) defaults to `session.AppendPaneRecord`. `recordAddedPanes` (`add.go:55`) is invoked once after the loop (`add.go:771`), re-reads `tmux.GetPanes` exactly once, matches by pane id, skips unknown ids silently, and never returns non-nil. If any loop iteration errors earlier, `runAdd` exits before :771 and no entries are appended for that run. The re-read panes carry `Type`/`NTMIndex` derived from titles (no `@ntm_*` were set).
- Storage side: `AppendPaneRecord` (`internal/session/storage.go:724`) locks via `acquireLock` and calls `appendPaneRecord` (`storage.go:702`): non-resumable types are skipped (`isAgentPaneForRecord`, `storage.go:589`); otherwise a fresh entry with empty session-id fields replaces any entry sharing the same `(windowIndex, agentType, ntmIndex)` key (`paneRecordKeyOfPane`/`paneRecordKeyOfEntry`, `storage.go:579`) and is appended.

### 2. Pane identity primitives and how resolution works

| Primitive | Location | Effect |
|---|---|---|
| `Client.SetPaneTitle` / `SetPaneTitle` | `internal/tmux/session.go:649` / `:676` | Title only; also `allow-set-title off`. Never touches `@ntm_*`. |
| `Client.SetPaneIdentity` / `SetPaneIdentity` | `internal/tmux/session.go:686` / `:695` | The only writer of `@ntm_type`/`@ntm_index`/`@ntm_variant` in `internal/`+`cmd/` (writes at :687–690). Errors discarded (`RunSilent`). |
| `FormatPaneName` | `internal/tmux/session.go:979` | Pure string builder. Writes nothing. |
| `WithPaneTag` | `internal/tmux/session.go:998` | Pure string helper; no non-test callers. |

Resolution in `GetPanes`: three format strings append identity vars last (`GetPanesContext :490` fields 9/10/11; `GetAllPanesContext :526` fields 10/11/12; `GetPanesWithActivityContext :1649` fields 10/11/12). Order per pane: (1) title parse (`parseAgentFromTitle :88` via `parsePaneFromParts :1741`; non-matching → `AgentUser, 0`); (2) command fallback sets `Type` only, never `NTMIndex` (:1749); (3) `applyPaneIdentityVars :704` overrides when `@ntm_type` is non-empty — `@ntm_index` alone is ignored, variant only when non-empty.

### 3. Every pane-creating path and its save-state impact

Baseline: `SetPaneIdentity` has exactly two callers in the tree. `WritePaneRecord`/`AppendPaneRecord` (`internal/session/storage.go:689`) are called only from `spawn` (`internal/cli/spawn.go:530`) and `add` (`internal/cli/add.go:47`).

| Path | Creation | Title | Identity vars | Record |
|---|---|---|---|---|
| `spawn` (`spawnSessionLogic` `internal/cli/spawn.go:1530`, hook `:2643`) | `CreateSessionWithHistoryLimit` + `SplitWindow`, titles `:2047`, rename `:2287` | Yes | Yes, unconditional (`:2284`) | Yes (`recordSpawnPanes`) |
| robot spawn (`launchAgent` `internal/robot/spawn.go:713`) | `CreateSessionWithHistoryLimit` + `SplitWindow` | Yes (`:737`, Agent Mail name else `<session>__<type>_<n>`) | Only when Agent Mail name resolves (`:748`) | No |
| `add` (`runAdd` `internal/cli/add.go:301`) | `SplitWindow` | Yes (`:507`) | **No** | Yes, append (`:771`) |
| `scale` up (`runScale` `internal/cli/scale.go:310`) | Delegates to `runAdd` | Via `add` | No | Via `add` |
| `scale` down | `KillPane` only (`:348`) | — | — | No (stale entry dropped at next save) |
| Respawn CLI + robot (`respawn.go:56` → `internal/robot/restart_pane.go:164`) | `respawn-pane -k` in place, same `%N` | No | No | No |
| Diagnose auto-fix (`diagnose.go:425`) | Bare `RespawnPane` | No | No | No |
| Swarm auto-respawner (`auto_respawner.go:413`) | Kill + `SendKeys` in same pane | No | No | No |
| Adopt (`runAdopt` `internal/cli/adopt.go:344`) | None (existing panes) | Yes, conditional (`:459`, only `AutoName && !DryRun`) | No | No |
| Controller (`buildControllerResponse` `internal/cli/controller.go:210`, title `:338`) | Reuse index 1 else `SplitWindow` (`:317`) | Yes | No | No |
| Create (`runCreate` `internal/cli/create.go:136`; JSON path duplicates `:487`) | `CreateSessionWithHistoryLimit` + `SplitWindow` | **No** | No | No |
| Session `Restore` (`internal/session/restore.go:21`) | Raw `new-window`/`split-window` | Yes, saved `Title` verbatim (`:162`) | No | No |
| Session `Resume` (`restore.go:346`) | `Restore` + `SendKeys` | Via `Restore` | No | No |
| Handoff `resume --spawn` (`internal/cli/resume.go:316` → `spawnSessionLogic`) | Delegates to `spawn` | Via `spawn` | Via `spawn` | Via `spawn` |
| Checkpoint restore (`internal/checkpoint/restore.go:96`, agents `:363` via raw `respawn-pane -k`) | `CreateSession` + raw splits | Yes, from checkpoint (`:233`, `:291`) | No | No |
| Ensemble spawn (`internal/ensemble/manager.go:77` → swarm `createSession` `internal/swarm/orchestrator.go:136`) | `CreateSession` + `SplitWindow`, titles `:229`/`SetPaneTitle :558` | Yes | No | No |
| Swarm orchestrator generic (`orchestrator.go:715`) | Same `createSession` | Yes | No | No |
| Workflow templates (`spawn -t <tmpl>` → `spawnSessionLogic`) | Via `spawn` (`internal/cli/spawn.go:1223`, `:1403`) | Via `spawn` | Via `spawn` | Via `spawn` |
| Context rotation (`DefaultPaneSpawner.SpawnAgent` `internal/context/rotation.go:182`; `rotateAgent :590` kills old pane `:746`) | `SplitWindow` + title (`:192`) | Yes | No | No |
| Profile switch (`runProfileSwitch` `internal/cli/profile_switch.go:168`) | Retitles existing panes | Yes | No | No |
| serve `SetPaneTitle` API (`internal/serve/server.go:3490`) | Retitles one pane | Yes, verbatim | No | No |
| `SetPaneTags`/`AddPaneTags` (`internal/tmux/session.go:767`) | Retitles with tags | Yes | No | No |

### 4. Save-time behavior when keys collide or records are absent

- The key comes from `paneRecordKeyOfPane` (`internal/session/storage.go:579`): `(WindowIndex, AgentType, NTMIndex)`. Never the tmux `%N`, never pane index alone. Only cc/cod/gmi/agy-family types participate (`agentsession.ResumeProvider` non-empty, `internal/agentsession/discover.go:49`).
- No record entry for a live agent pane: lazy fill in `reconcilePaneIDs` (`internal/session/capture.go:220`, fill pass `:305`). Discovery is memoized per `(provider, cwd)` (`:323`); only the first pane in `(WindowIndex, Index)` order wins the id; others save empty (`SessionID`, fresh launch on resume). The pane still lands in the refreshed record (`:358`), persisted by `Save` via `refreshPaneRecordAfterSave` (`storage.go:141` → `:737`). Missing or corrupt record files behave as empty records (`storage.go:605`).
- Colliding keys in a stored record: first entry wins in the `stored` map (`capture.go:224`); `upsertPaneRecordEntries` does the same on merge (`storage.go:646`).
- Two live panes on one key: the first (sorted order) claims the recorded id (`capture.go:290`); the loser saves empty but its refreshed entry re-copies the stored id (`:372`, never-regress), so the persisted record keeps two entries with the same key and the same id, repeating on every save. With different cwds and no record, both panes can win distinct saved ids, but both are stored under one key; next save only the first is visible, the second saves empty and its entry is overwritten with the first pane's id. Non-empty recorded ids are never overwritten by empties on merge (`storage.go:655`). Entries for dead panes are dropped because the refreshed list contains only live panes.

## Code References
- `internal/cli/add.go:301` - `runAdd`: cobra `RunE :238` and `runScale :310` are the only callers
- `internal/cli/add.go:273` - `paneTitleTypeAndIndex`: title-only suffix parsing for index seeding
- `internal/cli/add.go:412` - `GetPanes` snapshot + `maxIndices` seeding (titles only, once)
- `internal/cli/add.go:503` - pre-increment assigns `num` per agent type
- `internal/cli/add.go:492,506,507` - `SplitWindow`, `FormatPaneName`, `SetPaneTitle` (no `SetPaneIdentity` anywhere in the file)
- `internal/cli/add.go:43,55,771` - `paneRecordAppendHook`, `recordAddedPanes`, its single call site
- `internal/tmux/session.go:686,695` - `SetPaneIdentity`: sole writer of `@ntm_type`/`@ntm_index`/`@ntm_variant`
- `internal/tmux/session.go:649,676` - `SetPaneTitle`: title-only writer
- `internal/tmux/session.go:704` - `applyPaneIdentityVars`: identity-var override (`@ntm_index` alone ignored)
- `internal/tmux/session.go:88,892,979` - `parseAgentFromTitle`, `PaneTitleSuffix`, `FormatPaneName`
- `internal/cli/spawn.go:2284,2643` - `SetPaneIdentity` (unconditional) and record hook in `spawnSessionLogic`
- `internal/robot/spawn.go:713,737,748` - robot spawn: title always, identity only with Agent Mail name, no record write
- `internal/session/storage.go:579,689,702,724` - record keys, `WritePaneRecord`, `appendPaneRecord`, `AppendPaneRecord`
- `internal/session/capture.go:220,290,305,323,372` - `reconcilePaneIDs`: claim, lazy fill, memoization, never-regress copy

## Architecture Documentation
Pane identity has two layers that must agree: the persisted record key, built from tmux-reported `(window, type, ntm index)`, and the allocation logic that assigns those numbers. Allocation is fragmented: `spawn` assigns and persists identity vars; `add` (and therefore `scale`) assigns from a separate title parser; adopt, controller, ensemble/swarm, and rotation each compute their own numbers from titles or local counters; restore paths replay saved titles. The record layer, written only by `spawn` and `add`, assumes keys are unique, but nothing enforces that assumption at allocation time. Save is the only reconciler, and its first-wins plus never-regress rules convert allocation collisions into permanently duplicated record entries rather than errors.

## Historical Context (from thoughts/)
- Research: `thoughts/searchable/shared/research/2026-10-22-08-10-per-pane-session-id-discovery.md` (discovery model and per-pane signal map)
- Plan: `thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record.md` (six-behavior record implementation, now implemented)
- Review: `thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record-REVIEW.md` (advisory warned about key fragility and add-index reuse; the live test then reproduced it)

## Related Research
- None further in `thoughts/searchable/shared/research/` as of this writing.

## Open Questions
- Should `add` (and therefore `scale`) seed `maxIndices` from the `Type`/`NTMIndex` fields `GetPanes` already resolved instead of re-parsing titles, and should it call `SetPaneIdentity` like `spawn` does?
- Should robot spawn write the pane record, and should its identity write be unconditional rather than tied to Agent Mail name resolution?
- Should restore/resume and checkpoint restore set identity vars when they replay saved titles, so restored sessions keep stable keys across splits and kills?
- Should `reconcilePaneIDs` error or warn visibly when two live panes collide on one key instead of silently persisting duplicates?

---

## Follow-up Research 2026-10-22 — Pane naming at initial launch and Agent Mail registration

**Date**: 2026-10-22T11:37:11-04:00 (EDT)
**Git Commit**: 305bc656dec025680c886439a72e736046cdd9a6
**Scope**: This section is appended to the document above and does not modify it. It answers the follow-up question: on initial `ntm` launch, what code names each pane, what generates/selects the Agent Mail name (e.g. `RubyDog`), who sends the registration, and where that name is written into tmux (pane title vs `@ntm_type`/`@ntm_index` identity vars).

### Summary of this follow-up

There are two initial-launch spawn paths and they share one naming sequence with one divergence. Both first compute a deterministic NTM title (`<session>__<type>_<index>`, plus a model/persona variant), then resolve an Agent Mail identity for the pane (reuse from the persisted session registry, else mint one from the external Agent Mail server), then overwrite the pane title with the Agent Mail name and thread the same name into the launched process as `$AGENT_NAME`. The Agent Mail name itself is **not generated in this repo**: both spawn paths call `CreateAgentIdentity` with no `Name`, so the `name`/`name_hint` argument is omitted and the server returns the adjective+noun name in `Agent.Name` ([internal/agentmail/types.go:69](internal/agentmail/types.go:69), example `GreenCastle` at :71; [internal/agentmail/types.go:271](internal/agentmail/types.go:271) — `Name` is "Optional; auto-generated if empty" at :276). The divergence is the identity vars: the CLI path writes `@ntm_type`/`@ntm_index`/`@ntm_variant` unconditionally before retitling, while the robot path writes them only when an Agent Mail name resolved — so a robot pane launched without Agent Mail keeps the parseable `<session>__<type>_<n>` title and no identity vars, while a CLI pane always has identity vars even when its title stays in NTM grammar.

### 1. Where the index and the first title come from (before any naming)

- Per-type 1-based indices are assigned when agent specs are flattened, before any tmux work: `AgentSpecs.Flatten` increments a per-type counter and stamps each `FlatAgent{Type, Index}` ([internal/cli/agent_spec.go:173](internal/cli/agent_spec.go:173), index field documented at :161). The persona-set path does the same per persona in `expandProfileAgents` ([internal/cli/spawn.go:442](internal/cli/spawn.go:442), `Index: indices[at]` at :459).
- Session/pane creation (CLI): `spawnSessionLogic` creates the session with `CreateSessionWithHistoryLimit` when missing ([internal/cli/spawn.go:1774](internal/cli/spawn.go:1774)), adds panes with `tmux.SplitWindow` ([internal/cli/spawn.go:1867](internal/cli/spawn.go:1867)), re-reads panes, sorts them deterministically with `sortPanesForAssignment` so agent *i* always lands on the same pane ([internal/cli/spawn.go:1901](internal/cli/spawn.go:1901)), and skips pane 0 when a user pane is requested (:1904-1907).
- Session/pane creation (robot): `GetSpawn` performs the same create/split/re-read shape ([internal/robot/spawn.go:543](internal/robot/spawn.go:543), splits at :566, re-read at :575), then builds a `nameMap` (see §4) and a per-spawn Agent Mail state via `setupAgentMailState` ([internal/robot/spawn.go:620](internal/robot/spawn.go:620)).
- The first title written to every agent pane is the pure string from `tmux.FormatPaneName` — `<session>__<canonical-type>_<index>` with `_<variant>` appended when a variant is non-empty; it writes nothing itself ([internal/tmux/session.go:979](internal/tmux/session.go:979)).

### 2. CLI spawn (`ntm spawn`): exact naming order per pane

In the launch loop of `spawnSessionLogic` ([internal/cli/spawn.go:2033](internal/cli/spawn.go:2033)):

1. **NTM title first.** `title := tmux.FormatPaneName(opts.Session, string(agent.Type), agent.Index, agent.Model)` then `tmux.SetPaneTitle(pane.ID, title)` ([internal/cli/spawn.go:2046](internal/cli/spawn.go:2046)).
2. **Persona retitle (optional).** If a persona resolved for the agent (from the model-keyed `PersonaMap` at :2188-2207 or an attached `--profile-set/--profiles` persona at :2214-2236), the title is recomputed with the persona name as the variant — `FormatPaneName(..., personaName)` — and written again ([internal/cli/spawn.go:2240](internal/cli/spawn.go:2240)).
3. **Agent Mail identity resolution, before launch.** The spawn registry is loaded lazily once via `LoadBestSessionAgentRegistry` ([internal/cli/spawn.go:1939](internal/cli/spawn.go:1939)); if `registry.GetAgent(title, pane.ID)` already maps this pane title/ID to a name, that name is reused ([internal/cli/spawn.go:2255](internal/cli/spawn.go:2255)). Otherwise the single pane is pre-registered by calling `registerSpawnedAgents` with a one-element `[]spawnedAgentInfo` ([internal/cli/spawn.go:2261](internal/cli/spawn.go:2261)) and the returned `AgentMap["<paneIndex>"]` becomes `agentMailName` (:2272). The comment at :2248-2253 states the intent: resolve *before* launch so the name can be shown in the title and threaded into the startup command; the post-launch registration (§5) then reuses the persisted registry entry and mints no second identity.
4. **Identity vars, unconditional.** `identityVariant` is the model alias, or the persona name when one is assigned ([internal/cli/spawn.go:2280](internal/cli/spawn.go:2280)); `tmux.SetPaneIdentity(pane.ID, string(agent.Type), agent.Index, identityVariant)` runs for every pane regardless of whether an Agent Mail name exists ([internal/cli/spawn.go:2284](internal/cli/spawn.go:2284)).
5. **Agent Mail retitle.** If `agentMailName != ""`, the pane title is overwritten with the bare Agent Mail name (`title = agentMailName; tmux.SetPaneTitle(pane.ID, title)`) ([internal/cli/spawn.go:2285](internal/cli/spawn.go:2285)). This is the write that makes the tmux border show e.g. `RubyDog` while addressing keeps working via the identity vars written in step 4 (rationale comment :2275-2279).
6. **Launch with the name in the environment.** The generated command is prefixed with `AGENT_NAME=<quoted name>` when a name resolved ([internal/cli/spawn.go:2332](internal/cli/spawn.go:2332)), then sent with `tmux.SendKeys` (:2366). After launch, a goroutine sends the agent a bootstrap prompt — `register with agent mail. use %s for your name` — so the agent process self-registers under the same reserved identity ([internal/cli/spawn.go:2421](internal/cli/spawn.go:2421)). The launched pane is recorded in `launchedAgents` with both `paneTitle: title` (the final, possibly Agent Mail, title) and `agentName: agentMailName` ([internal/cli/spawn.go:2527](internal/cli/spawn.go:2527)).

### 3. Robot spawn (`--robot-spawn`): same sequence, conditional identity vars

`launchAgent` ([internal/robot/spawn.go:713](internal/robot/spawn.go:713)) mirrors the CLI path (its comment at :720-723 says so explicitly, bd-3frkf):

1. `fallbackTitle := fmt.Sprintf("%s__%s_%d", session, shortType, num)` using the short type code ([internal/robot/spawn.go:717](internal/robot/spawn.go:717)).
2. `agentMailName := resolveAgentMailIdentity(am, dir, fallbackTitle, pane.ID, shortType, "")`; when non-empty it replaces the title ([internal/robot/spawn.go:724](internal/robot/spawn.go:724)).
3. The title (fallback or Agent Mail name) is written with `tmux.SetPaneTitle` ([internal/robot/spawn.go:737](internal/robot/spawn.go:737)).
4. **Divergence:** `tmux.SetPaneIdentity(pane.ID, canonicalType, num, "")` runs **only inside `if agentMailName != ""`** ([internal/robot/spawn.go:747](internal/robot/spawn.go:747)) — no identity vars, and an empty variant, when Agent Mail is unavailable/disabled. (The main document's table records this as "identity only when Agent Mail name resolves"; this follow-up confirms the guard is the `agentMailName` emptiness check at :747-749.)
5. The command is prefixed `AGENT_NAME=<name>` when a name resolved ([internal/robot/spawn.go:754](internal/robot/spawn.go:754)) and launched via `tmux.SendKeysForAgent` (:774). Robot spawn sends no "register with agent mail" bootstrap prompt; the pre-registration in step 2 is the only registration.

`GetSpawn` drives this per type with 1-based `i+1` numbering for claude/codex/gemini/antigravity ([internal/robot/spawn.go:625](internal/robot/spawn.go:625), :635, :645, :655) and calls `amState.persist()` after all launches ([internal/robot/spawn.go:664](internal/robot/spawn.go:664)).

### 4. Three different "names" that must not be conflated

| Name | Producer | Where it lives | Example |
|---|---|---|---|
| NTM pane title | `tmux.FormatPaneName` ([internal/tmux/session.go:979](internal/tmux/session.go:979)) | tmux pane title (until overwritten) | `myproj__cc_1`, `myproj__cc_1_opus` |
| Agent Mail name | External Agent Mail server, returned in `Agent.Name` | Pane title (after retitle), `$AGENT_NAME`, identity files, session registry | `RubyDog`, `GreenCastle` |
| Robot/NATO name | `AgentNameMap.GenerateName` ([internal/robot/agent_names.go:64](internal/robot/agent_names.go:64)) | Only the `--robot-spawn`/`--robot-agent-names` JSON output (`SpawnedAgent.Name`), never written to tmux | `claude-alpha` |

- The robot NATO name is assigned *after* `launchAgent` returns (`agent.Name = nameMap.AssignNew("claude", agent.Pane)` at [internal/robot/spawn.go:628](internal/robot/spawn.go:628) and siblings), uses custom names first then `{type-prefix}-{NATO word}` ([internal/robot/agent_names.go:73](internal/robot/agent_names.go:73)), and also backs `--robot-agent-names` via `BuildNameMapFromSession` ([internal/robot/agent_names.go:236](internal/robot/agent_names.go:236)). It plays no part in tmux titles or Agent Mail.
- A fourth deterministic string, `assignmentAgentName(session, agentType, paneIndex)` = `<session>_<type>_<paneIndex>` ([internal/cli/assign.go:1254](internal/cli/assign.go:1254)), is used for assignment/init-prompt identity (`You are agent <name>` preamble, [internal/cli/spawn.go:4606](internal/cli/spawn.go:4606)) — also not an Agent Mail name and never a pane title.

### 5. Who actually sends the registration, and with what tool

There are two distinct Agent Mail tools, and the spawn paths use different ones than a plain reading of "register_agent" suggests:

- `Client.RegisterAgent` sends the MCP tool **`register_agent`** ([internal/agentmail/tools.go:75](internal/agentmail/tools.go:75), `callToolWithBusyRetry(ctx, "register_agent", ...)` at :94). It includes `args["name"]` only when `opts.Name != ""` (:81-83).
- `Client.CreateAgentIdentity` sends the MCP tool **`create_agent_identity`** ([internal/agentmail/tools.go:109](internal/agentmail/tools.go:109), call at :122). It maps `opts.Name` to `name_hint` (only when non-empty, :115-117) — a hint, not a requested exact name.

**Per-pane spawn registration uses `create_agent_identity`, not `register_agent`:**

- CLI: `registerSpawnedAgents` ([internal/cli/spawn.go:3222](internal/cli/spawn.go:3222)) checks `cfg.AgentMail.Enabled` (:3224), client availability (:3240), loads/creates the `SessionAgentRegistry` (:3256-3259), calls `EnsureProject` (:3265), then per agent: reuse via `registry.GetAgent(paneTitle, paneID)` (:3283) or `client.CreateAgentIdentity(regCtx, RegisterAgentOptions{ProjectKey: workingDir, Program: agentTypeToProgram(agentType), Model: model})` — **no `Name` field set** ([internal/cli/spawn.go:3310](internal/cli/spawn.go:3310)). Program mapping (`cc→claude-code`, `cod→codex-cli`, `gmi→gemini-cli`, …) is `agentTypeToProgram` ([internal/cli/spawn.go:3401](internal/cli/spawn.go:3401)). On `ErrTransientBusy` it reconciles via `ListAgents` and claims the highest-ID unclaimed agent matching program+model ([internal/cli/spawn.go:3319](internal/cli/spawn.go:3319)). Callers: the pre-launch one-pane call at :2261, then post-launch calls for all launched panes in JSON mode ([internal/cli/spawn.go:2787](internal/cli/spawn.go:2787)) and twice in non-JSON mode (:2873, :2929 — the second is a duplicate post-hooks call; both are registry-reuse hits after the pre-launch mint, so no second server identity is created).
- Robot: `resolveAgentMailIdentity` ([internal/robot/spawn.go:836](internal/robot/spawn.go:836)) reuses via `am.registry.GetAgent(paneTitle, paneID)` (:841) or calls `am.client.CreateAgentIdentity(ctx, RegisterAgentOptions{ProjectKey: workingDir, Program: agentMailProgram(shortType), Model: model})` — again no `Name` ([internal/robot/spawn.go:851](internal/robot/spawn.go:851)); same busy-reconcile via `ListAgents` (:863-882). Its state is set up by `setupAgentMailState`, which returns nil when Agent Mail is disabled, the client is unavailable, or `EnsureProject` fails ([internal/robot/spawn.go:799](internal/robot/spawn.go:799)) — nil means graceful degradation to the `<session>__<type>_<n>` title, never a spawn failure (:784-788).

**`register_agent` proper is used for the session coordinator and non-spawn flows:**

- CLI spawn also registers one session-level coordinator: `registerSessionAgent(opts.Session, dir)` ([internal/cli/spawn.go:3182](internal/cli/spawn.go:3182), called at :2771 JSON / :2858 non-JSON) → `Client.RegisterSessionAgent` ([internal/agentmail/session.go:285](internal/agentmail/session.go:285)), which omits `Name` on first registration so "the server auto-generates a valid adjective+noun identity" (comment :323-324; call :325) and re-registers with the persisted name on later spawns (:304). This coordinator name is stored in `SessionAgentInfo.AgentName` (:336-337) and printed as `Registered with Agent Mail as %s` ([internal/cli/spawn.go:3205](internal/cli/spawn.go:3205)) — it is **not** written to any pane title.
- Other production `RegisterAgent` callers (not pane naming): the HumanOverseer re-claim in `SendOverseerMessage` ([internal/agentmail/tools.go:897](internal/agentmail/tools.go:897)), [internal/scanner/notify.go:399](internal/scanner/notify.go:399), [internal/serve/mail.go:546](internal/serve/mail.go:546), and [internal/tui/dashboard/dashboard.go:5926](internal/tui/dashboard/dashboard.go:5926). (`internal/resilience/monitor.go:108` and `internal/context/monitor.go:335` have unrelated same-named in-process methods, not Agent Mail calls.)

**How the name is generated/selected — the full selection rule in this repo:**

1. If the persisted `SessionAgentRegistry` already maps this pane (by title first, then pane ID — `GetAgent` at [internal/agentmail/session.go:696](internal/agentmail/session.go:696), `AddAgent` writer at :651) to a name, that exact name is selected and no server call is made.
2. Otherwise ntm sends `create_agent_identity` with only `project_key`, `program`, and `model`; the server mints the adjective+noun name and returns it as `Agent.Name` (the response struct documents `Name // e.g. "GreenCastle"` at [internal/agentmail/types.go:71](internal/agentmail/types.go:71)). There is no local wordlist, adjective/noun table, or name generator for Agent Mail names in `internal/agentmail`, `internal/cli`, or `internal/robot` — the only local name generators are the NATO map (§4) and `assignmentAgentName`, neither of which feeds registration. The `RubyDog`-style name a user sees is therefore server-chosen; ntm only persists and reuses it.
3. On a transient-busy error, selection falls back to server-side reconciliation: the most recently created (highest-ID) listed agent with matching program+model that no earlier pane in the same spawn has claimed ([internal/cli/spawn.go:3326](internal/cli/spawn.go:3326); robot equivalent [internal/robot/spawn.go:869](internal/robot/spawn.go:869)).

### 6. Where the name is written (tmux title vs identity vars vs files)

| Destination | Writer | Content |
|---|---|---|
| Pane title (`#{pane_title}`) | `Client.SetPaneTitle` — `select-pane -T <title>` plus `allow-set-title off` ([internal/tmux/session.go:649](internal/tmux/session.go:649)) | Last write wins at spawn: Agent Mail name if resolved, else `<session>__<type>_<index>[_<variant>]` |
| `@ntm_type` / `@ntm_index` / `@ntm_variant` pane options | `Client.SetPaneIdentity` — three `set-option -p` calls, errors discarded ([internal/tmux/session.go:686](internal/tmux/session.go:686)) | Canonical type, 1-based NTM index, model-or-persona variant (variant option only when non-empty, :689-691) |
| Per-pane identity file | `agentmail.WriteIdentity` (canonical `~/.config/agent-mail/identity/<sha1(project)[:12]>/<sanitized-pane-id>`, atomic, [internal/agentmail/pane_identity.go:67](internal/agentmail/pane_identity.go:67)) plus legacy `/tmp/agent-mail-name.*` via `WriteLegacyCompatIdentity` (:187) | The bare Agent Mail name + newline |
| Session registry (on disk) | `registry.AddAgent(paneTitle, paneID, name)` + `SaveSessionAgentRegistry` ([internal/cli/spawn.go:3375](internal/cli/spawn.go:3375), :3390; robot [internal/robot/spawn.go:886](internal/robot/spawn.go:886), persist :900) | pane-title→name and pane-ID→name maps, plus registration tokens |
| Agent process environment | `AGENT_NAME=<name>` prefix on the launch command ([internal/cli/spawn.go:2333](internal/cli/spawn.go:2333); robot [internal/robot/spawn.go:755](internal/robot/spawn.go:755)) | The Agent Mail name |

Read-back: `applyPaneIdentityVars` ([internal/tmux/session.go:704](internal/tmux/session.go:704)) lets `@ntm_type` override the title-derived type/index/variant when `@ntm_type` is non-empty — this is precisely what makes the "title is the Agent Mail name" state addressable, and why the CLI/robot divergence in §3 matters: a pane whose title is `RubyDog` with no `@ntm_*` vars falls back to title parsing, which yields no type/index (the defect mechanism documented in the main sections above).

### 7. End-to-end timeline (initial launch)

```text
FlatAgent.Index assigned (per type, 1-based)
  internal/cli/agent_spec.go:173 Flatten / internal/cli/spawn.go:459 expandProfileAgents
→ session + panes created (CreateSessionWithHistoryLimit, SplitWindow)
  internal/cli/spawn.go:1774,1867 / internal/robot/spawn.go:543,566
→ first title: FormatPaneName(session, type, index, modelOrPersona)
  internal/cli/spawn.go:2046,2240 / internal/robot/spawn.go:717
→ Agent Mail identity resolved pre-launch (registry reuse else mint)
  internal/cli/spawn.go:2255,2261 → registerSpawnedAgents internal/cli/spawn.go:3222
  internal/robot/spawn.go:724 → resolveAgentMailIdentity internal/robot/spawn.go:836
→ registration RPC: create_agent_identity {project_key, program, model} (no name)
  internal/agentmail/tools.go:109,122 → server returns Agent.Name (e.g. RubyDog)
→ identity written: WriteIdentity + registry.AddAgent (internal/cli/spawn.go:3362,3375)
→ tmux writes: SetPaneIdentity @ntm_type/@ntm_index/@ntm_variant (CLI unconditional
  internal/cli/spawn.go:2284; robot only-if-named internal/robot/spawn.go:748),
  then SetPaneTitle := Agent Mail name (internal/cli/spawn.go:2287; robot :737 via :726)
→ launch: AGENT_NAME=<name> <agent command> via SendKeys
  internal/cli/spawn.go:2333,2366 / internal/robot/spawn.go:755,774
→ (CLI only) bootstrap prompt "register with agent mail. use <name> for your name"
  internal/cli/spawn.go:2422
→ session coordinator registered via register_agent (no pane-title effect)
  internal/cli/spawn.go:2771/2858 → internal/agentmail/session.go:325
→ observable: GetPanes + applyPaneIdentityVars resolves type/index from @ntm_* vars
  internal/tmux/session.go:704
```

### Workflow Closure Map

Production trigger: `ntm spawn` / `--robot-spawn` invocation → source-of-truth write: `FlatAgent.Index` + pane creation → signal/call: `CreateAgentIdentity` RPC to the external Agent Mail server → handler: server mints `Agent.Name` → side effects: identity file + session registry + tmux title/`@ntm_*` writes + `AGENT_NAME` env → read API: `tmux.GetPanes` with `applyPaneIdentityVars` → user-visible result: pane border shows the Agent Mail name while `ntm` addressing resolves by type/index. Error behavior: every Agent Mail failure degrades gracefully (nil status / empty name → NTM-grammar title kept; identity vars still set on the CLI path, unset on the robot path); registration errors never fail spawn ([internal/cli/spawn.go:3220](internal/cli/spawn.go:3220), [internal/robot/spawn.go:784](internal/robot/spawn.go:784)). Load-bearing labels: `spawnSessionLogic` production-called (CLI entry), `GetSpawn`/`launchAgent` production-called (robot entry), `registerSpawnedAgents` production-called (pre-launch :2261 and post-launch :2787), `CreateAgentIdentity` production-called, `SetPaneIdentity` production-called (2 callers, per main document). `highest_new_connector`: none — this is a follow-up documentation pass; no nodes are added or changed (`adds_or_changes: false` throughout).

### ClosureMap (structured — derive() input)

```json
{
  "behavior": "On initial ntm spawn, each pane is titled with its server-minted Agent Mail name while tmux @ntm_type/@ntm_index vars preserve type/index addressing",
  "git_commit": "305bc656dec025680c886439a72e736046cdd9a6",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "flat-agent-index", "module": "internal/cli", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": "AgentSpecs.Flatten" },
    { "id": "spawn-entry", "module": "internal/cli/internal/robot", "is_entrypoint": true, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "create-agent-identity", "module": "internal/agentmail", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "tmux-identity-and-title", "module": "internal/tmux", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "panes-observable", "module": "internal/tmux", "is_entrypoint": false, "adds_or_changes": false, "read_path": "tmux.GetPanes", "seedable_store": null }
  ],
  "edges": [
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null }
  ]
}
```

### Closure adapter (staged proposal — in-document only, not wired into the repo)

> Staged as a proposal inside this research document per the parent instruction to append findings only; no sibling `.closure-adapter.py` file was created and nothing is wired into the repo.

```python
"""Closure adapter (STAGED PROPOSAL — not wired into the repo).
Derived from the ClosureMap for: pane Agent Mail naming at spawn.  Pin: 305bc656dec025680c886439a72e736046cdd9a6.
Promote into /home/maceo/Dev/ntm and complete each TODO(promote) before use.
Speaks the 7-op contract apps/closure-oracle already talks to (mock_adapter.py).
"""
import http.server, json, sys
ASYNC_EDGES = []        # each needs a real driver
CONNECTOR = {e: True for e in ASYNC_EDGES}
SINK = []                                          # Phase-0 /seed_sink target

def handle(op, p):
    if op == "/reset":        SINK.clear(); CONNECTOR.update({e: True for e in ASYNC_EDGES}); return {"ok": True}
    if op == "/set_connector": CONNECTOR[p["edge"]] = p["enabled"]; return {"ok": True}
    if op == "/seed_sink":     SINK.append(p["value"]); return {"ok": True}
    if op == "/seed":
        # TODO(promote): seed AgentSpecs.Flatten with p["data"]        (internal/cli/agent_spec.go:173)
        return {"ok": True}
    if op == "/trigger":
        # TODO(promote): call spawnSessionLogic(p["args"])                (internal/cli/spawn.go:1549)
        return {"ok": True}
    if op == "/drive":
        if not CONNECTOR.get(p["edge"], True): return {"ok": True}  # oracle disabled = red-at-seam
        # TODO(promote): call driver for p["edge"] — (no async edges)   (n/a)
        return {"ok": True}
    if op == "/observe":
        # TODO(promote): return json.dumps(tmux.GetPanes())           (internal/tmux/session.go:704)
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

### Open questions from this follow-up

- The non-JSON CLI path calls `registerSpawnedAgents` twice after launch ([internal/cli/spawn.go:2873](internal/cli/spawn.go:2873) and :2929); both are registry-reuse no-ops after pre-launch minting, but is the duplication intentional?
- Robot spawn writes identity vars only when an Agent Mail name resolves ([internal/robot/spawn.go:748](internal/robot/spawn.go:748)) while CLI spawn writes them unconditionally ([internal/cli/spawn.go:2284](internal/cli/spawn.go:2284)) — should the robot guard be dropped so robot panes keep stable `(type, index)` keys when Agent Mail is down?

