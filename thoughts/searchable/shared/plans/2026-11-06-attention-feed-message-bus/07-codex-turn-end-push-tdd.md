# Loop 7: Codex turn-end push — TDD plan

Loop document: [07-codex-turn-end-push.md](07-codex-turn-end-push.md). Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), processes **Turn-End Push** and **Hook Installation**, rules **One Telling**, **Reminder Format** and **Fail Open**.

Epic `bd-7yrae`. Sprint beads `bd-7yrae.1` and `bd-7yrae.2`.

## Overview

Codex agents get the same turn-end reminder as Claude agents, through Codex's own hooks. Spawn writes `.codex/hooks.json` in each Codex work folder before launch and keeps it out of `git status`. It then saves Codex's approval for ntm's own Stop entry in `~/.codex/config.toml`, so Codex doesn't stop at its "Hooks need review" screen. It tells you when it couldn't save the approval, or when Codex hasn't trusted the folder yet. ntm never passes `--dangerously-bypass-hook-trust`, and your other hooks still need your own review. When the hook can't run, the Idle Nudge from Loop 5 still covers the agent.

The behaviors are 19 observable results in 2 sprints:
- **Sprint 1 (Behaviors 1 to 13):** the Codex hook file written and approved by every spawn path, and the notice when that couldn't be done.
- **Sprint 2 (Behaviors 14 to 19):** an approved Codex agent told at turn end, once, with failures that stay silent.

## Current State Analysis

- `claudeconfig.InstallStopHook` (`internal/claudeconfig/claudeconfig.go:370`) writes `<workdir>/.claude/settings.local.json`. It uses `WriteStopHook` (`:327`), `mergeStopHook` (`:385`), `writeSettingsAtomic` (`:215`) and `git.EnsureExcluded` (`internal/git/root.go:33`). It refuses the home folder (`ErrHomeFolder`, `:270`).
- `installClaudeStopHookIfActive` (`internal/cli/agent_hook.go:94`) is called only for Claude agents: `spawn.go:2352` (`if agent.Type == AgentTypeClaude`) and `add.go:751`.
- Robot spawn writes the Claude file in its Claude loop (`internal/robot/spawn.go:640`, `writeClaudeStopHook` at `:738`). Its Codex loop (`:650`) writes nothing. Problems go in `SpawnOutput.Warnings` (`:76`).
- `runAgentHookStop` (`agent_hook.go:133`) prints `{"decision":"block","reason":...}` (`stopDecision`, `:52`). `readTurnEndInput` uses plain `json.Unmarshal`, so unknown fields are ignored.
- The default Codex command (`internal/config/templates.go:325`) has `--dangerously-bypass-approvals-and-sandbox`, and no `--dangerously-bypass-hook-trust`.
- The Idle Nudge already covers Codex panes (Loop 5, the `cod` input marker in `internal/agent/input_line.go`).

### Key Discoveries

These come from strings in the Codex 0.167 binary on this VPS (`~/.codex/packages/standalone/releases/0.167.0-x86_64-unknown-linux-musl/bin/codex`). Sprint 1's evidence check confirms them with a real run.
- **Hook file.** The project hook file is `.codex/hooks.json`. Codex also accepts `[hooks]` in a `config.toml`. Its shape matches Claude's: event name, then a list of groups (`matcher`, `hooks`), each handler tagged `type: "command"` with `command`, `timeout` and `statusMessage`.
- **Stop input.** The Stop input has `stop_hook_active`, `cwd`, `session_id`, `turn_id`, `model`, `permission_mode`, `transcript_path` and `hook_event_name`.
- **Stop output.** The `stop.command.output` schema has `additionalProperties: false` and allows `continue`, `decision` (`"block"`), `reason`, `stopReason`, `suppressOutput` and `systemMessage`. Codex enforces Claude's rule that `block` needs a `reason`. So ntm's existing output fits as it is, and no `--format=codex` flag is needed (approved at 4c).
- **Approval.** You approve hooks inside Codex with `/hooks`, which shows "1 hook needs review before it can run." Approval is saved in the user `config.toml` as `hooks.state."<key>"` with `enabled` and `trusted_hash` (`HookStateToml`). The evidence record below has the key and hash formats, and the startup screen that led to the change below.
- **Project trust.** Codex also has project trust (`projects.<path>.trust_level`), and says "The new worktree is not trusted; run Codex there." A project's `.codex/` folder may load only after the project is trusted.
- **Global file.** `~/.codex/hooks.json` is your global Codex hook file, so writing into the home folder must be refused, as with Claude.
- **Test pattern.** `TestClosure_RobotSpawnWritesClaudeStopHook` (`agent_hook_test.go:1328`) spawns `--spawn-cod=1` and expects no warnings. Sprint 1 keeps that expectation when the approval is saved and the folder is trusted, and adds cases that bring a notice.

## Desired End State

### Observable Behaviors

1. Given a clean folder, when the Codex writer runs, then `.codex/hooks.json` holds one ntm Stop entry with the absolute ntm path, the project key and timeout 5, and no token.
2. Given a file with other events, other Stop entries and other keys, when the writer runs twice, then everything else is kept, there is one ntm entry, and the second run doesn't write.
3. Given an old ntm entry naming another binary, when the writer runs, then it's replaced.
4. Given a file that isn't valid JSON or isn't the expected shape, when the writer runs, then the file is unchanged and the error names it.
5. Given the home folder as the work folder, when the writer runs, then it refuses and writes nothing.
6. Given a git work folder, when the writer runs, then `.codex/hooks.json` is in the exclude file once. Outside git, that step is skipped.
7. Given `.codex/hooks.json` is tracked by git, when spawn runs, then the file is left alone and a warning says why (approved at 4c).
8. Given Codex agents, when `ntm spawn`, `ntm spawn --worktrees`, robot spawn or `ntm add` runs, then each Codex work folder has one entry before launch, with the main project key. A mixed folder gets both files.
9. Given `stop_hook_enabled = false`, when any spawn path runs, then no Codex file is written and no notice is shown.
10. Given the Codex file can't be written, when spawn runs, then a warning names it and the agent launches.
11. Given Codex's `config.toml`, when ntm approves its own written entry, then it saves exactly that entry's key and hash, keeps every other byte, does nothing when the entry is already approved, and leaves an unusable file unchanged.
12. Given a Codex spawn, when the approval is saved and the folder is trusted, then no notice is shown. When ntm couldn't save it, or Codex hasn't trusted the folder, one notice per hook file says what to do (robot spawn puts it in `warnings`).
13. Given the default Codex command, when the CLI and robot paths build it, then it has no `--dangerously-bypass-hook-trust`.
14. Given the full Codex Stop input, when the hook reads it, then it parses, and `stop_hook_active: true` stays silent.
15. Given a Codex agent with new mail, when the written hook command runs as Codex runs it, then it prints one object that passes Codex's output schema, with `[#id]`, sender and subject.
16. Given Claude input, when the hook runs, then its output is the same as in Loop 3.
17. Given Agent Mail is down or the store fails, when the Codex hook runs, then nothing is printed, it exits 0 and the log has the reason.
18. Given a Codex pane, when the hook tells first and the nudge polls next (or the reverse), then the message is announced once.
19. Given a Codex agent whose hook can't run (no approval saved), when mail arrives while it's idle, then the Idle Nudge still types the reminder.

## What We're NOT Doing

- Passing `--dangerously-bypass-hook-trust`.
- Approving any hook other than ntm's own Stop entry, or changing `enabled` on any hook.
- Saving Codex's folder trust (`projects.<path>.trust_level`). Codex still asks you to trust a new folder, as it does today (see `bd-57xdn` for Claude's version of that prompt).
- Writing `[hooks]` into `.codex/config.toml`. The JSON file keeps project settings separate (evidence item 5 checks this).
- A `--format=codex` flag (dropped at 4c: Codex takes Claude's output).
- Changing Codex's `notify` setting from the Agent Mail installer. Loop 8's migration covers old hooks.
- Gemini or Antigravity hooks.

## Scope decisions (4c, 2026-11-10)

- **Test setup:** the same as Loops 5 and 6, plus Codex's `stop.command.output` schema copied into testdata.
- **Tracked hook file:** skip and warn. The Idle Nudge still covers that agent.
- **Approval state that can't be read:** the notice shows on every Codex spawn in that project and says ntm couldn't check.

## Decision change (Sprint 1 evidence, 2026-11-10)

The evidence check found that an unapproved or changed hook stops Codex at startup on a "Hooks need review" screen. That would hit every new folder, every new worktree, and every change of the ntm binary path. You chose: **ntm approves it.** Spawn writes `trusted_hash` for ntm's own entry only. This replaces the scope decision "Codex hooks approved manually" (SCOPE.md section 7 and Appendix C, updated). Behaviors 11, 12 and 19 changed to match. The notice now covers the cases where ntm couldn't save the approval, or Codex hasn't trusted the folder.
- **Output format:** no `--format=codex` flag.

## Testing Strategy

- **Framework:** Go `testing`, with `pgregory.net/rapid` for properties. Both are already used in the repo.
- **Test types:**
  - Unit: Behaviors 1 to 7, 11, 13, 14, 16 and 17.
  - Closure: Behaviors 8, 9, 10, 12, 15, 18 and 19.
- **Setup:**
  - Closure tests run `Execute` in helper processes, on a private tmux server, with the fake Agent Mail server and stand-in agents. That's the Loop 3 to 6 harness (`closureEnv`, `fakeAgentMail`, `standInAgentConfig`, `screenAgentConfig`, `spawnNudgeAgent`).
  - The stand-in agent capture (`agent_hook_test.go:912`) gains a `CodexHooks` field read from `.codex/hooks.json` at launch, so tests see the file as it was when the agent started.
  - `CODEX_HOME` points at a temp folder in every test, so no test reads or writes the real `~/.codex`.
  - Approval fixtures are `config.toml` files copied from the Sprint 1 evidence run, with paths changed to `/home/user/...`.
  - Codex's output schema is saved as `internal/cli/testdata/codex_stop_output_schema.json`, copied from the binary's strings.
- **How tests run:** `go test -short ./internal/claudeconfig/ ./internal/git/ ./internal/robot/ ./internal/cli/`, plus the sprint verification list at the end.

## Workflow Closure

The research's Chain A (`thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`, Workflow Closure Map) runs from `ntm mail send` to a Stop hook's output. Loop 3 replaced its depth-2 node with `ntm agent-hook stop`. This loop adds a second hook file for that node, written by the spawn paths, and a Codex-shaped run of the same command.

- **`highest_new_connector`:**
  - Sprint 1: the spawn paths' Codex branch (`installStopHookIfActive` and robot spawn's Codex loop), plus the approval notice.
  - Sprint 2: the written Codex hook command, run with Codex's input and environment.
