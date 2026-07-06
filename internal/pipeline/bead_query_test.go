package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBeadQuery_ParseYAMLAndValidate(t *testing.T) {
	content := `
schema_version: "2.0"
name: bead-query
steps:
  - id: collect
    bead_query:
      label: [hypothesis, phase-4]
      status: open
      filter: 'priority==1 && label==hypothesis'
    output_var: hypothesis_beads
`

	workflow, err := ParseString(content, "yaml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() failed: %+v", result.Errors)
	}

	query := workflow.Steps[0].BeadQuery
	if query == nil {
		t.Fatal("BeadQuery = nil")
	}
	if !reflect.DeepEqual(query.Label, StringOrList{"hypothesis", "phase-4"}) {
		t.Fatalf("BeadQuery.Label = %#v", query.Label)
	}
	if query.Status != "open" || query.Filter != "priority==1 && label==hypothesis" {
		t.Fatalf("BeadQuery = %#v", query)
	}
}

func TestBeadQuery_ParseTOMLKnownFields(t *testing.T) {
	content := `
schema_version = "2.0"
name = "bead-query-toml"

[[steps]]
id = "collect"
output_var = "hypothesis_beads"

[steps.bead_query]
label = "hypothesis"
status = "open"
filter = "status==open"
`

	workflow, err := ParseString(content, "toml")
	if err != nil {
		t.Fatalf("ParseString() error = %v", err)
	}
	if result := Validate(workflow); !result.Valid {
		t.Fatalf("Validate() failed: %+v", result.Errors)
	}
	if got := workflow.Steps[0].BeadQuery.Label; !reflect.DeepEqual(got, StringOrList{"hypothesis"}) {
		t.Fatalf("BeadQuery.Label = %#v", got)
	}
}

func TestBeadQuery_JSONRoundTrip(t *testing.T) {
	step := Step{
		ID: "collect",
		BeadQuery: &BeadQueryStep{
			Label:  StringOrList{"hypothesis"},
			Status: "open",
			Filter: "priority==1",
		},
		OutputVar: "beads",
	}

	data, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got Step
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v\nJSON:\n%s", err, data)
	}
	if !reflect.DeepEqual(got, step) {
		t.Fatalf("JSON round trip mismatch\nwant: %#v\n got: %#v\nJSON:\n%s", step, got, data)
	}
}

func TestBeadQuery_ValidationConflict(t *testing.T) {
	result := Validate(&Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-conflict",
		Steps: []Step{{
			ID:        "bad",
			Command:   "br list --json",
			BeadQuery: &BeadQueryStep{Label: StringOrList{"hypothesis"}},
		}},
	})
	if result.Valid {
		t.Fatal("Validate() succeeded, want conflict")
	}
	for _, err := range result.Errors {
		if strings.Contains(err.Message, "cannot combine bead_query") {
			return
		}
	}
	t.Fatalf("Validate() errors = %+v, want bead_query conflict", result.Errors)
}

func TestExecuteBeadQueryCapturesTypedRecords(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-exec",
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Label:  StringOrList{"hypothesis"},
				Status: "open",
				Filter: "status==open && label==hypothesis",
			},
			OutputVar: "hypothesis_beads",
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		t.Helper()
		wantArgs := []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "open"}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("br args = %#v, want %#v", args, wantArgs)
		}
		return []byte(`{"issues":[
			{"id":"bd-1","title":"One","description":"first","labels":["hypothesis","phase-4"],"status":"open","priority":1,"issue_type":"task"},
			{"id":"bd-2","title":"Two","labels":["other"],"status":"open","priority":1,"issue_type":"task"},
			{"id":"bd-3","title":"Three","labels":["hypothesis"],"status":"closed","priority":1,"issue_type":"task"}
		]}`), nil
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	result := state.Steps["collect"]
	records, ok := result.ParsedData.([]BeadRecord)
	if !ok {
		t.Fatalf("ParsedData = %T, want []BeadRecord", result.ParsedData)
	}
	if len(records) != 1 || records[0].ID != "bd-1" || records[0].Title != "One" {
		t.Fatalf("records = %#v", records)
	}

	var output []BeadRecord
	if err := json.Unmarshal([]byte(result.Output), &output); err != nil {
		t.Fatalf("result.Output is not BeadRecord JSON: %v\n%s", err, result.Output)
	}
	if !reflect.DeepEqual(output, records) {
		t.Fatalf("output records = %#v, want %#v", output, records)
	}
	if got := state.Variables["hypothesis_beads"]; got != result.Output {
		t.Fatalf("output var = %#v, want %#v", got, result.Output)
	}
	if got := state.Variables["hypothesis_beads_parsed"]; !reflect.DeepEqual(got, records) {
		t.Fatalf("parsed output var = %#v, want %#v", got, records)
	}
}

