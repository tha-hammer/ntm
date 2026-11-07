# Loop 1: Durable Feed TDD Implementation Plan

Loop document: [01-durable-feed.md](01-durable-feed.md). Scope: [SCOPE.md](SCOPE.md). Index: [00-master-index.md](00-master-index.md).

**Beads:** epic `bd-bdjf4`, with `bd-bdjf4.1` (Sprint 1), `bd-bdjf4.2` (Sprint 2) and `bd-bdjf4.3` (Sprint 3). Each sprint blocks the next.

## Overview

After this loop, every ntm process uses `state.db` the first time it uses the attention feed, events last 7 days, and a failed store write no longer leaves a process in memory for the rest of its life. There are 21 testable behaviors across 3 sprints: 10 BLOCKING and 11 LEAF.

## Current State Analysis

The default feed is in memory (`internal/robot/attention_feed.go:2956`). Only `--robot-*` commands (`internal/cli/root.go:2517`) and `ntm serve` (`internal/cli/serve.go:99`) install a store-backed feed. So the dashboard's mail events (`internal/tui/dashboard/dashboard.go:2919`) stay in its memory.

### Key Discoveries

- `AttentionStore` (`internal/robot/attention_feed.go:27`) has two real implementers: `state.Store`, and `stubAttentionStore` in tests (`attention_feed_test.go:2588`). Adding a method means changing only these.
- One failed write sets `ephemeralFromCursor` forever (`attention_feed.go:537-552`). Replay and stats read it at `:3926` and `:4067`.
- `Append` holds `appendMu` during the store write, and SQLite's busy timeout is 5 seconds (`internal/state/store.go:64`). The dashboard calls `Append` inside its update loop, so a retry can stall the screen.
- `Replay` reads `f.store` without a lock (`attention_feed.go:944-950`). Attaching a store after construction needs a safe accessor.
- `AgentMailInboxSummaryMsg` is exported, and a zero `Gen` is accepted (`internal/tui/dashboard/model.go:825`). Dashboard tests already drive it (`dashboard_layout_test.go:2560`), so the closure trigger needs no new seam.
- `canSkipConfigLoading` returns early from `PersistentPreRunE` for commands such as `version` (`internal/cli/root.go:112`), so opener registration must come before it.
- `internal/cli/testmain_test.go:39` doesn't isolate the config folder. Any CLI test reaching the new opener would otherwise touch the real `state.db`.
- Nothing in `internal/tui/dashboard` uses `slog` today. The default handler writes to stderr, which would garble the alternate screen, so the dashboard needs its logs sent to a file.
- `GCExpiredEvents` compares text: `expires_at < datetime('now')` (`internal/state/runtime_store.go:2110`). Stored values look like `2026-11-06 20:03:16.918199136+00:00`.
- `attention_contract.go:42` and `:1159` state the old 1 hour value.

## Desired End State

A person can run a dashboard, quit it, and read its mail events from another process. Those events last 7 days. If the store fails, a running dashboard shows a warning, and later events are saved again once the store returns.

### Observable Behaviors

Listed by sprint below, in Given / When / Then form.

## What We're NOT Doing

- Addressed events, recipients or duplicate checks (Loop 2).
- Delivering other processes' events to subscribers (Loop 4).
- Writing memory-only events to the store after recovery. They stay readable by that process until it exits.
- Changing `--robot-*` or `ntm serve` startup when `state.db` can't open. **Decision:** they keep exiting with their existing error, because their projection refresh needs the store. This is an approved exception to the scope's "doesn't exit" wording.
- The full `docs/attention-feed-contract.md` update (Loop 9).

**Accepted limit:** during an outage, a process replaying its own memory-only events could step past another process's durable event with a lower cursor. Other processes read only the store, so they aren't affected.

## Testing Strategy

- **Framework:** Go standard `testing`.
- **Database:** real SQLite files in `t.TempDir()` for closure and integration tests. `stubAttentionStore` for unit tests that need scripted failures.
- **Closure tests:** re-exec the test binary as helper processes through ntm's real startup, inside `go test -short`. They never call `t.Skip`. A helper that fails, or produces no output to assert on, fails the test.
- **Property tests:** `pgregory.net/rapid` v1.3.0, a new test-only dependency.
- **Mocking:** only fault injection around the real store, for failures that can't be produced quickly. The Agent Mail server is outside the map, so it can be faked.

**Environment on this VPS:**
- Go 1.26.3 is at `/usr/local/go/bin/go`, and isn't on the default PATH, so run `export PATH=$PATH:/usr/local/go/bin` first.
- `golangci-lint`, `goimports` and `rch` aren't installed here. If they're still missing at build time, report those checks as not run, and don't count them as passed.

## Workflow Closure

Anchors come from the research's Chain B and Chain C (`thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`).

- **Research correction:** Chain B ended inside one dashboard process. This loop adds the connector that makes it cross processes.
- **`highest_new_connector`:** the store opener registered at ntm startup, which `GetAttentionFeed` then uses.
- **Trigger choice:** `fetchAgentMailInboxes` is unchanged, so the trigger moves inward to the handler for its result, which stays above the new connector.

### Production Operation Chain

`dashboard startup -> registerAttentionStoreOpener -> AgentMailInboxSummaryMsg handler -> GetAttentionFeed (opens state.db) -> PublishMailPending -> AttentionFeed.Append -> Store.AppendAttentionEvent -> state.db -> (new process) --robot-events -> AttentionFeed.Replay -> JSON output`

**Production callers:**

| New code | Production caller |
|---|---|
| `SetAttentionStoreOpener` | `rootCmd.PersistentPreRunE` through `registerAttentionStoreOpener` |
| `WithAttentionStoreOpener` | `GetAttentionFeed` |
| `Store.ReserveAttentionCursor` | `AttentionFeed.Append`, on recovery |
| `AttentionFeed.StoreHealth` | `Model.updateTickerData` |
| `TickerData.FeedWarning` | `Model.updateTickerData` |

### Closure Test: A dashboard's mail event can be read from another process after it exits [BLOCKING]

Behavior 1.
- **SOURCE (seed only)**: `AgentMailInboxSummaryMsg` contents, standing in for the Agent Mail inbox.
- **TRIGGER (start)**: `dashboard.Model.Update(AgentMailInboxSummaryMsg)` : `internal/tui/dashboard/dashboard.go:2884`, after real `rootCmd.PersistentPreRunE`. (boundary = highest_new_connector)
- **DRIVERS (async edges)**: none. The process exit is the edge, and the parent test waits for it.
- **OBSERVE (assert via)**: `ntm --robot-events --events-category=mail` in a new process, through `Execute` : `internal/cli/root.go:2455`, reaching `AttentionFeed.Replay` : `attention_feed.go:944`.
- **FORBIDDEN SPAN**: `GetAttentionFeed`, `PublishMailPending`, `Append`, `Store.AppendAttentionEvent`, any raw `attention_events` read or write.
- **RED-AT-SEAM proof**: remove `registerAttentionStoreOpener()` from `PersistentPreRunE`. Expected: no `mail.received` in the reader's output.
- **DRIVABILITY**: the store path comes from `XDG_CONFIG_HOME`. No clock is needed, because the span has no async edge.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. Needs only a temp folder and the test binary. Fails if a helper exits non-zero or its output has no events array.

### Closure Test: A command that doesn't use the feed doesn't open the store [BLOCKING]

Behavior 5.
- **SOURCE (seed only)**: an empty config folder.
- **TRIGGER (start)**: `Execute` with `ntm version` : `internal/cli/root.go:2455`. (boundary = outermost)
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `state.db` doesn't exist at `state.DefaultPath()`. The file's absence is the promise.
- **FORBIDDEN SPAN**: `state.Open`, the opener, `GetAttentionFeed`.
- **RED-AT-SEAM proof**: make `registerAttentionStoreOpener` open the store at once. Expected: `state.db` exists.
- **DRIVABILITY**: config path from the environment.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. The test also checks that the helper printed a version, so it can't pass by doing nothing.

### Closure Test: A dashboard's durable feed doesn't call Agent Mail through the projection refresh [BLOCKING]

