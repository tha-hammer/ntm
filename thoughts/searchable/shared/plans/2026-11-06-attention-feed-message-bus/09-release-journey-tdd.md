# Loop 9: Release journey — TDD plan

Loop document: [09-release-journey.md](09-release-journey.md). Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), section 6 (Connected Release Journey, Cross-Process Failure, the five quality targets, deliverable 18).

Epic `bd-upu1q`. Sprint bead `bd-upu1q.1`.

## Overview

Loops 1 to 8 proved each piece with closure tests, each inside one Go test binary. This loop proves the whole path with the real `ntm` binary:
- a message reaches the agent at turn end, for Claude and Codex;
- a message reaches an idle agent as one nudge, and a snoozed one waits for its time;
- a restart loses nothing and repeats nothing;
- an Agent Mail outage blocks nothing, and reminders come back.

It also measures the scope's five quality targets, brings `docs/attention-feed-contract.md` and `--robot-help` in line with the code, and adds a short README section.

There are 11 behaviors in 1 sprint:
- Behaviors 1 to 6 are end-to-end tests in `e2e/`.
- Behaviors 8 to 10 are unit tests.
- Behaviors 7 and 11 are checked by hand and recorded.

## Current State Analysis

- **The e2e harness the loop document names is legacy.** `e2e/scenario_harness.go` and `e2e/scenario_harness_test.go` carry `//go:build e2e && legacy_scenario_harness` (line 1), so `-tags=e2e` doesn't build them. They also don't isolate `HOME`, the XDG folders or `NTM_CONFIG`. The new tests don't use them.
- **e2e builds the binary.** `TestMain` (`e2e/suite_test.go:30`) builds `./cmd/ntm` into a temp folder, or uses `E2E_NTM_BIN`, and puts it first on `PATH`. Tests run `ntm` as a real process. The run command is `go test -tags=e2e ./e2e/`. `make test-e2e` (`Makefile:69`) leaves out the tag, so it builds none of these files.
- **The isolated setup to copy** is `closureEnv` (`internal/cli/attention_closure_test.go:63`), which e2e can't import:
  - a temp root with `config/`, `work/`, `home/` and `data/`;
  - `HOME`, `CODEX_HOME`, `XDG_CONFIG_HOME` and `XDG_DATA_HOME` set, and `NTM_CONFIG` empty;
  - `AGENT_MAIL_URL` pointing at the fake server.
  - `privateTmux` (`internal/cli/agent_hook_test.go:837`) sets `TMUX_TMPDIR` to a short `/tmp/ntmtx*` folder. Its cleanup kills only that server.
- **The fake Agent Mail** is `newFakeAgentMail` (`internal/cli/agent_hook_test.go:30-169`): an `httptest` JSON-RPC server answering `health_check`, `ensure_project`, `register_agent`, `create_agent_identity` and `fetch_inbox`. It checks the issued token. e2e needs its own copy.
- **The stand-in agents** are `screenAgentScript` and `screenAgentConfig` (`internal/cli/idle_nudge_test.go:43-115`). Each is a bash loop that records its env, redraws a screen file, and appends typed lines to `typed`. The idle screens are `internal/cli/testdata/cc_idle.txt` and `cod_idle.txt`.
- **Under `go test`, spawn starts no monitor.** `os.Executable()` is the `.test` binary (`internal/resilience/monitor_launch.go:14`), so closure tests start `internal-monitor` themselves (`startMonitor`, `idle_nudge_test.go:244`). With the real binary, `ntm spawn` starts the detached monitor (`resilience.NewInternalMonitorCommand`, `monitor_launch.go:62`). So the e2e tests cross one connection that no closure test has crossed.
- **Contract doc:**
  - Section 6.3 already says 7 days (`docs/attention-feed-contract.md:256`, since `43234bd6`, Loop 1).
  - The capabilities example (line 655) already says `604800`, but no Go code produces `cursor_retention_min_sec`.
  - `retention_expires_at` (line 330) isn't produced by any code either.
  - The doc doesn't cover recipient fields (`recipient_project` and `recipient_agent`, `internal/robot/attention_contract.go:298-299`), `--events-recipient` (`internal/cli/root.go:3326`), the mail uniqueness index (`internal/state/migrations/017_attention_recipient_unique.sql:14`), the store-backed feed by default (`SetAttentionStoreOpener`, `internal/robot/attention_feed.go:3627`), or the 1 second cross-process poll (`AttentionPollInterval`, `attention_feed.go:550`).
- **`--robot-help`** (`RenderHelp`, `internal/robot/robot.go:1990`) lists `--robot-events` with `--since-cursor` and `--events-limit` only (`:2061`). `--robot-capabilities` already lists `--events-recipient` (`internal/robot/capabilities.go:596`).
- **README** has nothing on the hook, nudge, switches or snooze. "### 4. Coordination, Reservations, and Human Oversight" (`README.md:227`) is the natural place.
- **Switches:** `[agent_mail]` holds `stop_hook_enabled`, `idle_nudge_enabled`, `idle_nudge_interval_seconds` and `human_typing_quiet_seconds` (`internal/config/config.go:1313-1343`). Snooze is `ntm mail snooze <message-id> --until <time>` (`internal/cli/mail.go:1135`).

### Key Discoveries

- **Numbers already recorded:**
  - Hook speed, against the real Agent Mail (Loop 3, `03-claude-turn-end-push-tdd.md:271`): 165 ms cold, then a median of about 113 ms.
  - The nudge default of 30 seconds is fixed (`05-idle-nudge-tdd.md:61`). No end-to-end delay has been recorded.
- **Work deferred to this loop:**
  - e2e tests from Loops 1 to 3.
  - Codex end to end (Loop 7, `07-codex-turn-end-push-tdd.md:1332`).
  - Snooze with real agents (Loop 6, `06-agent-snooze-tdd.md:1068`).
  - The serve stream by hand (Loop 4, `04-cross-process-delivery-tdd.md:1322`).
  - The contract doc (Loops 1 and 2).
- **Agent names come from registration.** A spawned agent's name is in its stand-in's recorded env (`AGENT_NAME`), as `agentEnv` reads it in `idle_nudge_test.go`.
- **The Codex Stop input fixture** is `internal/cli/testdata/codex_stop_input.json`. e2e reads it by path.

## Desired End State

### Observable Behaviors

