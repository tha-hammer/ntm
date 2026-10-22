# Add Pane Identity TDD Implementation Plan

## Overview
Fix `ntm add` creating panes that carry identity only in their titles. `runAdd` titles each new pane `<session>__<type>_<index>` but never writes the tmux `@ntm_type`/`@ntm_index` identity vars, and it computes the next index by re-parsing pane titles alone. Any existing pane whose title is an Agent Mail name (e.g. `RubyDog`) is invisible to that seeding, so `add` re-issues an in-use `(type, index)`, the pane-record key `(WindowIndex, AgentType, NTMIndex)` collides, and save-time reconcile persists duplicate keys/ids on every save thereafter. Emphasis is on observable behavior: added panes resolve by type/index even after retitling, `add` never re-issues a live index, and a live key collision warns visibly instead of silently duplicating the record forever.

Source research: [2026-10-22-11-29-add-identity-callers.md](/home/maceo/Dev/ntm/thoughts/searchable/shared/research/2026-10-22-11-29-add-identity-callers.md) (read fully, including the 2026-10-22 follow-up on pane naming and Agent Mail registration).

Baseline precondition (blocking, verified 2026-10-22):
- The pane-record implementation this plan builds on is **uncommitted in the working tree**, not on `HEAD` (`305bc656`). `git status --short` shows modified [add.go](/home/maceo/Dev/ntm/internal/cli/add.go), [spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go), [spawn_state_test.go](/home/maceo/Dev/ntm/internal/cli/spawn_state_test.go), [capture.go](/home/maceo/Dev/ntm/internal/session/capture.go), [storage.go](/home/maceo/Dev/ntm/internal/session/storage.go), [state.go](/home/maceo/Dev/ntm/internal/session/state.go), and [session_test.go](/home/maceo/Dev/ntm/internal/session/session_test.go). Every line reference, hook (`paneRecordAppendHook`), and function (`reconcilePaneIDs`, `upsertPaneRecordEntries`, `paneRecordKeyOf*`) cited below exists only in that dirty tree.
- **Land that changeset first** as its own commit/PR (the [2026-10-22-08-19 pane-id-record plan](/home/maceo/Dev/ntm/thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record.md)), with its tests green. Do not start Behavior 1 Red on a clean `HEAD` checkout and do not fold the uncommitted baseline into this fix's diff. Gate: `git status --short` is clean for the paths above and `go test -short ./internal/cli/ ./internal/session/` passes before any behavior below starts.

