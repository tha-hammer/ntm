# Per-Pane Provider Session ID Record TDD Implementation Plan

## Overview
Fix `ntm sessions save` writing one provider session id to every same-cwd pane. Add a small JSON record that is written when a session spawns and appended when a pane is added. Save reconciles the record against live panes, lazily discovers ids for unmatched panes, and never assigns the same id twice to same-provider panes. Emphasis is on observable saved-state behavior, not internals.

Decisions (user-confirmed): record lives alongside saved state; save lazily discovers and fills unmatched panes; save dedupes ids across same-type panes.

Review-locked amendments (from `2026-10-22-08-19-tdd-pane-id-record-REVIEW.md`, all anchors re-verified against live code): record envelope plus full entry schema; exported `WritePaneRecord`/`AppendPaneRecord` with private dir-taking cores; every record read-modify-write under the existing `acquireLock`; reconcile sorts a copy of live panes by `(WindowIndex, Index)` and keys the dedupe used-set by `(provider, id)` via `ResumeProvider`; refresh never regresses a filled id to empty; spawn writes are merge-only; hook warnings go to stderr via `fmt.Fprintln(os.Stderr, ...)`; spawn hook sits before the JSON fork; the record is keyed by live tmux session name; `Delete`/`Archive`/`Unarchive` carry the sidecar. None of this changes the decided shape: record in `pane-records` subdir, lazy fill, dedupe, six behaviors.

## Current State Analysis
Save flows through `runSessionsSave` (`internal/cli/session_persist.go:134`) into `session.Capture` (`internal/session/capture.go:17`). `mapPaneStates` (`internal/session/capture.go:187`) calls `agentsession.Discover(type, paneCwd)` per pane (`:211`). `Discover` (`internal/agentsession/discover.go:71`) returns only the single newest session per `(agentType, workDir)`. Same cwd means same id on every pane. Nothing pane-specific (pane id, PID, scrollback) feeds discovery.

What exists today:
- Spawn hook point: `spawnSessionLogic` ends with `finalPanes, _ := tmux.GetPanes(opts.Session)` (`internal/cli/spawn.go:2643`) sorted by `sortPanesForAssignment` (`internal/cli/spawn.go:516`, call site `:2647`). The JSON fork `if IsJSONOutput()` follows at `:2701`, so the record hook must sit between `:2647` and `:2701` or JSON mode never records. Pane titles use `tmux.FormatPaneName` (`internal/tmux/session.go:979`).
- Add hook point: `runAdd` (`internal/cli/add.go:263`) creates panes via `tmux.SplitWindow` (`:451`) and titles them via `tmux.SetPaneTitle` (`:466`). The JSON emit `if IsJSONOutput()` precedes `PrintJSON` (`:737`), so the append must happen before it, once per new pane.
- `spawn.go` and `add.go` do not import `internal/session` today (verified: no matches), so the record helpers they call must be exported; testable cores stay private and take a `dir` parameter.
- Storage: `StorageDir()` is `~/.ntm/sessions` (`internal/session/storage.go:25`); `Save` writes `<name>.json` atomically with `0600` (`:43`). `List` reads every top-level `*.json` (`:252`, `IsDir` skip at `:271`), and `ListArchived` applies the same guard (`:417`, `:435`), so the record must live in a subdirectory, not as a sibling `.json` file.
- Locking: `acquireLock` (`internal/session/lock_unix.go:14`) already serializes the `Save`/`Delete`/`Archive`/`Unarchive` paths (`internal/session/storage.go:91,221,343,381`). Record load-modify-write rides this same lock; lock order is flat (one acquisition per command path, helpers never nest).
- Stable pane identity: `(WindowIndex, AgentType, NTMIndex)` from `tmux.Pane` (`internal/tmux/session.go:59`). Tmux pane ids (`%N`) change on restore and must not be the key. `NTMIndex` is 1-based per agent type; `AgentType` stores the `Pane.Type` string exactly as read from `GetPanes`.
- Provider vocabulary: `agentsession.ResumeProvider` (`internal/agentsession/discover.go:49`) maps type aliases to resume providers; `gmi` and `agy` map to distinct providers (pinned by `TestResumeProvider`, `internal/agentsession/agentsession_test.go:38`). Dedupe must key on the provider, never the raw type string.
- Restore treats empty as fresh: `if ps.SessionID != ""` gates the resume command (`internal/session/restore.go:394`); empty means fresh launch, which is why dedupe assigns empty instead of a duplicate.
- Warnings: `output.PrintWarning` writes to stdout (`internal/output/progress.go:304`), so record-hook warnings must use `fmt.Fprintln(os.Stderr, ...)` to keep `--json` output parseable (matches the webhook best-effort precedent at `internal/cli/spawn.go:2652`).
- Record filenames go through `normalizeSavedSessionName` (`internal/session/storage.go:512`), keyed by the live tmux session name, never the `--name` save-as name.
- Tests: stdlib `testing`, table-driven, `t.Parallel()` for pure functions. Fast gate is `go test -short ./...`. `TestMapPaneStates` (`internal/session/session_test.go:844`) covers topology only. `homeDir` in discovery is overridable for tests (`internal/agentsession/discover.go:65`).

