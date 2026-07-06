package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func BenchmarkSubstituteVariables(b *testing.B) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bench-substitute", Settings: DefaultWorkflowSettings()}
	executor := createBenchmarkExecutor(workflow)
	executor.state.Variables["one"] = "alpha"
	executor.state.Variables["nested"] = map[string]interface{}{"path": map[string]interface{}{"value": "omega"}}
	executor.state.Variables["pane"] = map[string]interface{}{"model": "codex", "index": 3}
	SetLoopVars(executor.state, "item", map[string]interface{}{"id": "H-001", "score": 42}, 7, 100)

	var manyVars string
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("v%d", i)
		executor.state.Variables[key] = i
		manyVars += fmt.Sprintf("${vars.%s}", key)
	}

	cases := map[string]string{
		"one_var":                "value=${vars.one}",
		"five_vars":              "${vars.one}:${loop.index}:${loop.count}:${pane.model}:${item.id}",
		"fifty_vars":             manyVars,
		"nested_path":            "${vars.nested.path.value}",
		"pane_field":             "${pane.model}/${pane.index}",
		"item_field":             "${item.id}/${item.score}",
		"no_substitution_needed": "plain text with no template markers",
	}

	for name, template := range cases {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = executor.substituteVariables(template)
			}
		})
	}
}

func BenchmarkForeachIteration(b *testing.B) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bench-foreach", Settings: DefaultWorkflowSettings()}
	items := make([]map[string]interface{}, 100)
	for i := range items {
		items[i] = map[string]interface{}{"id": fmt.Sprintf("H-%03d", i), "score": i}
	}
	rawItems, err := json.Marshal(items)
	if err != nil {
		b.Fatal(err)
	}

	b.Run("sequential_100", func(b *testing.B) {
		benchmarkForeachIteration(b, workflow, string(rawItems), false)
	})
	b.Run("parallel_100_max8", func(b *testing.B) {
		benchmarkForeachIteration(b, workflow, string(rawItems), true)
	})
}

func benchmarkForeachIteration(b *testing.B, workflow *Workflow, items string, parallel bool) {
	step := &Step{
		ID: "fanout",
		Foreach: &ForeachConfig{
			Items:         items,
			Parallel:      parallel,
			MaxConcurrent: 8,
			Steps: []Step{{
				ID:          "continue",
				LoopControl: LoopControlContinue,
			}},
		},
	}
	workflow.Steps = []Step{*step}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		executor := createBenchmarkExecutor(workflow)
		result := executor.executeForeach(context.Background(), step, workflow)
		if result.Status != StatusCompleted {
			b.Fatalf("foreach status = %s, error = %#v", result.Status, result.Error)
		}
	}
}

func BenchmarkStepDispatch(b *testing.B) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bench-dispatch", Settings: DefaultWorkflowSettings()}
	step := &Step{
		ID:          "dispatch_continue",
		LoopControl: LoopControlContinue,
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		executor := createBenchmarkExecutor(workflow)
		result := executor.executeForeachNestedStepOnce(context.Background(), step, workflow)
		if result.Status != StatusFailed {
			b.Fatalf("dispatch status = %s, want failed prompt resolution for sentinel step", result.Status)
		}
	}
}

func BenchmarkPaneMetadataLookup(b *testing.B) {
	panes := []tmux.Pane{
		{ID: "%1", Index: 1, NTMIndex: 1, Type: tmux.AgentCodex, Variant: "codex", Tags: []string{"model=codex", "domain=api,db"}},
		{ID: "%2", Index: 2, NTMIndex: 2, Type: tmux.AgentClaude, Variant: "opus", Tags: []string{"model=opus", "domain=docs"}},
		{ID: "%3", Index: 3, NTMIndex: 3, Type: tmux.AgentGemini, Variant: "gemini", Tags: []string{"model=gemini", "productive_ignorance=true"}},
	}

	b.Run("cache_miss_load", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			cache, err := LoadPaneMetadataCache(NewMockTmuxClient(panes...), "bench", "")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := cache.Lookup("%2"); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("cache_hit_lookup", func(b *testing.B) {
		cache, err := LoadPaneMetadataCache(NewMockTmuxClient(panes...), "bench", "")
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := cache.Lookup("2"); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("tmux_pane_to_vars", func(b *testing.B) {
		pane := tmux.Pane{ID: "%4", Index: 4, NTMIndex: 4, Type: tmux.AgentCodex, Variant: "codex", Tags: []string{"model=codex", "domain=infra,ops"}}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			vars := paneMetadataFromTmuxPane(pane).variableMap()
			if vars["domain"] != "infra" {
				b.Fatalf("domain = %v, want infra", vars["domain"])
			}
		}
	})
}

func BenchmarkTemplateDispatchFastPath(b *testing.B) {
	workflow := &Workflow{SchemaVersion: SchemaVersion, Name: "bench-template-dispatch", Settings: DefaultWorkflowSettings()}
	step := &Step{
		ID:       "template_missing",
		Template: "missing-template.md",
		Pane:     PaneSpec{Index: 1},
		Timeout:  Duration{Duration: time.Second},
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		executor := createBenchmarkExecutor(workflow)
		result := executor.executeTemplate(context.Background(), step, workflow)
		if result.Status != StatusFailed {
			b.Fatalf("template status = %s, want failed missing template", result.Status)
		}
	}
}

func createBenchmarkExecutor(workflow *Workflow) *Executor {
	cfg := DefaultExecutorConfig("bench")
	cfg.DefaultTimeout = 2 * time.Second
	executor := NewExecutor(cfg)
	executor.graph = NewDependencyGraph(workflow)
	executor.state = &ExecutionState{
		RunID:      "bench-run",
		WorkflowID: workflow.Name,
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps:      make(map[string]StepResult),
		Variables:  make(map[string]interface{}),
	}
	executor.defaults = workflow.Defaults
	executor.limits = workflow.Settings.Limits.EffectiveLimits()
	return executor
}
