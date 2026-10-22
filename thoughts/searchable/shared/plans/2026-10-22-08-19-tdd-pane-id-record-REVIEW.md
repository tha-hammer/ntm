# Plan Review Report: Per-Pane Provider Session ID Record TDD Plan

**Plan**: `thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record.md`
**Date**: 2026-10-22
**Reviewers**: general reviewer + contract, interface, promise, data-model, API-surface, plan-hygiene analysts (all claims verified against live code)

## Review Summary

| Category | Status | Issues Found |
|----------|--------|--------------|
| Contracts | warning | 10 (4 high) |
| Interfaces | warning | 10 (4 high) |
| Promises | warning | 8 (3 high) |
| Data Models | warning | 15 (3 high) |
| APIs | warning | 8 (2 high) |
| CodeCleanup Gates | pass with notes | 7 prospective guardrails |

**Approval status: Needs Minor Revision** — the design is sound and correctly scoped. Every finding below is a specification gap, not a redesign. Address the required fixes in the plan, then implement.

## What's good (do not change)

- Behavior slicing is right: six independently testable behaviors in TDD order.
- Record under `StorageDir()/pane-records/` correctly avoids the `List` top-level `*.json` glob (`internal/session/storage.go:271`).
- Stable key excludes tmux `%N` ids and tmux positional `Index`; empty-at-spawn plus lazy fill matches provider reality.
- Dedupe direction (first keeps id, other gets empty) is a strict improvement: `Resume` treats empty as fresh launch (`internal/session/restore.go:394`), which beats replaying one conversation into every pane.
- Best-effort hooks with stderr-only warnings match the webhook precedent (`internal/cli/spawn.go:2652`).
- Non-goals hold the scope tight. No `StateVersion` bump needed (values change, schema does not).

## Citation corrections (trivial, fix while editing)

- `Save` is at `storage.go:43`, not `:45`. Second `List`-glob cite `:425` is `ListArchived` (same `IsDir` guard at `:435`, conclusion holds). `TestMapPaneStates` is at `session_test.go:844`, not `:842`.

## Required fixes (resolve before Behavior 1)

1. **Pin the full record schema.** Behavior 1 lists only four fields, but Behavior 3 fills `SessionID/Provider/File`. Specify entry `{window_index, agent_type, ntm_index, session_id,omitempty, session_provider,omitempty, session_file,omitempty}` inside envelope `{"version": 1, "entries": [...]}`. Readers ignore unknown fields; new fields are `omitempty`. This also resolves `ntm_index` 1-based per-type scoping, `agent_type` vocabulary (store the `Pane.Type` string as read from `GetPanes`), and empty-record representation (empty array, file always kept).
2. **Pin the exported surface and seams.** Exported `WritePaneRecord` / `AppendPaneRecord` (package `cli` has no `session` import today and cannot call lowercase helpers). Pure core `reconcilePaneIDs(panes, record, discover func(agentType, cwd string) *agentsession.Info) ([]PaneState, []PaneRecordEntry)` in `capture.go`; `mapPaneStates` delegates with real `Discover`. Test seam is a `dir` parameter on private helpers; production resolves `StorageDir()` plus `normalizeSavedSessionName` (`storage.go:512`).
3. **Put record read-modify-write under `acquireLock`.** Concurrent add+add loses appends; concurrent save+save can double-assign one discovered id; spawn-hook write can land between save's read and refresh. Same lock everywhere, stated lock order. Add a two-writer append test.
4. **Define reconcile order and sort before dedupe.** Sort a copy of live panes by `(WindowIndex, Index)` (mirrors `restore.go`) before matching and assignment. Assert with reverse-ordered input in Behavior 4. Key the dedupe used-set by `(provider, id)` via `ResumeProvider`, never the raw type string (locks `gmi != agy` with a test).
5. **Refresh never regresses a filled id to empty.** If lazy fill yields empty (idle agent, nil discovery), the refreshed record keeps the old non-empty id. Only fill empties and drop unmatched entries.
6. **Spawn hook is merge-only.** A re-run (retry, double spawn, spawn/save race) must never overwrite non-empty recorded ids with empties. Add a re-run test.
7. **Warnings go through `fmt.Fprintln(os.Stderr, ...)`, never `output.PrintWarning*`.** The latter writes to stdout (`internal/output/program.go:304`) and would break `--json` parseability. Add a `--json` stdout-parses test with a failing record write.
8. **Spawn hook sits before the JSON fork.** Insert immediately after `sortPanesForAssignment` (`spawn.go:2647`), before `if IsJSONOutput()` (`:2701`), or JSON mode never records. Add hook appends once per new pane before the JSON emit (`add.go:737`).
9. **Record is keyed by live tmux session name** (through `normalizeSavedSessionName`), never the `--name` save-as name. Document rename/fork semantics. `Delete` deletes the sidecar; `Archive`/`Unarchive` move/restore it (or consciously defer in NOT-doing; orphans are currently silent).

