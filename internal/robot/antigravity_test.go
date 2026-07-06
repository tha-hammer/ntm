package robot

import "testing"

// TestAntigravityResolveAndDetect locks in agy type-resolution and pane-title
// detection, and guards the gmi/agy disambiguation.
func TestAntigravityResolveAndDetect(t *testing.T) {
	for _, in := range []string{"agy", "antigravity", "antigravity-cli"} {
		if got := ResolveAgentType(in); got != "antigravity" {
			t.Errorf("ResolveAgentType(%q) = %q, want \"antigravity\"", in, got)
		}
	}
	if ResolveAgentType("agy") == "gemini" {
		t.Error("agy must not resolve to gemini")
	}

	// agy panes are titled "<session>__agy_<n>" (or "__agy__<variant>").
	if got := DetectAgentType("proj__agy_1"); got != "antigravity" {
		t.Errorf("DetectAgentType(proj__agy_1) = %q, want \"antigravity\"", got)
	}
	if got := DetectAgentType("proj__agy__variant"); got != "antigravity" {
		t.Errorf("DetectAgentType(proj__agy__variant) = %q, want \"antigravity\"", got)
	}
	// The legacy gemini short form must still detect as gemini, not antigravity.
	if got := DetectAgentType("proj__gmi_1"); got != "gemini" {
		t.Errorf("DetectAgentType(proj__gmi_1) = %q, want \"gemini\"", got)
	}
}
