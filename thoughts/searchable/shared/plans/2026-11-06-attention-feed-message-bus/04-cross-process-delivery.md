# Loop 4: Cross-process delivery

Index: [00-master-index.md](00-master-index.md). Scope: [SCOPE.md](SCOPE.md), process **Cross-Process Delivery**.

## Overview

`Subscribe` handlers only see events appended in their own process (`internal/robot/attention_feed.go:2248`, `:2271`). So a running `ntm serve` doesn't stream events that a dashboard, hook or other ntm process wrote, even though they're all in `state.db`. Readers that poll with `Replay` already see them. After this loop, a store-backed feed with subscribers also hands them events from other processes.

Covers deliverable 7. Needs Loop 1. The hook doesn't depend on this loop, so nothing stops if it comes after Loop 3.

---

## Sprint 1: Other processes' events reach subscribers

### Testable function

"Watch the `ntm serve` event stream, have a dashboard write a mail event, and see it arrive on the stream once."

### Acceptance criteria

- [ ] An event written by another process reaches the `ntm serve` stream at `/api/v1/attention/stream` and its WebSocket `attention` topic.
- [ ] Each event reaches each subscriber once, in cursor order, including events appended in the same process.
- [ ] A subscriber's resume cursor never skips an event because a later one arrived first.
- [ ] A store-backed feed with no subscribers doesn't poll the store, and short-lived commands aren't kept running by it.
- [ ] A failed store read is logged and retried, and `feed.Stop` ends the polling cleanly with no goroutine left behind.

### Requirements

**Data:** No schema change. Reads through `GetAttentionEventsSince` (`internal/state/runtime_store.go:1622`).

**API:**
- A polling goroutine inside the feed, started by the first subscriber and stopped by `Stop`. **Assumption:** it polls every second, matching the `--attention-poll` default.
- **Ordering risk.** If a process hands out its own appends immediately while polling for others, its event with cursor 11 can go out before another process's cursor 10. A client resuming from 11 would then miss 10. **Proposed:** a store-backed feed delivers only through the store poll, and a local append wakes the poll at once so local delivery stays quick. An in-memory feed keeps today's direct delivery. The TDD step should confirm that SQLite's single writer commits cursors in order.
- While a feed is memory-only after a failure (Loop 1, Sprint 3), its in-memory events are still delivered, in the order the replay gives them.
- The existing SSE setup (`prepareAttentionStream`, `internal/serve/server.go:4838`) replays and then subscribes. It must not lose or repeat an event at that handover.

**UI:** None. Existing stream formats don't change.

### Test scenarios

| Scenario | Steps | Expected result |
|---|---|---|
| Other process's event | Second feed on the same file appends | Subscriber gets it once |
| Own event | Subscribed feed appends | Subscriber gets it once |
| Interleaved writers | Two feeds append many events at once | Each subscriber sees every cursor once, in increasing order |
| Replay handover | Events written during stream setup | Each reaches the client once |
| No subscribers | Store-backed feed with no handlers | No store polling |
| Unsubscribe and stop | Remove the last handler, then call `Stop` | Polling ends, and the leak check passes |
| Read failure | Fake store fails, then recovers | One log line, and missed events still arrive |
| Expired cursor | The poll's cursor falls out of retention | Continues from the oldest event and reports it |
| Slow handler | One handler blocks briefly | Other events aren't lost, matching today's delivery behavior |

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop4 ./cmd/ntm`.
2. In a scratch project folder, create a session: `/tmp/ntm-loop4 spawn streamtest --cc=1`.
3. Start the server: `/tmp/ntm-loop4 serve --port 8080`.
4. In a second shell, watch the stream: `curl -N http://127.0.0.1:8080/api/v1/attention/stream | grep --line-buffered mail.received`.
5. In a third shell, send mail: `/tmp/ntm-loop4 mail send streamtest --to <agent> "stream test"`.
6. Open `/tmp/ntm-loop4 dashboard streamtest`, and wait for the `✉` badge.
7. Pass: within a few seconds, the second shell shows one `mail.received` event naming your message.
8. Quit the dashboard and wait a minute.
9. Pass: the same event hasn't appeared again.
