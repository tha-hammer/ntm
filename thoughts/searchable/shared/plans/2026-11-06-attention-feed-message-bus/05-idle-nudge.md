# Loop 5: Idle nudge

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Idle Nudge**, rules **One Telling**, **Reminder Format** and **Human Typing Guard**, contract **PaneNudgeState**.

## Overview

A Stop hook only fires when a turn ends, so a message to an idle agent waits until something else starts a turn. After this loop, `internal-monitor` notices new mail for an idle agent and types one batched reminder into its pane, unless a person may be typing there.

Covers deliverables 12, 13 and the nudge half of 16. Needs Loop 3, for the told record and reminder text.

The nudge reuses these existing functions:
- Idle detection: `determineAgentState` (`internal/cli/assign.go:1211`), `robot.IsLiveBusy` (`internal/robot/activity.go:844`), `tmux.CaptureForStatusDetection`
- Sending: `tmux.SendKeysForAgentDoubleEnter` (`internal/tmux/session.go:1279`)
- Styled capture: `CapturePaneOutputANSIContext` (`internal/tmux/session.go:1502`)
- Project and session: the spawn manifest's `ProjectDir`, and `LoadBestSessionAgentRegistry`

---

## Sprint 1: Nudge an idle spawned agent

### Testable function

"Leave a spawned agent idle and detached, send it two messages, and see one reminder listing both, with nothing more on later polls and no repeat from the Stop hook."

### Acceptance criteria

- [ ] `internal-monitor` polls every 30 seconds (**Assumption**), and types one reminder covering all new mail for an idle agent.
- [ ] Later polls type nothing for those messages, and the agent's next turn end doesn't repeat them (**One Telling**).
- [ ] A busy agent gets no nudge, and its Stop hook tells it instead.
- [ ] The monitor never types into a plain shell: a pane whose agent has crashed or exited is skipped. Reminder text holds other agents' subjects, so it must never reach a shell prompt.
- [ ] A failed send releases the claims, so the next poll tries again. Agent Mail or tmux failing is logged and leaves the monitor's other work running.

### Requirements

**Data:** Uses `told_records` from Loop 3, with `told_by` set to `idle_nudge`.

**API:**
- A new loop started from `runMonitor` (`internal/cli/monitor.go:44`), stopped with the monitor's context. It doesn't change the resilience monitor's crash handling.
- Order within one poll: find messages, check idle, check it's a live agent, claim, send.
- The check for a live agent uses the pane's current command and existing agent detection. The TDD step should confirm which existing check is the right one.
- Tokens come from the session registry. **Fail Open** applies.

**UI:** The reminder appears as a prompt in the agent's pane, following **Reminder Format**.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Idle with mail | Fake inbox with 2 messages, idle pane | One send listing both |
| Next poll | Run the poll again | No send |
| Busy | Busy pane | No send, no claim |
| Then idle | Busy, then idle on a later poll | One send |
| Told by hook | `ToldRecord` already exists | No send |
| Shell prompt | Pane's agent has exited | No send, and a log line |
| Send failure | Fake tmux send fails | Claims released, next poll retries |
| Agent Mail down | Fetch fails | No send, the loop continues |
| Message arrives mid-poll | Message appears after the fetch | Picked up by the next poll |
| Monitor restart | New monitor process, messages already told | No repeat |

### Human test script

1. Build and install the binary used for the monitor: `go build -o /tmp/ntm-loop5 ./cmd/ntm`.
2. In a scratch git repo, create a session: `/tmp/ntm-loop5 spawn nudgetest --cc=1`. Don't attach to it.
3. Wait until the agent has finished starting. Check with `/tmp/ntm-loop5 --robot-activity=nudgetest`.
4. Send two messages quickly: `/tmp/ntm-loop5 mail send nudgetest --to <agent> "first"`, then the same with `"second"`.
5. Wait about 30 seconds, then run `/tmp/ntm-loop5 --robot-tail=nudgetest --lines=40`.
6. Pass: one reminder appears, naming both subjects, and the agent runs `fetch_inbox`.
7. Wait another 2 minutes, and run the same tail.
8. Pass: no further reminder, and the agent's turn-end didn't repeat them.
9. Kill the agent process in its pane with Ctrl+C until the pane shows a shell, then send another message.
10. Pass: after 2 minutes, nothing has been typed at the shell prompt.

---

## Sprint 2: Nudge an added agent

### Testable function

"Add an agent with `ntm add`, leave it idle, send it mail, and see it nudged."

