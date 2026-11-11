# Attention Feed Contract

> **Authoritative specification for the ntm attention feed system.**
> This document defines the JSON semantics, event envelope, cursor mechanics, and operator loop for AI agents consuming ntm's attention feed. Every field, flag, and number here is taken from the code. The test `TestAttentionContractDocMatchesCode` (internal/robot/attention_contract_doc_test.go) checks the parts that can be checked mechanically.

**Status:** IMPLEMENTED
**Contract version:** 1.2.0 (`AttentionContractVersion` in internal/robot/attention_contract.go)
**Bead:** br-aa0nj (initial), bd-j9jo3.9.4 (refresh)
**Created:** 2026-03-21
**Updated:** 2026-11-11

---

## 1. Foundational Principle

**The LLM is the driver; ntm is the nervous system.**

ntm provides:
- **Sensing:** observe agent states, session health, alerts, beads, mail
- **Actuation:** spawn, send, interrupt, wait, route

ntm does NOT provide:
- Planning, scheduling, or mission runtime
- A second source of truth for task/coordination state
- Opaque heuristics that cannot be explained in code/tests

The attention feed is a **read-only event stream** that tells the operator "what changed" so they can decide what to do next. All actions flow through existing robot commands.

`--robot-capabilities` states this boundary as machine-readable guardrails (`attention.guardrails`): `nervous_system_not_planner`, `one_obvious_tending_loop`, `cursor_resync_is_explicit`, and `unsupported_conditions_stay_explicit`.

---

## 2. The Operator Loop

The canonical operator loop (the same one `--robot-help` prints) is:

```text
1. BOOTSTRAP:  ntm --robot-snapshot
               -> full state, latest_cursor, replay_window
2. ATTEND:     ntm --robot-attention --attention-cursor=<cursor>
               -> sleeps until something needs attention, then returns a digest,
                  a wake_reason, and cursor_info.next_command
3. ACT:        ntm --robot-send=..., ntm --robot-interrupt=..., or a next_actions entry
4. LOOP:       run cursor_info.next_command (it carries the new cursor)

If CURSOR_EXPIRED: run ntm --robot-snapshot again and start from its latest_cursor.
```

Use `--robot-events` for raw replay and debugging, and `--robot-digest` for a summary without waiting. `--robot-wait` blocks on a named pane-state or attention condition in one session.

### 2.1 Cold Start

On first invocation or after context loss, an operator MUST call `--robot-snapshot` to establish baseline state. The snapshot returns:
- Full system state (`sessions`, `summary`, `work`, `beads_summary`, `alerts`, `agent_mail`, `sources`, and more)
- `latest_cursor`: the newest event cursor, to pass as `--attention-cursor` or `--since-cursor`
- `replay_window`: the cursor range that can still be replayed, and the retention period
- `attention_summary`: event counts, the top action-required items, and `next_steps`

### 2.2 Cursor Expiration and Resync

A cursor expires when the events right after it have been deleted (see §6.3). `--robot-events` then replies (real output):

```json
{
  "success": false,
  "timestamp": "2026-11-11T16:20:40Z",
  "version": "1.0.0",
  "output_format": "json",
  "error": "cursor 1 has expired (earliest available: 9, retention: 168h0m0s)",
  "error_code": "CURSOR_EXPIRED",
  "hint": "ntm --robot-snapshot",
  "events": [],
  "next_cursor": 0,
  "has_more": false,
  "replay_window": {
    "supported": true,
    "oldest_cursor": 9,
    "latest_cursor": 0,
    "event_count": 0,
    "retention_period": "168h0m0s",
    "resync_command": "ntm --robot-snapshot"
  }
}
```

The operator MUST run `replay_window.resync_command` (always `ntm --robot-snapshot`) and continue from the new snapshot's `latest_cursor`. Do not guess what was missed.

---

## 3. Event Envelope

Every event, on every surface, is one `AttentionEvent` (internal/robot/attention_contract.go). Example:

```json
{
  "cursor": 1234,
  "ts": "2026-11-11T16:18:15.775271473Z",
  "session": "myproject",
  "pane": 2,
  "category": "agent",
  "type": "agent.state_change",
  "source": "state_tracker",
  "actionability": "interesting",
  "severity": "info",
  "summary": "Agent cc_2 transitioned from generating to idle",
  "details": {
    "agent_id": "cc_2",
    "from_state": "generating",
    "to_state": "idle",
    "signal": "agent_state_changed",
    "signal_reason": "agent lifecycle or state changed"
  },
  "next_actions": [
    {
      "action": "robot-tail",
      "args": "--robot-tail=myproject --panes=2 --lines=50",
      "reason": "Check what the agent produced"
    }
  ]
}
```

### 3.1 Field Definitions

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `cursor` | integer (int64) | Yes | Position in the event sequence, e.g. `1234`. Strictly increasing. See §6. |
| `ts` | string | Yes | RFC3339 UTC timestamp (usually with nanoseconds) when the event occurred |
| `session` | string | No | Session name if the event is session-scoped. Absent for system-wide events. |
| `recipient_project` | string | No | Agent Mail project key a mail event is addressed to. Absent on other events. See §16. |
| `recipient_agent` | string | No | Agent Mail agent name a mail event is addressed to. Absent on other events. See §16. |
| `message_id` | integer (int64) | No | Agent Mail message id of an addressed mail event. Absent on other events. See §16. |
| `pane` | integer | No | Pane index if the event is pane-scoped. Omitted when 0, so pane 0 and "no pane" look the same. |
| `category` | string | Yes | Event category. See §3.2 |
| `type` | string | Yes | Event type, written `category.name` (e.g. `agent.state_change`). See §3.2 |
| `source` | string | No | Component that generated the event (e.g. `agent_mail`, `state_tracker`, `conflict_detector`, `adapter.work_coordination`) |
| `actionability` | string | Yes | `background`, `interesting`, or `action_required`. See §4 |
| `severity` | string | Yes | `debug`, `info`, `warning`, `error`, `critical` |
| `reason_code` | string | No | Machine-readable reason (e.g. `health:ok`), so consumers can classify without parsing `summary` |
| `summary` | string | Yes | Human-readable one-line summary |
| `details` | object | No | Event-specific structured data. Keys vary by type. |
| `next_actions` | array of NextAction | No | Mechanical follow-up commands. Omitted when there are none. See §5. |