Behavior 7.
- **SOURCE (seed only)**: `AgentMailInboxSummaryMsg` contents.
- **TRIGGER (start)**: the same as Behavior 1. (boundary = highest_new_connector)
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: request count on a fake Agent Mail server at `AGENT_MAIL_URL`, which is the system boundary, plus the event being readable through `--robot-events`.
- **FORBIDDEN SPAN**: `RefreshNormalizedProjection`, `WorkCoordinationAdapter.Collect`.
- **RED-AT-SEAM proof**: make the opener call `initializeRobotPersistence`. Expected: the server receives `tools/call` requests.
- **DRIVABILITY**: `AGENT_MAIL_URL` is read from the environment (`internal/agentmail/client.go`).
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/` with `httptest.Server`. Fails if the event isn't readable, so a zero count can't pass on its own.

### Closure Test: An event 2 hours old is still returned to a new reader [BLOCKING]

Behavior 10.
- **SOURCE (seed only)**: an `AttentionEvent` with `Ts` 2 hours ago.
- **TRIGGER (start)**: `NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(store)).Append` : `attention_feed.go:524`. (boundary = highest_new_connector, which is the default config)
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `Replay` on a second feed, with a second connection to the same file : `attention_feed.go:944`.
- **FORBIDDEN SPAN**: `attentionEventToStored`, `Store.AppendAttentionEvent`, `GCExpiredEvents`, raw SQL.
- **RED-AT-SEAM proof**: leave retention at 1 hour. Expected: the event is gone.
- **DRIVABILITY**: `state.Open` takes a path. Expiry comes from the event's `Ts`, so no clock is needed.
- **EXECUTION (must run)**: runs in `go test -short ./internal/robot/` on a temp file.

### Closure Test: A cursor over an hour old doesn't return CURSOR_EXPIRED [BLOCKING]

Behavior 11.
- **SOURCE (seed only)**: two events dated 2 hours ago.
- **TRIGGER (start)**: a helper process appending through real startup and `GetAttentionFeed`. (boundary = highest_new_connector)
- **DRIVERS (async edges)**: none.
- **OBSERVE (assert via)**: `ntm --robot-events --since-cursor=<first cursor>` in a new process.
- **FORBIDDEN SPAN**: `Replay`, `GetAttentionReplayWindow`, raw SQL.
- **RED-AT-SEAM proof**: leave retention at 1 hour. Expected: the second event isn't returned.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`.

### Closure Test: An event past 7 days is removed, and a stale cursor gets CURSOR_EXPIRED [BLOCKING]

Behavior 12.
- **SOURCE (seed only)**: events dated 8 days ago and 6 days ago, then a new one.
- **TRIGGER (start)**: `AttentionFeed.Append` on a default feed.
- **DRIVERS (async edges)**: none. The existing cleanup runs inside `Replay`.
- **OBSERVE (assert via)**: `Replay` on a second feed, then `Replay` from the stale cursor.
- **FORBIDDEN SPAN**: `GCExpiredEvents`, raw SQL.
- **RED-AT-SEAM proof**: set retention to 30 days. Expected: the 8 day event is returned.
- **DRIVABILITY**: expiry comes from `Ts`.
- **EXECUTION (must run)**: runs in `go test -short ./internal/robot/`.

### Closure Test: Many processes can write to one state.db [BLOCKING]

Behavior 14. This is also the Sprint 3 evidence check.
- **SOURCE (seed only)**: none.
- **TRIGGER (start)**: 8 helper processes, each appending 50 events through real startup and `GetAttentionFeed`. (boundary = highest_new_connector)
- **DRIVERS (async edges)**: none. The parent waits for the helpers.
- **OBSERVE (assert via)**: `--robot-events` in a new process, paged to the end.
- **FORBIDDEN SPAN**: `Store.AppendAttentionEvent`, raw SQL.
- **RED-AT-SEAM proof**: remove `registerAttentionStoreOpener`. Expected: no events.
- **DRIVABILITY**: same as Behavior 1.
- **EXECUTION (must run)**: runs in `go test -short ./internal/cli/`. Record the counts and timing in the sprint notes.

### Closure Test: After a failed store write, later events are durable and readable elsewhere [BLOCKING]

Behavior 15.
- **SOURCE (seed only)**: a real SQLite lock held by a separate connection, then released.
- **TRIGGER (start)**: `AttentionFeed.Append` : `attention_feed.go:524`.
- **DRIVERS (async edges)**: the retry time, through `WithAttentionClock`. A test clock moves forward 30 seconds.
- **OBSERVE (assert via)**: `Replay` on a second feed with a second connection.
- **FORBIDDEN SPAN**: `ReserveAttentionCursor`, the recovery internals, raw reads.
- **RED-AT-SEAM proof**: restore the one-way switch, so a failed write never retries. Expected: the later event is missing from the second feed.
- **DRIVABILITY**: **Finding: the feed has no clock seam today.** This plan adds `WithAttentionClock`.
- **EXECUTION (must run)**: runs in `go test -short ./internal/robot/`. Takes about 5 seconds, waiting out the real busy timeout. It doesn't skip in short mode.

### Closure Test: Later store cursors sort above memory-only ones [BLOCKING]

Behavior 19.
- **SOURCE (seed only)**: memory-only events, produced by fault injection around a real store.
- **TRIGGER (start)**: `AttentionFeed.Append`, during and after the failure.
- **DRIVERS (async edges)**: `WithAttentionClock`.
- **OBSERVE (assert via)**: `Replay` on the same feed.
- **FORBIDDEN SPAN**: `sqlite_sequence`, raw SQL.
- **RED-AT-SEAM proof**: remove the `ReserveAttentionCursor` call. Expected: a durable event repeats a memory-only cursor.
- **DRIVABILITY**: fault injection embeds the real `*state.Store` and fails only the chosen append calls.
- **EXECUTION (must run)**: runs in `go test -short ./internal/robot/`.

### Closure Test: A running dashboard shows a warning while its feed is memory-only, and clears it after recovery [BLOCKING]

Behavior 21.
- **SOURCE (seed only)**: fault injection on the store.
- **TRIGGER (start)**: `AttentionFeed.Append` on the feed installed with `SetAttentionFeed`.
- **DRIVERS (async edges)**: `Model.Update(DashboardTickMsg)` : `internal/tui/dashboard/dashboard.go:2249`, and `WithAttentionClock`.
- **OBSERVE (assert via)**: `Model.View()`.
- **FORBIDDEN SPAN**: `StoreHealth`, `updateTickerData`, `TickerPanel.SetData`.
- **RED-AT-SEAM proof**: remove the health check from `updateTickerData`. Expected: no warning in the view.
- **DRIVABILITY**: dashboard tests already use `SetAttentionFeed`, with `lockAttentionFeedForTest`.
- **EXECUTION (must run)**: runs in `go test -short ./internal/tui/dashboard/`.

---

## Sprint 1: Durable feed everywhere

**Bead:** `bd-bdjf4.1`.

**Testable function:** "Run a dashboard until it shows new mail, quit it, then see the `mail.received` event from another process with `ntm --robot-events --events-category=mail`."

**Order of work:** write Behavior 1 first and watch it fail. It stays red until the last step. Then 3, 2, 4, 6, 8, 9, 5, 7.

**Build notes (2026-11-06):**

- **Bug found and fixed, not in the original plan:** `Store.GetAttentionReplayWindow` failed on a real file as soon as a row existed, because `MAX(ts)` comes back as text and can't scan into `time.Time`. `replayFromStore` then fell back to the in-memory journal without saying so. So no process could read another's events from `state.db`, including `--robot-*` and `ntm serve`. This contradicts the research's follow-up section, which traced the write side and assumed the read side worked. Regression test: `TestGetAttentionReplayWindow_WithRowsOnRealFile`.
- **Plan change:** `initializeRobotPersistence` no longer has its "already initialized" early return, because the store is now shared with the opener. It has one caller, which runs once per process.

**Red-at-seam record:**

| Behavior | Connector disabled | Red result | Green once restored |
|---|---|---|---|
| 1 | `registerAttentionStoreOpener` call removed | `mail.received not readable from another process` | Yes |
| 5 | Opener registration made to open the store at once | `state.db exists after ntm version` | Yes |
| 7 | Opener registration made to run `initializeRobotPersistence` | `dashboard path made 3 Agent Mail calls, want 0` | Yes |

All three ran through helper processes in `go test -short`, with no skips.

**Verification record:**

- Race detector: clean on `internal/robot`, `internal/state` and `internal/tui/dashboard`, and on this sprint's `internal/cli` tests.
- Test tamper check against 70fe5915: clean.
- `go vet` and `gofmt`: clean on the changed packages.
- Real `state.db`: modification time unchanged across all test runs.
- Full `go test -short ./...`: 28,800 passed, 13 failed, 226 skipped. All the failures also occur on an untouched copy of 70fe5915, or are shown to depend on live tmux sessions. They're filed as a bug bead.
- Not run, because they aren't installed here: `golangci-lint`, `goimports`, `ubs`.

**Human test, first run by the agent at the user's request: NOT passed.**

- Step 7 returned `{"success":true,"events":[]}`, because no mail event was ever published.
- The dashboard showed `Mail: offline` and no `✉` badge after 70 seconds.
- Proven cause on the mail side: `ntm mail inbox` fails because ntm sends `registration_token` to `fetch_inbox`, and Agent Mail 0.3.0 at 1d5c82a rejects it as an unexpected argument. Not yet proven: that this is the only reason the dashboard shows offline.
- Script gaps: the project folder must exist first, or `ntm spawn` prompts to create it. On this VPS it also needs its own `.ntm/config.toml`, because `~/.beads` makes the project lookup resolve to `/home/debian` (bead `bd-3rja0`).

