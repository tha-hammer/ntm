# Loop 4: Cross-Process Delivery TDD Implementation Plan

Loop document: [04-cross-process-delivery.md](04-cross-process-delivery.md). Scope: [SCOPE.md](SCOPE.md), process **Cross-Process Delivery**. Index: [00-master-index.md](00-master-index.md).

**Beads:** epic `bd-fx927`, with one bead for the loop's one sprint: `bd-fx927.1`.

## Overview

After this loop, a store-backed attention feed with at least one subscriber reads `state.db` every second and hands each subscriber the events other processes wrote, once each, in cursor order. A running `ntm serve` then streams a mail event that a dashboard, a hook or any other ntm process wrote. Events the feed appends itself go the same way, and an append wakes the poll at once, so local delivery stays quick. A feed with no store keeps today's direct delivery.

There are 14 testable behaviors in 1 sprint: 5 BLOCKING and 9 LEAF.

**Change from the approved list:** Behavior 7 (short-lived commands aren't kept running) was listed as a BLOCKING closure test. No short-lived command subscribes, and Go exits when `main` returns whatever goroutines are running, so a closure test of exit time can't go red at the poll. It is a LEAF unit test here. The closure exit check stays as a support test.

## Current State Analysis

`Subscribe` (`internal/robot/attention_feed.go:2619`) only adds a handler to a map. Handlers are called from `drainPendingEvents` (`:1262`), which runs only after this process's own `Append`, `AppendAddressed` or `PublishEphemeral`. Nothing ever reads the store for new rows. So `ntm serve` (`internal/cli/serve.go:99`) builds a store-backed feed and streams only its own appends. Readers that poll with `Replay`, such as `--robot-attention`, already see other processes' events.

### Key Discoveries

- **Store reads.** `AttentionStore.GetAttentionEventsSince(sinceCursor, limit)` (`attention_feed.go:34`) returns rows with a cursor above `sinceCursor`. `replayFromStore` (`:4330`) wraps it. It checks the replay window, reports `CursorExpiredError` when the cursor has left retention, merges this process's memory-only events (`mergeByCursor`, `:716`), hydrates operator state, and falls back to the journal when the window read fails. It also runs `store.GCExpiredEvents()` on every call (`:4366`), which is a write. A once-a-second poll must not do that.
- **Cursor order on commit.** `Store.AppendAttentionEvent` (`internal/state/runtime_store.go:1606`) is one autocommit `INSERT`, and `LastInsertId` is the cursor. SQLite has one writer at a time, and the row's AUTOINCREMENT id is chosen inside that write. So rows commit in cursor order, and a reader never sees cursor 11 before 10. Behavior 4 checks this with real processes.
- **The ordering risk is real today.** `Append` (`:780`) delivers its own event at once. A poll that also delivers other processes' events would give one subscriber 11 (own) before 10 (other). The SSE live loop (`internal/serve/server.go:5106`) and the WebSocket path (`deliverAttentionEvent`, `server.go:4588`) both skip any cursor at or below the last one sent, so 10 would be lost. The plan routes every store-backed delivery through the poll.
- **Heartbeat cursors clash with stored cursors.** `PublishEphemeral` (`:910`) takes `f.cursor.Next()`. In a store-backed feed that counter tracks the store's newest cursor, so a heartbeat takes the number the next stored event will get. `storeBackedStats` (`:4487`) reports `NewestCursor = max(store newest, f.cursor.Current())`. The SSE handover sets its replay boundary from that (`server.go:4866`) and skips live events at or below it. So after a heartbeat, the next stored event can be skipped at the handover.
- **SSE handover.** `prepareAttentionStream` (`server.go:4838`) subscribes first into a 100-slot channel (a full channel drops, with a count), then reads `Stats`, then replays up to `stats.NewestCursor`. The live loop starts its cursor at that boundary (`server.go:5056`). This is correct as long as the poll's start point is fixed inside `Subscribe`, before `Stats` is read.
- **WebSocket.** `Server.Start` subscribes once and broadcasts every event to the hub topics `attention` and `attention:<session>` (`server.go:1146`). A client's own attention subscription (`handleAttentionSubscribe`, `server.go:4514`) also calls `c.Subscribe(topics)` with the same topics. **Check at Red:** if one client gets both the hub broadcast and its own delivery, Behavior 2 goes red for that reason, not the poll. The fix then is to leave attention topics out of the hub subscription in `handleAttentionSubscribe`.
- **Outage recovery.** During a store write outage, `Append` and `AppendAddressed` keep events in `memoryOnly` (`:672`) and deliver them. When an addressed event is published again after recovery, it is saved with a new store cursor, and `forgetMemoryOnlyLocked` reports it was already delivered (`:858`). A poll would see that new row and deliver the same message again unless it remembers what it delivered from memory.
- **Existing delivery pattern.** `heartbeatLoop` (`:2676`) is the model for the poll loop. It runs on `stopWg`, ends on `stopCh`, and skips work while `subscriberCount() == 0`.
- **Tests that rely on synchronous delivery from a store-backed feed:** only `TestAppendAddressed_ManyGoroutines` (`attention_feed_test.go:6079`) and `TestAppendAddressed_OutageThenRecovery` (`:6115`). Each counts deliveries right after appending. They must wait for the poll. Their assertions stay the same. The tamper check will list them, and the build record says why. Every other Subscribe test uses a feed with no store.
- **Test helpers.** `openStoreFile` (`attention_feed_test.go:5200`) gives each feed its own connection to one file, so two feeds behave like two processes. `appendAged` (`:5212`), `testAttentionEvent` (`:4945`), `faultInjectingStore` (`:5365`), `newTestClock` (`:5348`) and `captureSlog` (`:5429`) exist. In `internal/cli`, `newClosureEnv`, `helperCommand`, the `append-events`, `publish-dashboard-mail` and `execute` helper modes, `readAllCursors` and `privateTmux` exist (`attention_closure_test.go`, `agent_hook_test.go`).
- **`ntm serve`.** It defaults to `--auth-mode local` (`server.go:822`) on `127.0.0.1`. Routes: `GET /api/v1/attention/stream` (`server.go:1077`) with `since_cursor`, and `GET /api/v1/ws` (`server.go:1087`) with a `subscribe` message whose data holds `topics` and `since_cursor` (`server.go:4430`). It runs a projection refresh every 15 seconds (`serve.go:118`), which calls tmux, so closure tests give it a private tmux server.

## Desired End State

`curl -N http://127.0.0.1:8080/api/v1/attention/stream` shows a `mail.received` event within about a second of a dashboard in another shell writing it, once. The same holds for a WebSocket client on the `attention` topic. Stopping the server stops the poll. A short command that never subscribes never polls.

### Observable Behaviors

Listed under Sprint 1 below, in Given / When / Then form.

## What We're NOT Doing

- No schema change and no new store method. The poll uses `GetAttentionEventsSince` and `GetAttentionReplayWindow`.
- No change to `--robot-attention`. It already polls with `Replay`.
- No change to stream formats, the SSE buffer size or its drop counting.
- No push signal between processes, such as a file watch or a socket. The poll is the only cross-process path, by the loop document.
- **Accepted limit, from Loop 1:** during a store write outage, a memory-only event can share a cursor with a stored event another process writes. The poll delivers the memory event, and the stored one with the same cursor is not delivered. Replay keeps both. This needs a write outage and another writer in the same second.
- **Accepted limit:** a handler that blocks holds up delivery to every subscriber of that feed, as today. `Stop` waits for a blocked handler to return.

**Design decisions (from the loop document, as approved):**
- A store-backed feed delivers only through the poll. A local append wakes it.
- A feed with no store delivers directly, as today.
- The poll interval is 1 second, matching the `--attention-poll` default.

## Testing Strategy

- **Framework:** Go standard `testing`.
- **Database:** real SQLite files in `t.TempDir()`. In `internal/robot`, through `openStoreFile`. In `internal/cli`, through ntm's real startup in helper processes.
- **Poll driver:** a new option, `WithAttentionPollTicker`, takes the function that makes the poll's ticker. Unit tests pass a fake ticker they tick by hand and record its interval and `Stop` calls. Production and the closure tests use the real one-second ticker.
- **Waiting:** delivery happens on the poll goroutine, so tests collect events on a channel and wait with a deadline. A missing event fails the test. "No more events" is checked by ticking the fake ticker, then calling `waitForPollPass`, which waits for the pass that tick started to finish.
- **Closure tests:** `internal/cli` starts `ntm serve` as a long-running helper process on a free port, with a private tmux server and a closed Agent Mail port. Writers are separate helper processes. The test reads the real SSE stream or WebSocket over HTTP. A helper that fails, or a server that never answers, fails the test. No closure test calls `t.Skip`.
- **Property tests:** `pgregory.net/rapid`, already in `go.mod`.

**Environment on this VPS:**
- Go 1.26.3 is at `/usr/local/go/bin/go`, and isn't on the default PATH, so run `export PATH=$PATH:/usr/local/go/bin` first.
- `golangci-lint`, `goimports` and `rch` aren't installed here. If they're still missing at build time, report those checks as not run, and don't count them as passed.
- Red runs of property tests leave a record in `testdata/rapid/`. Move it out of the repo before committing.

## Workflow Closure

Anchors come from the research's Chain C (`thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`, "Workflow Closure Map"): projection refresh or append in one process, `state.db`, and a read in another process, where the reader polls. Today that reader is `Replay`. This loop adds a reader inside the feed, whose consumer is `Subscribe`, and through it Chain B's serve stream.

- **`highest_new_connector`:** the poll loop that `Subscribe` starts, which reads the store and calls `notifySubscribers`.
- **Trigger choice:** BLOCKING behaviors 1, 2 and 4 start at `Execute` with `serve` in one helper process and with a writer command in another. Behavior 3 starts at `AttentionFeed.Append` on a subscribed feed. Behavior 5 starts at the SSE handler over real HTTP.

### Production Operation Chain

`writer process: dashboard poll / agent-hook stop / any Append -> AttentionFeed.Append -> Store.AppendAttentionEvent -> state.db -> (ntm serve) pollLoop tick -> pollOnce -> replayFromStore (GetAttentionReplayWindow, GetAttentionEventsSince) -> notifySubscribers -> prepareAttentionStream handler / WSClient.deliverAttentionEvent / hub broadcast -> SSE writer / WebSocket -> client`

**Production callers:**

| New code | Production caller |
|---|---|
| `AttentionFeed.startPollLocked` | `AttentionFeed.Subscribe`, on the first subscriber of a store-configured feed |
| `AttentionFeed.pollLoop` | `startPollLocked` |
| `AttentionFeed.pollOnce` | `pollLoop`, on each tick and each wake |
| `AttentionFeed.wakePoll` | `Append`, `AppendAddressed` |
| `AttentionPollInterval` | `pollLoop` |
| `WithAttentionPollTicker` | tests only. Production uses the default real ticker |
| `replayFromStore` without its cleanup call | `Replay` and `ReplayForRecipient` run the cleanup themselves, `pollOnce` doesn't |

### Closure Test: A dashboard's mail event reaches the `ntm serve` stream once [BLOCKING]

Behavior 1.
- **SOURCE (seed only)**: a new `state.db` in the closure env's config folder. A private tmux server.
- **TRIGGER (start)**: `Execute` with `serve --port <free port>` in a long-running helper process. Then `publish-dashboard-mail` in a second helper process, which goes through the real dashboard startup and its mail poll path. (boundary = highest_new_connector)
- **DRIVERS (async edges)**: the serve process's real one-second poll. The test reads the stream with a 5 second deadline for the event, then 3 more seconds to check for a repeat.
- **OBSERVE (assert via)**: `GET /api/v1/attention/stream?since_cursor=-1` over real HTTP, parsed as SSE. Then `--robot-events --events-category=mail` in a third process, to tie the streamed cursor to the stored row.
- **FORBIDDEN SPAN**: `pollLoop`, `pollOnce`, `replayFromStore`, `notifySubscribers`, `prepareAttentionStream`, raw SQL.
- **RED-AT-SEAM proof**: don't start the poll from `Subscribe`. Expected: no `mail.received` event within 5 seconds. (Today's code gives the same red.)
- **DRIVABILITY**: `state.db` location from `XDG_CONFIG_HOME`, tmux from `TMUX_TMPDIR`, a closed `AGENT_MAIL_URL`. The span is async, and the driver is the real ticker, bounded by deadlines.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. If the server doesn't answer `GET /api/v1/attention/events` within 10 seconds, the test fails. The cleanup sends SIGINT, waits 5 seconds, then kills.

