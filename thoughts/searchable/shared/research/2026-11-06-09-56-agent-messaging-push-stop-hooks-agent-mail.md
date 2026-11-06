---
date: 2026-11-06T09:56:03-05:00
researcher: Claude Opus 5.5
git_commit: 905820535176783780bcf57035f4e9687c43ce24
branch: main
repository: ntm
topic: "Agent-to-agent communication in ntm: push and 'visual check' via Claude Code Stop hooks, message body via Agent Mail"
tags: [research, codebase, agent-mail, mcp-agent-mail, claude-code-hooks, stop-hook, notifications, signal-file, pane-identity, ntm-send, dashboard, attention-feed]
status: complete
last_updated: 2026-11-06
last_updated_by: Claude Opus 5.5
last_updated_note: "Added follow-up research tracing whether the attention feed is shared across processes"
---

# Research: Agent-to-agent communication — Stop-hook push + Agent Mail messages

**Date**: 2026-11-06T09:56:03-05:00
**Researcher**: Claude Opus 5.5
**Git Commit**: 905820535176783780bcf57035f4e9687c43ce24 (ntm, pushed; `main` == `origin/main`)
**Also read**:
- `/home/maceo/Dev/mcp_agent_mail` @ `1d5c82a7223c2c17b7190d1710415abf1f438255` (github.com/tha-hammer/mcp_agent_mail)
- `/home/maceo/.claude/hooks/` (not a git repo)

**Branch**: main
**Repository**: ntm (github.com/tha-hammer/ntm)

ntm permalink base: `https://github.com/tha-hammer/ntm/blob/905820535176783780bcf57035f4e9687c43ce24/<file>#L<line>`
mcp_agent_mail permalink base: `https://github.com/tha-hammer/mcp_agent_mail/blob/1d5c82a7223c2c17b7190d1710415abf1f438255/<file>#L<line>`

## Research Question

How to better support communication between agents in ntm. We need push-style notifications and reminders, like an async app gives a human, but shaped for LLMs:
- Claude Code **Stop hooks** for the "visual check" and "push" part.
- **Agent Mail** for the actual message.
- There may already be an Agent Mail hook that handles the push; look in the `.claude` folders for it.

## Summary

**1. An Agent Mail push hook already exists, and it is a three-part set in `~/.claude/hooks/`:**

| Script | Event | What it does |
|---|---|---|
| `ensure_window_id.sh` | SessionStart | Prints `MCP_AGENT_MAIL_WINDOW_ID` (a per-tmux-pane UUID cached in `/tmp/mcp-window-id-tmux-$TMUX_PANE`). Prints `MCP_AGENT_MAIL_AGENT_NAME` when it finds a breadcrumb under `$(git rev-parse --git-common-dir)/agent-mail/`. |
| `check_critical_on_stop.sh` | Stop | When the agent's signal file says `importance` is `urgent` or `high`, writes `[URGENT] You have priority messages waiting. Run fetch_inbox before going idle.` to stderr and exits 2. This keeps the agent from going idle. It fires once per signalled message id. |
| `check_mail_on_close.sh` | PostToolUse (Bash) | After any `bd close`, if a signal file exists, writes `Task complete. You have unread messages. Run fetch_inbox or check_in to read them.` and exits 2. |

**2. The push channel is a signal file written by the Agent Mail server.**
- Path: `~/.mcp_agent_mail/signals/projects/<slug>/agents/<Agent>.signal`.
- Written on every delivery to `to` and `cc` recipients (`storage.py:3091`, called from `app.py:4862-4878`).
- Deleted when the agent calls `fetch_inbox` or `check_in` (`app.py:8345-8347`, `app.py:8032-8035`).
- Off by default (`NOTIFICATIONS_ENABLED=false`, `config.py:402`). The running server on this machine (PID 1330, started Oct 21) has `NOTIFICATIONS_ENABLED=true`.
- The server also adds a `_mail_waiting` hint to the result of any non-inbox tool call while a signal file exists (`app.py:408-451`).

**3. Hook registration is per project, by hand, and outside ntm.**
- Global `~/.claude/settings.json`: not registered. Its six Stop hooks have no mail awareness.
- ntm's own `.claude/settings.json`: not registered.
- Registered in committed `.claude/settings.json` files of several repos, e.g. `zero-knowledge-proofs`, `CodeWriter9.0`, `silmari-writer`, `mcp_agent_mail`. ntm worktrees of those repos (e.g. `~/ntm_Dev/zk-5d-replay-ledger`) inherit the hooks through git.
- The `mcp_agent_mail` installer (`scripts/integrate_claude_code.sh`) installs a different mechanism: a rate-limited `check_inbox.sh` PostToolUse poller, and no Stop hook.
- Nothing in ntm writes or copies these hooks.

**4. What ntm does with Agent Mail today.**
- At spawn it registers one Agent Mail identity per pane (`registerSpawnedAgents`, `internal/cli/spawn.go:3223`; robot `resolveAgentMailIdentity`, `internal/robot/spawn.go:850`).
- It stores the name in its own files:
  - `~/.config/agent-mail/identity/<sha1>/<pane>` (`internal/agentmail/pane_identity.go:54`)
  - `/tmp/agent-mail-name.*` (`:187`)
  - the session `agent_registry.json`
- It launches the agent with `AGENT_NAME=<name>` (`spawn.go:2334`, `robot/spawn.go:769`).
- It types `register with agent mail. use <Name> for your name` into the pane (`spawn.go:2423`, CLI path only).
- It does not write the git-common-dir breadcrumbs, `MCP_AGENT_MAIL_AGENT_NAME`, or `/tmp/mcp-window-id-tmux-*` that the hooks read.

**5. ntm's own push and visual surfaces.**
- Pushing text into panes: `ntm send` and robot send, both via tmux `send-keys` / `paste-buffer` with double-Enter (`internal/tmux/session.go:1279`). Neither waits for idle.
- Mail state shown for humans and orchestrators:
  - dashboard badge `✉ N new (M urgent)`, polled every 30 s (`internal/tui/dashboard/dashboard.go:799`, `:4067-4084`)
  - header ticker
  - `mail.received` / `mail.ack_required` events in the attention feed (`dashboard.go:2917-2944`)
- ntm writes Claude Code hooks for spawned Claude agents only through the `CLAUDE_CODE_HOOKS` env var, and only a `PreToolUse` Bash entry for DCG/RCH (`spawn.go:2102-2157`, `internal/integrations/dcg/hooks.go:21-23`).
- No ntm code pushes mail text or "you have mail" into an agent pane.
- There is no generic reminder or idle-nudge scheduler.

## Detailed Findings

### 1. The existing Agent Mail hook set (`~/.claude/hooks/`)

All three scripts are executable, dated 2026-10-19, and use exit codes and stderr only. None writes JSON to stdout.

**`ensure_window_id.sh`** (SessionStart)
- **Window id.** Inside tmux, uses `TMUX_PANE` (falls back to `tmux display-message -p '#{pane_id}'`) and caches a UUID in `/tmp/mcp-window-id-tmux-<pane>`. Outside tmux, caches it in `/tmp/mcp-window-id-$$`.
- **Prints** `MCP_AGENT_MAIL_WINDOW_ID=<uuid>`.
- **Name lookup.** Looks for `$(git rev-parse --git-common-dir)/agent-mail/identity-<uuid>.json`, then the shared `identity.json`, and prints `MCP_AGENT_MAIL_AGENT_NAME=<agent_name>` from the one it finds.
- **Stdout.** Goes to Claude as SessionStart context; it does not set the shell environment of later hooks.