**Fix added at the user's choice, not in the original plan:** `agentmail.Client.callTool` retries once without `registration_token` when the server rejects that argument, and remembers the rejection for that client. Tests: `TestFetchInbox_RetriesWithoutTokenWhenServerRejectsIt`, `TestCallTool_OtherValidationErrorsAreNotRetried`.

**Second cause, from the agent's own test runs:** `go test` started the machine's tmux server, so new sessions inherited test settings, including a dead `AGENT_MAIL_URL` and a temporary `XDG_CONFIG_HOME`. That explains `Mail: offline`.

**Human test, final run by the agent:** dashboard started with those three settings removed. It showed `Mail: 1 unread`. After it quit, a separate `--robot-events` process returned "New mail from HumanOverseer: durable feed test", and the event is in the real `state.db` as cursor 6. The per-pane `✉` badge wasn't seen in the captured screen.

**Human test, local run by the user on 7f9f22a1: not passed as written.** The event was saved, but step 6 returned no `mail.received`.

- **Bug fixed:** `BuildEventsOutput` applied `--events-limit` before the filters, so the limit counted events examined rather than matches, and `has_more` only looked at what was left after filtering. Matching events beyond the first page were silently missed. It now scans until it has enough matches or reaches the end, up to 50,000 events per request, and past that reports `has_more` with a cursor to resume from. Tests: `TestBuildEventsOutput_FilterFindsEventsBeyondLimit`, `TestBuildEventsOutput_NextCursorPassesNonMatchingTail`, `TestBuildEventsOutput_FilteredPagingReturnsEveryMatchOnce`, `TestBuildEventsOutput_NoFilterKeepsLimitBehavior`, `TestReplayMatchingEvents_StopsAtScanLimit`.
- **Script error, not a code bug:** the `✉` badge exists only in `renderPaneGrid`, used under 120 columns. Wider layouts use `renderPaneList`, which has no badge, so the script now checks the ticker.

**Human test, local rerun by the user on 5d2e2d9a, machine rocketman:** the event was written at cursor 560412, and the dashboard showed `Mail: 1 unread` within 2 seconds. Following `next_cursor` from a separate process reached it on page 3, and paging was correct. Step 6 as written printed nothing, because a replay starts from the oldest event and 207 mail events were in the window. The script now notes the cursor before sending. This changes the script, not the code, and the user decides whether the sprint passes.

**Sprint 1 passed, by the user's decision, on that run's paging evidence.** The rewritten step 6 hasn't been run.

**Event volume, from that run:** 465 cursors in 57 minutes, about 490 an hour, or roughly 82,000 retained over 7 days at that rate. Of 207 mail events, 199 were `mail.unread`, repeating the same "9 unread" summary (bead filed).

**`ntm kill` and the monitor:** the user's report was right. The pattern uses `(?:...)`, which `pgrep` and `pkill` reject as a regex error, and `ntm` ignores that error. So the kill never reaches the monitor, which exits by itself about 25 seconds after the session dies. An earlier note here blamed differing program names, which was wrong: that VPS check waited 40 seconds, so it didn't show the kill working. Not fixed yet, because that exit path also does cleanup `runKill` doesn't. Bead `bd-ndisv`.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 1 |
| A dashboard's `mail.received` event is in `state.db` after the dashboard exits | 1, 2, 9 |
| A separate `ntm --robot-events` process returns that event | 1 |
| A command that never uses the feed, such as `ntm version`, doesn't open `state.db` | 5 |
| `go test -short ./internal/robot/... ./internal/tui/dashboard/... ./internal/serve/...` never touches the real `state.db` | 3, 6 |
| `--robot-overlay` still skips persistence, and the projection refresh still runs only for `--robot-*` and `ntm serve` | 7, 8 |
| Test scenario: open failure returns an in-memory feed and nothing panics | 4 |

### Behavior 1: Dashboard mail is readable from another process [BLOCKING]

#### Test Specification

**Given** a process started through ntm's real startup with an isolated config folder.
**When** the dashboard model handles a mail summary and the process exits.
**Then** `--robot-events --events-category=mail` in another process returns a `mail.received` event naming the subject.

**Edge Cases:** the reader must not depend on any memory shared with the writer, so they're separate processes.

**Property:** no property: a fixed journey with one fixed input, and nothing to generate over.

**Files touched:** `internal/cli/attention_closure_test.go` (new), `internal/cli/testmain_test.go`, `internal/cli/root.go`, `internal/robot/attention_feed.go`. The new test file is needed because helper processes are a new kind of test setup in this package.

#### TDD Cycle

##### 🔴 Red

**File:** `internal/cli/attention_closure_test.go`

```go
func TestClosure_DashboardMailReadableFromAnotherProcess(t *testing.T) {
	env := newClosureEnv(t) // temp XDG_CONFIG_HOME, closed AGENT_MAIL_URL, temp cwd
	env.run(t, "publish-dashboard-mail")

	out := env.run(t, "execute", "--robot-events", "--events-category=mail")
	events := parseRobotEvents(t, out)
	for _, e := range events {
		if e.Type == "mail.received" && strings.Contains(e.Summary, "durable feed test") {
			return
		}
	}
	t.Fatalf("mail.received not readable from another process; output: %s", out)
}
```

The helper entry goes at the top of `TestMain`, before anything else:

```go
if mode := os.Getenv("NTM_CLOSURE_HELPER"); mode != "" {
	os.Exit(runClosureHelper(mode))
}
```

The publishing helper uses real startup and the real model:

```go
func closureHelperPublishDashboardMail() int {
	os.Args = []string{"ntm", "dashboard"}
	if err := rootCmd.PersistentPreRunE(newDashboardCmd(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeRobotPersistence()
	m := dashboard.New("closure", os.Getenv("NTM_CLOSURE_PROJECT"))
	_, _ = m.Update(dashboard.AgentMailInboxSummaryMsg{
		ProjectKey: os.Getenv("NTM_CLOSURE_PROJECT"),
		Inboxes:    map[string][]agentmail.InboxMessage{"%1": {{ID: 42, From: "RedStone", Subject: "durable feed test"}}},
		AgentMap:   map[string]string{"%1": "BlueLake"},
	})
	return 0
}
```

Expected failure: no `mail.received` event in the reader's output.

##### 🟢 Green

**File:** `internal/cli/root.go`

```go
// Top of PersistentPreRunE, before canSkipConfigLoading returns.
registerAttentionStoreOpener(os.Args[1:])
```

```go
func registerAttentionStoreOpener(args []string) {
	for _, arg := range args {
		if robotFlagSkipsPersistence(arg) {
			return
		}
	}
	robot.SetAttentionStoreOpener(func() (robot.AttentionStore, error) {
		return openProcessStateStore()
	})
}

// openProcessStateStore opens and migrates state.db once per process.
func openProcessStateStore() (*state.Store, error) {
	robotStateStoreMu.Lock()
	defer robotStateStoreMu.Unlock()
	if robotStateStore != nil {
		return robotStateStore, nil
	}
	store, err := state.Open("")
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	if err := store.Migrate(); err != nil {
		store.Close()
		return nil, fmt.Errorf("migrate state store: %w", err)
	}
	robotStateStore = store
	return store, nil
}
```

`initializeRobotPersistence` calls `openProcessStateStore` instead of opening its own, keeping its existing exit on error, `SetProjectionStore` and refresh.

**File:** `internal/robot/attention_feed.go`: see Behaviors 2 to 4.

##### 🔵 Refactor

- [ ] **No duplication:** `initializeRobotPersistence` and the opener share `openProcessStateStore`. Check whether `runServe` can use it too, and leave it if its store lifetime differs.
- [ ] **Reveals intent:** `registerAttentionStoreOpener` says what it does.
- [ ] **Complexity went down:** `initializeRobotPersistence` loses its open and migrate branches.
- [ ] **No shallow wrappers:** the opener closure stays one line, and adds no new type in `cli`.
- [ ] **Fits existing patterns:** follows `SetAttentionFeed` and `SetProjectionStore`.

#### Success Criteria

**Automated:**
- [ ] Red: `go test -short -run TestClosure_DashboardMailReadableFromAnotherProcess ./internal/cli/` fails, naming the missing event.
- [ ] Red-at-seam recorded, then green.
- [ ] `go test -short ./internal/cli/ ./internal/robot/` passes after refactor.

**Manual:** covered by the human test script.

### Behavior 2: Store opens once on first use [LEAF: robot package only, synchronous, no registration crossed]

#### Test Specification

**Given** a registered opener. **When** `GetAttentionFeed` is called many times at once. **Then** the opener runs once, and all callers get one feed.

**Edge Cases:** `PeekAttentionFeed` still returns nil before first use, and `SetAttentionFeed` still replaces the feed.