### Closure Test: Another process's event reaches a WebSocket `attention` client once [BLOCKING]

Behavior 2.
- **SOURCE (seed only)**: as Behavior 1.
- **TRIGGER (start)**: `Execute` with `serve` in a helper process. A WebSocket client connects to `/api/v1/ws` and sends `{"type":"subscribe","data":{"topics":["attention"],"since_cursor":-1}}`. Then `publish-dashboard-mail` in a second helper process.
- **DRIVERS (async edges)**: the real poll, with the same deadlines as Behavior 1.
- **OBSERVE (assert via)**: WebSocket frames of type `event` whose data is a `mail.received` event for message 42.
- **FORBIDDEN SPAN**: `pollOnce`, `notifySubscribers`, `WSClient.deliverAttentionEvent`, `WSHub.Publish`.
- **RED-AT-SEAM proof**: don't start the poll from `Subscribe`. Expected: no frame within 5 seconds.
- **DRIVABILITY**: as Behavior 1. The client is `github.com/gorilla/websocket`, already a dependency.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

### Closure Test: An event the feed appends itself reaches its subscriber once [BLOCKING]

Behavior 3.
- **SOURCE (seed only)**: a `state.db` file opened through `openStoreFile`.
- **TRIGGER (start)**: `AttentionFeed.Append` and `AttentionFeed.AppendAddressed` on a feed with one subscriber.
- **DRIVERS (async edges)**: the poll goroutine, woken by the append. A fake ticker shows the wake alone delivers, with no tick needed.
- **OBSERVE (assert via)**: the handler's received events. Then `Replay(0, 10)` on a second feed opened on the same file.
- **FORBIDDEN SPAN**: `pollOnce`, `drainPendingEvents`, `notifySubscribers`, `memoryOnly`.
- **RED-AT-SEAM proof**: keep the direct delivery in `publishLocked` for store-backed feeds as well as the poll. Expected: the handler gets each event twice.
- **DRIVABILITY**: store file, fake ticker through `WithAttentionPollTicker`.
- **EXECUTION (must run)**: runs in `go test -short ./internal/robot/`.

### Closure Test: Events from writers running at once reach the stream in cursor order [BLOCKING]

Behavior 4.
- **SOURCE (seed only)**: a new `state.db` and a private tmux server.
- **TRIGGER (start)**: `Execute` with `serve` in a helper process, with an SSE client connected. Then 4 `append-events 20` helper processes started together.
- **DRIVERS (async edges)**: the real poll. The test reads until it has 80 events from the contention session, with a 10 second deadline.
- **OBSERVE (assert via)**: the streamed cursors. Then `readAllCursors`, which reads the stored rows with `--robot-events` in another process.
- **FORBIDDEN SPAN**: `Store.AppendAttentionEvent`, `pollOnce`, raw SQL.
- **RED-AT-SEAM proof**: in `pollOnce`, set the next start point to the newest cursor in the replay window instead of the last event delivered. Expected: events that commit during a pass are skipped, and the streamed set is smaller than the stored set. Behavior 4's property test in `internal/robot` catches the same change with a fixed seed.
- **DRIVABILITY**: as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. 80 events stay under the SSE handler's 100-slot buffer, so a drop would be a real failure. Run it 20 times at build time.

### Closure Test: A resumed stream gets every event after its cursor once, across the handover [BLOCKING]

Behavior 5.
- **SOURCE (seed only)**: a `state.db` file with 10 events, and a store-backed feed on it installed as the global feed.
- **TRIGGER (start)**: `GET /api/v1/attention/stream?since_cursor=5` on an `httptest.NewServer` around the real router, while a second feed on its own connection appends 30 events.
- **DRIVERS (async edges)**: the feed's real poll goroutine (real ticker), and the writer goroutine. The reader waits up to 5 seconds for cursor 40.
- **OBSERVE (assert via)**: the streamed SSE `attention` events, parsed from the HTTP response.
- **FORBIDDEN SPAN**: `prepareAttentionStream`, `filterAttentionReplayBoundary`, `pollOnce`.
- **RED-AT-SEAM proof**: in `Subscribe`, read the poll's start point lazily inside the poll goroutine instead of before `Subscribe` returns. Expected: events written between the replay boundary and the poll's first read are missing. With a fixed writer schedule, the test also runs the writer from inside a test hook on `Stats` so the gap appears every time.
- **DRIVABILITY**: store file; the real router through `setupTestServer`.
- **EXECUTION (must run)**: runs in `go test -short ./internal/serve/`.

---

## Sprint 1: Other processes' events reach subscribers

**Bead:** `bd-fx927.1`.

**Testable function:** "Watch the `ntm serve` event stream, have a dashboard write a mail event, and see it arrive on the stream once."