Locked decisions (no open questions):
- `add` writes identity via the existing `tmux.SetPaneIdentity` at creation, mirroring `spawn`; variant is `agent.Model` (for persona adds this is already the persona name, matching spawn's model-or-persona rule). The write is tested through a pure identity-args helper plus one extracted writer behind a var seam — `runAdd` itself is not unit-driven (see Behavior 1 for why, verified against live code).
- Next-index seeding reads the `Type`/`NTMIndex` fields `GetPanes` already resolved (identity-var-backed), unioned with the legacy title parse so title-only legacy panes still count. Seed keys **and** loop keys are canonical: today the loop key is the raw `string(agent.Type)` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:490)), so canonicalizing the loop lookup is part of Behavior 2's production change, not a pre-existing fact.
- Record dedupe is by `paneRecordKey` with first-in-sorted-order winning; the loser persists no record entry **and its assigned `SessionID`/`SessionProvider`/`SessionFile` in the saved state are cleared to empty** (including the distinct-cwd lazy-fill case where the loser discovered a different id), the winner's linkage is untouched, and the collision is reported by `reconcilePaneIDs` as a returned value and emitted exactly once per colliding key per `Capture` call on stderr (see Behavior 3). While the live collision persists, every save warns again — that repetition is intended, not a defect.
- `scale` needs no code change and **no new test**: scale-up already delegates to `runAdd` ([scale.go](/home/maceo/Dev/ntm/internal/cli/scale.go:310)); inheritance is verified by Behaviors 1–2 passing plus existing scale tests and the manual check (see Behavior 4). No `runAdd` seam is added for scale.
- Robot spawn: identity-write parity is in scope (drop the Agent-Mail-only guard) behind three named var seams in [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go) (see Behavior 5); robot pane-record writing is an explicit non-goal (see What We're NOT Doing).

## Current State Analysis
`runAdd` (`internal/cli/add.go:301`) is called only by the cobra `RunE` (`internal/cli/add.go:238`) and by `runScale` scale-up (`internal/cli/scale.go:310`). It snapshots panes once (`tmux.GetPanes`, `internal/cli/add.go:413`), seeds `maxIndices` via `parseIndex`/`paneTitleTypeAndIndex` applied to `p.Title` only (`internal/cli/add.go:421-430`), then pre-increments per agent (`internal/cli/add.go:503`). The loop creates each pane with `tmux.SplitWindow` (`internal/cli/add.go:492`), titles it with `tmux.FormatPaneName` + `tmux.SetPaneTitle` (`internal/cli/add.go:506-507`), and never calls `tmux.SetPaneIdentity` — the string does not appear in `add.go`. After the loop, `recordAddedPanes` (`internal/cli/add.go:55`, hook var `paneRecordAppendHook` at `internal/cli/add.go:46`) re-reads panes once and appends one record entry per new pane id (`internal/cli/add.go:771`); it never returns non-nil.

What exists today:
- Identity primitive: `SetPaneIdentity` (`internal/tmux/session.go:686`, package wrapper `:695`) is the sole writer of `@ntm_type`/`@ntm_index`/`@ntm_variant`; it returns nothing and discards errors (`RunSilent`), so calling it can never fail `add`. `SetPaneTitle` (`internal/tmux/session.go:649`) writes title only.
- Read-back: `GetPanes` appends the identity vars last and `applyPaneIdentityVars` ([session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:704)) overrides the title parse whenever `@ntm_type` is non-empty (`@ntm_index` alone is ignored). `parsePaneFromParts` ([session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:1716)) therefore already populates `Pane.Type`/`Pane.NTMIndex`/`Pane.Variant` ([session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:59)) correctly for identity-backed panes — `runAdd` simply ignores those fields when seeding. (The source research cites `:1741` for `parsePaneFromParts`; live code puts it at `:1716`. This plan uses the live number; the research file is not edited here.)
- Spawn precedent: `spawnSessionLogic` calls `tmux.SetPaneIdentity(pane.ID, string(agent.Type), agent.Index, identityVariant)` unconditionally ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2284)) before the Agent Mail retitle ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2285)), with model-or-persona variant ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2280)). Its record hook `recordSpawnPanes` wraps `session.WritePaneRecord` ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:530)).
- Title seeding defect surface: `paneTitleTypeAndIndex` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:273)) only sees NTM-grammar titles; `TestPaneTitleTypeAndIndex` ([cli_helpers_extra_test.go](/home/maceo/Dev/ntm/internal/cli/cli_helpers_extra_test.go:126)) pins `project__cc_2` → `(cc, 2)` and `custom-title` → not ok. An Agent Mail title like `RubyDog` contributes nothing. Seed keys are canonicalized title types ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:295)) while the loop key at [add.go](/home/maceo/Dev/ntm/internal/cli/add.go:490) is the raw `agentTypeStr := string(agent.Type)`, consumed raw at [add.go](/home/maceo/Dev/ntm/internal/cli/add.go:503) — a second, quieter mismatch Behavior 2 fixes by canonicalizing the loop lookup.
- Record keys: `paneRecordKeyOfPane`/`paneRecordKeyOfEntry` (`internal/session/storage.go:579`) build `(WindowIndex, AgentType, NTMIndex)`; only resumable types participate (`isAgentPaneForRecord`, `internal/session/storage.go:589`). `appendPaneRecord` (`internal/session/storage.go:702`) already replaces a same-key stored entry, and `upsertPaneRecordEntries` (`internal/session/storage.go:646`) is first-wins on stored keys — but neither dedupes two live panes arriving in one refreshed list.
- Save-time collision behavior: `reconcilePaneIDs` (`internal/session/capture.go:220`) lets the first pane in `(WindowIndex, Index)` order claim the recorded id (`internal/session/capture.go:290`); the loser saves empty, yet the refreshed list re-copies the stored id onto the loser's entry (never-regress, `internal/session/capture.go:372`), so `refreshPaneRecordAfterSave` (`internal/session/storage.go:737`) persists two entries with the same key and id on every save. There is no warning anywhere on this path.
- Robot spawn divergence: `launchAgent` ([spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:713)) writes the fallback or Agent Mail title ([spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:717)) via a direct `tmux.SetPaneTitle` at [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:737) whose error returns early at `:738-741` — before the identity line — and calls `tmux.SetPaneIdentity` only inside `if agentMailName != ""` ([spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:747)), with empty variant, and writes no pane record at all. No tmux call in `launchAgent` goes through a var seam today. CLI spawn writes identity unconditionally — the parity gap named in the research follow-up.
- Never-fail-add convention: `recordAddedPanes` warns on stderr via `fmt.Fprintln(os.Stderr, ...)` (`internal/cli/add.go:61,74`) and never fails the command; auto-checkpoint and webhook init in `runAdd` are likewise best-effort. `output.PrintWarning*` goes to stdout, so all new warnings use stderr to keep `--json` output parseable.
- Test conventions: Go stdlib `testing`, table-driven, hooks as package-level vars stubbed with `t.Cleanup` restore (pattern: `TestRecordAddedPanes`, `internal/cli/spawn_state_test.go:361`). Gates: `make test` = `go test -v -short ./...` (Makefile:61), plus `go vet ./...` and `golangci-lint run` in CI.

