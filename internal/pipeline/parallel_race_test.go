package pipeline

// Race-detector regression suite for parallel + foreach + nested topologies.
//
// Documented invocations (per bd-7ramj.16 acceptance):
//
//	go test -race -run '^TestRace_' ./internal/pipeline/...
//	go test -race -count=10 -run '^TestRace_' ./internal/pipeline/...
//
// CI is expected to run the race-targeted suite separately from the default
// `go test -short` path so the race overhead does not slow normal runs.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newRaceExecutor(t *testing.T, workflowName string) *Executor {
	t.Helper()
	cfg := DefaultExecutorConfig("race-session")
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      fmt.Sprintf("race-%s-%d", workflowName, time.Now().UnixNano()),
		WorkflowID: workflowName,
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}
	return e
}

// stressSnapshots spins concurrent snapshotState + substituteVariables readers
// until stop is closed. Returns a WaitGroup the caller should wg.Wait() on
// after closing stop.
func stressSnapshots(t *testing.T, e *Executor, readers int, stop <-chan struct{}, templates ...string) *sync.WaitGroup {
	t.Helper()
	var wg sync.WaitGroup
	if len(templates) == 0 {
		templates = []string{"${vars.absent | \"x\"}"}
	}
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

// TestRace_ParallelInline_50steps fans out 50 inline steps via executeParallel
// while concurrent readers stress varMu/stateMu.
func TestRace_ParallelInline_50steps(t *testing.T) {
	const stepCount = 50
	parallelSteps := make([]Step, stepCount)
	for i := range parallelSteps {
		parallelSteps[i] = Step{
			ID:     fmt.Sprintf("inline_%02d", i),
			Prompt: fmt.Sprintf("task %d", i),
		}
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-parallel-inline",
		Settings:      DefaultWorkflowSettings(),
		Steps: []Step{
			{ID: "parallel_group", Parallel: ParallelSpec{Steps: parallelSteps}},
		},
	}

	e := newRaceExecutor(t, "race-parallel-inline")
	e.graph = NewDependencyGraph(workflow)

	step := &Step{
		ID:       "parallel_group",
		Parallel: ParallelSpec{Steps: parallelSteps},
	}

	stop := make(chan struct{})
	wg := stressSnapshots(t, e, 4, stop, "ping ${vars.absent | \"x\"}")

	result := e.executeParallel(context.Background(), step, workflow)

	close(stop)
	wg.Wait()

	if result.Status != StatusCompleted {
		t.Fatalf("expected StatusCompleted, got %s (err=%+v)", result.Status, result.Error)
	}
	e.stateMu.RLock()
	gotSteps := len(e.state.Steps)
	e.stateMu.RUnlock()
	if gotSteps != stepCount {
		t.Fatalf("expected %d step results, got %d", stepCount, gotSteps)
	}
}

// TestRace_ForeachLoopUnderSnapshotPressure runs a 100-iteration foreach loop
// (sequential dispatch — the parallel iteration runtime in bd-2ubxp.12 is still
// open) while concurrent readers exercise the loop-scope stack via substitution.
// Detects races on state.Variables across pushLoopVars/popLoopVars boundaries.
func TestRace_ForeachLoopUnderSnapshotPressure(t *testing.T) {
	const itemCount = 100
	items := make([]interface{}, itemCount)
	for i := range items {
		items[i] = fmt.Sprintf("item-%03d", i)
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-foreach",
		Settings:      DefaultWorkflowSettings(),
	}
	workflow.Settings.OnError = ErrorActionContinue

	e := newRaceExecutor(t, "race-foreach")
	e.graph = NewDependencyGraph(workflow)
	e.varMu.Lock()
	e.state.Variables["data"] = items
	e.varMu.Unlock()

	step := &Step{
		ID: "foreach_root",
		Loop: &LoopConfig{
			Items:         "${data}",
			As:            "row",
			MaxIterations: IntOrExpr{Value: itemCount + 1},
			Steps: []Step{
				{ID: "iter_substep", Prompt: "process ${loop.row}"},
			},
		},
	}

	stop := make(chan struct{})
	wg := stressSnapshots(t, e, 8, stop,
		"loop ${loop.row | \"none\"}",
		"index ${loop.index | \"-\"}",
		"data ${data | \"none\"}",
	)

	res := e.executeLoop(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("foreach loop expected StatusCompleted, got %s (err=%+v)", res.Status, res.Error)
	}
}

// TestRace_NestedForeachParallel covers the bd-7ramj.16 spec scenario "outer
// foreach + inner parallel substeps". Each iteration dispatches a parallel
// group; outer readers spin snapshots so the test fails under -race if any
// shared map is touched without holding the right mutex.
//
// Note: nested parallel-IN-parallel groups are explicitly rejected by
// executeParallelStep at runtime today (see executor.go) so this exercises
// the supported foreach->parallel topology rather than parallel->parallel.
func TestRace_NestedForeachParallel(t *testing.T) {
	const iterations = 6
	const innerWidth = 12
	items := make([]interface{}, iterations)
	for i := range items {
		items[i] = i
	}

	innerParallel := make([]Step, innerWidth)
	for i := range innerParallel {
		innerParallel[i] = Step{
			ID:     fmt.Sprintf("inner_%d", i),
			Prompt: fmt.Sprintf("inner work ${loop.row} #%d", i),
		}
	}

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "race-loop-parallel",
		Settings:      DefaultWorkflowSettings(),
	}

	e := newRaceExecutor(t, "race-loop-parallel")
	e.graph = NewDependencyGraph(workflow)
	e.varMu.Lock()
	e.state.Variables["rows"] = items
	e.varMu.Unlock()

	step := &Step{
		ID: "loop_with_parallel",
		Loop: &LoopConfig{
			Items:         "${rows}",
			As:            "row",
			MaxIterations: IntOrExpr{Value: iterations + 1},
			Steps: []Step{
				{ID: "fanout", Parallel: ParallelSpec{Steps: innerParallel}},
			},
		},
	}

	stop := make(chan struct{})
	wg := stressSnapshots(t, e, 4, stop,
		"snap ${loop.row | \"-\"}",
		"snap2 ${loop.index | \"-\"}",
	)

	res := e.executeLoop(context.Background(), step, workflow)
	close(stop)
	wg.Wait()

	if res.Status != StatusCompleted {
		t.Fatalf("loop+parallel expected StatusCompleted, got %s (err=%+v)", res.Status, res.Error)
	}
}