1. Given a spawned Claude or Codex agent and a message on the fake Agent Mail, when the Stop command that spawn wrote runs as that agent, then it prints a block decision naming the sender and subject, a second run prints nothing, and `--robot-events --events-recipient=<agent>` in another process lists the event once.
2. Given a spawned idle agent with a 1 second nudge poll, when a message arrives, then the monitor that spawn started types one reminder naming it, and later polls type nothing. A message snoozed for 3 seconds is left out until then, and nudged once after.
3. Given that session with events told by the hook and the nudge, when the monitor and session are killed and spawned again in the same folder, then both events are still listed once, and nothing is announced again.
4. Given a running session, when the fake Agent Mail stops, then the hook exits 0 at once with no output, and the monitor keeps running and types nothing. When the server comes back with a message waiting, then the hook and the nudge announce it.
5. Given a work folder whose hook files hold other entries, when `ntm spawn` runs there 3 times, then each file has one ntm entry, and the other entries are unchanged.
6. Given behaviors 1 and 2, when they're timed, then 20 hook runs with no new mail each take under 1 second, and the nudge arrives within 2 polls. The numbers are logged.
7. Given this VPS and your local machine, when the real-machine checks run, then hook speed against the real Agent Mail, a Loop 1 Sprint 2 event's retention, and migration completeness are recorded. Any miss is brought to you.
8. Given `docs/attention-feed-contract.md`, when it's checked against the code, then its retention equals the code's, it names no field the code never produces, and it covers the recipient fields, `--events-recipient`, mail uniqueness, the store-backed feed by default and cross-process delivery.
9. Given `ntm --robot-help`, when it's read, then `--robot-events` lists `--events-recipient`.
10. Given README.md, when it's checked, then a section covers the turn-end hook, the idle nudge, the four `[agent_mail]` switches and `ntm mail snooze`, and every key and command it names is real.
11. Given the sprint's changes, when the release gates run, then the build passes, and tests, gofmt and lint show no new failures.

## What We're NOT Doing

- Changing the `mcp_agent_mail` server, or using the real Agent Mail in e2e tests.
- Fixing the 7 known failures in bd-5zbcm, or the 3 files gofmt already lists (your 4c answer).
- Adding `retention_expires_at` or `cursor_retention_min_sec` to the code. The doc changes to match the code (your 4c answer).
- Reviving or fixing the legacy scenario harness.
- Delivery across machines.

## Scope decisions (4c, 2026-11-11)

- **Test setup:** Go `testing` and `rapid`. The e2e tests use `//go:build e2e` and run with `go test -tags=e2e ./e2e/`. They build ntm themselves and run in a throwaway home and config folder, with a private tmux server and a fake Agent Mail inside the test. The real Agent Mail is used only in the by-hand checks and the human test.
- **Gates:** no new failures. The build must pass. Tests may fail only on the bd-5zbcm list. gofmt may list only its 3 old files. golangci-lint is reported as not run while it isn't installed.
- **Added:** snooze in behavior 2's e2e test, and an `ntm serve` stream step in the human test.
- **Behaviors:** all 11 approved.

## Testing Strategy

- **Framework:** Go `testing`. No property tests here: every behavior is a fixed scenario or a fixed file, with nothing to generate over.
- **Test types:**
  - e2e (`//go:build e2e`): behaviors 1 to 6, in `e2e/attention_journey_test.go`.
  - Unit: behavior 8 (`internal/robot`), behavior 9 (`internal/robot`), and behavior 10 (`internal/cli`).
  - By hand, recorded in the build record: behaviors 7 and 11, and the human test.
- **Setup (`e2e/attention_env_test.go`, new):** e2e has no hermetic environment, and the closure helpers live in another package's test files. The new file holds:
  - `journeyEnv`: the temp root, the env vars as in `closureEnv`, and a private tmux server.
  - `ntm(args...)`: runs the built binary with that env.
  - `fakeAgentMail`: copied from `internal/cli/agent_hook_test.go:30-169`. It gains `stop()` and `restart()`, which re-listen on the same address.
  - The stand-in `claude` and `codex` scripts and `writeConfig`: copied from `screenAgentScript` and `screenAgentConfig`.
  - `killMonitors`: finds `internal-monitor` processes whose `/proc/<pid>/environ` has this env's `HOME`, and stops them. It runs at cleanup and in behavior 3.
- **Fails closed:** a missing `tmux` or `bash` fails the test with `t.Fatal`. The tests don't skip.
- **How tests run:**
  - `go test -tags=e2e -count=1 -v -run 'TestE2E_' ./e2e/`.
  - Unit: `go test -short ./internal/robot/ ./internal/cli/`, with `-timeout 40m` for full `internal/cli` runs.

## Workflow Closure

The research's Chain A (`ntm mail send` -> signal file -> `check_critical_on_stop.sh`) and Chain B (dashboard poll -> in-memory feed) are what Loops 1 to 8 replaced. This loop closes the whole replaced chain with the real binary. The source is a message on Agent Mail, and the observables are the hook's stdout, the pane's typed text and `--robot-events` in a separate process.

- **`highest_new_connector`:** the process boundaries: `ntm spawn` launching the detached `internal-monitor`; the hook command run by a shell; a second process reading `state.db`.
- **Trigger choice:** `ntm spawn` (the outermost entrypoint), and the shell command spawn wrote. The only seeds are messages on the fake server, the config file and, for behavior 5, existing hook entries.

### Production Operation Chain

`ntm spawn S --cc=1 -> installStopHookIfActive (.claude/settings.local.json, .codex/hooks.json) -> resilience.NewInternalMonitorCommand -> internal-monitor S (detached) -> runIdleNudgeLoop -> idleNudger.poll -> FetchInbox -> told record claim (state.db) -> SendKeysForAgentDoubleEnter -> pane | Claude/Codex turn end -> sh -c "<ntm> agent-hook stop --project D" -> ResolveHookIdentity -> FetchInbox -> AppendAddressedAttentionEvent + told record (state.db) -> block decision on stdout | ntm --robot-events --events-recipient=A (another process) -> GetAttentionEventsSince -> JSON`

**Production callers:** there's no new production code expected. If a test finds a defect, the fix goes in the code it belongs to, with its own test, and the build record names its production caller.

### Closure Test: A message reaches the hook output, for Claude and Codex [BLOCKING]

Behavior 1.
- **SOURCE (seed only):** a message on the fake Agent Mail, and the config file.
- **TRIGGER (start):** `ntm spawn` (the real binary); then the Stop command read from the written hook file, run with `sh -c` from the work folder (boundary = the outermost entrypoint).
- **DRIVERS (async edges):** none. The hook is synchronous.
- **OBSERVE (assert via):** the hook's stdout, and `ntm --robot-events --events-recipient=<agent>` in a separate process.
- **FORBIDDEN SPAN:** everything in ntm. The test seeds no `state.db` row and calls no ntm Go function.
- **RED-AT-SEAM proof:** spawn with `stop_hook_enabled = false`. Expected: "no ntm Stop entry in .claude/settings.local.json".
- **DRIVABILITY:** the store is the env's own `state.db`, through `XDG_CONFIG_HOME`. There's no async edge.
- **EXECUTION (must run):** real tmux (private socket), bash and the built binary. A missing one fails the test.