### Key Discoveries:
- The fix needs no new tmux machinery: `SetPaneIdentity` is already error-swallowing and `GetPanes` already resolves identity-backed `Type`/`NTMIndex`; only `runAdd`'s seeding and creation writes lag behind `spawn`.
- Because `runAdd` snapshots panes once and never re-reads between panes, correct seeding plus the existing pre-increment is sufficient — no per-pane re-read is needed once name-titled panes become visible.
- The duplicate-record persistence lives in the refreshed list built by `reconcilePaneIDs`, not in `appendPaneRecord`; dedupe must land there (with a storage-level guard as defense in depth).
- `SetPaneIdentity` is a plain function on `DefaultClient`, not a var seam, and `runAdd` calls `tmux.GetPanes` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:413)), `tmux.SplitWindow` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:492)), and `tmux.SetPaneTitle` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:507)) directly with no stubbable seam anywhere in the loop. Driving `runAdd` from a unit test would require stubbing all of those plus command generation and `SendKeys` — this plan does **not** add that seam family. Instead Behavior 1 extracts a pure identity-args helper and one thin writer behind a single new var seam (`setPaneIdentityHook`, mirroring `paneRecordAppendHook` at [add.go](/home/maceo/Dev/ntm/internal/cli/add.go:46)), unit-tests those, and covers the `runAdd` wiring itself with the manual tmux check. The same direct-call defect in robot `launchAgent` is handled in Behavior 5 with three named seams, because that function is small enough for the seam route to stay honest.

## Desired End State
After `ntm add`, each new pane carries `@ntm_type`/`@ntm_index` (and `@ntm_variant` when a model/persona applies) identical to what its title encodes, so retitling it later (e.g. to an Agent Mail name) never loses addressing. `add` never assigns an index already held by a live same-type pane, however that pane is titled. `scale` up behaves identically by inheritance. When two live panes nonetheless collide on one record key (legacy sessions, manual retitles), save keeps the first pane's linkage, saves the loser empty, warns visibly on stderr, and persists exactly one record entry per key. Robot-spawned panes carry identity vars whether or not Agent Mail resolved a name. No failure in identity writing, record appending, or dedupe warning ever fails `add`, `scale`, `spawn`, or save.

### Observable Behaviors:
- Add writes identity: Given a live session, when `add` creates a pane titled `<session>__cc_2`, then the pane's `@ntm_type`/`@ntm_index`/`@ntm_variant` equal `cc`/`2`/model-or-persona.
- Add never re-issues a live index: Given an existing `cc` pane titled `RubyDog` with identity `(cc, 1)`, when `add --cc=1` runs, then the new pane is numbered 2, not 1.
- Save dedupes colliding keys with a warning: Given two live panes on one record key, when save runs, then exactly one record entry persists for that key, the loser's saved `SessionID` is empty, and a stderr warning names the collision — once per colliding key per save, repeating on every save until the live collision is resolved.
- Scale inherits: Given the same `RubyDog` session, when `scale` adds a `cc` pane via `runAdd`, then it is numbered 2 and carries identity vars, with no scale-specific code and no scale-specific test — the delegation at [scale.go](/home/maceo/Dev/ntm/internal/cli/scale.go:310) plus Behaviors 1–2 are the proof.
- Robot identity parity: Given robot spawn with Agent Mail unavailable, when a pane launches with its fallback title, then `@ntm_type`/`@ntm_index` are still set.
- Best-effort preserved: Given identity-write or record-append failure injection, when `add` runs, then `add` still succeeds, launches agents, and warns on stderr only.

## What We're NOT Doing
- No robot pane-record writing: robot spawn has no record hook today and its roster semantics differ from CLI spawn; assessed and deferred as a follow-up, not silently dropped (research open question 2).
- No changes to restore/resume, checkpoint restore, adopt, controller, create, ensemble/swarm, or context-rotation identity (research §3); they replay or compute titles only, and stable keys there are separate follow-ups.
- No change to the record key shape, record schema, `StateVersion`, or `Discover` semantics; dedupe works within the existing `(WindowIndex, AgentType, NTMIndex)` key.
- No new CLI flags, output fields, or JSON shape changes; the only new user-visible output is the stderr collision warning.
- No migration of already-duplicated stored records beyond save-time healing: the next save after this fix persists one entry per key (stale duplicates drop because the refreshed list contains only deduped live panes).
- No per-pane `GetPanes` re-read inside the add loop; the single snapshot plus correct seeding is the design.

## Testing Strategy
- **Framework**: Go stdlib `testing`, table-driven (`tests := []struct{...}` + `t.Run`), matching [cli_helpers_extra_test.go](/home/maceo/Dev/ntm/internal/cli/cli_helpers_extra_test.go:126) and [session_test.go](/home/maceo/Dev/ntm/internal/session/session_test.go:1050). No new test libraries.
- **Test Types**: Unit (pure seeding, identity args, reconcile dedupe), Integration (hook-seam `runAdd`/`launchAgent` with stubbed seams, record round-trip in `t.TempDir()`), no new E2E (manual tmux check in Success Criteria).
- **Mocking/Setup**: Existing var seam `paneRecordAppendHook` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:46)); one new var seam `setPaneIdentityHook` in [add.go](/home/maceo/Dev/ntm/internal/cli/add.go) mirroring it, behind the extracted `writeAddPaneIdentity` writer; pure helpers (`seedMaxIndices`, `paneIdentityArgs`) take literals so no live tmux is needed. `runAdd` is never driven from a unit test — its direct `tmux.GetPanes`/`SplitWindow`/`SetPaneTitle` calls ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:413)) have no seams and none are added. Robot parity drives the real `launchAgent` through three new named var seams in [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go) (`setPaneTitleHook`, `setPaneIdentityHook`, `sendKeysForAgentHook`) with `am == nil` — never by shelling to tmux.
- **Order**: Baseline precondition first, then B2 seeding helper (pure, unblocks B1/B4), B1 identity write, B3 dedupe+warning, B4 scale verification (no code, no new test), B5 robot parity, B6 best-effort regression pin last (existing test, already green).