`details` is free-form, but replayed events usually carry operator-state annotations such as `attention_state`, `attention_item_key`, `attention_fingerprint`, and `attention_explanation_code`. Many events also carry `signal` and `signal_reason`, which the wait conditions in §7.4 match on.

### 3.2 Event Categories and Types

Categories (the `EventCategory` constants): `session`, `pane`, `agent`, `actuation`, `file`, `mail`, `bead`, `system`, `alert`, `incident`, `health`.

Types the code defines (`EventType` constants):

| Category | Types |
|----------|-------|
| `session` | `session.created`, `session.destroyed`, `session.attached`, `session.detached` |
| `pane` | `pane.created`, `pane.destroyed`, `pane.output`, `pane.resized`, `pane.context_hot` |
| `agent` | `agent.started`, `agent.stopped`, `agent.state_change`, `agent.error`, `agent.handoff`, `agent.stalled`, `agent.recovered`, `agent.compacted`, `agent.idle`, `agent.prompt_wait` |
| `actuation` | `actuation.requested`, `actuation.outcome`, `actuation.verified` |
| `file` | `file.changed`, `file.conflict`, `file.reserved`, `file.released` |
| `mail` | `mail.received`, `mail.ack_required`, `mail.acknowledged`, `mail.unread` |
| `bead` | `bead.created`, `bead.updated`, `bead.closed`, `bead.unblocked` |
| `system` | `system.startup`, `system.shutdown`, `system.health_change`, `system.cursor_reset`, `system.spawn`, `system.heartbeat` (live only, see §10.1) |
| `alert` | `alert.attention_required`, `alert.warning`, `alert.info`, `alert.generic` |
| `incident` | `incident.opened`, `incident.promoted`, `incident.recurred`, `incident.resolved`, `incident.muted` |
| `health` | `health.change` |

There is also `reservation.conflict`. Treat unknown categories and types as forward-compatible additions, not errors.

---

## 4. Actionability Classes

Events are classified by how urgently they demand operator attention:

| Class | Meaning | Examples |
|-------|---------|----------|
| `background` | Informational, no action typically needed | Source health OK, agent started generating, heartbeat |
| `interesting` | Worth noticing but not urgent | Agent became idle, new mail (`mail.received`) |
| `action_required` | Cannot be resolved without the operator | Agent error, file conflict, mail that needs an ack (`mail.ack_required`) |

**Important:** Actionability is NOT the same as severity. A high-severity event may resolve itself, and a low-severity event may block progress. Severity order, lowest first: `debug`, `info`, `warning`, `error`, `critical`.

---

## 5. Next Actions

The `next_actions` array lists mechanical follow-up commands. They are not suggestions or heuristics. Each one is a robot command that makes sense as an immediate response.

### 5.1 Rules

1. **Real surfaces only:** the feed drops any action whose `action` name is not on its allow-list: `robot-attention`, `robot-bead-show`, `robot-context`, `robot-diff`, `robot-digest`, `robot-events`, `robot-graph`, `robot-inspect-coordination`, `robot-mail-check`, `robot-plan`, `robot-send`, `robot-snapshot`, `robot-status`, `robot-tail`, `robot-watch-bead`.
2. **Copy-paste ready:** `args` is the full flag string. Run it as `ntm <args>`.
3. **Mechanically derivable:** if an action would need judgment, it is left out.

### 5.2 Structure