**Property:** no property: concurrency count with no input domain.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestGetAttentionFeed_OpensStoreOnce(t *testing.T) {
	resetGlobalFeedForTest(t)
	var calls atomic.Int32
	SetAttentionStoreOpener(func() (AttentionStore, error) {
		calls.Add(1)
		return newStubAttentionStore(), nil
	})
	var wg sync.WaitGroup
	feeds := make([]*AttentionFeed, 50)
	for i := range feeds {
		wg.Add(1)
		go func(i int) { defer wg.Done(); feeds[i] = GetAttentionFeed() }(i)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("opener ran %d times, want 1", calls.Load())
	}
	for _, f := range feeds {
		if f != feeds[0] {
			t.Fatal("callers got different feeds")
		}
	}
}
```

##### 🟢 Green

```go
var globalFeedOpener func() (AttentionStore, error)

func SetAttentionStoreOpener(open func() (AttentionStore, error)) {
	globalFeedMu.Lock()
	defer globalFeedMu.Unlock()
	globalFeedOpener = open
}

func GetAttentionFeed() *AttentionFeed {
	globalFeedMu.Lock()
	defer globalFeedMu.Unlock()
	if globalFeed == nil {
		opts := []AttentionFeedOption{}
		if globalFeedOpener != nil {
			opts = append(opts, WithAttentionStoreOpener(globalFeedOpener))
		}
		globalFeed = NewAttentionFeed(DefaultAttentionFeedConfig(), opts...)
	}
	return globalFeed
}
```

##### 🔵 Refactor

- [ ] **No duplication:** the feed construction stays in one place.
- [ ] **Reveals intent:** the doc comment says tests leave the opener unset.
- [ ] **Complexity went down:** no new branches beyond the opener check.
- [ ] **No shallow wrappers:** `SetAttentionStoreOpener` guards shared state.
- [ ] **Fits existing patterns:** matches `SetAttentionFeed`.

#### Success Criteria

**Automated:** `go test -short -race -run TestGetAttentionFeed_OpensStoreOnce ./internal/robot/` fails first, then passes.

### Behavior 3: No opener stays in memory [LEAF: robot package only, synchronous]

#### Test Specification

**Given** no opener. **When** the feed is first used. **Then** it's in memory, `StoreHealth().Configured` is false, and no file is created.

**Edge Cases:** this is the state every `internal/robot`, `dashboard` and `serve` test relies on.

**Property:** no property: a single fixed state.

**Files touched:** `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestGetAttentionFeed_NoOpenerStaysInMemory(t *testing.T) {
	resetGlobalFeedForTest(t)
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	feed := GetAttentionFeed()
	if feed.StoreHealth().Configured {
		t.Fatal("feed reports a store with no opener")
	}
	feed.Append(testAttentionEvent("memory only"))
	if _, err := os.Stat(filepath.Join(cfgDir, "ntm", "state.db")); !os.IsNotExist(err) {
		t.Fatalf("state.db was created: %v", err)
	}
}
```

Fails first because `StoreHealth` doesn't exist.

##### 🟢 Green

Add `StoreHealth` with a `Configured` field, set when a store or opener is present. Behavior 4 fills in the rest.

##### 🔵 Refactor

- [ ] **No duplication:** `testAttentionEvent` reuses or replaces the repeated event literals in nearby tests.
- [ ] **Reveals intent:** the test name states the promise.
- [ ] **Complexity went down:** no production branch added beyond the field.
- [ ] **No shallow wrappers:** none added.
- [ ] **Fits existing patterns:** matches `JournalStats`.

#### Success Criteria

**Automated:** `go test -short -run TestGetAttentionFeed_NoOpenerStaysInMemory ./internal/robot/` fails first, then passes.

### Behavior 4: Open failure doesn't crash [LEAF: robot package only, synchronous]

#### Test Specification

**Given** an opener that returns an error. **When** the feed is first used. **Then** an in-memory feed is returned, events can be appended and replayed, and `StoreHealth` shows `Configured` true, `Durable` false and the error.

**Edge Cases:** a nil store with a nil error is treated as a failure. Retrying is Behavior 20.

**Property:** no property: a fixed failure.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestGetAttentionFeed_OpenFailureFallsBackToMemory(t *testing.T) {
	resetGlobalFeedForTest(t)
	SetAttentionStoreOpener(func() (AttentionStore, error) { return nil, errors.New("disk full") })
	feed := GetAttentionFeed()
	got := feed.Append(testAttentionEvent("still works"))
	events, _, err := feed.Replay(got.Cursor-1, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("replay = %d events, err %v", len(events), err)
	}
	h := feed.StoreHealth()
	if !h.Configured || h.Durable || !strings.Contains(h.LastError, "disk full") {
		t.Fatalf("health = %+v", h)
	}
}
```

##### 🟢 Green

```go
type AttentionStoreHealth struct {
	Configured bool
	Durable    bool
	LastError  string
	Since      time.Time
}

func WithAttentionStoreOpener(open func() (AttentionStore, error)) AttentionFeedOption {
	return func(f *AttentionFeed) {
		f.storeOpener = open
		store, err := open()
		if err != nil || store == nil {
			f.recordStoreFailureLocked(openFailure(err))
			return
		}
		f.store = store
	}
}
```

##### 🔵 Refactor

- [ ] **No duplication:** failure recording is one function, reused in Sprint 3.
- [ ] **Reveals intent:** `StoreHealth` reads plainly at call sites.
- [ ] **Complexity went down:** callers don't check for a nil store.
- [ ] **No shallow wrappers:** `StoreHealth` returns a copy taken under a lock.
- [ ] **Fits existing patterns:** matches `AttentionFeedOption`.

#### Success Criteria

**Automated:** `go test -short -run TestGetAttentionFeed_OpenFailureFallsBackToMemory ./internal/robot/` fails first, then passes.

### Behavior 5: Unrelated commands don't open the store [BLOCKING]

#### Test Specification

**Given** an empty config folder. **When** `ntm version` runs through `Execute`. **Then** the helper prints a version, and no `state.db` exists.

**Edge Cases:** `version` returns early from `PersistentPreRunE`, so this also proves registration before that return is harmless.

**Property:** no property: a fixed command.

**Files touched:** `internal/cli/attention_closure_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_VersionDoesNotOpenStore(t *testing.T) {
	env := newClosureEnv(t)
	out := env.run(t, "execute", "version")
	if strings.TrimSpace(out) == "" {
		t.Fatal("version printed nothing; the helper may not have run")
	}
	if _, err := os.Stat(env.stateDB()); !os.IsNotExist(err) {
		t.Fatalf("state.db exists after ntm version: %v", err)
	}
}
```

This should pass as soon as the helper exists, because Behavior 1's design is lazy. So prove it with the seam: temporarily make the opener open at once, see it fail, and restore it. Record that in the sprint notes.

##### 🟢 Green

No extra code beyond Behavior 1. If it fails, find what calls `GetAttentionFeed` or `state.Open` on that path, and move that call.

##### 🔵 Refactor

