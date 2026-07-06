package pipeline

// Race-detector regression suite for the foreach runtime (bd-rx0h2).
//
// Documented invocations:
//
//	go test -race -run '^TestRace_Foreach' ./internal/pipeline/...
//	go test -race -count=10 -run '^TestRace_Foreach' ./internal/pipeline/...
//
// These tests exercise the actual `executeForeach`,
// `executeForeachIterationsSequential`, `executeForeachIterationsParallel`,
// `selectForeachPane`, `pushForeachVars`, and `popForeachVars` paths.
// Earlier `TestRace_*` coverage in parallel_race_test.go only exercised
// `executeLoop` (Step.Loop) and the loop-scope stack helpers; the foreach
// fan-out runtime remained untested under -race.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// newForeachRaceExecutor builds a DryRun executor wired to a fresh
// ExecutionState. Pass a non-nil mock to install it as the tmux transport
// (required for ForeachPane tests; harmless for the rest).
func newForeachRaceExecutor(t *testing.T, workflowName string, mock *MockTmuxClient) *Executor {
	t.Helper()
	cfg := DefaultExecutorConfig("race-session")
	cfg.DryRun = true
	cfg.DefaultTimeout = 2 * time.Second
	e := NewExecutor(cfg)
	if mock != nil {
		e.SetTmuxClient(mock)
	}
	e.state = &ExecutionState{
		RunID:      fmt.Sprintf("race-foreach-%s-%d", workflowName, time.Now().UnixNano()),
		WorkflowID: workflowName,
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}
	settings := DefaultWorkflowSettings()
	e.limits = settings.Limits.EffectiveLimits()
	return e
}

// stressForeachReaders mirrors stressSnapshots but is parameterised with the
// templates we want to substitute. Returned WaitGroup must be Wait()ed after
// closing stop.
func stressForeachReaders(t *testing.T, e *Executor, readers int, stop <-chan struct{}, templates ...string) *sync.WaitGroup {
	t.Helper()
	if len(templates) == 0 {
		templates = []string{`${vars.absent | "x"}`}
	}
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if snap := e.snapshotState(); snap == nil {
					t.Errorf("reader %d: snapshotState returned nil", idx)
					return
				}
				_ = e.substituteVariables(templates[idx%len(templates)])
			}
		}(i)
	}
	return &wg
}

// TestRace_ForeachSequentialRuntime exercises the real executeForeach +
// executeForeachIterationsSequential path under concurrent state-snapshot and
// variable-substitution pressure.
func TestRace_ForeachSequentialRuntime(t *testing.T) {
	const itemCount = 40
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-foreach-seq",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue

	e := newForeachRaceExecutor(t, workflow.Name, nil)
	e.graph = NewDependencyGraph(workflow)

	items := make([]string, itemCount)
	for i := range items {
		items[i] = fmt.Sprintf(`"row-%02d"`, i)
	}
	itemsLiteral := "[" + joinStrings(items, ",") + "]"

	step := &Step{
		ID: "foreach_seq",
		Foreach: &ForeachConfig{
			Items: itemsLiteral,
			As:    "row",
			Steps: []Step{{
				ID:      "echo",
				Command: "printf '%s' '${item}'",
			}},
		},
	}

	stop := make(chan struct{})
	wg := stressForeachReaders(t, e, 6, stop,
		`row ${loop.row | "-"}`,
		`index ${loop.index | "-"}`,
		`item ${item | "-"}`,
	)

	res := e.executeForeach(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("foreach status = %s err=%+v", res.Status, res.Error)
	}
	iterations, ok := res.ParsedData.([]foreachIterationResult)
	if !ok {
		t.Fatalf("ParsedData type = %T, want []foreachIterationResult", res.ParsedData)
	}
	if len(iterations) != itemCount {
		t.Fatalf("iterations = %d, want %d", len(iterations), itemCount)
	}
}

