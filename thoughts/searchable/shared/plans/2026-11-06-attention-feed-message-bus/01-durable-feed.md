# Loop 1: Durable feed

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Durable Feed Opening**.

## Overview

Today only `--robot-*` commands and `ntm serve` use the SQLite feed. A dashboard's mail events live in its memory and vanish when it exits. After this loop, any ntm process uses `state.db` the first time it uses the feed, events last 7 days, and a failed store write no longer leaves a process in memory for the rest of its life.

Covers deliverables 1, 5 and 2, in that order. Needs nothing from earlier loops.

---

## Sprint 1: Durable feed everywhere

### Testable function

"Run a dashboard until it shows new mail, quit it, then see the `mail.received` event from another process with `ntm --robot-events --events-category=mail`."

### Acceptance criteria

- [ ] A dashboard's `mail.received` event is in `state.db` after the dashboard exits.
- [ ] A separate `ntm --robot-events` process returns that event.
- [ ] A command that never uses the feed, such as `ntm version`, doesn't open `state.db`.
- [ ] `go test -short ./internal/robot/... ./internal/tui/dashboard/... ./internal/serve/...` never touches the real `state.db`.
- [ ] `--robot-overlay` still skips persistence, and the projection refresh still runs only for `--robot-*` and `ntm serve`.

### Requirements

**Data:** No schema change. Uses the existing `attention_events` table in `state.db`.

**API:**
- ntm's startup registers a function that opens and migrates the store. The first `GetAttentionFeed` call (`internal/robot/attention_feed.go:2956`) uses it to build a store-backed feed, once per process.
- Without a registered function, as in tests, the feed stays in memory.
- The store is closed on process exit, including when the dashboard exits.
- `initializeRobotPersistence` (`internal/cli/root.go:2517`) and `runServe` keep their projection refresh and use the same store.