## Behavior 1: Add writes tmux identity vars at creation

### Test Specification
**Given**: `runAdd` creates a pane for agent type `cc`, assigned index `num`, model/persona variant `agent.Model`
**When**: the pane is titled via `tmux.FormatPaneName` + `SetPaneTitle` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:506))
**Then**: `SetPaneIdentity(paneID, canonicalType, num, variant)` is invoked for that pane before launch (`SendKeys`), so `GetPanes` later resolves `Type`/`NTMIndex` from `@ntm_*` even if the title is later replaced by an Agent Mail name

**Edge Cases**: empty variant writes only `@ntm_type`/`@ntm_index` (existing `SetPaneIdentity` behavior, [session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:689)); persona adds pass the persona name (already in `agent.Model`); plugin/custom types canonicalize the same way `FormatPaneName` does; multiple panes in one `add` each get their own `(type, num)`.

### TDD Cycle

Feasibility note (verified): `runAdd` calls `tmux.GetPanes` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:413)), `tmux.SplitWindow` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:492)), and `tmux.SetPaneTitle` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:507)) directly — no stubbable seam exists, so a unit test that drives `runAdd` end-to-end is infeasible as sketched in the original plan. This behavior therefore splits into (a) a pure identity-args helper, (b) one extracted writer behind a single new var seam, and (c) the manual tmux check for the `runAdd` wiring. No `GetPanes`/`SplitWindow`/`SetPaneTitle`/`SendKeys` seams are added.

#### 🔴 Red: Write Failing Test
**File**: [add_test.go](/home/maceo/Dev/ntm/internal/cli/add_test.go)
```go
func TestPaneIdentityArgsForAdd(t *testing.T) {
    // table-driven, pure: (FlatAgent{Type, Model}, num) ->
    // want (canonicalType, num, variant); persona case: Model holds persona name
}
func TestWriteAddPaneIdentityUsesHook(t *testing.T) {
    // stub setPaneIdentityHook (new var seam) to capture (paneID, type, index, variant),
    // save/restore with t.Cleanup per TestRecordAddedPanes pattern
    // (spawn_state_test.go:361); call writeAddPaneIdentity("%7", agent, 2)
    // Assert: exactly one hook call, canonical type, index == num, variant == agent.Model
}
```

#### 🟢 Green: Minimal Implementation
**File**: [add.go](/home/maceo/Dev/ntm/internal/cli/add.go)
```go
// New seam beside paneRecordAppendHook (add.go:46):
var setPaneIdentityHook = tmux.SetPaneIdentity

// Pure: identity args for one added pane.
func paneIdentityArgs(agent FlatAgent, num int) (string, int, string) {
    return string(tmux.AgentType(agent.Type).Canonical()), num, agent.Model
}

// Thin writer so the seam call is testable without driving runAdd.
func writeAddPaneIdentity(paneID string, agent FlatAgent, num int) {
    typ, idx, variant := paneIdentityArgs(agent, num)
    setPaneIdentityHook(paneID, typ, idx, variant)
}
// In runAdd loop, immediately after SetPaneTitle succeeds (add.go:507),
// before command generation/launch:
writeAddPaneIdentity(paneID, agent, num)
```
Placement rationale: mirrors spawn's identity-before-launch order ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2284)); `SetPaneIdentity` returns nothing and swallows tmux errors ([session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:686)), so no error branch and no new failure mode for `add`.

#### 🔵 Refactor: Improve Code
Share the canonical-type expression with the `FormatPaneName` call so title and identity can never diverge; no helper extraction beyond `paneIdentityArgs`/`writeAddPaneIdentity` and the seam.

### Success Criteria
**Automated:**
- [ ] Red first: `go test -short -run 'TestPaneIdentityArgsForAdd|TestWriteAddPaneIdentityUsesHook' ./internal/cli/` fails before the helpers/seam exist
- [ ] Green: same command passes; `go test -short ./internal/cli/` passes

**Manual (this is the only coverage of the `runAdd` wiring itself — required, not optional):**
- [ ] `ntm add <session> --cc=1`, then `tmux show-options -p -t <pane> @ntm_type` / `@ntm_index` / `@ntm_variant` show the new pane's identity matching its title

---

## Behavior 2: Next-index seeding reads resolved identity fields, not titles alone

