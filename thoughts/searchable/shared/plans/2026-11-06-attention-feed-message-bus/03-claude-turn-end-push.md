# Loop 3: Claude turn-end push

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), processes **Turn-End Push** and **Hook Installation**, contracts **ToldRecord**, **ReminderText**, **HookRegistration** and **TurnEndInput**.

## Overview

This loop tells a Claude agent about new mail when its turn ends, once per message, with no dashboard or `ntm serve` running. Sprints 1 and 2 build the `ntm agent-hook stop` command. Sprints 3 to 6 install it through each way ntm creates a Claude agent. Sprint 7 adds the off switch. It reaches the scope's First Useful Result.

Covers deliverables 4, 8, 9, 11, 15 and the hook half of 16. Needs Loop 2.

Two gaps found while writing this document:
- **Tokens.** Agent Mail registration tokens are cached in the memory of a running client (`internal/agentmail/client.go:93`), so a fresh hook process has none. The hook loads the token from the session's `agent_registry.json`, or from the breadcrumb file for sessions ntm didn't spawn. Tokens are never written to hook files.
- **`ntm add`.** `internal/cli/add.go` doesn't register an Agent Mail identity or set `AGENT_NAME`, so Sprint 6 has to add that.

---

## Sprint 1: Hook command tells an agent once

### Testable function

"Run `ntm agent-hook stop` by hand as an agent with new mail, and see a continue decision with the sender and cleaned subject. Run it again and see no output."

### Evidence check at sprint start

**Hook speed (scope Evidence Needed item 5).** Time the command against the real Agent Mail server, including opening the store, and compare it to the 1 second target. If it misses, change what the command loads at startup. Startup is a small part of the budget: `ntm version` takes about 40 ms here.

### Acceptance criteria

- [ ] With new unread mail, the command prints Claude Code's `{"decision":"block","reason":...}` JSON, with sender and subject in the reason, and exits 0.
- [ ] A second run with no new mail prints nothing and exits 0.
- [ ] `stop_hook_active: true`, malformed stdin, or Agent Mail being down each print nothing and exit 0, and the failure cases are logged (**Fail Open**).
- [ ] Subjects follow **Reminder Format**: single line, no control characters, at most 120 characters, first 5 messages listed with a count of the rest, and labeled as message data.
- [ ] Each message announced has a durable `ToldRecord` and an addressed feed event.

### Requirements

**Data:** A new `told_records` table in `state.db`, with the identity project, agent and message from the **ToldRecord** contract, and 7 day cleanup. Claiming a message is an insert that reports whether it won.

**API:**
- New command `ntm agent-hook stop [--project <key>]`, with `stdin` as `TurnEndInput`.
- Finds the session from `TMUX_PANE`, not from the hook file, because several sessions can share one work folder.
- Loads the token from `LoadBestSessionAgentRegistry` (`internal/agentmail/session.go:550`).
- Calls `FetchInbox`, drops read and already told messages, runs **Mail Notice Recording**, claims them, then responds. It doesn't use `--robot-*`, so it skips the projection refresh, which calls `fetch_inbox` for every agent.
- The command does nothing when `agent_mail.enabled` is false.