**Order of work:** write 1 as the outer test. Then 6, 3, 13, 14, 12, 9, 10, 11, 8, 7, then 4 and 5, then 2. Behavior 1 goes green last.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 1 |
| An event written by another process reaches the `ntm serve` stream at `/api/v1/attention/stream` and its WebSocket `attention` topic | 1, 2 |
| Each event reaches each subscriber once, in cursor order, including events appended in the same process | 3, 4, 12, 13, 14 |
| A subscriber's resume cursor never skips an event because a later one arrived first | 4, 5, 13 |
| A store-backed feed with no subscribers doesn't poll the store, and short-lived commands aren't kept running by it | 6, 7 |
| A failed store read is logged and retried, and `feed.Stop` ends the polling cleanly with no goroutine left behind | 8, 9 |
| Expired cursor continues from the oldest event and is reported (loop document, test scenarios; scope Failure) | 10 |
| Slow handler: other events aren't lost (loop document, test scenarios) | 11 |
| A memory-only feed's events are still delivered in replay order (loop document, API) | 12 |

### Behavior 1: A dashboard's mail event reaches the `ntm serve` stream once [BLOCKING]

#### Test Specification

**Given** `ntm serve` running on a new `state.db`, with an SSE client connected to `/api/v1/attention/stream?since_cursor=-1`. **When** a separate process runs the dashboard's mail path and writes a `mail.received` event for message 42. **Then** the client gets exactly one `mail.received` event for message 42 within 5 seconds. Its cursor matches the row `--robot-events` lists. 3 seconds later, the event still hasn't appeared again.

**Edge Cases:** the server started before the writer's `state.db` existed (the server creates and migrates it, so both use one file). A heartbeat from the server's own feed may arrive first, and the SSE path drops it.

**Property:** no property: a fixed two-process journey. Ordering properties are in Behavior 4.

**Files touched:** `internal/cli/attention_closure_test.go` (`startServe`, `readAttentionStream`, the test). The feed changes are in `internal/robot/attention_feed.go`, under Behaviors 3 to 14.

#### TDD Cycle

##### 🔴 Red

**File:** `internal/cli/attention_closure_test.go`

```go
// closureServe is a running `ntm serve` helper process.
type closureServe struct {
	baseURL string
	cmd     *exec.Cmd
}

// startServe runs `ntm serve` on a free port as a helper process, waits until it
// answers, and stops it on cleanup with SIGINT, then a kill after 5 seconds.
func (e *closureEnv) startServe(t *testing.T) *closureServe

// attentionStream reads SSE `attention` events from the server in the background.
type attentionStream struct {
	events chan robot.AttentionEvent
	cancel context.CancelFunc
}

func (s *closureServe) openAttentionStream(t *testing.T, sinceCursor int64) *attentionStream

// next returns the next event that matches, or fails the test at the deadline.
func (a *attentionStream) next(t *testing.T, match func(robot.AttentionEvent) bool, within time.Duration) robot.AttentionEvent

// none fails the test if a matching event arrives within the wait.
func (a *attentionStream) none(t *testing.T, match func(robot.AttentionEvent) bool, wait time.Duration)

func isMail42(e robot.AttentionEvent) bool {
	return e.Type == robot.EventTypeMailReceived && e.MessageID == 42
}

func TestClosure_ServeStreamsOtherProcessMail(t *testing.T) {
	env := newClosureEnv(t)
	env.privateTmux(t)
	serve := env.startServe(t)
	stream := serve.openAttentionStream(t, -1)

	env.run(t, "publish-dashboard-mail")

	got := stream.next(t, isMail42, 5*time.Second)
	stream.none(t, isMail42, 3*time.Second)

	stored := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-category=mail"))
	if len(stored) != 1 || stored[0].Cursor != got.Cursor {
		t.Fatalf("streamed cursor %d, stored %+v", got.Cursor, stored)
	}
}
```

`startServe` picks a port by listening on `127.0.0.1:0` and closing it, starts `execute serve --port <port>` with `helperCommand` and `cmd.Start`, and polls `GET /api/v1/attention/events` until it answers 200. If the helper exits early, the test fails with its stderr.

Expected failure: `no matching event within 5s`.

##### 🟢 Green

No code in this behavior. It goes green when Behaviors 3 to 14 are in.

##### 🔵 Refactor

- [ ] **No duplication:** `startServe` reuses `helperCommand`. The stream reader is shared with Behavior 4.
- [ ] **Reveals intent:** `next` and `none` read as the assertion they make.
- [ ] **Complexity went down:** one reader goroutine per stream, ended by `cancel` on cleanup.
- [ ] **No shallow wrappers:** `closureServe` owns the process and its cleanup.
- [ ] **Fits existing patterns:** the helper process and `parseRobotEvents` follow `attention_closure_test.go`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_ServeStreamsOtherProcessMail ./internal/cli/` fails first, then passes, with red-at-seam recorded.

**Manual:** the human test script below.

### Behavior 2: Another process's event reaches a WebSocket `attention` client once [BLOCKING]

#### Test Specification

**Given** `ntm serve` and a WebSocket client subscribed to `attention` with `since_cursor` -1. **When** another process writes a `mail.received` event for message 42. **Then** the client gets exactly one `event` frame for it within 5 seconds, and no second one in the next 3 seconds.

**Edge Cases:** the subscribe reply arrives first and is skipped. Frames for heartbeats are skipped.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/attention_closure_test.go`. If the duplicate described in Key Discoveries shows up at Red: `internal/serve/server.go` (`handleAttentionSubscribe`).

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_ServeWebSocketGetsOtherProcessMail(t *testing.T) {
	env := newClosureEnv(t)
	env.privateTmux(t)
	serve := env.startServe(t)
	ws := serve.openAttentionSocket(t) // dials /api/v1/ws, sends the subscribe message, waits for its reply

	env.run(t, "publish-dashboard-mail")

	ws.next(t, isMail42, 5*time.Second)
	ws.none(t, isMail42, 3*time.Second)
}
```

`openAttentionSocket` returns the same `next` and `none` methods as the SSE reader, over an `events` channel fed by a reader goroutine that decodes `WSEvent` frames.

Expected failure: `no matching event within 5s`. If instead it fails on `none` with a second frame, the duplicate comes from the hub broadcast, and Green includes the `handleAttentionSubscribe` fix.

##### 🟢 Green

No feed code beyond Behaviors 3 to 14. Only if the duplicate shows up: in `handleAttentionSubscribe` (`server.go:4526`), subscribe the client to its regular topics only, since its attention topics are served by its own feed subscription:

```go
// Attention topics are delivered by the client's own feed subscription, with
// its cursor and filters. Joining the hub topic as well would send them twice.
_, regular := partitionAttentionTopics(topics)
c.Subscribe(regular)
```

##### 🔵 Refactor

- [ ] **No duplication:** the frame reader and the SSE reader share `next` and `none` through one small `eventReader` type.
- [ ] **Reveals intent:** the subscribe message is built from `serve.WSMessage`, not a string.
- [ ] **Complexity went down:** no extra branches in the server.
- [ ] **No shallow wrappers:** none added.
- [ ] **Fits existing patterns:** the subscribe data matches `TestWSAttentionSubscription` (`server_test.go:6490`).

#### Success Criteria

**Automated:** `go test -short -run TestClosure_ServeWebSocketGetsOtherProcessMail ./internal/cli/` fails first, then passes, with red-at-seam recorded.

### Behavior 3: An event the feed appends itself reaches its subscriber once [BLOCKING]

#### Test Specification

**Given** a store-backed feed with one subscriber and a fake poll ticker that is never ticked. **When** the feed appends 3 events with `Append` and 1 with `AppendAddressed`, then publishes the same addressed message again. **Then** the handler gets 4 events, each once, in cursor order, and only the wake delivered them. A second feed on the same file replays the same 4 cursors.

**Edge Cases:** a handler that appends a follow-on event gets it after the current one, as `TestAttentionFeed_SubscriberFollowOnEventPreservesDeliveryOrder` (`attention_feed_test.go:726`) requires for memory feeds. A repeat `AppendAddressed` delivers nothing.

**Property:** Invariant: for any sequence of appends from the subscribed feed and from a second feed on the same file (generated as a list of `{writer: 0|1, addressed: bool, messageID: 1..5}`), the handler's cursors are strictly increasing and equal the stored cursors after the subscribe point, and no addressed message is delivered twice.

**Domain:** sequences of 1 to 40 appends, built from `testAttentionEvent` and `PublishMailPending` so the events are realistic.

```go
rapid.Check(t, func(rt *rapid.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	other := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	got := collect(feed)
	steps := rapid.SliceOfN(appendStepGen(), 1, 40).Draw(rt, "steps")
	for _, s := range steps {
		s.apply(feed, other)
	}
	ticker.tick()
	feed.waitForPollPass(t)
	assertCursorsIncreasing(rt, got.cursors())
	assertSameCursors(rt, got.cursors(), storedCursorsAfter(rt, path, 0))
	assertNoMessageTwice(rt, got.events())
})
```

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
// fakePollTicker is a poll ticker the test ticks by hand.
type fakePollTicker struct {
	ch       chan time.Time
	interval atomic.Int64
	made     atomic.Int32
	stopped  atomic.Int32
}

func newFakePollTicker() *fakePollTicker
func (f *fakePollTicker) factory() func(time.Duration) AttentionPollTicker // records interval and count
func (f *fakePollTicker) tick()

// newPollTestFeed builds a store-backed feed with heartbeats off and the fake ticker.
func newPollTestFeed(t *testing.T, store AttentionStore, ticker *fakePollTicker) *AttentionFeed

// eventSink collects delivered events and lets tests wait for a count.
type eventSink struct{ ch chan AttentionEvent; ... }

func collect(feed *AttentionFeed) *eventSink
func (s *eventSink) waitFor(t *testing.T, n int, within time.Duration) []AttentionEvent

func TestAttentionFeed_OwnAppendDeliveredOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	sink := collect(feed)

	for i := 0; i < 3; i++ {
		feed.Append(testAttentionEvent(fmt.Sprintf("own %d", i)))
	}
	feed.PublishMailPending("/proj", "RedStone", "BlueLake", "hello", 42, "")
	feed.PublishMailPending("/proj", "RedStone", "BlueLake", "hello", 42, "")

	got := sink.waitFor(t, 4, 2*time.Second)
	feed.waitForPollPass(t)
	if extra := sink.drain(); len(extra) != 0 {
		t.Fatalf("extra deliveries: %+v", extra)
	}
	assertIncreasing(t, got)

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	replayed, _, err := reader.Replay(0, 10)
	if err != nil || !sameCursors(got, replayed) {
		t.Fatalf("replayed %+v (err %v), delivered %+v", replayed, err, got)
	}
}
```