### Test Specification
**Given**: existing panes as `[]tmux.Pane` literals — (a) title `proj__cc_2` with `Type=cc, NTMIndex=2`; (b) title `RubyDog` with `Type=cc, NTMIndex=1` (identity-backed, Agent Mail titled); (c) title `proj__cc_3` with zero identity fields (legacy title-only); (d) user pane `Type=user, NTMIndex=0`; (e) title `custom-title` with no identity
**When**: seeding runs over the snapshot
**Then**: `maxIndices[cc] == 3` (max across identity fields AND legacy title parse); user/unresolved panes contribute nothing; the next `cc` index issued is 4; a session whose only `cc` pane is `RubyDog (cc,1)` issues 2, never 1

**Edge Cases**: identity `NTMIndex=0` with parseable title falls back to title; identity type needing canonicalization keys identically to the loop once the loop lookup is canonicalized (today the loop key at [add.go](/home/maceo/Dev/ntm/internal/cli/add.go:490) is raw `string(agent.Type)` — see Green); duplicate same-index panes keep the max once; empty pane list seeds empty.

### TDD Cycle

#### 🔴 Red: Write Failing Test
**File**: [add_test.go](/home/maceo/Dev/ntm/internal/cli/add_test.go) (table-driven, `t.Run` per case above)
```go
func TestSeedMaxIndicesFromPanes(t *testing.T) {
    // cases: identity-only (RubyDog), title-only legacy, mixed, user pane, empty
    // got := seedMaxIndices(tc.panes); assert reflect.DeepEqual(got, tc.want)
}
```
Also add a failing case to `TestPaneTitleTypeAndIndex` neighborhood only if the helper keeps using it — do not change that function's pinned behavior ([cli_helpers_extra_test.go](/home/maceo/Dev/ntm/internal/cli/cli_helpers_extra_test.go:126)).

#### 🟢 Green: Minimal Implementation
**File**: [add.go](/home/maceo/Dev/ntm/internal/cli/add.go:418)
```go
// Pure, testable replacement for the parseIndex closure (add.go:421):
func seedMaxIndices(panes []tmux.Pane) map[string]int {
    // per pane: candidate from (p.Type, p.NTMIndex) when NTMIndex > 0 and
    // type canonicalizes valid/non-user; union with paneTitleTypeAndIndex(p.Title);
    // keep max per canonical type string
}
```
`runAdd` calls `maxIndices := seedMaxIndices(panes)`. The pre-increment loop is **changed** to key canonically — today `agentTypeStr := string(agent.Type)` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:490)) is used raw for `maxIndices[agentTypeStr]++` ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:503)); Green introduces `seedKey := string(tmux.AgentType(agent.Type).Canonical())` for the `maxIndices` increment/lookup only (display/command uses of `agentTypeStr` are untouched), so seed keys and loop keys match. Inside `seedMaxIndices`, the identity candidate key is likewise `string(tmux.AgentType(p.Type).Canonical())` when valid and non-user.

#### 🔵 Refactor: Improve Code
Delete the now-dead `parseIndex` closure; keep `paneTitleTypeAndIndex` (still the legacy fallback and independently pinned).

### Success Criteria
**Automated:**
- [ ] Red first: `go test -short -run TestSeedMaxIndicesFromPanes ./internal/cli/` fails on `RubyDog` case (got 0/absent, want 1)
- [ ] Green + no regression: `go test -short ./internal/cli/` passes, including `TestPaneTitleTypeAndIndex`

**Manual:**
- [ ] In a session with an Agent Mail-titled `cc_1` pane, `ntm add --cc=1` titles the new pane `__cc_2`

---

## Behavior 3: Record dedupe-by-key with visible warning on live collision

### Test Specification
**Given**: two live panes sharing key `(window 0, cc, 1)` — first holds recorded id `sess-1`, second would lazily discover or copy the same id; stored record already contains the key (including the already-duplicated two-entry form persisted by the buggy save)
**When**: `reconcilePaneIDs` runs (via save/`Capture`) and the refreshed record is persisted via `upsertPaneRecordEntries`/`refreshPaneRecordAfterSave`
**Then**: saved states keep today's semantics (first in `(WindowIndex, Index)` order keeps `sess-1`, loser saves empty); the refreshed/persisted record contains exactly one entry for the key; `reconcilePaneIDs` returns one `paneKeyCollision` per colliding key and `Capture` writes one stderr warning per collision naming the key and both pane ids

**Edge Cases**: distinct keys never warn or drop; loser with different cwd and a **different lazily discovered id** still dedupes by key — its entry does not persist **and its assigned `SessionID`/`SessionProvider`/`SessionFile` in the returned states are cleared to empty** (the discovered id is discarded, not saved), so the loser saves empty and fresh-launches on resume exactly like the recorded-duplicate loser; never-regress preserved for the winner only (filled id never replaced by empty, [capture.go](/home/maceo/Dev/ntm/internal/session/capture.go:372)) — the never-regress copy must not resurrect the loser; non-resumable panes ignored as today ([storage.go](/home/maceo/Dev/ntm/internal/session/storage.go:589)).

