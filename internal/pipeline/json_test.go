package pipeline

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestJSONRoundTrip_NewSchemaTypes(t *testing.T) {
	workflow := Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "json-new-schema-types",
		Notes:         StringOrList{"operator note", "second note"},
		Defaults: map[string]interface{}{
			"triage_pane": "${vars.default_pane}",
		},
		Outputs: []OutputDecl{
			{Name: "report", Description: "Final report", Path: "deliverables/report.md"},
		},
		Settings: WorkflowSettings{
			Timeout:          Duration{Duration: 2 * time.Minute},
			OnError:          ErrorActionContinue,
			NotifyOnComplete: true,
			NotifyOnError:    true,
			NotifyChannels:   []string{"desktop", "mail"},
			MailRecipient:    "operator",
		},
		Steps: []Step{
			{
				ID:     "pane-index",
				Agent:  "codex",
				Pane:   PaneSpec{Index: 2},
				Prompt: "literal pane",
			},
			{
				ID:        "pane-expr",
				Agent:     "claude",
				Pane:      PaneSpec{Expr: "${defaults.triage_pane}"},
				Prompt:    "expr pane",
				DependsOn: []string{"pane-index"},
				After:     AfterRef{"bootstrap", "warm-cache"},
			},
			{
				ID:     "parallel-flag",
				Prompt: "fan out foreach body",
				Parallel: ParallelSpec{
					Flag: true,
				},
			},
			{
				ID: "parallel-steps",
				Parallel: ParallelSpec{
					Steps: []Step{
						{ID: "branch-a", Prompt: "A"},
						{ID: "branch-b", Command: "printf B"},
					},
				},
			},
			{
				ID:      "on-failure-retry",
				Command: "false",
				OnFailure: OnFailureSpec{
					Action:     "retry",
					RetryCount: 3,
				},
			},
			{
				ID:      "on-failure-fallback",
				Command: "false",
				OnFailure: OnFailureSpec{
					Fallback: map[string]interface{}{
						"pane":     "${defaults.triage_pane}",
						"template": "recovery.md",
					},
				},
			},
			{
				ID: "loop-limit-value",
				Loop: &LoopConfig{
					Items:         "${vars.items}",
					As:            "item",
					MaxIterations: IntOrExpr{Value: 10},
					Steps: []Step{
						{ID: "loop-body", Prompt: "process ${item}"},
					},
				},
			},
			{
				ID: "foreach-config",
				Foreach: &ForeachConfig{
					Items:         "${vars.files}",
					As:            "file",
					Models:        StringOrList{"codex", "gemini"},
					MaxRounds:     IntOrExpr{Expr: "${defaults.max_rounds}"},
					Filter:        "state==active",
					PaneStrategy:  "round_robin",
					Template:      "review.md",
					Params:        map[string]interface{}{"severity": "high"},
					Parallel:      true,
					MaxConcurrent: 4,
					Steps: []Step{
						{ID: "foreach-body", Prompt: "review ${file}"},
					},
				},
			},
		},
	}

	data, err := json.Marshal(workflow)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got Workflow
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v\nJSON:\n%s", err, data)
	}

	if !reflect.DeepEqual(got, workflow) {
		t.Fatalf("JSON round trip mismatch\nwant: %#v\n got: %#v\nJSON:\n%s", workflow, got, data)
	}
}

