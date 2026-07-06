package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSkipKind_WhenConditionFalse(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "skip-kind-when",
		Steps: []Step{
			{ID: "skip", Prompt: "skip me", When: "false"},
		},
	}

	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	e := NewExecutor(cfg)

	state, err := e.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	result := state.Steps["skip"]
	if result.Status != StatusSkipped {
		t.Fatalf("Status = %s, want %s", result.Status, StatusSkipped)
	}
	if result.SkipKind != SkipKindWhenCondition {
		t.Fatalf("SkipKind = %q, want %q", result.SkipKind, SkipKindWhenCondition)
	}
}

func TestSkipKind_FailedDependency(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "skip-kind-failed-dep",
		Settings: WorkflowSettings{
			OnError: ErrorActionContinue,
		},
		Steps: []Step{
			{ID: "fail", Command: "false"},
			{ID: "blocked", Command: "printf should-not-run", DependsOn: []string{"fail"}},
		},
	}

	e := NewExecutor(DefaultExecutorConfig("test"))
	state, err := e.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := state.Steps["blocked"]
	if result.Status != StatusSkipped {
		t.Fatalf("Status = %s, want %s", result.Status, StatusSkipped)
	}
	if result.SkipKind != SkipKindFailedDependency {
		t.Fatalf("SkipKind = %q, want %q", result.SkipKind, SkipKindFailedDependency)
	}
}

func TestSkipKind_StartFrom(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "skip-kind-start-from",
		Steps: []Step{
			{ID: "setup", Prompt: "setup"},
			{ID: "target", Prompt: "target", DependsOn: []string{"setup"}},
		},
	}

	cfg := DefaultExecutorConfig("test")
	cfg.DryRun = true
	cfg.StartFromStep = "target"
	e := NewExecutor(cfg)

	state, err := e.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := state.Steps["setup"]
	if result.Status != StatusSkipped {
		t.Fatalf("Status = %s, want %s", result.Status, StatusSkipped)
	}
	if result.SkipKind != SkipKindStartFrom {
		t.Fatalf("SkipKind = %q, want %q", result.SkipKind, SkipKindStartFrom)
	}
}

func TestSkipKind_JSONOmitempty(t *testing.T) {
	data, err := json.Marshal(StepResult{StepID: "done", Status: StatusCompleted})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if json.Valid(data) && string(data) == "" {
		t.Fatal("Marshal() returned empty JSON")
	}
	if containsJSONField(data, "skip_kind") {
		t.Fatalf("completed StepResult unexpectedly includes skip_kind: %s", data)
	}

	data, err = json.Marshal(StepResult{StepID: "skip", Status: StatusSkipped, SkipKind: SkipKindWhenCondition})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !containsJSONField(data, "skip_kind") {
		t.Fatalf("skipped StepResult missing skip_kind: %s", data)
	}
}

func containsJSONField(data []byte, field string) bool {
	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return false
	}
	_, ok := decoded[field]
	return ok
}
