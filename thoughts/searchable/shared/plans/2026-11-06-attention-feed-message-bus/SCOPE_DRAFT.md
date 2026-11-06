# Scope draft: attention feed as a message bus, with a Stop hook push and idle nudge

Sources read: the research document (pinned to ntm 90582053) and this repo at 70fe5915. Both loaded.
The research paths use `/home/maceo`; on this VPS they are `/home/debian`. The feed lines it cites still match.

## 1. Goal

**Problem.** Agents in ntm don't notice messages from each other unless they happen to check their inbox. The attention feed can't carry this yet:
- The dashboard's feed is in memory, so its mail events vanish on exit and no other process can read them (`internal/tui/dashboard/dashboard.go:2919`, `internal/cli/root.go:2488`).
- Nothing tells an agent about new mail when its turn ends or while it sits idle. ntm writes no Stop hook into a work directory.

**Who has it.** Claude and Codex agents spawned by ntm, and the human or orchestrator watching them.

**How we will know it is solved.**
- A message sent to a working ntm-spawned agent is surfaced when that turn ends, once per message, with no dashboard or `ntm serve` running.
- A message sent to an idle ntm-spawned agent reaches it through one batched nudge, with no dashboard or `ntm serve` running. The time limit depends on the monitor's poll rate (**assumed 30 seconds, not yet measured**).
- An agent can defer a message, is not reminded before the chosen time, and is reminded after it.
- A feed event written by one ntm process can be read by another process, and again after a restart, for the full 7 day retention.
- The hook takes under 1 second when there is no new mail (**target, not yet measured**).
- Re-running spawn never duplicates the hook or removes other entries in the settings file.
- After the migration, no repo on either machine registers the old Stop script, and mail reminders still work in those repos.

## 2. Deliverables

**Durable feed**
1. **Durable feed everywhere.** Every ntm process opens the SQLite store in `state.db` the first time it uses the feed, including the dashboard and `internal-monitor`. The projection refresh stays with `--robot-*` and `ntm serve`. Tests stay away from the real `state.db`.
2. **No silent drop to memory.** Today one failed store write moves a process to memory-only for the rest of its life (`internal/robot/attention_feed.go:534-552`). A long-running process must recover and report it.
3. **Events addressed to an agent.** Mail recipients are only inside the `details` JSON today. Add a recipient that can be queried, so an agent reads only its own events.
4. **Shared record of what each agent was told.** The Stop hook and the nudge use the same durable record, so an agent isn't told twice about one message.
5. **7 day retention.** The default changes from 1 hour (`attention_feed.go:101`) to 7 days.
6. **No duplicates after restart.** The dashboard tracks seen mail in memory (`seenMailAttentionIDs`) and appends without a duplicate check. Once durable, each restart would write all unread mail again.
7. **Cross-process delivery.** `Subscribe` handlers only see events appended in their own process. Long-running processes such as `ntm serve` need to pick up events other processes wrote.

**Turn-end push**
8. **Hook command in ntm.** A fast Go command that finds the agent, asks Agent Mail for its unread mail, records new messages in the feed, and tells the agent to check its inbox. Any new unread message triggers it once. It stops looping by using `stop_hook_active` and the record in item 4.
9. **Claude hook written at session creation.** `ntm spawn` (including `--worktrees`), robot spawn and `ntm add` merge the Stop hook into `<workdir>/.claude/settings.local.json`:
   - It keeps existing entries, doesn't add duplicates, and writes the file atomically.
   - It records the absolute path of the ntm binary, because hooks don't get an interactive shell's PATH. The Stripe plugin's hooks failed this way today when they couldn't find `node`.
   - It records the project key, because worktree panes run in a different folder from the project key given to Agent Mail (`internal/cli/spawn.go:2346`, `:2788`).
   - It stays in place after the session ends, and the path is added to `.git/info/exclude`.
10. **Codex hook written at session creation.** The same paths write a Codex turn-end hook using Codex's stable hooks feature, with the same file handling as item 9. You approve its trust manually, so ntm keeps Codex's trust check on and must tell you clearly when a project's hook still needs approval. New worktrees may each need approval. The exact Codex config format must be confirmed in planning.
11. **Identity, and safe in any session.** The command looks for a name in `AGENT_NAME`, then `MCP_AGENT_MAIL_AGENT_NAME`, then the Agent Mail breadcrumb files, and does nothing if it finds none. Panes that share a folder share the file and are told apart by name. Migrated repos keep reminders for sessions ntm didn't spawn.

