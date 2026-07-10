package tmux

import (
	"regexp"
	"strings"
)

// chromeLinePatterns match whole lines that are agent-TUI chrome rather than
// model output: status bars, the input box, spinner/hint lines, and modal
// footers. They are intentionally anchored and specific — each targets a string
// that only appears in an agent's rendered chrome, never in normal model text —
// so CleanPaneText strips noise without eating content. Add here (with a real
// captured example) rather than broadening an existing pattern.
var chromeLinePatterns = []*regexp.Regexp{
	regexp.MustCompile(`CONTEXT:\s*[⛁⛀⛶]`),                    // Claude context meter bar
	regexp.MustCompile(`^\s*[◈◉]\s*(PWD|CONTEXT):`),           // Claude status bar (pwd/context)
	regexp.MustCompile(`bypass permissions on`),               // Claude permissions footer
	regexp.MustCompile(`new task\?\s*/clear to save`),         // Claude context-save hint
	regexp.MustCompile(`\bContext\s+\d{1,3}%\s+left\b`),       // Codex context-left status line
	regexp.MustCompile(`(?i)\besc to interrupt\b`),            // active-work hint (both agents)
	regexp.MustCompile(`^\s*─+\s*Worked for\s`),               // Codex completion banner
	regexp.MustCompile(`(?i)Enter to select\b.*\b(Tab|Arrow)`), // menu chrome
	regexp.MustCompile(`(?i)\besc to cancel\b`),                 // menu chrome (incl. wrapped tail)
	regexp.MustCompile(`(?i)How is Claude doing this session`), // session survey overlay
	regexp.MustCompile(`^\s*\d:\s*(Bad|Fine|Good|Dismiss)\b`),  // survey option row
}

// boxLineRegex matches a line made up (after trimming) entirely of box-drawing
// glyphs and whitespace — the horizontal rules and dividers agent TUIs paint.
var boxLineRegex = regexp.MustCompile(`^[\s─│┌┐└┘├┤┬┴┼╭╮╰╯━┃═║╔╗╚╝▁▔▁▂▃▄▅▆▇█]+$`)

// CleanPaneText turns a raw pane capture into text suited for an LLM to read.
// It drops whole-line TUI chrome (box-drawing rules, status bars, the input
// box, spinner/hint lines, modal footers) and collapses runs of blank lines,
// while leaving all model output intact. It only removes lines it is confident
// are chrome; when in doubt a line is kept. It does not strip ANSI — capture the
// text stripped (the default) and pass it here; the raw/ANSI stream is a
// separate concern for terminal rendering.
func CleanPaneText(raw string) string {
	lines := strings.Split(raw, "\n")
	out := make([]string, 0, len(lines))
	blankRun := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if strings.TrimSpace(trimmed) == "" {
			// Collapse consecutive blank lines to a single separator.
			if !blankRun && len(out) > 0 {
				out = append(out, "")
			}
			blankRun = true
			continue
		}
		if boxLineRegex.MatchString(strings.TrimSpace(trimmed)) {
			continue
		}
		if isChromeLine(trimmed) {
			continue
		}
		out = append(out, trimmed)
		blankRun = false
	}
	// Trim a trailing blank left by collapsing.
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func isChromeLine(line string) bool {
	for _, re := range chromeLinePatterns {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}
