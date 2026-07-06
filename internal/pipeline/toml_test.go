package pipeline

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestTOMLRoundTrip_NewSchemaTypes(t *testing.T) {
	workflow := Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "toml-new-schema-types",
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

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(workflow); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	var got Workflow
	md, err := toml.Decode(buf.String(), &got)
	if err != nil {
		t.Fatalf("Decode() error = %v\nTOML:\n%s", err, buf.String())
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		t.Fatalf("Decode() left undecoded keys: %v\nTOML:\n%s", undecoded, buf.String())
	}

	if !reflect.DeepEqual(got, workflow) {
		t.Fatalf("TOML round trip mismatch\nwant: %#v\n got: %#v\nTOML:\n%s", workflow, got, buf.String())
	}
}

func TestParseStringTOML_NewSchemaTypesCanonicalShape(t *testing.T) {
	var buf bytes.Buffer
	workflow := Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "canonical-toml-shape",
		Steps: []Step{
			{
				ID:     "pane",
				Prompt: "hello",
				Pane:   PaneSpec{Index: 1},
			},
			{
				ID: "foreach",
				Foreach: &ForeachConfig{
					Items:     "${vars.items}",
					Models:    StringOrList{"codex"},
					MaxRounds: IntOrExpr{Value: 2},
					Steps: []Step{
						{ID: "body", Prompt: "body"},
					},
				},
			},
		},
	}
	if err := toml.NewEncoder(&buf).Encode(workflow); err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	got, err := ParseString(buf.String(), "toml")
	if err != nil {
		t.Fatalf("ParseString() error = %v\nTOML:\n%s", err, buf.String())
	}
	if got.Steps[0].Pane != (PaneSpec{Index: 1}) {
		t.Fatalf("Pane = %#v, want Index=1", got.Steps[0].Pane)
	}
	if got.Steps[1].Foreach == nil {
		t.Fatal("Foreach = nil, want config")
	}
	if !reflect.DeepEqual(got.Steps[1].Foreach.Models, StringOrList{"codex"}) {
		t.Fatalf("Foreach.Models = %#v, want codex list", got.Steps[1].Foreach.Models)
	}
	if got.Steps[1].Foreach.MaxRounds != (IntOrExpr{Value: 2}) {
		t.Fatalf("Foreach.MaxRounds = %#v, want Value=2", got.Steps[1].Foreach.MaxRounds)
	}
}

func TestParseStringTOML_ScalarAlternationForms(t *testing.T) {
	tests := []struct {
		name    string
		content string
		assert  func(t *testing.T, got *Workflow)
	}{
		{
			name: "pane scalar",
			content: `
schema_version = "2.0"
name = "pane-scalar"

[[steps]]
id = "pane"
prompt = "hello"
pane = 3
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if got.Steps[0].Pane != (PaneSpec{Index: 3}) {
					t.Fatalf("Pane = %#v, want Index=3", got.Steps[0].Pane)
				}
			},
		},
		{
			name: "pane expression",
			content: `
schema_version = "2.0"
name = "pane-expression"

[[steps]]
id = "pane"
prompt = "hello"
pane = "${defaults.triage_pane}"
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if got.Steps[0].Pane != (PaneSpec{Expr: "${defaults.triage_pane}"}) {
					t.Fatalf("Pane = %#v, want expression", got.Steps[0].Pane)
				}
			},
		},
		{
			name: "parallel scalar",
			content: `
schema_version = "2.0"
name = "parallel-scalar"

[[steps]]
id = "parallel"
prompt = "hello"
parallel = true
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if !got.Steps[0].Parallel.Flag {
					t.Fatal("Parallel.Flag = false, want true")
				}
			},
		},
		{
			name: "after scalar",
			content: `
schema_version = "2.0"
name = "after-scalar"

[[steps]]
id = "after"
prompt = "hello"
after = "pane"
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if !reflect.DeepEqual(got.Steps[0].DependsOn, []string{"pane"}) {
					t.Fatalf("DependsOn = %#v, want pane after normalization", got.Steps[0].DependsOn)
				}
				if len(got.Steps[0].After) != 0 {
					t.Fatalf("After = %#v, want normalized empty alias", got.Steps[0].After)
				}
			},
		},
		{
			name: "notes scalar",
			content: `
schema_version = "2.0"
name = "notes-scalar"
notes = "single note"

[[steps]]
id = "step"
prompt = "hello"
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if !reflect.DeepEqual(got.Notes, StringOrList{"single note"}) {
					t.Fatalf("Notes = %#v, want single note list", got.Notes)
				}
			},
		},
		{
			name: "output bare string",
			content: `
schema_version = "2.0"
name = "output-string"
outputs = ["deliverables/report.md"]

[[steps]]
id = "step"
prompt = "hello"
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if len(got.Outputs) != 1 || got.Outputs[0].Path != "deliverables/report.md" {
					t.Fatalf("Outputs = %#v, want bare path output", got.Outputs)
				}
			},
		},
		{
			name: "output single key",
			content: `
schema_version = "2.0"
name = "output-single-key"
outputs = [{ workspace = "${workspace_path}" }]

[[steps]]
id = "step"
prompt = "hello"
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if len(got.Outputs) != 1 || got.Outputs[0].Name != "workspace" || got.Outputs[0].Path != "${workspace_path}" {
					t.Fatalf("Outputs = %#v, want single-key output", got.Outputs)
				}
			},
		},
		{
			name: "on failure retry shorthand",
			content: `
schema_version = "2.0"
name = "retry-shorthand"

[[steps]]
id = "step"
command = "false"
on_failure = "retry:2"
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				step := got.Steps[0]
				if step.OnError != ErrorActionRetry || step.RetryCount != 2 {
					t.Fatalf("OnError=%q RetryCount=%d, want retry:2", step.OnError, step.RetryCount)
				}
				if !step.OnFailure.IsZero() {
					t.Fatalf("OnFailure = %#v, want normalized zero", step.OnFailure)
				}
			},
		},
		{
			name: "loop max iterations scalar",
			content: `
schema_version = "2.0"
name = "max-iterations"

[[steps]]
id = "loop"

[steps.loop]
items = "${vars.items}"
max_iterations = 5
`,
			assert: func(t *testing.T, got *Workflow) {
				t.Helper()
				if got.Steps[0].Loop == nil || got.Steps[0].Loop.MaxIterations != (IntOrExpr{Value: 5}) {
					t.Fatalf("Loop = %#v, want MaxIterations Value=5", got.Steps[0].Loop)
				}
			},
		},
		{
			name: "foreach models and rounds scalar",
			content: `
schema_version = "2.0"
name = "foreach-scalars"

[[steps]]
id = "foreach"

[steps.foreach]
items = "${vars.items}"
models = "codex"
max_rounds = "${defaults.max_rounds}"
`,
			assert: func(t *testing.T, got *Workflow) {
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
			got, err := ParseString(tt.content, "toml")
			if err != nil {
				t.Fatalf("ParseString() error = %v", err)
			}
			tt.assert(t, got)
		})
	}
}