- [ ] **No duplication:** shares `newClosureEnv`.
- [ ] **Reveals intent:** `env.stateDB()` hides the path rule.
- [ ] **Complexity went down:** no production change.
- [ ] **No shallow wrappers:** `newClosureEnv` holds the env list and temp folders.
- [ ] **Fits existing patterns:** standard `testing`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_VersionDoesNotOpenStore ./internal/cli/`, with red-at-seam recorded.

### Behavior 6: CLI tests stay isolated [LEAF: test setup only, no production path]

#### Test Specification

**Given** the CLI test binary. **When** tests start. **Then** `state.DefaultPath()` is under a temp folder, and that folder is removed afterward.

**Edge Cases:** helper processes get their own folder. `t.Setenv` in existing tests still works. `NTM_CONFIG` is cleared.

**Property:** no property: a fixed setup.

**Files touched:** `internal/cli/testmain_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestMain_IsolatesStateDB(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(state.DefaultPath(), filepath.Join(home, ".config")) {
		t.Fatalf("tests would use the real state.db: %s", state.DefaultPath())
	}
}
```

##### 🟢 Green

```go
cfgDir, err := os.MkdirTemp("", "ntm-cli-test-config-")
if err != nil {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
os.Setenv("XDG_CONFIG_HOME", cfgDir)
os.Unsetenv("NTM_CONFIG")
code := m.Run()
os.RemoveAll(cfgDir)
```

Existing CLI tests may read the real user config by accident. Fix any that break by giving them the config they need.

##### 🔵 Refactor

- [ ] **No duplication:** one setup point.
- [ ] **Reveals intent:** a comment explains why it's needed.
- [ ] **Complexity went down:** tests can't reach real state.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the dashboard package's `TestMain` sets its environment the same way.

#### Success Criteria

**Automated:**
- [ ] `go test -short ./internal/cli/` passes.
- [ ] The modification time of `~/.config/ntm/state.db` is unchanged after the sprint's full test run.

### Behavior 7: Dashboard runs no projection refresh [BLOCKING]

#### Test Specification

**Given** a counting fake Agent Mail server. **When** the dashboard startup path publishes an event. **Then** the event is readable, and the server received no `tools/call` requests from that process.

**Edge Cases:** the reader is `--robot-events`, which does run a refresh by design, so count only during the publish helper.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/attention_closure_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_DashboardFeedRunsNoProjectionRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	env := newClosureEnv(t)
	env.set("AGENT_MAIL_URL", srv.URL+"/mcp/")
	env.run(t, "publish-dashboard-mail")
	if got := calls.Load(); got != 0 {
		t.Fatalf("dashboard path made %d Agent Mail calls", got)
	}
	out := env.run(t, "execute", "--robot-events", "--events-category=mail")
	if len(parseRobotEvents(t, out)) == 0 {
		t.Fatal("no event, so a zero count proves nothing")
	}
}
```

##### 🟢 Green

No extra code: the opener doesn't call `RefreshNormalizedProjection`. Prove it with the seam in the closure spec.

##### 🔵 Refactor

- [ ] **No duplication:** shares the helpers.
- [ ] **Reveals intent:** the second assertion's message explains why it exists.
- [ ] **Complexity went down:** no production change.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `httptest` is used across the repo.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_DashboardFeedRunsNoProjectionRefresh ./internal/cli/`, with red-at-seam recorded.

### Behavior 8: `--robot-overlay` still skips the store [LEAF: pure argument check in cli]

#### Test Specification

**Given** arguments. **When** startup decides on the opener. **Then** `--robot-overlay` and `--robot-overlay=x` register no opener, and other arguments do.

**Edge Cases:** `--robot-overlay` among other flags.

**Property:** no property: a small, fixed table of flags.

**Files touched:** `internal/cli/root.go`, `internal/cli/cli_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionStoreOpenerWanted(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"dashboard"}, true},
		{[]string{"--robot-status"}, true},
		{[]string{"--robot-overlay"}, false},
		{[]string{"--robot-overlay=x", "--json"}, false},
	} {
		if got := attentionStoreOpenerWanted(tc.args); got != tc.want {
			t.Errorf("%v: got %v, want %v", tc.args, got, tc.want)
		}
	}
}
```

##### 🟢 Green

Extract `attentionStoreOpenerWanted(args)` from `registerAttentionStoreOpener`, using the existing `robotFlagSkipsPersistence`.

##### 🔵 Refactor

- [ ] **No duplication:** reuses `robotFlagSkipsPersistence`.
- [ ] **Reveals intent:** the name states the question.
- [ ] **Complexity went down:** the loop lives in one function.
- [ ] **No shallow wrappers:** it has logic of its own.
- [ ] **Fits existing patterns:** next to `shouldInitializeRobotPersistence`.

#### Success Criteria

**Automated:** `go test -short -run TestAttentionStoreOpenerWanted ./internal/cli/` fails first, then passes.

### Behavior 9: Store closes on exit [LEAF: cli package only, synchronous]

#### Test Specification

**Given** a store opened on first use. **When** `closeRobotPersistence` runs, as `Execute` defers. **Then** it's closed, and a later open gets a working store.

**Edge Cases:** closing with nothing open does nothing, and the store's WAL contents are readable by the next process.

**Property:** no property: a fixed lifecycle.

**Files touched:** `internal/cli/root.go`, `internal/cli/attention_closure_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestProcessStateStore_ClosesAndReopens(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, err := openProcessStateStore()
	if err != nil {
		t.Fatal(err)
	}
	closeRobotPersistence()
	if err := first.DB().Ping(); err == nil {
		t.Fatal("store still open after close")
	}
	second, err := openProcessStateStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeRobotPersistence)
	if second == first {
		t.Fatal("got the closed store back")
	}
}
```

If `Store` has no `DB()` accessor, assert with a failing query method instead.

##### 🟢 Green

`closeRobotPersistence` takes `robotStateStoreMu`, closes, and clears the variable. `Execute` already defers it (`root.go:2456`).

##### 🔵 Refactor

- [ ] **No duplication:** one close path.
- [ ] **Reveals intent:** consider renaming `robotStateStore` to `processStateStore`, since it's no longer robot-only.
- [ ] **Complexity went down:** no second store variable.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** existing deferred close.

#### Success Criteria

**Automated:** `go test -short -run TestProcessStateStore_ClosesAndReopens ./internal/cli/` passes.

### Human test script

1. Build the binary: `go build -o /tmp/ntm-loop1 ./cmd/ntm`.
2. Make a scratch project folder and create a session: `/tmp/ntm-loop1 spawn feedtest --cc=1`.
3. Find the agent's name: `/tmp/ntm-loop1 --robot-status`.
4. Note the feed's position, because a replay starts from the oldest event: `CUR=$(/tmp/ntm-loop1 --robot-events --events-limit=1 | jq .replay_window.latest_cursor)`.
5. Send it mail: `/tmp/ntm-loop1 mail send feedtest --to <agent> "durable feed test"`.
6. Open `/tmp/ntm-loop1 dashboard feedtest`, and wait up to 60 seconds for `Mail: 1 unread` in the bottom ticker. The per-pane `✉` badge only appears in terminals under 120 columns.
7. Quit the dashboard.
8. In the same shell as step 4, run `/tmp/ntm-loop1 --robot-events --events-category=mail --since-cursor="$CUR" | jq '.events[] | select(.type=="mail.received") | .summary'`.
9. Pass: the summary names your message.

---

## Sprint 2: 7 day retention

**Bead:** `bd-bdjf4.2`.

**Testable function:** "Write an event, wait over an hour, and see `ntm --robot-events` still return it, with an expiry 7 days out in `state.db`."

**Order of work:** 13, then 10, 11, 12. Write 10 first as the outer test, and it goes green with 13.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 10, 11 |
| New events are saved with `expires_at` 7 days after their timestamp | 13 |
| An event over an hour old is still returned by `--robot-events` | 10, 11 |
| A cursor over an hour old doesn't return `CURSOR_EXPIRED` | 11 |
| An event past 7 days is removed by the existing cleanup | 12, 13 |
| The in-memory journal's size limit stays at 10,000 events | 13 |

### Behavior 10: Two-hour-old event survives [BLOCKING]

#### Test Specification

**Given** a default feed on a real store file. **When** an event dated 2 hours ago is appended. **Then** a second feed, on a second connection to the file, returns it from `Replay`.

**Edge Cases:** `Replay`'s cleanup runs before the read, so a wrong expiry removes the event.

**Property:** no property here. The expiry invariant is in Behavior 13.

**Files touched:** `internal/robot/attention_feed_test.go`, `internal/robot/attention_feed.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_TwoHourOldEventStillReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	e := testAttentionEvent("two hours old")
	e.Ts = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	writer.Append(e)

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Summary != "two hours old" {
		t.Fatalf("got %+v, want the old event", events)
	}
}
```

Expected failure: no events, because the 1 hour expiry has passed.

##### 🟢 Green

```go
const attentionEventRetention = 7 * 24 * time.Hour

func DefaultAttentionFeedConfig() AttentionFeedConfig {
	return AttentionFeedConfig{
		JournalSize:       10000,
		RetentionPeriod:   attentionEventRetention,
		HeartbeatInterval: 30 * time.Second,
	}
}
```

Update the comment at `attention_contract.go:42` and the sample at `:1159`.

##### 🔵 Refactor

- [ ] **No duplication:** `openStoreFile` is shared with Behaviors 12, 15 and 19.
- [ ] **Reveals intent:** a named constant replaces the literal.
- [ ] **Complexity went down:** a one-line change.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** matches `newTestAttentionStore`, with a real path.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_TwoHourOldEventStillReplayed ./internal/robot/`, with red-at-seam recorded.

### Behavior 11: Old cursor stays valid [BLOCKING]

#### Test Specification

**Given** two events dated 2 hours ago, written through real startup in a helper process. **When** `--robot-events --since-cursor=<first cursor>` runs in a new process. **Then** it succeeds and returns the second event, with no `CURSOR_EXPIRED`.

**Edge Cases:** the projection refresh in the reader may add health events, so the test looks for the specific event.

**Property:** no property: a fixed journey.

**Files touched:** `internal/cli/attention_closure_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_OldCursorNotExpired(t *testing.T) {
	env := newClosureEnv(t)
	cursors := parseCursors(t, env.run(t, "append-old-events", "2h", "2"))
	out := env.run(t, "execute", "--robot-events", fmt.Sprintf("--since-cursor=%d", cursors[0]))
	if strings.Contains(out, "CURSOR_EXPIRED") {
		t.Fatalf("cursor expired: %s", out)
	}
	for _, e := range parseRobotEvents(t, out) {
		if e.Cursor == cursors[1] {
			return
		}
	}
	t.Fatalf("second event not returned: %s", out)
}
```

##### 🟢 Green

No extra code beyond Behavior 10. Adds an `append-old-events` helper mode that uses real startup and `robot.GetAttentionFeed().Append`.

##### 🔵 Refactor