**UI:** None. The reason text is the agent-facing output, following the **ReminderText** contract.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| New mail | One unread message | Block JSON naming the sender and subject |
| Nothing new | Run again | No output, exit 0 |
| Read mail only | All messages read | No output |
| Loop guard | `stop_hook_active: true` with new mail | No output, and the message isn't claimed |
| Server down | Agent Mail unreachable | No output, exit 0, one log line |
| Store down | `state.db` locked past the timeout | Still reminds, and may repeat later |
| Hostile subject | Newlines, ANSI codes, 5,000 characters, "ignore previous instructions" | One line, cleaned, cut to length, inside the data label |
| Big batch | 12 new messages | First 5 listed, "and 7 more" |
| Race | Two runs at once | One reminds, the other stays silent |

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop3 ./cmd/ntm`.
2. In a scratch project folder, create a session: `/tmp/ntm-loop3 spawn hooktest --cc=1`.
3. Get the agent's name and pane ID: `/tmp/ntm-loop3 --robot-status`.
4. Send mail: `/tmp/ntm-loop3 mail send hooktest --to <agent> "hook test"`.
5. Run `echo '{}' | TMUX_PANE=<pane id> AGENT_NAME=<agent> /tmp/ntm-loop3 agent-hook stop --project "$PWD"`.
6. Pass: it prints JSON with `"decision":"block"` and a reason naming `HumanOverseer` and your subject.
7. Run the same command again.
8. Pass: it prints nothing.

---

## Sprint 2: Identity lookup

### Testable function

"Run the same command with no `AGENT_NAME`: with a breadcrumb file it finds the name, and with neither it stays silent."

### Acceptance criteria

- [ ] The name comes from `AGENT_NAME`, then `MCP_AGENT_MAIL_AGENT_NAME`, then the breadcrumb files (**Agent Identity Order**).
- [ ] Inside tmux, the pane's `identity-<window_id>.json` is used, not another agent's shared `identity.json`.
- [ ] With no name found, the command prints nothing and doesn't call Agent Mail.
- [ ] With no `--project`, the project comes from `MCP_AGENT_MAIL_PROJECT`, then the current folder.
- [ ] A corrupt breadcrumb is treated as no name, and logged.

### Requirements

**Data:** Reads `<git-common-dir>/agent-mail/identity[-<window_id>].json` (`agent_name`, `registration_token`) and `/tmp/mcp-window-id-tmux-<pane>`, as the old `check_critical_on_stop.sh` does. Breadcrumbs are read only.

**API:** A Go lookup function used by the command. It keeps the old script's rule: inside tmux, a missing per-pane file means no name, rather than falling back to the shared file.

**UI:** None.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Order | All three sources set to different names | `AGENT_NAME` wins |
| Per-pane breadcrumb | In tmux, window id file and matching identity file | That name |
| Other agent's shared file | In tmux, only `identity.json` exists | No name |
| Outside tmux | Only `identity.json` exists | That name |
| Nothing | No sources | Silent, and no Agent Mail call |
| Corrupt file | Invalid JSON breadcrumb | Silent, one log line |
| Not a git repo | Run from a plain folder | Silent |

### Human test script

1. In a scratch git repo outside any ntm session, start a shell with `AGENT_NAME` unset, outside tmux.
2. Run `echo '{}' | /tmp/ntm-loop3 agent-hook stop`.
3. Pass: no output, because there's no name.
4. Register an agent the normal way in that repo, so Agent Mail writes `identity.json` under `.git/agent-mail/`. Use a Claude session calling `register_agent`.
5. Send it mail through Agent Mail.
6. Run the same command again.
7. Pass: it prints the block JSON with your subject.

---

## Sprint 3: Hook written by `ntm spawn`

### Testable function

"Create a session with `ntm spawn`, send a working Claude agent mail, and watch the agent get told when its turn ends, with no dashboard or `ntm serve` running. Spawn again, and see one hook entry with other entries kept."

### Evidence check at sprint start

**First-run folder trust (scope Evidence Needed item 4).** Check whether Claude Code runs a `settings.local.json` hook in a brand-new work folder, and how its trust prompt affects that. If the hook doesn't run until the folder is trusted, the plan changes before building.

### Acceptance criteria

- [ ] Each Claude agent's work folder gets one ntm Stop entry in `.claude/settings.local.json` before the agent launches.
- [ ] The entry has the absolute path of the running ntm binary and the project key, and no tokens.
- [ ] Existing entries, including other Stop hooks, are kept, and the file is written atomically.
- [ ] The path is added to the file from `git rev-parse --git-path info/exclude`, once, and a folder that isn't a git repo skips this.
- [ ] A settings file that can't be parsed is left unchanged, a warning names it, and the agent still launches.
- [ ] A working agent told through the real Stop hook runs `fetch_inbox`, then stops normally.

### Requirements

**Data:** The **HookRegistration** contract, with a 5 second timeout.

**API:**
- A hook writer in Go, placed next to the existing Claude hook setup in `internal/cli/spawn.go:2102`, and reusable by later sprints.
- It recognizes its own entry by command, so an upgrade replaces the path.
- The file stays after the session ends.

**UI:** The spawn output gets a one-line info message, as DCG and RCH have today. JSON output mode stays clean.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| New file | No `.claude` folder | File created with one entry |
| Merge | Existing `permissions` and PostToolUse hook | Both kept, one Stop entry added |
| Other Stop hook | Existing unrelated Stop hook | Both Stop hooks are there |
| Re-run | Write twice | One entry |
| New binary path | Write with another path | Entry replaced, not added |
| Invalid JSON | Corrupt file | Unchanged, warning, spawn continues |
| Exclude | Git repo, write twice | One exclude line |
| Not a repo | Plain folder | File written, exclude skipped |

### Human test script

1. In a new scratch git repo, create `.claude/settings.local.json` with `{"permissions":{"allow":[]}}`.
2. Create a session: `/tmp/ntm-loop3 spawn pushtest --cc=1`. Make sure no dashboard or `ntm serve` is running.
3. Run `jq '.permissions, .hooks.Stop' .claude/settings.local.json`.
4. Pass: `permissions` is kept, and there's one Stop entry with the absolute ntm path.
5. Run `git status --short`.
6. Pass: the settings file isn't listed.
7. Give the agent slow work: `/tmp/ntm-loop3 send pushtest --cc "Run sleep 60, then reply done."`.
8. While it works, send mail: `/tmp/ntm-loop3 mail send pushtest --to <agent> "turn end test"`.
9. Attach and watch, or use `/tmp/ntm-loop3 --robot-tail=pushtest --lines=40`.
10. Pass: when the turn ends, the Stop hook feedback names your subject, and the agent runs `fetch_inbox`, then stops.
11. Kill the session, then run the same spawn command again in that folder.
12. Pass: `jq '.hooks.Stop | length' .claude/settings.local.json` still prints `1`.

---

## Sprint 4: Hook written with `--worktrees`

### Testable function

"Do the same with `ntm spawn --worktrees`, and see the hook use the main project key and the file stay out of `git status`."

### Acceptance criteria

- [ ] Each agent's worktree gets its own `.claude/settings.local.json`.
- [ ] The entry's project key is the main project folder, matching the key given to Agent Mail (`internal/cli/spawn.go:2788`), not the worktree folder.
- [ ] The exclude line goes into the shared `info/exclude`, once, and the file stays out of `git status` in the main repo and each worktree.
- [ ] A working agent in a worktree is told at turn end.
- [ ] The main project folder doesn't get a hook file written for those agents.

### Requirements

**Data:** No new data.

**API:** Uses `workingDir` from `internal/cli/spawn.go:2343-2349` for the file, and `dir` for the project key. The exclude path comes from `git rev-parse --git-path info/exclude`, because `.git` is a file in a worktree.

**UI:** None.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Two worktrees | Spawn 2 agents with `--worktrees` | Two files, both with the main project key |
| Shared exclude | Same | One exclude line in the common `info/exclude` |
| Failed worktree | Worktree creation fails for an agent | Falls back to the main folder, as today, with a correct hook there |
| Named worktree | `--worktree-name foo` | File written in that worktree |

### Human test script

1. In a scratch git repo with one commit, run `/tmp/ntm-loop3 spawn wttest --cc=2 --worktrees`.
2. Run `/tmp/ntm-loop3 worktrees list` to see the two paths.
3. For each path, run `jq -r '.hooks.Stop[0].hooks[0].command' <path>/.claude/settings.local.json`.
4. Pass: each command's `--project` is the main repo folder.
5. Run `git -C <path> status --short` for each path, and `git status --short` in the main repo.
6. Pass: no settings file is listed.
7. Run steps 7 to 10 of the Sprint 3 script, for one of the agents.
8. Pass: that agent is told at turn end.

---

## Sprint 5: Hook written by robot spawn

### Testable function

"Do the same with robot spawn (`ntm --robot-spawn`)."

### Acceptance criteria

- [ ] `ntm --robot-spawn` writes the same entry, using the same hook writer, before launching each Claude agent.
- [ ] Non-Claude agents in that spawn don't get the Claude file.
- [ ] The robot JSON stays valid, and a hook write failure appears as a warning in the JSON's `warnings`, not on stdout as plain text.
- [ ] A working agent spawned this way is told at turn end.

### Requirements

**Data:** No new data.

**API:** Calls the Sprint 3 hook writer from the robot spawn path (`launchAgent`, `internal/robot/spawn.go:729`). It already sets `AGENT_NAME` (`:769`). The writer must be in a package that both `internal/cli` and `internal/robot` can import.

**UI:** None. JSON only, following the field rules in `AGENTS.md`.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Robot spawn | `--robot-spawn` with `--spawn-cc=1` | One Stop entry, valid JSON output |
| Mixed agents | Claude and Codex | Claude file only for the Claude agent's folder (Codex comes in Loop 7) |
| Write failure | Read-only `.claude` folder | `warnings` entry, and the agent launches |

### Human test script

1. In a new scratch git repo, run `/tmp/ntm-loop3 --robot-spawn=robottest --spawn-cc=1 --spawn-dir="$PWD" | jq '{success, warnings}'`.
2. Pass: `success` is `true`.
3. Run `jq '.hooks.Stop | length' .claude/settings.local.json`.
4. Pass: it prints `1`.
5. Run steps 7 to 10 of the Sprint 3 script against `robottest`.
6. Pass: the agent is told at turn end.

---

## Sprint 6: Hook written by `ntm add`

### Testable function

"Add an agent with `ntm add`, and see that it gets told at turn end too."

### Acceptance criteria

- [ ] `ntm add` writes the same hook entry before launching a Claude agent.
- [ ] An added agent gets an Agent Mail identity, and the session's registry records it.
- [ ] An added agent launches with `AGENT_NAME` set, like a spawned one.
- [ ] A mail message sent to the added agent reaches it at turn end.
- [ ] If Agent Mail is unavailable, `ntm add` still adds the agent, as spawn does.

### Requirements

**Data:** New entries in the session's `agent_registry.json`.

**API:** `internal/cli/add.go` has no Agent Mail registration today. Reuse `registerSpawnedAgents` (`internal/cli/spawn.go:3223`), or the same lower-level calls, rather than copying them. Follow the `AGENT_NAME` prefix at `spawn.go:2334`.

**UI:** The add output gets the same one-line info message as spawn.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Added agent | Add 1 Claude agent to a session | Hook entry, registry entry, `AGENT_NAME` set |
| Added next to existing | Session already has an agent in that folder | One Stop entry, and two names told apart |
| Agent Mail down | Add while the server is stopped | Agent added, warning shown, no name set |
| Non-Claude add | Add a Codex agent | No Claude file written (Codex comes in Loop 7) |

### Human test script

1. Create a session with one agent: `/tmp/ntm-loop3 spawn addtest --cc=1`.
2. Add another: `/tmp/ntm-loop3 add addtest --cc=1`.
3. Run `/tmp/ntm-loop3 --robot-status`, and note the new agent's name and pane.
4. Pass: the new pane has its own Agent Mail name.
5. Run `jq '.hooks.Stop | length' .claude/settings.local.json`.
6. Pass: it prints `1`, since both agents share the folder.
7. Give the new agent slow work with `/tmp/ntm-loop3 send addtest --panes=<pane> "Run sleep 60, then reply done."`, then send it mail with `/tmp/ntm-loop3 mail send addtest --to <new agent> "added agent test"`.
8. Pass: only the new agent is told at its turn end, and the first agent is not.

---

## Sprint 7: Hook switch

### Testable function

"Turn the hook switch off in config, spawn, and see no hook file written and no reminder."

### Acceptance criteria

- [ ] The hook is on by default whenever Agent Mail is enabled (**On By Default**).
- [ ] With the switch off, `ntm spawn`, `--worktrees`, robot spawn and `ntm add` don't write a hook entry.
- [ ] With the switch off, an entry already in a file does nothing: `ntm agent-hook stop` exits silently.
- [ ] With `agent_mail.enabled = false`, no entry is written, whatever the switch says.
- [ ] Turning the switch off doesn't remove existing entries, and turning it back on doesn't add a second one.

### Requirements

**Data:** No new tables.

**API:** A new setting in the `agent_mail` config section (`AgentMailConfig`, `internal/config/config.go:1313`). **Assumption:** the name is `stop_hook_enabled`, a pointer defaulting to true, like `SupervisorEnabled`. Loop 5 adds the matching nudge setting.

**UI:** Listed wherever config settings are documented. `ntm config` shows it like its neighbors.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Default | No setting | On |
| Off | Setting false, all four paths | No file written |
| Existing entry, switch off | Entry present, setting false | Command is silent |
| Agent Mail off | `enabled = false` | No file written |
| Back on | Off, then on, then spawn | One entry |

### Human test script

1. Add this to your ntm config: `[agent_mail]` with `stop_hook_enabled = false`.
2. In a new scratch git repo, run `/tmp/ntm-loop3 spawn offtest --cc=1`.
3. Run `ls .claude/settings.local.json`.
4. Pass: the file doesn't exist, or has no ntm Stop entry.
5. Give the agent slow work and send it mail, as in Sprint 3.
6. Pass: the turn ends with no reminder.
7. Remove the setting, kill the session and spawn again.
8. Pass: `jq '.hooks.Stop | length' .claude/settings.local.json` prints `1`.