### Key Discoveries:
- Spawn-time provider session files usually do not exist yet (created on first agent activity), so spawn writes roster entries with empty ids; ids arrive at save time.
- `List` globbing forces the record into `StorageDir()/pane-records/<sanitized>.json` (dirs are skipped by `IsDir` checks). The `pane-records` directory name lives in one named const beside `sessionDirName`.
- Dedupe helps even with no record: two same-cwd Claude panes currently both get newest; after the fix the second gets empty (fresh launch on resume) instead of a harmful duplicate resume.
- The record works around `Discover` returning single-newest per type+cwd (pinned by `TestDiscoverClaude`); state this rationale in the format comment so a future `Discover` improvement knows what the record is for.

## Desired End State
After spawn, a record exists with one entry per agent pane. After add, it gains one entry. After save, the saved state carries the recorded id per pane, lazily discovered ids for new panes, zero duplicate ids within a provider, and no stale entries. All record I/O is best-effort and never fails spawn, add, or save.

### Record schema (locked)
Envelope: `{"version": 1, "entries": [...]}`. Entry: `{window_index, agent_type, ntm_index, session_id,omitempty, session_provider,omitempty, session_file,omitempty}`. Readers ignore unknown fields; all `session_*` fields are `omitempty`. An empty record is an envelope with an empty array; the file is always kept (so the add-path append always has a target). `session_file` changes in lockstep with `session_id` wherever an id is assigned or cleared.

### Record key and lifecycle (locked)
The record filename derives from the live tmux session name via `normalizeSavedSessionName`, never the `--name` save-as alias. Rename semantics: saving under an alias reads the record of the live session and does not create an alias-named record. Fork semantics: a forked/cloned live session starts with no record and self-populates via lazy fill. `Delete` (`storage.go:195`) deletes the sidecar; `Archive` (`:342`) moves it alongside the archived state; `Unarchive` (`:380`) restores it. Sidecar moves happen inside those functions' existing `acquireLock` scope, so no new lock surface appears.

### Observable Behaviors:
- Spawn writes record: Given spawn with N agent panes, when spawn completes, then the record holds N entries keyed by stable identity with empty session ids.
- Add appends record: Given a live session with a record, when one pane is added, then the record gains exactly one entry for the new pane.
- Save reconciles record: Given a record and live panes, when save runs, then each matched pane's saved `SessionID`/`Provider`/`File` equals its recorded values, with no `Discover` call for recorded panes.
- Save lazily fills: Given a live pane with no record entry, when save runs, then the pane gets a freshly discovered id.
- Save dedupes: Given two same-provider panes resolving to the same discovered id, when save runs, then only the first in `(WindowIndex, Index)` order keeps it; the other gets empty.
- Save drops stale entries: Given a record entry with no matching live pane, when save runs, then the entry is dropped from the refreshed record, and a refreshed filled id is never regressed to empty.
- Automation-visible value change (intended): a pane that previously saved a duplicate id now saves an absent id plus lockstep-cleared `session_file`; consumers must treat absent as "fresh launch", not as missing data.

## What We Are NOT Doing
- No changes to provider session file formats or the casr resume path.
- No scrollback parsing for ids and no PID-to-session-file joining (recorded as open questions in research).
- No migration of old saved states; old saves simply have no record and get lazy-fill plus dedupe.
- No new top-level files; record logic extends `internal/session/storage.go` and `internal/session/capture.go` with tests beside them.
- No `StateVersion` bump (saved values change, schema does not).
- No key-shape change for window reorder: a reorder orphans `(window, type, index)` keys for one save cycle until lazy-fill plus stale-drop self-heals; window stays in the key, used for ordering.
- No per-pane discovery deadline; lazy fill stays best-effort like the rest of capture (save already pays up to 30s per `display-message`).
- No new numbered behaviors beyond the six below; locking, schema, lifecycle, and hook placement are specified inside them.

