# Loop 8: Hook migration

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Hook Migration**.

## Overview

Several repos register the old hook scripts in committed `.claude/settings.json` files. This loop moves them to the Go command. The `ntm agent-hook migrate` command previews by default, needs `--apply` to change anything, and never deletes scripts or commits. A short guide covers your local machine.

Covers deliverable 17. Needs Loop 3, for the finished command and identity lookup.

**Where the old scripts are registered:**
- On this VPS: only `/home/debian/Dev/mcp_agent_mail/.claude/settings.json`, found by searching `~/Dev` and `~/ntm_Dev`.
- On your local machine, per the research: `zero-knowledge-proofs`, `CodeWriter9.0`, `silmari-writer`, `mcp_agent_mail`, and ntm worktrees that inherit those committed files.

**Commands come in different forms.** The `mcp_agent_mail` file uses absolute paths under `/home/maceo`, to repo-local copies, so those hooks can't work on this VPS as committed. Other repos use `~/.claude/hooks/...`. So the command matches on the script's file name, in any path. Migrating `mcp_agent_mail` also fixes that file's Stop hook on this VPS.

**What each script becomes, from the scope's assumption:**

| Old script | Event | Migration |
|---|---|---|
| `check_critical_on_stop.sh` | Stop | Replaced by the Go command |
| `check_mail_on_close.sh` | PostToolUse | Removed, since turn-end now covers any new mail |
| `ensure_window_id.sh` | SessionStart | Left alone, because the breadcrumb lookup depends on it |

---

## Sprint 1: Preview

### Testable function

"Run `ntm agent-hook migrate` on this VPS, and see `mcp_agent_mail` listed with its planned change and nothing written."

### Acceptance criteria

- [ ] With no folders named, it scans `~/Dev` and `~/ntm_Dev`. Named folders replace that default.
- [ ] Every settings file registering an old script is listed, whatever path form its command uses.
- [ ] Each match shows its planned change: what's replaced, what's removed, and what's left alone.
- [ ] Nothing is written. File modification times and `git status` don't change.
- [ ] A file that can't be parsed is reported as failed, the scan continues, and the exit code is non-zero.

After Sprint 1's human test (2026-11-10): only files whose text names an old script are parsed strictly; folders the scan can't read are skipped with one summary line; and a worktree's shared copy counts under its main repo in the totals.

### Requirements

**Data:** Reads `.claude/settings.json` and `.claude/settings.local.json`. A local settings file can register the old scripts too, and it isn't committed. **Assumption:** it's handled the same way, and the report marks that file as having nothing to commit.

**API:**
- New subcommand `ntm agent-hook migrate [folders...] [--apply]`, with `--json` following the repo's patterns.
- The scan doesn't descend into `.git`, `node_modules`, `.venv` or similar folders. **Assumption:** it looks for `.claude` folders at any depth below the named folders.
- A worktree that inherits a committed file is listed separately, and marked as sharing that file with its main repo, so you don't commit it twice.
- The planned Go command is shown in full, including the check that the ntm binary is under your home folder.

**UI:** A readable report, grouped by repo, ending in totals.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Three path forms | `~/.claude/hooks/x.sh`, absolute `/home/maceo/...`, `$CLAUDE_PROJECT_DIR/...` | All matched |
| Similar name | `my_check_critical_on_stop.sh.bak` | Not matched |
| No matches | Folder with no old scripts | "Nothing to migrate", exit 0 |
| Not writing | Preview with a read-only tree | Works, and nothing changes |
| Invalid JSON | Corrupt settings file | Reported, the others continue, non-zero exit |
| Skipped folders | Match inside `node_modules` | Not listed |
| Worktree | Worktree of a matched repo | Marked as shared |
| Binary outside home | ntm installed at `/usr/local/bin` | Error for each repo, per the scope |

### Human test script

1. Build the binary, and put it under your home folder: `go build -o ~/.local/bin/ntm-loop8 ./cmd/ntm`.
2. Note the current state: `git -C ~/Dev/mcp_agent_mail status --short .claude`.
3. Run `ntm-loop8 agent-hook migrate`.
4. Pass: `mcp_agent_mail` is listed, showing the Stop hook replaced, `check_mail_on_close.sh` removed, and `ensure_window_id.sh` left alone.
5. Run the same `git status` command again.
6. Pass: its output hasn't changed.
7. Run `ntm-loop8 agent-hook migrate /tmp`.
8. Pass: nothing is listed from `~/Dev`, showing that named folders replace the default.

---

## Sprint 2: Apply

### Testable function