func TestParseStringTOML_RejectsParallelInlineStepArrays(t *testing.T) {
	tests := []string{
		`
schema_version = "2.0"
name = "parallel-inline-list"

[[steps]]
id = "parallel"
parallel = [{ id = "child", prompt = "hello" }]
`,
		`
schema_version = "2.0"
name = "parallel-inline-steps"

[[steps]]
id = "parallel"

[steps.parallel]
steps = [{ id = "child", prompt = "hello" }]
`,
	}

	for _, content := range tests {
		_, err := ParseString(content, "toml")
		if err == nil {
			t.Fatal("ParseString() error = nil, want inline step array limitation")
		}
		if !strings.Contains(err.Error(), "TOML inline step arrays are not supported") {
			t.Fatalf("ParseString() error = %v, want inline step array limitation", err)
		}
	}
}

// bd-k44ib: a TOML workflow that puts step-shaped fields directly under
// `[steps.parallel]` (a single table instead of `[[steps.parallel.steps]]`,
// an array of tables) used to be silently accepted with an empty parallel
// block, then fail validation with a misleading "step must have prompt, ..."
// error. The parser must surface the bad table shape directly.
func TestParseStringTOML_RejectsParallelTableWithStepFields(t *testing.T) {
	cases := map[string]string{
		"id_and_prompt_at_table_level": `
schema_version = "2.0"
name = "probe"

[[steps]]
id = "p"

[steps.parallel]
id = "child"
prompt = "hello"
`,
		"command_at_table_level": `
schema_version = "2.0"
name = "probe"

[[steps]]
id = "p"

[steps.parallel]
command = "echo hi"
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseString(content, "toml")
			if err == nil {
				t.Fatalf("ParseString() error = nil, want unexpected-key error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "[steps.parallel]") || !strings.Contains(msg, "[[steps.parallel.steps]]") {
				t.Fatalf("ParseString() error = %v, want hint pointing at the canonical array-of-tables form", err)
			}
		})
	}
}

// bd-k44ib: malformed `[steps.parallel.<unknown>]` subtables under a parallel
// step should not be silently swallowed by filterUndecodedTOMLKeys. Only the
// canonical `steps.parallel.steps.*` leftovers from inline-array decoding
// are suppressed.
func TestParseStringTOML_RejectsParallelUnknownSubtable(t *testing.T) {
	content := `
schema_version = "2.0"
name = "probe"

[[steps]]
id = "p"
prompt = "hi"

[steps.parallel.bogus]
nope = "yes"
`
	_, err := ParseString(content, "toml")
	if err == nil {
		t.Fatalf("ParseString() error = nil, want error for unknown steps.parallel subtable")
	}
}
