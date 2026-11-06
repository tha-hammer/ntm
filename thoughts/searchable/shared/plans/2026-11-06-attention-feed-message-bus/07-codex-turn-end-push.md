# Loop 7: Codex turn-end push

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), processes **Turn-End Push** and **Hook Installation**.

## Overview

Codex agents get the same turn-end reminder as Claude agents, using Codex's own hooks feature. You approve the hook manually, so ntm keeps Codex's trust check on and doesn't pass `--dangerously-bypass-hook-trust`. Until you approve it, a Codex agent still gets the Idle Nudge from Loop 5.

Covers deliverable 10. Needs Loop 3, for the command and hook writer. Its Idle Nudge fallback comes from Loop 5, which the build order places first.

**What's known from Codex 0.167 on this VPS:**
- `codex features list` shows `hooks` as stable and on.
- `codex --help` describes `--dangerously-bypass-hook-trust` as running enabled hooks "without requiring persisted hook trust". So trust is saved somewhere.
- `codex --help` has no `hooks` subcommand.

**What isn't known yet:** the hook file's location and format, whether a Codex hook can make the agent continue, how you approve a hook, and how ntm can tell that a hook isn't approved. Sprint 1's evidence check answers these before any of it is built. The requirements below may change as a result.

---

## Sprint 1: Hook written, and approval notice

### Testable function

"Spawn a Codex agent, see the hook file written, and see ntm tell you that the project's hook still needs your approval."

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
- [ ] When a project's hook still needs approval, the spawn output tells you and says how to approve it. Robot spawn puts this in `warnings`.
- [ ] ntm's Codex launch command doesn't gain `--dangerously-bypass-hook-trust`.

### Requirements

**Data:** No new tables. The **HookRegistration** contract, in the file format the evidence check finds.

**API:**
- The hook writer from Loop 3 gains a Codex format, chosen by agent type at the same call sites.
- If approval state can't be read, the notice shows on every Codex spawn in a project and says it couldn't check. **Assumption.**

**UI:** One clear notice per project, not one per agent, with the exact approval steps.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| New file | Codex spawn in a clean folder | File with one entry |
| Merge | Existing Codex project settings | Kept, with one entry added |
| Re-run | Spawn twice | One entry |
| Worktrees | `--worktrees` with Codex | One file per worktree, with the main project key |
| Mixed session | Claude and Codex in one folder | Each file gets one entry |
| Unapproved | Fresh project | Notice with approval steps |
| Approved | Project already approved, if it can be detected | No notice |
| Launch command | Inspect the built Codex command | No bypass flag |
| Hook switch off | `stop_hook_enabled = false` | No file, no notice |

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop7 ./cmd/ntm`.
2. In a new scratch git repo, run `/tmp/ntm-loop7 spawn codextest --cod=1`.
3. Pass: the spawn output tells you this project's Codex hook needs approval, and says how.
4. Open the Codex hook file named in that notice.
5. Pass: it has one entry, with the absolute ntm path and this project's key.
6. Run `git status --short`.
7. Pass: the hook file isn't listed.
8. Run `tmux list-panes -a -F '#{pane_start_command}' | grep -c dangerously-bypass-hook-trust`.
9. Pass: it prints `0`.

---

## Sprint 2: Approved Codex agent told at turn end

### Testable function

"Approve the hook, send the working Codex agent mail, and see it learn about the mail when its turn ends, as far as Codex's hooks allow."

### Acceptance criteria

- [ ] After your approval, a working Codex agent learns about new mail at turn end, with sender and subject (**Reminder Format**).
- [ ] The response uses the form Codex expects, and the Claude output doesn't change.
- [ ] One message is announced once across the hook and nudge (**One Telling**).
- [ ] Before approval, the hook doesn't run, and the Codex agent still gets the Idle Nudge.
- [ ] Fail-open behavior matches Claude's: a problem never blocks a Codex agent (**Fail Open**).

### Requirements

**Data:** Uses `told_records`, like Claude.

**API:** The hook command chooses its output format from the agent tool. **Assumption:** a flag in the registered command, such as `--format=codex`, because the file written for each tool is already specific to it. `AGENT_NAME` is already set for Codex panes on the CLI and robot paths.

**UI:** None beyond the reminder.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Codex format | Run the command with the Codex format and new mail | Output matches the evidence check's format |
| Claude format unchanged | Run without the flag | Same as Loop 3 |
| Loop guard | Codex's equivalent of `stop_hook_active`, if any | No loop |
| Nothing new | Run again | Silent |
| Told by nudge | Nudge already announced it | Silent |
| Before approval | Unapproved hook, idle agent | Idle Nudge still arrives |

### Human test script

1. Keep the session from Sprint 1. Leave the hook unapproved for now.
2. With the Codex agent idle, send mail: `/tmp/ntm-loop7 mail send codextest --to <agent> "before approval"`.
3. Pass: within about a minute, the Idle Nudge arrives.
4. Follow the approval steps from the spawn notice, then kill the session and spawn again.
5. Give it slow work: `/tmp/ntm-loop7 send codextest --cod "Run sleep 60, then reply done."`.
6. While it works, send mail: `/tmp/ntm-loop7 mail send codextest --to <agent> "turn end test"`.
7. Run `/tmp/ntm-loop7 --robot-tail=codextest --lines=40` after the turn ends.
8. Pass: the agent learned about your message at turn end, in the way the evidence check found, and reacted once.
9. Wait 2 minutes.
10. Pass: no second reminder about that message.