- **Trigger choice:** `Execute` with `spawn`, `--robot-spawn`, `add` and `internal-monitor`, and the command read from the written `.codex/hooks.json`, each in a helper process. The only direct seeds are messages on the fake server, config files, and approval fixtures in `CODEX_HOME`.

### Production Operation Chain

`ntm spawn | --robot-spawn | ntm add -> (Codex agent) installStopHookIfActive -> claudeconfig.InstallCodexStopHook (tracked check -> merge -> atomic write -> EnsureExcluded) -> claudeconfig.ApproveCodexStopHook (CodexHookApproval read -> write trusted_hash for ntm's entry in CODEX_HOME/config.toml) -> codexApprovalNotices (only when not approved or folder untrusted) -> spawn output | warnings -> agent launch (no review screen) -> Codex turn end -> .codex/hooks.json command: ntm agent-hook stop --project P -> runAgentHookStop -> FetchInbox -> claimUnread (mailToAnnounce, ClaimToldRecord) -> {"decision":"block","reason":ReminderText} -> Codex continues`

**Production callers:**

| New code | Production caller |
|---|---|
| `claudeconfig.InstallCodexStopHook`, `CodexHooksPath` | `installStopHookIfActive` (cli), `writeStopHook` (robot) |
| `claudeconfig.WriteStopHookFile` (the shared merge for both files) | `WriteStopHook`, `InstallCodexStopHook` |
| `git.IsTracked` | `InstallCodexStopHook` |
| `claudeconfig.ApproveCodexStopHook`, `CodexHookApproval`, `codexHookKey`, `codexHookHash` | `installCodexStopHook` (cli) and robot `writeStopHook`; `CodexHookApproval` is also called by `ApproveCodexStopHook` |
| `installStopHookIfActive` (replaces `installClaudeStopHookIfActive`) | `spawn.go:2352`, `add.go:751` |
| `codexApprovalNotices` | `runSpawn` / `spawnSessionLogic` and `runAdd`, printed once after the launches |
| robot `writeStopHook(agentType, dir)` (replaces `writeClaudeStopHook`) | robot spawn's Claude and Codex loops (`spawn.go:640`, `:650`) |

### Closure Test: Every spawn path writes one Codex hook entry before launch [BLOCKING]