## Testing Strategy
- **Framework**: Go stdlib `testing`, table-driven, matching `TestMapPaneStates` style.
- **Test Types**: Unit (record read/write/match, reconcile pure logic, dedupe ordering, provider keying), Integration (spawn-to-record and save-round-trip with faked tmux/discovery seams, two-writer lock test, `--json` stdout-parse test), no new E2E (existing `ntm-tvn2` covers save/restore E2E).
- **Mocking/Setup**: Override `homeDir` for discovery fixtures; feed `mapPaneStates`-level inputs as `[]tmux.Pane` literals; use `t.TempDir()` for record storage tests; inject a failing writer to prove hooks never fail commands.
- **Order**: record IO first, then match, then reconcile with lazy fill, then dedupe, then spawn/add hooks last.

## Behavior 1: Record written at spawn

### Test Specification
**Given**: a session spawn that created N agent panes (fake `GetPanes` result as `[]tmux.Pane` literals)
**When**: the spawn-end record hook runs
**Then**: `pane-records/<normalized-session>.json` holds the envelope `{"version": 1, "entries": [...]}` with N entries, each with `window_index`, `agent_type`, `ntm_index`, and empty `session_id`/`session_provider`/`session_file` (fields absent on the wire via `omitempty`)

**Edge Cases**: zero agent panes writes an envelope with an empty array (file kept); user-only panes are skipped; record write failure only warns (stderr) and never fails spawn; re-running the write over a record that already holds non-empty ids never overwrites them with empties (merge-only, pinned by the Behavior 6 re-run test).

**Lifecycle**: `Delete` removes the sidecar; `Archive`/`Unarchive` move/restore it. Pinned by `TestPaneRecordLifecycle`: seed a record, run each operation through `Delete`/`Archive`/`Unarchive`, assert the sidecar follows the state file in each case.

### TDD Cycle

#### Red: Write Failing Test
**File**: `internal/session/session_test.go`
```go
func TestWritePaneRecordAtSpawn(t *testing.T) {
    t.Parallel()
    // Arrange: dir := t.TempDir(), panes := []tmux.Pane{{...}, {...}}
    // Act: err := writePaneRecord(dir, "proj", panes)
    // Assert: read back JSON, want version 1 plus N entries keyed by
    // (window, type, ntm index) with empty/absent session fields
}

func TestPaneRecordLifecycle(t *testing.T) {
    // Arrange: seeded pane-records/<name>.json via writePaneRecord
    // Act/Assert: Delete drops the sidecar; Archive moves it; Unarchive restores it
}
```

#### Green: Minimal Implementation
**File**: `internal/session/storage.go`
```go
// PaneRecordEntry + PaneRecordFile{Version, Entries} + exported WritePaneRecord
// (resolves StorageDir + normalizeSavedSessionName, takes acquireLock);
// private writePaneRecord(dir, name, panes) core for tests. MkdirAll 0700,
// atomic write 0600 via the existing Save helper. Merge-only: preserve any
// existing non-empty session_* values for matched keys. Sidecar follow-through
// inside Delete/Archive/Unarchive under their existing lock scope.
// pane-records directory name is a named const beside sessionDirName.
```

#### Refactor: Improve Code
Reuse the existing atomic-write helper used by `Save` instead of a second copy. Share the load/parse path between append, reconcile, and lifecycle.

### Success Criteria
**Automated:**
- [x] Test fails before implementation (Red): `go test -short -run 'TestWritePaneRecordAtSpawn|TestPaneRecordLifecycle' ./internal/session/`
- [x] Test passes after (Green): same command
- [x] Package suite green: `go test -short ./internal/session/`

**Manual:**
- [ ] `ntm spawn` output unchanged; record file appears under `~/.ntm/sessions/pane-records/`

---

## Behavior 2: Record appended on pane add

### Test Specification
**Given**: an existing record with K entries
**When**: one pane is added (new `tmux.Pane` captured right after `SplitWindow`/`SetPaneTitle`, carrying its stable `Index`)
**Then**: the record holds K+1 entries; the K old entries are semantically equal (field-by-field, plus one golden-file test for the exact JSON shape; not a byte-identical assertion)