```json
{
  "next_actions": [
    {
      "action": "robot-mail-check",
      "args": "--robot-mail-check --mail-project=/home/you/projects/myproject --mail-status=unread --mail-agent=BlueLake",
      "reason": "Inspect the unread message"
    },
    {
      "action": "robot-diff",
      "args": "--robot-diff=myproject",
      "reason": "Compare agent changes"
    }
  ]
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `action` | string | Yes | Robot command name, without `--` (e.g. `robot-tail`) |
| `args` | string | Yes | Complete flags for that command |
| `reason` | string | No | Why this action is relevant |

The same `NextAction` shape is used by `attention_summary.next_steps` in the snapshot.

---

## 6. Cursor Semantics

### 6.1 Format

A cursor is a 64-bit integer, for example `1234`. Cursors are assigned by the SQLite store (an AUTOINCREMENT column in `state.db`), so every ntm process that shares the store shares one cursor sequence. A feed with no store counts in memory.

Two values are reserved when you pass a cursor in:
- `0` means "from the beginning" (everything still retained).
- `-1` means "from now" (skip history). Snapshot `latest_cursor` is the usual way to start from now.

### 6.2 Properties

1. **Strictly increasing:** a later event always has a larger cursor.
2. **Gaps allowed:** cursors can skip values (filtered or deleted events).
3. **Replay-safe:** asking for events after cursor X returns events with cursor > X, in cursor order.
4. **Resumable:** pass `next_cursor` (events), `cursor_info.end_cursor` (attention), or `cursor_info.next_cursor` (wait) to the next call.

### 6.3 Retention and Expiration

- Events are kept for **7 days** after they are written (`RetentionPeriod` in `DefaultAttentionFeedConfig()`, reported as `"retention_period": "168h0m0s"`).
- Expired rows are deleted when a replay reads the store. A feed with no store also caps its in-memory journal at 10,000 events.
- A cursor is expired when it is older than the event just before the oldest one kept (`since_cursor < oldest_cursor - 1`). `0` and `-1` never expire.
- On expiry, commands return `CURSOR_EXPIRED` (see §2.2 and §12.2).

### 6.4 Cursor in Snapshot

`--robot-snapshot` returns `latest_cursor` and `replay_window` (real output, trimmed):

```json
{
  "success": true,
  "timestamp": "2026-11-11T16:18:56Z",
  "version": "1.0.0",
  "output_format": "json",
  "schema_id": "ntm:robot:snapshot:v1",
  "schema_version": "ntm.robot.snapshot.v1",
  "ts": "2026-11-11T16:18:56Z",
  "attention_contract_version": "1.2.0",
  "latest_cursor": 13,
  "replay_window": {
    "supported": true,
    "reason": "ready",
    "oldest_cursor": 9,
    "latest_cursor": 13,
    "event_count": 5,
    "oldest_timestamp": "2026-11-11T15:05:31.989416597Z",
    "latest_timestamp": "2026-11-11T16:18:54.464172245Z",
    "retention_period": "168h0m0s",
    "resync_command": "ntm --robot-snapshot"
  },
  "sessions": [
    ...
  ]
}
```

---

## 7. Command Cluster

All flags below are listed in `ntm --robot-capabilities`.

### 7.1 `--robot-snapshot`

Full state dump with the cursor for later polling.

```bash
ntm --robot-snapshot
ntm --robot-snapshot --since=2026-11-11T00:00:00Z
```

With `--since=<RFC3339>` the snapshot is a delta of state-tracker changes (`ts`, `since`, `changes`). A delta has no cursor; use `--robot-events` for cursor-based change history.

**Response (trimmed):**
```json
{
  "success": true,
  "timestamp": "2026-11-11T16:18:56Z",
  "version": "1.0.0",
  "schema_id": "ntm:robot:snapshot:v1",
  "ts": "2026-11-11T16:18:56Z",
  "attention_contract_version": "1.2.0",
  "latest_cursor": 13,
  "replay_window": {
    "supported": true,
    "oldest_cursor": 9,
    "latest_cursor": 13,
    "retention_period": "168h0m0s",
    "resync_command": "ntm --robot-snapshot"
  },
  "sessions": [
    ...
  ],
  "summary": {
    "total_sessions": 2,
    "total_agents": 4
  },
  "alerts": [],
  "attention_summary": {
    "total_events": 5,
    "action_required_count": 0,
    "interesting_count": 0,
    "by_category": {
      "health": 5
    },
    "unsupported_signals": [
      "bead_orphaned"
    ],
    "next_steps": [
      {
        "action": "robot-events",
        "args": "--robot-events --since-cursor=13 --events-limit=20",
        "reason": "Continue following new attention events from the snapshot cursor"
      },
      {
        "action": "robot-snapshot",
        "args": "--robot-snapshot",
        "reason": "Resync if a saved cursor falls outside the current replay window"
      }
    ]
  }
}
```

### 7.2 `--robot-events`

Raw event replay after a cursor.

```bash
ntm --robot-events --since-cursor=1234
ntm --robot-events --since-cursor=1234 --events-limit=50
ntm --robot-events --since-cursor=1234 --events-category=agent --events-actionability=action_required
ntm --robot-events --since-cursor=0 --events-recipient=BlueLake
ntm --robot-events --events-incident=inc-20260322-abc --events-window-before=5m --events-window-after=1m
ntm --robot-events --events-as-of=2026-11-11T03:50:00Z --events-limit=25
```

**Response:**
```json
{
  "success": true,
  "timestamp": "2026-11-11T16:18:31Z",
  "version": "1.2.0",
  "output_format": "json",
  "events": [
    {
      "cursor": 1234,
      "ts": "2026-11-11T16:18:15.775271473Z",
      "category": "health",
      "type": "health.change",
      "source": "adapter.work_coordination",
      "actionability": "background",
      "severity": "info",
      "reason_code": "health:ok",
      "summary": "source work_coordination healthy",
      "details": {
        "source_name": "work_coordination",
        "available": true,
        "fresh": true,
        "attention_state": "new"
      },
      "next_actions": [
        {
          "action": "robot-status",
          "args": "--robot-status",
          "reason": "Inspect normalized source health"
        }
      ]
    }
  ],
  "next_cursor": 1234,
  "has_more": false,
  "replay_window": {
    "supported": true,
    "oldest_cursor": 1100,
    "latest_cursor": 1234,
    "event_count": 0,
    "retention_period": "168h0m0s",
    "resync_command": "ntm --robot-snapshot"
  }
}
```

- `next_cursor` is where the next call continues: pass it as `--since-cursor`. With no events it is the newest cursor.
- `has_more` is true when more matching events exist past this page, or when a filtered scan stopped after 50,000 events. Call again from `next_cursor`.
- Historical modes (`--events-incident`, `--events-as-of`) add `replay_target`, `boundedness`, `reconstruction`, and `incident`. The two modes cannot be combined.

**Parameters:**
| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--since-cursor` | int | 0 | Replay events after this cursor (0 = all retained, -1 = from now) |
| `--events-limit` | int | 100 | Max events to return (capped at 1000) |
| `--events-category` | string | all | One category, exact match |
| `--events-actionability` | string | all | One actionability level, exact match |
| `--events-session` | string | all | Only events for this session |
| `--events-recipient` | string | none | Only events addressed to this agent, in any project, any letter case (§16) |
| `--events-incident` | string | none | Bounded replay around a durable incident id |
| `--events-as-of` | RFC3339 | none | Bounded reconstruction at or before a time (at most 24h ago) |
| `--events-window-before` | duration | 5m | Context before the incident start (incident mode) |
| `--events-window-after` | duration | 1m | Context after the incident end (incident mode). Before + after must be at most 1h. |
| `--profile` | string | none | Profile thresholds applied before the explicit filters (§9) |

There is no severity filter flag. Filters are applied while reading, so `--events-limit` counts matching events, not events scanned.

### 7.3 `--robot-digest`

Token-efficient summary of recent events, without waiting.

```bash
ntm --robot-digest
ntm --robot-digest --profile=minimal
ntm --robot-digest --since-cursor=1234
```

The digest starts after `--attention-cursor`, or `--since-cursor` when that is not set, and covers everything retained when neither is set. `--attention-session` limits it to one session.

**Response (real shape, trimmed):**
```json
{
  "success": true,
  "timestamp": "2026-11-11T16:18:54Z",
  "version": "1.2.0",
  "output_format": "json",
  "cursor_start": 1230,
  "cursor_end": 1234,
  "period_start": "2026-11-11T15:05:31.989416597Z",
  "period_end": "2026-11-11T16:18:52.1180643Z",
  "event_count": 4,
  "by_category": {
    "health": 4
  },
  "by_actionability": {
    "background": 4
  },
  "buckets": {
    "background": [
      {
        "event": {
          "cursor": 1234,
          "ts": "2026-11-11T16:18:52.1180643Z",
          "category": "health",
          "type": "health.change",
          "actionability": "background",
          "severity": "info",
          "summary": "source work_coordination healthy"
        },
        "cursor_start": 1234,
        "cursor_end": 1234,
        "source_event_count": 1
      }
    ]
  },
  "suppressed": {
    "total": 1,
    "by_reason": {
      ...
    }
  },
  "summary": "source work_coordination healthy; 0 action_required, 0 interesting, 3 background; 1 suppressed from 4 events",
  "prioritized_queue": [
    ...
  ],
  "active_incidents": [],
  "replay_window": {
    "supported": true,
    "oldest_cursor": 1100,
    "latest_cursor": 1234,
    "event_count": 0,
    "retention_period": "168h0m0s",
    "resync_command": "ntm --robot-snapshot"
  }
}
```