// TestRace_PostPipelineDuringMain is reserved for the tight overlap between
// the main step graph and post_pipeline_steps. The runtime hook for
// post_pipeline_steps lives in bd-w6nth.5 (still open) — the field is parsed
// but not yet dispatched. When that bead lands this test should be filled in
// with a workflow that finishes a parallel group exactly as the post-pipeline
// dispatch begins.
func TestRace_PostPipelineDuringMain(t *testing.T) {
	t.Skip("blocked on bd-w6nth.5: post_pipeline_steps runtime not yet wired")
}

// TestRace_VariableScopeStackUnderForeach stresses the variable-scope stack
// (pushLoopVars/popLoopVars) under heavy concurrent substituteVariables and
// snapshotState pressure. This is the unit-level version of the foreach
// scenario in TestRace_NestedForeachParallel and is fast enough to be the
// canary signal for varMu regressions.
func TestRace_VariableScopeStackUnderForeach(t *testing.T) {
	e := newRaceExecutor(t, "race-scope")
	e.varMu.Lock()
	e.state.Variables["base"] = "stable"
	e.varMu.Unlock()

	const writers = 4
	const readers = 8
	const itersPerWriter = 200

	var writerWG sync.WaitGroup
	var readerWG sync.WaitGroup
	stop := make(chan struct{})
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
				_ = e.substituteVariables("${base | \"-\"} ${loop.row_w0 | \"-\"} ${loop.index | \"-\"}")
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
				scope := e.loopExec.pushLoopVars(varName, fmt.Sprintf("v-%d-%d", id, i), i, itersPerWriter)
				atomic.AddInt64(&pushes, 1)
				e.loopExec.popLoopVars(scope)
			}
		}(w)
	}

	writerWG.Wait()
	close(stop)
	readerWG.Wait()

	if got := atomic.LoadInt64(&pushes); got != int64(writers*itersPerWriter) {
		t.Fatalf("expected %d push/pop cycles, got %d", writers*itersPerWriter, got)
	}

	// After all writers popped their scopes the base variable must still be
	// the original value — otherwise pushLoopVars leaked a write through.
	e.varMu.RLock()
	got := e.state.Variables["base"]
	e.varMu.RUnlock()
	if got != "stable" {
		t.Fatalf("base variable corrupted by scope stack: got %v want \"stable\"", got)
	}
}
