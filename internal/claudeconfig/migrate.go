package claudeconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Hook migration moves committed Claude Code settings files from the old
// Agent Mail hook scripts to ntm's Go Stop command (Loop 8). It changes other
// projects' shared files, so planning is pure and writing is separate.

// The old Agent Mail hook scripts, matched by exact file name in any path.
const (
	OldStopScript   = "check_critical_on_stop.sh"
	OldCloseScript  = "check_mail_on_close.sh"
	OldWindowScript = "ensure_window_id.sh"
)

// OldHookKind says which old script a hook command runs, if any.
type OldHookKind int

const (
	OldHookNone OldHookKind = iota
	// OldHookStop is replaced by ntm agent-hook stop.
	OldHookStop
	// OldHookClose is removed: turn-end reminders cover any new mail.
	OldHookClose
	// OldHookWindow is kept: the breadcrumb lookup depends on it.
	OldHookWindow
)

func (k OldHookKind) String() string {
	switch k {
	case OldHookStop:
		return OldStopScript
	case OldHookClose:
		return OldCloseScript
	case OldHookWindow:
		return OldWindowScript
	default:
		return "none"
	}
}

var oldHookScripts = map[string]OldHookKind{
	OldStopScript:   OldHookStop,
	OldCloseScript:  OldHookClose,
	OldWindowScript: OldHookWindow,
}

// shellRunners are interpreters a hook command may name before its script.
var shellRunners = map[string]bool{"sh": true, "bash": true, "zsh": true}

// ClassifyOldHookCommand reports which old script a hook command runs. The
// script is the command's first word, or its second after sh, bash or zsh,
// and it matches only when its file name is exactly one of the old names.
func ClassifyOldHookCommand(command string) OldHookKind {
	words, ok := splitShellWords(command)
	if !ok || len(words) == 0 {
		return OldHookNone
	}
	script := words[0]
	if shellRunners[path.Base(script)] && len(words) > 1 {
		script = words[1]
	}
	return oldHookScripts[path.Base(script)]
}