**Idle agents**
12. **Idle nudge in `internal-monitor`.** The per-session monitor polls Agent Mail for its agents. When an agent is idle and has new mail, it types one reminder into the pane covering all new mail, and each message is announced once. It reuses existing functions (`determineAgentState`, `robot.IsLiveBusy`, `tmux.SendKeysForAgentDoubleEnter`) rather than new ones.
13. **Protect a human's typing.** Claude Code and Codex show preview text that looks pre-typed, so plain text on the input line proves nothing. The nudge waits for the next poll in either case:
    - Someone attached to the session is on that pane and was active in the last 60 seconds (tmux `client_activity`). **Assumption:** 60 seconds. This field hasn't been tested with a live attached client yet.
    - The input line holds text that isn't styled as preview text. This needs a test on each agent type, including what happens when no style difference can be found.
14. **Agent snooze.** An agent can defer a message to a chosen time with an ntm command, using the feed's existing snooze state (`SnoozedUntil`). The hook and nudge skip it until then, and the nudge reminds the agent afterward. An agent that defers without snoozing isn't reminded again.

**Shared behavior**
15. **Reminder text.** The Stop hook and nudge show each message's sender and subject, plus the instruction to run `fetch_inbox`. Subjects are written by other agents, so each one is cut to a single line with control characters removed, limited in length, and labeled as message data rather than instructions. **Assumption:** a long batch lists the first few messages and a count of the rest.
16. **On by default, with switches.** Both are active on every spawn when Agent Mail is enabled, with separate config settings to turn off the hook or the nudge. With the hook off, spawn doesn't write the hook files.

**Migration**
17. **Migration command and guide.** An ntm command that replaces old hook registrations with the Go command, previews by default, and needs a flag to change anything, plus a short guide for your local machine. It scans the folders you name, defaulting to `~/Dev` and `~/ntm_Dev`, and lists every match in the preview. It never deletes the scripts, and you commit the changes in each repo. **Assumption:** it replaces the Stop and `check_mail_on_close.sh` registrations and leaves `ensure_window_id.sh`, because the breadcrumb lookup depends on it.

**Quality**
18. **Tests and docs.** Tests for each item above, end-to-end tests from message to hook output and from message to idle nudge, and updates to `docs/attention-feed-contract.md`.

## 3. Out of scope

- Moving message bodies out of Agent Mail. The feed carries notices; Agent Mail keeps the messages.
- Changes to the `mcp_agent_mail` server.
- Delivery across machines. `state.db` is local to one machine, and the migration is run separately on each one.
- The global `~/.claude/settings.json`.
- Deleting the old hook scripts from `SAI/hooks`.
- Hooks for Gemini, which has no Stop equivalent.
- Replacing the `CLAUDE_CODE_HOOKS` handling for DCG and RCH.

## 4. Open questions and answers

1. **Where should the hook get its message information?**
   - **Answer:** Ask Agent Mail for this agent's unread mail, and record new messages in the feed.
2. **Which mail should make the agent keep working when its turn ends?**
   - **Answer:** Any new unread message, once per message.
3. **What about agents that are already idle?**
   - **Answer:** Include the idle nudge. Use ntm's existing functions for idle detection and for sending to a pane.
4. **Which ntm processes should use the durable feed?**
   - **Answer:** Every ntm process, opened the first time the feed is used.
5. **How long should events be kept?**
   - **Answer:** 7 days.
6. **Which spawn paths and agent types get the hook?**
   - **Answer:** Claude and Codex. **Assumption:** on all three paths: `ntm spawn` (including `--worktrees`), robot spawn and `ntm add`.
7. **Should we use the existing `check_critical_on_stop.sh`?**
   - **Answer:** Migrate to the Go command. This is a VPS, so a migration process is needed for the local machine.
8. **How should ntm handle the hook file in the user's repo?**
   - **Answer:** Leave the file after the session ends, and add it to `.git/info/exclude`.

Questions raised by those answers:

9. **Which process should run the idle nudge?**
   - **Answer:** `internal-monitor`.
10. **How should the nudge handle repeats?** Your correction: an agent that has been told hasn't ignored the message. It may act now or defer the work.
    - **Answer:** One batched reminder, each message announced once, plus an agent snooze for deferring to a chosen time.
11. **What form should the migration take?**
    - **Answer:** An ntm command that previews by default, plus a guide.
12. **How should ntm get the Codex hook trusted?**
    - **Answer:** You trust it manually in each project.

Round 2:

13. **How should the nudge protect a human's typing?** Your correction: Claude Code and Codex show preview text that looks pre-typed, so a plain unsent-text check would be wrong.
    - **Answer:** Check recent keystrokes and also the text style on the input line.
14. **What should the reminders say?**
    - **Answer:** Sender and subject.
15. **Should the hook and nudge be on by default?**
    - **Answer:** On, with a config switch.
16. **How should the migration command find repos?**
    - **Answer:** Scan the folders you name, defaulting to `~/Dev` and `~/ntm_Dev`.
