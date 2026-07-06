package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestExecuteForeachSkippedOptionalBodyStepContinuesIteration(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "foreach-skipped-optional-body",
		Settings:      DefaultWorkflowSettings(),
	}
	step := &Step{
		ID: "optional_body",
		Foreach: &ForeachConfig{
			Items: `["skip","run"]`,
			Steps: []Step{
				{
					ID:      "optional",
					When:    `${item} == "run"`,
					Command: `printf 'optional-%s' '${item}'`,
				},
				{
					ID:      "always",
					Command: `printf 'always-%s' '${item}'`,
				},
			},
		},
	}
	workflow.Steps = []Step{*step}
	executor := createForeachTestExecutor(t, workflow)

	result := executor.executeForeach(context.Background(), step, workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}

	iterations := foreachIterationsFromResult(t, result)
	if len(iterations) != 2 {
		t.Fatalf("iterations = %d, want 2", len(iterations))
	}
	if iterations[0].Skipped {
		t.Fatalf("iteration with later completed body step marked skipped: %#v", iterations[0])
	}
	if got := len(iterations[0].Results); got != 2 {
		t.Fatalf("iteration 0 results = %d, want skipped optional plus completed always", got)
	}
	if iterations[0].Results[0].Status != StatusSkipped || iterations[0].Results[1].Status != StatusCompleted {
		t.Fatalf("iteration 0 results = %#v, want skipped optional then completed always", iterations[0].Results)
	}

	got := strings.Join(foreachLeafOutputs(result), ",")
	want := "always-skip,optional-run,always-run"
	if got != want {
		t.Fatalf("leaf outputs = %q, want %q", got, want)
	}
	if !strings.Contains(result.Output, "0 skipped") {
		t.Fatalf("foreach output = %q, want no skipped iterations", result.Output)
	}
}
