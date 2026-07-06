package agent

import "testing"

// TestAntigravityAgentType locks in the agy provider's identity + alias
// resolution and guards against confusion with the legacy Gemini CLI.
func TestAntigravityAgentType(t *testing.T) {
	if AgentTypeAntigravity != "agy" {
		t.Errorf("AgentTypeAntigravity = %q, want \"agy\"", AgentTypeAntigravity)
	}

	for _, alias := range []string{
		"agy", "antigravity", "antigravity-cli", "antigravity_cli",
		"antigravitycli", "google-antigravity", "AGY", "  Antigravity  ",
	} {
		if got := AgentType(alias).Canonical(); got != AgentTypeAntigravity {
			t.Errorf("Canonical(%q) = %q, want %q", alias, got, AgentTypeAntigravity)
		}
	}

	if got := AgentTypeAntigravity.DisplayName(); got != "Antigravity CLI" {
		t.Errorf("DisplayName = %q, want \"Antigravity CLI\"", got)
	}
	if got := AgentTypeAntigravity.ProfileName(); got != "Antigravity" {
		t.Errorf("ProfileName = %q, want \"Antigravity\"", got)
	}
	if !AgentTypeAntigravity.IsValid() {
		t.Error("AgentTypeAntigravity should be valid")
	}
	if !AgentTypeAntigravity.NeedsDoubleEnter() {
		t.Error("agy should need double-enter (mirrors the Gemini CLI TUI)")
	}

	// agy and the legacy gemini provider must never collapse into each other,
	// even though they share the ~/.gemini parent directory.
	if AgentType("agy").Canonical() == AgentTypeGemini {
		t.Error("agy must not canonicalize to gemini")
	}
	if AgentType("gmi").Canonical() == AgentTypeAntigravity {
		t.Error("gmi must not canonicalize to antigravity")
	}
}