### Closure Test: A message reaches an idle agent as one nudge, and a snoozed one waits [BLOCKING]

Behavior 2.
- **SOURCE (seed only):** messages on the fake server, and the config with `idle_nudge_interval_seconds = 1` and `human_typing_quiet_seconds = 2`.
- **TRIGGER (start):** `ntm spawn`, which starts the real detached monitor; `ntm mail snooze` as the agent.
- **DRIVERS (async edges):** the monitor's 1 second poll is real. The test waits for the stand-in's `typed` file, with a deadline (15 seconds), as Loop 5's closure tests do.
- **OBSERVE (assert via):** the stand-in's `typed` file (what tmux typed into the pane), and `--robot-events`.
- **FORBIDDEN SPAN:** the monitor, its poll and the store. The test doesn't start the monitor itself.
- **RED-AT-SEAM proof:** spawn with `idle_nudge_enabled = false`. Expected: "no reminder typed within 15s".
- **DRIVABILITY:** the poll interval comes from the config, and is frozen into the manifest at spawn.
- **EXECUTION (must run):** as behavior 1.

### Closure Test: A restart loses nothing and repeats nothing [BLOCKING]

Behavior 3.
- **SOURCE (seed only):** behavior 2's session.
- **TRIGGER (start):** `killMonitors`, `tmux kill-session`, then `ntm spawn` again in the same folder.
- **DRIVERS (async edges):** the new monitor's poll, observed for 5 polls.
- **OBSERVE (assert via):** `--robot-events --events-recipient=<agent>`, the hook's stdout, and `typed`.
- **FORBIDDEN SPAN:** `state.db` is never read or written by the test.
- **RED-AT-SEAM proof:** point the respawn at a fresh `XDG_CONFIG_HOME`, so it gets an empty `state.db`. Expected: "events lost after restart".
- **DRIVABILITY and EXECUTION:** as behavior 2.

### Closure Test: An Agent Mail outage blocks nothing, and reminders resume [BLOCKING]

Behavior 4.
- **SOURCE (seed only):** the fake server's state, and a message added while it's down.
- **TRIGGER (start):** `fakeAgentMail.stop()`, the hook command, the monitor's polls, then `restart()`.
- **DRIVERS (async edges):** the monitor's poll.
- **OBSERVE (assert via):** the hook's exit code, stdout and time; the monitor process is still alive; `typed`.
- **FORBIDDEN SPAN:** as behavior 2.
- **RED-AT-SEAM proof:** there's no fail-open seam to switch off without changing code. The red proof is the outage itself: the test checks the hook's run time is under 5 seconds, not just its exit code.
- **DRIVABILITY and EXECUTION:** as behavior 2. `restart()` listens again on the saved address.

### Closure Test: Spawning again keeps one hook entry and the others [BLOCKING]

Behavior 5.
- **SOURCE (seed only):** `.claude/settings.local.json` and `.codex/hooks.json` with other entries, written before the first spawn.
- **TRIGGER (start):** `ntm spawn` 3 times, with `tmux kill-session` between runs.
- **DRIVERS (async edges):** none.
- **OBSERVE (assert via):** the two files, parsed.
- **FORBIDDEN SPAN:** the `claudeconfig` merge code.
- **RED-AT-SEAM proof:** none needed: Loops 3 and 7 proved the merge. This guards the target through the real binary.
- **DRIVABILITY and EXECUTION:** as behavior 1.

## Sprint 1: The whole path, working

**Bead:** `bd-upu1q.1`.

**Testable function:** "Run the scope's Connected Release Journey by hand, and run the end-to-end tests for message to hook output and message to idle nudge, with `docs/attention-feed-contract.md` updated to match."

**Order of work:** 1, 2, 3, 4, 5, 6, then 8, 9, 10, then 7 and 11. The e2e tests come first, so any defect they find is fixed before the docs describe the code.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 1, 2 and 8, then your by-hand journey |
| The Connected Release Journey passes by hand on this VPS, with no dashboard or `ntm serve` running | the human test, checked first by 1, 2 and 3 |
| Two end-to-end tests pass in `e2e/`: message to hook output, and message to idle nudge | 1, 2 |
| The scope's Cross-Process Failure passes: with Agent Mail stopped mid-session, agents stop normally, nothing blocks, and reminders resume once it's back | 4 |
| `docs/attention-feed-contract.md` matches the code, including section 6.3 and the `retention_expires_at` field | 8 |
| The five quality targets in `SCOPE.md` section 6 are measured or checked, and results are recorded. Any miss is brought to you | 5, 6, 7 |
| `go build ./cmd/ntm`, `go test -short ./...`, `golangci-lint run` and `gofmt -l .` all pass | 11 (no new failures, your 4c answer) |
| Requirement: e2e tests use a throwaway config folder, so the real `state.db` isn't touched | 1 to 6 (`journeyEnv`) |
| Requirement: `--robot-capabilities` and `--robot-help` list the new flags | 9 (capabilities already lists `--events-recipient`) |
| Requirement: a short section in the main docs covers the hook, nudge, switches and snooze | 10 |
| Scenario: message to hook output | 1 |
| Scenario: message to idle nudge | 2 |
| Scenario: restart mid-journey | 3 |
| Scenario: Agent Mail outage | 4 |
| Scenario: hook speed | 6, 7 |
| Scenario: idle reminder delay | 6, and human test step 9 |
| Scenario: event retention | 7 |
| Scenario: hook file safety | 5 |
| Scenario: migration completeness | 7 |

### Behavior 1: A message reaches the hook output, for Claude and Codex [BLOCKING]

#### Test Specification

**Given** a `journeyEnv` with Agent Mail on (the fake server), and a git work folder under `projects_base`. **When**:
1. `ntm spawn hook --cc=1` runs. The agent's name is read from the stand-in's env.
2. The fake server gets message 101 from RedStone, subject "journey hook", for that agent.
3. The `agent-hook stop` command from `.claude/settings.local.json` runs with `sh -c` from the work folder, with `AGENT_NAME=<agent>` and Claude's Stop input.

**Then**:
- stdout is one JSON object, with `decision` set to `block` and a `reason` containing "[#101] from RedStone: journey hook";
- a second run prints nothing;
- `ntm --robot-events --events-recipient=<agent>` lists exactly one event for message 101.