- [ ] **No duplication:** shares helper setup.
- [ ] **Reveals intent:** the mode name says what it writes.
- [ ] **Complexity went down:** no production change.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** same helper design.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_OldCursorNotExpired ./internal/cli/`, with red-at-seam recorded.

### Behavior 12: Past 7 days is removed [BLOCKING]

#### Test Specification

**Given** events dated 8 days ago and 6 days ago, then a new one. **When** a new reader replays. **Then** the 8 day event is gone, the 6 day event remains, and replaying from the 8 day event's cursor returns `CursorExpiredError`.

**Edge Cases:** the window is read before cleanup runs (`attention_feed.go:3890-3918`), so the test replays once first, then checks the stale cursor.

**Property:** no property: fixed ages either side of the limit.

**Files touched:** `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_PastRetentionRemovedAndCursorExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(writer.Stop)
	eightDays := appendAged(writer, "eight days", 8*24*time.Hour)
	appendAged(writer, "padding", 8*24*time.Hour)
	appendAged(writer, "six days", 6*24*time.Hour)
	appendAged(writer, "new", 0)

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	first, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, first, "six days", "new")

	_, _, err = reader.Replay(eightDays.Cursor, 10)
	var expired *CursorExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("err = %v, want CursorExpiredError", err)
	}
}
```

Writing the padding event is needed because `CursorExpired` is `sinceCursor < OldestCursor-1`.

##### 🟢 Green

No extra code beyond Behavior 10. It fails on 1 hour retention, because "six days" is gone.

##### 🔵 Refactor

- [ ] **No duplication:** `appendAged` is shared.
- [ ] **Reveals intent:** the comment explains the padding event.
- [ ] **Complexity went down:** no production change.
- [ ] **No shallow wrappers:** `appendAged` sets the timestamp and appends.
- [ ] **Fits existing patterns:** matches the existing cursor expiry tests.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_PastRetentionRemovedAndCursorExpires ./internal/robot/`, with red-at-seam recorded.

### Behavior 13: Expiry is computed correctly [LEAF: pure conversion, plus one state package query]

#### Test Specification

**Given** any timestamp. **When** an event is converted for storage with the default config. **Then** `expires_at` is exactly 7 days later.

**Edge Cases:**
- `JournalSize` stays 10,000.
- The cleanup boundary is pinned: an event expiring 2 seconds ago is removed, and one expiring in 2 seconds is kept.
- A row saved earlier with a 1 hour expiry keeps it, since no migration changes old rows.

**Property:** **Invariant** — for all `ts`, `attentionEventToStored(e(ts), cfg.RetentionPeriod).ExpiresAt == ts + 7 days`.

**Domain:** timestamps between 2000 and 2100, at nanosecond precision, formatted as `AttentionEvent.Ts` is in production.

```go
func TestAttentionEventToStored_ExpiryProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		ts := time.Unix(rapid.Int64Range(946684800, 4102444800).Draw(rt, "sec"),
			rapid.Int64Range(0, 999999999).Draw(rt, "nsec")).UTC()
		stored := attentionEventToStored(AttentionEvent{Ts: ts.Format(time.RFC3339Nano)},
			DefaultAttentionFeedConfig().RetentionPeriod)
		if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(ts.Add(7*24*time.Hour)) {
			rt.Fatalf("expiry = %v for ts %v", stored.ExpiresAt, ts)
		}
	})
}
```

**Files touched:** `go.mod`, `go.sum`, `internal/robot/attention_feed_test.go`, `internal/state/state_test.go`.

#### TDD Cycle

##### 🔴 Red

The property above, plus:

```go
func TestDefaultAttentionFeedConfig_JournalSizeUnchanged(t *testing.T) {
	if got := DefaultAttentionFeedConfig().JournalSize; got != 10000 {
		t.Fatalf("JournalSize = %d", got)
	}
}
```

and, in `internal/state/state_test.go`, a table test for `GCExpiredEvents` with expiries 2 seconds ago and 2 seconds ahead. Run `go get pgregory.net/rapid@v1.3.0` first.

##### 🟢 Green

Same change as Behavior 10.

##### 🔵 Refactor

- [ ] **No duplication:** the property uses the production config.
- [ ] **Reveals intent:** the invariant is one line.
- [ ] **Complexity went down:** no production branch.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the first `rapid` use in the repo, kept to test files, so it stays out of the release binary.

#### Success Criteria

**Automated:**
- [ ] `go test -short -run 'ExpiryProperty|JournalSizeUnchanged' ./internal/robot/` passes.
- [ ] `go mod tidy` leaves `go.mod` and `go.sum` stable.

### Human test script

1. Create an event, as in Sprint 1, or by running `/tmp/ntm-loop1 --robot-status` in a project.
2. Run `sqlite3 -readonly ~/.config/ntm/state.db "select cursor, ts, expires_at from attention_events order by cursor desc limit 1"`.
3. Pass so far: `expires_at` is 7 days after `ts`.
4. Note the cursor, and wait over an hour.
5. Run `/tmp/ntm-loop1 --robot-events --since-cursor=<cursor minus 1> | jq '.events[0].cursor, .success'`.
6. Pass: it prints your cursor and `true`.

---

## Sprint 3: No silent drop to memory

**Bead:** `bd-bdjf4.3`.

**Testable function:** "Hold a lock on `state.db` while a dashboard runs, see a warning, release the lock, and see later events from that same dashboard saved."

**Evidence check at sprint start:** Behavior 14 is scope Evidence Needed item 6. If writers fail inside the 5 second busy timeout, stop and adjust the retry approach before building recovery.

**Order of work:** 14, then write 15 as the outer test, then 16, 17, 18, 19, 20, and 15 goes green, then 21.

**Existing test:** `TestAttentionFeed_StoreWriteFailureFallsBackConsistently` (`attention_feed_test.go:1837`) pins today's behavior. With no clock movement, the retry limit means it should keep passing. If a change to it is needed, keep every assertion that still describes the behavior, and explain the change in the commit.

| Acceptance criterion | Behaviors |
|---|---|
| Testable function | 15, 21 |
| A store write failure no longer leaves a process memory-only for life | 15, 16 |
| A later append that succeeds is durable and readable by other processes | 14, 15 |
| Failure and recovery each produce one log line with the error, and a running dashboard shows a visible warning | 17, 21 |
| Cursors stay in increasing order within a process after recovery, and events held in memory stay readable by that process until it exits | 18, 19 |
| A process that can't open the store at all keeps working in memory, reports it, and tries again | 4, 20 |

### Behavior 14: Many writers succeed [BLOCKING]

#### Test Specification

**Given** 8 helper processes on one `state.db`. **When** each appends 50 events through real startup. **Then** all 400 are readable with distinct cursors, or every shortfall is matched by a reported failure.

**Edge Cases:** first-use migration can run in all 8 at once. WAL mode is enabled at open.

**Property:** no property: a fixed load.

**Files touched:** `internal/cli/attention_closure_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_ManyWritersOneStateDB(t *testing.T) {
	env := newClosureEnv(t)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	start := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := env.tryRun("append-events", "50"); err != nil {
				errs <- fmt.Sprintf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("writer failed: %s", e)
	}
	cursors := readAllCursors(t, env, "contention")
	t.Logf("8 writers, %d events, %s", len(cursors), time.Since(start))
	if len(cursors) != 400 || !allDistinct(cursors) {
		t.Fatalf("got %d events, want 400 distinct", len(cursors))
	}
}
```

Each `append-events` helper exits non-zero if its feed's `StoreHealth` isn't durable at the end. That's how a silent drop to memory would show up.

##### 🟢 Green

If it passes as is, record the numbers. If migration races fail, make `Store.Migrate` safe under concurrent first use. If appends exhaust the busy timeout, stop and bring the numbers back, as the evidence check says.

##### 🔵 Refactor

- [ ] **No duplication:** `readAllCursors` is the one paging helper.
- [ ] **Reveals intent:** the log line records the evidence.
- [ ] **Complexity went down:** only what the evidence requires.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** same helper design.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_ManyWritersOneStateDB -v ./internal/cli/`, with the logged counts copied to the sprint notes.

### Behavior 15: Later events are durable after a failure [BLOCKING]

#### Test Specification

**Given** a feed whose write failed while a separate connection held an exclusive lock. **When** the lock ends, the retry time passes, and another event is appended. **Then** a second feed reads the later event, and `StoreHealth` shows `Durable` true.

**Edge Cases:** the memory-only event isn't in the second feed, which matches the approved design.

**Property:** no property here. Sequences of failures are covered by Behavior 18.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_RecoversAfterStoreLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	clock := newTestClock()
	writer := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(openStoreFile(t, path)), WithAttentionClock(clock.Now))
	t.Cleanup(writer.Stop)

	release := holdExclusiveLock(t, path)
	writer.Append(testAttentionEvent("during lock")) // real busy timeout, about 5 seconds
	release()

	clock.Advance(attentionStoreRetryInterval + time.Second)
	writer.Append(testAttentionEvent("after lock"))

	reader := NewAttentionFeed(DefaultAttentionFeedConfig(), WithAttentionStore(openStoreFile(t, path)))
	t.Cleanup(reader.Stop)
	events, _, err := reader.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "after lock")
	if !writer.StoreHealth().Durable {
		t.Fatalf("health = %+v", writer.StoreHealth())
	}
}
```