### Acceptance criteria

- [ ] The poll finds agents from the session's agent registry, not only from the spawn manifest, because `internal/cli/add.go` doesn't update the manifest.
- [ ] An added agent is nudged like a spawned one, with no monitor restart.
- [ ] Each agent is matched to its own pane by pane ID, so the reminder reaches the right one.
- [ ] A registry entry for a pane that no longer exists is skipped.
- [ ] Only Claude and Codex panes are nudged. The agent type comes from the pane's `@ntm_type` value.

### Requirements

**Data:** Reads `PaneIDMap` from `SessionAgentRegistry` (`internal/agentmail/session.go:405`), re-read on each poll. Loop 3, Sprint 6 puts added agents there.

**API:** The monitor's crash and restart tracking keeps using the manifest. This loop doesn't change it.

**UI:** None.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Added after start | Registry gains an entry while the monitor runs | Nudged on a later poll |
| Right pane | Two agents, mail to one | Only that pane gets the reminder |
| Dead pane | Registry entry with a missing pane | Skipped, no error loop |
| Other agent type | Gemini pane | Not nudged |
| Missing registry | No registry file yet | Poll does nothing, and the loop continues |

### Human test script

1. Create a session: `/tmp/ntm-loop5 spawn addnudge --cc=1`.
2. Add an agent: `/tmp/ntm-loop5 add addnudge --cc=1`.
3. Wait for both to be idle, and find the new agent's name with `/tmp/ntm-loop5 --robot-status`.
4. Send mail only to the new agent: `/tmp/ntm-loop5 mail send addnudge --to <new agent> "added nudge"`.
5. Wait about 30 seconds, and run `/tmp/ntm-loop5 --robot-tail=addnudge --lines=30`.
6. Pass: the reminder is in the new agent's pane only.

---

## Sprint 3: Hold back for recent keystrokes

### Testable function

"Attach to a session and type in the agent's pane, send it mail, and see no nudge until your keystrokes have been quiet for 60 seconds."

### Evidence check at sprint start

**tmux `client_activity` (scope Evidence Needed item 2).** Test it with a live attached client that types, with a client viewing a different pane, and with a client that's attached but idle. It's documented in tmux 3.5a, and a detached session has no clients, but it hasn't been tested live. `client_activity` is per client, so check how to tell which pane that client's keystrokes reach.

### Acceptance criteria

- [ ] With a client attached on this pane and active in the last 60 seconds (**Assumption**), the nudge waits for a later poll, and writes no told record.
- [ ] After 60 quiet seconds, the nudge is typed on the next poll.
- [ ] A client attached to the same session but on a different pane doesn't hold back this pane's nudge.
- [ ] A detached session is nudged as normal.
- [ ] A failure reading tmux client state means waiting for the next poll, not typing.

### Requirements

**Data:** Fills `recent_human_activity` in **PaneNudgeState**.

**API:** A new tmux helper that lists clients with `client_activity`, and their active pane. It follows the patterns in `internal/tmux/session.go`, such as `IsAttached` (`:1825`). ntm doesn't read `client_activity` today.

**UI:** None. The wait is logged once for each message batch, not on every poll.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Recent keys, this pane | Fake client active 20 seconds ago on this pane | No send |
| Quiet | Active 90 seconds ago | Send |
| Boundary | Active exactly 60 seconds ago | Pinned by the test, and treated as quiet |
| Other pane | Recent keys, client on another pane | Send |
| Two clients | One quiet, one recent, on this pane | No send |
| Detached | No clients | Send |
| Read failure | tmux command fails | No send |

### Human test script

1. Create a session: `/tmp/ntm-loop5 spawn typetest --cc=1`, and wait for the agent to be idle.
2. Attach to it, and stay on the agent's pane. Type a few characters, delete them, and repeat every 20 seconds.
3. In another shell, send mail: `/tmp/ntm-loop5 mail send typetest --to <agent> "typing test"`.
4. Keep typing for 2 minutes.
5. Pass: no reminder appears while you keep typing.
6. Clear the input line and stop typing.
7. Pass: within about 90 seconds, the reminder appears.

---

## Sprint 4: Hold back for unsent text

### Testable function

"Leave real text unsent in an idle pane, and see no nudge. Then leave only preview text showing, and see the nudge arrive."

### Evidence check at sprint start

**Preview text style (scope Evidence Needed item 3).** Capture real panes with `tmux capture-pane -e` on Claude Code and Codex, showing preview text, typed text, and an empty input line. Record the exact styling for each agent. If an agent type shows no reliable difference, that type uses the keystroke check alone, as the scope says.