The same runs for `ntm spawn hookcod --cod=1`, with the command from `.codex/hooks.json` and the input from `internal/cli/testdata/codex_stop_input.json`.

**Edge cases:**
- The first `--robot-events` call is another process. It isn't the hook, and it isn't the dashboard.
- The hook command is used exactly as written, with no edits to quoting or paths.

**Property:** no property: fixed scenario, nothing to generate over.

**Files touched:** `e2e/attention_env_test.go` (new: the env, the fake server, stand-ins, the `ntm` runner), `e2e/attention_journey_test.go` (new).

#### TDD Cycle

##### 🔴 Red

Write `TestE2E_MessageToHookOutput` with the helpers. First run it with `stop_hook_enabled = false` in the config. Expected: "no ntm Stop entry in .claude/settings.local.json". Then remove that line.

```go
func TestE2E_MessageToHookOutput(t *testing.T) {
	for _, tool := range []struct{ flag, settings, input string }{
		{"--cc=1", ".claude/settings.local.json", `{"session_id":"e2e","hook_event_name":"Stop","stop_hook_active":false}`},
		{"--cod=1", ".codex/hooks.json", codexStopInput(t)},
	} {
		env := newJourneyEnv(t)
		agent, dir := env.spawn(t, "hook", tool.flag)
		env.mail.addMessage(agent, 101, "RedStone", "journey hook")
		command := ntmStopCommand(t, filepath.Join(dir, tool.settings))
		out := env.shell(t, dir, command, tool.input, "AGENT_NAME="+agent)
		requireBlock(t, out, "[#101] from RedStone: journey hook")
		if again := env.shell(t, dir, command, tool.input, "AGENT_NAME="+agent); again != "" {
			t.Fatalf("second turn end printed %q", again)
		}
		requireEventsOnce(t, env, agent, 101)
	}
}
```

##### 🟢 Green

No production change is expected. A failure here is a real defect: fix it in its own package, with its own unit or closure test, and record it.

##### 🔵 Refactor

- [ ] **No duplication:** the env, server and stand-ins live once in `attention_env_test.go`, and every journey test uses them.
- [ ] **Reveals intent:** helpers are named for what a user does (`spawn`, `shell`, `requireBlock`).
- [ ] **Complexity went down:** the Claude and Codex runs share one table.
- [ ] **No shallow wrappers:** `ntm()` adds the env and captures output; it isn't a pass-through.
- [ ] **Fits existing patterns:** `closureEnv` and `screenAgentConfig`.

#### Success Criteria

**Automated:** `go test -tags=e2e -count=1 -run TestE2E_MessageToHookOutput ./e2e/` fails with the switch off, then passes.

### Behavior 2: A message reaches an idle agent as one nudge, and a snoozed one waits [BLOCKING]

#### Test Specification

**Given** a `journeyEnv` with a 1 second nudge poll and a 2 second typing quiet period. **When**:
1. `ntm spawn nudge --cc=1` runs, and the stand-in shows `cc_idle.txt`. No test code starts the monitor.
2. Message 201 ("while idle") arrives.

**Then**:
- within 15 seconds, `typed` holds one reminder naming "while idle";
- after 5 more seconds, `typed` is unchanged.

**And when**:
1. Message 202 ("later") arrives.
2. `ntm mail snooze 202 --until 3s --project <dir>` runs as the agent, before the next poll. To keep the timing safe, the message is added and snoozed together: snooze first, using the message's id, and the inbox already holding it.

**Then**:
- no reminder names "later" within 2 seconds;
- one reminder names it within 15 seconds after that;
- nothing more follows.

**Edge cases:**
- The monitor process running is the one `ntm spawn` started. The test finds it with `killMonitors`'s lookup, and checks there is exactly one.
- The reminder mentions `fetch_inbox`, so the agent knows how to read the mail.

**Property:** no property: fixed scenario.

**Files touched:** `e2e/attention_journey_test.go`, `e2e/attention_env_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestE2E_MessageToIdleNudge`, first run with `idle_nudge_enabled = false`. Expected: "no reminder typed within 15s".

##### 🟢 Green

No production change expected. If the snooze can't be placed before the poll, the test adds the message only after snoozing it. Snooze checks the inbox for unknown ids (`snoozeInboxLimit`, `mail.go:1131`), so the message must already be on the fake server. The add and the snooze happen in under one poll.

##### 🔵 Refactor

- [ ] **No duplication:** waiting on `typed` is one helper, shared with behaviors 3, 4 and 6.
- [ ] **Reveals intent:** the snooze part is its own block, with a comment naming Loop 6.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `waitTyped`, and `TestClosure_NudgeSkipsSnoozed` (`internal/cli/mail_snooze_test.go:562`).

#### Success Criteria

**Automated:** `go test -tags=e2e -count=1 -run TestE2E_MessageToIdleNudge ./e2e/` fails with the switch off, then passes.

### Behavior 3: A restart loses nothing and repeats nothing [BLOCKING]

#### Test Specification

**Given** behavior 2's session, plus message 203 told by the hook (as in behavior 1). **When**:
- `killMonitors` stops the monitor;
- `tmux kill-session -t nudge` runs;
- `ntm spawn nudge --cc=1` runs again in the same folder, and the new stand-in shows the idle screen.

**Then**:
- `--robot-events --events-recipient=<agent>` lists 201, 202 and 203 once each;
- the hook prints nothing;
- the new stand-in's `typed` stays empty for 5 polls.

**Edge cases:** the agent keeps its name across the respawn (registration reuses it). If it doesn't, the test fails and the finding is brought to you; the test isn't loosened.

**Property:** no property: fixed scenario.

**Files touched:** `e2e/attention_journey_test.go` (a phase of `TestE2E_MessageToIdleNudge`).

#### TDD Cycle

##### 🔴 Red

Run the respawn with `XDG_CONFIG_HOME` pointing to a fresh folder. Expected: "events lost after restart". Then restore it.

##### 🟢 Green

No production change expected.

##### 🔵 Refactor

- [ ] **No duplication:** `requireEventsOnce` is shared with behavior 1.
- [ ] **Reveals intent:** the restart phase is named.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 1's restart closure tests.

#### Success Criteria

**Automated:** `TestE2E_MessageToIdleNudge` passes, restart phase included.

### Behavior 4: An Agent Mail outage blocks nothing, and reminders resume [BLOCKING]

#### Test Specification

**Given** a spawned idle agent, as in behavior 2. **When** `mail.stop()` runs and message 301 ("after outage") is added to the stopped server's inbox. **Then**:
- the hook command exits 0 with empty stdout, within 5 seconds;
- 3 polls later, the monitor process is still alive and `typed` is empty.

