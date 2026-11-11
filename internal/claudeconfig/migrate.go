package claudeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return "~/" + QuotedPath(filepath.ToSlash(rel)) + stopHookMarker, nil
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

// spanNode is one JSON value and where it sits in the file's text. Objects
// also record each key's span. Migration edits a committed file by splicing
// these spans, so every byte it doesn't change, including hand formatting,
// stays as it was and git diff shows only the changed lines.
type spanNode struct {
	start, end int
	kind       byte // '{', '[' or 's' for a scalar
	keys       []string
	keyStarts  []int
	keyEnds    []int
	children   []*spanNode
}

// member returns the value of an object's key, or nil.
func (n *spanNode) member(key string) (int, *spanNode) {
	for i, k := range n.keys {
		if k == key {
			return i, n.children[i]
		}
	}
	return -1, nil
}

// parseSettingsSpans indexes one JSON document's values by byte span.
func parseSettingsSpans(raw []byte) (*spanNode, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root, err := parseSpanValue(dec, raw)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("text after the JSON value")
	}
	return root, nil
}

// skipSeparators moves past blanks, commas and colons to a token's first byte.
func skipSeparators(raw []byte, i int) int {
	for i < len(raw) && strings.IndexByte(" \t\r\n,:", raw[i]) >= 0 {
		i++
	}
	return i
}

func parseSpanValue(dec *json.Decoder, raw []byte) (*spanNode, error) {
	before := int(dec.InputOffset())
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	n := &spanNode{start: skipSeparators(raw, before), kind: 's'}
	if delim, ok := tok.(json.Delim); ok {
		n.kind = byte(delim)
		for dec.More() {
			if n.kind == '{' {
				keyBefore := int(dec.InputOffset())
				keyTok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := keyTok.(string)
				n.keys = append(n.keys, key)
				n.keyStarts = append(n.keyStarts, skipSeparators(raw, keyBefore))
				n.keyEnds = append(n.keyEnds, int(dec.InputOffset()))
			}
			child, err := parseSpanValue(dec, raw)
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child)
		}
		if _, err := dec.Token(); err != nil { // the closing } or ]
			return nil, err
		}
	}
	n.end = int(dec.InputOffset())
	return n, nil
}

// textEdit replaces raw[start:end] with text.
type textEdit struct {
	start, end int
	text       string
}

// deletionEdits removes the marked elements of a list whose element spans
// are starts[i]:ends[i], with the separators around them, keeping the list
// well formed. At least one element must stay.
func deletionEdits(starts, ends []int, removed []bool) []textEdit {
	var edits []textEdit
	for i := 0; i < len(removed); i++ {
		if !removed[i] {
			continue
		}
		j := i
		for j+1 < len(removed) && removed[j+1] {
			j++
		}
		if j+1 < len(removed) {
			edits = append(edits, textEdit{starts[i], starts[j+1], ""}) // up to the next kept element
		} else {
			edits = append(edits, textEdit{ends[i-1], ends[j], ""}) // from the previous kept element's end
		}
		i = j
	}
	return edits
}