`buckets` has `action_required`, `interesting`, and `background` lists (each omitted when empty). The default bucket limits are 5, 4, and 3 items. Repeated events are coalesced into one item and counted in `suppressed`. `suppressed.by_reason` maps a reason to a count; in the run above it was `{"bucket_limit": 1}`.

### 7.4 `--robot-wait`

Block until a named condition is met in one session.

```bash
ntm --robot-wait=myproject --wait-until=idle --timeout=5m
ntm --robot-wait=myproject --wait-until=idle --wait-transition --timeout=2m --panes=1,2
ntm --robot-wait=myproject --wait-until=action_required --attention-cursor=1234 --profile=operator
```

**Named conditions (exhaustive list):**

| Condition | Kind | Met when |
|-----------|------|----------|
| `idle` | pane | Agents are waiting for input |
| `complete` | pane | Idle and no output for 5 seconds |
| `generating` | pane | Agents are generating |
| `healthy` | pane | No agent is in error or stalled |
| `stalled` | pane | Agents are stalled |
| `rate_limited` | pane | Rate-limit text or telemetry seen in the pane |
| `attention` | attention | Any `interesting` or `action_required` event after the cursor |
| `action_required` | attention | Any `action_required` event after the cursor |
| `mail_pending` | attention | A `mail.received` event |
| `mail_ack_required` | attention | A `mail.ack_required` event |
| `context_hot` | attention | An event with the `context_hot` signal |
| `reservation_conflict` | attention | A file reservation conflict event |
| `file_conflict` | attention | A file conflict event |
| `session_changed` | attention | A session lifecycle event |
| `pane_changed` | attention | A pane lifecycle or geometry event |

Pane conditions apply to all matching agents (or any one with `--any`). Several conditions can be given comma-separated; they are ANDed. Attention conditions scan events after `--attention-cursor`.

`bead_orphaned` is **deliberately unsupported**: `--wait-until=bead_orphaned` fails with `INVALID_FLAG`, and `--robot-capabilities` explains why under `attention.unsupported_conditions`.

**No free-form condition DSL.** Conditions are enumerated and documented. This prevents:
- Ambiguity in condition parsing
- Version skew between client expectations and server capabilities
- Untestable condition combinations

**Response (attention condition met):**
```json
{
  "success": true,
  "timestamp": "2026-11-11T16:35:00Z",
  "version": "1.0.0",
  "session": "myproject",
  "condition": "action_required",
  "waited_seconds": 45.2,
  "wake_payload": {
    "matched_condition": "action_required",
    "trigger_event": {
      "cursor": 1240,
      "ts": "2026-11-11T16:34:59Z",
      "session": "myproject",
      "category": "file",
      "type": "file.conflict",
      "actionability": "action_required",
      "severity": "error",
      "summary": "File conflict: internal/robot/types.go modified by [cc_1 cc_2]"
    },
    "trigger_count": 1,
    "details": {
      "matched_conditions": ["action_required"]
    }
  },
  "cursor_info": {
    "observed_cursor": 1240,
    "next_cursor": 1241,
    "oldest_cursor": 1100
  }
}
```

Pane conditions instead return `agents` (each with `pane`, `state`, `met_at`, `agent_type`). A timeout returns `TIMEOUT` with `agents_pending`.

### 7.5 `--robot-attention`

The one obvious tending command: wait until something needs attention, then return a digest of what changed.

```bash
ntm --robot-attention --attention-cursor=1234
ntm --robot-attention --attention-cursor=1234 --attention-timeout=2m
ntm --robot-attention --attention-session=myproject --profile=debug --attention-condition=action_required
```

| Flag | Default | Description |
|------|---------|-------------|
| `--attention-cursor` | 0 | Wait for events after this cursor. With 0, any retained interesting event wakes it at once. |
| `--attention-condition` | `attention` | `attention`, `action_required`, or `mail_pending` (the other attention conditions in §7.4 also work) |
| `--attention-session` | all | Only events for this session |
| `--attention-timeout` | 5m | Return after this long even if nothing happened |
| `--attention-poll` | 1s | How often to check |
| `--profile` | none | Profile applied to the condition check and the digest |

**Response (real output, timeout case):**
```json
{
  "success": true,
  "timestamp": "2026-11-11T16:19:10Z",
  "version": "1.2.0",
  "output_format": "json",
  "wake_reason": "timeout",
  "waited_seconds": 2.001065656,
  "digest": {
    "cursor_start": 1234,
    "cursor_end": 1234,
    "event_count": 0,
    "by_category": {},
    "by_actionability": {},
    "buckets": {},
    "suppressed": {
      "total": 0
    },
    "summary": "no matching changes"
  },
  "cursor_info": {
    "start_cursor": 1234,
    "end_cursor": 1234,
    "oldest_cursor": 1100,
    "next_command": "ntm --robot-attention --attention-cursor=1234"
  },
  "replay_window": {
    "supported": true,
    "oldest_cursor": 1100,
    "latest_cursor": 1234,
    "event_count": 6,
    "retention_period": "168h0m0s",
    "resync_command": "ntm --robot-snapshot"
  }
}
```

When something needs attention, `wake_reason` is `attention`, `matched_condition` names the condition, and `trigger_event` holds the event that woke it. On `CURSOR_EXPIRED`, `wake_reason` is `cursor_expired` and `cursor_info.next_command` is the resync command.

### 7.6 `--robot-overlay`

Hands a session (and optionally a cursor) back to a human in a tmux popup. Its reply (`session`, `cursor`, `no_wait`, `launched`, `dismissed`, `pid`) is not part of the event stream. It does not use the durable store.

```bash
ntm --robot-overlay --overlay-session=myproject --overlay-cursor=1234 --overlay-no-wait
```

---