**When** `mail.restart()` runs. **Then**:
- within 15 seconds, one reminder names "after outage";
- a hook run afterwards prints nothing, since the nudge already told it.

**Edge cases:**
- While the server is down, nothing is written to `state.db` for message 301: `--robot-events` doesn't list it until the server is back.
- The monitor's log shows the failure, but nothing in the pane does.

**Property:** no property: fixed scenario.

**Files touched:** `e2e/attention_journey_test.go`, `e2e/attention_env_test.go` (`stop`, `restart`).

#### TDD Cycle

##### 🔴 Red

`TestE2E_AgentMailOutage`. The red proof is the time limit: the test fails if the hook takes 5 seconds or more, or exits non-zero.

##### 🟢 Green

No production change expected. A hook that waits on a dead server is a defect in `internal/agentmail`'s client timeout, and gets fixed there.

##### 🔵 Refactor

- [ ] **No duplication:** shares the nudge helpers.
- [ ] **Reveals intent:** "down" and "back" phases.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 3's fail-open closure tests.

#### Success Criteria

**Automated:** `go test -tags=e2e -count=1 -run TestE2E_AgentMailOutage ./e2e/` passes.

### Behavior 5: Spawning again keeps one hook entry and the others [BLOCKING]

#### Test Specification

**Given** a work folder with:
- `.claude/settings.local.json` holding a PostToolUse entry `echo kept` and a `"model": "opus"` setting;
- `.codex/hooks.json` holding a Stop entry `echo codex-kept`.

**When** `ntm spawn safe --cc=1 --cod=1` runs 3 times, with `tmux kill-session -t safe` between runs. **Then**:
- each file has exactly one ntm Stop entry;
- `echo kept`, `"model": "opus"` and `echo codex-kept` are still there, unchanged;
- `.git/info/exclude` lists each ntm-written file once.

**Edge cases:** none beyond the above.

**Property:** no property: fixed scenario.

**Files touched:** `e2e/attention_journey_test.go`.

#### TDD Cycle

##### 🔴 Red

`TestE2E_RespawnKeepsHookFiles`. It's expected to pass at once, guarding the target through the real binary. The build record says so.

##### 🟢 Green

No production change expected.

##### 🔵 Refactor

- [ ] **No duplication:** `ntmStopCommand` (behavior 1) does the entry lookup.
- [ ] **Reveals intent:** the kept entries are named constants.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 3's merge tests.

#### Success Criteria

**Automated:** `go test -tags=e2e -count=1 -run TestE2E_RespawnKeepsHookFiles ./e2e/` passes.

### Behavior 6: Hook speed and nudge delay are within target [LEAF: timing of behaviors 1 and 2, no new path]

#### Test Specification

**Given** behavior 1's Claude session, with no new mail. **When** the hook command runs 20 times. **Then** each run takes under 1 second, and the test logs the median and the maximum.

**Given** behavior 2's message 201. **When** it's timed from `addMessage` to the reminder in `typed`. **Then** it's under 2 seconds plus 2 polls (4 seconds, with the 1 second poll), and the test logs the delay.

**Edge cases:** the first run is cold (new `state.db`). It's logged separately, and it must also be under 1 second.

**Property:** no property: a measurement.

**Files touched:** `e2e/attention_journey_test.go`.

#### TDD Cycle

##### 🔴 Red

The timing checks are added to `TestE2E_MessageToHookOutput` and `TestE2E_MessageToIdleNudge`. They're checked by temporarily lowering the limit to 1 ms: expected "hook run 1 took …, want under 1ms". Then the limit is restored.

##### 🟢 Green

No production change expected. A miss is brought to you, not tuned away.

##### 🔵 Refactor

- [ ] **No duplication:** one `timeRuns` helper.
- [ ] **Reveals intent:** the targets are named constants citing SCOPE.md section 6.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 3's hook speed measurement.

#### Success Criteria

**Automated:** both tests pass with the timing checks, and `-v` prints the numbers. The build record has them.

### Behavior 7: The real-machine targets are checked and recorded [LEAF: measured by hand]

#### Test Specification

**Given** this VPS, with the real Agent Mail running. **When** these checks run. **Then** each result is in the build record:
1. **Hook speed:** in a scratch git repo, with the agent registered and no new mail, run the hook 20 times (`for i in $(seq 20); do /usr/bin/time -f %e /tmp/ntm-release agent-hook stop </dev/null; done`, with `AGENT_NAME` set). Each run is under 1 second. The median and maximum are recorded.
2. **Event retention:** an event written during Loop 1 Sprint 2's human test is still listed by `--robot-events --since-cursor=0`. Its `expires_at` is 7 days after it was written. This is read with `sqlite3 -readonly` on `state.db`: a read-only check of a measurement, not an assertion path.
3. **Migration completeness:** `ntm agent-hook migrate ~/Dev` on this VPS and on your local machine prints "Nothing to migrate.", and the migration guide's "Test one repo" check passes in one migrated repo on each machine. This needs your migration done and committed on both machines first. Until then, it's recorded as not yet met and brought to you.

**Edge cases:** if the Loop 1 event has passed its 7 days by the time of the check, a newer event is used, and the record says so.

**Property:** no property: a measurement.

**Files touched:** the build record in this file.

#### TDD Cycle

##### 🔴 Red

n/a (measured by hand). Each check states its pass condition before it runs.

##### 🟢 Green

Run the checks and record the numbers.

##### 🔵 Refactor

- [ ] **No duplication:** n/a.
- [ ] **Reveals intent:** each result names its SCOPE.md target.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** n/a.
- [ ] **Fits existing patterns:** Loop 3's measurement record.

#### Success Criteria

**Manual:** the five targets each have a recorded result. Any miss is raised with you, not hidden.

### Behavior 8: The contract doc matches the code [LEAF: reads one file and code constants]

#### Test Specification

**Given** `docs/attention-feed-contract.md`. **When** `TestAttentionContractDocMatchesCode` (in `internal/robot`) reads it. **Then**:
- section 6.3 states the retention that `attentionEventRetention` holds, as "**7 days**", derived from the constant rather than typed into the test;
- the doc names neither `retention_expires_at` nor `cursor_retention_min_sec`, which no code produces;
- it names each recipient JSON tag of `AttentionEvent`, read by reflection (`recipient_project`, `recipient_agent`);
- it names the `--events-recipient` flag;
- it describes mail event uniqueness (project, recipient and message id);
- it says the feed is store-backed by default;
- it says cross-process delivery polls every `AttentionPollInterval` ("1 second").

