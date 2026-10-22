# Add Pane Identity — Intended `bd` Issue Set (Fallback)

> **Status: NOT CREATED — `bd` writes blocked.** This file is the exact intended
> issue set from the TDD plan. Create it verbatim once the schema blocker below
> is resolved by the single designated migrator. Do not edit the titles,
> dependencies, or acceptance text when transcribing; capture the real ids
> returned by `bd create` and substitute them for the symbolic ids
> (`EPIC`, `B1`–`B6`) used here.

Source plan: `thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md`
Source research: `thoughts/searchable/shared/research/2026-10-22-11-29-add-identity-callers.md`

## Blocker (observed 2026-10-22)

- `bd list --status=open` and all `bd search` calls succeed read-only but print:
  `Warning: refusing to auto-apply 4 pending schema migrations to a remote-backed database (v49 -> v53): migrating clones independently forks the schema (#4259)` and `Writes are blocked until the schema is reconciled.`
- One write was attempted to confirm (epic create, probe description) and was
  refused with the same v49 -> v53 message. No issue was created.
- Per instructions: **no migration was run, nothing was forced**
  (`BD_ALLOW_REMOTE_MIGRATE=1 bd migrate`, `bd bootstrap`, and `--force` were
  all deliberately NOT used).
- Resolution (owner: the single designated migrator only, on one machine):
  `BD_ALLOW_REMOTE_MIGRATE=1 bd migrate && bd dolt push`; every other clone
  then runs `bd bootstrap`. After that, re-run the creation commands in
  §"Creation commands" below.

## Duplicate check (done before filing)

- `bd list --status=open` — 56 open issues, none about pane identity.
- `bd search "pane identity"`, `bd search "pane record"`,
  `bd search "add identity"`, `bd search "ntm_index"` — all: no matches.
- Conclusion: no existing issue matches this plan; create fresh, do not link.

## Worktree isolation guidance (also in the epic description)

Parallel multi-agent implementation in git worktrees:

- One agent per behavior issue (`B1`–`B6`); never two agents on one issue.
- Each agent works in its own worktree branched from `main`, named for the
  issue, e.g. `git worktree add ../ntm-<id> -b <id>-<slug> main`
  (`bd` ids replace `<id>` once created).
- Respect the dependency graph in §"Dependency graph": an agent only starts a
  blocked issue after its blockers have landed on `main` (rebase the worktree
  onto latest `main` before starting).
- TDD per the source plan: write the failing test first and observe Red,
  then minimal Green, then Refactor. Gate per behavior is the `go test`
  command in its Success Criteria; full gate before PR is
  `make test` (`go test -v -short ./...`), `go vet ./...`, `golangci-lint run`.
- Same-file caution: `B1` and `B2` both touch `internal/cli/add.go`, and `B3`
  touches `internal/session/{capture,storage}.go`; the blocking edges below
  already serialize the `add.go` pair. Rebase before opening a PR; do not
  merge another agent's worktree branch directly.

## Dependency graph

Blocking edges (`X blocks Y` = Y may not start until X is landed on `main`).
`bd dep add <blocked> <blocker>` form given in §"Creation commands".

| Blocker | Blocked | Why |
|---|---|---|
| B2 (seeding) | B1 (identity write) | Plan order: pure seeding helper first; serializes the two `internal/cli/add.go` edits and gives B1 canonical seed keys to share |
| B2 (seeding) | B3 (dedupe) | Index seeding must land before dedupe (per filing instruction) |
| B1 (identity write) | B3 (dedupe) | Identity write must land before dedupe (per filing instruction); dedupe defends the key the write creates |
| B1 (identity write) | B4 (scale pin) | Scale inherits B1's identity write via `runAdd`; the pin test asserts it |
| B2 (seeding) | B4 (scale pin) | Scale inherits B2's seeding via `runAdd`; the pin test asserts numbering |
| B1 (identity write) | B6 (best-effort sweep) | Sweep verifies B1 introduced no propagated error |
| B2 (seeding) | B6 (best-effort sweep) | Sweep is last (plan order) |
| B3 (dedupe) | B6 (best-effort sweep) | Sweep verifies B3's warning path stays stderr-only and never fails save |
| B4 (scale pin) | B6 (best-effort sweep) | Sweep covers `scale` up by inheritance, so the pin must exist first |
| EPIC | — | Parent of B1–B6 (parent-child, not a blocking edge); close EPIC when all six land |

`B5` (robot parity) has **no blockers**: it touches only
`internal/robot/spawn.go`, is independent of the `add.go` chain, and should run
in parallel from day one.