**UI:** None in this sprint. Reporting an open failure is Sprint 3.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Dashboard event survives exit | Publish through the feed from a process with the opener registered, close it, reopen | Event is returned by `Replay` |
| Two processes | Writer process appends, reader process replays | Reader sees the event |
| No opener | Call `GetAttentionFeed` with no opener registered | In-memory feed, with no file created |
| Opened once | Call `GetAttentionFeed` many times, concurrently | The opener runs once |
| Open failure | Opener returns an error | In-memory feed is returned, and nothing panics |
| Command that doesn't use the feed | Run `ntm version` with an empty config folder | No `state.db` is created |

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop1 ./cmd/ntm`.
2. Make a scratch project folder and create a session: `/tmp/ntm-loop1 spawn feedtest --cc=1`.
3. Find the agent's name: `/tmp/ntm-loop1 --robot-status`.
4. Send it mail: `/tmp/ntm-loop1 mail send feedtest --to <agent> "durable feed test"`.
5. Open `/tmp/ntm-loop1 dashboard feedtest`, and wait up to 60 seconds for `Mail: 1 unread` in the bottom ticker. The per-pane `✉` badge only appears in terminals under 120 columns.
6. Quit the dashboard.
7. In another shell, run `/tmp/ntm-loop1 --robot-events --events-category=mail | jq '.events[] | select(.type=="mail.received") | .summary'`.
8. Pass: the summary names your message.

---

## Sprint 2: 7 day retention

### Testable function

"Write an event, wait over an hour, and see `ntm --robot-events` still return it, with an expiry 7 days out in `state.db`."

### Acceptance criteria

- [ ] New events are saved with `expires_at` 7 days after their timestamp.
- [ ] An event over an hour old is still returned by `--robot-events`.
- [ ] A cursor over an hour old doesn't return `CURSOR_EXPIRED`.
- [ ] An event past 7 days is removed by the existing cleanup.
- [ ] The in-memory journal's size limit stays at 10,000 events.

### Requirements

**Data:** No schema change. Events saved before this change keep their existing 1 hour expiry.

**API:** `DefaultAttentionFeedConfig` (`attention_feed.go:98`) sets `RetentionPeriod` to 7 days. Anything that documents the old value is updated.

**UI:** None.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| New event expiry | Append an event and read `expires_at` | 7 days after `ts` |
| Inside retention | Replay an event dated 2 days ago | Returned |
| Past retention | Replay after an event's `expires_at` has passed | Removed by cleanup, and an old cursor gets `CURSOR_EXPIRED` |
| Boundary | Event exactly at its `expires_at` | Follows the store's existing cleanup comparison, and the test pins it |

### Human test script

1. Create an event, as in Sprint 1, or by running `/tmp/ntm-loop1 --robot-status` in a project.
2. Run `sqlite3 -readonly ~/.config/ntm/state.db "select cursor, ts, expires_at from attention_events order by cursor desc limit 1"`.
3. Pass so far: `expires_at` is 7 days after `ts`.
4. Note the cursor, and wait over an hour.
5. Run `/tmp/ntm-loop1 --robot-events --since-cursor=<cursor minus 1> | jq '.events[0].cursor, .success'`.
6. Pass: it prints your cursor and `true`.

---

## Sprint 3: No silent drop to memory

### Testable function

"Hold a lock on `state.db` while a dashboard runs, see a warning, release the lock, and see later events from that same dashboard saved."

### Evidence check at sprint start

**Writer contention (scope Evidence Needed item 6).** Run many processes writing to one `state.db` at once, and record whether they all succeed inside the 5 second busy timeout. If they don't, adjust the retry approach before building recovery.

### Acceptance criteria

- [ ] A store write failure no longer leaves a process memory-only for life (`attention_feed.go:534-552`).
- [ ] A later append that succeeds is durable and readable by other processes.
- [ ] Failure and recovery each produce one log line with the error, and a running dashboard shows a visible warning. **Assumption:** the warning uses the dashboard's existing ticker.
- [ ] Cursors stay in increasing order within a process after recovery, and events held in memory stay readable by that process until it exits.
- [ ] A process that can't open the store at all keeps working in memory, reports it, and tries again.

### Requirements

**Data:** No schema change. Events held in memory during a failure aren't written to the store later, as the scope's **Store Recovery** policy says.

**API:**
- Replace the one-way switch `ephemeralFromCursor` with a state that can return to the store. Replay and stats at `attention_feed.go:3926` and `:4067` must handle events from both sources.
- Store cursors come from AUTOINCREMENT, and other processes write too. So a cursor issued while in memory may also be issued by the store later. The design has to prevent duplicates in a process's replay, and the TDD step should settle how.
- The feed exposes whether it is store-backed and its last store error.

**UI:** A short dashboard ticker warning while the feed is memory-only, which clears on recovery.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Single failure | Fake store fails once, then succeeds | Second event is in the store |
| Long outage | Fake store fails many times, then succeeds | One failure report, one recovery report |
| Replay across recovery | Stored, memory-only, then stored events | Each is returned once, in cursor order |
| Cursor clash | Another writer takes a cursor a memory-only event used | No duplicate or lost event in the replay |
| Open fails, then works | Opener fails first, then succeeds | Later events are durable |
| Many writers (real file) | Many processes append at once | All succeed, or failures are reported and recovered |

### Human test script

Do this with no other ntm agents running, because the lock blocks all writers to `state.db`.

1. Start `/tmp/ntm-loop1 dashboard feedtest`, and keep the session from Sprint 1.
2. In another shell, hold the lock for 60 seconds: `printf 'BEGIN EXCLUSIVE;\n.shell sleep 60\nCOMMIT;\n' | sqlite3 ~/.config/ntm/state.db`.
3. Send mail: `/tmp/ntm-loop1 mail send feedtest --to <agent> "during lock"`. If the command itself is blocked by the lock, note that and send it through the agent's MCP tools instead.
4. Within about 60 seconds, the dashboard shows the ticker warning.
5. Let the lock end. Send another message: `/tmp/ntm-loop1 mail send feedtest --to <agent> "after lock"`.
6. Wait for the next poll. The ticker warning clears.
7. Run `sqlite3 -readonly ~/.config/ntm/state.db "select summary from attention_events where event_type='mail.received' order by cursor desc limit 3"`.
8. Pass: the "after lock" message is listed, written by the same dashboard you never restarted.
