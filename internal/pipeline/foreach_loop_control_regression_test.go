package pipeline

import (
	"context"
	"strings"
	"testing"
)

func TestExecuteForeachLoopControlRunsControllingStepBody(t *testing.T) {
	workflow, err := ParseString(`
schema_version: "2.0"
name: foreach-loop-control-body
steps:
  - id: fanout
    foreach:
      items: '["a","b","c"]'
      steps:
        - id: before
          command: "printf 'before-%s' '${item}'"
        - id: continue_on_b
          command: "printf 'stop-%s' '${item}'"
          when: '${item} == "b"'
          loop_control: continue
        - id: after
          command: "printf 'after-%s' '${item}'"
`, "yaml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() errors = %+v", result.Errors)
	}

	step := &workflow.Steps[0]
	executor := createForeachTestExecutor(t, workflow)
	result := executor.executeForeach(context.Background(), step, workflow)
	if result.Status != StatusCompleted {
		t.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
	}

	iterations := foreachIterationsFromResult(t, result)
	if iterations[1].Control != LoopControlContinue {
		t.Fatalf("iteration 1 control = %q, want continue", iterations[1].Control)
	}
	if got := strings.Join(foreachLeafOutputs(result), ","); got != "before-a,after-a,before-b,stop-b,before-c,after-c" {
		t.Fatalf("leaf outputs = %q, want controlling command output before continue", got)
	}
}