### TDD Cycle

#### 🔴 Red: Write Failing Test
**File**: [session_test.go](/home/maceo/Dev/ntm/internal/session/session_test.go) beside `TestReconcilePaneRecord` (:1394)
```go
func TestReconcilePaneIDsDedupesCollidingKeys(t *testing.T) {
    // table-driven: colliding pair / already-duplicated stored record / distinct keys /
    // distinct-cwd lazy-fill pair discovering two different ids on one key
    // Act: states, refreshed, collisions := reconcilePaneIDs(panes, cwds, record, fakeDiscover)
    // Assert: exactly one refreshed entry per paneRecordKey (the sorted-first winner);
    // loser state SessionID/SessionProvider/SessionFile all == "" in every case,
    // including the distinct-cwd case; len(collisions) == 1 with the key and both pane ids;
    // distinct-keys case: collisions empty, nothing dropped or cleared
}
func TestUpsertPaneRecordEntriesDedupesByKey(t *testing.T) {
    // duplicate keys in one incoming list -> stored file holds one entry.
    // Winner rule: a filled SessionID beats an empty one regardless of order;
    // if both filled or both empty, the first in list order wins.
}
```
Warning assertion (pinned, not implementer's choice): the unit test asserts the **returned** `collisions` value from the pure `reconcilePaneIDs`; the stderr text itself is covered by the manual save check in Success Criteria. No stderr capture is added inside the pure function.

#### 🟢 Green: Minimal Implementation
**Files**: [capture.go](/home/maceo/Dev/ntm/internal/session/capture.go:358) (refreshed-list build), [storage.go](/home/maceo/Dev/ntm/internal/session/storage.go:646) (upsert guard)
```go
// Pinned mechanism (no alternative): reconcilePaneIDs stays pure — no stderr,
// no os import. New return value reports collisions:
//
//   type paneKeyCollision struct {
//       Key         paneRecordKey
//       WinnerPaneID string
//       LoserPaneID  string
//   }
//   func reconcilePaneIDs(...) ([]PaneState, []PaneRecordEntry, []paneKeyCollision)
//
// Update both call sites: Capture (capture.go:66) uses all three returns;
// mapPaneStates (capture.go:195) discards collisions (states, _, _ := ...).
// Update every existing 2-value test call site in session_test.go.
//
// reconcilePaneIDs: track seen paneRecordKey while building refreshed in
// (WindowIndex, Index) sorted order; first wins, later same-key panes append
// no refreshed entry, their assigned id/provider/file are cleared in the
// returned states, and one paneKeyCollision per colliding key is appended.
// The never-regress stored-id copy (capture.go:372) applies only to the
// winner / non-colliding panes, never to a deduped loser.
//
// Capture (capture.go:66) is the only emitter, exactly once per collision:
//   paneStates, refreshed, collisions := reconcilePaneIDs(...)
//   for _, c := range collisions {
//       fmt.Fprintf(os.Stderr, "warning: duplicate pane identity: key (window %d, type %s, index %d) shared by panes %s and %s; keeping %s, %s saves empty\n", ...)
//   }
// (capture.go already imports os; add fmt if absent.)
//
// upsertPaneRecordEntries (storage.go:646): dedupe incoming entries by key
// before merging — filled SessionID beats empty regardless of order; both
// filled or both empty -> first in list order wins — so no writer can
// persist duplicates again (defense in depth). The stored-record
// never-regress merge rule above it is unchanged.
```
One mechanism, tested once: the pure function returns the report, `Capture` emits it. No warning inside `reconcilePaneIDs`, no double warning. Because the warning fires per `Capture` call, it **repeats on every save while the live collision persists** until a pane is killed or retitled — intended, and the manual criterion below expects the repeat.

#### 🔵 Refactor: Improve Code
Reuse `paneRecordKeyOfPane`/`paneRecordKeyOfEntry` ([storage.go](/home/maceo/Dev/ntm/internal/session/storage.go:579)); do not introduce a second key type.

### Success Criteria
**Automated:**
- [ ] Red first: `go test -short -run 'TestReconcilePaneIDsDedupesCollidingKeys|TestUpsertPaneRecordEntriesDedupesByKey' ./internal/session/` fails (refreshed holds 2 entries per key)
- [ ] Green: same + `go test -short ./internal/session/` passes; `--json` save stdout still parses (warning is stderr-only)

**Manual:**
- [ ] Hand-craft the `RubyDog`/`__cc_1` collision, run `ntm sessions save`: exactly one warning on stderr naming the key and both pane ids, sidecar `pane-records/<session>.json` holds one entry for the key, and a **second save warns again the same way** (once per key per save while the collision persists) while still holding exactly one entry — warning repetition is the expected steady state until a colliding pane is killed or retitled, not a failure

---

## Behavior 4: Scale inherits the fix via `runAdd` (no scale-specific code)

### Test Specification
**Given**: session with an identity-backed, Agent Mail-titled `cc` pane at `(cc, 1)`
**When**: scale-up for `cc` executes (`runScale` → `runAdd`, [scale.go](/home/maceo/Dev/ntm/internal/cli/scale.go:310))
**Then**: the scaled pane is numbered 2 and receives identity vars exactly as in Behaviors 1–2; `scale.go` gains no seeding or identity logic of its own

**Edge Cases**: scale-down untouched (kill-only, stale entry dropped at next save as today); multi-type scale-up seeds each type independently.

### TDD Cycle

#### 🔴 Red: Write Failing Test
None — **no new test for this behavior** (this is the one consistent path; the earlier draft contradicted itself by demanding a delegation test while forbidding both the seam it needs and any `scale.go` diff). `runScale` has no injectable `runAdd` seam today and none is added. Delegation is already a live-code fact — scale-up constructs `AddOptions{Session, Agents}` and calls `runAdd` at [scale.go](/home/maceo/Dev/ntm/internal/cli/scale.go:306) — and numbering/identity are proven once by `TestSeedMaxIndicesFromPanes` (Behavior 2) and `TestPaneIdentityArgsForAdd`/`TestWriteAddPaneIdentityUsesHook` (Behavior 1). A scale-level test would only duplicate those or test a seam that exists solely for the test.

#### 🟢 Green: Minimal Implementation
**File**: none in [scale.go](/home/maceo/Dev/ntm/internal/cli/scale.go). Green is Behaviors 1–2 landing.

#### 🔵 Refactor: Improve Code
None. Any diff in `scale.go` for this behavior is a smell — call it out in review.

### Success Criteria
**Automated:**
- [ ] `go test -short -run 'TestScale|TestSeedMaxIndicesFromPanes|TestPaneIdentityArgsForAdd|TestWriteAddPaneIdentityUsesHook' ./internal/cli/` passes (existing scale tests plus the Behavior 1–2 tests; no new scale test)
- [ ] `git diff --stat` shows no change in `internal/cli/scale.go`

**Manual:**
- [ ] `ntm scale` up in the `RubyDog` session numbers/identifies identically to `ntm add`

---

## Behavior 5: Robot spawn identity parity (record write = explicit non-goal)

### Test Specification
**Given**: robot `launchAgent` ([spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:713)) with Agent Mail unavailable (`agentMailName == ""`), fallback title `<session>__cc_1`
**When**: the pane launches
**Then**: `SetPaneIdentity(pane.ID, canonicalType, num, "")` is called unconditionally — same as CLI spawn ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2284)) — so the pane keeps a stable `(type, index)` key even though its title is the fallback; with an Agent Mail name the behavior is unchanged (identity + retitle)