func TestBeadQueryFilterOperators(t *testing.T) {
	records := []BeadRecord{
		{ID: "bd-1", Labels: []string{"hypothesis"}, Status: "open", Priority: 1},
		{ID: "bd-2", Labels: []string{"question"}, Status: "open", Priority: 2},
		{ID: "bd-3", Labels: []string{"hypothesis"}, Status: "closed", Priority: 2},
	}

	got, err := filterBeadRecords(records, `label==hypothesis && status!=closed || id=="bd-2"`)
	if err != nil {
		t.Fatalf("filterBeadRecords() error = %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, record := range got {
		ids = append(ids, record.ID)
	}
	if !reflect.DeepEqual(ids, []string{"bd-1", "bd-2"}) {
		t.Fatalf("filtered ids = %#v", ids)
	}
}

func TestBeadQueryFilterRejectsParens(t *testing.T) {
	// bd-3at8h: the string-split parser cannot honor parens, so silently
	// chopping `(label==hypothesis || label==phase-4)` into clauses with
	// the parens attached (`(label==hypothesis` / `label==phase-4)`) used
	// to either return an unknown-field error or, worse, the wrong result
	// set. Reject parens up front with a clear hint instead of producing
	// a misleading match. (Replacing matchBeadFilter with the foreach
	// filter parser is tracked for a follow-up.)
	records := []BeadRecord{
		{ID: "bd-1", Labels: []string{"hypothesis"}, Status: "open"},
	}
	_, err := filterBeadRecords(records, `status==open && (label==hypothesis || label==phase-4)`)
	if err == nil {
		t.Fatal("filterBeadRecords() error = nil, want parens-rejection error")
	}
	if !strings.Contains(err.Error(), "parenthesized expressions") {
		t.Fatalf("filterBeadRecords() error = %q, want parens hint", err.Error())
	}
}

func TestBeadQuerySubstitutesVariablesInArgsAndFilter(t *testing.T) {
	// bead_query is meant to replace shell-piped br|jq commands. Without
	// variable substitution, fields like label/status/filter would be sent
	// verbatim (literal "${vars.target_label}") to br, returning the wrong
	// result set with no diagnostic.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-vars",
		Vars: map[string]VarDef{
			"target_label":    {Type: VarTypeString, Default: "hypothesis"},
			"target_state":    {Type: VarTypeString, Default: "open"},
			"target_priority": {Type: VarTypeString, Default: "1"},
		},
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Label:    StringOrList{"${vars.target_label}"},
				Status:   "${vars.target_state}",
				Priority: "${vars.target_priority}",
				Filter:   "status==${vars.target_state}",
			},
			OutputVar: "beads",
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		t.Helper()
		wantArgs := []string{"list", "--json", "--limit", "0", "--label", "hypothesis", "--status", "open", "--priority", "1"}
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("br args = %#v, want %#v", args, wantArgs)
		}
		return []byte(`{"issues":[
			{"id":"bd-1","status":"open","labels":["hypothesis"],"priority":1,"issue_type":"task"},
			{"id":"bd-2","status":"closed","labels":["hypothesis"],"priority":1,"issue_type":"task"}
		]}`), nil
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	records, ok := state.Steps["collect"].ParsedData.([]BeadRecord)
	if !ok {
		t.Fatalf("ParsedData = %T, want []BeadRecord", state.Steps["collect"].ParsedData)
	}
	// Filter "status==open" must apply against the resolved value, leaving
	// only bd-1.
	if len(records) != 1 || records[0].ID != "bd-1" {
		t.Fatalf("filtered records = %#v, want one record bd-1", records)
	}
}

func TestBeadQueryFailsOnUnresolvedVariable(t *testing.T) {
	// A typo'd reference must fail the step rather than silently sending the
	// literal "${vars.unknown}" to br.
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-unresolved",
		Steps: []Step{{
			ID: "collect",
			BeadQuery: &BeadQueryStep{
				Label: StringOrList{"${vars.unknown}"},
			},
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		t.Fatalf("br must not be called when variable substitution fails; args=%v", args)
		return nil, nil
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want substitution failure")
	}
	stepResult := state.Steps["collect"]
	if stepResult.Status != StatusFailed {
		t.Fatalf("Status = %v, want %v", stepResult.Status, StatusFailed)
	}
	if stepResult.Error == nil || !strings.Contains(stepResult.Error.Message, "variable substitution failed") {
		t.Fatalf("step error = %#v, want substitution failure", stepResult.Error)
	}
}

func TestBeadQueryBrErrorFailsStep(t *testing.T) {
	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "bead-query-error",
		Steps: []Step{{
			ID:        "collect",
			BeadQuery: &BeadQueryStep{Label: StringOrList{"hypothesis"}},
		}},
	}

	config := DefaultExecutorConfig("session")
	config.BeadQueryRunBr = func(ctx context.Context, args []string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}

	executor := NewExecutor(config)
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want bead_query failure")
	}
	if got := state.Steps["collect"].Error; got == nil || !strings.Contains(got.Message, "bead_query") {
		t.Fatalf("step error = %#v, want bead_query error", got)
	}
}