**Edge Cases**: missing record file means create with one entry; add of a user pane leaves the record untouched; concurrent add+add never loses an entry (two-goroutine append test under `acquireLock` asserts K+2); a re-added pane after a kill takes `max+1` and must not inherit the killed pane's id (stale-drop at save heals any key reuse; assert the new entry starts empty).

### TDD Cycle

#### Red: Write Failing Test
**File**: `internal/session/session_test.go`
```go
func TestAppendPaneRecordOnAdd(t *testing.T) {
    // Arrange: seed record with K entries; Act: AppendPaneRecord core (...new pane...)
    // Assert: K+1 entries, old entries semantically equal, golden JSON match
}

func TestAppendPaneRecordConcurrent(t *testing.T) {
    // Arrange: seeded record; Act: two goroutines append distinct panes
    // Assert: both entries present, none lost
}
```

#### Green: Minimal Implementation
**File**: `internal/session/storage.go`
```go
// Exported AppendPaneRecord (StorageDir + normalizeSavedSessionName resolution,
// acquireLock load-modify-write); private dir-taking core for tests.
// Missing file -> single-entry record via the Behavior 1 writer.
```

#### Refactor: Improve Code
Share load/parse path between append and save-reconcile (from Behavior 1 refactor).

### Success Criteria
**Automated:**
- [x] Red then Green: `go test -short -run 'TestAppendPaneRecordOnAdd|TestAppendPaneRecordConcurrent' ./internal/session/`
- [x] Package suite green: `go test -short ./internal/session/`

**Manual:**
- [ ] `ntm add` output unchanged; record gains one entry.

---

## Behavior 3: Save reconciles record against live panes

### Test Specification
**Given**: a record with ids for panes A and B, plus live panes A, B, C
**When**: save captures state
**Then**: saved panes A and B carry their recorded `session_id`/`provider`/`file`; C goes to lazy fill; no `Discover` call is made for A or B (assert via call count on a fake discover func); unmatched panes sharing one `(provider, cwd)` share one memoized discovery result per save (assert single call for C+D with same provider+cwd)

**Edge Cases**: corrupt record JSON behaves like a missing record; record entries for user panes are ignored; `mapPaneStates` delegates to the pure core with real `Discover`, so topology behavior in `TestMapPaneStates` is untouched.

### TDD Cycle

#### Red: Write Failing Test
**File**: `internal/session/session_test.go`
```go
func TestReconcilePaneRecord(t *testing.T) {
    // Arrange: record entries + live panes (A, B recorded; C new)
    // Act: states, refreshed := reconcilePaneIDs(panes, record, fakeDiscover)
    // Assert: A, B keep recorded ids; fakeDiscover called only for C
}
```

#### Green: Minimal Implementation
**File**: `internal/session/capture.go`
```go
// Pure core reconcilePaneIDs(panes, record, discover func(agentType, cwd string) *agentsession.Info)
//   -> ([]PaneState, []PaneRecordEntry) in capture.go; mapPaneStates delegates
// with real Discover. Match live panes by (WindowIndex, AgentType, NTMIndex)
// on a copy sorted by (WindowIndex, Index) (mirrors restore.go); fill
// SessionID/Provider/File from record. Memoize discover per (provider, cwd).
```

#### Refactor: Improve Code
Fold the match key into one small comparable struct used by both reconcile and stale-drop; keep the memo map inside the single reconcile pass.

### Success Criteria
**Automated:**
- [x] Red then Green: `go test -short -run TestReconcilePaneRecord ./internal/session/`
- [x] Package suite green: `go test -short ./internal/session/`

**Manual:**
- [ ] Hand-fixed ids in the record file (not the saved JSON) survive the next `ntm sessions save` without re-editing.

---

## Behavior 4: Save lazily fills unmatched panes with dedupe

### Test Specification
**Given**: live panes C and D (same provider, same cwd) with no record entries, where discovery returns id X for both, fed in reverse `(WindowIndex, Index)` order
**When**: save captures state
**Then**: the first pane in sorted stable order gets id X; the second gets empty `session_id` with lockstep-cleared `session_file` (never a duplicate); a `gmi` pane and an `agy` pane resolving to the same raw id do NOT dedupe against each other (provider-keyed used-set, mirroring `TestResumeProvider`)

**Edge Cases**: discovery returning nil for a pane leaves it empty; different providers never share the dedupe set; recorded ids seed the used-set before lazy fill runs, so lazy fill can never steal an id already recorded for another pane.

### TDD Cycle

