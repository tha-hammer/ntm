package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// The captures are real panes from Claude Code v2.1.319 and Codex
// (GPT-6.1-Sol), taken with tmux capture-pane -e -p on 2026-11-09 for Loop 5
// Sprint 4. Codex has no empty capture: it always shows its placeholder in an
// empty box.
var typedTextFixtures = []struct {
	file  string
	agent AgentType
	want  TypedText
}{
	{"cc_input_typed.ansi", AgentTypeClaudeCode, TypedTextPresent},
	{"cc_input_preview.ansi", AgentTypeClaudeCode, TypedTextAbsent},
	{"cc_input_empty.ansi", AgentTypeClaudeCode, TypedTextAbsent},
	{"cc_startup_placeholder.ansi", AgentTypeClaudeCode, TypedTextAbsent},
	{"cod_input_typed.ansi", AgentTypeCodex, TypedTextPresent},
	{"cod_input_preview.ansi", AgentTypeCodex, TypedTextAbsent},
	{"cod_startup_placeholder.ansi", AgentTypeCodex, TypedTextAbsent},
}

func readTypedTextFixture(t testing.TB, file string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return string(content)
}

// Loop 5, Behavior 21: typed text is told apart from preview text by its
// style, on real captures.
func TestTypedTextState_Captures(t *testing.T) {
	for _, fx := range typedTextFixtures {
		t.Run(fx.file, func(t *testing.T) {
			if got := TypedTextState(readTypedTextFixture(t, fx.file), fx.agent); got != fx.want {
				t.Fatalf("TypedTextState = %v, want %v", got, fx.want)
			}
		})
	}
}

func TestTypedTextState_Cases(t *testing.T) {
	const claudePrompt = "\x1b[39m❯ "
	const codexPrompt = "\x1b[1m›\x1b[0m "
	cases := []struct {
		name    string
		capture string
		agent   AgentType
		want    TypedText
	}{
		{"typed then preview", claudePrompt + "hel\x1b[2mlo world\x1b[0m\n", AgentTypeClaudeCode, TypedTextPresent},
		{"trailing spaces after the prompt", claudePrompt + "   \n", AgentTypeClaudeCode, TypedTextAbsent},
		{"no input line", "\x1b[39mThinking about it\n\x1b[2mesc to interrupt\x1b[0m\n", AgentTypeClaudeCode, TypedTextUnknown},
		{"a history prompt is not the input line", "\x1b[37m\x1b[100m❯ Reply with ok\x1b[39m\n", AgentTypeClaudeCode, TypedTextUnknown},
		{"no styles at all", stripANSICodes(readTypedTextFixture(t, "cc_input_typed.ansi")), AgentTypeClaudeCode, TypedTextUnknown},
		{"codex empty", codexPrompt + "\n", AgentTypeCodex, TypedTextAbsent},
		{"codex typed then placeholder", codexPrompt + "fix\x1b[2m the tests\x1b[0m\n", AgentTypeCodex, TypedTextPresent},
		{"dim carried from the line above", "\x1b[2mhint\n" + claudePrompt + "suggested\x1b[0m\n", AgentTypeClaudeCode, TypedTextAbsent},
		{"a 24-bit color is not dim", claudePrompt + "\x1b[38;2;246;226;183mtyped\x1b[39m\n", AgentTypeClaudeCode, TypedTextPresent},
		{"normal intensity ends dim", claudePrompt + "\x1b[2mhint \x1b[22mtyped\n", AgentTypeClaudeCode, TypedTextPresent},
		{"aliases are canonicalized", claudePrompt + "typed\n", AgentType("claude"), TypedTextPresent},
		{"gemini is unknown", readTypedTextFixture(t, "cc_input_typed.ansi"), AgentTypeGemini, TypedTextUnknown},
		{"empty capture", "", AgentTypeClaudeCode, TypedTextUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TypedTextState(tc.capture, tc.agent); got != tc.want {
				t.Fatalf("TypedTextState(%q) = %v, want %v", tc.capture, got, tc.want)
			}
		})
	}
}

// Behavior 21 property: more scrollback above the input box never changes the
// result.
func TestTypedTextState_ScrollbackProperty(t *testing.T) {
	captures := make([]string, len(typedTextFixtures))
	for i, fx := range typedTextFixtures {
		captures[i] = readTypedTextFixture(t, fx.file)
	}
	rapid.Check(t, func(rt *rapid.T) {
		i := rapid.IntRange(0, len(typedTextFixtures)-1).Draw(rt, "fixture")
		extra := rapid.SliceOfN(rapid.StringMatching(`[a-z ]{0,40}`), 0, 30).Draw(rt, "scrollback")
		capture := strings.Join(append(extra, captures[i]), "\n")
		if got := TypedTextState(capture, typedTextFixtures[i].agent); got != typedTextFixtures[i].want {
			rt.Fatalf("%s with %d scrollback lines = %v, want %v", typedTextFixtures[i].file, len(extra), got, typedTextFixtures[i].want)
		}
	})
}
