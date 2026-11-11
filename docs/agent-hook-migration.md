# Moving Agent Mail hooks to `ntm agent-hook stop`

Older projects tell Claude Code to run Agent Mail's shell scripts. ntm now does the same job itself: at the end of each turn, `ntm agent-hook stop` tells the session about new mail. This guide moves each project's `.claude/settings.json` over to it, one repo at a time.

| Old hook script | Where it's registered | What the migration does |
|---|---|---|
| `check_critical_on_stop.sh` | Stop | Replaced by `~/<path to ntm> agent-hook stop`, with a 5 second timeout |
| `check_mail_on_close.sh` | PostToolUse (Bash) | Removed. The turn-end reminder covers new mail |
| `ensure_window_id.sh` | SessionStart | Left alone. ntm needs it to find a session's name inside tmux |

What stays the same:
- Every other hook, matcher and setting in the file. Only the changed lines are edited, so the diff is small.
- The old scripts stay on disk. The migration never deletes a script, so a rollback works straight away.
- Your git history. ntm never commits. You check each diff and commit it yourself.

Run `ntm agent-hook migrate --help` to see all the options.

## Install the new ntm

The new Stop command is written into committed files, and those are shared by every machine that checks the repo out. So it names ntm from your home folder (`~/.local/bin/ntm`), not by an absolute path like `/home/you/...`. The migration refuses to run if ntm isn't under your home folder.

From your ntm checkout:

```bash
git pull
go build -o ~/.local/bin/ntm ./cmd/ntm
```

Install it at the same place (`~/.local/bin/ntm`) on every machine that uses these repos.

You should see: `ntm agent-hook migrate --help` prints the migrate help.

## Preview

The preview shows every change and writes nothing. With no folder named, it scans `~/Dev` and `~/ntm_Dev`. You can name folders or single repos instead:

```bash
ntm agent-hook migrate
ntm agent-hook migrate ~/Dev/zero-knowledge-proofs
```

You should see one block per repo. Each file shows what you commit and where, then one line per change: `replace`, `remove`, or `keep`.

- **A worktree's copy** says "shared with <main repo>; commit once there". A linked worktree checks out the main repo's committed file, so you migrate and commit it once, in the main repo.
- **A `settings.local.json` file** says "nothing to commit (local file)". It's yours alone and isn't in git.
- **Folders ntm can't read** are skipped, with one line at the end saying how many.
- **Failures** (a file that can't be parsed, or ntm outside your home folder) are listed under "Failed". The rest of the report still shows, and the command exits with code 1.

### Example preview

```text
/home/you/Dev/zero-knowledge-proofs
  .claude/settings.json (commit in /home/you/Dev/zero-knowledge-proofs)
    keep SessionStart: /home/you/Dev/zero-knowledge-proofs/.claude/hooks/ensure_window_id.sh (needed for the breadcrumb lookup)
    remove PostToolUse (Bash): /home/you/Dev/zero-knowledge-proofs/.claude/hooks/check_mail_on_close.sh (and its now empty group)
    replace Stop: /home/you/Dev/zero-knowledge-proofs/.claude/hooks/check_critical_on_stop.sh
      with ~/.local/bin/ntm agent-hook stop

Preview only: 1 file would change in 1 repo. Run with --apply to write them.
```

## Apply

Start with one repo, so you can check the result before doing the rest:

```bash
ntm agent-hook migrate --apply ~/Dev/zero-knowledge-proofs
```

When that repo looks right, apply the rest:

```bash
ntm agent-hook migrate --apply
```

`--apply` writes each listed file in place, keeping its permissions. It does three things it won't change:

- It skips a worktree's shared copy, saying "skipped: migrate and commit <main repo>, then update this worktree".
- It never commits, and never deletes a script.
- If one file can't be written, that file is left as it was, the others are still migrated, and the command exits with code 1.

`settings.local.json` files aren't in git, so copy any you care about first if you want an easy way back.

### Example apply

```text
/home/you/Dev/zero-knowledge-proofs
  .claude/settings.json (commit in /home/you/Dev/zero-knowledge-proofs)
    keep SessionStart: /home/you/Dev/zero-knowledge-proofs/.claude/hooks/ensure_window_id.sh (needed for the breadcrumb lookup)
    remove PostToolUse (Bash): /home/you/Dev/zero-knowledge-proofs/.claude/hooks/check_mail_on_close.sh (and its now empty group)
    replace Stop: /home/you/Dev/zero-knowledge-proofs/.claude/hooks/check_critical_on_stop.sh
      with ~/.local/bin/ntm agent-hook stop

Changed 1 file in 1 repo. Check each diff, then commit:
  git -C /home/you/Dev/zero-knowledge-proofs add .claude/settings.json && git -C /home/you/Dev/zero-knowledge-proofs commit -m "Move Agent Mail hooks to ntm agent-hook stop"
```

Run the same command again and you should see that nothing more is written. Worktree copies still show as skipped until you update them (see "Commit each repo").

## Check the diff

```bash
git -C ~/Dev/zero-knowledge-proofs diff .claude/settings.json
```

You should see only these changes:
- the Stop command line now reads `"command": "~/.local/bin/ntm agent-hook stop",`;
- the `check_mail_on_close.sh` entry is gone, with its group if nothing else was in it;
- if the old Stop entry had no timeout, a `"timeout": 5` line is added.

Nothing else in the file changes.

## Commit each repo

The apply report ends with one command per repo. Paste each one after you've checked that repo's diff:

```bash
git -C ~/Dev/zero-knowledge-proofs add .claude/settings.json && git -C ~/Dev/zero-knowledge-proofs commit -m "Move Agent Mail hooks to ntm agent-hook stop"
```

For a repo with linked worktrees: commit in the main repo first. Then bring that commit into each worktree's branch, for example with `git merge` or `git rebase` there. After that, `ntm agent-hook migrate` no longer lists the worktree.

## Test one repo

Check that a plain Claude session, not one started by ntm, still hears about new mail.

1. Start the session from the repo root: `cd ~/Dev/zero-knowledge-proofs && claude`. The hook takes the project from the folder the session started in, so a session started in a subfolder looks for mail under that subfolder.
2. In the session, register with Agent Mail as you normally do. Agent Mail saves the session's name and token in the repo's git folder, and the hook reads them from there. They are never written into `settings.json`.
3. Give the session some slow work. While it works, send it a message through Agent Mail.
4. You should see: when its turn ends, the session is told about the message, with the sender and subject.

If it isn't told, look at the end of `~/.local/share/ntm/logs/agent-hook.log`. The hook writes there any problem it hits, such as no name found for the session.

## Roll back

The old scripts are still on disk, so reverting the commit brings the old hooks straight back:

```bash
git -C ~/Dev/zero-knowledge-proofs revert --no-edit HEAD
```

Use the commit's id instead of `HEAD` if you've committed since. For a `settings.local.json` file, put back the copy you made before applying.
