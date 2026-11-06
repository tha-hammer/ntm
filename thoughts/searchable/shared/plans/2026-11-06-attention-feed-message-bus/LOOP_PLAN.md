# Loop plan: attention feed message bus

Scope: `SCOPE.md` in this folder. Each sprint ends with one thing a person can try at its real entry point and see work.
Item numbers are the deliverables in `SCOPE.md` section 6. Item 18 (tests and docs) runs through every sprint; its end-to-end tests and doc update finish in Loop 9.

## 1. Loops

| # | Loop | Deliverables | Sprints | Needs |
|---|---|---|---|---|
| 1 | Durable feed | 1, 2, 5 | 3 | Nothing |
| 2 | Addressed mail notices | 3, 6 | 2 | Loop 1 |
| 3 | Claude turn-end push | 4, 8, 9, 11, 15, 16 (hook switch) | 7 | Loop 2 |
| 4 | Cross-process delivery | 7 | 1 | Loop 1 |
| 5 | Idle nudge | 12, 13, 16 (nudge switch) | 5 | Loop 3 |
| 6 | Agent snooze | 14 | 2 | Loop 5 |
| 7 | Codex turn-end push | 10 | 2 | Loop 3 |
| 8 | Hook migration | 17 | 3 | Loop 3 |
| 9 | Release journey | 18 | 1 | All earlier loops |

Total: 9 loops, 26 sprints. Loops 1 to 3 reach the scope's First Useful Result: a working Claude agent learns about mail at turn end.

## 2. Sprints

**Loop 1: Durable feed**
1. Run a dashboard until it shows new mail, quit it, then see the `mail.received` event from another process with `ntm --robot-events --events-category=mail`.
2. Write an event, wait over an hour, and see `ntm --robot-events` still return it, with an expiry 7 days out in `state.db`.
3. Hold a lock on `state.db` while a dashboard runs, see a warning, release the lock, and see later events from that same dashboard saved.

**Loop 2: Addressed mail notices**
1. Send mail to two agents, then list only one agent's events with `ntm --robot-events --events-recipient=<name>`.
2. Restart a dashboard with unread mail already recorded, and see no new events written for those messages.

**Loop 3: Claude turn-end push**
1. Run `ntm agent-hook stop` by hand as an agent with new mail, and see a continue decision with the sender and cleaned subject. Run it again and see no output.
2. Run the same command with no `AGENT_NAME`: with a breadcrumb file it finds the name, and with neither it stays silent.
3. Create a session with `ntm spawn`, send a working Claude agent mail, and watch the agent get told when its turn ends, with no dashboard or `ntm serve` running. Spawn again, and see one hook entry with other entries kept.
4. Do the same with `ntm spawn --worktrees`, and see the hook use the main project key and the file stay out of `git status`.
5. Do the same with robot spawn (`ntm --robot-spawn`).
6. Add an agent with `ntm add`, and see that it gets told at turn end too.
7. Turn the hook switch off in config, spawn, and see no hook file written and no reminder.

**Loop 4: Cross-process delivery**
1. Watch the `ntm serve` event stream, have a dashboard write a mail event, and see it arrive on the stream once.

**Loop 5: Idle nudge**
1. Leave a spawned agent idle and detached, send it two messages, and see one reminder listing both, with nothing more on later polls and no repeat from the Stop hook.
2. Add an agent with `ntm add`, leave it idle, send it mail, and see it nudged.
3. Attach to a session and type in the agent's pane, send it mail, and see no nudge until your keystrokes have been quiet for 60 seconds.
4. Leave real text unsent in an idle pane, and see no nudge. Then leave only preview text showing, and see the nudge arrive.
5. Turn the nudge switch off in config, and see idle agents get no reminder while the Stop hook still works.

**Loop 6: Agent snooze**
1. Run `ntm mail snooze <id> --until <time>`, and see no reminder mention that message before the time. See an error for another agent's message or a past time.
2. Leave the agent idle past the snooze time, and see one reminder that includes the message.

**Loop 7: Codex turn-end push**
1. Spawn a Codex agent, see the hook file written, and see ntm tell you that the project's hook still needs your approval.
2. Approve the hook, send the working Codex agent mail, and see it learn about the mail when its turn ends, as far as Codex's hooks allow.

**Loop 8: Hook migration**
1. Run `ntm agent-hook migrate` on this VPS, and see `mcp_agent_mail` listed with its planned change and nothing written.
2. Run it with `--apply`, see the file change with other entries kept, and run it again to see no change.
3. Follow the guide on your local machine for one repo, and see a plain session there still get mail reminders.

**Loop 9: Release journey**
1. Run the scope's Connected Release Journey by hand, and run the end-to-end tests for message to hook output and message to idle nudge, with `docs/attention-feed-contract.md` updated to match.

## 3. Open questions and answers

1. **When should the scope's 6 Evidence Needed checks run?**
   - **Answer:** At the start of the sprint that needs each one:
     - Writer contention: Loop 1, Sprint 3
     - Hook speed: Loop 3, Sprint 1
     - First-run folder trust: Loop 3, Sprint 3
     - tmux `client_activity`: Loop 5, Sprint 3
     - Preview text style: Loop 5, Sprint 4
     - Codex hook format: Loop 7, Sprint 1
2. **How should a person see one agent's events in Loop 2, Sprint 1?**
   - **Answer:** Add `--events-recipient` to `--robot-events`.
3. **Where should the human tests run?**
   - **Answer:** On this VPS, except Loop 8, Sprint 3 on your local machine.
4. **How should this work be tracked in beads?**
   - **Answer:** One epic per loop and one bead per sprint, created at the TDD step.