Critical path: `B2 -> B1 -> B3 -> B6` (B4 hangs off B1+B2, before B6).

## Issue set

### EPIC — Add pane identity: `ntm add` writes `@ntm_type`/`@ntm_index`, seeds from resolved identity, dedupes colliding pane records

- **Symbolic id:** `EPIC`
- **Type:** epic
- **Priority:** 1 (P1)
- **Description:**

```markdown
Implement the TDD plan at
thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(source research:
thoughts/searchable/shared/research/2026-10-22-11-29-add-identity-callers.md).

Problem: `runAdd` titles each new pane `<session>__<type>_<index>` but never
writes the tmux `@ntm_type`/`@ntm_index` identity vars, and computes the next
index by re-parsing pane titles alone. A pane titled with an Agent Mail name
(e.g. `RubyDog`) is invisible to that seeding, so `add` re-issues an in-use
(type, index), the pane-record key (WindowIndex, AgentType, NTMIndex)
collides, and save-time reconcile persists duplicate keys/ids on every save.

Observable end state (plan §Desired End State / §Observable Behaviors):
- Add writes identity: a pane titled `<session>__cc_2` carries
  @ntm_type/@ntm_index/@ntm_variant = cc/2/model-or-persona.
- Add never re-issues a live index: existing `RubyDog` (cc,1) + `add --cc=1`
  numbers the new pane 2.
- Save dedupes colliding keys with a stderr warning; exactly one record
  entry per key persists.
- Scale up inherits all of the above via runAdd with no scale-specific code.
- Robot-spawned panes carry identity vars with or without Agent Mail.
- Identity/record failures never fail add/scale/spawn/save (stderr only).

Children: B1 identity write, B2 seeding, B3 dedupe+warning, B4 scale pin,
B5 robot parity, B6 best-effort sweep. Explicit non-goal (deferred follow-up,
do not fold in): robot pane-record writing. Also out of scope: restore/resume,
checkpoint restore, adopt, controller, create, ensemble/swarm,
context-rotation identity; record key shape/schema/StateVersion; new CLI
flags or JSON fields; per-pane GetPanes re-read in the add loop.

WORKTREE ISOLATION (multi-agent): one agent per behavior issue, each in its
own git worktree from main (git worktree add ../ntm-<id> -b <id>-<slug> main).
Start a blocked issue only after its blockers land on main; rebase onto
latest main first. TDD Red->Green->Refactor per the plan; full gate before
PR: make test, go vet ./..., golangci-lint run. Close this epic when all
six children are landed.
```

### B1 — Add writes tmux identity vars at creation (`SetPaneIdentity` in `runAdd`)

- **Symbolic id:** `B1`
- **Type:** task
- **Priority:** 1 (P1)
- **Blocked by:** B2
- **Blocks:** B3, B4, B6
- **Description:**

```markdown
Plan: thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(Behavior 1)

Given: `runAdd` creates a pane for agent type `cc`, assigned index `num`,
model/persona variant `agent.Model`.
When: the pane is titled via `tmux.FormatPaneName` + `SetPaneTitle`
(internal/cli/add.go:506).
Then: `SetPaneIdentity(paneID, canonicalType, num, variant)` is invoked for
that pane before launch (SendKeys), so GetPanes later resolves
Type/NTMIndex from @ntm_* even if the title is later replaced by an Agent
Mail name.

Edge cases: empty variant writes only @ntm_type/@ntm_index (existing
SetPaneIdentity behavior, internal/tmux/session.go:689); persona adds pass
the persona name (already in agent.Model); plugin/custom types canonicalize
the same way FormatPaneName does; multiple panes in one `add` each get
their own (type, num).

Implementation sketch (plan §Behavior 1 Green): new var seam
`setPaneIdentityHook = tmux.SetPaneIdentity` beside paneRecordAppendHook
(internal/cli/add.go:46); call it immediately after SetPaneTitle succeeds,
with string(tmux.AgentType(agentTypeStr).Canonical()); share the
canonical-type expression with the FormatPaneName call.

Test file targets:
- internal/cli/add_test.go — TestRunAddSetsPaneIdentity (table-driven;
  stub setPaneIdentityHook and paneRecordAppendHook with t.Cleanup restore,
  pattern TestRecordAddedPanes at internal/cli/spawn_state_test.go:361)

Success criteria:
- Red first: go test -short -run TestRunAddSetsPaneIdentity ./internal/cli/
  fails before the seam/call exists.
- Green: same passes; go test -short ./internal/cli/ passes.
- Manual: ntm add <session> --cc=1, then tmux show-options -p -t <pane>
  @ntm_type / @ntm_index show the new pane's identity.
```