// splitShellWords splits a command into words as a POSIX shell would before
// expansion: blanks separate words, single quotes keep everything, double
// quotes keep everything but \" \\ \$ and \`, and a backslash outside quotes
// keeps the next character. Variables and ~ stay as written. It reports false
// for an unclosed quote.
func splitShellWords(command string) ([]string, bool) {
	var words []string
	var word strings.Builder
	inWord := false
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		case c == '\'':
			end := strings.IndexByte(command[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			word.WriteString(command[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '"':
			i++
			for ; i < len(command) && command[i] != '"'; i++ {
				if command[i] == '\\' && i+1 < len(command) && strings.IndexByte("\"\\$`", command[i+1]) >= 0 {
					i++
				}
				word.WriteByte(command[i])
			}
			if i >= len(command) {
				return nil, false
			}
			inWord = true
		case c == '\\' && i+1 < len(command):
			i++
			word.WriteByte(command[i])
			inWord = true
		default:
			word.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, true
}

// ErrBinaryOutsideHome means the ntm binary isn't under the home folder, so a
// committed ~/-relative command can't name it.
var ErrBinaryOutsideHome = errors.New("ntm binary is outside your home folder")

// shellSafePath matches a relative path that needs no quoting.
var shellSafePath = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)

// CommittedStopHookCommand is the Stop command written into committed
// settings files: ~/<path to ntm> agent-hook stop. Committed files are shared
// across machines with different home folders, so the binary is named from
// ~/, which the shell expands only while it stays unquoted. There's no
// --project, so the hook takes the project from MCP_AGENT_MAIL_PROJECT or the
// session's folder.
func CommittedStopHookCommand(ntmPath, home string) (string, error) {
	home = filepath.Clean(home)
	rel, err := filepath.Rel(home, filepath.Clean(ntmPath))
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: ntm is at %s, outside %s; install it under your home folder so committed files work on every machine", ErrBinaryOutsideHome, ntmPath, home)
	}
	rel = filepath.ToSlash(rel)
	if !shellSafePath.MatchString(rel) {
		rel = tmux.ShellQuote(rel)
	}
	return "~/" + rel + stopHookMarker, nil
}

// MigrationAction is what happens to one hook entry.
type MigrationAction string

const (
	MigrationReplace MigrationAction = "replace"
	MigrationRemove  MigrationAction = "remove"
	MigrationKeep    MigrationAction = "keep"
)

// PlannedChange is one hook entry the migration touches or reports.
type PlannedChange struct {
	Action       MigrationAction `json:"action"`
	Event        string          `json:"event"`
	Group        int             `json:"group"`
	Handler      int             `json:"handler"`
	Matcher      string          `json:"matcher,omitempty"`
	Old          string          `json:"old"`
	New          string          `json:"new,omitempty"`
	GroupEmptied bool            `json:"group_emptied,omitempty"`
}

// HookMigrationPlan is the migration planned for one settings file.
type HookMigrationPlan struct {
	Changes []PlannedChange `json:"changes"`
}

// HasChanges reports whether applying the plan would change the file.
func (p HookMigrationPlan) HasChanges() bool {
	for _, c := range p.Changes {
		if c.Action != MigrationKeep {
			return true
		}
	}
	return false
}

// hookEventOrder is Claude Code's event order, used to report events in a
// stable order; other events follow, sorted.
var hookEventOrder = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SubagentStop", "PreCompact", "SessionEnd"}

// hookEntry is one command hook in a settings file.
type hookEntry struct {
	event          string
	group, handler int
	matcher        string
	command        string
}

// settingsHookEntries lists every command hook, by event order, group and
// handler. A file that isn't the settings shape is ErrUnparseableSettings.
func settingsHookEntries(settings map[string]any) ([]hookEntry, map[string][]int, error) {
	hooks := map[string]any{}
	switch existing := settings["hooks"].(type) {
	case nil:
	case map[string]any:
		hooks = existing
	default:
		return nil, nil, fmt.Errorf("%w: hooks is %T, not an object", ErrUnparseableSettings, existing)
	}
	events := make([]string, 0, len(hooks))
	known := map[string]bool{}
	for _, event := range hookEventOrder {
		known[event] = true
		if _, ok := hooks[event]; ok {
			events = append(events, event)
		}
	}
	var others []string
	for event := range hooks {
		if !known[event] {
			others = append(others, event)
		}
	}
	sort.Strings(others)
	events = append(events, others...)

	var entries []hookEntry
	groupSizes := map[string][]int{}
	for _, event := range events {
		groups, ok := hooks[event].([]any)
		if !ok {
			return nil, nil, fmt.Errorf("%w: hooks.%s is %T, not a list", ErrUnparseableSettings, event, hooks[event])
		}
		for g, group := range groups {
			groupMap, ok := group.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("%w: hooks.%s[%d] is %T, not an object", ErrUnparseableSettings, event, g, group)
			}
			list, ok := groupMap["hooks"].([]any)
			if !ok {
				return nil, nil, fmt.Errorf("%w: hooks.%s[%d].hooks is %T, not a list", ErrUnparseableSettings, event, g, groupMap["hooks"])
			}
			matcher, _ := groupMap["matcher"].(string)
			groupSizes[event] = append(groupSizes[event], len(list))
			for h, hook := range list {
				hookMap, ok := hook.(map[string]any)
				if !ok {
					return nil, nil, fmt.Errorf("%w: hooks.%s[%d].hooks[%d] is %T, not an object", ErrUnparseableSettings, event, g, h, hook)
				}
				command, _ := hookMap["command"].(string)
				entries = append(entries, hookEntry{event: event, group: g, handler: h, matcher: matcher, command: command})
			}
		}
	}
	return entries, groupSizes, nil
}

// PlanHookMigration plans the move from the old scripts to newCommand. It only
// reads settings. A file with no old Stop or close script plans no change; an
// ensure_window_id.sh entry is listed as kept. When the file has old scripts,
// it ends with exactly one ntm Stop entry, newCommand: an ntm entry already
// there is reused (and updated), otherwise the first old Stop entry is
// replaced in place.
func PlanHookMigration(settings map[string]any, newCommand string) (HookMigrationPlan, error) {
	entries, groupSizes, err := settingsHookEntries(settings)
	if err != nil {
		return HookMigrationPlan{}, err
	}
	var plan HookMigrationPlan
	change := func(e hookEntry, action MigrationAction, newCmd string) PlannedChange {
		return PlannedChange{Action: action, Event: e.event, Group: e.group, Handler: e.handler, Matcher: e.matcher, Old: e.command, New: newCmd}
	}

	hasOld := false
	for _, e := range entries {
		if kind := ClassifyOldHookCommand(e.command); kind == OldHookStop || kind == OldHookClose {
			hasOld = true
		}
	}
	keepNtm := -1 // index in entries of the ntm Stop entry that stays
	if hasOld {
		for i, e := range entries {
			if e.event == "Stop" && IsStopHookCommand(e.command) {
				keepNtm = i
				break
			}
		}
	}
	replaced := false
	for i, e := range entries {
		kind := ClassifyOldHookCommand(e.command)
		switch {
		case kind == OldHookWindow:
			plan.Changes = append(plan.Changes, change(e, MigrationKeep, ""))
		case !hasOld:
		case kind == OldHookClose:
			plan.Changes = append(plan.Changes, change(e, MigrationRemove, ""))
		case kind == OldHookStop && e.event == "Stop" && keepNtm < 0 && !replaced:
			plan.Changes = append(plan.Changes, change(e, MigrationReplace, newCommand))
			replaced = true
		case kind == OldHookStop:
			plan.Changes = append(plan.Changes, change(e, MigrationRemove, ""))
		case e.event == "Stop" && IsStopHookCommand(e.command):
			if i != keepNtm {
				plan.Changes = append(plan.Changes, change(e, MigrationRemove, ""))
			} else if e.command != newCommand {
				plan.Changes = append(plan.Changes, change(e, MigrationReplace, newCommand))
			}
		}
	}

	removed := map[[2]any]int{}
	for _, c := range plan.Changes {
		if c.Action == MigrationRemove {
			removed[[2]any{c.Event, c.Group}]++
		}
	}
	for i, c := range plan.Changes {
		if c.Action == MigrationRemove {
			plan.Changes[i].GroupEmptied = removed[[2]any{c.Event, c.Group}] == groupSizes[c.Event][c.Group]
		}
	}
	return plan, nil
}

// ParseHookSettings decodes one settings file's text. A file that isn't a
// JSON object is ErrUnparseableSettings, naming the file.
func ParseHookSettings(path string, raw []byte) (map[string]any, error) {
	settings, err := decodeSettings(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnparseableSettings, path, err)
	}
	return settings, nil
}