`waitForPollPass` is an unexported test hook on the feed: it ticks nothing, but waits until the poll goroutine is idle (no pass running and no wake pending), with a 2 second deadline.

Expected failure: compile error, `undefined: AttentionPollTicker` and `WithAttentionPollTicker`.

##### 🟢 Green

**File:** `internal/robot/attention_feed.go`:

```go
// AttentionPollInterval is how often a store-backed feed with subscribers reads
// events that other processes wrote. It matches the --attention-poll default.
const AttentionPollInterval = time.Second

// attentionPollBatch is the most events one store read returns.
const attentionPollBatch = 500

// AttentionPollTicker is the timer the poll waits on.
type AttentionPollTicker interface {
	C() <-chan time.Time
	Stop()
}

// WithAttentionPollTicker sets how the feed makes its poll ticker. Tests pass a
// ticker they tick by hand. The default is a time.Ticker.
func WithAttentionPollTicker(newTicker func(time.Duration) AttentionPollTicker) AttentionFeedOption

// New AttentionFeed fields:
//	pollMu        sync.Mutex
//	pollStarted   bool
//	pollStopped   bool                // set by Stop, so a later Subscribe starts nothing
//	pollFrom      int64               // the last cursor delivered; guarded by pollMu
//	pollGen       uint64              // bumped when the start point is reset
//	pollWake      chan struct{}       // capacity 1
//	newPollTicker func(time.Duration) AttentionPollTicker
```

`Subscribe` adds the handler, then, for a store-configured feed whose subscriber count went from 0 to 1, sets `pollFrom` to the feed's newest cursor before it returns, and starts the poll goroutine the first time:

```go
func (f *AttentionFeed) Subscribe(handler AttentionHandler) func() {
	f.subMu.Lock()
	id := f.subNextID.Add(1)
	f.subs[id] = subscription{id: id, handler: handler}
	first := len(f.subs) == 1
	f.subMu.Unlock()

	if first && f.storeConfigured() {
		f.pollMu.Lock()
		f.resetPollFromLocked() // newest stored or memory-only cursor, read now
		f.startPollLocked()
		f.pollMu.Unlock()
	}
	return func() {
		f.subMu.Lock()
		defer f.subMu.Unlock()
		delete(f.subs, id)
	}
}
```

`pollLoop` follows `heartbeatLoop`: it runs on `stopWg`, returns on `stopCh`, and waits on the ticker or `pollWake`, then calls `pollOnce`. `pollOnce` does nothing with no subscribers. Otherwise it reads `replayFromStore(store, from, attentionPollBatch, store.GetAttentionEventsSince, nil)`, calls `notifySubscribers` for each event, moves `pollFrom` forward after each one (unless `pollGen` changed), and reads again while a full batch came back.

`Append` and `AppendAddressed` in a store-configured feed call `f.journal.Append` and `f.wakePoll()` instead of `enqueuePendingEventLocked`. `publishLocked` keeps direct delivery for a feed with no store:

```go
// publishLocked keeps the event in the journal, then delivers it. A
// store-configured feed delivers through its poll, so this process's events and
// other processes' events reach subscribers in one cursor order.
func (f *AttentionFeed) publishLocked(event AttentionEvent) {
	f.journal.Append(event)
	if f.storeConfigured() {
		f.appendMu.Unlock()
		f.wakePoll()
		return
	}
	startDrain := f.enqueuePendingEventLocked(event)
	f.appendMu.Unlock()
	if startDrain {
		f.drainPendingEvents()
	}
}
```

`replayFromStore` loses its `store.GCExpiredEvents()` call. `Replay` and `ReplayForRecipient` call it before `replayFromStore`, so their behavior doesn't change.

**File:** `internal/robot/attention_feed_test.go`: `TestAppendAddressed_ManyGoroutines` and `TestAppendAddressed_OutageThenRecovery` wait for delivery with `sink.waitFor` and `waitForPollPass` before counting. Their expected counts stay the same.

##### 🔵 Refactor

- [ ] **No duplication:** one delivery path for store-backed feeds. `pollOnce` reuses `replayFromStore`, so expiry, memory merge and hydration live in one place.
- [ ] **Reveals intent:** `publishLocked`'s comment says why a store-backed feed waits for the poll.
- [ ] **Complexity went down:** no second dedup structure between direct and polled delivery.
- [ ] **No shallow wrappers:** `pollOnce` holds the read, the skip rules and the position.
- [ ] **Fits existing patterns:** `pollLoop` mirrors `heartbeatLoop` (`:2676`). The option mirrors `WithAttentionClock` (`:517`).

#### Success Criteria

**Automated:** `go test -short -run 'TestAttentionFeed_OwnAppendDeliveredOnce|TestAttentionFeed_OwnAppendProperty' ./internal/robot/` fails first, then passes. `go test -short ./internal/robot/` stays green, including the two adjusted tests.

### Behavior 4: Events from writers running at once reach the stream in cursor order [BLOCKING]

#### Test Specification

**Given** `ntm serve` with an SSE client from `since_cursor` -1. **When** 4 helper processes each append 20 events to the same `state.db` at once. **Then** the client gets 80 events from the contention session within 10 seconds, with strictly increasing cursors, each once. Their cursors equal the stored cursors `readAllCursors` lists.

**Edge Cases:** a poll pass that starts while a writer is mid-commit. The next pass picks up the row, because the start point is the last event delivered.

**Property:** Invariant, in `internal/robot` as a support test: two writer feeds on separate connections append in parallel goroutines (counts generated 1 to 50 each) while the test ticks the subscriber's fake ticker at random points. The delivered cursors are strictly increasing and equal the stored cursors above the subscribe point. This is also the check that SQLite commits cursors in order: a gap seen and later filled would break "strictly increasing".

```go
rapid.Check(t, func(rt *rapid.T) {
	a, b := rapid.IntRange(1, 50).Draw(rt, "a"), rapid.IntRange(1, 50).Draw(rt, "b")
	// two writer feeds append a and b events in goroutines; the test ticks between
	// rapid-chosen sleeps of 0-2ms until both finish, then ticks once more
	assertCursorsIncreasing(rt, delivered)
	assertSameCursors(rt, delivered, storedCursorsAfter(rt, path, start))
})
```

**Files touched:** `internal/cli/attention_closure_test.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_ServeStreamsParallelWritersInOrder(t *testing.T) {
	const writers, each = 4, 20
	env := newClosureEnv(t)
	env.privateTmux(t)
	serve := env.startServe(t)
	stream := serve.openAttentionStream(t, -1)

	env.runParallel(t, writers, "", "append-events", strconv.Itoa(each))

	inSession := func(e robot.AttentionEvent) bool { return e.Session == closureContentionSession }
	got := make([]int64, 0, writers*each)
	for len(got) < writers*each {
		got = append(got, stream.next(t, inSession, 10*time.Second).Cursor)
	}
	stream.none(t, inSession, 2*time.Second)
	if !strictlyIncreasing(got) {
		t.Fatalf("streamed cursors out of order: %v", got)
	}
	if want := readAllCursors(t, env, closureContentionSession); !slices.Equal(got, want) {
		t.Fatalf("streamed %v, stored %v", got, want)
	}
}
```