## Advisory warnings (address if cheap)

- Memoize one discovery result per `(provider, cwd)` per save: N same-cwd panes currently fire N identical tree walks, and a file created mid-save can return different ids per pane.
- Bound lazy fill (per-pane deadline, skip recorded as slow) — save already pays up to 30s per `display-message`.
- `add` entry should prefer re-`GetPanes` matched by new pane id over local `num`; verify `maxIndices` cannot reuse a killed pane's NTM index or a re-added pane inherits a dead id.
- Window reorder orphans `(window, type, index)` keys for one save cycle until lazy-fill self-heals; consider matching on `(agent_type, ntm_index)` with window as ordering-only payload.
- Byte-identical append assertion is over-strict; assert semantic equality plus one golden-file test.
- Fix Behavior 3's manual criterion to say hand-fixed ids go in the record, not the saved JSON.
- Document the observable value change (duplicate id becomes absent) as the intended fix for automation consumers; note `session_file` changes in lockstep.
- Adopt-path and controller panes have no record entries until lazy fill; renamed titles churn through lazy-fill plus stale-drop.
- Record the workaround rationale in the format comment: the record works around `Discover` returning single-newest per type+cwd, pinned by `TestDiscoverClaude`.

## Suggested plan amendments

```diff
# Behavior 1: schema + surface
+ Add: envelope {"version": 1, "entries": [...]}, entry tags window_index/agent_type/ntm_index/session_id?/session_provider?/session_file?
+ Add: exported WritePaneRecord/AppendPaneRecord; private dir-taking cores for tests
+ Add: normalizeSavedSessionName for record filenames; pane-records/ named const beside sessionDirName
# Behavior 3: reconcile
+ Add: reconcilePaneIDs(panes, record, discoverFn) -> (states, refreshedRecord); mapPaneStates delegates
+ Add: sort copy by (WindowIndex, Index) before match/assign; used-set keyed (provider, id)
~ Modify: no Discover calls for recorded panes stays; unmatched panes share one memoized result per (provider, cwd)
# Behavior 4: dedupe
+ Add: reverse-order input test; gmi-vs-agy provider-key test
# Behavior 5: refresh
+ Add: never-overwrite-filled invariant; refresh runs only after successful Save; refresh failure swallowed
# Behavior 6: hooks
+ Add: spawn hook at spawn.go:2647 pre-fork; add hook per pane pre-emit; fmt.Fprintln(os.Stderr) warnings; merge-only spawn writes
# Locking (all behaviors)
+ Add: acquireLock across record load->reconcile->refresh and append; two-writer test
```

## Tracking

- No `bd` issue was created: `bd create` is refused on this clone (remote schema v49 vs v53, writes blocked). Related open issues: `ntm-tvn2`, `ntm-gqtp`.
- Research: `thoughts/searchable/shared/research/2026-10-22-08-10-per-pane-session-id-discovery.md`.