**Edge Cases**: `AGENT_NAME` prefixing unchanged (only when named, [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:754)); nil Agent Mail state still never fails spawn ([spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:784)).

### TDD Cycle

#### 🔴 Red: Write Failing Test
**File**: [spawn_test.go](/home/maceo/Dev/ntm/internal/robot/spawn_test.go) (beside [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go))

Feasibility note (verified): `launchAgent` calls `tmux.SetPaneTitle` directly at [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:737) and returns early on its error at `:738-741`, **before** the identity call at `:748`; it also calls `tmux.SetPaneIdentity` and `tmux.SendKeysForAgent` directly. None is stubbable today, so the seams below are named production changes, not test-only assumptions. `resolveAgentMailIdentity` needs no seam: with `am == nil` it returns `""` without side effects ([spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:841)), and `SanitizePaneCommand`/`BuildPaneCommand` are pure, so three seams suffice to drive the real `launchAgent`.

```go
func TestLaunchAgentSetsIdentityWithoutAgentMail(t *testing.T) {
    // Stub the three new seams (save/restore with t.Cleanup):
    //   setPaneTitleHook     -> nil error, records title
    //   setPaneIdentityHook  -> records (paneID, type, index, variant)
    //   sendKeysForAgentHook -> nil error
    // Act: launchAgent(pane, "sess", "claude", 1, t.TempDir(), "echo hi", nil) // am == nil => agentMailName == ""
    // Assert: exactly one identity call (canonical type, num 1, variant "");
    //         fallback title was written; agent.Error == ""
    // Second case: setPaneTitleHook returns error -> early return preserved:
    //         identity hook NOT called, agent.Error names the title failure.
}
```

#### 🟢 Green: Minimal Implementation
**File**: [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go)
```go
// New var seams (package level, mirroring paneRecordAppendHook in cli/add.go):
var setPaneTitleHook = tmux.SetPaneTitle
var setPaneIdentityHook = tmux.SetPaneIdentity
var sendKeysForAgentHook = tmux.SendKeysForAgent

// In launchAgent: route the existing calls through the seams —
//   tmux.SetPaneTitle(pane.ID, title)        (spawn.go:737) -> setPaneTitleHook(...)
//   tmux.SendKeysForAgent(...)               (spawn.go:774) -> sendKeysForAgentHook(...)
// Remove the if agentMailName != "" guard around SetPaneIdentity;
// keep the explanatory comment (spawn.go:743) and empty variant, via the seam:
setPaneIdentityHook(pane.ID, string(tmux.AgentType(agentType).Canonical()), num, "")
```
The title-error early return at [spawn.go](/home/maceo/Dev/ntm/internal/robot/spawn.go:738) is preserved exactly: identity is written only after a successful title write, now unconditionally with respect to Agent Mail.