Expected failure before Behavior 3's Green: `no matching event within 10s`. After it, this test is the guard for the red-at-seam change in the closure section.

##### 🟢 Green

No new code. Behavior 3's poll provides it. If the property test finds a gap that fills later, `pollOnce` must stop at the first missing cursor below the window's newest and read again on the next tick. Record that in the build record if it happens.

##### 🔵 Refactor

- [ ] **No duplication:** `strictlyIncreasing` is shared with Behavior 5.
- [ ] **Reveals intent:** the test names writers and counts as constants.
- [ ] **Complexity went down:** nothing added to production code.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** reuses `append-events` and `readAllCursors` from `TestClosure_ManyWritersOneStateDB` (`attention_closure_test.go:659`).

#### Success Criteria

**Automated:** `go test -short -run TestClosure_ServeStreamsParallelWritersInOrder -count=20 ./internal/cli/` passes 20 times. The robot property test passes with `-rapid.checks=200`.

### Behavior 5: A resumed stream gets every event after its cursor once, across the handover [BLOCKING]

#### Test Specification

**Given** a store-backed global feed on a file with 10 events. **When** a client opens `/api/v1/attention/stream?since_cursor=5` while a second connection appends 30 events. **Then** the client gets cursors 6 to 40, each once, in order: 6 to 10 or more from the replay and the rest live, with none missing at the switch.

**Edge Cases:** the writer finishes before the request (all from replay). The writer starts after the replay (all live). The test runs all three schedules.

**Property:** no property: three fixed schedules cover the handover cases. The ordering property is in Behavior 4.

**Files touched:** `internal/serve/server_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionStream_ResumeAcrossHandoverMissesNothing(t *testing.T) {
	for _, schedule := range []string{"before", "during", "after"} {
		t.Run(schedule, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			feed := installStoreBackedServeFeed(t, path) // real store file, real poll ticker, heartbeats off
			appendN(t, feed, 10)
			writer := openWriterFeed(t, path)           // second connection, as another process

			srv, _ := setupTestServer(t)
			ts := httptest.NewServer(srv.router)
			t.Cleanup(ts.Close)

			if schedule == "before" {
				appendN(t, writer, 30)
			}
			stream := openSSE(t, ts.URL+"/api/v1/attention/stream?since_cursor=5")
			switch schedule {
			case "during":
				go appendN(t, writer, 30)
			case "after":
				stream.waitConnected(t)
				appendN(t, writer, 30)
			}
			got := stream.cursorsUntil(t, 40, 5*time.Second)
			if !slices.Equal(got, rangeCursors(6, 40)) {
				t.Fatalf("got %v", got)
			}
		})
	}
}
```

Expected failure before Behavior 3's Green: the "during" and "after" cases time out, because the writer's events never arrive live.

##### 🟢 Green

No new code beyond Behavior 3. The test pins two of Behavior 3's choices: the start point is set inside `Subscribe`, before `prepareAttentionStream` reads `Stats`, and Behavior 13 keeps heartbeats from lifting the replay boundary.

##### 🔵 Refactor

- [ ] **No duplication:** `openSSE` reuses the SSE parsing already in `server_test.go` if one exists. Otherwise it is added once and used by every stream test here.
- [ ] **Reveals intent:** schedule names say when the writer runs.
- [ ] **Complexity went down:** no production change.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `setupTestServer` and the stream query follow `TestAttentionStreamAcceptsBoundaryCursor` (`server_test.go:2677`).

#### Success Criteria

**Automated:** `go test -short -run TestAttentionStream_ResumeAcrossHandoverMissesNothing -count=10 ./internal/serve/` passes 10 times, with red-at-seam recorded.

### Behavior 6: No subscribers, no polling; the first subscriber starts it at 1 second [LEAF: one unit, fake ticker and a counting store]

#### Test Specification

**Given** a store-backed feed with no subscribers. **When** time passes and other processes write. **Then** no poll ticker is made and the store isn't read. **When** the first handler subscribes. **Then** one ticker is made with a 1 second interval, and a tick reads the store. **When** the last handler unsubscribes. **Then** ticks read nothing. A later subscriber starts no second ticker, and it gets only events written after it subscribed.

**Edge Cases:** two subscribers, then one leaves: polling goes on.

**Property:** no property: a fixed sequence of subscribe and unsubscribe steps.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go` (`countingReadStore`, which wraps `*state.Store` and counts `GetAttentionEventsSince` and `GetAttentionReplayWindow` calls).

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_PollsOnlyWithSubscribers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store := &countingReadStore{Store: openStoreFile(t, path)}
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker)
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)

	writer.Append(testAttentionEvent("before anyone listens"))
	if ticker.made.Load() != 0 || store.reads() != 0 {
		t.Fatalf("polled with no subscribers: tickers %d, reads %d", ticker.made.Load(), store.reads())
	}

	sink := collect(feed)
	unsubscribe := feed.Subscribe(sink.handler) // second handle to check the count below
	if got := time.Duration(ticker.interval.Load()); got != AttentionPollInterval || AttentionPollInterval != time.Second {
		t.Fatalf("interval = %s", got)
	}
	writer.Append(testAttentionEvent("after"))
	ticker.tick()
	if got := sink.waitFor(t, 2, 2*time.Second); got[0].Summary != "after" {
		t.Fatalf("got %+v, want only the event written after subscribing", got)
	}

	unsubscribe()
	sink.unsubscribe()
	feed.waitForPollPass(t)
	before := store.reads()
	ticker.tick()
	feed.waitForPollPass(t)
	if store.reads() != before {
		t.Fatal("read the store with no subscribers")
	}
	collect(feed)
	if ticker.made.Load() != 1 {
		t.Fatalf("tickers made = %d, want 1", ticker.made.Load())
	}
}
```