func TestJSONScalarAlternationForms_NewSchemaTypes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		assert  func(t *testing.T, got Workflow)
	}{
		{
			name: "pane scalar",
			content: `{
				"schema_version": "2.0",
				"name": "bad-pane",
				"steps": [{"id": "pane", "prompt": "hello", "pane": 3}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if got.Steps[0].Pane != (PaneSpec{Index: 3}) {
					t.Fatalf("Pane = %#v, want Index=3", got.Steps[0].Pane)
				}
			},
		},
		{
			name: "pane expression",
			content: `{
				"schema_version": "2.0",
				"name": "expr-pane",
				"steps": [{"id": "pane", "prompt": "hello", "pane": "${defaults.triage_pane}"}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if got.Steps[0].Pane != (PaneSpec{Expr: "${defaults.triage_pane}"}) {
					t.Fatalf("Pane = %#v, want expression", got.Steps[0].Pane)
				}
			},
		},
		{
			name: "parallel scalar",
			content: `{
				"schema_version": "2.0",
				"name": "bad-parallel",
				"steps": [{"id": "parallel", "prompt": "hello", "parallel": true}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if !got.Steps[0].Parallel.Flag {
					t.Fatalf("Parallel.Flag = false, want true")
				}
			},
		},
		{
			name: "parallel list",
			content: `{
				"schema_version": "2.0",
				"name": "parallel-list",
				"steps": [{"id": "parallel", "parallel": [{"id": "child", "prompt": "hello"}]}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if len(got.Steps[0].Parallel.Steps) != 1 || got.Steps[0].Parallel.Steps[0].ID != "child" {
					t.Fatalf("Parallel.Steps = %#v, want child step", got.Steps[0].Parallel.Steps)
				}
			},
		},
		{
			name: "after scalar",
			content: `{
				"schema_version": "2.0",
				"name": "bad-after",
				"steps": [{"id": "after", "prompt": "hello", "after": "pane"}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if !reflect.DeepEqual(got.Steps[0].After, AfterRef{"pane"}) {
					t.Fatalf("After = %#v, want pane", got.Steps[0].After)
				}
			},
		},
		{
			name: "notes scalar",
			content: `{
				"schema_version": "2.0",
				"name": "bad-notes",
				"notes": "single note",
				"steps": [{"id": "step", "prompt": "hello"}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if !reflect.DeepEqual(got.Notes, StringOrList{"single note"}) {
					t.Fatalf("Notes = %#v, want single note list", got.Notes)
				}
			},
		},
		{
			name: "output bare string",
			content: `{
				"schema_version": "2.0",
				"name": "bad-output",
				"outputs": ["deliverables/report.md"],
				"steps": [{"id": "step", "prompt": "hello"}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if len(got.Outputs) != 1 || got.Outputs[0].Path != "deliverables/report.md" {
					t.Fatalf("Outputs = %#v, want bare path output", got.Outputs)
				}
			},
		},
		{
			name: "output single key",
			content: `{
				"schema_version": "2.0",
				"name": "single-key-output",
				"outputs": [{"workspace": "${workspace_path}"}],
				"steps": [{"id": "step", "prompt": "hello"}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if len(got.Outputs) != 1 || got.Outputs[0].Name != "workspace" || got.Outputs[0].Path != "${workspace_path}" {
					t.Fatalf("Outputs = %#v, want single-key output", got.Outputs)
				}
			},
		},
		{
			name: "on failure retry shorthand",
			content: `{
				"schema_version": "2.0",
				"name": "retry-shorthand",
				"steps": [{"id": "step", "command": "false", "on_failure": "retry:2"}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				spec := got.Steps[0].OnFailure
				if spec.Action != "retry" || spec.RetryCount != 2 {
					t.Fatalf("OnFailure = %#v, want retry:2", spec)
				}
			},
		},
		{
			name: "loop max iterations scalar",
			content: `{
				"schema_version": "2.0",
				"name": "max-iterations",
				"steps": [{"id": "loop", "loop": {"items": "${vars.items}", "max_iterations": 5}}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				if got.Steps[0].Loop == nil || got.Steps[0].Loop.MaxIterations != (IntOrExpr{Value: 5}) {
					t.Fatalf("MaxIterations = %#v, want Value=5", got.Steps[0].Loop)
				}
			},
		},
		{
			name: "foreach models and rounds scalar",
			content: `{
				"schema_version": "2.0",
				"name": "foreach-scalars",
				"steps": [{"id": "foreach", "foreach": {"items": "${vars.items}", "models": "codex", "max_rounds": "${defaults.max_rounds}"}}]
			}`,
			assert: func(t *testing.T, got Workflow) {
				t.Helper()
				foreach := got.Steps[0].Foreach
				if foreach == nil {
					t.Fatal("Foreach = nil, want config")
				}
				if !reflect.DeepEqual(foreach.Models, StringOrList{"codex"}) {
					t.Fatalf("Models = %#v, want codex list", foreach.Models)
				}
				if foreach.MaxRounds != (IntOrExpr{Expr: "${defaults.max_rounds}"}) {
					t.Fatalf("MaxRounds = %#v, want expression", foreach.MaxRounds)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Workflow
			err := json.Unmarshal([]byte(tt.content), &got)
			if err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			tt.assert(t, got)
		})
	}
}

func TestJSONExecutionState_StableShape(t *testing.T) {
	started := time.Date(2026, 5, 7, 6, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	state := ExecutionState{
		RunID:      "run-20260507-060000-abcd",
		WorkflowID: "json-state",
		Status:     StatusCompleted,
		StartedAt:  started,
		UpdatedAt:  finished,
		Steps: map[string]StepResult{
			"step-1": {
				StepID:     "step-1",
				Status:     StatusCompleted,
				StartedAt:  started,
				FinishedAt: finished,
				PaneUsed:   "%1",
				AgentType:  "codex",
				Output:     "done",
			},
		},
		Variables: map[string]interface{}{
			"answer": "42",
		},
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got ExecutionState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v\nJSON:\n%s", err, data)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("ExecutionState JSON mismatch\nwant: %#v\n got: %#v\nJSON:\n%s", state, got, data)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal(raw) error = %v", err)
	}
	for _, key := range []string{"run_id", "workflow_id", "status", "started_at", "updated_at", "steps", "variables"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("ExecutionState JSON missing key %q in %s", key, data)
		}
	}
}
