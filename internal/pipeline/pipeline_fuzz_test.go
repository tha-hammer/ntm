package pipeline

// Fuzz suite for pipeline parsing, variable substitution, and filter
// expression evaluation. Owns bd-7ramj.17.
//
// Fuzz targets follow Go 1.18+ native fuzzing. Seed corpora live inline via
// f.Add so the file is self-contained and reproducible without external
// fixtures. Run a quick fuzz pass with:
//
//	go test -run='^Fuzz' ./internal/pipeline/...                # seed corpus only
//	go test -fuzz=FuzzParseWorkflow -fuzztime=30s ./internal/pipeline/...
//	go test -fuzz=FuzzSubstituteVariables -fuzztime=30s ./internal/pipeline/...
//	go test -fuzz=FuzzFilterExpr -fuzztime=30s ./internal/pipeline/...
//
// All targets share one acceptance contract: never panic for any input. Errors
// are valid outputs; panics are bugs.

import (
	"strings"
	"testing"
	"time"
)

// ----------------------------------------------------------------------------
// FuzzParseWorkflow
// ----------------------------------------------------------------------------

// fuzzParseSeeds is a small corpus of representative shapes. Heavier fixtures
// (real brennerbot workflows) live in e2e/ and are out of scope for the fuzz
// seed corpus per the AGENTS.md no-file-proliferation rule.
var fuzzParseSeeds = []string{
	// Minimal valid workflow.
	`schema_version: "v2.0"
name: minimal
steps:
  - id: a
    prompt: hello
`,
	// Parallel + foreach + loop in one workflow.
	`schema_version: "v2.0"
name: shapes
defaults:
  retry_count: 1
steps:
  - id: fanout
    parallel:
      - id: p1
        prompt: one
      - id: p2
        prompt: two
  - id: iterate
    loop:
      items: "${data}"
      as: row
      steps:
        - id: do
          prompt: process ${loop.row}
  - id: spread
    foreach:
      items: "${things}"
      steps:
        - id: handle
          prompt: handle ${item}
`,
	// Aliases that Normalize() collapses (after / body / template_params).
	`schema_version: "v2.0"
name: aliases
steps:
  - id: first
    prompt: a
  - id: second
    prompt: b
    after: [first]
  - id: third
    template: tpl
    template_params:
      x: 1
`,
	// Edge case: deeply nested mappings (still bounded).
	`schema_version: "v2.0"
name: deep
vars:
  outer:
    middle:
      inner:
        leaf: value
steps:
  - id: x
    prompt: ${vars.outer.middle.inner.leaf}
`,
	// Empty / pathological inputs that should surface clean errors, not panic.
	``,
	`---`,
	`name: only-name`,
	`schema_version: v2.0
steps:
  - {}`,
	// Toml shape (we'll feed both formats to the parser).
	`schema_version = "v2.0"
name = "toml-min"

[[steps]]
id = "a"
prompt = "hello"
`,
}

func FuzzParseWorkflow(f *testing.F) {
	for _, s := range fuzzParseSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, content string) {
		// Bound input length so a single fuzz iteration cannot hang the parser
		// on a multi-megabyte string.
		if len(content) > 64*1024 {
			t.Skip("oversize input")
		}

		// Try both formats. Either MAY return an error; neither MAY panic.
		for _, format := range []string{"yaml", "toml"} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("ParseString(%q) panicked: %v\ninput=%q", format, r, truncate(content, 200))
					}
				}()
				w, err := ParseString(content, format)
				if err == nil && w != nil {
					// Normalize is idempotent; calling it twice must not panic
					// or change shape (we only check no-panic here; full
					// idempotency is bd-7ramj.4's contract).
					w.Normalize()
				}
			}()
		}
	})
}

// ----------------------------------------------------------------------------
// FuzzSubstituteVariables
// ----------------------------------------------------------------------------

func newFuzzSubstitutor() *Substitutor {
	state := &ExecutionState{
		RunID:      "fuzz-run",
		WorkflowID: "fuzz",
		Status:     StatusRunning,
		StartedAt:  time.Now(),
		Steps: map[string]StepResult{
			"step_a": {StepID: "step_a", Status: StatusCompleted, Output: "ok"},
			"step_b": {
				StepID:     "step_b",
				Status:     StatusCompleted,
				Output:     "done",
				ParsedData: map[string]interface{}{"count": 7, "label": "alpha"},
			},
		},
		Variables: map[string]interface{}{
			"name":    "fuzz",
			"count":   3,
			"flag":    true,
			"nested":  map[string]interface{}{"key": "value", "deeper": map[string]interface{}{"x": 1}},
			"list":    []interface{}{"a", "b", "c"},
			"empty":   "",
			"zero":    0,
			"falseV":  false,
			"unicode": "héllo ω",
			// loop scope keys so ${loop.X} resolves cleanly.
			"loop.item":  "first",
			"loop.index": 0,
			"loop.count": 3,
		},
	}
	sub := NewSubstitutor(state, "fuzz-session", "fuzz")
	sub.SetDefaults(map[string]interface{}{
		"timeout":  "30s",
		"max_step": 10,
	})
	return sub
}