### B2 — Next-index seeding reads resolved identity fields, not titles alone

- **Symbolic id:** `B2`
- **Type:** task
- **Priority:** 1 (P1)
- **Blocked by:** — (start first)
- **Blocks:** B1, B3, B4, B6
- **Description:**

```markdown
Plan: thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(Behavior 2)

Given: existing panes as []tmux.Pane — (a) title proj__cc_2 with
Type=cc, NTMIndex=2; (b) title RubyDog with Type=cc, NTMIndex=1
(identity-backed, Agent Mail titled); (c) title proj__cc_3 with zero
identity fields (legacy title-only); (d) user pane Type=user, NTMIndex=0;
(e) title custom-title with no identity.
When: seeding runs over the snapshot.
Then: maxIndices[cc] == 3 (max across identity fields AND legacy title
parse); user/unresolved panes contribute nothing; the next cc index issued
is 4; a session whose only cc pane is RubyDog (cc,1) issues 2, never 1.

Edge cases: identity NTMIndex=0 with parseable title falls back to title;
identity type needing canonicalization keys identically to the loop's
string(agent.Type); duplicate same-index panes keep the max once; empty
pane list seeds empty.

Implementation sketch (plan §Behavior 2 Green): pure helper
seedMaxIndices(panes []tmux.Pane) map[string]int replacing the parseIndex
closure (internal/cli/add.go:421); per pane take (p.Type, p.NTMIndex) when
NTMIndex > 0 and type canonicalizes valid/non-user, union with
paneTitleTypeAndIndex(p.Title), keep max per canonical type. Delete the
dead parseIndex closure; keep paneTitleTypeAndIndex (pinned separately).

Test file targets:
- internal/cli/add_test.go — TestSeedMaxIndicesFromPanes (table-driven,
  t.Run per case: identity-only RubyDog, title-only legacy, mixed, user
  pane, empty)
- internal/cli/cli_helpers_extra_test.go — TestPaneTitleTypeAndIndex
  behavior unchanged (regression pin, :126)

Success criteria:
- Red first: go test -short -run TestSeedMaxIndicesFromPanes
  ./internal/cli/ fails on the RubyDog case (got 0/absent, want 1).
- Green + no regression: go test -short ./internal/cli/ passes, including
  TestPaneTitleTypeAndIndex.
- Manual: in a session with an Agent Mail-titled cc_1 pane,
  ntm add --cc=1 titles the new pane __cc_2.
```

### B3 — Record dedupe-by-key with visible warning on live collision

- **Symbolic id:** `B3`
- **Type:** task
- **Priority:** 1 (P1)
- **Blocked by:** B1, B2
- **Blocks:** B6
- **Description:**

```markdown
Plan: thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(Behavior 3)

Given: two live panes sharing record key (window 0, cc, 1) — first holds
recorded id sess-1, second would lazily discover or copy the same id; the
stored record already contains the key (including the already-duplicated
two-entry form persisted by the buggy save).
When: reconcilePaneIDs runs (via save/Capture) and the refreshed record is
persisted via upsertPaneRecordEntries/refreshPaneRecordAfterSave.
Then: saved states keep today's semantics (first in (WindowIndex, Index)
order keeps sess-1, loser saves empty); the refreshed/persisted record
contains exactly one entry for the key; a warning naming the key and both
pane ids is written to stderr.

Edge cases: distinct keys never warn or drop; loser with different
cwd/discovered id still dedupes by key (its entry does not persist);
never-regress preserved for the winner (filled id never replaced by empty,
internal/session/capture.go:372); non-resumable panes ignored as today
(internal/session/storage.go:589).

Implementation sketch (plan §Behavior 3 Green): in reconcilePaneIDs track
seen paneRecordKey while building the refreshed list (first in sorted
order wins, later same-key panes append no entry); surface the collision
once — returned report consumed by Capture, or stderr write — as
fmt.Fprintf(os.Stderr, "warning: duplicate pane identity ..."); also dedupe
incoming entries by key (first wins) in upsertPaneRecordEntries
(internal/session/storage.go:646) as defense in depth. Reuse
paneRecordKeyOfPane/paneRecordKeyOfEntry (storage.go:579); one warning
mechanism, tested once, no double warning. Warning is stderr-only so
--json stdout stays parseable.

Test file targets:
- internal/session/session_test.go — TestReconcilePaneIDsDedupesCollidingKeys
  (beside TestReconcilePaneRecord :1394; colliding pair /
  already-duplicated stored record / distinct keys; assert exactly one
  refreshed entry per paneRecordKey, loser SessionID == "", and the
  warning text/key is observed)
- internal/session/session_test.go — TestUpsertPaneRecordEntriesDedupesByKey
  (duplicate keys in one incoming list -> stored file holds one entry,
  first wins)

Success criteria:
- Red first: go test -short -run
  'TestReconcilePaneIDsDedupesCollidingKeys|TestUpsertPaneRecordEntriesDedupesByKey'
  ./internal/session/ fails (refreshed holds 2 entries per key).
- Green: same + go test -short ./internal/session/ passes; --json save
  stdout still parses.
- Manual: hand-craft the RubyDog/__cc_1 collision, run ntm sessions save:
  warning on stderr, sidecar pane-records/<session>.json holds one entry
  for the key, second save warns-or-clean without re-duplicating.
```