Behavior 8.
- **SOURCE (seed only)**: git repos under the env's projects base, one with an existing `.codex/hooks.json` (another event, another Stop entry, a top-level key), and the stand-in agent config.
- **TRIGGER (start)**: `Execute` with `spawn <s> --cod=1`, `spawn <s> --cod=2 --worktrees`, `--robot-spawn=<s> --spawn-cc=1 --spawn-cod=1`, and `add <s> --cod=1`, in helper processes (boundary = highest_new_connector, the spawn paths' Codex branch).
- **DRIVERS (async edges)**: the agent launch is a tmux send. `capture.waitForType(t, "codex", n, 20s)` waits for the stand-in to record its launch.
- **OBSERVE (assert via)**: the stand-in's `CodexHooks` as read at launch, `git status --short`, and `countExcludeLines`.
- **FORBIDDEN SPAN**: `installStopHookIfActive`, `InstallCodexStopHook`, `WriteStopHookFile`, `EnsureExcluded`. The test never writes the ntm entry itself.
- **RED-AT-SEAM proof**: keep the call sites' `agent.Type == AgentTypeClaude` check. Expected: "codex hooks at launch = nil, want one ntm Stop entry".
- **DRIVABILITY**: the ntm path is the helper binary (`helperBinary(t)`), the store and config come from the env, and `CODEX_HOME` is a temp folder. No clock is needed.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/` on the private tmux server. `testutil.RequireTmuxThrottled` fails when tmux is missing. There's no skip to green.

### Closure Test: The hook switch stops the Codex file and the notice [BLOCKING]

Behavior 9.
- **SOURCE (seed only)**: `stop_hook_enabled = false` in the env's config, and a git repo.
- **TRIGGER (start)**: `Execute` with `spawn <s> --cod=1`, then `--robot-spawn=<s2> --spawn-cod=1` (boundary = highest_new_connector).
- **DRIVERS (async edges)**: `capture.waitForType`.
- **OBSERVE (assert via)**: no `.codex/hooks.json` at launch. Spawn output and robot `warnings` have no approval notice.
- **FORBIDDEN SPAN**: `StopHookActive`, `installStopHookIfActive`.
- **RED-AT-SEAM proof**: drop the switch check for Codex. Expected: "codex hooks written with the switch off".
- **DRIVABILITY**: config file through `env.writeConfig`.
- **EXECUTION (must run)**: as above.

### Closure Test: A failed Codex hook write warns, and the agent still launches [BLOCKING]

Behavior 10 (and Behavior 7 at spawn level).
- **SOURCE (seed only)**: three repos. One has `.codex/hooks.json` holding `{bad`. One has a `.codex` that is a file, not a folder. One has a committed `.codex/hooks.json`.
- **TRIGGER (start)**: `Execute` with `spawn <s> --cod=1` in each, and `--robot-spawn` in the first (boundary = highest_new_connector).
- **DRIVERS (async edges)**: `capture.waitForType`.
- **OBSERVE (assert via)**: the agent launched. The output has one warning naming the file and the reason. The bad and committed files are byte-for-byte unchanged. `git status --short` is empty for the committed one.
- **FORBIDDEN SPAN**: `InstallCodexStopHook`, `git.IsTracked`.
- **RED-AT-SEAM proof**: return the write error from the spawn path instead of warning. Expected: the spawn exits non-zero and no agent launches.
- **DRIVABILITY**: as above.
- **EXECUTION (must run)**: as above.

### Closure Test: Spawn approves its Codex hook, and tells you when it couldn't [BLOCKING]

Behavior 12.
- **SOURCE (seed only)**: `CODEX_HOME/config.toml` fixtures: no file; a file with your own settings, another hook's state and this folder trusted; the same without folder trust; one approving an older ntm command; one that can't be parsed; and one with ntm's entry set to `enabled = false`.
- **TRIGGER (start)**: `Execute` with `spawn <s> --cod=2`, `spawn <s> --cod=2 --worktrees`, `--robot-spawn=<s> --spawn-cod=2`, and `add <s> --cod=1` (boundary = highest_new_connector).
- **DRIVERS (async edges)**: `capture.waitForType`.
- **OBSERVE (assert via)**:
  - `CodexHookApproval` through a `codex-approval` helper mode. That is the same production read spawn uses to decide whether to write.
  - The rest of `config.toml` byte for byte.
  - The spawn's stdout, and the robot response's `warnings`.
- **FORBIDDEN SPAN**: `ApproveCodexStopHook`, `codexHookHash`, `codexHookKey`, `codexApprovalNotices`. The test never writes `trusted_hash` itself.
- **RED-AT-SEAM proof**: don't call `ApproveCodexStopHook` from the spawn path. Expected: "approval for <file> = needs approval, want granted".
- **DRIVABILITY**: `CODEX_HOME` set in the helper environment.
- **EXECUTION (must run)**: as above.

### Closure Test: An approved Codex hook tells a working agent about new mail [BLOCKING]

Behavior 15.
- **SOURCE (seed only)**: one message to BlueLake on the fake server, and a spawned Codex stand-in whose pane has `AGENT_NAME=BlueLake`.
- **TRIGGER (start)**: the `command` string read from the written `.codex/hooks.json`, run with `sh -c` in the agent's work folder, with Codex's Stop input on stdin and the environment the evidence check found Codex passes (boundary = highest_new_connector, the written command).
- **DRIVERS (async edges)**: none. The hook is synchronous.
- **OBSERVE (assert via)**: stdout parsed as one JSON object and checked against `testdata/codex_stop_output_schema.json`. A second run prints nothing.
- **FORBIDDEN SPAN**: `runAgentHookStop`, `claimUnread`, `ClaimToldRecord`, the store.
- **RED-AT-SEAM proof**: write the Codex entry without `--project`. Expected: the worktree case prints nothing, because the folder isn't the Agent Mail project.
- **DRIVABILITY**: store from the env's `XDG_CONFIG_HOME`, Agent Mail URL from the config.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

### Closure Test: A Codex agent hears about one message once, across the hook and the nudge [BLOCKING]

Behavior 18.
- **SOURCE (seed only)**: messages on the fake server, a Codex screen agent spawned with `spawnNudgeAgent(..., "--cod=1")`.
- **TRIGGER (start)**: the written Codex hook command, and `internal-monitor` started by spawn (boundary = highest_new_connector).
- **DRIVERS (async edges)**: the monitor's poll, waited on with `waitTypedCount`. The hook runs synchronously.
- **OBSERVE (assert via)**: the hook's stdout and the text typed into the pane.
- **FORBIDDEN SPAN**: `ClaimToldRecord`, `mailToAnnounce`, the store.
- **RED-AT-SEAM proof**: the hook skips `claimUnread` and tells every unread message. Expected: "message 91 announced 2 times".
- **DRIVABILITY**: `human_typing_quiet_seconds` and the poll interval from the env config, as in Loop 5.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/` on the private tmux server.

### Closure Test: When the Codex hook can't run, the agent still gets the Idle Nudge [BLOCKING]

Behavior 19.
- **SOURCE (seed only)**: one message, and a `CODEX_HOME/config.toml` that can't be parsed, so spawn saves no approval and the written hook can't run.
- **TRIGGER (start)**: `Execute` with `spawn <s> --cod=1`, which starts `internal-monitor` (boundary = outermost).
- **DRIVERS (async edges)**: the monitor's poll, waited on with `waitTyped`.
- **OBSERVE (assert via)**: the reminder typed into the Codex pane.
- **FORBIDDEN SPAN**: the nudge internals, `nudgeTargets`.
- **RED-AT-SEAM proof**: make the nudge skip panes whose folder has a Codex hook. Expected: "no reminder typed within 20s".
- **DRIVABILITY**: as in Loop 5.
- **EXECUTION (must run)**: as above.

## Sprint 1: Hook written and approved

**Bead:** `bd-7yrae.1`.

**Testable function:** "Spawn a Codex agent, see the hook file written and approved, and see Codex start without stopping at its hook review screen."

### Evidence check at sprint start

Run this before Behavior 1, and record the answers below this list. If Codex hooks can't make the agent continue, stop and bring it to you before building: that changes Sprint 2.
1. **Project hook file.** Confirm the path (`.codex/hooks.json`), its format and the turn-end event name (`Stop`). Write a test hook that logs its stdin, start Codex in a scratch repo, and end a turn.
2. **Stop input and continue.** Confirm the stdin fields, and that `{"decision":"block","reason":"..."}` makes Codex continue with the reason. Check what Codex does with empty output and with exit 0.
3. **Approval.** Approve the hook with `/hooks`. Record what changes in `~/.codex/config.toml`: the `hooks.state` key, and what `trusted_hash` hashes. Change the command, then try a new worktree, and record whether each needs approving again.
4. **Reading approval.** Can ntm read approval state without changing it, and can it compute the hash for an entry it wrote? If not, Behavior 11 always returns "couldn't check".
5. **Project config.** Does `.codex/hooks.json` interfere with `.codex/config.toml` or other Codex settings? Must the project be trusted (`projects.<path>.trust_level`) before Codex reads `.codex/` at all? If it must, the notice includes that step.
6. **Hook environment.** Does the hook process get `AGENT_NAME`, `TMUX_PANE` and `MCP_AGENT_MAIL_PROJECT` from the Codex process? If `AGENT_NAME` is stripped, Sprint 2's identity falls back to the breadcrumb, and Behavior 15 tests that path.

Copy `stop.command.output` from the binary's strings into `internal/cli/testdata/codex_stop_output_schema.json`, and save the approval `config.toml` (scrubbed) as fixtures in `internal/claudeconfig/testdata/`.

### Evidence record (2026-11-10, Codex 0.167.0 on this VPS)

**Setup.** A scratch git repo in the session scratchpad, with `.codex/hooks.json` holding one Stop entry. The entry pointed at a script that logs its stdin, folder and environment, and returns `block` once. Codex ran in tmux as plain `codex` with its normal sandbox. Starting it with `--dangerously-bypass-approvals-and-sandbox`, ntm's real flag, was refused by this session's permission check, so that flag's effect on the dialogs below is unconfirmed. `~/.codex/config.toml` was backed up first.

1. **Hook file.** Confirmed: Codex lists the entry under Stop, with the source "Project config - <repo>/.codex/hooks.json", Mode Sync and Timeout 5s. Claude's handler shape works unchanged.
2. **Input and continue.**
   - Stdin has `session_id`, `turn_id`, `transcript_path`, `cwd`, `hook_event_name: "Stop"`, `model`, `permission_mode`, `stop_hook_active` and `last_assistant_message`.
   - `{"decision":"block","reason":"..."}` made Codex show "Blocked by hook" with the reason and carry on. The next stop came with `stop_hook_active: true`.
   - Empty output with exit 0 let the agent stop.
   - The hook runs in the repo folder.
3. **Approval.**
   - Before an untrusted project's `.codex/` is read, Codex asks "Trust this folder?" and saves `[projects."<path>"] trust_level = "trusted"`.
   - A new or changed hook then stops Codex at startup with a "Hooks need review" screen: 1. Review hooks, 2. Trust all and continue, 3. Continue without trusting (hooks won't run).
   - Trusting saves `[hooks.state."<abs path>/.codex/hooks.json:stop:<group>:<handler>"] trusted_hash = "sha256:<hex>"`. It takes effect at once, in the same session.
   - Changing the command brought the review screen back.
   - "Continue without trusting" saved nothing: the hook didn't run, and `/hooks` still shows it as needing review.
   - A worktree's `hooks.json` has a different path, so it has a different key and needs its own approval.
4. **Reading approval.** Yes, read-only, and the hash can be computed. It is SHA-256 over compact JSON with sorted keys: `{"event_name":"stop","hooks":[{"async":false,"command":"<command>","timeout":5,"type":"command"}]}`, with a `matcher` key only when one is set. This reproduced the saved hash exactly (`c105e293…`). Source: openai/codex `codex-rs/hooks/src/engine/discovery.rs` `hook_hash` and `codex-rs/config/src/fingerprint.rs` `version_for_toml`, found by a helper agent and confirmed by the match. `enabled` counts as on unless it is `false`. The group and handler indexes are part of the key, so moving the ntm group to another position needs approval again.
5. **Project config.** `.codex/hooks.json` is its own file and doesn't touch `.codex/config.toml`. The project has to be trusted before Codex reads `.codex/` at all.
6. **Hook environment.** `AGENT_NAME`, `MCP_AGENT_MAIL_PROJECT`, `TMUX` and `TMUX_PANE` all reach the hook from the Codex process.

**Finding that changes Sprint 1:** an unapproved or changed hook stops Codex at startup on the "Hooks need review" screen. That happens on every fresh spawn until you approve, so text that ntm types at launch, or the Idle Nudge, would land in that menu. Brought to you before building, as the loop document asks.

**Left on this VPS:** the scratch repo's project trust and hook approval entries in `~/.codex/config.toml`. The backup is `<scratchpad>/ev7/codex-config.before.toml`.

**Order of work:** evidence check, then 1, 2, 3, 4, 5, 6, 7, 13, 11, then 8 as the outer test, then 9, 10, 12.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 8, 12 |
| `ntm spawn` (including `--worktrees`), robot spawn and `ntm add` each write one Codex hook entry before launching a Codex agent | 1, 8 |
| The entry has the absolute ntm path and the project key, and no tokens, with the same safe merge as the Claude file: other entries kept, atomic write, no duplicates | 1, 2, 3, 4, 5, 8 |
| The file's path is added to the git exclude file, and `stop_hook_enabled = false` also stops this file being written | 6, 7, 9 |
| Spawn saves Codex's approval for ntm's own entry only, changing nothing else in Codex's config. If it can't, or Codex hasn't trusted the folder yet, the spawn output tells you and says what to do. Robot spawn puts this in `warnings` | 11, 12 |
| ntm's Codex launch command doesn't gain `--dangerously-bypass-hook-trust` | 13 |
| If Codex's config can't be read or written, the approval isn't saved, and the notice says so on that spawn (requirement) | 11, 12 |
| At most one notice per hook file, not one per agent, with the exact steps (UI) | 12 |
| A hook write failure warns and the spawn continues (scope Hook Installation, Failure) | 4, 10 |
| Scenarios: new file, merge, re-run, worktrees, mixed session, approval saved, approval kept, folder not trusted, config unusable, launch command, switch off | 1, 2, 8, 11, 12, 13, 9 |

### Behavior 1: The Codex writer creates `.codex/hooks.json` with one ntm Stop entry [LEAF: one package, synchronous file write]

#### Test Specification

**Given** an empty temp folder. **When** `InstallCodexStopHook(dir, "/opt/ntm bin/ntm", "/home/user/proj")` runs. **Then** `dir/.codex/hooks.json` is:

```json
{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"'/opt/ntm bin/ntm' agent-hook stop --project '/home/user/proj'","timeout":5}]}]}}
```

The result reports `HooksPath`, `Changed: true` and `Excluded: false` (not a repo). The file mode is 0600, and `.codex` is 0700 when created.

**Edge Cases:** a project key with a quote is shell-quoted as `StopHookCommand` does. The command holds no token (the test greps it for `registration_token`, `bearer` and `eyJ`).

**Property:** Roundtrip. For any ntm path and project key built from printable characters, quotes and spaces, the written command, run through `sh -c 'printf "%s\n" "$@"' sh` in place of the binary, gives back the same two paths.

```go
rapid.Check(t, func(rt *rapid.T) {
	ntm := "/" + rapid.StringMatching(`[a-z '"$ ]{1,20}`).Draw(rt, "ntm")
	key := "/" + rapid.StringMatching(`[a-z '"$ ]{1,20}`).Draw(rt, "key")
	dir := t.TempDir()
	if _, err := InstallCodexStopHook(dir, ntm, key); err != nil {
		rt.Fatal(err)
	}
	assertCommandWords(rt, onlyCodexEntry(rt, dir).Command, ntm, key)
})
```

**Files touched:** `internal/claudeconfig/codex_hooks.go` (new: `CodexHooksPath`, `InstallCodexStopHook`, `CodexStopHookResult`), `internal/claudeconfig/claudeconfig.go` (`WriteStopHookFile`, shared by both files), `internal/claudeconfig/codex_hooks_test.go` (new).

The new file is genuinely new functionality (another tool's file format and approval state). It sits in `claudeconfig` so it shares the unexported merge and atomic write without exporting them.

#### TDD Cycle

##### 🔴 Red

`TestInstallCodexStopHook_NewFile` and `TestInstallCodexStopHook_CommandRoundTripProperty`. Expected failure: `undefined: InstallCodexStopHook`.

##### 🟢 Green

```go
// CodexHooksPath is Codex's project hook file, relative to the work folder.
const CodexHooksPath = ".codex/hooks.json"

// InstallCodexStopHook writes the ntm Stop entry into workDir's Codex hook
// file and keeps that file out of git status. Codex reads the same hook shape
// as Claude Code, so the merge is shared.
func InstallCodexStopHook(workDir, ntmPath, projectKey string) (CodexStopHookResult, error) {
	path := filepath.Join(workDir, filepath.FromSlash(CodexHooksPath))
	changed, err := WriteStopHookFile(path, StopHookCommand(ntmPath, projectKey))
	result := CodexStopHookResult{HooksPath: path, Changed: changed}
	if err != nil {
		return result, err
	}
	result.Excluded, err = git.EnsureExcluded(workDir, CodexHooksPath)
	return result, err
}
```

`WriteStopHook` becomes `WriteStopHookFile(filepath.Join(workDir, LocalSettingsPath), command)`, so the read, parse, merge, compare and atomic write live once.

##### 🔵 Refactor

- [ ] **No duplication:** one `WriteStopHookFile` for both tools. The home-folder check (Behavior 5) and the tracked check (Behavior 7) live in one guard used by both installers where they apply.
- [ ] **Reveals intent:** the doc comment says why Codex shares Claude's merge.
- [ ] **Complexity went down:** `WriteStopHook` shrinks to one line.
- [ ] **No shallow wrappers:** `WriteStopHook` is kept only if callers other than `InstallStopHook` use it. Otherwise it's inlined.
- [ ] **Fits existing patterns:** `InstallStopHook` (`claudeconfig.go:370`).

#### Success Criteria

**Automated:** `go test -short -run TestInstallCodexStopHook ./internal/claudeconfig/` fails first, then passes. The existing `TestWriteStopHook*` and `TestInstallStopHook*` tests still pass.

### Behavior 2: The merge keeps everything else, and a re-run doesn't write [LEAF: one package]

#### Test Specification

**Given** `.codex/hooks.json` holding a top-level `"description"`, a `PreToolUse` group with a matcher, and a Stop group with another tool's command. **When** the writer runs twice. **Then**:
- All three are kept, in place.
- Stop has the other group plus one ntm group, appended.
- The second run reports `Changed: false`, and the file's modification time doesn't move.

**Edge Cases:** an empty file and a JSON `null` are treated as `{}`. A Stop group that held only ntm entries is dropped before the new one is added.

**Property:** Idempotent. For any generated hooks object (random events, groups and non-ntm commands), `merge(merge(x)) == merge(x)`, the result has exactly one ntm Stop entry, and every non-ntm handler in `x` is still there.

```go
rapid.Check(t, func(rt *rapid.T) {
	settings := genHooksSettings(rt) // reuses the Claude merge test generator
	once, err := mergeStopHook(settings, cmd)
	if err != nil {
		rt.Fatal(err)
	}
	twice, _ := mergeStopHook(once, cmd)
	if !sameJSON(once, twice) || countNtmStop(once) != 1 || !keepsOthers(settings, once) {
		rt.Fatalf("merge not idempotent or lost entries: %v", once)
	}
})
```

**Files touched:** `internal/claudeconfig/codex_hooks_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestInstallCodexStopHook_MergeKeepsOthers` and `TestInstallCodexStopHook_IdempotentProperty`. They fail until Behavior 1's shared writer handles the Codex path. If they pass at once after Behavior 1, record that in the build record and keep them as guards.

##### 🟢 Green

No new code beyond Behavior 1 is expected. Any gap goes into `mergeStopHook`, never into a Codex-only copy.

##### 🔵 Refactor

- [ ] **No duplication:** the generator is shared with the Claude merge tests.
- [ ] **Reveals intent:** test names say what's kept.
- [ ] **Complexity went down:** no Codex branch in the merge.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `claudeconfig_test.go` merge tests.

#### Success Criteria

**Automated:** `go test -short -run 'TestInstallCodexStopHook_(Merge|Idempotent)' ./internal/claudeconfig/` passes.

### Behavior 3: An old ntm entry naming another binary is replaced [LEAF: one package]

#### Test Specification

**Given** `.codex/hooks.json` with an ntm entry for `/old/ntm agent-hook stop --project /p`, and one for `'/tmp/ntm x' agent-hook stop`. **When** the writer runs with `/new/ntm`. **Then** both old entries are gone, and there is one entry for `/new/ntm`. A command that only contains the words, like `echo agent-hook stop`, is kept.

**Edge Cases:** `IsStopHookCommand` decides, as for Claude.

**Property:** no property: fixed inputs. Behavior 2's property covers the general case.

**Files touched:** `internal/claudeconfig/codex_hooks_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestInstallCodexStopHook_ReplacesOldBinary`.

##### 🟢 Green

Shared merge, no new code expected.

##### 🔵 Refactor

- [ ] **No duplication:** `IsStopHookCommand` only.
- [ ] **Reveals intent:** the kept look-alike command is named in the test.
- [ ] **Complexity went down:** none added.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the Claude replace test.

#### Success Criteria

**Automated:** `go test -short -run TestInstallCodexStopHook_ReplacesOldBinary ./internal/claudeconfig/` passes.

### Behavior 4: A file that can't be parsed is left unchanged [LEAF: one package]

#### Test Specification

**Given** `.codex/hooks.json` holding each of: `{bad`, `[]`, `{"hooks":[]}`, `{"hooks":{"Stop":{}}}`, `{"hooks":{"Stop":[1]}}`. **When** the writer runs. **Then** `errors.Is(err, ErrUnparseableSettings)`, the error names the path, and the bytes are unchanged.

**Edge Cases:** `.codex` is a regular file: the error names it, and nothing is created.

**Property:** Invariant. For any byte string that isn't a JSON object of the expected shape, the file's bytes are unchanged after the writer runs.

```go
rapid.Check(t, func(rt *rapid.T) {
	raw := rapid.SliceOf(rapid.Byte()).Filter(func(b []byte) bool { return !parsesAsHooksObject(b) }).Draw(rt, "raw")
	dir := writeCodexHooks(t, raw)
	_, _ = InstallCodexStopHook(dir, "/ntm", "/p")
	assertBytes(rt, dir, raw)
})
```

**Files touched:** `internal/claudeconfig/codex_hooks_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestInstallCodexStopHook_UnparseableUnchanged` and `TestInstallCodexStopHook_UnparseableProperty`.

##### 🟢 Green

Shared `WriteStopHookFile`. The error message says "Codex hook file" when the path ends in `CodexHooksPath`, so the warning names the right tool.

##### 🔵 Refactor

- [ ] **No duplication:** one parse path.
- [ ] **Reveals intent:** the error names the file and the bad field.
- [ ] **Complexity went down:** none added.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `ErrUnparseableSettings`.

#### Success Criteria

**Automated:** `go test -short -run TestInstallCodexStopHook_Unparseable ./internal/claudeconfig/` passes.

### Behavior 5: The home folder is refused [LEAF: one package]

#### Test Specification

**Given** `HOME` set to a temp folder. **When** `InstallCodexStopHook(home, ...)` runs, also with a trailing slash. **Then** `errors.Is(err, ErrHomeFolder)`, and `home/.codex/hooks.json` doesn't exist. That file is your global Codex hook file.

**Edge Cases:** a folder under home (`~/Dev/x`) is allowed.

**Property:** no property: two fixed paths.

**Files touched:** `internal/claudeconfig/codex_hooks.go`, `internal/claudeconfig/claudeconfig.go` (the shared guard), `internal/claudeconfig/codex_hooks_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestInstallCodexStopHook_RefusesHomeFolder`. Expected failure: the file is created in home.

##### 🟢 Green

Move the home check from `InstallStopHook` into `refuseHomeFolder(workDir) error`, called by both installers. `ErrHomeFolder`'s text becomes "refusing to write an agent Stop hook into the home folder".

##### 🔵 Refactor

- [ ] **No duplication:** one home check.
- [ ] **Reveals intent:** the comment names both global files it protects.
- [ ] **Complexity went down:** `InstallStopHook` loses its inline check.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `ErrHomeFolder`.

#### Success Criteria

**Automated:** `go test -short -run 'Test.*HomeFolder' ./internal/claudeconfig/` passes.

### Behavior 6: The Codex file is added to the git exclude file once [LEAF: one package, real git]

#### Test Specification

**Given** a new git repo. **When** the writer runs twice. **Then** `.codex/hooks.json` appears once in `info/exclude`, `git status --short` is empty, and `Excluded` is true. In a linked worktree, the shared exclude file gets the line. Outside git, `Excluded` is false with no error.

**Edge Cases:** an existing `.claude/settings.local.json` exclude line is kept.

**Property:** no property: fixed repo layouts.

**Files touched:** `internal/claudeconfig/codex_hooks_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestInstallCodexStopHook_ExcludedFromGit`.

##### 🟢 Green

`git.EnsureExcluded(workDir, CodexHooksPath)` in Behavior 1's code.

##### 🔵 Refactor

- [ ] **No duplication:** `EnsureExcluded` only.
- [ ] **Reveals intent:** the test names the worktree case.
- [ ] **Complexity went down:** none added.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the Claude exclude test.

#### Success Criteria

**Automated:** `go test -short -run TestInstallCodexStopHook_ExcludedFromGit ./internal/claudeconfig/` passes.

### Behavior 7: A committed `.codex/hooks.json` is left alone, with a reason [LEAF: one package, real git]

#### Test Specification

**Given** a repo that commits `.codex/hooks.json`. **When** the writer runs. **Then**:
- It returns `ErrCodexHooksTracked`, whose text is "<path> is committed to git; ntm leaves it alone. Add the ntm Stop entry yourself, or rely on the idle reminder".
- The bytes are unchanged, and `git status --short` is empty.
- No exclude line is added.

**Given** a file that is only staged (added, not committed). **Then** it's also tracked, and is left alone.

**Edge Cases:** `git.IsTracked` outside a repo returns false with no error. A git failure returns the error, and nothing is written.

**Property:** no property: fixed repo states.

**Files touched:** `internal/git/root.go` (`IsTracked(dir, path) (bool, error)`, running `git -C dir ls-files --error-unmatch -- path`), `internal/git/root_test.go`, `internal/claudeconfig/codex_hooks.go`, `internal/claudeconfig/codex_hooks_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestIsTracked` (committed, staged, untracked, ignored, not a repo) and `TestInstallCodexStopHook_TrackedFileUntouched`. Expected failure: `undefined: git.IsTracked`.

##### 🟢 Green

`InstallCodexStopHook` checks `git.IsTracked(workDir, CodexHooksPath)` before writing. Exit code 1 from `ls-files --error-unmatch` means "not tracked". Exit code 128 outside a repo means "not tracked". Anything else is an error.

##### 🔵 Refactor

- [ ] **No duplication:** `IsTracked` follows `EnsureExcluded`'s git call style, with a shared `gitDirCommand` helper if both need the same timeout setup.
- [ ] **Reveals intent:** the error tells you what to do.
- [ ] **Complexity went down:** one check before the write.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `EnsureExcluded` (`internal/git/root.go:33`).

#### Success Criteria

**Automated:** `go test -short -run 'TestIsTracked|TestInstallCodexStopHook_TrackedFileUntouched' ./internal/git/ ./internal/claudeconfig/` passes.

### Behavior 13: ntm's Codex command has no hook-trust bypass [LEAF: string build]

#### Test Specification

**Given** the default config, and a config with a custom Codex model and reasoning effort. **When** the Codex launch command is built for the CLI spawn path and for robot spawn's `agentCommands["codex"]`. **Then** neither contains `--dangerously-bypass-hook-trust` or `bypass_hook_trust`.

**Edge Cases:** if you put the flag in your own `agents.codex` command, ntm passes it unchanged. ntm doesn't add it or remove it, and this test documents that.

**Property:** Invariant. For any model and effort strings, the built default command never contains the bypass flag.

```go
rapid.Check(t, func(rt *rapid.T) {
	model := rapid.StringMatching(`[a-z0-9.-]{1,20}`).Draw(rt, "model")
	cmd := buildDefaultCodexCommand(t, model, "medium")
	if strings.Contains(cmd, "bypass-hook-trust") || strings.Contains(cmd, "bypass_hook_trust") {
		rt.Fatalf("command has the bypass flag: %s", cmd)
	}
})
```

**Files touched:** `internal/config/templates_test.go` (or the existing test file for agent templates), `internal/robot/spawn_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestCodexCommandNoHookTrustBypass`. This guards a property that is already true, so it passes at once. The build record shows it red by adding the flag to the template temporarily, then reverting.

##### 🟢 Green

No production change.

##### 🔵 Refactor

- [ ] **No duplication:** uses the production template builder.
- [ ] **Reveals intent:** the test comment cites the scope decision "Codex hooks approved manually".
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** existing template tests.

#### Success Criteria

**Automated:** `go test -short -run TestCodexCommandNoHookTrustBypass ./internal/config/ ./internal/robot/` passes, and was shown red once.

### Behavior 11: ntm approves its own Codex entry, and nothing else [LEAF: one package, real files]

#### Test Specification

**Given** `CODEX_HOME` holding each fixture, and a written `.codex/hooks.json` whose ntm group is at index g. **When** `ApproveCodexStopHook(codexHome, hooksPath, command)` runs. **Then**:

| Fixture | Result | `config.toml` afterwards |
|---|---|---|
| No `config.toml` | `ApprovalGranted`, written | new file, 0600, holding only `[hooks.state."<hooksPath>:stop:<g>:0"]` and `trusted_hash` |
| Your settings and comments, another hook's state | `ApprovalGranted`, written | every original byte kept, and the ntm table appended |
| ntm's key already has this hash | `ApprovalGranted`, not written | unchanged bytes and modification time |
| ntm's key has an older hash | `ApprovalGranted`, written | only that table's `trusted_hash` line replaced |
| ntm's key has `enabled = false` | `ApprovalDisabled`, not written | unchanged: you turned it off |
| `config.toml` doesn't parse | `ApprovalUnknown` with the parse error, not written | unchanged |
| `config.toml` can't be written | `ApprovalUnknown` with the error | unchanged |

The result also reports `FolderTrusted`, from `projects."<folder>".trust_level == "trusted"`.

`codexHookKey(hooksPath, g)` is `<hooksPath>:stop:<g>:0`. `codexHookHash(command)` is `"sha256:" + hex(sha256(json))`, where json is the compact JSON with sorted keys `{"event_name":"stop","hooks":[{"async":false,"command":<command>,"timeout":5,"type":"command"}]}`. That json is encoded without HTML escaping, as serde_json does.

**Edge Cases:**
- `CODEX_HOME` unset means `~/.codex`.
- The written result is decoded again before the atomic rename. If it doesn't decode, or ntm's key isn't there with the hash, nothing is renamed.
- A command with `<`, `>` or `&` hashes the same as Codex does (golden value from a real `/hooks` approval in the build record).

**Property:**
- Oracle: `codexHookHash` matches the evidence fixture (`c105e293…`) and a second real approval recorded in the build.
- Invariant: for any generated `config.toml` that parses (random tables, comments and `hooks.state` entries), the file after approval minus ntm's table equals the original, and `CodexHookApproval` then returns `ApprovalGranted`.
- Idempotent: a second run doesn't write.

```go
rapid.Check(t, func(rt *rapid.T) {
	raw := genCodexConfig(rt) // valid TOML: random tables, comments, hooks.state entries
	home := writeCodexConfig(t, raw)
	got, err := ApproveCodexStopHook(home, hooksPath, command)
	if err != nil || got.State != ApprovalGranted {
		rt.Fatalf("approve: %v %v", got, err)
	}
	assertOnlyNtmTableAdded(rt, raw, readConfig(t, home), codexHookKey(hooksPath, 0))
	if again, _ := ApproveCodexStopHook(home, hooksPath, command); again.Changed {
		rt.Fatal("second approval wrote")
	}
})
```

**Files touched:** `internal/claudeconfig/codex_hooks.go` (`CodexApproval`, `CodexHookApproval`, `ApproveCodexStopHook`, `codexHookKey`, `codexHookHash`, `codexHomeDir`), `internal/claudeconfig/codex_hooks_test.go`, `internal/claudeconfig/testdata/codex_config_*.toml` (fixtures from the evidence run, with paths changed to `/home/user/...`).

#### TDD Cycle

##### 🔴 Red

`TestCodexHookHash_MatchesCodex`, `TestApproveCodexStopHook` (table), `TestApproveCodexStopHook_KeepsOtherBytesProperty`. Expected failure: `undefined: ApproveCodexStopHook`.

##### 🟢 Green

- `CodexHookApproval` decodes `config.toml` with `BurntSushi/toml` and compares ntm's key's `trusted_hash` with `codexHookHash(command)`. It never writes.
- `ApproveCodexStopHook` calls it first and returns at once when the state is granted, disabled or unknown.
- Otherwise it edits the text, not a re-encoded TOML, so comments and order survive. If the `[hooks.state."<key>"]` table exists, it replaces or adds the `trusted_hash` line inside that table. If not, it appends the table at the end.
- It decodes the result to check it, then writes it atomically with the file's existing mode (0600 for a new file).
- The group index g comes from the written `hooks.json`: the position of the group holding ntm's entry.

##### 🔵 Refactor

- [ ] **No duplication:** one key builder and one hash function, each citing the evidence record.
- [ ] **Reveals intent:** the states are named, not booleans.
- [ ] **Complexity went down:** one decode to check, one text edit, one decode to verify.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `writeSettingsAtomic` (`claudeconfig.go:215`) for the atomic write.

#### Success Criteria

**Automated:** `go test -short -run 'TestCodexHookHash|TestApproveCodexStopHook|TestCodexHookApproval' ./internal/claudeconfig/` fails first, then passes.

### Behavior 8: Every spawn path writes one Codex hook entry before launch [BLOCKING]

#### Test Specification

**Given** the closure env with stand-in agents. **When** each path below runs. **Then**:

| Path | Result at launch |
|---|---|
| `spawn codexhook --cod=1` in a repo with an existing `.codex/hooks.json` | one ntm Stop entry for `helperBinary(t)` and the repo, other entries kept, no token, not in `git status` |
| the same, killed and spawned again | still one entry, one exclude line |
| `spawn wt --cod=2 --worktrees` | each worktree's `.codex/hooks.json` has one entry with the main repo as `--project` |
| `--robot-spawn=mixed --spawn-cc=1 --spawn-cod=1` | `.claude/settings.local.json` and `.codex/hooks.json` each have one entry |
| `add codexhook --cod=1` | the added pane's folder has one entry |

**Edge Cases:** a Claude-only spawn writes no `.codex` folder. A Codex-only spawn writes no `.claude` folder.

**Property:** no property: fixed spawn runs.

**Files touched:**
- `internal/cli/agent_hook.go`: `installStopHookIfActive(c, agentType, workDir, projectKey) *codexApprovalNotice` replaces `installClaudeStopHookIfActive`.
- `internal/cli/spawn.go:2352` and `internal/cli/add.go:751`: call it for Claude and Codex.
- `internal/robot/spawn.go`: `writeStopHook(agentType, dir)` replaces `writeClaudeStopHook`, and is called in the Codex loop (`:650`).
- `internal/cli/agent_hook_test.go`: `TestClosure_SpawnWritesCodexStopHookBeforeLaunch`, `TestClosure_WorktreeSpawnCodexHooksUseMainProject`, `TestClosure_RobotSpawnWritesBothHookFiles`, `TestClosure_AddedCodexAgentGetsHook`. The stand-in capture gains `CodexHooks`.

#### TDD Cycle

##### 🔴 Red

The four closure tests. Expected failure: "codex hooks at launch = nil, want one ntm Stop entry".

##### 🟢 Green

```go
// installStopHookIfActive writes the turn-end hook into an agent's work folder
// before launch, in the file that agent's tool reads at startup.
func installStopHookIfActive(c *config.Config, agentType AgentType, workDir, projectKey string) *codexApprovalNotice {
	if c == nil || !c.AgentMail.StopHookActive() {
		return nil
	}
	switch agentType {
	case AgentTypeClaude:
		installClaudeStopHook(workDir, projectKey)
		return nil
	case AgentTypeCodex:
		return installCodexStopHook(workDir, projectKey)
	}
	return nil
}
```

`installCodexStopHook` prints "Codex Stop hook written: <path>" (text mode), warns on failure as the Claude path does, and returns the approval notice for Behavior 12.

##### 🔵 Refactor

- [ ] **No duplication:** the ntm path lookup and the warning wording are shared by both tools.
- [ ] **Reveals intent:** call sites read `installStopHookIfActive(cfg, agent.Type, ...)`, with no type check.
- [ ] **Complexity went down:** the `if agent.Type == AgentTypeClaude` guards are gone from spawn and add.
- [ ] **No shallow wrappers:** `installClaudeStopHookIfActive` is removed, not kept as a wrapper.
- [ ] **Fits existing patterns:** the existing call sites and their comments.

#### Success Criteria

**Automated:** `go test -short -run 'TestClosure_.*Codex.*Hook|TestClosure_RobotSpawnWritesBothHookFiles' ./internal/cli/` fails first, then passes. `TestClosure_RobotSpawnWritesClaudeStopHook` seeds folder trust in `CODEX_HOME`, so its `warnings` stay empty.

### Behavior 9: The hook switch stops the Codex file and the notice [BLOCKING]

#### Test Specification

**Given** `stop_hook_enabled = false`. **When** `spawn s --cod=1` and `--robot-spawn=s2 --spawn-cod=1` run. **Then** no `.codex/hooks.json` exists at launch, there's no "Codex Stop hook" line in the output, and `warnings` is empty.

**Edge Cases:** an existing ntm entry is left in place, as for Claude. The hook command itself does nothing while the switch is off (Loop 3).

**Property:** no property: fixed config.

**Files touched:** `internal/cli/agent_hook_test.go` (extend `TestClosure_StopHookSwitchOffWritesNothing` with Codex cases).

#### TDD Cycle

##### 🔴 Red

The Codex cases of `TestClosure_StopHookSwitchOffWritesNothing`. They fail only if Behavior 8 skipped the switch. The build record shows the red-at-seam check.

##### 🟢 Green

The switch check at the top of `installStopHookIfActive`, and in robot spawn's Codex loop.

##### 🔵 Refactor

- [ ] **No duplication:** one switch check per path.
- [ ] **Reveals intent:** the test names both paths.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the Claude switch test.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_StopHookSwitchOffWritesNothing ./internal/cli/` passes.

### Behavior 10: A failed Codex hook write warns, and the agent still launches [BLOCKING]

#### Test Specification

**Given** three repos: one with `{bad` in `.codex/hooks.json`, one where `.codex` is a file, and one that commits `.codex/hooks.json`. **When** `spawn s --cod=1` runs in each (and `--robot-spawn` in the first). **Then**:
- The agent launches.
- The output has one warning: "Codex Stop hook not written in <dir>: <reason>". Robot spawn puts that text in `warnings`.
- The bad and committed files are unchanged, and `git status --short` stays empty for the committed one.
- No approval notice is shown for a file that wasn't written.

**Edge Cases:** `--json` spawn output stays one JSON object, with the warning logged, as for Claude.

**Property:** no property: fixed failures.

**Files touched:** `internal/cli/agent_hook.go`, `internal/robot/spawn.go`, `internal/cli/agent_hook_test.go` (`TestClosure_SpawnCodexHookFailuresStillLaunch`).

#### TDD Cycle

##### 🔴 Red

`TestClosure_SpawnCodexHookFailuresStillLaunch`.

##### 🟢 Green

`installCodexStopHook` warns and returns nil on error. The robot `writeStopHook` returns the warning.

##### 🔵 Refactor

- [ ] **No duplication:** one warning format for both tools.
- [ ] **Reveals intent:** the tracked-file warning tells you what to do.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestClosure_SpawnStopHookBadSettingsStillLaunches` (`agent_hook_test.go:1198`).

#### Success Criteria

**Automated:** `go test -short -run TestClosure_SpawnCodexHookFailuresStillLaunch ./internal/cli/` passes.

### Behavior 12: Spawn approves its Codex hook, and tells you once per file when it couldn't [BLOCKING]

#### Test Specification

**Given** the closure env with `CODEX_HOME` fixtures. **When** each run below happens. **Then**:

| Run and fixture | Approval afterwards | Output |
|---|---|---|
| `spawn s --cod=2`, folder trusted, your settings present | granted for the one hook file, your settings kept | no notice |
| the same, spawned again | unchanged file | no notice |
| `spawn s --cod=2`, folder not trusted | granted | one notice: Codex will ask you to trust `<folder>` the first time; accept it |
| `spawn wt --cod=2 --worktrees` | granted for each worktree's file | one notice block listing the worktree folders Codex hasn't trusted |
| `--robot-spawn=s --spawn-cod=2`, folder trusted | granted | `warnings` empty |
| `--robot-spawn=s --spawn-cod=2`, `config.toml` can't be parsed | not saved, file unchanged | one `warnings` entry naming the file and the reason |
| `add s --cod=1`, no `config.toml` | granted, new file | notice about folder trust |
| ntm's entry has `enabled = false` | unchanged | one notice: you turned ntm's Codex hook off in `/hooks`; turn it on there to get turn-end reminders |

Notice texts (text mode):

```
Codex will ask you to trust <folder> before ntm's turn-end hook can run there. Accept it when you attach.
Codex hook not approved in <folder>: <reason>. Codex will show its "Hooks need review" screen; choose "Trust all and continue".
```

**Edge Cases:** a Claude-only spawn never touches `CODEX_HOME`. With `--json`, the notice goes to the log and the JSON output stays clean.

**Property:** no property: fixed spawn runs.

**Files touched:** `internal/cli/agent_hook.go` (`installCodexStopHook`, `codexApprovalNotice`, `codexApprovalNotices`), `internal/cli/spawn.go` and `internal/cli/add.go` (collect, then print once after the launches), `internal/robot/spawn.go` (approval, dedupe by file), `internal/cli/agent_hook_test.go` (`TestClosure_SpawnApprovesCodexHook`, `codex-approval` helper mode).

#### TDD Cycle

##### 🔴 Red

`TestClosure_SpawnApprovesCodexHook`. Expected failure: "approval for <file> = needs approval, want granted".

##### 🟢 Green

`installCodexStopHook` calls `claudeconfig.ApproveCodexStopHook` after a successful write. It returns a notice when the state isn't granted, or when the folder isn't trusted. Spawn keeps the notices in a map keyed by hook path, and prints them sorted after the launch loop. Robot spawn appends each file's text to `warnings` once.

##### 🔵 Refactor

- [ ] **No duplication:** one function builds the notice text for the CLI and robot paths.
- [ ] **Reveals intent:** each notice says what you'll see and what to choose.
- [ ] **Complexity went down:** one collector, not a flag per agent.
- [ ] **No shallow wrappers:** the collector is a small map type with `add` and `lines`, not a wrapper around print.
- [ ] **Fits existing patterns:** spawn's existing end-of-run summary output.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_SpawnApprovesCodexHook ./internal/cli/` fails first, then passes.

### Build record (2026-11-10)

Base commit `66662aa7`. All 13 behaviors are built.

**Code:**
- `internal/claudeconfig/codex_hooks.go` (new):
  - The writer: `InstallCodexStopHook`.
  - The approval: `CodexHookApproval`, `ApproveCodexStopHook`, `codexHookKey`, `codexHookHash`.
  - `InstallAndApproveCodexStopHook`, the notice text, and `CodexHomeDir`.
- `internal/claudeconfig/claudeconfig.go`: `WriteStopHookFile` and `decodeSettings` are shared by both tools, and `refuseHomeFolder` is shared by both installers.
- `internal/git/root.go`: `IsTracked`.
- `internal/cli/agent_hook.go`: `installStopHookIfActive` replaces `installClaudeStopHookIfActive`. It has a Codex branch, plus the `codexHookNotices` collector.
- `internal/cli/spawn.go` and `internal/cli/add.go`: they call it for every agent type, and print the notices once after the launch loop.
- `internal/robot/spawn.go`: `writeStopHook(agentType, dir)` replaces `writeClaudeStopHook` and also runs in the Codex loop. `appendNewWarnings` reports each notice once.
- **Test isolation:** both `TestMain`s set `CODEX_HOME` to a temp folder (`TestMain_IsolatesCodexHome` checks it), and the closure env sets its own `CODEX_HOME`. No test can write the real `~/.codex/config.toml`.

**Tests:**
- Unit: `TestInstallCodexStopHook_*` (Behaviors 1 to 7), `TestIsTracked`, `TestCodexHookHash_MatchesCodex`, `TestApproveCodexStopHook`, `TestApproveCodexStopHook_KeepsOtherBytesProperty`, `TestCodexHookApproval_ReadOnly`, `TestCodexHomeDir` (Behavior 11), `TestCodexCommandNoHookTrustBypass` and `TestRobotCodexCommandNoHookTrustBypass` (Behavior 13), and `TestInstallStopHookIfActive_CodexOffThenOn` (Behavior 9).
- Closure: `TestClosure_SpawnWritesCodexStopHookBeforeLaunch`, `TestClosure_WorktreeSpawnCodexHooksUseMainProject`, `TestClosure_RobotSpawnWritesBothHookFiles`, `TestClosure_AddedCodexAgentGetsHook` (Behavior 8), the Codex cases of `TestClosure_StopHookSwitchOffWritesNothing` (Behavior 9), `TestClosure_SpawnCodexHookFailuresStillLaunch` (Behaviors 7 and 10), and `TestClosure_SpawnApprovesCodexHook` (Behavior 12).
- Changed: `TestClosure_RobotSpawnWritesClaudeStopHook` now seeds Codex folder trust, so its empty-`warnings` check still holds with a Codex agent.

**Hash oracle:** two real approvals from Codex 0.167.0. The second command (`'/tmp/ntm x' agent-hook stop --project '/p&q<r>'`, hash `c6187086…`) proves the JSON is encoded without HTML escaping, as Codex does.

**Red-at-seam, each observed and then reverted:**
- Spawn reads the approval without saving it. Three closure tests went red with "approval … = needs approval, want granted", and robot spawn gave the "not approved" warning.
- The CLI Codex branch returns early. Three closure tests went red with "ntm Stop entries = [], want exactly 1".
- Robot spawn ignores `stop_hook_enabled`. "codex robot" went red with an unexpected trust warning.
- Behavior 13: adding the flag to the template turned `TestCodexCommandNoHookTrustBypass` red.
- Behavior 10's own check (returning the write error) wasn't run on its own. Its closure test asserts the launch and the warning directly.

**Differences from the plan:**
- Behavior 3: `echo agent-hook stop` (unquoted) does count as an ntm entry under Loop 3's `IsStopHookCommand`, which matches any first word followed by ` agent-hook stop`. The kept look-alike is `echo 'agent-hook stop'`, as in Loop 3's tests.
- `.codex` is created 0755 by the shared writer, not 0700. The hook file itself is 0600.
- Closure tests read the approval with `claudeconfig.CodexHookApproval` in the test process, the same production read spawn uses, instead of a new helper mode.
- `ApproveCodexStopHook` finds the group and handler position itself, from the written `hooks.json`.
- Behaviors 2, 3 and 6 passed as soon as Behavior 1's shared writer was in. They stay as guards.

**Verification:**
- `go build` ok.
- gofmt lists only the 3 files that were already listed.
- vet ok.
- `-race` ok for `claudeconfig` and `git`.
- `robot` and `config` ok.
- The closure set (`TestClosure_.*Codex|.*StopHook|RobotSpawnWrites`) passed 5 runs in a row.
- `go test -short ./...` failed only on the bd-5zbcm list, plus `TestParityRobotStatusV1Endpoint`. That test passed 3 of 3 alone, and was filed as flaky: bd-d4zvs.
- Tamper check clean.
- golangci-lint, goimports and ubs aren't installed.

**Left behind:** `internal/config/testdata/rapid/TestCodexCommandNoHookTrustBypass/*.fail`, from the Behavior 13 red check. It will be deleted only with your permission.

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

## Sprint 2: Approved Codex agent told at turn end

**Bead:** `bd-7yrae.2`.

**Testable function:** "Send a working Codex agent mail, and see it learn about the mail when its turn ends."

**Order of work:** 14, 16, 17, then 15 as the outer test, then 18 and 19.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 15 |
| After your approval, a working Codex agent learns about new mail at turn end, with sender and subject (**Reminder Format**) | 15 |
| The response uses the form Codex expects, and the Claude output doesn't change | 14, 15, 16 |
| One message is announced once across the hook and nudge (**One Telling**) | 18 |
| When the hook can't run (approval not saved, or the folder not trusted), the Codex agent still gets the Idle Nudge | 19 |
| Fail-open behavior matches Claude's: a problem never blocks a Codex agent (**Fail Open**) | 17 |
| Scenarios: Codex format, Claude format unchanged, loop guard, nothing new, told by nudge, hook can't run | 15, 16, 14, 15, 18, 19 |

### Behavior 14: The hook reads Codex's Stop input, and its loop guard [LEAF: one parser]

#### Test Specification

**Given** Codex's Stop input, saved from the evidence run (with `turn_id`, `model`, `permission_mode`, `transcript_path: null` and `hook_event_name: "Stop"`). **When** `readTurnEndInput` reads it. **Then** it's ok, and `StopHookActive` matches the input. **Given** `stop_hook_active: true` and new mail. **When** `runAgentHookStop` runs. **Then** it prints nothing and makes no Agent Mail call.

**Edge Cases:** `transcript_path` and `last_assistant_message` as `null` parse.

**Property:** Invariant. For any Codex Stop input generated from the schema's fields, with any values, `readTurnEndInput` returns ok and keeps `stop_hook_active`.

```go
rapid.Check(t, func(rt *rapid.T) {
	active := rapid.Bool().Draw(rt, "active")
	raw := genCodexStopInput(rt, active) // fields from stop.command.input
	got, ok := readTurnEndInput(strings.NewReader(raw))
	if !ok || got.StopHookActive != active {
		rt.Fatalf("input %s: ok=%v active=%v", raw, ok, got.StopHookActive)
	}
})
```

**Files touched:** `internal/cli/agent_hook_test.go`, `internal/cli/testdata/codex_stop_input.json`.

#### TDD Cycle

##### 🔴 Red

`TestReadTurnEndInput_CodexStop` and `TestRunAgentHookStop_CodexLoopGuard`. They may pass at once, because unknown fields are already ignored. If they do, the build record says so, and they stay as guards.

##### 🟢 Green

No change expected. If Codex sends a field that breaks parsing, fix the parser for both tools.

##### 🔵 Refactor

- [ ] **No duplication:** the input fixture is shared with Behavior 15.
- [ ] **Reveals intent:** `turnEndInput`'s comment names both tools.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `readTurnEndInput` tests.

#### Success Criteria

**Automated:** `go test -short -run 'TestReadTurnEndInput_CodexStop|TestRunAgentHookStop_CodexLoopGuard' ./internal/cli/` passes.

### Behavior 16: The Claude output doesn't change [LEAF: existing tests guard it]

#### Test Specification

**Given** the Loop 3 cases. **When** the hook runs with Claude input. **Then** the output is byte-for-byte what the Loop 3 tests expect: `{"decision":"block","reason":"..."}` and a newline, with no other keys.

**Edge Cases:** no new keys such as `systemMessage` are added for either tool.

**Property:** no property: the existing golden outputs.

**Files touched:** `internal/cli/agent_hook_test.go` (one golden test on the exact bytes, if none exists).

#### TDD Cycle

##### 🔴 Red

`TestRunAgentHookStop_OutputBytesUnchanged`. It passes at once, as a guard. The build record shows it red by adding a field to `stopDecision`.

##### 🟢 Green

No change.

##### 🔵 Refactor

- [ ] **No duplication:** reuses the existing reminder fixture.
- [ ] **Reveals intent:** the test comment says both tools read this output.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `stopDecision` tests.

#### Success Criteria

**Automated:** `go test -short -run 'TestRunAgentHookStop|TestClosure_StopHook' ./internal/cli/` passes.

### Behavior 17: A Codex hook failure stays silent [LEAF: same Fail Open path as Claude]

#### Test Specification

**Given** Codex's Stop input and `AGENT_NAME=BlueLake`. **When** the hook runs with each of: Agent Mail unreachable, the state store unopenable, no agent name, and input over the size limit. **Then** stdout is empty, the exit code is 0, and `agent-hook.log` has one line naming the reason. If the store fails, the agent is still told, as the Loop 3 rule says.

**Edge Cases:** an unwritable log folder still prints nothing (`TestClosure_StopHookUnwritableLogStaysSilent`).

**Property:** no property: fixed failure cases.

**Files touched:** `internal/cli/agent_hook_test.go` (Codex cases in `TestClosure_StopHookFailsOpen`).

#### TDD Cycle

##### 🔴 Red

The Codex cases pass at once, because the path is shared. The build record notes it.

##### 🟢 Green

No change.

##### 🔵 Refactor

- [ ] **No duplication:** the table gains an input column, not a second test.
- [ ] **Reveals intent:** the case names say Codex.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestClosure_StopHookFailsOpen` (`agent_hook_test.go:676`).

#### Success Criteria

**Automated:** `go test -short -run TestClosure_StopHookFailsOpen ./internal/cli/` passes.

### Behavior 15: An approved Codex hook tells a working agent about new mail [BLOCKING]

#### Test Specification

**Given** a spawned Codex stand-in in a repo and in a worktree, with message 91 from GreenCastle ("deploy plan") to BlueLake on the fake server. **When** the command read from that folder's `.codex/hooks.json` runs with `sh -c`, in the agent's folder, with Codex's Stop input and the environment Codex gives hooks (evidence item 6). **Then**:
- Stdout is one JSON object that passes `testdata/codex_stop_output_schema.json`, with `decision: "block"` and a `reason` holding `[#91] from GreenCastle: deploy plan` and the snooze line.
- A second run prints nothing ("Nothing new").
- If evidence item 6 found `AGENT_NAME` stripped, the same holds through the breadcrumb identity.

**Edge Cases:** the worktree run uses the main repo as the project, from `--project`.

**Property:** no property: fixed runs. The output schema check is the oracle.

**Files touched:** `internal/cli/agent_hook_test.go` (`TestClosure_WrittenCodexHookCommandTellsAgent`, a small JSON Schema check for this one schema: required keys, `additionalProperties: false`, enum for `decision`), `internal/cli/testdata/codex_stop_output_schema.json`.

#### TDD Cycle

##### 🔴 Red

`TestClosure_WrittenCodexHookCommandTellsAgent`. It's red until Sprint 1's writer is in place for the worktree case. The build record shows the red-at-seam check (the entry written without `--project`).

##### 🟢 Green

No production change is expected. If the evidence check found that Codex needs a different shape, this is where the format choice goes, and you're asked first (loop document, Sprint 1 evidence check).

##### 🔵 Refactor

- [ ] **No duplication:** the hook-command runner is shared with `TestClosure_WrittenStopHookCommandTellsAgent` (`agent_hook_test.go:1221`).
- [ ] **Reveals intent:** the test names Codex's schema as the oracle.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** the schema check is a short function, not a new dependency.
- [ ] **Fits existing patterns:** the Claude written-command test.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_WrittenCodexHookCommandTellsAgent ./internal/cli/` passes.

### Behavior 18: A Codex agent hears about one message once, across the hook and the nudge [BLOCKING]

#### Test Specification

**Given** a Codex screen agent spawned with `spawnNudgeAgent(t, env, agents, "codonce", "--cod=1")`, and the monitor running. **When**:
- (a) message 91 arrives while the agent is working, the written Codex hook runs at turn end, and then the agent goes idle;
- (b) message 92 arrives while the agent is idle, the nudge types it, and then the Codex hook runs.

**Then** in (a) the hook tells 91 and no reminder about 91 is typed in the next 3 polls. In (b) the nudge tells 92 and the hook prints nothing.

**Edge Cases:** a new message 93 after (b) is told by whichever runs first.

**Property:** no property: fixed timing scenarios.

**Files touched:** `internal/cli/idle_nudge_test.go` (`TestClosure_CodexToldOnceAcrossHookAndNudge`, following `TestClosure_ToldOnceAcrossHookAndNudge` at `:364`).

#### TDD Cycle

##### 🔴 Red

`TestClosure_CodexToldOnceAcrossHookAndNudge`. It needs Sprint 1's Codex hook file. The build record shows the red-at-seam check.

##### 🟢 Green

No production change expected. The told record is shared.

##### 🔵 Refactor

- [ ] **No duplication:** shares the scenario helper with the Claude test, taking the agent type as a parameter.
- [ ] **Reveals intent:** case names (a) and (b) as above.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestClosure_ToldOnceAcrossHookAndNudge`.

#### Success Criteria

**Automated:** `go test -short -count=5 -run TestClosure_CodexToldOnceAcrossHookAndNudge ./internal/cli/` passes.

### Behavior 19: When the Codex hook can't run, the agent still gets the Idle Nudge [BLOCKING]

#### Test Specification

**Given** `spawn codnudge --cod=1` with a `CODEX_HOME/config.toml` that can't be parsed, so the hook file is written but no approval is saved. **When** message 95 arrives and the agent is idle. **Then** the reminder for 95 is typed into the Codex pane within 20 seconds, once.

**Edge Cases:** the nudge doesn't read Codex approval state at all.

**Property:** no property: fixed scenario.

**Files touched:** `internal/cli/idle_nudge_test.go` (`TestClosure_CodexHookNotRunStillNudged`).

#### TDD Cycle

##### 🔴 Red

`TestClosure_CodexHookNotRunStillNudged`. It may pass once Sprint 1 lands. The build record shows the red-at-seam check.

##### 🟢 Green

No production change expected.

##### 🔵 Refactor

- [ ] **No duplication:** uses `spawnNudgeAgent` and `waitTyped`.
- [ ] **Reveals intent:** the test comment names the 4c and scope rule.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 5 nudge closure tests.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_CodexHookNotRunStillNudged ./internal/cli/` passes.

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

## Sprint verification (every sprint)

```bash
export PATH=$PATH:/usr/local/go/bin
go build ./cmd/ntm
gofmt -l internal/ cmd/
go vet ./internal/claudeconfig/ ./internal/git/ ./internal/robot/ ./internal/cli/
go test -short -race ./internal/claudeconfig/ ./internal/git/
go test -short ./internal/robot/ ./internal/cli/
go test -short -count=5 -run 'TestClosure_.*Codex|TestClosure_.*StopHook|TestClosure_RobotSpawnWrites' ./internal/cli/
go test -short ./...           # compare failures with the known list in bd-5zbcm
bun ~/.claude/SAI/Tools/TestTamperCheck.ts --base <sprint start commit>
```

Before each commit, grep the staged diff for tokens: `registration_token"?:"[^[]|bearer [a-z0-9]|eyJ|maceojourdan`. Fixtures from the evidence run must be scrubbed first. `golangci-lint`, `goimports` and `ubs` are reported as not run if they're still missing.

## Integration & E2E Testing

- **Integration:** the Codex writer and approval reader on real files and real git in `internal/claudeconfig` and `internal/git`.
- **Closure:** Behaviors 8, 9, 10, 12, 15, 18 and 19 in `internal/cli`, with the real spawn, add, robot spawn, monitor, hook command, store and the fake Agent Mail server.
- **E2E:** none new in code. The human test scripts run real Codex. Loop 9's release journey covers Codex end to end.

## Beads

The master index sets the rule: one epic per loop, and one bead per sprint, created at the TDD step.
- Epic: `bd-7yrae`, "Loop 7: Codex turn-end push".
- Sprint 1: `bd-7yrae.1`, "Loop 7 Sprint 1: Codex hook written, and approval notice".
- Sprint 2: `bd-7yrae.2`, "Loop 7 Sprint 2: Approved Codex agent told at turn end". It is blocked by `bd-7yrae.1`.

## References

- Loop document: [07-codex-turn-end-push.md](07-codex-turn-end-push.md)
- Scope: [SCOPE.md](SCOPE.md), processes **Turn-End Push** and **Hook Installation**, Evidence Needed item 1, Appendix C (Codex)
- Research: `thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`, Workflow Closure Map, Chain A
- Loop 3 TDD plan: [03-claude-turn-end-push-tdd.md](03-claude-turn-end-push-tdd.md) (hook writer, spawn call sites, closure harness)
- Loop 5 TDD plan: [05-idle-nudge-tdd.md](05-idle-nudge-tdd.md) (nudge closure harness)
- Patterns:
  - `InstallStopHook`, `WriteStopHook`, `mergeStopHook` (`internal/claudeconfig/claudeconfig.go:370`, `:327`, `:385`)
  - `installClaudeStopHookIfActive`, `runAgentHookStop`, `readTurnEndInput` (`internal/cli/agent_hook.go:94`, `:133`, `:197`)
  - Spawn call sites (`internal/cli/spawn.go:2352`, `internal/cli/add.go:751`)
  - Robot spawn loops and `writeClaudeStopHook` (`internal/robot/spawn.go:640`, `:650`, `:738`)
  - `git.EnsureExcluded` (`internal/git/root.go:33`)
  - Default Codex command (`internal/config/templates.go:325`)
- Codex 0.167 evidence: binary strings for `stop.command.input`, `stop.command.output`, `HookStateToml` (`enabled`, `trusted_hash`), `hooks.state."`, `.codex/hooks.json` and the `/hooks` review text