**`check_critical_on_stop.sh`** (Stop)
- **Identity order:**
  1. `$MCP_AGENT_MAIL_AGENT_NAME`
  2. Inside tmux, `identity-<window_id>.json` where window_id = `cat /tmp/mcp-window-id-tmux-$TMUX_PANE`
  3. Otherwise `identity.json`

  It exits 0 when no name is found.
- **Project** = `$MCP_AGENT_MAIL_PROJECT` or `$(pwd)`. Slug = realpath, lowercase, non-`[a-z0-9]` runs → `-`.
- **Signal dir** = `$MCP_AGENT_MAIL_SIGNALS_DIR` or `~/.mcp_agent_mail/signals`.
- **Decision.** Reads `message.importance`:
  - For `urgent` or `high`, it reads `message.id` and compares it with a `.<Agent>.<TMUX_PANE|PPID>.surfaced` file. If the id is new, it writes the id, prints `[URGENT|HIGH] You have priority messages waiting. Run fetch_inbox before going idle.` to stderr, and exits 2 (block stop).
  - `normal`/`low` → exit 0 (no interruption).
- **Comment vs server behaviour.** The comment on the surfaced-id file reads "nothing clears the signal file when the inbox is read, so blocking every time loops the agent forever". The server does delete the signal file on `fetch_inbox` / `check_in` (see §2).

**`check_mail_on_close.sh`** (PostToolUse, matcher `Bash`)
- Reads `tool_input.command` from stdin and acts only when it contains `bd close`.
- Uses the same identity, slug and signal path as the Stop hook.
- If the signal file exists at all, prints `Task complete. You have unread messages. Run fetch_inbox or check_in to read them.` and exits 2. Its comment says "non-blocking for PostToolUse".

**Where they are registered**

| Settings file | SessionStart | PostToolUse (Bash) | Stop |
|---|---|---|---|
| `~/.claude/settings.json` (global) | — | — | — (six other Stop hooks, none mail-aware) |
| `/home/maceo/Dev/ntm/.claude/settings.json` | `bd prime` only | — | — |
| `/home/maceo/Dev/zero-knowledge-proofs/.claude/settings.json` (git-tracked) | `~/.claude/hooks/ensure_window_id.sh` | `check_mail_on_close.sh` (timeout 5) | `check_critical_on_stop.sh` (timeout 5) |
| `~/ntm_Dev/zk-5d-replay-ledger/.claude/settings.json` (worktree of the above, tracked file) | same | same | same |
| `~/ntm_Dev/zk-design-gaps-2026-10-16-09-21/.claude/settings.json` | same | same | same |
| `/home/maceo/Dev/CodeWriter9.0/.claude/settings.json` | same | same | same |
| `/home/maceo/Dev/silmari-writer/.claude/settings.json` | same, plus `file_reservations active` | same, plus `file_reservations list` | same |
| `/home/maceo/Dev/mcp_agent_mail/.claude/settings.json` | repo-local copies in `mcp_agent_mail/.claude/hooks/` | repo-local copy | repo-local copy |

Notes:
- **How ntm worktrees get the hooks.** They inherit them only because the source repo commits `.claude/settings.json`; `git ls-files` shows it tracked in both `zero-knowledge-proofs` and the `zk-5d-replay-ledger` worktree.
- **Breadcrumbs exist there.** That repo's `<git-common-dir>/agent-mail/` holds `identity.json` plus three `identity-<uuid>.json` files, so agents there have registered with a window id.

**Existing global Stop hooks** (`~/.claude/settings.json`, lines 342-367): `LastResponseCache`, `ResponseTabReset`, `VoiceCompletion`, `DocIntegrity`, `ChecklistEnforcer`, `Broker`.
- A grep for `mail|signal|inbox` across `~/.claude/hooks/**/*.ts` finds nothing Agent-Mail related.
- `Broker` (`~/.claude/hooks/lib/broker.ts:107`) is a dispatcher that emits `{decision:"block", reason}` on Stop, with a block-count cap and a `stop_hook_active` loop guard.
- `SP0Probe.Stop.hook.ts:35` and `lib/sp0-probe.ts:5` use the same JSON form. `lib/sp0-probe.ts:5` describes it as auto-continuing the agent without a user prompt.

### 2. Agent Mail server: the signal file, the hint, the message (`/home/maceo/Dev/mcp_agent_mail/src/mcp_agent_mail/`)

**Signal writer** — `emit_notification_signal` (`storage.py:3091-3154`)
- Returns early if notifications are disabled (`:3111`).
- Debounces per `(slug, agent)` at `debounce_ms` (default 100 ms, in-process dict `_SIGNAL_DEBOUNCE`).
- Writes `signals_dir/projects/<project.slug>/agents/<agent>.signal` with plain `write_text` (overwrite; not atomic).
- File content:

  ```json
  {"timestamp": "...", "project": "<slug>", "agent": "<name>",
   "message": {"id": <int>, "from": "<sender>", "subject": "...", "importance": "low|normal|high|urgent"}}
  ```

- Errors are swallowed.

**Who triggers it**
- Only `_deliver_message` (`app.py:4645`), which emits for `to_agents + cc_agents`; bcc is excluded (`app.py:4862-4878`).
- That covers `send_message`, `reply_message`, `respond_contact`, and topic subscribers, who are expanded into to/cc before delivery (`app.py:6036-6067`).

**Clearing** — `clear_notification_signal` (`storage.py:3157`) unlinks the file. It is called only from:
- `fetch_inbox` (`app.py:8345-8347`), regardless of `urgent_only` / `limit` / `topic`
- `check_in` (`app.py:8032-8035`)

`mark_message_read`, `acknowledge_message` and `macro_start_session` do not clear it. Nothing deletes `.surfaced` files.