"Run it with `--apply`, see the file change with other entries kept, and run it again to see no change."

### Acceptance criteria

- [ ] `--apply` changes only the old script entries in each file, keeping every other hook, matcher and setting, and the file's key order where possible.
- [ ] The new Stop entry uses a `~/`-relative ntm path, not an absolute one, because committed files are shared across machines.
- [ ] If the binary isn't under the home folder, that repo reports an error and isn't changed.
- [ ] A second run reports no change for migrated files, and writes nothing.
- [ ] The report lists what changed and what's left to commit. The command never commits, and never deletes a script.

### Requirements

**Data:** The **HookRegistration** contract, in its committed form: no `--project`, so the command finds the project from `MCP_AGENT_MAIL_PROJECT` or the current folder (Loop 3, Sprint 2).

**API:**
- Reuses the Loop 3 hook writer's atomic write and merge, plus a removal function.
- Removing an entry can leave an empty hook group, so the group is removed too. A group holding other hooks stays.
- The file is written with the same indentation it had. **Assumption:** the diff should be just the changed lines, for easy review.

**UI:** A report ending with the exact `git add` and `git commit` commands per repo, for you to run.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Replace | Old Stop entry | One new entry |
| Stop group with others | Old entry next to another Stop hook | Other hook kept |
| PostToolUse group | Old entry in a shared group | Only that entry removed |
| Empty group | Old entry alone in a group | Group removed |
| SessionStart | `ensure_window_id.sh` | Unchanged |
| Re-run | Apply twice | Second run: no change, and no write |
| Already has new entry | New entry and old entry together | One new entry, old removed |
| Minimal diff | Well-formatted file | Only changed lines in `git diff` |
| Scripts kept | After apply | Script files still exist |
| Partial failure | One read-only file among three | Others migrated, one failure, non-zero exit |

### Human test script

1. Run `ntm-loop8 agent-hook migrate ~/Dev --apply`.
2. Run `git -C ~/Dev/mcp_agent_mail diff .claude/settings.json`.
3. Pass: the Stop command is now `~/.local/bin/ntm-loop8 agent-hook stop`. The `check_mail_on_close.sh` entry is gone. The `file_reservations` hooks and `ensure_window_id.sh` are untouched.
4. Run `ls ~/Dev/mcp_agent_mail/.claude/hooks/`.
5. Pass: the script files are all still there.
6. Run `ntm-loop8 agent-hook migrate ~/Dev --apply` again.
7. Pass: it reports no change, and `git diff --stat` for that file is the same as before.
8. Run `git -C ~/Dev/mcp_agent_mail checkout -- .claude/settings.json` only if you don't want to keep this test change. Otherwise, commit it in that repo yourself.

---

## Sprint 3: Guide, tried on your local machine

### Testable function

"Follow the guide on your local machine for one repo, and see a plain session there still get mail reminders."

### Acceptance criteria

- [ ] A guide exists at `docs/agent-hook-migration.md`, and is complete enough to follow without this plan.
- [ ] It gives the order: install the new ntm, preview, apply, check the diff, commit per repo, then test one repo.
- [ ] It covers rollback (`git revert` of that commit), and says the old scripts stay on disk.
- [ ] After you follow it for one repo on your local machine, a plain Claude session in that repo, with a breadcrumb, gets a mail reminder at turn end.
- [ ] Any gap you hit while following it is fixed in the guide.

### Requirements

**Data:** None.

**API:** None, unless the local run finds a defect. The local machine's ntm must be at or past this loop's commit, and its binary must live under the home folder.

**UI:** A short document in `docs/`, in plain language, with one example output for preview and apply.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Commands are real | Run each command in the guide on this VPS | Each works as written |
| Local preview | Run on your local machine | Lists the repos the research found |
| Reminder after migration | Plain session in a migrated repo | Told at turn end |
| Rollback | Follow the rollback steps in a scratch clone | Old registration restored |

### Human test script

Run this on your local machine.

1. Pull the ntm repo and install the new binary, following the guide.
2. Run the guide's preview command.
3. Pass: `zero-knowledge-proofs`, `CodeWriter9.0`, `silmari-writer` and `mcp_agent_mail` are listed.
4. Apply it to one repo only: the guide's command with `~/Dev/zero-knowledge-proofs`.
5. Check the diff and commit it, as the guide says.
6. Start a plain Claude session in that repo, not through ntm, and register with Agent Mail.
7. Give it slow work. While it works, send it a message through Agent Mail.
8. Pass: when the turn ends, it's told about the message.
9. Note any step where the guide was wrong or unclear, and bring those back to be fixed.