**Edge cases:** during Green, every quoted field name in the doc's JSON examples is grepped against the code's JSON tags. Any other name the code never produces is fixed in the doc and added to the test's list of banned names.

**Property:** no property: one file.

**Files touched:** `docs/attention-feed-contract.md`, `internal/robot/robot_test.go`. There's no doc test file in `internal/robot`, so the test goes next to `TestPrintHelp_AttentionLoopMatchesCapabilities`.

#### TDD Cycle

##### 🔴 Red

`TestAttentionContractDocMatchesCode`. Expected first failure: "the doc names retention_expires_at, which no code produces".

##### 🟢 Green

Edit the doc:
- Remove `retention_expires_at` from the example at line 330.
- Replace `cursor_retention_min_sec` with what `--robot-capabilities` really reports. If capabilities has no `attention_feed` block, the example is cut to match.
- Add a section "Addressed mail events": the recipient fields, `--events-recipient`, and uniqueness.
- Add a section "Durable feed and cross-process delivery": the store by default, the 1 second poll, and that a same-process event isn't delivered twice.

##### 🔵 Refactor

- [ ] **No duplication:** the test reads constants and tags from code, not copies.
- [ ] **Reveals intent:** each check's failure message names the doc section.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestPrintHelp_AttentionLoopMatchesCapabilities` (`internal/robot/robot_test.go:726`).

#### Success Criteria

**Automated:** `go test -short -run TestAttentionContractDocMatchesCode ./internal/robot/` fails first, then passes.

### Behavior 9: `--robot-help` lists `--events-recipient` [LEAF: one help text]

#### Test Specification

**Given** `RenderHelp()`. **When** `TestPrintHelp_EventsRecipient` reads it. **Then** the `--robot-events` line lists `--events-recipient`, and every flag that capabilities lists for `robot-events` appears on that line.

**Edge cases:** none.

**Property:** no property: one text.

**Files touched:** `internal/robot/robot.go` (`RenderHelp`), `internal/robot/robot_test.go`.

#### TDD Cycle

##### 🔴 Red

Expected: "--robot-events help lacks --events-recipient".

##### 🟢 Green

Add `--events-recipient=AGENT` to the line at `robot.go:2061`.

##### 🔵 Refactor

- [ ] **No duplication:** the test reads the flag list from capabilities.
- [ ] **Reveals intent:** n/a.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestPrintHelp_AttentionLoopMatchesCapabilities`.

#### Success Criteria

**Automated:** `go test -short -run TestPrintHelp ./internal/robot/` passes.

### Behavior 10: README covers the hook, nudge, switches and snooze [LEAF: reads one file]

#### Test Specification

**Given** README.md. **When** `TestREADMEMailReminders` (in `internal/cli`) reads it. **Then**:
- a section titled "Mail reminders" sits under "### 4. Coordination, Reservations, and Human Oversight";
- it names all four `[agent_mail]` keys, and every key it names is a TOML tag of `config.AgentMailConfig`, read by reflection;
- every `ntm mail snooze` and `ntm agent-hook` line in its code blocks parses with the real commands' flags;
- it links `docs/agent-hook-migration.md`.

**Edge cases:** none.

**Property:** no property: one file.

**Files touched:** `README.md`, `internal/cli/mail_snooze_test.go`.

#### TDD Cycle

##### 🔴 Red

Expected: "README has no Mail reminders section".

##### 🟢 Green

Write the section in plain language, covering:
- what happens at turn end and when an agent is idle;
- the four switches with their defaults;
- `ntm mail snooze 42 --until 30m`;
- the migration guide link.

##### 🔵 Refactor

- [ ] **No duplication:** the keys come from the struct.
- [ ] **Reveals intent:** n/a.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestAgentHookMigrationGuide` (Loop 8).

#### Success Criteria

**Automated:** `go test -short -run TestREADMEMailReminders ./internal/cli/` fails first, then passes.

### Behavior 11: The release gates show no new failures [LEAF: commands]

#### Test Specification

**Given** the sprint's changes. **When** the gates run. **Then**:
- `go build ./cmd/ntm` passes;
- `go test -short -timeout 40m ./...` fails only on the bd-5zbcm list;
- `gofmt -l .` lists only `cmd/closure-adapter/main.go`, `internal/swarm/codex_home_test.go` and `internal/tmux/clean.go`;
- `go vet` passes on the changed packages, and `gofmt -l e2e/` is empty;
- `golangci-lint run` is reported as not run while it isn't installed.

**Edge cases:** a new failure anywhere is fixed or raised, not added to the known list.

**Property:** no property: commands.

**Files touched:** the build record.

#### TDD Cycle

##### 🔴 Red

n/a.

##### 🟢 Green

Run the gates, and record their output.

##### 🔵 Refactor

n/a.

#### Success Criteria

**Manual:** the build record shows each gate's result.

### Build record (2026-11-11)

Base commit `6434f7ec`. Behaviors 1 to 6 and 8 to 10 are built. Behavior 7 is partly measured; the rest is in the human test. Behavior 11 is below.

**e2e (`e2e/attention_env_test.go` and `e2e/attention_journey_test.go`, both new):**
- `journeyEnv` is the throwaway environment for the built binary. It has a temp home, config, data and Codex home, a private tmux server (`/tmp/ntmjx*`) and `PATH` narrowed to the stand-ins and ntm.
- `journeyAgentMail` is the fake server, with `stop()` and `restart()` on the same address.
- The stand-in `claude` and `codex` scripts are included.
- `monitors()` and `killMonitors()` find this env's `internal-monitor` processes by command line and `HOME`. Cleanup leaves none running (checked with `ps` after each run).
- The four tests:
  - `TestE2E_MessageToHookOutput` (behaviors 1 and 6), for Claude and Codex.
  - `TestE2E_MessageToIdleNudge` (behaviors 2, 3 and 6, with snooze).
  - `TestE2E_AgentMailOutage` (behavior 4).
  - `TestE2E_RespawnKeepsHookFiles` (behavior 5).
- They passed 3 runs in a row: `go test -tags=e2e -count=3 -run '^TestE2E_(MessageToHookOutput|MessageToIdleNudge|AgentMailOutage|RespawnKeepsHookFiles)$' ./e2e/`.

**Found and fixed: the e2e package didn't compile with `-tags=e2e`.** `e2e/rch_hook_e2e_test.go` used `dcg.HookEntry.Command`, which moved to `HookEntry.Hooks[i].Command`. It now uses `Hooks[0].Command`, after checking there's exactly one handler. No assertion changed. Until this fix, no e2e test could build.