**Config** (`config.py:153-172`, `:401-406`; env or the server's `.env`)

| Key | Default |
|---|---|
| `NOTIFICATIONS_ENABLED` | false |
| `NOTIFICATIONS_SIGNALS_DIR` | `~/.mcp_agent_mail/signals` |
| `NOTIFICATIONS_INCLUDE_METADATA` | true |
| `NOTIFICATIONS_DEBOUNCE_MS` | 100 |

- The server reads `NOTIFICATIONS_SIGNALS_DIR`. The hooks read `MCP_AGENT_MAIL_SIGNALS_DIR`. Both default to the same directory.
- **Live state, 2026-11-06:**
  - the server process is `.venv/bin/python -m mcp_agent_mail.cli serve-http` (PID 1330)
  - `.env` contains `NOTIFICATIONS_ENABLED=true`
  - `~/.mcp_agent_mail/signals/projects/` has per-project directories, including `home-maceo-dev-ntm`
  - no `.signal` files were present at the time of the check

**Slug agreement.**
- Server slug for a plain project = `slugify(realpath(human_key))` (`app.py:1802`, `utils.py:175-179`), which matches the hook's slug.
- When `worktrees_enabled` is on, the slug becomes `{repo}-{sha1[:10]}`-style (`app.py:1843-1910`).
- `_ensure_project` may also return an older project whose stored path resolves to the same realpath (`app.py:2199-2209`).

**In-band hint** — `_check_signal_file` (`app.py:393`), wired at `app.py:756-761`. On any tool outside `_INBOX_TOOLS`, while a signal file exists:
- the server calls `ctx.info("[MAIL WAITING] …")`
- it sets `result["_mail_waiting"]`, with text `[MAIL WAITING] You have unread messages. … — call fetch_inbox to read them.` (`app.py:437-449`)

**Identity breadcrumbs** — `_write_identity_breadcrumb` (`app.py:3097-3209`)
- Writes `<git-common-dir>/agent-mail/identity-<window_id>.json`, or `identity.json` when there is no window id.
- Content: `{schema_version:2, agent_name, project_slug, registration_token, window_id, written_at}`, written atomically with mode 0600.
- The shared `identity.json` is seeded only if absent when a window id is given.
- Called from:
  - `register_agent` (window id from the `window_id` param at `app.py:5031`, or server env)
  - `macro_start_session` (param at `app.py:8598`)
  - `create_agent_identity` (server env `MCP_AGENT_MAIL_WINDOW_ID` only, `config.py:476`; no parameter)

**Message model and inbox tools**
- `Message` has `importance` (low, normal, high, urgent; free-form tolerated), `ack_required`, `thread_id` and `topic`. `MessageRecipient` has `kind` (to, cc, bcc), `read_ts` and `ack_ts`.
- Tools:
  - `fetch_inbox(project_key, agent_name, limit=20, urgent_only, include_bodies, since_ts, topic)` (`app.py:8274`): returns newest first; does not mark read; clears the signal file
  - `check_in` (`app.py:7967`): ledger status + inbox; clears the signal file
  - `acknowledge_message`, `mark_message_read`, `fetch_topic`, `subscribe_topic`, `summarize_recent`
- There is no MCP `resources/updated`, webhook or SSE push. The signal file and the `_mail_waiting` hint are the only push-side mechanisms.

**Installer** (`scripts/integrate_claude_code.sh`)
- Writes the project's `.claude/settings.json` with:
  - SessionStart: `file_reservations active`, `acks pending`
  - PreToolUse Edit: `file_reservations soon`
  - PostToolUse Bash: `check_inbox.sh` with inline `AGENT_MAIL_TOKEN`, interval 120 s
  - PostToolUse for `send_message` / `file_reservation_paths`: `list-acks` / `file_reservations list`
- Puts the MCP server entry in `.claude/settings.local.json` and `~/.claude/settings.json`.
- Has no Stop hook and no `ensure_window_id`.
- `scripts/hooks/check_inbox.sh` polls `fetch_inbox` over JSON-RPC, prints `INBOX REMINDER`, and always exits 0. Because it calls `fetch_inbox`, it also clears the signal file.
- Codex: `notify = [...]` turn-complete wrapper (`integrate_codex_cli.sh:187-234`).
- Gemini: SessionStart / BeforeTool / AfterTool hooks, no Stop equivalent (`integrate_gemini_cli.sh:201-264`).

### 3. ntm's Agent Mail integration (`internal/agentmail`, spawn, mail CLI)

**Client** (`internal/agentmail/client.go:157`)
- JSON-RPC `tools/call` to `http://127.0.0.1:8765/mcp/` (`AGENT_MAIL_URL` / `AGENT_MAIL_TOKEN` env override).
- Busy retry with 0.5 / 1 / 2 s backoff.
- Tools wrapped in `tools.go`:
  - `CreateAgentIdentity` (`:109`)
  - `RegisterAgent` (`:75`)
  - `SendMessage` (`:158`)
  - `FetchInbox` (`:235`)
  - `MarkMessageRead`, `AcknowledgeMessage`, reservations, contacts
  - `StartSession` (`macro_start_session`, `:722`; no non-test caller)
- `SendOverseerMessage` (`:872`) registers `HumanOverseer` and sends at `importance=high` with a directive preamble.

**Spawn registration** — CLI `registerSpawnedAgents` (`internal/cli/spawn.go:3223`); robot `setupAgentMailState` (`internal/robot/spawn.go:813`) and `resolveAgentMailIdentity` (`:850`)
- Reuses the name from the session registry, or calls `CreateAgentIdentity` (server picks the name).
- Writes:
  - `agentmail.WriteIdentity` → `~/.config/agent-mail/identity/<sha1(project)[:12]>/<sanitized pane id>` (plain text name; `CanonicalIdentityPath`, `pane_identity.go:54`)
  - `WriteLegacyCompatIdentity` → `/tmp/agent-mail-name.<sha1>.<pane>` (`:187`)
  - `~/.config/ntm/sessions/<session>/<slug>/agent_registry.json` (pane title / pane id → name + registration tokens)
- Launches with `AGENT_NAME=<name>` prefixed to the command (`spawn.go:2334`; `robot/spawn.go:769`) and sets the pane title to the name.
- CLI path only: a goroutine types `register with agent mail. use <Name> for your name` into the pane (`spawn.go:2423`).

**Identity contract seam.** Repo-wide grep finds no use in ntm of:
- `MCP_AGENT_MAIL_AGENT_NAME`, `MCP_AGENT_MAIL_PROJECT`, `MCP_AGENT_MAIL_WINDOW_ID`
- `git-common-dir`
- `identity.json`
- `/tmp/mcp-window-id`

ntm's identity store and the hooks' identity sources are different files with different formats:

| Who | File | Format | Key |
|---|---|---|---|
| ntm (`pane_identity.go`) | `~/.config/agent-mail/identity/<sha1(project)[:12]>/<pane>`, `/tmp/agent-mail-name.*` | plain-text name | project sha1 + tmux pane id |
| Agent Mail server (`_write_identity_breadcrumb`) | `<git-common-dir>/agent-mail/identity[-<window_id>].json` | JSON, `agent_name` | window id (UUID) |
| hooks (`ensure_window_id.sh`) | `/tmp/mcp-window-id-tmux-<pane>` | UUID | tmux pane id |

`ResolveIdentity` (`pane_identity.go:125`) reads ntm's paths plus legacy `~/.claude/agent-mail/identity.<pane>`. It does not read the git-common-dir breadcrumbs.

**Commands**
- `ntm mail send|inbox|read|ack` (`internal/cli/mail.go:24`; `runMailSendOverseer` at `:770`)
- `ntm message` (`internal/cli/message.go`, `UnifiedMessenger` over Agent Mail and bd)
- `--robot-mail-check` (`internal/robot/mail_check.go:287`)
- `--robot-mail`

None of these send keys to a pane.

**Agent instructions**
- `AGENTS.md` template (`internal/cli/agents_template.go:52-55`): "ensure_project then register_agent … fetch_inbox, acknowledge_message, send_message for coordination". No polling cadence.
- `swarm` session template (`internal/templates/schema.go:1413-1417`): "Check inbox regularly for coordination messages".

**Other senders**
- `coordinator` sends assignment, conflict and digest mail (`coordinator/assign.go:224`, `conflicts.go:272,323`, `digest.go:173`). `AssignWork`, `NegotiateConflict` and `NotifyConflict` have no non-test callers. Digest runs only with `send_digests=true`.
- `pipeline` `mail_send` / `mail_inbox_check` steps (`pipeline/mail_steps.go:93`).

### 4. ntm's push into panes, idle detection, and visual surfaces

**Push primitive** (`internal/tmux/session.go`)
- `SendKeysForAgentDoubleEnter` (`:1279`): text, wait 1 s, Enter, wait 0.5 s, Enter.
- `needsBufferSend` (`:1226`) chooses `load-buffer` + `paste-buffer -p` (bracketed paste) versus chunked `send-keys -l`, per agent type and content.
- `DisplayMessage` (`:1339`) shows text in the tmux status line.

**`ntm send`** (`internal/cli/send.go:1101` → `sendPromptToPane` `:3074`)
- Runs redaction, routing, dedup and `pre-send` / `post-send` ntm command hooks, then delivers.
- No idle check or queue. `--pace-dispatch` is advisory only.
- Robot `GetSend` (`internal/robot/robot.go:6515`) is the same. `GetSendAndAck` (`internal/robot/ack.go:486`) polls for an ack after sending.

**Idle and busy classification**
- `robot.StateClassifier` (`internal/robot/activity.go:639`; states GENERATING / WAITING / THINKING / ERROR / STALLED)
- `robot.IsLiveBusy` (`activity.go:844`)
- `determineAgentState` (`internal/cli/assign.go:1211`)
- `status.UnifiedDetector` (coordinator)

Idle gating before a push exists only in:
- spawn init prompt (`waitForPaneAgentIdle`, `spawn.go:4654`)
- `ntm assign`
- `swarm.PromptInjector.sendToPane` (`internal/swarm/prompt_injector.go:252`)
- Codex `/goal`

**Coordinator** (`internal/coordinator/coordinator.go:294` monitorLoop, every 5 s)
- Classifies panes and emits `agent.idle` / `agent.busy` / `agent.error` / `agent.recovered` events (`:464`).
- Does not read inboxes and makes no tmux send calls.

**Notifications and events**
- `internal/notify` (`notify.go:304`) has no mail event type. Channels: desktop `notify-send`, webhook, shell, log, `.ntm/human_inbox` filebox.
- `internal/events` and `internal/webhook` bridge agent and session events to HTTP.
- `alerts.AlertMailBacklog` is declared, but no code in `internal/alerts` generates it.

**ntm command hooks** (`internal/hooks/config.go:36`, `executor.go:88`)
- pre/post spawn, send, add, create, kill (shutdown names defined but not fired).
- Shell commands with `NTM_*` env. These are ntm's own hooks, separate from Claude Code hooks.

**Claude Code hooks written by ntm**
- For Claude agents, spawn and add build JSON with only `PreToolUse` → `Bash` → `dcg` (`internal/integrations/dcg/hooks.go:68`).
- It is passed as env var `CLAUDE_CODE_HOOKS` (`spawn.go:2149`, `add.go:620`).
- The string `CLAUDE_CODE_HOOKS` occurs in the installed `claude` binary. How Claude Code reads it was not confirmed. `claude --help` documents `--settings <file-or-json>` as the supported way to load additional settings.
- `ntm setup --hooks` installs `~/.claude/hooks/PreToolUse/ntm-safety.sh` plus git/rm wrappers (`internal/cli/safety.go:516`). It does not edit any `settings.json`.

**Detached monitor** (`internal/cli/monitor.go`, `internal/resilience/monitor.go`)
- Health, crash restart and rate-limit status-line hints. No mail.
- The restart re-sends the launch command through an injected `sendFunc(agent.PaneID, paneCmd, true)` (`resilience/monitor.go:841`).

**Dashboard — the existing visual notification for humans**
- `fetchAgentMailInboxes` (`internal/tui/dashboard/dashboard.go:799`): every 30 s (`MailInboxRefreshInterval`, `model.go:499`), 4 workers, limit 5, no bodies.
- Per-pane badge `✉ N new` / `(M urgent)` (`:4067-4084`).
- Ticker `Mail: N unread` (`panels/ticker.go:304-326`).
- Inbox detail behind the `i` key (`:5834-5883`).
- Publishes unseen messages to the attention feed as `mail.received` / `mail.ack_required` (`dashboard.go:2938-2942` → `robot/attention_feed.go:2059`, `:2082`).
- `--robot-attention` and `--robot-wait` with `mail_pending` / `mail_ack_required` (`robot/wait.go:94-95`) consume those event types.
- `GetAttentionFeed()` (`attention_feed.go:2956`) returns an in-memory feed by default. The dashboard never swaps in the SQLite-backed feed, so its mail events stay in the dashboard process (see Follow-up Research below).

**Reminder-like mechanisms that exist**

| Mechanism | Trigger | Text |
|---|---|---|
| Dashboard compaction recovery (`internal/status/recovery.go:164`, plain `tmux.SendKeys`) | compaction detected while dashboard runs; 30 s cooldown, 5 per pane | "Reread AGENTS.md …" |
| `ntm assign --watch` (`internal/cli/assign.go`) | every 30 s, idle agents | "Please work on bead …" |
| `ntm send --batch --delay` | fixed interval from a file | user-supplied |
| Resilience rate-limit hint | rate limit detected | tmux status line `ntm rotate …` |

There is no generic idle nudge, periodic reminder, or mail-driven pane injection.

## Code References

Claude Code hooks (not in a repo):
- `~/.claude/hooks/ensure_window_id.sh` — per-pane window UUID + name from breadcrumb (SessionStart)
- `~/.claude/hooks/check_critical_on_stop.sh` — Stop block on urgent/high signal, once per message id
- `~/.claude/hooks/check_mail_on_close.sh` — PostToolUse on `bd close`
- `~/.claude/hooks/lib/broker.ts:107` — Stop `{decision:"block"}` dispatcher with loop guard

mcp_agent_mail (`src/mcp_agent_mail/`):
- `storage.py:3091` — `emit_notification_signal`
- `storage.py:3157` — `clear_notification_signal`
- `app.py:4862-4878` — signal emit in `_deliver_message` (to + cc)
- `app.py:8345-8347` — `fetch_inbox` clears signal
- `app.py:8032-8035` — `check_in` clears signal
- `app.py:393`, `:437-449` — `_check_signal_file`, `_mail_waiting` hint
- `app.py:3097` — `_write_identity_breadcrumb`
- `app.py:5031`, `:8598` — `window_id` params on `register_agent`, `macro_start_session`
- `config.py:401-406` — `NOTIFICATIONS_*` settings
- `config.py:476` — `MCP_AGENT_MAIL_WINDOW_ID`
- `scripts/integrate_claude_code.sh` — installer (check_inbox poller, no Stop hook)
- `scripts/hooks/check_inbox.sh` — rate-limited fetch_inbox poller

ntm:
- `internal/agentmail/client.go:157` — `NewClient`
- `internal/agentmail/tools.go:109`, `:158`, `:235`, `:872` — `CreateAgentIdentity`, `SendMessage`, `FetchInbox`, `SendOverseerMessage`
- `internal/agentmail/pane_identity.go:54`, `:125`, `:187` — `CanonicalIdentityPath`, `ResolveIdentity`, `WriteLegacyCompatIdentity`
- `internal/agentmail/session.go:285` — `RegisterSessionAgent`
- `internal/cli/spawn.go:3223` — `registerSpawnedAgents`
- `internal/cli/spawn.go:2334` — `AGENT_NAME=` prefix
- `internal/cli/spawn.go:2423` — bootstrap register prompt
- `internal/cli/spawn.go:2102-2157` — `CLAUDE_CODE_HOOKS` built for Claude panes
- `internal/robot/spawn.go:729`, `:813`, `:850` — `launchAgent`, `setupAgentMailState`, `resolveAgentMailIdentity`
- `internal/integrations/dcg/hooks.go:68` — `GenerateHookConfig` (PreToolUse only)
- `internal/cli/mail.go:24`, `:770` — `ntm mail`, `runMailSendOverseer`
- `internal/robot/mail_check.go:287` — `GetMailCheck`
- `internal/cli/send.go:1101`, `:3074` — `runSendInternal`, `sendPromptToPane`
- `internal/tmux/session.go:1226`, `:1279`, `:1339` — `needsBufferSend`, `SendKeysForAgentDoubleEnter`, `DisplayMessage`
- `internal/robot/robot.go:6515` — `GetSend`
- `internal/robot/ack.go:486` — `GetSendAndAck`
- `internal/robot/activity.go:639`, `:844` — `StateClassifier`, `IsLiveBusy`
- `internal/cli/assign.go:1211` — `determineAgentState`
- `internal/cli/spawn.go:4654` — `waitForPaneAgentIdle`
- `internal/swarm/prompt_injector.go:252` — `sendToPane` (wait-for-ready then send)
- `internal/coordinator/coordinator.go:294`, `:464` — `monitorLoop`, `emitEvent`
- `internal/coordinator/digest.go:153`, `assign.go:110` — `SendDigest`, `AssignWork`
- `internal/notify/notify.go:304` — `Notify`
- `internal/hooks/config.go:36`, `executor.go:88` — ntm command hook events / runner
- `internal/cli/safety.go:516` — `runSafetyInstall` (`ntm setup --hooks`)
- `internal/tui/dashboard/dashboard.go:799`, `:2938-2942`, `:4067-4084` — inbox poll, attention publish, badge
- `internal/robot/attention_feed.go:2059`, `:2082`, `:2956` — `PublishMailPending`, `PublishMailAckRequired`, `GetAttentionFeed`
- `internal/robot/wait.go:94-95` — `mail_pending` / `mail_ack_required` wait conditions
- `internal/status/recovery.go:164` — compaction-recovery prompt push
- `internal/cli/agents_template.go:137` — `renderAgentsTemplate`

Citation verification:
- **ntm.** Semgrep verifier, 41 citations: 40 `verified`. The one fallback (`resilience/monitor.go:841`) was corrected after a targeted read: the call there is `sendFunc`, not `SendKeys`.
- **mcp_agent_mail.** Python and hook lines were confirmed by targeted reads.

## Architecture Documentation

- **Three separate halves, joined by files.**

  | Half | Owner | What it covers |
  |---|---|---|
  | Message | Agent Mail server | SQLite + git archive |
  | Push signal | Agent Mail server → file under `~/.mcp_agent_mail/signals` | — |
  | Check | Claude Code hook scripts | read that file on SessionStart / PostToolUse / Stop |

  ntm is in none of these three paths. It manages names, panes and a human-facing dashboard.
- **Identity is the join key, held in three stores.** ntm's per-pane name files, the server's git-common-dir breadcrumbs, and the hooks' tmux-pane window-id cache. The hooks find a name only through `$MCP_AGENT_MAIL_AGENT_NAME` or the breadcrumbs.
- **Hook output protocol in use.** Both protocols are used on this machine:
  - exit code 2 + stderr, for all three mail hooks
  - JSON `{"decision":"block","reason":…}` on stdout, for Broker / SP0Probe
- **Hook loop guards in use.**
  - The mail Stop hook dedups by message id in a `.surfaced` file.
  - Broker uses `stop_hook_active` and a block-count cap.
- **Push into a pane** is always tmux keystrokes (`send-keys` or bracketed `paste-buffer`) followed by Enter. Idle gating is opt-in per caller, never in `ntm send`.
- **Visual notification for humans** is poll-based: the dashboard polls every 30 s and fans results out to the attention feed. No component pushes to an agent's context except the Claude Code hooks and the server's `_mail_waiting` field on tool results.

## Workflow Closure Map

**Behavior studied:** a message sent through Agent Mail becomes noticeable to the receiving agent (LLM push) and to the human or orchestrator (visual check). There are two sibling chains that share the message write.

**Production trigger used for both:** `ntm mail send` (`runMailSendOverseer`, `internal/cli/mail.go:770` → `agentmail.Client.SendOverseerMessage`, `tools.go:872`). Any agent's own `send_message` MCP call reaches the same server node.

### Chain A — agent push (Agent Mail signal file → Claude Code Stop hook)

| Depth | Node | Location | Label | Adds/changes |
|---|---|---|---|---|
| 0 | `ntm mail send` → `SendOverseerMessage` → server `send_message` (SOURCE + ENTRYPOINT) | `internal/cli/mail.go:770`; `internal/agentmail/tools.go:872` | production-called (cobra `newMailSendCmd`, `mail.go:52`) | not yet specified |
| 1 | `_deliver_message` → `emit_notification_signal` writes `<Agent>.signal` | mcp_agent_mail `app.py:4862-4878`, `storage.py:3091` | production-called (inside the server request) | not yet specified |
| 2 | Claude Code Stop hook `check_critical_on_stop.sh` reads the signal, exits 2 with stderr text (OBSERVABLE) | `~/.claude/hooks/check_critical_on_stop.sh` | registered per project only (see §1 table); not registered for `/home/maceo/Dev/ntm` | not yet specified |

Edge evidence:
- **0→1** — HTTP JSON-RPC `tools/call send_message`, synchronous, cross-process.
  - Contract: project_key, sender, to/cc, subject, body, importance (`high` for overseer).
  - Runtime: `registration_token` / `sender_token` attached by `tools.go:22-51`.
  - Errors: signal write errors are swallowed (`suppress(Exception)`). The 100 ms debounce can keep the first message's id/importance.
  - Test: `registration_token_test.go` `TestSendOverseerMessage_MCPPath`, against a fake server.
- **1→2** — asynchronous. The file sits until the receiving agent ends a turn and Claude Code fires Stop.
  - Contract: signal JSON `message.importance` and `message.id`.
  - Identity: `$MCP_AGENT_MAIL_AGENT_NAME` or the git-common-dir breadcrumb, which ntm does not write.
  - Gating: fires only for `urgent` / `high`, once per id (`.surfaced`).
  - Downstream: the agent then calls `fetch_inbox`, which deletes the signal (`app.py:8345-8347`).
  - Tests: none found that exercise this hook (no test directory for these scripts). Server-side signal tests are in mcp_agent_mail `tests/test_tool_filter_and_notifications.py::TestNotifications`.

Sibling observable on the same signal: `check_mail_on_close.sh` (PostToolUse, `bd close` only, any importance).

### Chain B — visual check (dashboard poll → attention feed)

| Depth | Node | Location | Label | Adds/changes |
|---|---|---|---|---|
| 0 | `ntm mail send` → server `send_message` (SOURCE + ENTRYPOINT) | `internal/cli/mail.go:770` | production-called | not yet specified |
| 1 | `fetchAgentMailInboxes` (30 s poll; `FetchInbox` limit 5) | `internal/tui/dashboard/dashboard.go:799` | production-called (dashboard tick, `dashboard.go:4920`) | not yet specified |
| 2 | `PublishMailPending` / `PublishMailAckRequired` → attention feed | `internal/robot/attention_feed.go:2059`, `:2082` (called `dashboard.go:2940-2942`) | production-called (dashboard only) | not yet specified |
| 3 | attention feed replay read (OBSERVABLE) | `internal/robot/attention_feed.go:944` `(*AttentionFeed).Replay` | production-called (`--robot-attention`, `--robot-wait mail_pending`) | not yet specified |

Edge evidence:
- **0→1** — asynchronous poll.
  - Driver: `fetchAgentMailInboxes`, only while a dashboard is running.
  - Uses the session registry name per pane (`LoadBestSessionAgentRegistry`).
  - Read side effect: `fetch_inbox` clears the agent's signal file on the server, so a dashboard poll can remove the file the Stop hook reads.
- **1→2** — synchronous, in process, deduped by `seenMailAttentionIDs`.
- **2→3** — in-memory journal by default (`GetAttentionFeed`, `attention_feed.go:2956`). Cross-process visibility was not traced.
- **Tests** found for the publish functions: none named for the dashboard → attention edge. The `mail_pending` wait conditions live in `internal/robot/wait.go`.

`highest_new_connector`: **not yet determined**. The request is research ahead of a design; no node has been chosen to add or change.

Negative evidence:
- No ntm code writes `MCP_AGENT_MAIL_AGENT_NAME`, git-common-dir breadcrumbs, or Claude `Stop` / `PostToolUse` hooks.
- No ntm code injects mail into panes.
- `alerts.AlertMailBacklog` has no generator.
- `coordinator.AssignWork`, `NegotiateConflict` and `NotifyConflict` are test-only.
- The Semgrep closure mapper returns `llm-fallback` for Go directory targets (seen in this session's earlier run). Labels above come from grep plus targeted reads.

### ClosureMap (structured — derive() input) — Chain A, agent push via Stop hook

```json
{
  "behavior": "When a message is sent through Agent Mail (e.g. ntm mail send) with high or urgent importance, the receiving Claude Code agent is blocked from going idle by its Stop hook and told to run fetch_inbox.",
  "git_commit": "905820535176783780bcf57035f4e9687c43ce24",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "ntm_mail_send_overseer", "module": "internal/cli+internal/agentmail", "is_entrypoint": true, "adds_or_changes": false, "read_path": null, "seedable_store": "agentmail.Client.SendOverseerMessage" },
    { "id": "agent_mail_emit_notification_signal", "module": "external:mcp_agent_mail@1d5c82a7 storage.py", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "claude_stop_hook_check_critical", "module": "external:~/.claude/hooks", "is_entrypoint": false, "adds_or_changes": false, "read_path": "check_critical_on_stop.sh", "seedable_store": null }
  ],
  "edges": [
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": true, "cross_boundary": true, "driver": "check_critical_on_stop.sh" }
  ]
}
```

Resolvability note:
- `agentmail.Client.SendOverseerMessage` resolves in ntm at the pinned SHA.
- Node 1 resolves in mcp_agent_mail at `1d5c82a7`.
- `check_critical_on_stop.sh` (read_path and driver) is a script under `~/.claude/hooks/`, outside any repo. It does not resolve inside the ntm repo at the pin.
- This map is cross-repo and fails safe to BLOCKING downstream.

### ClosureMap (structured — derive() input) — Chain B, dashboard visual check

```json
{
  "behavior": "When a message is sent through Agent Mail to an ntm-managed agent, a running ntm dashboard shows it as a per-pane mail badge and publishes a mail.received or mail.ack_required attention event readable via --robot-attention.",
  "git_commit": "905820535176783780bcf57035f4e9687c43ce24",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "ntm_mail_send_overseer", "module": "internal/cli+internal/agentmail", "is_entrypoint": true, "adds_or_changes": false, "read_path": null, "seedable_store": "agentmail.Client.SendOverseerMessage" },
    { "id": "dashboard_fetch_agent_mail_inboxes", "module": "internal/tui/dashboard", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "attention_publish_mail", "module": "internal/robot", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "attention_feed_replay", "module": "internal/robot", "is_entrypoint": false, "adds_or_changes": false, "read_path": "robot.AttentionFeed.Replay", "seedable_store": null }
  ],
  "edges": [
    { "is_async": true, "cross_boundary": true, "driver": "fetchAgentMailInboxes" },
    { "is_async": false, "cross_boundary": true, "driver": null },
    { "is_async": false, "cross_boundary": false, "driver": null }
  ]
}
```

All `adds_or_changes` are `false` because no modification has been chosen yet.

### Closure adapters (staged proposals)

Two adapters are staged next to this document, one per ClosureMap. Neither is wired into the repo.
- `2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.closure-adapter.py` — Chain A
- `2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.dashboard.closure-adapter.py` — Chain B

Chain A adapter:

```python
"""Closure adapter (STAGED PROPOSAL — not wired into the repo).
Derived from the ClosureMap for: Agent Mail high/urgent message -> Claude Code Stop hook push.
Pin: ntm 905820535176783780bcf57035f4e9687c43ce24 (+ mcp_agent_mail 1d5c82a7).
Promote into /home/maceo/Dev/ntm and complete each TODO(promote) before use.
Speaks the 7-op contract apps/closure-oracle already talks to (mock_adapter.py).
"""
import http.server, json, sys
ASYNC_EDGES = ["signal_to_stop_hook"]             # driver: run check_critical_on_stop.sh
CONNECTOR = {e: True for e in ASYNC_EDGES}
SINK = []                                          # Phase-0 /seed_sink target

def handle(op, p):
    if op == "/reset":        SINK.clear(); CONNECTOR.update({e: True for e in ASYNC_EDGES}); return {"ok": True}
    if op == "/set_connector": CONNECTOR[p["edge"]] = p["enabled"]; return {"ok": True}
    if op == "/seed_sink":     SINK.append(p["value"]); return {"ok": True}
    if op == "/seed":
        # TODO(promote): ensure project + receiving agent exist via agentmail.Client
        #   (EnsureProject / CreateAgentIdentity, internal/agentmail/tools.go:54, :109)
        return {"ok": True}
    if op == "/trigger":
        # TODO(promote): agentmail.Client.SendOverseerMessage(...) with importance high
        #   (internal/agentmail/tools.go:872; CLI entry runMailSendOverseer internal/cli/mail.go:770)
        return {"ok": True}
    if op == "/drive":
        if not CONNECTOR.get(p["edge"], True): return {"ok": True}  # oracle disabled = red-at-seam
        # TODO(promote): run ~/.claude/hooks/check_critical_on_stop.sh with TMUX_PANE /
        #   MCP_AGENT_MAIL_AGENT_NAME set for the receiver; capture exit code + stderr
        return {"ok": True}
    if op == "/observe":
        # TODO(promote): return {"exit": <code>, "stderr": <text>} from the hook run above
        return {"ok": True, "value": json.dumps(SINK)}
    return {"ok": False, "error": "unknown op"}

class Hn(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        out = json.dumps(handle(self.path, json.loads(self.rfile.read(n) or "{}"))).encode()
        self.send_response(200); self.send_header("Content-Length", str(len(out))); self.end_headers(); self.wfile.write(out)
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), Hn).serve_forever()
```

The Chain B adapter uses the same skeleton:
- `ASYNC_EDGES = ["dashboard_inbox_poll"]`
- `/drive` → `fetchAgentMailInboxes` (`internal/tui/dashboard/dashboard.go:799`)
- `/observe` → `robot.GetAttentionFeed().Replay(cursor, limit)` (`internal/robot/attention_feed.go:944`, `:2956`), filtered to `mail.received` / `mail.ack_required`

## Historical Context (from thoughts/)

Only `thoughts/searchable/` exists in this repo, so the paths below are the real paths.

- `thoughts/searchable/shared/research/2026-10-22-11-29-add-identity-callers.md` — prior research on pane identity callers (window_id / pane identity / mail).
- `thoughts/searchable/shared/plans/2026-10-22-11-38-tdd-add-pane-identity.md`, `…-add-pane-identity-bd-issues.md` — TDD plan and bead breakdown for pane identity.
- `thoughts/searchable/shared/research/2026-10-22-08-10-per-pane-session-id-discovery.md`, `thoughts/searchable/shared/plans/2026-10-22-08-19-tdd-pane-id-record.md` (+ `-REVIEW.md`) — per-pane session id record.
- mcp_agent_mail beads `mcp_agent_mail-9nj` / `mcp_agent_mail-eft` (closed): "Per-terminal UUID generation and hook setup". They created `ensure_window_id.sh` and reference plan `~/.claude/plans/serene-fluttering-shannon.md`, which no longer exists (`~/.claude/plans/` is empty).
- `docs/attention-feed-contract.md` (ntm) — the attention / notification contract that the `mail.*` events use.
- No document found mentions `check_critical_on_stop`, `check_mail_on_close` or `ensure_window_id` by name.

## Related Research

- `thoughts/searchable/shared/research/2026-11-06-09-41-spawn-admission-system-check-seams-and-grammar.md` — same-day research on the spawn path where per-pane Agent Mail registration also happens.

## Open Questions

- **Which agents are in scope?** Codex has a turn-complete `notify` hook from the mcp_agent_mail installer, and Gemini has no Stop equivalent.
- **Should normal-priority mail push at Stop?** Today it is surfaced only after `bd close` or through the `_mail_waiting` field.
- **Exact semantics of `CLAUDE_CODE_HOOKS`.** It was not confirmed whether it merges with or replaces settings-file hooks. `--settings <json>` is the documented flag.
- **Does an attention event cross processes?** Resolved in the follow-up section below: dashboard mail events do not; robot and serve mail events do, through `state.db`.
- **The `.surfaced` file is never cleaned up.** After `fetch_inbox` deletes the signal file, a new urgent message with a new id passes the dedup check; repeated ids are not re-surfaced.
- **Open beads related to messaging tests:**
  - `ntm-hzof` — create test-send.sh for message delivery
  - `ntm-nxg0` — test ntm send with real message delivery
  - `ntm-lmto` — test cross-session agent communication

## Follow-up Research 2026-11-06T10:30-05:00 — Is the attention feed shared across processes?

**Question:** do mail events that one ntm process publishes to the attention feed reach another process, for example a dashboard publishing and an orchestrator running `ntm --robot-attention`?

**Answer:**
- **Dashboard mail events: no.** They live only in the dashboard process's memory.
- **Mail events from `--robot-*` commands and `ntm serve`: yes.** They are a different, project-level kind of event, and they are shared through the SQLite file `~/.config/ntm/state.db`.

### How each process gets its feed

| Process | Feed it uses | Where set | Persisted? |
|---|---|---|---|
| `ntm dashboard` / TUI | lazily created in-memory feed (`NewAttentionFeed(DefaultAttentionFeedConfig())`) | `GetAttentionFeed()` `internal/robot/attention_feed.go:2956-2962` | No |
| any `ntm --robot-*` command (except `--robot-overlay`), or `--schema` | `NewAttentionFeed(..., WithAttentionStore(store))` | `initializeRobotPersistence` `internal/cli/root.go:2517-2541`, called from `PersistentPreRunE` at `root.go:202-206` when `shouldInitializeRobotPersistence` (`root.go:2488-2515`) is true | Yes, `state.db` |
| `ntm serve` | same store-backed feed | `runServe` `internal/cli/serve.go:99-103` | Yes, `state.db` |

- **Store path.** `state.DefaultPath()` (`internal/state/store.go:42-53`) resolves to `state.db` next to the ntm config file, `$XDG_CONFIG_HOME/ntm/state.db`, or `~/.config/ntm/state.db`.
- **Writes.** `Append` (`attention_feed.go:524-545`) writes to the store first, and the store assigns the cursor. If a store write fails once, the feed switches new events to journal-only mode.
- **Reads.** `Replay` (`attention_feed.go:944-950`) reads from SQLite whenever a store is set (`replayFromStore`, `:3888`), and from the in-memory journal otherwise.
- **Why the dashboard is not persisted.** `shouldInitializeRobotPersistence` looks only for `--schema` or a `--robot-*` argument in `os.Args`, and returns false for `serve`, which sets its own store. Plain `ntm dashboard` matches neither.

### The two mail-event producers

**1. Dashboard producer (per message, in memory only).**
- `fetchAgentMailInboxes` runs every 30 s (`internal/tui/dashboard/dashboard.go:799`).
- It publishes through `robot.GetAttentionFeed()` (`dashboard.go:2919`), using `PublishMailPending` / `PublishMailAckRequired` (`:2940-2942`).
- Because that feed has no store, these per-message `mail.received` / `mail.ack_required` events are visible only inside the dashboard, for example to its popup (`internal/tui/dashboard/popup.go:76`, `:113`).
- No other process can read them.

**2. Projection producer (project-level summaries, persisted).** Runs in robot and serve processes. `RefreshNormalizedProjection` (`internal/robot/robot.go:7028`):
1. Resolves the project as the explicit dir or the cwd's git root (`resolveNormalizedProjectDir`, `robot.go:7067-7080`).
2. Runs `adapters.WorkCoordinationAdapter.Collect` (`internal/robot/adapters/work_coordination.go:291`). That calls `ListProjectAgents`, then `FetchInbox` for every project agent except `HumanOverseer`, with limit 25 and bodies included (`work_coordination.go:345-363`).
3. Summarizes the inboxes into coordination problems (`work_coordination.go:~666-695`):
   - `urgent_mail` when `UrgentUnread > 0`
   - `pending_ack` when `PendingAck > 0`
   - `mail_backlog` when `TotalUnread >= threshold`
4. Turns those into `mail.unread` / `mail.ack_required` attention events (`normalizedCoordinationProblemEvent`, `robot.go:8149`, mapping at `:8174-8189`).
5. Appends them with `AppendDeduplicated` through `publishNormalizedAttentionSignals` (`robot.go:7844-7851`) into the store-backed feed, so they land in `state.db`.

These events summarize the whole project (counts plus agent lists in `details.agents`). They are not one event per message.

When the projection refresh runs:
- Once at the start of every `--robot-*` command, with a 10 s timeout (`root.go:2538-2540`).
- In `ntm serve`: once at startup, then every 15 s (`internal/cli/serve.go:170-191`).

### What `--robot-attention` actually waits on

- `PrintAttention` (`internal/robot/events.go:1079`) loops (`events.go:1122-1166`). On each poll it calls `checkAttentionConditions`, which calls `feed.Replay`, which reads SQLite. It then sleeps `--attention-poll` (default 1 s).
- The loop does not call `RefreshNormalizedProjection` again. While it waits, new mail becomes visible only if another process appends to `state.db`, either:
  - a running `ntm serve` (every 15 s), or
  - another `ntm --robot-*` command, which refreshes once at its own startup.
- With no `ntm serve` and no other robot command running, a waiting `--robot-attention --attention-condition=mail_pending` sees only the mail state captured when it started.

### Side effect on the Stop-hook signal files

Every projection refresh calls `fetch_inbox` for every agent in the project. On the Agent Mail server, `fetch_inbox` deletes that agent's signal file regardless of arguments (`mcp_agent_mail/src/mcp_agent_mail/app.py:8345-8347`). So each of these clears signal files that `check_critical_on_stop.sh` and `check_mail_on_close.sh` read:

| Caller | Agents covered | How often |
|---|---|---|
| Each `ntm --robot-*` invocation | all agents in the cwd's project | once per invocation |
| `ntm serve` | all agents in its cwd's project | every 15 s |
| `ntm dashboard` | agents mapped to panes in the session registry | every 30 s |

The messages themselves stay unread: `fetch_inbox` does not set `read_ts`. Only the signal file is removed.

### Updated closure maps

**Chain B, cross-process form.** Chain B above (dashboard → attention feed → `Replay`) holds only inside one dashboard process; its last edge does not reach a separate process. The cross-process path for mail attention is Chain C.

**Chain C — projection refresh → `state.db` → `--robot-attention`**

| Depth | Node | Location | Label | Adds/changes |
|---|---|---|---|---|
| 0 | `ntm mail send` → server `send_message` (SOURCE + ENTRYPOINT) | `internal/cli/mail.go:770` | production-called | not yet specified |
| 1 | `RefreshNormalizedProjection` → `WorkCoordinationAdapter.Collect` (`FetchInbox` per agent) | `internal/robot/robot.go:7028`; `adapters/work_coordination.go:291` | production-called (`root.go:2540`, `serve.go:171`, `:186`) | not yet specified |
| 2 | `publishNormalizedAttentionSignals` → `AttentionFeed.Append` → `state.db` | `robot.go:7844`; `attention_feed.go:524` | production-called | not yet specified |
| 3 | `--robot-attention` poll → `AttentionFeed.Replay` from store (OBSERVABLE) | `robot/events.go:1079`, `:1122-1166`; `attention_feed.go:944` | production-called (`--robot-attention`, `root.go:3278`) | not yet specified |

Edge evidence:
- **0→1** — asynchronous.
  - Driver: `RefreshNormalizedProjection`, run at robot-command startup or on the `ntm serve` 15 s ticker.
  - Errors are logged (`serve.go:172`, `:187`) or ignored (`root.go:2540`, `_ =`).
  - Agent Mail being unavailable yields `Reason: "agent mail unavailable"` and no mail problems (`work_coordination.go:340-342`).
- **1→2** — synchronous, deduplicated by `normalizedAttentionDedupWindow`.
- **2→3** — cross-process through SQLite. The reader polls every `--attention-poll`.
- **Tests:** none were traced for this end-to-end edge.

```json
{
  "behavior": "When a message is sent through Agent Mail to agents in a project, an orchestrator running ntm --robot-attention --attention-condition=mail_pending in another process wakes on a mail.unread or mail.ack_required event once a projection refresh (ntm serve or another --robot-* command) has written it to state.db.",
  "git_commit": "905820535176783780bcf57035f4e9687c43ce24",
  "repo": "/home/maceo/Dev/ntm",
  "nodes": [
    { "id": "ntm_mail_send_overseer", "module": "internal/cli+internal/agentmail", "is_entrypoint": true, "adds_or_changes": false, "read_path": null, "seedable_store": "agentmail.Client.SendOverseerMessage" },
    { "id": "refresh_normalized_projection", "module": "internal/robot+internal/robot/adapters", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "publish_normalized_attention_to_state_db", "module": "internal/robot", "is_entrypoint": false, "adds_or_changes": false, "read_path": null, "seedable_store": null },
    { "id": "robot_attention_replay_from_store", "module": "internal/robot", "is_entrypoint": false, "adds_or_changes": false, "read_path": "robot.AttentionFeed.Replay", "seedable_store": null }
  ],
  "edges": [
    { "is_async": true, "cross_boundary": true, "driver": "robot.RefreshNormalizedProjection" },
    { "is_async": false, "cross_boundary": false, "driver": null },
    { "is_async": false, "cross_boundary": true, "driver": null }
  ]
}
```

Staged adapter: `2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.projection.closure-adapter.py` (sibling file, not wired in).
- `/drive` → `robot.RefreshNormalizedProjection(ctx, store, projectDir, "")` (`robot.go:7028`)
- `/observe` → `robot.NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(store)).Replay(cursor, limit)` (`attention_feed.go:474`, `:944`), filtered to `mail.*`

### Follow-up code references
- `internal/cli/root.go:202-206` — `PersistentPreRunE` calls `initializeRobotPersistence`
- `internal/cli/root.go:2488-2515` — `shouldInitializeRobotPersistence` (args-based)
- `internal/cli/root.go:2517-2541` — `initializeRobotPersistence` (store-backed feed + one projection refresh)
- `internal/cli/serve.go:99-103`, `:170-191` — serve's store-backed feed and 15 s refresh ticker
- `internal/state/store.go:42-53` — `DefaultPath` for `state.db`
- `internal/robot/attention_feed.go:474` — `WithAttentionStore`
- `internal/robot/attention_feed.go:524-545` — `Append` (store first)
- `internal/robot/attention_feed.go:944-950` — `Replay` (store when set)
- `internal/robot/attention_feed.go:2956-2962` — lazy in-memory default
- `internal/robot/robot.go:7028`, `:7067-7080`, `:7844-7851`, `:8149`, `:8174-8189` — projection refresh, project resolution, publish, mail problem → event mapping
- `internal/robot/adapters/work_coordination.go:291`, `:345-363` — `Collect`, per-agent `FetchInbox` (limit 25, bodies)
- `internal/robot/events.go:1079`, `:1122-1166` — `PrintAttention` polling loop (no refresh inside)
- `internal/tui/dashboard/dashboard.go:2919`, `popup.go:76` — dashboard uses the lazily created in-memory feed

All lines above were confirmed with targeted reads at commit `905820535176783780bcf57035f4e9687c43ce24`.