### B4 — Scale inherits the fix via `runAdd` (no scale-specific code)

- **Symbolic id:** `B4`
- **Type:** task
- **Priority:** 2 (P2)
- **Blocked by:** B1, B2
- **Blocks:** B6
- **Description:**

```markdown
Plan: thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(Behavior 4)

Given: session with an identity-backed, Agent Mail-titled cc pane at
(cc, 1).
When: scale-up for cc executes (runScale -> runAdd,
internal/cli/scale.go:310).
Then: the scaled pane is numbered 2 and receives identity vars exactly as
in Behaviors 1-2; scale.go gains no seeding or identity logic of its own.

Edge cases: scale-down untouched (kill-only, stale entry dropped at next
save as today); multi-type scale-up seeds each type independently.

Implementation: none expected in internal/cli/scale.go — Green is B1+B2
landing. Any functional diff in scale.go for this behavior is a smell;
call it out in review. If runScale has no injectable runAdd seam today,
pin at the seedMaxIndices + delegation level; do not add production
indirection solely for this test without noting it as the refactor.

Test file targets:
- internal/cli/scale_test.go — TestScaleUpDelegatesToRunAddIdentity
  (assert scale-up constructs AddOptions{Session, Agents} and calls runAdd;
  numbering/identity proven by TestSeedMaxIndicesFromPanes +
  TestRunAddSetsPaneIdentity — no duplicate scale-level index math)

Success criteria:
- go test -short -run 'TestScale|TestSeedMaxIndices|TestRunAddSetsPaneIdentity'
  ./internal/cli/ passes.
- git diff --stat shows no functional change in internal/cli/scale.go.
- Manual: ntm scale up in the RubyDog session numbers/identifies
  identically to ntm add.
```

### B5 — Robot spawn identity parity (record write = explicit non-goal)

- **Symbolic id:** `B5`
- **Type:** task
- **Priority:** 2 (P2)
- **Blocked by:** — (independent; run in parallel)
- **Blocks:** —
- **Description:**

```markdown
Plan: thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(Behavior 5)

Given: robot launchAgent (internal/robot/spawn.go:713) with Agent Mail
unavailable (agentMailName == ""), fallback title <session>__cc_1.
When: the pane launches.
Then: SetPaneIdentity(pane.ID, canonicalType, num, "") is called
unconditionally — same as CLI spawn
(internal/cli/spawn.go:2284) — so the pane keeps a stable (type, index)
key even though its title is the fallback; with an Agent Mail name the
behavior is unchanged (identity + retitle).

Edge cases: AGENT_NAME prefixing unchanged (only when named,
internal/robot/spawn.go:754); nil Agent Mail state still never fails spawn
(internal/robot/spawn.go:784).

Implementation sketch (plan §Behavior 5 Green): remove the
`if agentMailName != ""` guard around SetPaneIdentity at
internal/robot/spawn.go:747, keeping the explanatory comment (:743) and
empty variant; align the comment with CLI spawn's wording
(internal/cli/spawn.go:2275).

Explicit non-goal: robot pane-record writing. CLI spawn writes the roster
via WritePaneRecord (internal/cli/spawn.go:530); robot spawn writes none
and has no hook. Defer to a follow-up issue and link it in the PR; do not
fold it into this fix.

Test file targets:
- internal/robot/spawn_test.go (robot spawn tests beside
  internal/robot/spawn.go) — TestLaunchAgentSetsIdentityWithoutAgentMail
  (same var-seam treatment as B1 if launchAgent's tmux calls are not
  already stubbable; assert the identity call occurs with
  agentMailName == "" and still occurs exactly once when named)

Success criteria:
- Red first: go test -short -run TestLaunchAgentSetsIdentityWithoutAgentMail
  ./internal/robot/ fails (no identity call when unnamed).
- Green: same + go test -short ./internal/robot/ passes.
- Manual: --robot-spawn with Agent Mail disabled:
  tmux show-options -p @ntm_type set on spawned panes.
```