**Red, each observed and then reverted:**
- Behavior 1 with `stop_hook_enabled = false`: "read …/.claude/settings.local.json: no such file or directory" (Claude), and the same for `.codex/hooks.json`.
- Behavior 2 with `idle_nudge_enabled = false`: "no "while idle" typed within 15s".
- Behavior 3 with a fresh config folder for the respawn: first "the respawned agent is "Agent3", was "Agent1"" (the session registry lives there too). With that check off: "--robot-events --events-recipient=Agent1 lists message 201 0 times".
- Behavior 6 with a 1 ms limit: "first hook run took 39.9ms, want under 1ms".
- Behavior 4: its red proof is its time limit, as planned.
- Behavior 5: passed at once, as planned (it guards a target).
- Behavior 8: 38 errors on the old doc (32 `evt_` cursors, `retention_expires_at`, `cursor_retention_min_sec`, the `attention_feed` block, the field table, and no section 16).
- Behavior 9: the four filters missing from `--robot-help`.
- Behavior 10: "README has no Mail reminders section".

**Measured (behavior 6, e2e, fake Agent Mail on localhost, 3 runs):**
- Hook with no new mail, 20 runs: medians 39 to 44 ms, maximum 51 ms. Target: under 1 second.
- First run with mail (cold): 40 to 51 ms.
- Hook while Agent Mail is down: 36 to 50 ms, exit 0, no output.
- Idle reminder delay with a 1 second poll: 1.007 to 1.008 seconds after the message.

**Behavior 7, on this VPS so far:**
- **Retention:** the real `state.db` holds today's health events; event 9 was written 2026-11-11 15:05:31 and expires 2026-11-18 15:05:31, exactly 7 days. It holds no Loop 1 Sprint 2 mail event: that human test ran on your local machine. That check is human test step 19.
- **Migration completeness: not yet met.** `ntm agent-hook migrate ~/Dev` on this VPS still lists `mcp_agent_mail` (Stop replaced, close removed, window kept). You haven't run the migration here yet. Step 20 covers both machines.
- **Hook speed against the real Agent Mail:** not measured on this VPS. It would mean registering a test agent on your live server. Loop 3 measured a median of about 113 ms. Step 18 times the human test's real agent.