#### Red: Write Failing Test
**File**: `internal/session/session_test.go`
```go
func TestLazyFillDedupesAcrossSameProviderPanes(t *testing.T) {
    // Arrange: panes C, D same provider+cwd in reverse order; fakeDiscover always returns "X"
    // Act: reconcile with empty record
    // Assert: exactly the sorted-first pane has SessionID "X"; the other is ""
}

func TestDedupeKeysByProviderNotType(t *testing.T) {
    // Arrange: gmi + agy panes, discovery returns same raw id for both
    // Act: reconcile with empty record
    // Assert: both keep the id (distinct providers, no dedupe)
}
```

#### Green: Minimal Implementation
**File**: `internal/session/capture.go`
```go
// Track usedIDs := map[provider]map[id]bool keyed by ResumeProvider(agentType),
// never the raw type string; seeded from recorded ids; skip any discovered id
// already used; assign only unused ids. Operates on the sorted copy from
// Behavior 3 so first-in-order wins deterministically.
```

#### Refactor: Improve Code
Extract the used-set into the same match-key struct area so reconcile stays a single pass.

### Success Criteria
**Automated:**
- [x] Red then Green: `go test -short -run 'TestLazyFillDedupesAcrossSameProviderPanes|TestDedupeKeysByProviderNotType' ./internal/session/`
- [x] Regression: existing `TestDiscoverClaude` newest-wins behavior untouched: `go test -short ./internal/agentsession/`
- [x] Package suite green: `go test -short ./internal/session/`

**Manual:**
- [ ] Two same-cwd Claude panes save with distinct-or-empty ids, never the same id twice.

---

## Behavior 5: Save drops stale entries and refreshes the record

### Test Specification
**Given**: a record with entries for panes A (live, filled id) and Z (killed)
**When**: save runs
**Then**: the refreshed record holds A only; the saved state has no trace of Z; if lazy fill yields empty for A this cycle (idle agent, nil discovery), the refreshed record keeps A's old non-empty id (never regress filled to empty — only fill empties and drop unmatched entries)

**Edge Cases**: all entries stale rewrites an envelope with an empty array (file kept, so add-path append still works); save with no live agent panes still succeeds; the refresh write runs only after a successful `Save`, and a refresh-write failure is swallowed (best-effort, save result stands).

### TDD Cycle

#### Red: Write Failing Test
**File**: `internal/session/session_test.go`
```go
func TestStaleRecordEntriesDroppedOnSave(t *testing.T) {
    // Arrange: record {A filled, Z}, live panes {A} with nil discovery for A
    // Act: reconcile + refresh record
    // Assert: refreshed record == {A with old id intact}
}
```

#### Green: Minimal Implementation
**File**: `internal/session/storage.go` + `internal/session/capture.go`
```go
// After reconcile, rewrite the record from live matched panes (keeping filled
// ids per the never-regress invariant). Refresh executes post-Save success
// inside the save path's acquireLock scope; refresh errors swallowed.
```

#### Refactor: Improve Code
Reuse `writePaneRecord` core for the refresh path.

### Success Criteria
**Automated:**
- [x] Red then Green: `go test -short -run TestStaleRecordEntriesDroppedOnSave ./internal/session/`
- [x] Full fast gate: `go test -short ./...` (remaining failures proven pre-existing on base)

**Manual:**
- [ ] Kill a pane, save, and confirm the record no longer lists it.

---

## Behavior 6: Spawn and add hooks wired (best-effort)

### Test Specification
**Given**: `spawnSessionLogic` between `sortPanesForAssignment` (`internal/cli/spawn.go:2647`) and the JSON fork `if IsJSONOutput()` (`:2701`), and `runAdd` after `SetPaneTitle` (`internal/cli/add.go:466`) but before the JSON emit (`:737`)
**When**: spawn or add completes
**Then**: the exported record hook runs with the live session name; any hook error is reported via `fmt.Fprintln(os.Stderr, ...)` only and never fails the command; stdout stays valid JSON under `--json` even with a failing record write (assert by parsing captured stdout)

**Edge Cases**: hook runs after titles are set so NTM indices exist; spawn re-run over an existing filled record merges only (re-run test: seed filled record, run hook with empty-id panes, assert ids intact); add appends exactly one entry per new pane before the JSON emit.

### TDD Cycle

#### Red: Write Failing Test
**File**: `internal/cli/spawn_state_test.go` (existing spawn-state test file)
```go
// Assert the record-write helper is invoked with finalPanes; inject a failing
// writer and assert spawn still returns nil and captured stdout still parses
// as JSON. Re-run case: seeded filled record + empty-id panes -> ids intact.
```