### B6 — Best-effort preserved: identity/record failures never fail `add`

- **Symbolic id:** `B6`
- **Type:** task
- **Priority:** 2 (P2)
- **Blocked by:** B1, B2, B3, B4
- **Blocks:** — (last)
- **Description:**

```markdown
Plan: thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md
(Behavior 6)

Given: failure injected at (a) paneRecordAppendHook (existing pattern),
(b) GetPanes re-read inside recordAddedPanes, (c) tmux identity write
(inherently error-swallowing).
When: add (and by inheritance scale up) runs.
Then: command succeeds, agents launch, JSON output (if any) still parses,
warnings appear on stderr only — byte-for-byte the current never-fail
contract (internal/cli/add.go:55).

Edge cases: SetPaneTitle failure still fails add as today
(internal/cli/add.go:507) — explicitly not softened by this plan; record
hook runs once per run, after the loop (internal/cli/add.go:771).

Implementation: expected no production change — recordAddedPanes already
never returns non-nil and SetPaneIdentity cannot error. Green = B1-B3 did
not regress this (no new `return err` on identity/record paths in runAdd);
if any propagated error crept in, move it to the stderr warning here.

Test file targets:
- internal/cli/spawn_state_test.go — TestRecordAddedPanesNeverFails
  (extending TestRecordAddedPanes :361 style; table-driven hook error /
  getPanes error / unknown pane id -> recordAddedPanes returns nil, hook
  call counts as expected)

Success criteria:
- go test -short -run 'TestRecordAddedPanes|TestRunAddSetsPaneIdentity'
  ./internal/cli/ passes with failure injection.
- Full gate: go vet ./... clean; golangci-lint run clean (CI parity);
  together with the epic gate make test.
- Manual: ntm add --json with record storage made unwritable: stdout is
  valid JSON, warning on stderr, exit 0.
```

## Creation commands (run verbatim once the blocker clears)

Substitute the real ids printed by each `bd create` for `EPIC`, `B1`–`B6`
in the `--parent` and `bd dep add` lines. Priorities follow `bd create`
convention (`1` = P1, `2` = P2). Types: epic for `EPIC`, `task` for B1–B6.

```bash
# EPIC (capture id as EPIC)
bd create "EPIC: Add pane identity (ntm add writes @ntm_type/@ntm_index, seeds from resolved identity, dedupes colliding pane records)" \
  --type epic --priority 1 --description "<EPIC description above>"

# Children (capture ids as B1..B6; --parent sets the epic hierarchy)
bd create "B2: Next-index seeding reads resolved identity fields, not titles alone" \
  --type task --priority 1 --parent EPIC --description "<B2 description above>"
bd create "B1: Add writes tmux identity vars at creation (SetPaneIdentity in runAdd)" \
  --type task --priority 1 --parent EPIC --description "<B1 description above>"
bd create "B3: Record dedupe-by-key with visible warning on live collision" \
  --type task --priority 1 --parent EPIC --description "<B3 description above>"
bd create "B4: Scale inherits the fix via runAdd (no scale-specific code)" \
  --type task --priority 2 --parent EPIC --description "<B4 description above>"
bd create "B5: Robot spawn identity parity (record write = explicit non-goal)" \
  --type task --priority 2 --parent EPIC --description "<B5 description above>"
bd create "B6: Best-effort preserved — identity/record failures never fail add" \
  --type task --priority 2 --parent EPIC --description "<B6 description above>"

# Blocking edges: bd dep add <blocked> <blocker>
bd dep add B1 B2
bd dep add B3 B2
bd dep add B3 B1
bd dep add B4 B1
bd dep add B4 B2
bd dep add B6 B1
bd dep add B6 B2
bd dep add B6 B3
bd dep add B6 B4
# B5: no deps (parallel from day one). Verify: bd dep tree EPIC / bd ready
```

Note: `--parent` creates the parent-child relation; it is not a blocking
edge, so the nine `bd dep add` lines above are all still required.
