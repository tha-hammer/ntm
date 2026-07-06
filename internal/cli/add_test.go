package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/output"
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