#### Green: Minimal Implementation
**File**: `internal/cli/spawn.go`, `internal/cli/add.go`
```go
// if err := session.WritePaneRecord(...liveSessionName, finalPanes...); err != nil {
//     fmt.Fprintln(os.Stderr, "warning: ...", err) }  // never output.PrintWarning*
// Add: session.AppendPaneRecord per new pane pre-emit, same stderr discipline.
```

#### Refactor: Improve Code
Single shared stderr warn helper if both call sites take the same shape; otherwise leave two small call sites.

### Success Criteria
**Automated:**
- [x] Red then Green: `go test -short -run TestSpawnWritesPaneRecord ./internal/cli/`
- [x] Full fast gate plus format: `go test -short ./...`, `gofmt -l .`, `go build ./cmd/ntm` (remaining failures proven pre-existing on base)

**Manual:**
- [ ] `ntm spawn` and `ntm add` behave exactly as before when the record dir is unwritable.

---

## Integration and E2E Testing
- Integration: spawn a real session, add a pane, save, and assert the saved JSON has per-pane entries with no duplicate ids within a provider (`ntm-tvn2` covers save/restore E2E; extend it rather than adding a new suite).
- E2E: resume from the saved state and confirm distinct conversations resume per pane.

## Review disposition (zero open items)
| # | Review finding | Plan location |
|---|---|---|
| 1 | Full record schema + envelope | Desired End State "Record schema (locked)", Behavior 1 spec |
| 2 | Exported surface and seams (`WritePaneRecord`/`AppendPaneRecord`, pure `reconcilePaneIDs`, dir-taking cores, `normalizeSavedSessionName`) | Current State, Behaviors 1–3 |
| 3 | `acquireLock` on every record read-modify-write + two-writer test | Current State, Behaviors 1, 2, 5 |
| 4 | Sort copy by `(WindowIndex, Index)`; used-set keyed `(provider, id)` via `ResumeProvider`; reverse-order + gmi-vs-agy tests | Behaviors 3, 4 |
| 5 | Never regress filled id to empty; refresh only after successful `Save`; refresh failure swallowed | Behavior 5 |
| 6 | Merge-only spawn writes + re-run test | Behaviors 1, 6 |
| 7 | `fmt.Fprintln(os.Stderr, ...)` + `--json` stdout-parses test | Current State, Behavior 6 |
| 8 | Spawn hook at `spawn.go:2647` pre-fork; add hook per pane pre-emit (`add.go:737`) | Current State, Behavior 6 |
| 9 | Key by live session name; rename/fork semantics; `Delete`/`Archive`/`Unarchive` sidecar | Desired End State "Record key and lifecycle", Behavior 1 lifecycle test |
| C | Citation corrections (`Save` `:43`, `ListArchived` `:425`/`IsDir` `:435`, `TestMapPaneStates` `:844`) | Current State, References |

## References
- Research: `thoughts/searchable/shared/research/2026-10-22-08-10-per-pane-session-id-discovery.md`
- Review: `thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record-REVIEW.md`
- Related issues: `ntm-tvn2` (save/restore E2E), `ntm-gqtp` (heuristic codex/gemini discovery)
- Save entry: `internal/cli/session_persist.go:134`
- Capture: `internal/session/capture.go:17`, `mapPaneStates` at `:187`, discover call at `:211`
- Discovery: `internal/agentsession/discover.go:71`; `ResumeProvider` at `:49`; `homeDir` at `:65`
- Storage: `internal/session/storage.go:25` (`StorageDir`), `:43` (`Save`), `:252` (`List`), `:271` (glob `IsDir` guard), `:417` (`ListArchived`), `:435` (same guard), `:512` (`normalizeSavedSessionName`); `Delete` `:195`, `Archive` `:342`, `Unarchive` `:380`; lock at `:91,221,343,381`
- Lock: `internal/session/lock_unix.go:14` (`acquireLock`)
- Spawn hook: `internal/cli/spawn.go:2643` (`GetPanes`), `:2647` (sort call site), `:2701` (JSON fork), `:2652` (stderr best-effort precedent); sort def at `:516`; add hook: `internal/cli/add.go:451`, `:466`, `:737` (JSON emit)
- Restore empty-means-fresh: `internal/session/restore.go:394`
- Stdout warning hazard: `internal/output/progress.go:304`
