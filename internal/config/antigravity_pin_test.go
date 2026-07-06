package config

import (
	"strings"
	"testing"
)

// TestAntigravityModelPin is the model-guard safety test (bd-47kjh.1.7): agy is
// HARD-pinned to "Gemini 3.1 Pro (High)" no matter what model/alias is requested,
// and the pin must not leak into the legacy gemini provider.
func TestAntigravityModelPin(t *testing.T) {
	if AntigravityRequiredModel != "Gemini 3.1 Pro (High)" {
		t.Fatalf("AntigravityRequiredModel = %q, want \"Gemini 3.1 Pro (High)\"", AntigravityRequiredModel)
	}

	m := &ModelsConfig{DefaultGemini: "gemini-3-pro-preview"}

	// Every agy alias/request resolves to the single allowed model — including
	// explicit attempts to select a Flash/other tier, which must be ignored.
	cases := []struct{ agentType, alias string }{
		{"agy", ""},
		{"agy", "flash"},
		{"agy", "gemini-3-flash"},
		{"agy", "pro-low"},
		{"antigravity", ""},
		{"antigravity", "anything"},
		{"antigravity-cli", "claude"},
	}
	for _, c := range cases {
		if got := m.GetModelName(c.agentType, c.alias); got != AntigravityRequiredModel {
			t.Errorf("GetModelName(%q, %q) = %q, want pinned %q",
				c.agentType, c.alias, got, AntigravityRequiredModel)
		}
	}

	// The agy pin must NOT affect the legacy gemini provider.
	if got := m.GetModelName("gmi", ""); got != "gemini-3-pro-preview" {
		t.Errorf("gemini default = %q, want gemini-3-pro-preview (agy pin leaked into gemini)", got)
	}
}

// TestAntigravityDefaultTemplate verifies the spawn template launches agy with
// the injected (pinned) model and agy's own autonomous flag — never gemini's --yolo.
func TestAntigravityDefaultTemplate(t *testing.T) {
	tmpl := DefaultAgentTemplates().Antigravity
	if tmpl == "" {
		t.Fatal("DefaultAgentTemplates().Antigravity is empty")
	}
	for _, want := range []string{"agy", "--model", "{{shellQuote .Model}}", "--dangerously-skip-permissions"} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("antigravity template %q missing %q", tmpl, want)
		}
	}
	if strings.Contains(tmpl, "--yolo") {
		t.Errorf("antigravity template must use --dangerously-skip-permissions, not gemini's --yolo: %q", tmpl)
	}
}