#### 🔵 Refactor: Improve Code
Align the comment with CLI spawn's wording ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2275)); no record write added here.

### Success Criteria
**Automated:**
- [ ] Red first: `go test -short -run TestLaunchAgentSetsIdentityWithoutAgentMail ./internal/robot/` fails (no identity call when unnamed)
- [ ] Green: same + `go test -short ./internal/robot/` passes

**Manual:**
- [ ] `--robot-spawn` with Agent Mail disabled: `tmux show-options -p @ntm_type` set on spawned panes

**Parity assessment (recorded, not implemented)**: CLI spawn writes the roster via `WritePaneRecord` ([spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:530)); robot spawn writes none and has no hook. Adding it changes robot JSON/session lifecycle semantics and is deferred to a follow-up issue — link it in the implementing PR; do not fold it into this fix.

---

## Behavior 6: Best-effort preserved — identity/record failures never fail add

### Test Specification
**Given**: failure injected at (a) `paneRecordAppendHook` (existing pattern), (b) `GetPanes` re-read inside `recordAddedPanes`, (c) tmux identity write (inherently error-swallowing)
**When**: `add` (and by inheritance `scale` up) runs
**Then**: command succeeds, agents launch, JSON output (if any) still parses, warnings appear on stderr only — byte-for-byte the current never-fail contract ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:55))

**Edge Cases**: `SetPaneTitle` failure still fails `add` as today ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:507)) — explicitly not softened by this plan; record hook runs once per run, after the loop ([add.go](/home/maceo/Dev/ntm/internal/cli/add.go:771)).

### TDD Cycle

#### 🔴 Red: Write Failing Test
None — **no new test**. This contract is already pinned and already green by `TestRecordAddedPanes` at [spawn_state_test.go](/home/maceo/Dev/ntm/internal/cli/spawn_state_test.go:361), which covers exactly the cases the earlier draft proposed duplicating: hook failure warns on stderr and returns nil, failed pane listing warns and returns nil, unknown pane ids are skipped, and empty ids never list panes. Verified against the live test (`spawn_state_test.go:361-430`). Writing `TestRecordAddedPanesNeverFails` would test the same function the same way — do not add it.

#### 🟢 Green: Minimal Implementation
Expected no production change — `recordAddedPanes` already never returns non-nil and `SetPaneIdentity` cannot error. Green = Behaviors 1–3 not regressing this (no new `return err` on identity/record paths in `runAdd`). This behavior is a regression gate over the existing test, run last.

#### 🔵 Refactor: Improve Code
None. If Behaviors 1–3 introduced any propagated error, move it to the stderr warning there and re-run this gate.

### Success Criteria
**Automated:**
- [ ] `go test -short -run 'TestRecordAddedPanes' ./internal/cli/` passes (existing test at [spawn_state_test.go](/home/maceo/Dev/ntm/internal/cli/spawn_state_test.go:361), unmodified) — plus the Behavior 1 writer tests under failure-free hook stubbing
- [ ] Full gate: `go vet ./...` clean; `golangci-lint run` clean (CI parity)

**Manual:**
- [ ] `ntm add --json` with record storage made unwritable: stdout is valid JSON, warning on stderr, exit 0

---

## Integration & E2E Testing
- Integration: `go test -short ./internal/cli/ ./internal/session/ ./internal/robot/` together (shared `paneRecordKey` contract across all three).
- Full gate before PR: `make test` (`go test -v -short ./...`), `go vet ./...`, `golangci-lint run`.
- E2E (manual, real tmux): spawn → Agent Mail retitles a pane → `ntm add` same type → assert numbering, `show-options @ntm_*`, `ntm sessions save` sidecar holds one entry per key → re-save warns again once per colliding key (expected while the collision persists) and still holds one entry per key.

## References
- Research: [2026-10-22-11-29-add-identity-callers.md](/home/maceo/Dev/ntm/thoughts/searchable/shared/research/2026-10-22-11-29-add-identity-callers.md) — erratum noted, not edited: it cites `parsePaneFromParts` at `session.go:1741`; live code has it at [session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:1716). All line numbers in this plan were re-verified against the working tree on 2026-10-22.
- Prior plan: [2026-10-22-08-19-tdd-pane-id-record.md](/home/maceo/Dev/ntm/thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record.md)
- Patterns: [add.go](/home/maceo/Dev/ntm/internal/cli/add.go:46) hook var seam; [spawn.go](/home/maceo/Dev/ntm/internal/cli/spawn.go:2284) unconditional identity; [session.go](/home/maceo/Dev/ntm/internal/tmux/session.go:686) `SetPaneIdentity`