// TestRace_ForeachParallelRuntime exercises the parallel iteration path
// (executeForeachIterationsParallel) so concurrent dispatch goroutines, the
// cancel/break controlMu, and per-iteration pushForeachVars/popForeachVars all
// run inside a single -race invocation.
func TestRace_ForeachParallelRuntime(t *testing.T) {
	const itemCount = 32
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-foreach-par",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue

	e := newForeachRaceExecutor(t, workflow.Name, nil)
	e.graph = NewDependencyGraph(workflow)

	items := make([]string, itemCount)
	for i := range items {
		items[i] = fmt.Sprintf("%d", i)
	}
	itemsLiteral := "[" + joinStrings(items, ",") + "]"

	step := &Step{
		ID: "foreach_par",
		Foreach: &ForeachConfig{
			Items:         itemsLiteral,
			As:            "n",
			Parallel:      true,
			MaxConcurrent: 4,
			Steps: []Step{{
				ID:      "echo",
				Command: "printf '%s' '${item}'",
			}},
		},
	}

	stop := make(chan struct{})
	wg := stressForeachReaders(t, e, 8, stop,
		`n ${loop.n | "-"}`,
		`item ${item | "-"}`,
		`idx ${loop.index | "-"}`,
	)

	res := e.executeForeach(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("foreach status = %s err=%+v", res.Status, res.Error)
	}
	iterations, _ := res.ParsedData.([]foreachIterationResult)
	if len(iterations) != itemCount {
		t.Fatalf("iterations = %d, want %d", len(iterations), itemCount)
	}
}

// TestRace_ForeachPaneSequential exercises the foreach_pane source path by
// installing a MockTmuxClient with a fixed roster of panes. Drives
// executeForeach -> resolveForeachItems(foreach_pane) -> prepareForeachIterations
// -> selectForeachPane while readers spin snapshots.
func TestRace_ForeachPaneSequential(t *testing.T) {
	mock := mockPaneRoster(8)
	t.Cleanup(mock.Reset)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-foreach-pane-seq",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue

	e := newForeachRaceExecutor(t, workflow.Name, mock)
	e.graph = NewDependencyGraph(workflow)

	step := &Step{
		ID: "foreach_pane_seq",
		ForeachPane: &ForeachConfig{
			Steps: []Step{{
				ID:      "echo",
				Command: "printf '%s' '${pane.id}'",
			}},
		},
	}

	stop := make(chan struct{})
	wg := stressForeachReaders(t, e, 6, stop,
		`pane ${pane.id | "-"}`,
		`pane-idx ${pane.index | "-"}`,
		`item ${item | "-"}`,
	)

	res := e.executeForeach(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("foreach_pane status = %s err=%+v", res.Status, res.Error)
	}
	iterations, _ := res.ParsedData.([]foreachIterationResult)
	if len(iterations) == 0 {
		t.Fatalf("expected non-empty iteration results")
	}
}

// TestRace_ForeachPaneParallel forces the foreach_pane fan-out through the
// parallel iteration runtime. Detects races between concurrent
// executeForeachIteration goroutines and the per-iteration variable scope
// stack while readers continuously snapshot state.
func TestRace_ForeachPaneParallel(t *testing.T) {
	mock := mockPaneRoster(6)
	t.Cleanup(mock.Reset)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-foreach-pane-par",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue

	e := newForeachRaceExecutor(t, workflow.Name, mock)
	e.graph = NewDependencyGraph(workflow)

	step := &Step{
		ID: "foreach_pane_par",
		ForeachPane: &ForeachConfig{
			Parallel:      true,
			MaxConcurrent: 4,
			Steps: []Step{{
				ID:      "echo",
				Command: "printf '%s' '${pane.id}'",
			}},
		},
	}

	stop := make(chan struct{})
	wg := stressForeachReaders(t, e, 8, stop,
		`pane ${pane.id | "-"}`,
		`pane-idx ${pane.index | "-"}`,
		`item ${item | "-"}`,
	)

	res := e.executeForeach(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("foreach_pane parallel status = %s err=%+v", res.Status, res.Error)
	}
}

