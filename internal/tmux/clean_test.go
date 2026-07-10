package tmux

import (
	"strings"
	"testing"
)

func TestCleanPaneText(t *testing.T) {
	// Assembled from real captured Claude + Codex frames (sequences/zigBrowser).
	raw := strings.Join([]string{
		"  Before I write, two decisions on how",
		"  to render this (given it's 15",
		"  endpoints + 5 tables + ~8 services):",
		"",
		"",
		"────────────────────────────────",
		"  ◈ PWD: sequences │ Branch: sequences │ Age: 17m",
		"  ◉ CONTEXT: ⛁⛁⛁⛁ 13% │ 5H: 8% │ WK: 13%",
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents",
		"❯ ",
		"5. Chat about this",
		"Enter to select · Tab/Arrow keys to navigate · Esc to cancel",
		"─ Worked for 28m 36s ──────────────────",
		"  gpt-5.5 xhigh · ~/ntm_Dev/sequences · Context 62% left",
		"· Seven-phasing… (1m 41s · ↓ 5.8k tokens)",
		"  1: Bad    2: Fine   3: Good   0: Dismiss",
		"  esc to interrupt",
	}, "\n")

	got := CleanPaneText(raw)

	// Content must survive.
	for _, want := range []string{
		"Before I write, two decisions on how",
		"endpoints + 5 tables",
		"5. Chat about this",
		// The rotating spinner line is content-ish (agent status) but not in the
		// chrome list; keeping it is acceptable — it carries the working signal.
		"Seven-phasing",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("CleanPaneText dropped content %q\n---\n%s", want, got)
		}
	}

	// Chrome must be gone.
	for _, bad := range []string{
		"◈ PWD:", "◉ CONTEXT:", "bypass permissions", "Enter to select",
		"Worked for 28m", "Context 62% left", "1: Bad", "esc to interrupt",
		"────────────────────────────────",
	} {
		if strings.Contains(got, bad) {
			t.Errorf("CleanPaneText kept chrome %q\n---\n%s", bad, got)
		}
	}

	// No run of 2+ blank lines should survive.
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("CleanPaneText left a blank-line run:\n%q", got)
	}
}

func TestCleanPaneTextEmptyAndAllChrome(t *testing.T) {
	if got := CleanPaneText(""); got != "" {
		t.Errorf("CleanPaneText(\"\") = %q, want empty", got)
	}
	allChrome := "──────\n  ◉ CONTEXT: ⛁ 5%\n  esc to interrupt\n"
	if got := CleanPaneText(allChrome); got != "" {
		t.Errorf("CleanPaneText(all chrome) = %q, want empty", got)
	}
}