### Acceptance criteria

- [ ] Typed text left on the input line holds back the nudge, however long it's been since the last keystroke.
- [ ] Preview text alone doesn't hold back the nudge.
- [ ] An empty input line doesn't hold back the nudge.
- [ ] An agent type with no reliable style difference reports `typed_text_present` as unknown, and the keystroke check decides alone.
- [ ] The check happens just before typing, and a capture failure means waiting for the next poll.

### Requirements

**Data:** Fills `typed_text_present` in **PaneNudgeState**, with true, false or unknown.

**API:** A pure function per agent type: styled pane capture in, true, false or unknown out. It's tested against real captures saved as test fixtures. It uses `CapturePaneOutputANSIContext`. Agent interfaces change, so unrecognized input should give unknown, not false. **Assumption.**

**UI:** None. A held nudge is logged once for each batch. A person who leaves text there for hours keeps the nudge waiting; the agent still learns at its next turn end. **Assumption.**

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Claude typed text | Saved capture with typed text | True |
| Claude preview text | Saved capture with preview text | False |
| Claude empty | Saved capture with an empty input | False |
| Codex typed, preview, empty | Saved captures | True, false, false, or unknown if no difference exists |
| Unknown layout | Capture from an unexpected screen | Unknown |
| Typed and preview together | Typed text with preview after it, if it occurs | True |
| Capture failure | Fake capture error | No send |

### Human test script

1. Create a session with one of each: `/tmp/ntm-loop5 spawn drafttest --cc=1 --cod=1`, and wait for both to be idle.
2. Attach, go to the Claude pane, and type `half a sentence` without pressing Enter.
3. Wait over 60 seconds with no more keys, and detach.
4. Send mail: `/tmp/ntm-loop5 mail send drafttest --to <claude agent> "draft test"`.
5. Wait 2 minutes, then attach.
6. Pass: the pane still shows only your text, with no reminder added, and nothing was sent.
7. Clear your text. Trigger preview text if it isn't showing: let the agent finish a short turn so it shows a suggestion.
8. Detach and wait about a minute.
9. Pass: the reminder arrives, and the preview text wasn't sent as part of it.
10. Repeat steps 2 to 9 on the Codex pane. If evidence showed Codex has no style difference, step 6 is expected to fail on that pane, and you should record that instead.

---

## Sprint 5: Nudge switch

### Testable function

"Turn the nudge switch off in config, and see idle agents get no reminder while the Stop hook still works."

### Acceptance criteria

- [ ] The nudge is on by default whenever Agent Mail is enabled (**On By Default**).
- [ ] With the switch off, the monitor doesn't poll Agent Mail for nudging and types nothing.
- [ ] With the switch off, the Stop hook still works. With only the hook off, the nudge still works.
- [ ] The monitor gets the setting even though its command line carries no `--config`.
- [ ] Turning the switch on takes effect on the next spawn.

### Requirements

**Data:** The setting is frozen into the spawn manifest, like `ReapOrphansOnExit` (`internal/resilience/manifest.go:43`), because the detached monitor's command line has no `--config`.

**API:** A new setting next to Loop 3's. **Assumption:** `idle_nudge_enabled`, in `[agent_mail]`, defaulting to true.

**UI:** Documented next to `stop_hook_enabled`.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Default | No setting | Nudge on |
| Off | Setting false | No poll, no send |
| Agent Mail off | `enabled = false` | No poll, no send |
| Hook off, nudge on | Both settings | Nudge works, no hook file |
| Custom config | Spawn with `--config` holding the setting | Monitor honors it |
| Old manifest | Manifest without the field | Treated as the default |

### Human test script

1. Add this to your ntm config: `[agent_mail]` with `idle_nudge_enabled = false`.
2. Create a session: `/tmp/ntm-loop5 spawn nonudge --cc=1`, and wait for the agent to be idle.
3. Send mail: `/tmp/ntm-loop5 mail send nonudge --to <agent> "no nudge"`.
4. Wait 2 minutes, and run `/tmp/ntm-loop5 --robot-tail=nonudge --lines=30`.
5. Pass: no reminder.
6. Give the agent slow work and send it another message, as in Loop 3, Sprint 3.
7. Pass: the Stop hook tells it when the turn ends.
8. Remove the setting, kill the session and spawn again, then repeat steps 2 to 4.
9. Pass: the reminder appears.
