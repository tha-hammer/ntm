package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestResolveAddAgentCommandTemplate_Ollama(t *testing.T) {

	oldCfg := cfg
	defer func() {
		cfg = oldCfg
	}()

	cfg = config.Default()
	cfg.Agents.Ollama = "ollama run {{shellQuote (.Model | default \"codellama:latest\")}}"

	cmd, env, err := resolveAddAgentCommandTemplate(AgentTypeOllama, nil, "http://127.0.0.1:11434")
	if err != nil {
		t.Fatalf("resolveAddAgentCommandTemplate() error = %v", err)
	}
	if cmd != cfg.Agents.Ollama {
		t.Fatalf("resolveAddAgentCommandTemplate() cmd = %q, want %q", cmd, cfg.Agents.Ollama)
	}
	if env["OLLAMA_HOST"] != "http://127.0.0.1:11434" {
		t.Fatalf("resolveAddAgentCommandTemplate() env OLLAMA_HOST = %q", env["OLLAMA_HOST"])
	}
}

func TestNewAddCmd_RegistersOllamaFlag(t *testing.T) {

	cmd := newAddCmd()
	if cmd.Flags().Lookup("ollama") == nil {
		t.Fatal("expected add command to register --ollama")
	}
}

// TestAddThreadsReasoningEffort is the ntm#195 regression guard. The `add`
// command parses the `:effort` segment of `--cc=N:model:effort` into the
// AgentSpec/FlatAgent, but runAdd previously omitted ReasoningEffort from the
// AgentTemplateVars handed to GenerateAgentCommand. The Claude template only
// emits `--effort` under `{{if .ReasoningEffort}}`, so the segment was
// silently dropped and the added pane launched at the CLI default — the same
// class of bug fixed for `spawn` in ntm#188. This drives the real
// parse→Flatten→render path the add loop uses and asserts the effort flows
// through, with a negative control proving an unset effort leaves no flag.
func TestAddThreadsReasoningEffort(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// Parse exactly as the --cc flag would, then flatten to the per-pane agent
	// the runAdd loop iterates over.
	spec, err := ParseAgentSpec("1:claude-opus-4-8:xhigh")
	if err != nil {
		t.Fatalf("ParseAgentSpec error = %v", err)
	}
	spec.Type = AgentTypeClaude
	flat := AgentSpecs{spec}.Flatten()
	if len(flat) != 1 {
		t.Fatalf("Flatten() len = %d, want 1", len(flat))
	}
	agent := flat[0]
	if agent.ReasoningEffort != "xhigh" {
		t.Fatalf("FlatAgent.ReasoningEffort = %q, want xhigh", agent.ReasoningEffort)
	}

	// Mirror runAdd's render: thread the flattened agent's effort into the vars.
	withEffort, err := config.GenerateAgentCommand(cfg.Agents.Claude, config.AgentTemplateVars{
		Model:           ResolveModel(agent.Type, agent.Model),
		ReasoningEffort: agent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (with effort) error = %v", err)
	}
	// The Claude template shell-quotes the value: `--effort 'xhigh'`.
	if !strings.Contains(withEffort, "--effort 'xhigh'") {
		t.Errorf("add render dropped reasoning effort: got %q, want it to contain %q", withEffort, "--effort 'xhigh'")
	}

	// Negative control: no effort parsed → no dangling --effort flag.
	noEffortSpec, err := ParseAgentSpec("1:claude-opus-4-8")
	if err != nil {
		t.Fatalf("ParseAgentSpec (no effort) error = %v", err)
	}
	noEffortSpec.Type = AgentTypeClaude
	noEffortAgent := AgentSpecs{noEffortSpec}.Flatten()[0]
	noEffort, err := config.GenerateAgentCommand(cfg.Agents.Claude, config.AgentTemplateVars{
		Model:           ResolveModel(noEffortAgent.Type, noEffortAgent.Model),
		ReasoningEffort: noEffortAgent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (no effort) error = %v", err)
	}
	if strings.Contains(noEffort, "--effort") {
		t.Errorf("unset effort left a dangling flag: %q", noEffort)
	}
}

// TestAddThreadsCodexReasoningEffort is the ntm#208 regression guard. Issue
// #208 reproduced against v1.18.3 (commit 6615dd7), which predates the ntm#195
// `add` fix: `ntm add --cod=1:MODEL:EFFORT` parsed the third spec field but
// runAdd handed GenerateAgentCommand an empty ReasoningEffort, so the default
// Codex template's `{{.ReasoningEffort | default "xhigh"}}` always emitted the
// fallback rather than the requested effort. This drives the real
// parse(--cod=1:model:low)→Flatten→render path the add loop uses against the
// default Codex template and asserts the requested effort reaches
// `model_reasoning_effort='low'`, with a negative control proving an unset
// effort falls back to the template default.
func TestAddThreadsCodexReasoningEffort(t *testing.T) {
	oldCfg := cfg
	defer func() { cfg = oldCfg }()
	cfg = config.Default()

	// Parse exactly as the --cod flag would, then flatten to the per-pane agent
	// the runAdd loop iterates over.
	spec, err := ParseAgentSpec("1:gpt-5.3-codex-spark:low")
	if err != nil {
		t.Fatalf("ParseAgentSpec error = %v", err)
	}
	spec.Type = AgentTypeCodex
	flat := AgentSpecs{spec}.Flatten()
	if len(flat) != 1 {
		t.Fatalf("Flatten() len = %d, want 1", len(flat))
	}
	agent := flat[0]
	if agent.ReasoningEffort != "low" {
		t.Fatalf("FlatAgent.ReasoningEffort = %q, want low", agent.ReasoningEffort)
	}

	// Mirror runAdd's render: thread the flattened agent's effort into the vars.
	withEffort, err := config.GenerateAgentCommand(cfg.Agents.Codex, config.AgentTemplateVars{
		Model:           ResolveModel(agent.Type, agent.Model),
		ReasoningEffort: agent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (with effort) error = %v", err)
	}
	// The Codex template shell-quotes the value: `model_reasoning_effort='low'`.
	if !strings.Contains(withEffort, "model_reasoning_effort='low'") {
		t.Errorf("add render dropped Codex reasoning effort: got %q, want it to contain %q", withEffort, "model_reasoning_effort='low'")
	}

	// Negative control: no effort parsed → template default (not 'low').
	noEffortSpec, err := ParseAgentSpec("1:gpt-5.3-codex-spark")
	if err != nil {
		t.Fatalf("ParseAgentSpec (no effort) error = %v", err)
	}
	noEffortSpec.Type = AgentTypeCodex
	noEffortAgent := AgentSpecs{noEffortSpec}.Flatten()[0]
	noEffort, err := config.GenerateAgentCommand(cfg.Agents.Codex, config.AgentTemplateVars{
		Model:           ResolveModel(noEffortAgent.Type, noEffortAgent.Model),
		ReasoningEffort: noEffortAgent.ReasoningEffort,
	})
	if err != nil {
		t.Fatalf("GenerateAgentCommand (no effort) error = %v", err)
	}
	if strings.Contains(noEffort, "model_reasoning_effort='low'") {
		t.Errorf("unset effort should not render low: %q", noEffort)
	}
}

// TestSeedMaxIndicesFromPanes pins Behavior 2 of the add-pane-identity plan:
// next-index seeding reads the resolved Type/NTMIndex fields GetPanes already
// populated from the @ntm_* identity vars, unioned with the legacy title
// parse, so an Agent Mail-titled pane (e.g. RubyDog) can never have its live
// index re-issued.
func TestSeedMaxIndicesFromPanes(t *testing.T) {
	tests := []struct {
		name  string
		panes []tmux.Pane
		want  map[string]int
	}{
		{
			name:  "identity-backed Agent Mail title",
			panes: []tmux.Pane{{Title: "RubyDog", Type: tmux.AgentClaude, NTMIndex: 1}},
			want:  map[string]int{"cc": 1},
		},
		{
			name:  "title-only legacy",
			panes: []tmux.Pane{{Title: "proj__cc_3"}},
			want:  map[string]int{"cc": 3},
		},
		{
			name: "mixed identity and legacy title",
			panes: []tmux.Pane{
				{Title: "proj__cc_2", Type: tmux.AgentClaude, NTMIndex: 2},
				{Title: "RubyDog", Type: tmux.AgentClaude, NTMIndex: 1},
				{Title: "proj__cc_3"},
				{Title: "user shell", Type: tmux.AgentUser},
				{Title: "custom-title"},
			},
			want: map[string]int{"cc": 3},
		},
		{
			name:  "user pane contributes nothing",
			panes: []tmux.Pane{{Title: "user shell", Type: tmux.AgentUser, NTMIndex: 5}},
			want:  map[string]int{},
		},
		{
			name:  "unresolvable title contributes nothing",
			panes: []tmux.Pane{{Title: "custom-title"}},
			want:  map[string]int{},
		},
		{
			name:  "identity index zero falls back to title",
			panes: []tmux.Pane{{Title: "proj__cc_2", Type: tmux.AgentClaude, NTMIndex: 0}},
			want:  map[string]int{"cc": 2},
		},
		{
			name:  "identity type canonicalized",
			panes: []tmux.Pane{{Title: "RubyDog", Type: tmux.AgentType("claude"), NTMIndex: 4}},
			want:  map[string]int{"cc": 4},
		},
		{
			name: "duplicate same-index panes keep max once",
			panes: []tmux.Pane{
				{Title: "RubyDog", Type: tmux.AgentClaude, NTMIndex: 2},
				{Title: "BlueCat", Type: tmux.AgentClaude, NTMIndex: 2},
			},
			want: map[string]int{"cc": 2},
		},
		{
			name:  "empty pane list",
			panes: nil,
			want:  map[string]int{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := seedMaxIndices(tc.panes)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("seedMaxIndices() = %v, want %v", got, tc.want)
			}
		})
	}

	// Next index issued is seed+1: the mixed session issues 4, and a session
	// whose only cc pane is RubyDog (cc,1) issues 2, never 1.
	mixed := seedMaxIndices([]tmux.Pane{
		{Title: "proj__cc_2", Type: tmux.AgentClaude, NTMIndex: 2},
		{Title: "RubyDog", Type: tmux.AgentClaude, NTMIndex: 1},
		{Title: "proj__cc_3"},
	})
	if next := mixed["cc"] + 1; next != 4 {
		t.Errorf("next cc index for mixed session = %d, want 4", next)
	}
	rubyOnly := seedMaxIndices([]tmux.Pane{
		{Title: "RubyDog", Type: tmux.AgentClaude, NTMIndex: 1},
	})
	if next := rubyOnly["cc"] + 1; next != 2 {
		t.Errorf("next cc index for RubyDog-only session = %d, want 2", next)
	}
}

// TestPaneIdentityArgsForAdd pins Behavior 1 of the add-pane-identity plan:
// the identity args for an added pane are the canonical type, the assigned
// index, and the model/persona variant already carried in agent.Model.
func TestPaneIdentityArgsForAdd(t *testing.T) {
	tests := []struct {
		name        string
		agent       FlatAgent
		num         int
		wantType    string
		wantIndex   int
		wantVariant string
	}{
		{
			name:        "claude with model variant",
			agent:       FlatAgent{Type: AgentTypeClaude, Model: "opus"},
			num:         2,
			wantType:    "cc",
			wantIndex:   2,
			wantVariant: "opus",
		},
		{
			name:        "empty variant writes type and index only",
			agent:       FlatAgent{Type: AgentTypeCodex},
			num:         1,
			wantType:    "cod",
			wantIndex:   1,
			wantVariant: "",
		},
		{
			name:        "persona name is the variant",
			agent:       FlatAgent{Type: AgentTypeClaude, Model: "reviewer"},
			num:         3,
			wantType:    "cc",
			wantIndex:   3,
			wantVariant: "reviewer",
		},
		{
			name:        "alias type canonicalizes",
			agent:       FlatAgent{Type: AgentType("claude"), Model: "opus"},
			num:         4,
			wantType:    "cc",
			wantIndex:   4,
			wantVariant: "opus",
		},
		{
			name:        "custom plugin type passes through canonical form",
			agent:       FlatAgent{Type: AgentType("myplugin"), Model: "v1"},
			num:         1,
			wantType:    "myplugin",
			wantIndex:   1,
			wantVariant: "v1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotIndex, gotVariant := paneIdentityArgs(tc.agent, tc.num)
			if gotType != tc.wantType || gotIndex != tc.wantIndex || gotVariant != tc.wantVariant {
				t.Errorf("paneIdentityArgs(%+v, %d) = (%q, %d, %q), want (%q, %d, %q)",
					tc.agent, tc.num, gotType, gotIndex, gotVariant, tc.wantType, tc.wantIndex, tc.wantVariant)
			}
		})
	}
}

// TestWriteAddPaneIdentityUsesHook pins that the extracted writer routes
// through the setPaneIdentityHook seam exactly once per pane with the
// canonical identity args, so runAdd's wiring is testable without tmux.
func TestWriteAddPaneIdentityUsesHook(t *testing.T) {
	prev := setPaneIdentityHook
	t.Cleanup(func() { setPaneIdentityHook = prev })

	type call struct {
		paneID  string
		typ     string
		index   int
		variant string
	}
	var calls []call
	setPaneIdentityHook = func(paneID, agentType string, ntmIndex int, variant string) {
		calls = append(calls, call{paneID: paneID, typ: agentType, index: ntmIndex, variant: variant})
	}

	writeAddPaneIdentity("%7", FlatAgent{Type: AgentTypeClaude, Model: "opus"}, 2)
	if len(calls) != 1 {
		t.Fatalf("hook calls = %d, want exactly 1: %+v", len(calls), calls)
	}
	want := call{paneID: "%7", typ: "cc", index: 2, variant: "opus"}
	if calls[0] != want {
		t.Errorf("hook call = %+v, want %+v", calls[0], want)
	}

	// Multiple panes in one add each get their own (type, num).
	writeAddPaneIdentity("%8", FlatAgent{Type: AgentTypeCodex}, 1)
	if len(calls) != 2 {
		t.Fatalf("hook calls after second pane = %d, want 2: %+v", len(calls), calls)
	}
	want = call{paneID: "%8", typ: "cod", index: 1, variant: ""}
	if calls[1] != want {
		t.Errorf("second hook call = %+v, want %+v", calls[1], want)
	}
}

func TestAddResponseJSONIncludesOllama(t *testing.T) {

	data, err := json.Marshal(output.AddResponse{
		AddedClaude: 1,
		AddedOllama: 2,
		TotalAdded:  3,
	})
	if err != nil {
		t.Fatalf("json.Marshal(AddResponse) error = %v", err)
	}

	encoded := string(data)
	if !strings.Contains(encoded, "\"added_ollama\":2") {
		t.Fatalf("AddResponse JSON = %s, want added_ollama field", encoded)
	}
}
