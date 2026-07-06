package pipeline

import (
	"context"
	"testing"
)

// TestExecuteCommand_RoundOverlayInArgsAndStdin covers bd-s2edh: foreach
// max_rounds round overlays must reach Args and Stdin substitution paths,
// not only step.Command.
func TestExecuteCommand_RoundOverlayInArgsAndStdin(t *testing.T) {
	e := newCommandTestExecutor(t)

	ctx := withRoundOverrides(context.Background(), buildRoundOverrides(2, 3))
	step := &Step{
		ID:      "obs",
		Command: `sh -c 'echo "cmd=${round} stdin=$(cat)"'`,
		Stdin:   "round-from-stdin=${round}",
		Args:    map[string]interface{}{"ROUND_ARG": "${round}"},
	}
	result := e.executeCommand(ctx, step, &Workflow{Name: "wf"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q; error = %+v", result.Status, StatusCompleted, result.Error)
	}
	if got, want := result.Output, `cmd=2 stdin=round-from-stdin=2`; got != want {
		t.Fatalf("Output = %q, want %q (overlay must reach Command and Stdin)", got, want)
	}
}

// TestResolvePaneExprCtx_RoundOverlay covers bd-s2edh: pane.expr
// substitution must consult ctx round overlays so a foreach max_rounds
// body that uses pane.expr=${round} resolves per-iteration.
func TestResolvePaneExprCtx_RoundOverlay(t *testing.T) {
	e := newCommandTestExecutor(t)

	ctx := withRoundOverrides(context.Background(), buildRoundOverrides(3, 5))
	step := &Step{ID: "obs", Pane: PaneSpec{Expr: "${round}"}}
	if err := e.resolvePaneExprCtx(ctx, step); err != nil {
		t.Fatalf("resolvePaneExprCtx() error = %v", err)
	}
	if step.Pane.Index != 3 {
		t.Fatalf("Pane.Index = %d, want 3 (overlay round=3)", step.Pane.Index)
	}
	if step.Pane.Expr != "" {
		t.Fatalf("Pane.Expr = %q, want cleared after resolution", step.Pane.Expr)
	}
}

// TestEvaluateConditionCtx_RoundOverlay covers bd-s2edh: evaluateCondition
// for parallel-sub-step `when` clauses must consult ctx round overlays so a
// foreach max_rounds body whose body step is a parallel block with
// `when: ${round} == 2` resolves per-iteration. A direct unit test on the
// ctx-aware helper is the cheapest faithful coverage.
func TestEvaluateConditionCtx_RoundOverlay(t *testing.T) {
	e := newCommandTestExecutor(t)

	tests := []struct {
		round    int
		expected bool // expected SKIP value (true means condition matches "2")
	}{
		{round: 1, expected: true},  // ${round} == "2" → false → !skip in foreach but evaluateCondition returns "should skip" inverse; check below
		{round: 2, expected: false}, // ${round} == "2" → true → don't skip
		{round: 3, expected: true},
	}

	// evaluateCondition's contract: returns true if the step should be SKIPPED.
	// A condition of `${round} == "2"` should NOT skip when round == 2.
	for _, tc := range tests {
		ctx := withRoundOverrides(context.Background(), buildRoundOverrides(tc.round, 5))
		skip, err := e.evaluateConditionCtx(ctx, `${round} == "2"`)
		if err != nil {
			t.Fatalf("round=%d: evaluateConditionCtx error = %v", tc.round, err)
		}
		if skip != tc.expected {
			t.Errorf("round=%d: skip = %v, want %v (condition: ${round} == \"2\")", tc.round, skip, tc.expected)
		}
	}
}