##### 🟢 Green

Replace `ephemeralFromCursor` with recoverable state, guarded by `appendMu`:

```go
type attentionStoreState struct {
	down      bool
	lastErr   error
	downSince time.Time
	nextRetry time.Time
}

func (f *AttentionFeed) storeAttemptAllowedLocked() bool {
	return f.currentStore() != nil && (!f.storeState.down || !f.now().Before(f.storeState.nextRetry))
}
```

`Append` uses it:

```go
if f.storeAttemptAllowedLocked() {
	cursor, err := f.appendToStoreLocked(&event)
	if err == nil {
		event.Cursor = cursor
		f.cursor.counter.Store(cursor)
		f.markStoreRecoveredLocked()
	} else {
		event.Cursor = f.cursor.Next()
		f.markStoreDownLocked(err)
		f.rememberMemoryOnlyLocked(event)
	}
} else {
	event.Cursor = f.cursor.Next()
	if f.currentStore() != nil {
		f.rememberMemoryOnlyLocked(event)
	}
}
```

`const attentionStoreRetryInterval = 30 * time.Second`. `WithAttentionClock` defaults to `time.Now`.

##### 🔵 Refactor

- [ ] **No duplication:** one code path sets a memory-only cursor. Remove `ephemeralFromCursor` entirely, with no leftover shim.
- [ ] **Reveals intent:** `Append` reads as try, recover or fall back.
- [ ] **Complexity went down:** the replay branch is simpler after Behavior 18.
- [ ] **No shallow wrappers:** `appendToStoreLocked` holds the convert, reserve and insert steps.
- [ ] **Fits existing patterns:** `AttentionFeedOption`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_RecoversAfterStoreLock ./internal/robot/`, with red-at-seam recorded and about 5 seconds' runtime.

### Behavior 16: Retries are limited during an outage [LEAF: feed only, driven by an injected clock]

#### Test Specification

**Given** a store that's down. **When** many events are appended with the clock still. **Then** the store is tried once. When the clock moves 30 seconds, the next append tries once more.

**Edge Cases:** exactly at the interval counts as allowed. A still-failing retry moves `nextRetry` forward.

**Property:** no property: scripted steps on a fixed interval.

**Files touched:** `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_RetryLimitedDuringOutage(t *testing.T) {
	store := newStubAttentionStore()
	store.failAppend.Store(true)
	clock := newTestClock()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)
	for i := 0; i < 100; i++ {
		feed.Append(testAttentionEvent("x"))
	}
	if got := store.appendAttempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	clock.Advance(attentionStoreRetryInterval)
	feed.Append(testAttentionEvent("y"))
	if got := store.appendAttempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}
```

##### 🟢 Green

`markStoreDownLocked` sets `nextRetry = f.now().Add(attentionStoreRetryInterval)`, including on each failed retry.

##### 🔵 Refactor

- [ ] **No duplication:** one place sets `nextRetry`.
- [ ] **Reveals intent:** a comment explains that each try can block for 5 seconds in the dashboard's update loop.
- [ ] **Complexity went down:** one comparison.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** the test reuses the stub.

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_RetryLimitedDuringOutage ./internal/robot/` fails first, then passes.

### Behavior 17: One report per change [LEAF: feed only, synchronous]

#### Test Specification

**Given** a long outage. **When** many appends and failed retries happen, then the store returns. **Then** there's one warning naming the error, and one info line with how many events stayed memory-only.

**Edge Cases:** a second outage produces a second pair.

**Property:** no property: scripted steps.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`, `internal/cli/dashboard.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_OneReportPerStateChange(t *testing.T) {
	logs := captureSlog(t)
	store := newStubAttentionStore()
	clock := newTestClock()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	store.failAppend.Store(true)
	for i := 0; i < 5; i++ {
		clock.Advance(attentionStoreRetryInterval)
		feed.Append(testAttentionEvent("down"))
	}
	store.failAppend.Store(false)
	clock.Advance(attentionStoreRetryInterval)
	feed.Append(testAttentionEvent("back"))

	if got := logs.count("attention feed store unavailable"); got != 1 {
		t.Fatalf("failure reports = %d, want 1", got)
	}
	if got := logs.count("attention feed store recovered"); got != 1 {
		t.Fatalf("recovery reports = %d, want 1", got)
	}
}
```

##### 🟢 Green

`markStoreDownLocked` logs only on the change to down. `markStoreRecoveredLocked` logs only on the change back. Both use `slog` with `err` and `memory_only_count`.

`runDashboard` sends `slog` to a file for the life of the TUI, because stderr would garble the alternate screen: `<resilience.LogDir()>/<session>-dashboard.log`. Check `runDashboard` tests still pass.

##### 🔵 Refactor

- [ ] **No duplication:** logging lives only in the two state changes.
- [ ] **Reveals intent:** message strings are named constants shared with the test.
- [ ] **Complexity went down:** no logging at call sites.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `AGENTS.md` logging rules: structured context with `slog`.

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_OneReportPerStateChange ./internal/robot/` fails first, then passes.

### Behavior 18: Replay stays in order [LEAF: feed only, using the stub store]

#### Test Specification

**Given** any mix of successful and failed writes. **When** the feed replays from 0. **Then** every event appears once, in increasing cursor order.

**Edge Cases:** memory-only events past the journal size are dropped oldest first, and `Stats` counts them. A memory-only event stays readable after recovery.

**Property:** **Invariant** — for every sequence of operations, `Replay(0)` returns strictly increasing cursors, and exactly the events that were appended, in order.

**Domain:** sequences of up to 200 operations: append, turn failure on or off, move the clock forward by 0 to 60 seconds. The stub's cursors come from a sequence that `ReserveAttentionCursor` can raise.

```go
func TestAttentionFeed_ReplayOrderProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		store := newStubAttentionStore()
		clock := newTestClock()
		feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 1000, RetentionPeriod: time.Hour},
			WithAttentionStore(store), WithAttentionClock(clock.Now))
		defer feed.Stop()
		var appended []int64
		steps := rapid.IntRange(1, 200).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "op") {
			case 0:
				appended = append(appended, feed.Append(testAttentionEvent("e")).Cursor)
			case 1:
				store.failAppend.Store(rapid.Bool().Draw(rt, "fail"))
			case 2:
				clock.Advance(time.Duration(rapid.IntRange(0, 60).Draw(rt, "secs")) * time.Second)
			}
		}
		events, _, err := feed.Replay(0, len(appended)+1)
		if err != nil {
			rt.Fatal(err)
		}
		if got := cursorsOf(events); !slices.Equal(got, appended) {
			rt.Fatalf("replay %v, appended %v", got, appended)
		}
		if !slices.IsSorted(appended) || hasDuplicates(appended) {
			rt.Fatalf("cursors not strictly increasing: %v", appended)
		}
	})
}
```

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

The property above. It should fail first on a recovery sequence, because later store cursors repeat memory-only ones.

##### 🟢 Green

Memory-only events live in `f.memoryOnly []AttentionEvent`, bounded by `JournalSize`. `replayFromStore` merges:

```go
stored := f.hydrateStoredAttentionEvents(storedEvents)
merged := mergeByCursor(stored, f.memoryOnlySince(sinceCursor))
if len(merged) > limit {
	merged = merged[:limit]
}
```

`storeBackedStats` counts `memoryOnly` in place of `journal.RangeStats(tailStart)`. Check whether `RangeStats` has other callers, and remove it if it has none.

##### 🔵 Refactor

- [ ] **No duplication:** `mergeByCursor` is the only merge.
- [ ] **Reveals intent:** memory-only events have their own field, rather than a cursor threshold.
- [ ] **Complexity went down:** the tail branch at `:3926` goes away.
- [ ] **No shallow wrappers:** `memoryOnlySince` takes the lock and filters.
- [ ] **Fits existing patterns:** `hydrateReplayEvents` handles memory-only events.

#### Success Criteria

**Automated:** `go test -short -run TestAttentionFeed_ReplayOrderProperty ./internal/robot/` fails first, then passes. Keep any shrunk failing sequence as a named regression test.

### Behavior 19: Later cursors sort above memory-only ones [BLOCKING]

#### Test Specification

**Given** memory-only events with cursors N+1 and N+2. **When** the store recovers. **Then** the next durable event's cursor is above N+2, on a real SQLite file.

**Edge Cases:** an empty table with no `sqlite_sequence` row yet. Raising never lowers it. A reservation failure leaves the feed down.

**Property:** no property here. Ordering across sequences is Behavior 18.

