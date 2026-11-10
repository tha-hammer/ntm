# Loop 7: Codex turn-end push

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), processes **Turn-End Push** and **Hook Installation**.

## Overview

Codex agents get the same turn-end reminder as Claude agents, using Codex's own hooks feature. Spawn saves Codex's approval for ntm's own Stop entry, so Codex doesn't stop at its "Hooks need review" screen (decision, 2026-11-10). ntm still never passes `--dangerously-bypass-hook-trust`, and your other hooks still need your own review. When the hook can't run, a Codex agent still gets the Idle Nudge from Loop 5.

Covers deliverable 10. Needs Loop 3, for the command and hook writer. Its Idle Nudge fallback comes from Loop 5, which the build order places first.

**What's known from Codex 0.167 on this VPS:**
- `codex features list` shows `hooks` as stable and on.
- `codex --help` describes `--dangerously-bypass-hook-trust` as running enabled hooks "without requiring persisted hook trust". So trust is saved somewhere.
- `codex --help` has no `hooks` subcommand.

**Found by Sprint 1's evidence check (2026-11-10):** the hook file is `.codex/hooks.json`, in Claude's format. ntm's `block` decision makes Codex continue. Approval is a `trusted_hash` in `~/.codex/config.toml` under `hooks.state."<file>:stop:<group>:<handler>"`, and ntm can compute it. An unapproved or changed hook stops Codex at startup on a "Hooks need review" screen, which is why spawn saves the approval for its own entry. Details: the evidence record in [07-codex-turn-end-push-tdd.md](07-codex-turn-end-push-tdd.md).

---

## Sprint 1: Hook written and approved

### Testable function

"Spawn a Codex agent, see the hook file written and approved, and see Codex start without stopping at its hook review screen."

### Evidence check at sprint start

**Codex hook format (scope Evidence Needed item 1).** Check Codex's documentation and try it on this VPS, recording:
1. The project-level hook file, its format and the name of the turn-end event.
2. What the hook receives on stdin, and how it makes the agent continue, if it can.
3. How you approve a hook, where approval is saved, and whether a changed command or a new worktree needs approving again.
4. Whether ntm can read approval state without changing it.
5. Whether a merged project config file would interfere with other Codex settings.

If Codex hooks can't force a continue, Sprint 2's reminder becomes whatever Codex allows, such as context on the next turn, and the Idle Nudge remains the main path. If so, stop and bring it to you before building, because it changes what this loop delivers.

### Acceptance criteria

- [ ] `ntm spawn` (including `--worktrees`), robot spawn and `ntm add` each write one Codex hook entry before launching a Codex agent.
- [ ] The entry has the absolute ntm path and the project key, and no tokens, with the same safe merge as the Claude file: other entries kept, atomic write, no duplicates.
- [ ] The file's path is added to the git exclude file, and `stop_hook_enabled = false` also stops this file being written.
- [ ] Spawn saves Codex's approval for ntm's own entry only, changing nothing else in Codex's config. If it can't, or Codex hasn't trusted the folder yet, the spawn output tells you and says what to do. Robot spawn puts this in `warnings`.
- [ ] ntm's Codex launch command doesn't gain `--dangerously-bypass-hook-trust`.

### Requirements

**Data:** No new tables. The **HookRegistration** contract, in the file format the evidence check finds.

**API:**
- The hook writer from Loop 3 gains a Codex format, chosen by agent type at the same call sites.
- If Codex's config can't be read or written, the approval isn't saved, and the notice says so on that spawn.