// TestMatchBeadFilterClause_OperatorByFirstOccurrence covers bd-o4ji9:
// when both `==` and `!=` appear in a clause, the operator is the one
// that appears first. Previously `==` was always preferred, so a clause
// like `priority!=1==broken` parsed `priority!=1` as the field and
// failed with "unknown bead field".
func TestMatchBeadFilterClause_OperatorByFirstOccurrence(t *testing.T) {
	record := BeadRecord{
		ID:       "bd-test",
		Priority: 1,
	}
	cases := []struct {
		clause  string
		want    bool
		wantErr bool
	}{
		{clause: "priority!=2", want: true},
		{clause: "priority==1", want: true},
		// Earliest operator wins: with `!=` first, the LHS is `priority`
		// and the RHS is the literal `1==something`. priority field is
		// "1", so "priority != 1==something" → not equal, true.
		{clause: "priority!=1==something", want: true},
		// `==` first, LHS is `priority`, RHS is `2!=junk`. priority is
		// "1", so equality with literal "2!=junk" → false.
		{clause: "priority==2!=junk", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			got, err := matchBeadFilterClause(record, tc.clause)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("matchBeadFilterClause(%q) err = nil, want error", tc.clause)
				}
				return
			}
			if err != nil {
				t.Fatalf("matchBeadFilterClause(%q) err = %v", tc.clause, err)
			}
			if got != tc.want {
				t.Errorf("matchBeadFilterClause(%q) = %v, want %v", tc.clause, got, tc.want)
			}
		})
	}
}

// TestMatchBeadFilterClause_LabelCaseInsensitive covers bd-6uylc: the
// `label`/`labels` list-membership branch was case-sensitive on the field
// name, so `Label==alpha` silently returned false even when the record had
// `alpha` in record.Labels (because beadRecordField returned the joined
// string and the special-case branch didn't fire). Field name comparison
// is now case-insensitive, matching beadRecordField.
// TestExecuteBeadQuery_DryRun_SanitizesControlBytes covers bd-g3mfx: the
// dry-run banner emitted by executeBeadQuery must scrub ANSI/OSC/C0 control
// bytes from the substituted args. bead_query scalar fields support
// ${steps.X.output}, so an upstream agent's output can flow into the
// joined args and reach the operator's terminal during --dry-run unless
// sanitized first. Same attack class as bd-82zsc (command) and bd-g40ad
// (agent/parallel/branch). Each control byte must round-trip as '?'.
func TestExecuteBeadQuery_DryRun_SanitizesControlBytes(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("bead-query-dryrun-sanitize")
	cfg.ProjectDir = tmpDir
	cfg.DryRun = true
	e := NewExecutor(cfg)
	e.state = &ExecutionState{
		RunID:      "run-bq-sanitize",
		WorkflowID: "wf",
		Variables:  map[string]interface{}{},
		Steps: map[string]StepResult{
			"evil": {Output: "\x1b[2J\x1b[H\x07payload"},
		},
	}

	step := &Step{
		ID: "dryrun-bq",
		BeadQuery: &BeadQueryStep{
			// Status flows into brListArgs via "--status <value>", so the
			// substituted bytes reach the joined dry-run banner. Filter is
			// in-memory only and never appears in args, so it would not
			// exercise the banner sanitization path.
			Status: "${steps.evil.output}",
		},
	}
	result := e.executeBeadQuery(context.Background(), step, &Workflow{Name: "test"})

	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q; error = %+v", result.Status, StatusCompleted, result.Error)
	}
	for _, b := range []byte(result.Output) {
		if b == '\x1b' || b == '\x07' || b == '\x00' {
			t.Fatalf("dry-run bead_query banner contains unsanitized control byte 0x%02x: %q", b, result.Output)
		}
	}
	if !strings.Contains(result.Output, "payload") {
		t.Errorf("dry-run bead_query banner dropped the trailing payload after sanitizing controls: %q", result.Output)
	}
	if !strings.Contains(result.Output, "Would run br") {
		t.Errorf("dry-run bead_query banner missing canonical prefix: %q", result.Output)
	}
}

func TestMatchBeadFilterClause_LabelCaseInsensitive(t *testing.T) {
	record := BeadRecord{
		ID:     "bd-test",
		Labels: []string{"alpha", "beta", "gamma"},
	}
	cases := []struct {
		clause string
		want   bool
	}{
		{"label==alpha", true},
		{"Label==alpha", true},
		{"LABEL==alpha", true},
		{"labels==beta", true},
		{"Labels==beta", true},
		{"label==zeta", false},
		{"Label!=alpha", false},
		{"Label!=zeta", true},
	}
	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			got, err := matchBeadFilterClause(record, tc.clause)
			if err != nil {
				t.Fatalf("matchBeadFilterClause(%q) err = %v", tc.clause, err)
			}
			if got != tc.want {
				t.Errorf("matchBeadFilterClause(%q) = %v, want %v", tc.clause, got, tc.want)
			}
		})
	}
}