// jsonText is s as a JSON string, without HTML escaping.
func jsonText(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// lineIndent is the blanks at the start of the line holding raw[i].
func lineIndent(raw []byte, i int) string {
	lineStart := bytes.LastIndexByte(raw[:i], '\n') + 1
	end := lineStart
	for end < len(raw) && (raw[end] == ' ' || raw[end] == '\t') {
		end++
	}
	return string(raw[lineStart:end])
}

// replaceEntryEdits points one hook entry at newCommand, with the
// HookRegistration timeout of 5 seconds.
func replaceEntryEdits(raw []byte, entry *spanNode, newCommand string) ([]textEdit, error) {
	c, command := entry.member("command")
	if command == nil {
		return nil, fmt.Errorf("hook entry has no command")
	}
	edits := []textEdit{{command.start, command.end, jsonText(newCommand)}}
	timeout := fmt.Sprint(StopHookTimeoutSeconds)
	switch _, value := entry.member("timeout"); {
	case value != nil && string(raw[value.start:value.end]) != timeout:
		edits = append(edits, textEdit{value.start, value.end, timeout})
	case value == nil && bytes.IndexByte(raw[entry.start:entry.end], '\n') < 0:
		edits = append(edits, textEdit{command.end, command.end, `, "timeout": ` + timeout})
	case value == nil:
		edits = append(edits, textEdit{command.end, command.end, ",\n" + lineIndent(raw, entry.keyStarts[c]) + `"timeout": ` + timeout})
	}
	return edits, nil
}

// ApplyHookMigration applies a plan to a settings file's text. It replaces
// the planned entries' commands, removes the planned entries, and removes a
// group, event list or the hooks object's content when that leaves it empty.
// Everything else stays byte for byte. A plan with no changes returns raw.
func ApplyHookMigration(raw []byte, plan HookMigrationPlan) ([]byte, error) {
	if !plan.HasChanges() {
		return raw, nil
	}
	root, err := parseSettingsSpans(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnparseableSettings, err)
	}
	_, hooks := root.member("hooks")
	if root.kind != '{' || hooks == nil || hooks.kind != '{' {
		return nil, fmt.Errorf("%w: no hooks object", ErrUnparseableSettings)
	}
	type at struct {
		event          string
		group, handler int
	}
	replace := map[at]string{}
	remove := map[at]bool{}
	for _, c := range plan.Changes {
		switch c.Action {
		case MigrationReplace:
			replace[at{c.Event, c.Group, c.Handler}] = c.New
		case MigrationRemove:
			remove[at{c.Event, c.Group, c.Handler}] = true
		}
	}

	var edits []textEdit
	eventRemoved := make([]bool, len(hooks.children))
	for e, event := range hooks.children {
		name := hooks.keys[e]
		if event.kind != '[' {
			return nil, fmt.Errorf("%w: hooks.%s isn't a list", ErrUnparseableSettings, name)
		}
		groupRemoved := make([]bool, len(event.children))
		var groupStarts, groupEnds []int
		for g, group := range event.children {
			groupStarts, groupEnds = append(groupStarts, group.start), append(groupEnds, group.end)
			_, list := group.member("hooks")
			if group.kind != '{' || list == nil || list.kind != '[' {
				return nil, fmt.Errorf("%w: hooks.%s[%d] has no hooks list", ErrUnparseableSettings, name, g)
			}
			handlerRemoved := make([]bool, len(list.children))
			var starts, ends []int
			kept := 0
			for h, entry := range list.children {
				starts, ends = append(starts, entry.start), append(ends, entry.end)
				handlerRemoved[h] = remove[at{name, g, h}]
				if handlerRemoved[h] {
					continue
				}
				kept++
				if newCommand, ok := replace[at{name, g, h}]; ok {
					entryEdits, err := replaceEntryEdits(raw, entry, newCommand)
					if err != nil {
						return nil, fmt.Errorf("%w: hooks.%s[%d].hooks[%d]: %v", ErrUnparseableSettings, name, g, h, err)
					}
					edits = append(edits, entryEdits...)
				}
			}
			switch {
			case kept == 0 && len(list.children) > 0:
				groupRemoved[g] = true
			case kept < len(list.children):
				edits = append(edits, deletionEdits(starts, ends, handlerRemoved)...)
			}
		}
		if allTrue(groupRemoved) && len(groupRemoved) > 0 {
			eventRemoved[e] = true
			continue
		}
		edits = append(edits, deletionEdits(groupStarts, groupEnds, groupRemoved)...)
	}
	switch {
	case allTrue(eventRemoved) && len(eventRemoved) > 0:
		edits = filterEditsOutside(edits, hooks.start, hooks.end)
		edits = append(edits, textEdit{hooks.start, hooks.end, "{}"})
	default:
		edits = append(edits, deletionEdits(hooks.keyStarts, childEnds(hooks), eventRemoved)...)
	}
	return spliceEdits(raw, edits)
}

// WriteMigratedSettings applies plan to raw, the text read from path, checks
// that planning the result finds nothing left to change, and writes it
// atomically to the file path points to (through a symlink), keeping that
// file's mode.
func WriteMigratedSettings(path string, raw []byte, plan HookMigrationPlan, newCommand string) error {
	out, err := ApplyHookMigration(raw, plan)
	if err != nil {
		return err
	}
	after, err := decodeSettings(out)
	if err != nil {
		return fmt.Errorf("the migrated text isn't valid JSON: %w", err)
	}
	if again, err := PlanHookMigration(after, newCommand); err != nil || again.HasChanges() {
		return fmt.Errorf("the migrated settings still need changes")
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	return writeFileAtomic(target, out, info.Mode().Perm())
}

// QuotedPath is p, single-quoted for a shell when it isn't plain.
func QuotedPath(p string) string {
	if shellSafePath.MatchString(p) {
		return p
	}
	return tmux.ShellQuote(p)
}

func allTrue(values []bool) bool {
	for _, v := range values {
		if !v {
			return false
		}
	}
	return true
}

func childEnds(n *spanNode) []int {
	ends := make([]int, len(n.children))
	for i, child := range n.children {
		ends[i] = child.end
	}
	return ends
}

// filterEditsOutside drops edits inside [start, end), which a larger edit
// replaces.
func filterEditsOutside(edits []textEdit, start, end int) []textEdit {
	kept := edits[:0]
	for _, e := range edits {
		if e.end <= start || e.start >= end {
			kept = append(kept, e)
		}
	}
	return kept
}

// spliceEdits applies non-overlapping edits to raw.
func spliceEdits(raw []byte, edits []textEdit) ([]byte, error) {
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	pos := 0
	for _, e := range edits {
		if e.start < pos {
			return nil, fmt.Errorf("overlapping edits at byte %d", e.start)
		}
		out.Write(raw[pos:e.start])
		out.WriteString(e.text)
		pos = e.end
	}
	out.Write(raw[pos:])
	return out.Bytes(), nil
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