// hookScanSkip are folders the scan never enters: version control, package
// and build output, where a .claude folder is a copy, not a project's own.
var hookScanSkip = map[string]bool{
	".git": true, "node_modules": true, ".venv": true, "venv": true, "vendor": true,
	"target": true, "dist": true, "build": true, "__pycache__": true, ".cache": true,
}

// hookSettingsFiles are the Claude Code settings files that can register hooks.
var hookSettingsFiles = []string{"settings.json", "settings.local.json"}

// MentionsOldHookScript reports whether a settings file's text names any old
// script. Files that don't are never parsed strictly, so an unrelated file in
// another shape isn't reported as a failure.
func MentionsOldHookScript(raw []byte) bool {
	text := string(raw)
	for name := range oldHookScripts {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
}

// FindHookSettings lists, sorted, every .claude/settings.json and
// .claude/settings.local.json below the roots. It skips hookScanSkip folders
// and doesn't follow symlinked folders. Folders it isn't allowed to read,
// such as another user's data folders, are skipped and counted in
// unreadable. Other problems are returned as errors, and the scan goes on.
func FindHookSettings(roots []string) (files []string, unreadable int, errs []error) {
	var found []string
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			errs = append(errs, err)
			continue
		}
		walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					unreadable++
				} else {
					errs = append(errs, err)
				}
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if p != root && hookScanSkip[d.Name()] {
				return fs.SkipDir
			}
			if d.Name() == ".claude" {
				for _, name := range hookSettingsFiles {
					file := filepath.Join(p, name)
					if info, err := os.Lstat(file); err == nil && !info.IsDir() {
						found = append(found, file)
					}
				}
				return fs.SkipDir
			}
			return nil
		})
		if walkErr != nil {
			errs = append(errs, walkErr)
		}
	}
	sort.Strings(found)
	return found, unreadable, errs
}