**UI:** At most one notice per hook file, not one per agent, with the exact steps.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| New file | Codex spawn in a clean folder | File with one entry |
| Merge | Existing Codex project settings | Kept, with one entry added |
| Re-run | Spawn twice | One entry |
| Worktrees | `--worktrees` with Codex | One file per worktree, with the main project key |
| Mixed session | Claude and Codex in one folder | Each file gets one entry |
| Approval saved | Fresh project, Codex folder trusted | Approval entry for ntm's entry only, no notice |
| Approval kept | Spawn again | Config unchanged |
| Folder not trusted | Codex hasn't trusted the folder | Approval saved, and a notice that Codex will ask to trust the folder |
| Config unusable | `config.toml` can't be parsed | Nothing written, and a notice |
| Launch command | Inspect the built Codex command | No bypass flag |
| Hook switch off | `stop_hook_enabled = false` | No file, no notice |

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop7 ./cmd/ntm`.
2. In a new scratch git repo, run `/tmp/ntm-loop7 spawn codextest --cod=1`.
3. Pass: the spawn output names the Codex hook file it wrote. If Codex hadn't trusted this folder yet, it also says Codex will ask you to trust it.
4. Attach to the session, accept the folder trust prompt if Codex shows one, and open the hook file named in the spawn output.
5. Pass: it has one entry, with the absolute ntm path and this project's key.
6. Run `git status --short`.
7. Pass: the hook file isn't listed.
8. Pass: Codex didn't stop on a "Hooks need review" screen. In the Codex pane, type `/hooks`, and see the ntm Stop hook shown as Trusted.
9. Run `tmux list-panes -a -F '#{pane_start_command}' | grep -c dangerously-bypass-hook-trust`.
10. Pass: it prints `0`.

---

## Sprint 2: Approved Codex agent told at turn end

### Testable function

"Send a working Codex agent mail, and see it learn about the mail when its turn ends."

### Acceptance criteria

- [ ] With the hook approved, a working Codex agent learns about new mail at turn end, with sender and subject (**Reminder Format**).
- [ ] The response uses the form Codex expects, and the Claude output doesn't change.
- [ ] One message is announced once across the hook and nudge (**One Telling**).
- [ ] When the hook can't run (approval not saved, or the folder not trusted), the Codex agent still gets the Idle Nudge.
- [ ] Fail-open behavior matches Claude's: a problem never blocks a Codex agent (**Fail Open**).
- [ ] Spawn's folder trust notice shows whenever Codex hasn't trusted the folder, including with ntm's `--dangerously-bypass-approvals-and-sandbox` flag, and says to press Enter on first launch (added 2026-11-10 after Sprint 1's test; corrected after Sprint 2's test, where Codex 0.164 still asked).

### Requirements

**Data:** Uses `told_records`, like Claude.

**API:** No new flag: Codex takes the same `{"decision":"block","reason":...}` output as Claude (evidence check, decided at 4c). `AGENT_NAME` is already set for Codex panes on the CLI and robot paths, and reaches the hook.

**UI:** None beyond the reminder.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Codex format | Run the command with Codex's input and new mail | Output passes Codex's output schema |
| Claude format unchanged | Run with Claude's input | Same as Loop 3 |
| Loop guard | Codex's equivalent of `stop_hook_active`, if any | No loop |
| Nothing new | Run again | Silent |
| Told by nudge | Nudge already announced it | Silent |
| Hook can't run | Hook not approved, idle agent | Idle Nudge still arrives |
| Trust notice | Spawn with ntm's default Codex command in an untrusted folder | One notice: Codex will ask to trust the folder on first launch; press Enter |

### Human test script

1. Keep the session from Sprint 1.
2. With the Codex agent idle, send mail: `/tmp/ntm-loop7 mail send codextest --to <agent> "idle test"`.
3. Pass: within about a minute, the Idle Nudge arrives, once.
4. Let the agent finish handling that reminder.
5. Give it slow work: `/tmp/ntm-loop7 send codextest --cod "Run sleep 60, then reply done."`.
6. While it works, send mail: `/tmp/ntm-loop7 mail send codextest --to <agent> "turn end test"`.
7. Run `/tmp/ntm-loop7 --robot-tail=codextest --lines=40` after the turn ends.
8. Pass: the agent learned about your message at turn end, in the way the evidence check found, and reacted once.
9. Wait 2 minutes.
10. Pass: no second reminder about that message.