(The sink's handler and the second subscription each get the event, so the first wait asks for 2.)

Expected failure: compile error until Behavior 3's types exist, then `polled with no subscribers` if the poll starts in `NewAttentionFeed`.

##### 🟢 Green

Covered by Behavior 3's `Subscribe` and `pollOnce`'s subscriber check. The start point is reset whenever the count goes from 0 to 1, with `pollGen` bumped so a pass already running doesn't write back an old position.

##### 🔵 Refactor

- [ ] **No duplication:** the subscriber check is `subscriberCount()`, as `heartbeatLoop` uses.
- [ ] **Reveals intent:** `resetPollFromLocked` says the new subscriber starts at "now".
- [ ] **Complexity went down:** one goroutine for the feed's life, not one per subscriber.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestAttentionFeed_HeartbeatLoopSkipsWithoutSubscribers` (`attention_feed_test.go:433`).

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_PollsOnlyWithSubscribers ./internal/robot/` fails first, then passes.

### Behavior 7: A feed that never gets a subscriber never polls [LEAF: one unit; Go exits whatever goroutines run, so only the poll's start can be tested]

#### Test Specification

**Given** a feed built the way `GetAttentionFeed` builds it, with a store opener. **When** a short command's work runs on it: `Append`, `AppendAddressed`, `Replay`, `ReplayForRecipient`, `Stats`. **Then** no poll ticker is made, and `Stop` returns within 100 milliseconds. As a support check, `ntm --robot-events` and `ntm agent-hook stop` helper processes on an existing `state.db` exit 0 within 2 seconds.

**Edge Cases:** the opener fails (a memory-only feed). Still no ticker.

**Property:** no property: a fixed call list.

**Files touched:** `internal/robot/attention_feed_test.go`, `internal/cli/attention_closure_test.go` (the support check).

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_ShortCommandNeverPolls(t *testing.T) {
	for _, openerFails := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "state.db")
		ticker := newFakePollTicker()
		feed := NewAttentionFeed(DefaultAttentionFeedConfig(),
			WithAttentionStoreOpener(testOpener(t, path, openerFails)),
			WithAttentionPollTicker(ticker.factory()))

		feed.Append(testAttentionEvent("short"))
		feed.PublishMailPending("/proj", "RedStone", "BlueLake", "hi", 7, "")
		_, _, _ = feed.Replay(0, 10)
		_, _, _ = feed.ReplayForRecipient("BlueLake", 0, 10)
		_ = feed.Stats()

		if ticker.made.Load() != 0 {
			t.Fatalf("opener fails %v: poll started without a subscriber", openerFails)
		}
		assertReturnsWithin(t, 100*time.Millisecond, feed.Stop)
	}
}
```

Expected failure: compile error until Behavior 3's option exists. Red-at-seam: start the poll in `NewAttentionFeed` when a store is configured. Expected: `poll started without a subscriber`.

##### 🟢 Green

No new code: the poll starts only in `Subscribe`.

##### 🔵 Refactor

- [ ] **No duplication:** `testOpener` is shared with any Loop 1 opener tests that already build one.
- [ ] **Reveals intent:** the test lists a short command's calls.
- [ ] **Complexity went down:** nothing added.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the support check follows `TestClosure_VersionDoesNotOpenStore` (`attention_closure_test.go:691`).

#### Success Criteria

**Automated:** `go test -short -run 'TestAttentionFeed_ShortCommandNeverPolls' ./internal/robot/` and `go test -short -run TestClosure_ShortCommandsExitPromptly ./internal/cli/` pass, with red-at-seam recorded for the first.

### Behavior 8: Stop ends the poll and leaves no goroutine [LEAF: one unit]

#### Test Specification

**Given** a feed whose poll is running for one subscriber. **When** `Stop` is called. **Then** it returns within 1 second, the poll ticker's `Stop` was called once, and `runtime.NumGoroutine()` falls back to its count from before the feed was built, within 1 second. A `Subscribe` after `Stop` makes no ticker. Calling `Stop` twice is safe.

**Edge Cases:** `Stop` while a pass is reading the store: the pass finishes, then the loop sees `stopCh`.

**Property:** no property: a fixed lifecycle.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_StopEndsPoll(t *testing.T) {
	baseline := runtime.NumGoroutine()
	ticker := newFakePollTicker()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))),
		WithAttentionPollTicker(ticker.factory()))
	collect(feed)

	assertReturnsWithin(t, time.Second, feed.Stop)
	feed.Stop()
	if ticker.stopped.Load() != 1 {
		t.Fatalf("ticker stopped %d times, want 1", ticker.stopped.Load())
	}
	waitForGoroutines(t, baseline, time.Second)
	collect(feed)
	if ticker.made.Load() != 1 {
		t.Fatal("Subscribe after Stop started a poll")
	}
}
```

Expected failure: compile error until Behavior 3's option exists. Red-at-seam: leave `stopCh` out of `pollLoop`'s `select`. Expected: `Stop` doesn't return within 1 second.

##### 🟢 Green

`Stop` sets `pollStopped` under `pollMu` before closing `stopCh`, and `startPollLocked` returns when it is set, so `stopWg.Add` never runs after `Wait` starts. `pollLoop` stops its ticker with `defer`.

##### 🔵 Refactor

- [ ] **No duplication:** shutdown is `stopCh` and `stopWg`, shared with `heartbeatLoop`.
- [ ] **Reveals intent:** `pollStopped` has a one-line comment saying why it exists.
- [ ] **Complexity went down:** one flag, no second channel.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `Stop` (`:2634`).

#### Success Criteria

**Automated:** `go test -short -race -run TestAttentionFeed_StopEndsPoll ./internal/robot/` fails first, then passes.

### Behavior 9: A failed read is logged once and retried from the last delivered cursor [LEAF: one unit, a store that fails on demand]

#### Test Specification

**Given** a subscribed feed whose store fails `GetAttentionEventsSince`. **When** another process writes an event and the ticker ticks twice. **Then** nothing is delivered, and one warning, `attention feed poll failed`, is logged with the error and the cursor. **When** the store recovers and the ticker ticks. **Then** the event is delivered once, and one info line, `attention feed poll recovered`, is logged.

**Edge Cases:** the window read fails instead. `replayFromStore` then falls back to the journal, which holds this process's events, so the process's own events are still delivered.

**Property:** no property: a fixed failure sequence.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go` (`faultInjectingStore` gains a `failReads` flag).

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_PollReadFailureRetries(t *testing.T) {
	logs := captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, store, ticker)
	sink := collect(feed)
	writer := newWriterFeed(t, path)

	store.failReads.Store(true)
	writer.Append(testAttentionEvent("written during the failure"))
	ticker.tick()
	feed.waitForPollPass(t)
	ticker.tick()
	feed.waitForPollPass(t)
	if got := sink.drain(); len(got) != 0 {
		t.Fatalf("delivered during failure: %+v", got)
	}
	if n := logs.count("attention feed poll failed"); n != 1 {
		t.Fatalf("failure logged %d times, want 1", n)
	}

	store.failReads.Store(false)
	ticker.tick()
	if got := sink.waitFor(t, 1, 2*time.Second); got[0].Summary != "written during the failure" {
		t.Fatalf("got %+v", got)
	}
	if logs.count("attention feed poll recovered") != 1 {
		t.Fatal("recovery not logged")
	}
}
```

Expected failure: no log line, once the poll exists without failure handling.

##### 🟢 Green

```go
const (
	attentionPollFailedLogMessage    = "attention feed poll failed"
	attentionPollRecoveredLogMessage = "attention feed poll recovered"
)

// pollFailing is only touched by the poll goroutine.
if err != nil {
	if !f.pollFailing {
		slog.Warn(attentionPollFailedLogMessage, "since_cursor", from, "error", err)
		f.pollFailing = true
	}
	return
}
if f.pollFailing {
	slog.Info(attentionPollRecoveredLogMessage, "since_cursor", from)
	f.pollFailing = false
}
```

The position doesn't move on failure, so the next pass reads from the same cursor.

##### 🔵 Refactor

- [ ] **No duplication:** log messages are constants, as `attentionStoreDownLogMessage` is (`:504`).
- [ ] **Reveals intent:** the failure branch says the position stays.
- [ ] **Complexity went down:** one flag, set and cleared in one place.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** Loop 1's store-down and recovered log pair.

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_PollReadFailureRetries ./internal/robot/` fails first, then passes.

### Behavior 10: An expired poll cursor continues from the oldest event and is reported [LEAF: one unit, a real store after cleanup]

#### Test Specification

**Given** a subscribed feed with a 1 hour retention whose poll last delivered cursor 1. **When** another process writes 3 events dated 2 hours ago, the store's cleanup removes them, and it writes 2 current events (cursors 5 and 6). **Then** a tick delivers 5 and 6, in order, and one warning, `attention feed poll cursor expired`, is logged with `since_cursor` 1 and `oldest_cursor` 5.

**Edge Cases:** every event expired and none are left: the poll waits at the newest cursor and logs nothing more.

**Property:** no property: a fixed timeline.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_PollContinuesAfterExpiredCursor(t *testing.T) {
	logs := captureSlog(t)
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	cfg := AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour}
	writer := NewAttentionFeed(cfg, WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	writer.Append(testAttentionEvent("start"))

	feed := NewAttentionFeed(cfg, WithAttentionStore(openStoreFile(t, path)), WithAttentionPollTicker(ticker.factory()))
	t.Cleanup(feed.Stop)
	sink := collect(feed)

	for i := 0; i < 3; i++ {
		appendAged(writer, fmt.Sprintf("old %d", i), 2*time.Hour)
	}
	gcStore(t, path)
	writer.Append(testAttentionEvent("new 1"))
	writer.Append(testAttentionEvent("new 2"))

	ticker.tick()
	got := sink.waitFor(t, 2, 2*time.Second)
	if got[0].Cursor != 5 || got[1].Cursor != 6 {
		t.Fatalf("got %+v", got)
	}
	if !logs.has("attention feed poll cursor expired", "since_cursor", int64(1), "oldest_cursor", int64(5)) {
		t.Fatalf("expiry not reported: %s", logs)
	}
}
```

Expected failure: nothing delivered, because `replayFromStore` returns `CursorExpiredError` and `pollOnce` treats it as a read failure.

##### 🟢 Green

```go
var expired *CursorExpiredError
if errors.As(err, &expired) {
	slog.Warn("attention feed poll cursor expired",
		"since_cursor", from, "oldest_cursor", expired.EarliestCursor)
	f.setPollFrom(expired.EarliestCursor-1, gen)
	continue // read again from the oldest event, in the same pass
}
```

##### 🔵 Refactor

- [ ] **No duplication:** expiry is decided by `replayFromStore`, as for `Replay`.
- [ ] **Reveals intent:** the log names both cursors, which is the "report" the scope asks for.
- [ ] **Complexity went down:** one branch before the general failure branch.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `CursorExpiredError` (`replayFromStore`, `:4358`).

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_PollContinuesAfterExpiredCursor ./internal/robot/` fails first, then passes.

### Behavior 11: A slow handler doesn't lose events [LEAF: one unit]

#### Test Specification

**Given** a subscribed feed whose handler blocks on the first event until the test releases it. **When** another process writes 5 more events while it blocks, and the ticker ticks several times. **Then** after release, the handler gets all 6, in order, once each.

**Edge Cases:** more events than one batch (600, with a batch of 500): all arrive, in order.

**Property:** no property: covered by the order property in Behavior 4.