**Behavior 8, by your answer "Whole doc":** `docs/attention-feed-contract.md` was rewritten to match the code (755 lines added, 574 removed), by a helper agent working against the test. `TestAttentionContractDocMatchesCode` (`internal/robot/attention_contract_doc_test.go`, new: it's an external test package because it imports `internal/serve`) checks:
- every JSON example parses;
- every field name in one is a JSON field of the robot or serve output types (map-typed fields hold data keys);
- the §3.1 table is exactly `AttentionEvent`'s fields;
- every `--flag` on an `ntm` line is in `--robot-capabilities`;
- §6.3's retention comes from the code, and §17's poll interval too;
- §16 covers the recipient fields, `--events-recipient` and uniqueness.

The helper found two mistakes in the first version of the test, both fixed:
- banning `"attention_feed"` also hit the heartbeat's real `source` value, so only the object `"attention_feed": {` is banned now;
- the comment said map keys were skipped, but the code didn't skip them.

**Claims removed from the doc because the code doesn't do them:** string cursors, `retention_expires_at`, `filters_applied`, filters overriding a profile, the `quiet` and `verbose` profiles, `component_health` and `_degraded` markers, `INVALID_CURSOR`, a severity filter flag, the old wait conditions, `--robot-digest --format`, and the capabilities `attention_feed` block.

**Code and help disagree; filed as beads rather than changed here:**
- `bd-k1mh8`: `--robot-events` and `--robot-digest` exit 0 when the reply has `success:false`.
- `bd-nep8h`: `--profile` defaults to empty, but capabilities and `--robot-help` say `operator`.

**Behaviors 9 and 10:**
- `--robot-help` gains a "Filters:" line under `--robot-events`, listing `--events-category`, `--events-session`, `--events-recipient=AGENT` and `--events-actionability`. `TestPrintHelp_EventsFilters` checks those four against capabilities. The plan said every `--robot-events` parameter would go on that line, but there are 13, which would be unreadable, so only the filters are listed.
- README gains "#### Mail reminders" under section 4. It covers the turn-end hook, the idle reminder, once per message, snooze, the four switches and the migration guide. `TestREADMEMailReminders` (in `internal/cli/mail_snooze_test.go`) checks it.

**Behavior 11 (release gates):**
- `go build` ok.
- gofmt lists only the 3 old files.
- `go vet` ok for `internal/robot`, `internal/cli` and `e2e` (with the tag).
- `go test -short ./internal/robot/` ok.
- `go test -short -timeout 40m ./...` failed only on the 7 tests in bd-5zbcm.
- The tamper check (base `6434f7ec`) is clean.

**Human test: PASSED on rocketman (2026-11-11), with notes.** You tested the uncommitted work applied onto 6434f7ec.
- **Steps 3 to 5:** on the first try the idle nudge told the agent, because Claude put `sleep 60` in the background and ended its turn early. A retry with foreground work (`python3 -c 'import time; time.sleep(60)'`) passed: message #12531 was told by the Stop hook (`told_by=stop_hook`), and the agent ran `fetch_inbox`. Step 3 now asks for foreground work.
- **Step 7:** passed. **Step 9:** passed; the nudge came after 11 seconds.
- **Step 11:** passed, but the respawn gave the agent a new name (PurpleFox instead of NavyCat), so "not announced again" proves less than it should. The e2e fake reuses names, so it doesn't catch this. Filed as `bd-301fl`.
- **Step 13:** passed (53 seconds). **Step 15:** passed.
- **Step 17:** passed. The event was on the SSE stream 1 second after it was written. `ntm serve` took about 10 seconds to start listening.
- **Step 19, hook speed against the real Agent Mail on rocketman:** minimum 0.27 s, median 0.33 s, maximum 0.45 s. That's under the 1 second target, but slower than Loop 3's VPS figure (about 113 ms). `/usr/bin/time` wasn't installed, so you used bash's `time`; step 18 now says so.
- **Step 21:** passed. Cursor 562960 was written 2026-11-07 and expires exactly 7 days later.
- **Step 23, migration completeness: not yet met.** Neither rocketman nor this VPS is migrated yet.
- **Seen again, not new in this loop:** Claude's trust prompt in a new folder, and the spawn prompt left unsent in the input.
- Evidence: `/home/debian/Dev/ntm-test-evidence/release-attention-feed-local-2026-11-11/`.

**Quality targets (SCOPE.md section 6):**

| Target | Result |
|---|---|
| Hook speed, no new mail: under 1 second | Met. e2e: median 39 to 44 ms. rocketman, real Agent Mail: median 0.33 s, maximum 0.45 s |
| Idle reminder delay: one poll | Met. e2e: 1.007 s with a 1 second poll. rocketman: 11 s with the 30 second default |
| Event retention: 7 days, after a restart | Met. A Loop 1 event (cursor 562960) expires exactly 7 days after it was written; e2e restart keeps every event |
| Hook file safety | Met. 3 spawns keep one ntm entry in each file, and the other entries unchanged |
| Migration completeness | **Not yet met.** Neither machine is migrated. Your migration on each machine, then step 22, closes it |
- golangci-lint, goimports and ubs weren't run (not installed).
- `go test -tags=e2e` with a broad `TestE2E_` pattern also ran older e2e tests: `TestE2E_RobotSmartRestartIntegration` and `TestE2E_EnsembleCLI_Synthesize_JSON` fail. They aren't in this loop, and the package couldn't build before this sprint, so they had never run.

### Human test script

Do this in a new scratch git repo, with no dashboard or `ntm serve` running.

1. Build the binary: `go build -o /tmp/ntm-release ./cmd/ntm`.
2. Run `/tmp/ntm-release spawn release --cc=1`, and wait for the agent to be idle. Find its name in the spawn output, or in its pane title: `tmux list-panes -t release -F '#{pane_title}'`. `--robot-status` shows only an agent count.
3. Give it slow work in the foreground: `/tmp/ntm-release send release --cc "Run python3 -c 'import time; time.sleep(60)' in the foreground, then reply done."`. (A plain `sleep 60` can be put in the background by Claude, which then ends its turn early, so the idle nudge tells it instead.)
4. While it works, send mail: `/tmp/ntm-release mail send release --to <agent> "mid turn"`.
5. Pass: when the turn ends, it's told about `mid turn`, with the sender and subject, and runs `fetch_inbox`.
6. In another process, run `/tmp/ntm-release --robot-events --events-recipient=<agent> | jq '.events[].summary'`.
7. Pass: the `mid turn` event is listed once.
8. Let the agent go idle, and send another message: `/tmp/ntm-release mail send release --to <agent> "while idle"`.
9. Pass: within about 30 seconds, one nudge names `while idle`.
10. Find the `internal-monitor` process for this session and kill it. Then kill and respawn the session in the same folder.
11. Pass: `/tmp/ntm-release --robot-events --events-recipient=<agent>` still lists both events, and neither message is announced again.
12. Run `go test -tags=e2e -v ./e2e/ -run <the two new test names>`.
13. Pass: both pass.
14. Read section 6.3 of `docs/attention-feed-contract.md`.
15. Pass: it says 7 days, and matches what you saw in step 6 of Loop 1, Sprint 2.

Added at 4c (Loop 4's deferred check):

16. In another terminal, run `/tmp/ntm-release serve`, and follow its event stream (as in Loop 4's human test). Then send another message to the agent and let a turn end.
17. Pass: the stream shows that mail event, written by the hook process, within a few seconds, and only once.

Added in the build (behavior 7's real-machine checks):

18. Hook speed: in the scratch repo, with no new mail for the agent, run `cmd=$(jq -r '.hooks.Stop[].hooks[].command | select(contains("agent-hook stop"))' .claude/settings.local.json); for i in $(seq 20); do time (echo '{}' | AGENT_NAME=<agent> sh -c "$cmd" >/dev/null); done` (bash's `time`; `/usr/bin/time` isn't installed everywhere).
19. Pass: every time is under 1 second. Note the median and the slowest.
20. Event retention, on the machine where you ran Loop 1 Sprint 2: `sqlite3 -readonly ~/.config/ntm/state.db "SELECT cursor, ts, expires_at FROM attention_events WHERE category='mail' ORDER BY cursor LIMIT 3"`.
21. Pass: the oldest mail event from that test is still there, its `expires_at` is 7 days after its `ts`, and `ntm --robot-events --since-cursor=0` still lists it.
22. Migration completeness, on each machine after you've migrated and committed: `ntm agent-hook migrate ~/Dev`.
23. Pass: it prints "Nothing to migrate.". If you haven't migrated a machine yet, note that instead. It's recorded as not yet met, not as a pass.

## Sprint verification (every sprint)

```bash
export PATH=$PATH:/usr/local/go/bin
go build ./cmd/ntm
gofmt -l internal/ cmd/ e2e/
go vet ./internal/robot/ ./internal/cli/ && go vet -tags=e2e ./e2e/
go test -short ./internal/robot/
go test -short -timeout 40m ./internal/cli/
go test -tags=e2e -count=3 -run 'TestE2E_' ./e2e/
go test -short -timeout 40m ./...      # compare failures with the known list in bd-5zbcm
bun ~/.claude/SAI/Tools/TestTamperCheck.ts --base <sprint start commit>
```

Before each commit, grep the staged diff for tokens: `registration_token"?:"[^[]|bearer [a-z0-9]|eyJ|maceojourdan`. Fixtures use `/home/user` and `/home/you`, never real usernames. `golangci-lint`, `goimports` and `ubs` are reported as not run if they're still missing.

## Integration & E2E Testing

- **E2E:** behaviors 1 to 6 are this project's first e2e tests, and the two the scope requires: message to hook output, and message to idle nudge. They run the real binary, the real detached monitor, real tmux and a real shell.
- **Integration:** the closure tests from Loops 1 to 8 stay the main regression net, in `internal/cli` and `internal/robot`.
- **By hand:** behavior 7 and the human test use the real Agent Mail and real Claude agents.

## Beads

- Epic `bd-upu1q` ("Loop 9: Release journey"), blocked by Loop 8's epic `bd-0iz9s`.
- Sprint bead `bd-upu1q.1` ("Loop 9 Sprint 1: The whole path, working").
- Known failures stay in `bd-5zbcm`. Any defect found here gets its own bead, linked from the build record.

## References

- Loop document: `09-release-journey.md`.
- Scope: `SCOPE.md` section 6.
- Research: `thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md` (Workflow Closure Map, lines 414-559).
- Patterns:
  - `closureEnv` (`internal/cli/attention_closure_test.go:63`), `privateTmux` (`internal/cli/agent_hook_test.go:837`), `newFakeAgentMail` (`internal/cli/agent_hook_test.go:30`);
  - `screenAgentScript` and `screenAgentConfig` (`internal/cli/idle_nudge_test.go:43-115`), `TestClosure_IdleAgentNudgedOnce` (`:328`);
  - `e2e/suite_test.go:30` (binary build).