// TestRace_PushForeachVarsConcurrent stresses pushForeachVars/popForeachVars
// directly. Multiple writers push and pop scopes while readers spin
// substituteVariables and snapshotState. After all writers settle, a sentinel
// variable that lived outside any pushed scope must still hold its original
// value — proving the scope helpers do not leak writes through the varMu.
func TestRace_PushForeachVarsConcurrent(t *testing.T) {
	e := newForeachRaceExecutor(t, "race-foreach-scope", nil)
	e.varMu.Lock()
	e.state.Variables["base"] = "stable"
	e.varMu.Unlock()

	const writers = 4
	const readers = 8
	const itersPerWriter = 200

	stop := make(chan struct{})
	var writerWG sync.WaitGroup
	var readerWG sync.WaitGroup
	var pushes int64

	for i := 0; i < readers; i++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = e.substituteVariables(`${base | "-"} ${pane.id | "-"} ${loop.index | "-"}`)
				_ = e.snapshotState()
			}
		}()
	}

	for w := 0; w < writers; w++ {
		writerWG.Add(1)
		go func(id int) {
			defer writerWG.Done()
			varName := fmt.Sprintf("row_w%d", id)
			for i := 0; i < itersPerWriter; i++ {
				paneVars := map[string]interface{}{
					"id":    fmt.Sprintf("%%w%d_%d", id, i),
					"index": i + 1,
					"role":  fmt.Sprintf("role-%d", id),
				}
				scope := e.pushForeachVars(varName, fmt.Sprintf("v-%d-%d", id, i), i, itersPerWriter, paneVars)
				atomic.AddInt64(&pushes, 1)
				e.popForeachVars(scope)
			}
		}(w)
	}

	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	if got := atomic.LoadInt64(&pushes); got != int64(writers*itersPerWriter) {
		t.Fatalf("expected %d push/pop cycles, got %d", writers*itersPerWriter, got)
	}

	e.varMu.RLock()
	got := e.state.Variables["base"]
	e.varMu.RUnlock()
	if got != "stable" {
		t.Fatalf("base variable corrupted by foreach scope stack: got %v want \"stable\"", got)
	}
}

// TestRace_SelectForeachPaneConcurrent fans out concurrent selectForeachPane
// callers across every supported strategy. selectForeachPane is intentionally
// stateless on the input slices, but reading from them while another caller
// might mutate the underlying tmux.Pane slice elsewhere is the realistic
// production race profile. -race must be clean across all strategies.
func TestRace_SelectForeachPaneConcurrent(t *testing.T) {
	const callers = 8
	const callsPerCaller = 200

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, NTMIndex: 1, Type: tmux.AgentClaude, Variant: "opus", Tags: []string{"frontend"}},
		{ID: "%2", Index: 2, NTMIndex: 2, Type: tmux.AgentCodex, Variant: "gpt-5", Tags: []string{"backend"}},
		{ID: "%3", Index: 3, NTMIndex: 3, Type: tmux.AgentGemini, Variant: "pro", Tags: []string{"frontend"}},
		{ID: "%4", Index: 4, NTMIndex: 4, Type: tmux.AgentClaude, Variant: "sonnet", Tags: []string{"backend"}},
	}
	strategyPanes := foreachStrategyPanes(panes)

	strategies := []string{"", "round_robin", "round_robin_by_domain", "by_model_family", "rotate_adjudicator"}
	items := []interface{}{
		map[string]interface{}{"domain": "frontend", "id": "task-a", "model_family": "claude"},
		map[string]interface{}{"domain": "backend", "id": "task-b", "model_family": "codex", "champions": []interface{}{"claude", "codex"}},
		map[string]interface{}{"domain": "data", "id": "task-c", "model_family": "gemini"},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, callers*callsPerCaller)

	for c := 0; c < callers; c++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < callsPerCaller; i++ {
				strategy := strategies[(seed+i)%len(strategies)]
				item := items[(seed+i)%len(items)]
				_, _, _, err := selectForeachPane(strategy, strategyPanes, panes, item, seed*callsPerCaller+i)
				// Some strategy/item combinations have legitimate errors
				// (e.g. by_model_family with unknown family). The race
				// detector cares about safe concurrent reads — not the
				// returned err — so we only fail on panics, which would
				// surface as a test crash, not via errCh.
				_ = err
			}
		}(c)
	}

	wg.Wait()
	close(errCh)
}

// joinStrings is a tiny strings.Join clone to avoid pulling the strings
// import for one call inside this test file (`strings` is used already in
// other tests but kept this file lean intentionally).
func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
}

// mockPaneRoster builds a MockTmuxClient seeded with n panes that look like a
// realistic NTM roster. Each pane has a distinct ID/index plus tags so
// selectForeachPane has non-trivial input.
func mockPaneRoster(n int) *MockTmuxClient {
	mock := NewMockTmuxClient()
	for i := 1; i <= n; i++ {
		var agentType tmux.AgentType
		switch i % 3 {
		case 0:
			agentType = tmux.AgentClaude
		case 1:
			agentType = tmux.AgentCodex
		default:
			agentType = tmux.AgentGemini
		}
		mock.AddPane("", tmux.Pane{
			ID:       fmt.Sprintf("%%%d", i),
			Index:    i,
			NTMIndex: i,
			Type:     agentType,
			Variant:  fmt.Sprintf("variant-%d", i),
			Tags:     []string{fmt.Sprintf("group-%d", i%2)},
		})
	}
	return mock
}