**Files touched:** `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_SlowHandlerLosesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	writer := newWriterFeed(t, path)
	release := make(chan struct{})
	var once sync.Once
	got := make(chan AttentionEvent, 700)
	feed.Subscribe(func(e AttentionEvent) {
		once.Do(func() { <-release })
		got <- e
	})

	writer.Append(testAttentionEvent("first"))
	ticker.tick()
	for i := 0; i < 5; i++ {
		writer.Append(testAttentionEvent(fmt.Sprintf("while blocked %d", i)))
		ticker.tryTick() // doesn't block when the loop is busy
	}
	close(release)
	ticker.tick()
	assertIncreasingCount(t, got, 6, 2*time.Second)
}
```

A second case writes 600 events before one tick and expects 600, in order.

Expected failure: the 600-event case gets 500 if `pollOnce` doesn't read again after a full batch.

##### 🟢 Green

`pollOnce` loops while the last read returned `attentionPollBatch` events. The ticker channel drops ticks while the loop is busy (as `time.Ticker` does), and the wake channel holds at most one signal, so nothing piles up.

##### 🔵 Refactor

- [ ] **No duplication:** one read loop.
- [ ] **Reveals intent:** the loop condition reads "a full batch means there may be more".
- [ ] **Complexity went down:** no buffer of its own; the store is the buffer.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** synchronous handlers, as `notifySubscribers` documents (`:2652`).

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_SlowHandlerLosesNothing ./internal/robot/` fails first (600 case), then passes.

### Behavior 12: During a store write outage, this process's events still arrive in replay order, and once only after recovery [LEAF: one unit, a store that fails on demand]

#### Test Specification

**Given** a subscribed store-backed feed whose store writes fail. **When** it appends 2 plain events and publishes mail notices for messages 42 and 43, and another process writes 1 event to the store. **Then** the handler gets all 5 in the order `Replay(from, …)` returns them. **When** writes recover, the retry interval passes, and messages 42 and 43 are published again (which saves them with new store cursors). **Then** the handler gets nothing more for 42 and 43.

**Edge Cases:** a memory-only event that was never delivered before recovery (no tick in between) is delivered once, from its stored copy.

**Property:** no property: a fixed outage timeline. `TestAppendAddressed_OutageThenRecovery` checks the same "once per message" count through the poll.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_OutageEventsDeliveredOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	ticker := newFakePollTicker()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(store), WithAttentionClock(clock.Now), WithAttentionPollTicker(ticker.factory()))
	t.Cleanup(feed.Stop)
	sink := collect(feed)
	writer := newWriterFeed(t, path)

	store.fail.Store(true)
	feed.Append(testAttentionEvent("held 1"))
	feed.PublishMailPending("/proj", "S", "RedStone", "m42", 42, "")
	writer.Append(testAttentionEvent("other process"))
	feed.Append(testAttentionEvent("held 2"))
	feed.PublishMailPending("/proj", "S", "RedStone", "m43", 43, "")
	ticker.tick()
	got := sink.waitFor(t, 5, 2*time.Second)
	want, _, _ := feed.Replay(0, 10)
	if !sameCursors(got, want) {
		t.Fatalf("delivered %v, replay order %v", cursorsOf(got), cursorsOf(want))
	}

	store.fail.Store(false)
	clock.Advance(AttentionStoreRetryInterval)
	feed.PublishMailPending("/proj", "S", "RedStone", "m42", 42, "")
	feed.PublishMailPending("/proj", "S", "RedStone", "m43", 43, "")
	ticker.tick()
	feed.waitForPollPass(t)
	if extra := sink.drain(); len(extra) != 0 {
		t.Fatalf("redelivered after recovery: %+v", extra)
	}
}
```

Expected failure: `redelivered after recovery`, with the stored copies of 42 and 43.

##### 🟢 Green

`pollOnce` remembers the addressed key (project, recipient, message id) of each memory-only event it delivers, in a map only the poll goroutine touches. A stored event with a remembered key is skipped once, and the key is dropped:

```go
// deliveredFromMemory holds the addressed keys of memory-only events the poll
// delivered. Their stored copies, saved after the store recovers, aren't
// delivered again. Only the poll goroutine reads or writes it.
deliveredFromMemory map[attentionAddressedKey]struct{}
```

`pollOnce` tells memory-only events apart by reading `memoryOnlySince(from)` itself and merging with `mergeByCursor`, instead of taking the merged list from `replayFromStore`. To keep one merge, `replayFromStore` is split: `storeEventsSince` (window check, expiry, read, hydrate) and the merge, which `Replay` and `pollOnce` both call.

##### 🔵 Refactor

- [ ] **No duplication:** `attentionAddressedKey` is built by the same code as `sameAddressedKey` (`:899`), which becomes a comparison of two keys.
- [ ] **Reveals intent:** the map's comment says what it prevents.
- [ ] **Complexity went down:** the split leaves `replayFromStore` shorter, and `Replay` reads as "store events, plus memory events, merged".
- [ ] **No shallow wrappers:** `storeEventsSince` does the window, expiry and hydration, not a pass-through.
- [ ] **Fits existing patterns:** `mergeByCursor` and `memoryOnlySince` (`:701`, `:716`).

#### Success Criteria

**Automated:** `go test -short -run 'TestAttentionFeed_OutageEventsDeliveredOnce|TestAppendAddressed_OutageThenRecovery' ./internal/robot/` fails first, then passes.

### Behavior 13: A heartbeat doesn't take a cursor a stored event will use [LEAF: one unit]

#### Test Specification

**Given** a store-backed feed with one subscriber, whose store's newest cursor is 3. **When** it publishes a heartbeat, and then another process writes an event. **Then** the heartbeat carries cursor 3, `Stats().NewestCursor` is still 3, the stored event gets cursor 4, and the subscriber gets the stored event. A feed with no store keeps today's heartbeat cursors.

**Edge Cases:** an SSE stream opened right after the heartbeat: its replay boundary is 3, so cursor 4 arrives live (Behavior 5 covers the stream).

**Property:** no property: a fixed sequence.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_HeartbeatKeepsStoredCursorFree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	ticker := newFakePollTicker()
	feed := newPollTestFeed(t, openStoreFile(t, path), ticker)
	writer := newWriterFeed(t, path)
	for i := 0; i < 3; i++ {
		writer.Append(testAttentionEvent("seed"))
	}
	sink := collect(feed)

	hb := feed.PublishEphemeral(AttentionEvent{Type: EventType(DefaultTransportLiveness.HeartbeatType)})
	if hb.Cursor != 3 || feed.Stats().NewestCursor != 3 {
		t.Fatalf("heartbeat cursor %d, newest %d, want 3 and 3", hb.Cursor, feed.Stats().NewestCursor)
	}
	stored := writer.Append(testAttentionEvent("after heartbeat"))
	ticker.tick()
	if got := sink.waitForType(t, EventTypeSystemHealthChange, 2*time.Second); got.Cursor != stored.Cursor {
		t.Fatalf("got %+v, want cursor %d", got, stored.Cursor)
	}
}
```

Expected failure: `heartbeat cursor 4, newest 4`.

##### 🟢 Green

```go
// A store-backed feed's cursors come from the store, so a live-only event
// carries the newest cursor instead of taking the next one.
if f.storeConfigured() {
	event.Cursor = f.cursor.Current()
} else {
	event.Cursor = f.cursor.Next()
}
```

Heartbeats keep direct delivery in both kinds of feed: they're live-only, and both stream paths skip them.

##### 🔵 Refactor

- [ ] **No duplication:** the cursor choice is one `if`.
- [ ] **Reveals intent:** the comment says why.
- [ ] **Complexity went down:** no change to `storeBackedStats`.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestAttentionFeed_HeartbeatLoopSkipsWithoutSubscribers` stays green.

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_HeartbeatKeepsStoredCursorFree ./internal/robot/` fails first, then passes. The existing heartbeat tests (`attention_feed_test.go:433` to `:520`) stay green.

### Behavior 14: A feed with no store still delivers at once [LEAF: regression check on existing tests]

#### Test Specification

**Given** a feed with no store and no opener. **When** it appends an event. **Then** the handler has it before `Append` returns, and no poll ticker is made.

**Edge Cases:** the existing follow-on and panic tests (`attention_feed_test.go:660`, `:688`, `:726`) stay green unchanged.

**Property:** no property: covered by the existing tests.

**Files touched:** `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_MemoryFeedDeliversDirectly(t *testing.T) {
	ticker := newFakePollTicker()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 10, RetentionPeriod: time.Hour},
		WithAttentionPollTicker(ticker.factory()))
	t.Cleanup(feed.Stop)
	var got atomic.Int32
	feed.Subscribe(func(AttentionEvent) { got.Add(1) })
	feed.Append(testAttentionEvent("direct"))
	if got.Load() != 1 || ticker.made.Load() != 0 {
		t.Fatalf("delivered %d, tickers %d", got.Load(), ticker.made.Load())
	}
}
```

Expected failure: compile error until the option exists. Red-at-seam: send every feed through the poll in `publishLocked`. Expected: `delivered 0`.

##### 🟢 Green

Covered by `publishLocked`'s `storeConfigured()` branch in Behavior 3.

##### 🔵 Refactor

- [ ] **No duplication:** none added.
- [ ] **Reveals intent:** the test name says the memory feed's contract.
- [ ] **Complexity went down:** n/a.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TestAttentionFeed_Subscribe` (`attention_feed_test.go:370`).