**Files touched:** `internal/state/runtime_store.go`, `internal/state/state_test.go`, `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_RecoveredCursorAboveMemoryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store := &faultInjectingStore{Store: openStoreFile(t, path)}
	clock := newTestClock()
	feed := NewAttentionFeed(DefaultAttentionFeedConfig(),
		WithAttentionStore(store), WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)

	store.fail.Store(true)
	first := feed.Append(testAttentionEvent("memory one"))
	second := feed.Append(testAttentionEvent("memory two"))
	store.fail.Store(false)
	clock.Advance(attentionStoreRetryInterval)
	third := feed.Append(testAttentionEvent("durable"))

	if !(first.Cursor < second.Cursor && second.Cursor < third.Cursor) {
		t.Fatalf("cursors %d, %d, %d", first.Cursor, second.Cursor, third.Cursor)
	}
	events, _, err := feed.Replay(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	assertSummaries(t, events, "memory one", "memory two", "durable")
}
```

##### 🟢 Green

**File:** `internal/state/runtime_store.go`

```go
// ReserveAttentionCursor makes the next attention cursor greater than minCursor.
func (s *Store) ReserveAttentionCursor(minCursor int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return withTx(s.db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = 'attention_events'`, minCursor); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO sqlite_sequence (name, seq)
			SELECT 'attention_events', ? WHERE NOT EXISTS
			(SELECT 1 FROM sqlite_sequence WHERE name = 'attention_events')`, minCursor)
		return err
	})
}
```

Use the transaction helper the state package already has, if there is one. Add the method to `AttentionStore`, and to `stubAttentionStore`. `appendToStoreLocked` calls it before the insert whenever `f.storeState.down` is true.

##### 🔵 Refactor

- [ ] **No duplication:** the reservation is in one place.
- [ ] **Reveals intent:** a comment explains why the sequence is raised, and points to the accepted limit.
- [ ] **Complexity went down:** no extra cursor bookkeeping in the feed.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** matches other `Store` methods' lock and error wrapping.

#### Success Criteria

**Automated:**
- [ ] `go test -short -run TestClosure_RecoveredCursorAboveMemoryOnly ./internal/robot/`, with red-at-seam recorded.
- [ ] A state package test covers the empty-table and never-lowers cases.

### Behavior 20: Failed open is retried [LEAF: feed only, driven by an injected clock]

#### Test Specification

**Given** an opener that fails and later works. **When** the retry time passes and an event is appended. **Then** later events reach the store, and `StoreHealth` shows `Durable` true.

**Edge Cases:** memory-only events from before the open keep their cursors, and the store's cursor is reserved above them. `Replay` running at the same time is safe under `-race`.

**Property:** no property: scripted steps.

**Files touched:** `internal/robot/attention_feed.go`, `internal/robot/attention_feed_test.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestAttentionFeed_FailedOpenIsRetried(t *testing.T) {
	store := newStubAttentionStore()
	var works atomic.Bool
	clock := newTestClock()
	feed := NewAttentionFeed(AttentionFeedConfig{JournalSize: 100, RetentionPeriod: time.Hour},
		WithAttentionClock(clock.Now),
		WithAttentionStoreOpener(func() (AttentionStore, error) {
			if !works.Load() {
				return nil, errors.New("disk full")
			}
			return store, nil
		}))
	t.Cleanup(feed.Stop)
	feed.Append(testAttentionEvent("memory"))
	works.Store(true)
	clock.Advance(attentionStoreRetryInterval)
	feed.Append(testAttentionEvent("durable"))
	if store.appendAttempts.Load() != 1 || !feed.StoreHealth().Durable {
		t.Fatalf("attempts %d, health %+v", store.appendAttempts.Load(), feed.StoreHealth())
	}
}
```

The options must apply in an order where the clock is set before the opener runs. Have `NewAttentionFeed` run the opener after all options, rather than inside the option.

##### 🟢 Green

Replace direct `f.store` reads with `f.currentStore()`, using an `atomic.Pointer` to a small holder, or a `sync.RWMutex`. Retrying an open follows the same `nextRetry` rule. It also calls `ReserveAttentionCursor(f.cursor.Current())`, and `syncCursorFromStore`.

##### 🔵 Refactor

- [ ] **No duplication:** opening and write retries share one timing check.
- [ ] **Reveals intent:** `currentStore()` is the only way to read the store.
- [ ] **Complexity went down:** the option only records the opener.
- [ ] **No shallow wrappers:** `currentStore` exists to make access race-safe.
- [ ] **Fits existing patterns:** the file already uses `sync/atomic`.

#### Success Criteria

**Automated:** `go test -short -race -run TestAttentionFeed_FailedOpenIsRetried ./internal/robot/` fails first, then passes.

### Behavior 21: Dashboard shows the warning [BLOCKING]

#### Test Specification

**Given** a running dashboard model with a store-backed feed. **When** the feed goes memory-only and a tick runs. **Then** the view shows the warning. **When** it recovers and a tick runs. **Then** the warning is gone.

**Edge Cases:**
- A feed with no store configured shows no warning, so tests and memory-only tools aren't noisy.
- A nil result from `PeekAttentionFeed` shows no warning.
- The warning is the first ticker segment, so scrolling doesn't hide it for long.

**Property:** no property: a fixed state change.

**Files touched:** `internal/tui/dashboard/panels/ticker.go`, `internal/tui/dashboard/dashboard.go`, `internal/tui/dashboard/dashboard_layout_test.go`, `internal/robot/attention_feed.go`.

#### TDD Cycle

##### 🔴 Red

```go
func TestClosure_DashboardWarnsWhileFeedIsMemoryOnly(t *testing.T) {
	lockAttentionFeedForTest(t)
	store := &faultInjectingStore{Store: openStoreFile(t, filepath.Join(t.TempDir(), "state.db"))}
	clock := newTestClock()
	feed := robot.NewAttentionFeed(robot.DefaultAttentionFeedConfig(),
		robot.WithAttentionStore(store), robot.WithAttentionClock(clock.Now))
	t.Cleanup(feed.Stop)
	robot.SetAttentionFeed(feed)
	t.Cleanup(func() { robot.SetAttentionFeed(nil) })

	m := sizedDashboardModel(t, 200, 50)
	store.fail.Store(true)
	feed.Append(testEvent("down"))
	m = tick(t, m)
	if !strings.Contains(stripANSI(m.View()), feedMemoryOnlyWarning) {
		t.Fatal("no warning while memory-only")
	}

	store.fail.Store(false)
	clock.Advance(robot.AttentionStoreRetryInterval)
	feed.Append(testEvent("back"))
	m = tick(t, m)
	if strings.Contains(stripANSI(m.View()), feedMemoryOnlyWarning) {
		t.Fatal("warning still shown after recovery")
	}
}
```

Export `AttentionStoreRetryInterval` for this test. `faultInjectingStore` is shared through a small test helper package if both `robot` and `dashboard` need it, or defined in each if it stays a few lines. Prefer the few lines.

##### 🟢 Green

**File:** `internal/tui/dashboard/panels/ticker.go`: add `FeedWarning string` to `TickerData`. `buildPlainSegments` puts it first when it's set.

**File:** `internal/tui/dashboard/dashboard.go`, in `updateTickerData`:

```go
if feed := robot.PeekAttentionFeed(); feed != nil {
	if h := feed.StoreHealth(); h.Configured && !h.Durable {
		data.FeedWarning = feedMemoryOnlyWarning
	}
}
```

`const feedMemoryOnlyWarning = "⚠ Events not saving (memory only)"`.

##### 🔵 Refactor

- [ ] **No duplication:** the ticker gets one more segment through the existing path.
- [ ] **Reveals intent:** the named constant is shared with the test.
- [ ] **Complexity went down:** no new message type or poll.
- [ ] **No shallow wrappers:** none.
- [ ] **Fits existing patterns:** `TickerData` and `buildPlainSegments`.

#### Success Criteria

**Automated:** `go test -short -run TestClosure_DashboardWarnsWhileFeedIsMemoryOnly ./internal/tui/dashboard/`, with red-at-seam recorded.

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

---

## Integration & E2E Testing

- **Integration:** all 10 closure tests run in `go test -short`, in `internal/cli`, `internal/robot` and `internal/tui/dashboard`.
- **E2E:** the `e2e/` tests for this project come in Loop 9.

## Verification Commands

Run after each sprint, with Go on the PATH:

```bash
export PATH=$PATH:/usr/local/go/bin
go build ./cmd/ntm
go test -short ./...
go test -short -race ./internal/robot/ ./internal/cli/ ./internal/tui/dashboard/
gofmt -l .
go vet ./internal/robot/ ./internal/cli/ ./internal/state/ ./internal/tui/dashboard/
ubs <changed-files>
stat -c '%y' ~/.config/ntm/state.db   # before and after go test: must match
```

Run `golangci-lint run` and `goimports -l .` if they're installed. Report them as not run if they aren't.

## References

- Research: `thoughts/searchable/shared/research/2026-11-06-09-56-agent-messaging-push-stop-hooks-agent-mail.md`
- Closure framework: `~/.claude/commands/references/closure-test-framework.md`
- Patterns: `internal/robot/attention_feed_test.go:1050` (store-backed tests), `:2588` (stub store), `internal/tui/dashboard/dashboard_layout_test.go:2560` (mail message), `internal/tui/dashboard/testmain_test.go:11` (feed test lock)
