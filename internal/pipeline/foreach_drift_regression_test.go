package pipeline

import (
	"context"
	"strings"
	"testing"
)

// TestForeach_ItemsDriftDetectedOnResume covers bd-gstw3: extending bd-3awat's
// items-fingerprint drift detection from legacy `loop:` to the newer
// `foreach:` syntax. A foreach step that resolves items from a dynamic source
// must refuse resume when the resolved list differs from the original run,
// otherwise CompletedIterationIDs at integer indices map to wrong items.
//
// The test calls executeForeach directly (mirroring the bd-3awat
// loop:items pattern in loops_test.go) so the prior ForeachState survives —
// Run() would initialize a fresh state and erase the pre-seeded fingerprint.
func TestForeach_ItemsDriftDetectedOnResume(t *testing.T) {
	cfg := DefaultExecutorConfig("foreach-drift")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:         "run-drift",
		WorkflowID:    "wf",
		Variables:     map[string]interface{}{},
		Steps:         map[string]StepResult{},
		ForeachState:  map[string]ForeachIterationState{},
		ParallelState: map[string]ParallelGroupState{},
		InFlightSteps: map[string]InFlightStepState{},
	}

	// Pre-seed prior state: prior run completed iter0 against ["A","B","C"].
	priorFingerprint := computeForeachItemsFingerprint([]interface{}{"A", "B", "C"})
	executor.state.ForeachState["fanout"] = ForeachIterationState{
		StepID:                "fanout",
		CompletedIterationIDs: []string{"fanout_iter0"},
		ItemsFingerprint:      priorFingerprint,
		Total:                 3,
	}

	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items: `["X","Y","Z"]`,
			As:    "item",
			Steps: []Step{{ID: "noop", Command: "true"}},
		},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "drift",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{*step},
	}

	res := executor.executeForeach(context.Background(), step, workflow)
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, want %q (drift should fail-fast)", res.Status, StatusFailed)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "items changed since prior run") {
		t.Fatalf("Error.Message = %v, want to mention 'items changed since prior run'", res.Error)
	}
}

// TestForeach_FingerprintRecordedOnFirstRun covers the other half of
// bd-gstw3: a fresh foreach run must record the items fingerprint so
// subsequent resumes can verify against it.
func TestForeach_FingerprintRecordedOnFirstRun(t *testing.T) {
	cfg := DefaultExecutorConfig("foreach-fp-record")
	cfg.ProjectDir = t.TempDir()
	executor := NewExecutor(cfg)
	executor.state = &ExecutionState{
		RunID:         "run-fp",
		WorkflowID:    "wf",
		Variables:     map[string]interface{}{},
		Steps:         map[string]StepResult{},
		ForeachState:  map[string]ForeachIterationState{},
		ParallelState: map[string]ParallelGroupState{},
		InFlightSteps: map[string]InFlightStepState{},
	}

	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items: `["A","B"]`,
			As:    "item",
			Steps: []Step{{ID: "noop", Command: "true"}},
		},
	}
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "fp",
		Settings:      DefaultWorkflowSettings(),
		Steps:         []Step{*step},
	}

	_ = executor.executeForeach(context.Background(), step, workflow)
	st, ok := executor.state.ForeachState["fanout"]
	if !ok {
		t.Fatal("state.ForeachState[fanout] missing after first run")
	}
	expected := computeForeachItemsFingerprint([]interface{}{"A", "B"})
	if st.ItemsFingerprint != expected {
		t.Fatalf("ItemsFingerprint = %q, want %q", st.ItemsFingerprint, expected)
	}
}