#### Success Criteria

**Automated:** `go test -short -run 'TestAttentionFeed_MemoryFeedDeliversDirectly|TestAttentionFeed_Subscribe' ./internal/robot/` passes, with red-at-seam recorded.

### Sprint verification

```bash
export PATH=$PATH:/usr/local/go/bin
go build ./cmd/ntm
go test -short -race ./internal/robot/ ./internal/serve/
go test -short ./internal/cli/
go test -short -run TestClosure_ServeStreamsParallelWritersInOrder -count=20 ./internal/cli/
go test -short ./...           # compare failures with the known list in bd-5zbcm
gofmt -l .
go vet ./internal/robot/ ./internal/serve/ ./internal/cli/
ubs internal/robot/attention_feed.go internal/serve/server.go
bun ~/.claude/SAI/Tools/TestTamperCheck.ts --base <sprint start commit>
```

`golangci-lint` and `goimports` are reported as not run if they're still missing.

### Build record (2026-11-08)

Base commit `47cbe7b7`. Red, then green, for each behavior:
- **1:** red was `no matching event within 5s`, with the server up and the stream connected. **Red-at-seam:** with `Subscribe` not starting the poll, Behaviors 1, 2 and 4 all fail with `no matching event`.
- **2:** written after the poll existed, so its red is the seam check above. It passed alone, but in the full `go test -short ./...` run it failed with "unexpected event": the duplicate described in Key Discoveries. `TestWSAttentionSubscriptionNoHubDuplicate` reproduces it every time ("sent 2 times, want 1"). **Fix differs from the plan:** leaving attention topics out of the client's hub topics could break a client that subscribes to `*`, so `WSHub.broadcastEvent` skips attention topics only for a client with an active attention subscription (`WSClient.hasActiveAttentionSub`). The hub holds `clientsMu` and then takes the client's `attentionSubMu`. No path takes them the other way round. The serve closure tests then passed 5 times out of 5, and the WebSocket tests passed under `-race`.
- **3:** red was the compile error `undefined: AttentionPollTicker`. **Red-at-seam:** with direct delivery kept beside the poll, the handler got each event twice. Also added `TestAttentionFeed_StoreBackedFollowOnKeepsOrder` (a handler's follow-on append arrives after the event that caused it) and the property `TestAttentionFeed_OwnAppendProperty`.
- **4:** the robot property `TestAttentionFeed_ParallelWritersDeliveredInOrder` passed 200 checks: SQLite commits cursors in order. **Red-at-seam:** moving the poll to the newest cursor after each pass skipped events (`delivered [1 … 9 15 16 …]`). The closure test passed too, but was written after the poll existed, so its red is the seam check under Behavior 1.
- **5:** **Red-at-seam:** with the start point read on the poll's first pass, the "during" and "after" cases got only `[6 … 10]` or `[6 … 11]`. The first try didn't go red: the stream's own feed had written the 10 seed events, which left its poll a pending wake-up, so the first pass ran at once. The writer now seeds them.
- **6:** red was the compile error, then `ticker interval = 0s`, because the ticker was made inside the goroutine. It's now made before the goroutine starts.
- **7:** passes by design. **Red-at-seam:** starting the poll in `NewAttentionFeed` failed with "the poll started without a subscriber".
- **8:** the first run failed on the goroutine count because the baseline was taken before the SQLite connection opened its own goroutine. The test was fixed. **Red-at-seam:** with `stopCh` left out of the poll loop, `Stop` didn't return within 1s.
- **9:** red was "failure logged 0 times, want 1".
- **10:** red was "got 0 events" once the test's start event was also dated in the past. The replay window starts at the oldest event that hasn't expired, so a fresh start event kept cursor 1 in the window.
- **11:** red was "got 507 events, want 606" (one batch, no second read).
- **12:** red was "redelivered after recovery" with the stored copy of message 42.
- **13:** red was "heartbeat cursor 4, newest 4".
- **14:** **Red-at-seam:** sending every feed through the poll gave "delivered 0".

Where the build differs from the plan:
- **A bug the plan missed, fixed with a test.** During a store outage, a memory-only event takes the next cursor from this process's counter. Once the poll has delivered other processes' higher cursors, that counter is behind, so the event got a cursor below the poll's position and was never delivered. `TestAttentionFeed_OutageEventAfterOtherWritersDelivered` was red with "held cursor = 1". The poll now raises the counter to the highest cursor it reads (`CursorAllocator.raiseTo`).
- **The cleanup call stays in `replayFromStore`.** It moved into `storeEventsSince`, which takes a `collectGarbage` flag. Replays pass true, and the poll, which doesn't call `replayFromStore`, passes false. So `Replay` and `ReplayForRecipient` didn't change.
- **A failed window read is a failed poll read.** It is logged and retried. It doesn't fall back to the journal as `Replay` does, because the journal and the memory-only list overlap, and merging both would deliver events twice.
- **One poll goroutine for the feed's life.** It starts with the first subscriber and idles, reading nothing, while there are none, as `heartbeatLoop` does. A new first subscriber moves the start point to now and bumps `pollGen`, so a pass already running can't write back an old position.
- **The ticker is made in `startPollFromNow`**, before the goroutine starts, so it exists when `Subscribe` returns.
- **`sameAddressedKey` is unchanged.** The poll has its own `attentionAddressedKey` with a lower-case agent name.
- **Test helpers:** `fakePollTicker.pass` ticks twice. The loop takes the second tick only after the first pass ends, so no `waitForPollPass` hook was needed in production code.
- **Three existing tests drive the poll by hand:** `TestAppendAddressed_RepeatDoesNothing`, `TestAppendAddressed_ManyGoroutines` and `TestAppendAddressed_OutageThenRecovery`. The plan named only the last two. `RepeatDoesNothing` uses a store file, which the plan's search missed. The assertions are unchanged, and the tamper check is clean.
- **Behavior 5 drives `handleAttentionStreamV1` directly** through `httptest.NewServer`, not through the router.

Verification on the VPS:
- `go build ./cmd/ntm` passes. `gofmt -l` is clean for `internal/robot`, `internal/serve` and `internal/cli`. Three files elsewhere, last changed in `b7003e0e`, are listed by `gofmt -l` and were left alone. `go vet` passes for the three packages.
- `go test -short -race ./internal/robot/` passes. `./internal/serve/` with `-race` times out at Go's default 10 minutes. With a 25 minute limit it passes in 751 s, while the base commit takes 1,272 s, so the timeout is the suite's length under `-race`, not a hang.
- `TestClosure_ServeStreamsParallelWritersInOrder` passed 20 times out of 20.
- `go test -short ./...` fails only on the known list in bd-5zbcm, after the hub fix.
- The tamper check against `47cbe7b7` is clean.
- `ubs`, `golangci-lint`, `goimports` and `rch` aren't installed, so those checks weren't run.

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop4 ./cmd/ntm`.
2. In a scratch project folder, create a session: `/tmp/ntm-loop4 spawn streamtest --cc=1`.
3. Start the server: `/tmp/ntm-loop4 serve --port 8080`.
4. In a second shell, watch the stream: `curl -N http://127.0.0.1:8080/api/v1/attention/stream | grep --line-buffered mail.received`.
5. In a third shell, send mail: `/tmp/ntm-loop4 mail send streamtest --to <agent> "stream test"`.
6. Open `/tmp/ntm-loop4 dashboard streamtest`, and wait for `Mail: 1 unread` in the bottom ticker.
7. Pass: within a few seconds, the second shell shows one `mail.received` event naming your message.
8. Quit the dashboard and wait a minute.
9. Pass: the same event hasn't appeared again.

## Integration & E2E Testing

- **Integration:** Behaviors 3 to 14 in `internal/robot`, with real SQLite files and two connections standing in for two processes. Behavior 5 in `internal/serve` over real HTTP.
- **Closure:** Behaviors 1, 2 and 4 in `internal/cli`, with `ntm serve` and writers as separate helper processes.
- **E2E:** none new. Loop 9's release journey runs the serve stream by hand.

## References

- Loop document: [04-cross-process-delivery.md](04-cross-process-delivery.md)
- Scope: [SCOPE.md](SCOPE.md), process **Cross-Process Delivery**
- Research: `thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`, Chains B and C
- Patterns: `heartbeatLoop` (`internal/robot/attention_feed.go:2676`), `prepareAttentionStream` (`internal/serve/server.go:4838`), `TestClosure_ManyWritersOneStateDB` (`internal/cli/attention_closure_test.go:659`)