var fuzzSubstituteSeeds = []string{
	"",
	"plain text",
	"${name}",
	"${vars.name}",
	"${vars.nested.key}",
	"${vars.nested.deeper.x}",
	"${vars.list}",
	"${vars.absent | \"fallback\"}",
	"${vars.absent | fallback }",
	"${steps.step_a.output}",
	"${steps.step_b.parsed_data.count}",
	"${steps.step_b.parsed_data.label}",
	"${defaults.timeout}",
	"${env.PATH | \"none\"}",
	"${loop.item} #${loop.index}",
	"\\${literal}",
	"${${nested}}",
	"${ }",
	"${",
	"${unterminated | ",
	"$",
	"prefix ${name} suffix ${count}",
	"${vars.unicode} ${name}",
	"${a.b.c.d.e.f.g.h.i.j.k.l.m.n.o.p}",
	"${" + strings.Repeat("a.", 200) + "z}",
	strings.Repeat("${name} ", 100),
}

func FuzzSubstituteVariables(f *testing.F) {
	for _, s := range fuzzSubstituteSeeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, template string) {
		if len(template) > 32*1024 {
			t.Skip("oversize template")
		}

		sub := newFuzzSubstitutor()

		// Substitute MAY return an error. It MAY NOT panic.
		// It MAY NOT run forever (Substitutor enforces a recursion depth cap;
		// if a future regression removes that cap, this test will time out
		// rather than wedge the fuzzer because go test enforces -timeout).
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Substitute panicked: %v\ntemplate=%q", r, truncate(template, 200))
				}
			}()
			out, _ := sub.Substitute(template)

			// Output well-formedness sanity check: it must not contain a
			// half-substituted ${...} marker that the original template did
			// not contain. If the input had no ${ but the output does, the
			// substitutor invented a marker.
			if !strings.Contains(template, "${") && strings.Contains(out, "${") {
				t.Fatalf("substitutor introduced ${ marker absent from input\ntemplate=%q\noutput=%q",
					truncate(template, 200), truncate(out, 200))
			}
		}()

		// SubstituteStrict is the more aggressive variant — it must also
		// never panic.
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("SubstituteStrict panicked: %v\ntemplate=%q", r, truncate(template, 200))
				}
			}()
			_, _ = sub.SubstituteStrict(template)
		}()
	})
}

// ----------------------------------------------------------------------------
// FuzzFilterExpr
// ----------------------------------------------------------------------------

var fuzzFilterSeeds = []string{
	"",
	"role==proposer",
	"role!=proposer",
	"state==active && model!=cc",
	"state==active || model==cc",
	"(role==proposer) && (state==active)",
	"((role==proposer))",
	"role==\"value with spaces\"",
	"role==",
	"==value",
	"(((((",
	")))))",
	"&&",
	"||",
	"a==b && c==d || e==f",
	"role==proposer && (state==active || model!=cc)",
	"role == proposer",
	strings.Repeat("(", 100) + "a==b" + strings.Repeat(")", 100),
	strings.Repeat("a==b && ", 100) + "x==y",
}

func FuzzFilterExpr(f *testing.F) {
	for _, s := range fuzzFilterSeeds {
		f.Add(s)
	}

	ctx := FilterContext{
		Item: map[string]interface{}{
			"state":  "active",
			"model":  "cod",
			"weight": 3,
			"role":   "proposer",
		},
		Pane: map[string]interface{}{
			"role":  "proposer",
			"index": 0,
			"id":    "%5",
		},
	}

	f.Fuzz(func(t *testing.T, expr string) {
		if len(expr) > 8*1024 {
			t.Skip("oversize expression")
		}

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("EvaluateForeachFilter panicked: %v\nexpr=%q", r, truncate(expr, 200))
			}
		}()
		_, _ = EvaluateForeachFilter(expr, ctx)
	})
}

// ----------------------------------------------------------------------------

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(" + itoaLen(len(s)-n) + " more)"
}

func itoaLen(n int) string {
	// Tiny helper to avoid pulling fmt into the hot fuzz path; values are
	// only used for failure messages.
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