## 8. Transports

The same `AttentionEvent` objects reach every surface. The reply envelopes around them differ by transport, as described below.

- **CLI:** one JSON object on stdout per command (§7).
- **HTTP** (`ntm serve`): under `/api/v1`.
  - `GET /attention/events`: replay. Query: `since_cursor`, `limit`, `category`, `actionability` (comma-separated), `session`, `incident_id`, `as_of`, `window_before_ms`, `window_after_ms`.
  - `GET /attention/digest`: digest. Query: `since_cursor`, `category`, `session`, `action_required_limit`, `interesting_limit`, `background_limit`, `trace`.
  - `GET /attention/stream`: SSE (§8.1).
  - `POST /attention/items/{cursor}/state`: operator state (acknowledge, snooze, pin, mute) for one item.
  - `GET /robot/snapshot`, `GET /robot/digest`, `GET /robot/attention`: the robot outputs over HTTP. `/robot/attention` is a non-blocking digest.
  - `GET /ws`: WebSocket (§8.2).
- The HTTP replay reply uses its own envelope: `success`, `timestamp`, `version`, `output_format`, `events`, `since_cursor`, `next_cursor`, `newest_cursor`, `oldest_cursor`, `event_count`, and `truncated` (the CLI's `has_more`). An expired cursor gets HTTP 409 with `error_code`, `message`, `oldest_cursor`, `newest_cursor`, and `resync_command`.

### 8.1 SSE Format

`GET /api/v1/attention/stream?since_cursor=1234` sends a `connected` event, replays events after the cursor, then streams live ones. Query filters: `category`, `session`, `actionability`, and `heartbeat` (seconds; 0 turns heartbeats off).

```text
event: connected
data: {"status":"connected","time":"2026-11-11T16:30:00Z","since_cursor":1234,"oldest_cursor":1100,"newest_cursor":1236}

event: attention
data: {"cursor":1235,"ts":"2026-11-11T16:30:01Z","session":"myproject","pane":2,"category":"agent","type":"agent.state_change","actionability":"interesting","severity":"info","summary":"Agent cc_2 transitioned from generating to idle"}

event: heartbeat
data: {"type":"heartbeat","stream_id":"watch_20261111T163000Z","time":"2026-11-11T16:30:05Z","oldest_cursor":1100,"newest_cursor":1236,"cursor_position":1235,"event_count":137,"subscriber_count":1,"next_heartbeat_ms":5000,"subscription_active":true}
```

If `since_cursor` has expired, the stream sends one `event: error` whose data holds `error_code` `CURSOR_EXPIRED`, `oldest_cursor`, `newest_cursor`, and `resync_command`, then closes.

### 8.2 WebSocket Format

Subscribe to the `attention` topic, or `attention:<session>`, with a `subscribe` message. Options go in `data`: `topics`, `since_cursor` (0 = beginning, -1 = now), `session`, `profile`, `category`, `actionability`, `exclude_muted`, `exclude_snoozed`.

```json
{"type": "subscribe", "ts": "2026-11-11T16:30:00Z", "request_id": "r1", "data": {"topics": ["attention"], "since_cursor": 1234}}
```

The server answers with an `ack`. Its `data.attention` has `subscribed`, `since_cursor`, `oldest_cursor`, `newest_cursor`, and `replay_count`, or `error_code` `CURSOR_EXPIRED` with a `resync_command`. Each event then arrives as a `WSEvent`. `seq` is the event's cursor, and `data` is the `AttentionEvent`:

```json
{
  "type": "event",
  "ts": "2026-11-11T16:30:01.120Z",
  "seq": 1235,
  "topic": "attention",
  "event_type": "agent.state_change",
  "data": {
    "cursor": 1235,
    "session": "myproject",
    "type": "agent.state_change",
    "summary": "Agent cc_2 transitioned from generating to idle"
  }
}
```

Request errors arrive as `WSError` frames:

```json
{"type": "error", "ts": "2026-11-11T16:30:00Z", "request_id": "r1", "code": "empty_topics", "message": "at least one topic required"}
```

---

## 9. Profiles and Defaults

### 9.1 Profile Semantics

A **profile** is a named set of minimum thresholds and category limits. The built-in profiles, as `--robot-capabilities` lists them:

```json
{
  "default_profile": "operator",
  "profiles": [
    {
      "name": "operator",
      "description": "Default profile for operator agents. Shows actionable events and important state changes.",
      "filters": {"min_severity": "info", "min_actionability": "interesting"}
    },
    {
      "name": "debug",
      "description": "Full verbosity for debugging. Shows all events including debug level.",
      "filters": {"min_severity": "debug", "min_actionability": "background"}
    },
    {
      "name": "minimal",
      "description": "Only critical items requiring immediate action.",
      "filters": {"min_severity": "error", "min_actionability": "action_required"}
    },
    {
      "name": "alerts",
      "description": "Only synthesized alert events.",
      "filters": {"categories": ["alert"]}
    }
  ]
}
```

### 9.2 Profile Selection

```bash
ntm --robot-events --since-cursor=1234 --profile=minimal
ntm --robot-attention --attention-cursor=1234 --profile=operator
```

`--profile` applies to `--robot-events`, `--robot-digest`, `--robot-attention`, and `--robot-wait`. Profile names match in any letter case.

### 9.3 Explicit Filters Narrow the Profile

With `--robot-events`, the profile's thresholds apply first, then `--events-category`, `--events-actionability`, `--events-session`, and `--events-recipient` keep only exact matches. An explicit filter never widens what the profile allows.

```bash
# operator thresholds, then only agent events
ntm --robot-events --since-cursor=1234 --profile=operator --events-category=agent
```

Replies do not echo the filters they applied.

### 9.4 No Profile Specified

`--robot-capabilities` reports `default_profile: "operator"`, and `--robot-help` calls operator the default. In the commands themselves, though, the `--profile` flag defaults to empty, and **no profile filter is applied when `--profile` is not given**. Every retained event is eligible, including `background` and `debug` ones. Pass `--profile=operator` to get operator filtering.

---

## 10. Transport Liveness and Schema Version

### 10.1 Heartbeat

- **Feed heartbeat:** a feed with subscribers publishes a `system.heartbeat` event (`source` `attention_feed`, `background`, `debug`) after **30 seconds** with no other events. It goes only to that process's live subscribers. It is never stored and never appears in replay, and it reuses the current cursor instead of taking a new one.
- **SSE heartbeat:** the SSE stream drops feed heartbeats and sends its own `event: heartbeat` (§8.1). The interval adapts: 1 second right after a replay, 5 seconds when idle, and 30 seconds during sustained traffic. The `heartbeat` query parameter fixes it. Treat a gap of more than twice `next_heartbeat_ms` as a dead connection and reconnect from your last cursor.
- **WebSocket:** feed heartbeats are not forwarded. The server sends WebSocket ping frames, and clients may send `{"type":"ping"}` to get a `pong`.

### 10.2 Schema Version

Robot replies carry `version` in the envelope:
- `--robot-events`, `--robot-digest`, and `--robot-attention` success replies report the attention contract version, `"version": "1.2.0"`.
- `--robot-snapshot` reports the envelope version `"1.0.0"`, plus `attention_contract_version: "1.2.0"`, `schema_id`, and `schema_version`.
- Error replies built by the shared error helper report `"version": "1.0.0"`.

The contract version follows semver:
- **Major:** Breaking changes to field semantics
- **Minor:** New fields added
- **Patch:** Documentation/bug fixes only

### 10.3 Capability Discovery

`--robot-capabilities` lists every attention command with its parameters (`commands[]`), plus an `attention` object (real output, trimmed):

```json
{
  "commands": [
    {
      "name": "events",
      "flag": "--robot-events",
      "category": "attention",
      "parameters": [
        {"name": "since-cursor", "flag": "--since-cursor", "type": "int", "required": false, "default": "0", "description": "Replay events after this cursor (0 = all)"},
        {"name": "events-recipient", "flag": "--events-recipient", "type": "string", "required": false, "description": "Only events addressed to this agent, in any project (any letter case)"}
      ]
    }
  ],
  "attention": {
    "contract_version": "1.2.0",
    "default_profile": "operator",
    "features": {
      ...
    },
    "signal_availability": {
      ...
    },
    "unsupported_conditions": [
      {
        "name": "bead_orphaned",
        "status": "unavailable",
        "reason": "ntm cannot prove work is orphaned from observable state alone. ...",
        "observables_available": ["bead status (open/in_progress/closed) via br/bv", "agent pane liveness (alive/stalled/dead)", "time since last pane output", "Agent Mail message history"],
        "what_would_change": "Agent Mail gains explicit 'bead abandon' or 'bead handoff' messages, giving ntm positive evidence of intent rather than inferring abandonment from silence."
      }
    ],
    "guardrails": [
      {"name": "cursor_resync_is_explicit", "rule": "When replay is stale or CURSOR_EXPIRED occurs, resync with --robot-snapshot instead of guessing missed history."}
    ]
  }
}
```

`features` and `signal_availability` are maps from a name to `{"status": ..., "note": ...}`. Today `features` has `cursor_replay`, `profile_presets`, and `operator_boundary`, all `available`. `signal_availability` has `bead_orphaned`, with status `unavailable`.

---

## 11. Degraded State Handling

### 11.1 Source Health

The snapshot reports the health of its data sources under `sources` and lists unhealthy ones in `degraded_sources`. `sources.sources` maps each source name (for example `work_coordination`) to its `name`, `available`, `fresh`, `reason_code`, `age_ms`, and `updated_at`. Source health changes also appear in the feed as `health.change` events.

```json
{
  "sources": {
    "sources": {
      ...
    },
    "all_fresh": true
  }
}
```

### 11.2 Store Outage

If `state.db` cannot be opened or written, the feed keeps working in memory. New events are held in that process only and are not seen by other processes. The feed tries the store again every 30 seconds, and the log records `attention feed store unavailable` and later `attention feed store recovered`. The dashboard shows when the feed is not durable. No robot reply carries a store-health field.

### 11.3 Capabilities

`AttentionCapabilities` has a `degraded` list (`feature`, `status`, `reason`, `impact`, `hint`), but ntm does not fill it today. Read degradation from snapshot `sources` / `degraded_sources` instead. Unsupported signals are always reported (`signal_availability`, `unsupported_conditions`, and snapshot `attention_summary.unsupported_signals`).

---

## 12. Help and Documentation

### 12.1 `--robot-help` Operator Loop

`--robot-help` includes the operator loop, the attention commands, the profiles, the guardrails, and the unsupported conditions. An excerpt:

```text
Attention Feed (Operator Loop):
-------------------------------
The recommended tending loop for operator agents:

  1. Bootstrap:  ntm --robot-snapshot
     → Get system state + latest_cursor + replay_window
  2. Attend:     ntm --robot-attention --attention-cursor=<cursor>
     → Sleep until attention needed, wake with digest + reason
  3. Act:        ntm --robot-send=proj --msg="fix X"
     → Execute the suggested action
  4. Loop:       Use cursor from step 2 response as --attention-cursor
     → Repeat from step 2

If cursor expires: re-run --robot-snapshot to resync.

Attention Feed Commands:
  --robot-events         Raw event replay (--since-cursor=N, --events-limit=50)
                         Filters: --events-category, --events-session,
                         --events-recipient=AGENT, --events-actionability
  --robot-digest         Aggregated summary of recent changes
  --robot-attention      Wait-then-digest (the one obvious tending command)
  --robot-overlay        Human handoff actuator (optionally non-blocking with --overlay-no-wait)
```

### 12.2 Error Examples

**CURSOR_EXPIRED** (`--robot-events`, `--robot-digest`, `--robot-attention`, `--robot-wait`): see §2.2. `--robot-attention` also sets `"wake_reason": "cursor_expired"`.

**INVALID_FLAG** (unsupported wait condition):
```json
{
  "success": false,
  "timestamp": "2026-11-11T16:40:00Z",
  "version": "1.0.0",
  "error": "unsupported condition 'bead_orphaned'",
  "error_code": "INVALID_FLAG",
  "hint": "Condition 'bead_orphaned' is deliberately unsupported. Use --robot-capabilities to see rationale.",
  "session": "myproject",
  "condition": "bead_orphaned",
  "waited_seconds": 0
}
```

An unknown condition gets `"error": "invalid condition 'foo'"`, also `INVALID_FLAG`, and a hint listing the valid conditions. `INVALID_FLAG` is also returned for combining `--events-incident` with `--events-as-of`, an `--events-as-of` time in the future or more than 24h ago, and an incident window over 1h.

**TIMEOUT** (`--robot-wait`):
```json
{
  "success": false,
  "timestamp": "2026-11-11T16:45:00Z",
  "version": "1.0.0",
  "error": "timeout after 5m0s",
  "error_code": "TIMEOUT",
  "hint": "Try increasing --timeout (or deprecated --wait-timeout) or check agent status with --robot-activity",
  "session": "myproject",
  "condition": "idle",
  "waited_seconds": 300.0,
  "agents_pending": ["%12"]
}
```

Other codes: `SESSION_NOT_FOUND` and `PANE_NOT_FOUND` (wait), `NOT_FOUND` (unknown incident id), `INTERNAL_ERROR`, `FEED_UNAVAILABLE`, and `AGENT_ERROR` (wait with `--wait-exit-on-error`). There is no `INVALID_CURSOR` code. A cursor that is not an integer is rejected by flag parsing, with a message on stderr and exit code 1.

---

## 13. Tests

The contract is checked by:

1. **This document:** internal/robot/attention_contract_doc_test.go (field table, JSON examples, flags, retention, poll interval).
2. **Feed and cursors:** internal/robot/attention_feed_test.go (append, replay, expiry, addressed events, the store poll).
3. **Profiles:** internal/robot/attention_profile_test.go.
4. **Wait conditions:** internal/robot/wait_test.go.
5. **Capabilities:** internal/robot/capabilities_test.go and capabilities_snapshot_test.go.
6. **Transports:** internal/serve/server_test.go, ws_events_test.go, ws_sse_reconnect_contract_test.go.
7. **End to end:** e2e/attention_journey_test.go.

---

## 14. Exit Codes

The attention commands do not all follow the project-wide 0/1/2 convention. Always read `success` and `error_code`.

| Command | 0 | 1 | 2 | 3 |
|---------|---|---|---|---|
| `--robot-events`, `--robot-digest` | Any JSON reply, **including** `success: false` (e.g. `CURSOR_EXPIRED`) | A flag value that cannot be parsed (stderr, no JSON) | — | — |
| `--robot-attention` | Woke on attention | Timed out (`success: true`, `wake_reason: "timeout"`) | `CURSOR_EXPIRED`, feed unavailable, or internal error | — |
| `--robot-wait` | Condition met | `TIMEOUT` | `SESSION_NOT_FOUND`, `INVALID_FLAG`, `PANE_NOT_FOUND`, `CURSOR_EXPIRED`, `INTERNAL_ERROR` | `AGENT_ERROR` |

No attention command returns `NOT_IMPLEMENTED`. Unsupported conditions are reported as `INVALID_FLAG`.

---

## 15. Implementation Notes

### 15.1 Where the Code Lives

- Envelope, types, profiles, capabilities: internal/robot/attention_contract.go
- Feed, cursors, store, poll, heartbeat: internal/robot/attention_feed.go
- `--robot-events`, `--robot-digest`, `--robot-attention`: internal/robot/events.go
- `--robot-wait`: internal/robot/wait.go
- Durable rows: the `attention_events` table in internal/state (runtime_store.go, migrations)
- HTTP, SSE, WebSocket: internal/serve/server.go

### 15.2 Event Generation

Producers call the feed (`Append`, `AppendAddressed`, `AppendDeduplicated`, `PublishEphemeral`). The store assigns the cursor. Events are kept for replay, and the feed delivers them to subscribers. Running `--robot-*` commands can itself append events, such as `health.change` from the source-health refresh.

### 15.3 Logging

The feed logs these contract-relevant conditions with `log/slog`:
- `attention feed store unavailable` / `attention feed store recovered`
- `attention feed poll failed` / `attention feed poll recovered`
- `attention feed poll cursor expired` (with `since_cursor` and `oldest_cursor`, see §17)

---

## 16. Addressed Mail Events

An addressed mail event is a `mail` event that names the agent it is for. ntm writes one when it learns of an unread Agent Mail message for an agent. This happens in the Agent Mail Stop hook (`ntm agent-hook stop`), the dashboard, the idle nudge, and `ntm mail` snooze handling.

- `mail.received`: `interesting`, `info`. Summary `New mail from <sender>: <subject>`.
- `mail.ack_required`: `action_required`, `warning`, when the message asks for an acknowledgment.

Three envelope fields say who the event is for:

| Field | Meaning |
|-------|---------|
| `recipient_project` | Agent Mail project key (usually the project path) |
| `recipient_agent` | Agent Mail agent name |
| `message_id` | Agent Mail message id |

`details` repeats `project_key`, `from`, `to`, `subject`, `message_id`, and `thread_id`, plus `ack_required` on ack events. `next_actions` points at `--robot-mail-check` for that project and agent. A mail event with no recipient is written as an ordinary, unaddressed event.

```json
{
  "cursor": 1250,
  "ts": "2026-11-11T16:50:00.123456789Z",
  "recipient_project": "/home/you/projects/myproject",
  "recipient_agent": "BlueLake",
  "message_id": 4521,
  "category": "mail",
  "type": "mail.received",
  "source": "agent_mail",
  "actionability": "interesting",
  "severity": "info",
  "summary": "New mail from RedStone: Schema review ready",
  "details": {
    "project_key": "/home/you/projects/myproject",
    "from": "RedStone",
    "to": "BlueLake",
    "subject": "Schema review ready",
    "message_id": 4521,
    "thread_id": "br-123"
  },
  "next_actions": [
    {
      "action": "robot-mail-check",
      "args": "--robot-mail-check --mail-project=/home/you/projects/myproject --mail-status=unread --mail-agent=BlueLake --thread=br-123",
      "reason": "Inspect the unread message"
    }
  ]
}
```

**Mail events are unique.** There is at most one mail event per project + recipient + message id, and the recipient name is compared in any letter case. The store enforces this with a unique index on `attention_events(recipient_project, recipient_agent COLLATE NOCASE, message_id)` (migration 017_attention_recipient_unique.sql, which also removed older duplicates and kept the oldest). Inserts use `ON CONFLICT ... DO NOTHING`. Recording the same message again writes nothing, returns the existing event, and does not deliver it to subscribers again, even when two processes record it at the same moment. A feed with no store checks its in-memory journal instead.

**Reading one agent's events.** `--events-recipient=AGENT` returns only events addressed to that agent, in any project and any letter case. The filter runs in the store query, so a busy feed cannot push the agent's events past the scan limit.

```bash
ntm --robot-events --since-cursor=0 --events-recipient=BlueLake
ntm --robot-events --since-cursor=1234 --events-recipient=bluelake --events-limit=20
```

---

## 17. Durable Feed and Cross-Process Delivery

**One store for every process.** Every ntm process uses the SQLite store `state.db` by default. At startup ntm registers a store opener (`SetAttentionStoreOpener`), and the feed opens `state.db` the first time it is used. Commands that never touch the feed never open it. `--robot-overlay` is the one exception and keeps its feed in memory. `state.db` sits next to the selected config file (`NTM_CONFIG`), or else at `$XDG_CONFIG_HOME/ntm/state.db`, or else at `~/.config/ntm/state.db`.

Because all processes write the same table, cursors form a single sequence, and a short-lived command such as `--robot-events`, `--robot-digest`, or `--robot-attention` sees events written by any process: the dashboard, hooks, `ntm serve`, and other robot calls.

**Live delivery across processes.** A long-running store-backed process with subscribers, such as `ntm serve` (which feeds SSE and WebSocket clients), reads other processes' events from the store **every 1 second** (`AttentionPollInterval`). It also reads immediately when it appends an event itself. Each pass reads up to 500 events after the last cursor it delivered and hands them to subscribers in cursor order.

- **Each event is delivered once.** That includes events the process appended itself: in a store-backed feed, its own events are delivered through the same read as everyone else's, not directly, so nothing arrives twice or out of order. A mail event first delivered from memory during a store outage is not delivered again when its stored copy appears.
- **Start point:** when the first subscriber arrives, the read starts at the newest cursor. To get history, replay with a cursor (`since_cursor` on SSE or WebSocket, or `--since-cursor` on the CLI).
- **Expired position:** if the read position has expired (the process fell more than 7 days behind), the read continues from the oldest event still kept. This is reported in the log as `attention feed poll cursor expired`, with the old position (`since_cursor`) and `oldest_cursor`.
- **Failures:** a failed read leaves the position where it was and is retried on the next pass. The first failure and the recovery are logged.
- The once-a-second read never deletes rows. Expired rows are removed by replays (§6.3).
- Feed heartbeats (§10.1) are live-only and never written to the store.

**Store outage.** When `state.db` cannot be written, the process's new events stay in memory with cursors above every stored cursor it has seen. Its own subscribers and replays still get them, but other processes do not, until the store comes back (§11.2).

---

## Appendix A: Example Event Payloads

### A.1 Agent State Change

```json
{
  "cursor": 1301,
  "ts": "2026-11-11T17:00:45.123456789Z",
  "session": "myproject",
  "pane": 2,
  "category": "agent",
  "type": "agent.state_change",
  "source": "state_tracker",
  "actionability": "interesting",
  "severity": "info",
  "summary": "Agent cc_2 transitioned from generating to idle",
  "details": {
    "agent_id": "cc_2",
    "from_state": "generating",
    "to_state": "idle",
    "signal": "agent_state_changed"
  }
}
```

### A.2 File Conflict

```json
{
  "cursor": 1302,
  "ts": "2026-11-11T17:01:00.987654321Z",
  "session": "myproject",
  "category": "file",
  "type": "file.conflict",
  "source": "conflict_detector",
  "actionability": "action_required",
  "severity": "error",
  "summary": "File conflict: internal/robot/types.go modified by [cc_1 cc_2]",
  "details": {
    "file": "internal/robot/types.go",
    "agents": ["cc_1", "cc_2"],
    "signal": "file_conflict"
  },
  "next_actions": [
    {
      "action": "robot-diff",
      "args": "--robot-diff=myproject",
      "reason": "Compare agent changes"
    }
  ]
}
```

### A.3 Mail That Needs an Acknowledgment

```json
{
  "cursor": 1303,
  "ts": "2026-11-11T17:02:00.123456789Z",
  "recipient_project": "/home/you/projects/myproject",
  "recipient_agent": "RedStone",
  "message_id": 4522,
  "category": "mail",
  "type": "mail.ack_required",
  "source": "agent_mail",
  "actionability": "action_required",
  "severity": "warning",
  "summary": "Ack required from BlueLake: Please confirm the migration order",
  "details": {
    "project_key": "/home/you/projects/myproject",
    "from": "BlueLake",
    "to": "RedStone",
    "subject": "Please confirm the migration order",
    "message_id": 4522,
    "thread_id": "br-124",
    "ack_required": true
  },
  "next_actions": [
    {
      "action": "robot-mail-check",
      "args": "--robot-mail-check --mail-project=/home/you/projects/myproject --mail-agent=RedStone --thread=br-124",
      "reason": "Inspect the message before acknowledging it"
    }
  ]
}
```

### A.4 Heartbeat (live only)

The heartbeat's `source` is `attention_feed` (left out of the example below).

```json
{
  "cursor": 1303,
  "ts": "2026-11-11T17:02:30Z",
  "category": "system",
  "type": "system.heartbeat",
  "actionability": "background",
  "severity": "debug",
  "summary": "Heartbeat",
  "details": {
    "subscriber_count": 1,
    "idle_ms": 30000
  }
}
```

---

## Appendix B: Changelog

- **2026-11-11:** Brought in line with the code: integer cursors, real flags (`--since-cursor`, `--events-limit`, `--attention-cursor`, `--wait-until`), real reply fields (`next_cursor`, `has_more`, `replay_window`, `cursor_info`), real profiles, conditions, error codes, and exit codes. Removed claims the code does not support (string cursors, a retention expiry field in events replies, `filters_applied`, `component_health`, `INVALID_CURSOR`, a severity filter, the `quiet`/`verbose` profiles). Added §16 Addressed Mail Events and §17 Durable Feed and Cross-Process Delivery.
- **2026-03-26:** Status updated to IMPLEMENTED; aligned with robot redesign (bd-j9jo3.9.4)
- **2026-03-21:** Initial contract (br-aa0nj)

---

*Reference: br-aa0nj, bd-j9jo3.9.4*
