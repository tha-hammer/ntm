package agent

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// TypedText is whether a person's unsent text is on an agent's input line.
type TypedText int

const (
	TypedTextUnknown TypedText = iota
	TypedTextAbsent
	TypedTextPresent
)

func (t TypedText) String() string {
	switch t {
	case TypedTextAbsent:
		return "absent"
	case TypedTextPresent:
		return "present"
	default:
		return "unknown"
	}
}

// inputLineMarkers is each agent's input-line prompt, as plain text once
// styles are removed. Both agents draw text they suggest on their own (a
// post-turn suggestion, hint text in an empty box) dim, with SGR 2, and a person's typed
// text without dim. Evidence: testdata/*_input_*.ansi, captured 2026-11-09.
var inputLineMarkers = map[AgentType]string{
	// Claude Code v2.1.319: "❯" and a no-break space. The prompts echoed in
	// the history above use an ordinary space, so they never match.
	AgentTypeClaudeCode: "❯ ",
	// Codex (GPT-6.1-Sol): "›". The history's prompts use it too; the input
	// line is the last one on the screen.
	AgentTypeCodex: "›",
}

// TypedTextState finds the agent's input line in a styled capture (tmux
// capture-pane -e) and tells a person's typed text from preview text by its
// style. A screen without styles, with no input line, or from an agent whose
// style isn't known is unknown, because agent interfaces change.
func TypedTextState(styledCapture string, agentType AgentType) TypedText {
	marker, ok := inputLineMarkers[agentType.Canonical()]
	if !ok || !strings.Contains(styledCapture, "\x1b[") {
		return TypedTextUnknown
	}
	for _, line := range slices.Backward(styledLines(styledCapture)) {
		rest, found := cutStyledPrefix(line, marker)
		if !found {
			continue
		}
		for _, c := range rest {
			if !c.dim && !unicode.IsSpace(c.r) {
				return TypedTextPresent
			}
		}
		return TypedTextAbsent
	}
	return TypedTextUnknown
}

// styledRune is one character on screen and whether it is drawn dim.
type styledRune struct {
	r   rune
	dim bool
}

// styledLines splits a styled capture into lines of characters. The dim
// state carries across lines, as tmux may not repeat a style at the start of
// the next line. Escape sequences other than SGR are skipped.
func styledLines(capture string) [][]styledRune {
	var lines [][]styledRune
	var line []styledRune
	dim := false
	runes := []rune(capture)
	for i := 0; i < len(runes); i++ {
		switch r := runes[i]; {
		case r == '\n':
			lines = append(lines, line)
			line = nil
		case r == '\x1b' && i+1 < len(runes) && runes[i+1] == '[':
			end := i + 2
			for end < len(runes) && (runes[end] < 0x40 || runes[end] > 0x7e) {
				end++
			}
			if end < len(runes) && runes[end] == 'm' {
				dim = applySGRDim(dim, string(runes[i+2:end]))
			}
			i = end
		case r == '\x1b' && i+1 < len(runes) && runes[i+1] == ']':
			// OSC, such as a hyperlink: ends at BEL or ESC \.
			end := i + 2
			for end < len(runes) && runes[end] != '\a' && !(runes[end] == '\x1b' && end+1 < len(runes) && runes[end+1] == '\\') {
				end++
			}
			if end < len(runes) && runes[end] == '\x1b' {
				end++
			}
			i = end
		case r == '\x1b':
			i++ // a two-byte escape
		case r == '\r':
		default:
			line = append(line, styledRune{r: r, dim: dim})
		}
	}
	return append(lines, line)
}

// applySGRDim applies one SGR sequence's parameters to the dim state. Color
// parameters (38, 48 and 58 with 5;n or 2;r;g;b) are skipped whole, so the 2
// in 38;2;r;g;b is not read as dim.
func applySGRDim(dim bool, params string) bool {
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		if strings.Contains(fields[i], ":") {
			continue // a colon-form color, one field
		}
		n, err := strconv.Atoi(fields[i])
		if fields[i] == "" {
			n, err = 0, nil
		}
		if err != nil {
			continue
		}
		switch n {
		case 0, 22:
			dim = false
		case 2:
			dim = true
		case 38, 48, 58:
			if i+1 < len(fields) && fields[i+1] == "5" {
				i += 2
			} else if i+1 < len(fields) && fields[i+1] == "2" {
				i += 4
			}
		}
	}
	return dim
}

// cutStyledPrefix reports whether line's text starts with prefix and returns
// the characters after it.
func cutStyledPrefix(line []styledRune, prefix string) ([]styledRune, bool) {
	want := []rune(prefix)
	if len(line) < len(want) {
		return nil, false
	}
	for i, r := range want {
		if line[i].r != r {
			return nil, false
		}
	}
	return line[len(want):], true
}
