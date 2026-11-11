package claudeconfig

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Loop 8, Behavior 1: old scripts are found by their exact file name, in any
// path form.
func TestClassifyOldHookCommand(t *testing.T) {
	for command, want := range map[string]OldHookKind{
		"~/.claude/hooks/check_critical_on_stop.sh":                                  OldHookStop,
		"/home/maceo/Dev/mcp_agent_mail/.claude/hooks/check_critical_on_stop.sh":     OldHookStop,
		`"$CLAUDE_PROJECT_DIR"/.claude/hooks/check_mail_on_close.sh`:                 OldHookClose,
		`$CLAUDE_PROJECT_DIR/.claude/hooks/check_mail_on_close.sh`:                   OldHookClose,
		"bash '/home/u/my dir/.claude/hooks/ensure_window_id.sh'":                    OldHookWindow,
		"sh ~/.claude/hooks/check_critical_on_stop.sh --verbose":                     OldHookStop,
		"my_check_critical_on_stop.sh.bak":                                           OldHookNone,
		"~/.claude/hooks/check_critical_on_stop.sh.orig":                             OldHookNone,
		"echo check_critical_on_stop.sh is old":                                      OldHookNone,
		"cd '/x' && uv run python -m mcp_agent_mail.cli file_reservations list '/x'": OldHookNone,
		"~/.local/bin/ntm agent-hook stop":                                           OldHookNone,
		"":                                                                           OldHookNone,
		"'unclosed /x/check_critical_on_stop.sh":                                     OldHookNone,
	} {
		if got := ClassifyOldHookCommand(command); got != want {
			t.Fatalf("ClassifyOldHookCommand(%q) = %v, want %v", command, got, want)
		}
	}
}

// quoteWord writes word plain, single quoted or double quoted.
func quoteWord(rt *rapid.T, word string) string {
	switch rapid.IntRange(0, 2).Draw(rt, "quote") {
	case 1:
		return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
	case 2:
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(word) + `"`
	default:
		return strings.ReplaceAll(word, " ", `\ `)
	}
}

// Loop 8, Behavior 1 property: in any folder and any quoting, the exact
// name matches its kind, and a longer name never matches.
func TestClassifyOldHookCommand_PathFormsProperty(t *testing.T) {
	names := map[string]OldHookKind{
		OldStopScript: OldHookStop, OldCloseScript: OldHookClose, OldWindowScript: OldHookWindow,
	}
	rapid.Check(t, func(rt *rapid.T) {
		name := rapid.SampledFrom([]string{OldStopScript, OldCloseScript, OldWindowScript}).Draw(rt, "name")
		dir := rapid.SampledFrom([]string{"~/.claude/hooks", "/home/u/a b/.claude/hooks", "$CLAUDE_PROJECT_DIR/.claude/hooks", "/x"}).Draw(rt, "dir")
		if dir == "$CLAUDE_PROJECT_DIR/.claude/hooks" || strings.HasPrefix(dir, "~") {
			// $ and ~ must stay unquoted or in double quotes to expand; keep them plain.
			if got := ClassifyOldHookCommand(dir + "/" + name); got != names[name] {
				rt.Fatalf("%q = %v", dir+"/"+name, got)
			}
			return
		}
		command := quoteWord(rt, dir+"/"+name)
		if got := ClassifyOldHookCommand(command); got != names[name] {
			rt.Fatalf("%q = %v, want %v", command, got, names[name])
		}
		suffix := rapid.SampledFrom([]string{".bak", "x", ".orig"}).Draw(rt, "suffix")
		if got := ClassifyOldHookCommand(quoteWord(rt, dir+"/"+name+suffix)); got != OldHookNone {
			rt.Fatalf("%s%s matched as %v", name, suffix, got)
		}
		if got := ClassifyOldHookCommand(quoteWord(rt, dir+"/my_"+name)); got != OldHookNone {
			rt.Fatalf("my_%s matched as %v", name, got)
		}
	})
}

// Loop 8, Behavior 3: the committed command is ~/-relative with no --project,
// and a binary outside home is refused.
func TestCommittedStopHookCommand(t *testing.T) {
	for _, tc := range []struct{ bin, home, want string }{
		{"/home/u/.local/bin/ntm", "/home/u", "~/.local/bin/ntm agent-hook stop"},
		{"/home/u/.local/bin/ntm", "/home/u/", "~/.local/bin/ntm agent-hook stop"},
		{"/home/u/my tools/ntm", "/home/u", "~/'my tools/ntm' agent-hook stop"},
	} {
		got, err := CommittedStopHookCommand(tc.bin, tc.home)
		if err != nil || got != tc.want {
			t.Fatalf("CommittedStopHookCommand(%q, %q) = %q, %v; want %q", tc.bin, tc.home, got, err, tc.want)
		}
		if !IsStopHookCommand(got) {
			t.Fatalf("IsStopHookCommand(%q) = false", got)
		}
	}
	for _, bin := range []string{"/usr/local/bin/ntm", "/home/uother/ntm", "/home/u"} {
		_, err := CommittedStopHookCommand(bin, "/home/u")
		if !errors.Is(err, ErrBinaryOutsideHome) || !strings.Contains(err.Error(), bin) {
			t.Fatalf("CommittedStopHookCommand(%q) err = %v", bin, err)
		}
	}
}

// Loop 8, Behavior 3 property: with HOME set, the shell turns the command
// back into the binary's absolute path.
func TestCommittedStopHookCommand_ShellProperty(t *testing.T) {
	home := t.TempDir()
	rapid.Check(t, func(rt *rapid.T) {
		rel := rapid.StringMatching(`[a-z][a-z '"$.-]{0,12}(/[a-z][a-z '"$.-]{0,12}){0,2}`).Draw(rt, "rel")
		bin := filepath.Join(home, rel)
		command, err := CommittedStopHookCommand(bin, home)
		if err != nil {
			rt.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", "printf '%s\\n' "+command)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		if err != nil {
			rt.Fatalf("sh -c %q: %v", command, err)
		}
		if want := bin + "\nagent-hook\nstop\n"; string(out) != want {
			rt.Fatalf("sh splits %q into %q, want %q", command, out, want)
		}
	})
}

func readFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "migrate", name))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := decodeSettings(data)
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

type changeKey struct {
	Action  MigrationAction
	Event   string
	Group   int
	Handler int
}

func planKeys(plan HookMigrationPlan) []changeKey {
	keys := make([]changeKey, 0, len(plan.Changes))
	for _, c := range plan.Changes {
		keys = append(keys, changeKey{c.Action, c.Event, c.Group, c.Handler})
	}
	return keys
}

// Loop 8, Behavior 2: each file's migration is planned.
func TestPlanHookMigration(t *testing.T) {
	const newCmd = "~/.local/bin/ntm agent-hook stop"
	for _, tc := range []struct {
		fixture string
		want    []changeKey
		changes bool
	}{
		{"agentmail.json", []changeKey{
			{MigrationKeep, "SessionStart", 0, 1},
			{MigrationRemove, "PostToolUse", 1, 0},
			{MigrationReplace, "Stop", 0, 0},
		}, true},
		{"project_dir.json", []changeKey{
			{MigrationRemove, "PostToolUse", 0, 1},
			{MigrationReplace, "Stop", 0, 1},
		}, true},
		{"ntm_and_old.json", []changeKey{
			{MigrationReplace, "Stop", 0, 0},
			{MigrationRemove, "Stop", 1, 0},
		}, true},
		{"plain.json", []changeKey{{MigrationKeep, "SessionStart", 0, 0}}, false},
		{"migrated.json", []changeKey{{MigrationKeep, "SessionStart", 0, 0}}, false},
	} {
		plan, err := PlanHookMigration(readFixture(t, tc.fixture), newCmd)
		if err != nil {
			t.Fatalf("%s: %v", tc.fixture, err)
		}
		if got := planKeys(plan); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s: plan = %+v, want %+v", tc.fixture, got, tc.want)
		}
		if plan.HasChanges() != tc.changes {
			t.Fatalf("%s: HasChanges = %v", tc.fixture, plan.HasChanges())
		}
	}

	plan, _ := PlanHookMigration(readFixture(t, "agentmail.json"), newCmd)
	for _, c := range plan.Changes {
		switch c.Action {
		case MigrationReplace:
			if c.New != newCmd || !strings.HasSuffix(c.Old, "/check_critical_on_stop.sh") || c.Matcher != "" {
				t.Fatalf("replace = %+v", c)
			}
		case MigrationRemove:
			if c.Matcher != "Bash" || !c.GroupEmptied {
				t.Fatalf("remove = %+v", c)
			}
		}
	}
	plan, _ = PlanHookMigration(readFixture(t, "project_dir.json"), newCmd)
	for _, c := range plan.Changes {
		if c.Action == MigrationRemove && c.GroupEmptied {
			t.Fatalf("a group with another hook would be emptied: %+v", c)
		}
	}
	plan, _ = PlanHookMigration(readFixture(t, "ntm_and_old.json"), newCmd)
	if c := plan.Changes[0]; c.Old != "~/.local/bin/ntm-old agent-hook stop" || c.New != newCmd {
		t.Fatalf("existing ntm entry change = %+v", c)
	}

	for _, bad := range []string{`{"hooks": []}`, `{"hooks": {"Stop": {}}}`, `{"hooks": {"Stop": [1]}}`} {
		settings, err := decodeSettings([]byte(bad))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PlanHookMigration(settings, newCmd); !errors.Is(err, ErrUnparseableSettings) {
			t.Fatalf("%s: err = %v", bad, err)
		}
	}
}

// Loop 8, Behavior 4: the scan finds settings files and skips the rest.
func TestFindHookSettings(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) string {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	want := []string{
		write("a/.claude/settings.json"),
		write("a/.claude/settings.local.json"),
		write("deep/x/y/z/.claude/settings.json"),
	}
	for _, skipped := range []string{"a/.git/x/.claude/settings.json", "b/node_modules/p/.claude/settings.json", "c/.venv/.claude/settings.json", "d/vendor/v/.claude/settings.json", "e/.claude/other.json"} {
		write(skipped)
	}
	if err := os.Symlink(root, filepath.Join(root, "a", "loop")); err != nil {
		t.Fatal(err)
	}
	got, unreadable, walkErrs := FindHookSettings([]string{root})
	if len(walkErrs) != 0 || unreadable != 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("FindHookSettings = %v (%d unreadable, %v), want %v", got, unreadable, walkErrs, want)
	}

	if _, _, errs := FindHookSettings([]string{filepath.Join(root, "missing")}); len(errs) != 1 {
		t.Fatalf("missing root errors = %v", errs)
	}
}

// Loop 8, Behavior 4 (Sprint 1 human test finding 1): folders the scan can't
// read are counted and skipped, not reported as errors.
func TestFindHookSettings_UnreadableFoldersSkipped(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("this test needs a non-root user, because root reads every folder")
	}
	root := t.TempDir()
	keep := filepath.Join(root, "ok", ".claude", "settings.json")
	for _, path := range []string{keep, filepath.Join(root, "locked1", "x", ".claude", "settings.json"), filepath.Join(root, "locked2", "settings.json")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, locked := range []string{"locked1", "locked2"} {
		dir := filepath.Join(root, locked)
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	got, unreadable, errs := FindHookSettings([]string{root})
	if len(errs) != 0 || unreadable != 2 || !reflect.DeepEqual(got, []string{keep}) {
		t.Fatalf("FindHookSettings = %v, %d unreadable, errors %v", got, unreadable, errs)
	}
}

// Loop 8, Behavior 2 (Sprint 1 human test finding 2): only a file that names
// an old script is parsed strictly.
func TestMentionsOldHookScript(t *testing.T) {
	for raw, want := range map[string]bool{
		`{"hooks":{"Stop":[{"command":"~/.claude/hooks/check_critical_on_stop.sh"}]}}`: true,
		`{"x":"check_mail_on_close.sh"}`:                                               true,
		`{"x":"/a/ensure_window_id.sh"}`:                                               true,
		`{"hooks":{"Stop":[{"command":"cargo fmt"}]}}`:                                 false,
		`{bad`: false,
		``:     false,
	} {
		if got := MentionsOldHookScript([]byte(raw)); got != want {
			t.Fatalf("MentionsOldHookScript(%s) = %v, want %v", raw, got, want)
		}
	}
}

// Loop 8, Behavior 4: the home folder's own ~/.claude is found only when
// home itself is named.
func TestFindHookSettings_HomeClaudeOnlyWhenNamed(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{".claude/settings.json", "Dev/p/.claude/settings.json"} {
		path := filepath.Join(home, rel)
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		_ = os.WriteFile(path, []byte("{}"), 0o644)
	}
	got, _, _ := FindHookSettings([]string{filepath.Join(home, "Dev")})
	if len(got) != 1 || !strings.HasSuffix(got[0], "Dev/p/.claude/settings.json") {
		t.Fatalf("Dev scan = %v", got)
	}
	got, _, _ = FindHookSettings([]string{home})
	if len(got) != 2 {
		t.Fatalf("home scan = %v", got)
	}
}

// --- Sprint 2: apply ---

// Loop 8, Behavior 8: every value's recorded span holds exactly that value,
// so edits can splice the text and leave the rest byte for byte.
func TestSettingsSpans_ExactProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		indent := rapid.SampledFrom([]string{"", "  ", "    ", "\t"}).Draw(rt, "indent")
		value := settingsObjectGen().Draw(rt, "settings")
		var raw []byte
		var err error
		if indent == "" {
			raw, err = json.Marshal(value)
		} else {
			raw, err = json.MarshalIndent(value, "", indent)
		}
		if err != nil {
			rt.Fatal(err)
		}
		root, err := parseSettingsSpans(raw)
		if err != nil {
			rt.Fatalf("parse: %v\n%s", err, raw)
		}
		var check func(n *spanNode)
		check = func(n *spanNode) {
			var got, want any
			if err := json.Unmarshal(raw[n.start:n.end], &got); err != nil {
				rt.Fatalf("span %d:%d isn't one value: %q", n.start, n.end, raw[n.start:n.end])
			}
			_ = json.Unmarshal(raw[n.start:n.end], &want)
			for i, child := range n.children {
				check(child)
				if n.kind == '{' {
					var key string
					if err := json.Unmarshal(raw[n.keyStarts[i]:n.keyEnds[i]], &key); err != nil || key != n.keys[i] {
						rt.Fatalf("key span = %q, want %q", raw[n.keyStarts[i]:n.keyEnds[i]], n.keys[i])
					}
				}
			}
			if !reflect.DeepEqual(got, want) {
				rt.Fatal("unreachable")
			}
		}
		check(root)
		if root.start != 0 || strings.TrimSpace(string(raw[root.end:])) != "" {
			rt.Fatalf("root span %d:%d of %d bytes", root.start, root.end, len(raw))
		}
	})
}

func applyFixture(t *testing.T, name string) (raw, out []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "migrate", name))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := decodeSettings(raw)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanHookMigration(settings, "~/.local/bin/ntm agent-hook stop")
	if err != nil {
		t.Fatal(err)
	}
	out, err = ApplyHookMigration(raw, plan)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return raw, out
}

// Loop 8, Behavior 9: applying a plan changes only the old entries, by
// splicing their text.
func TestApplyHookMigration(t *testing.T) {
	const newCmd = "~/.local/bin/ntm agent-hook stop"

	raw, out := applyFixture(t, "agentmail.json")
	want := strings.Replace(string(raw), `"/home/user/Dev/mcp_agent_mail/.claude/hooks/check_critical_on_stop.sh"`, `"`+newCmd+`"`, 1)
	closeGroup := `,
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "/home/user/Dev/mcp_agent_mail/.claude/hooks/check_mail_on_close.sh",
            "timeout": 5
          }
        ]
      }`
	if !strings.Contains(want, closeGroup) {
		t.Fatal("fixture changed; update the expected text")
	}
	want = strings.Replace(want, closeGroup, "", 1)
	if string(out) != want {
		t.Fatalf("agentmail.json migrated to:\n%s\nwant:\n%s", out, want)
	}

	_, out = applyFixture(t, "project_dir.json")
	settings, _ := decodeSettings(out)
	stop := settings["hooks"].(map[string]any)["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)
	post := settings["hooks"].(map[string]any)["PostToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)
	if len(stop) != 2 || stop[0].(map[string]any)["command"] != "bd sync" || stop[1].(map[string]any)["command"] != newCmd ||
		len(post) != 1 || post[0].(map[string]any)["command"] != "echo closed" {
		t.Fatalf("project_dir.json migrated to:\n%s", out)
	}

	_, out = applyFixture(t, "ntm_and_old.json")
	settings, _ = decodeSettings(out)
	groups := settings["hooks"].(map[string]any)["Stop"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"] != newCmd {
		t.Fatalf("ntm_and_old.json migrated to:\n%s", out)
	}

	raw, out = applyFixture(t, "tabs_no_timeout.json")
	if !strings.Contains(string(out), "\t\t\t\t\t\t\"command\": \""+newCmd+"\",\n\t\t\t\t\t\t\"timeout\": 5\n") || !strings.HasSuffix(string(out), "\t\"model\": \"opus\"\n}\n") {
		t.Fatalf("tabs_no_timeout.json migrated to:\n%s", out)
	}
	_ = raw

	raw, out = applyFixture(t, "close_only.json")
	wantClose := strings.Replace(string(raw), `    "hooks": {
        "PostToolUse": [
            {
                "matcher": "Bash",
                "hooks": [
                    { "type": "command", "command": "~/.claude/hooks/check_mail_on_close.sh", "timeout": 5 }
                ]
            }
        ]
    },
`, `    "hooks": {},
`, 1)
	if string(out) != wantClose {
		t.Fatalf("close_only.json migrated to:\n%s\nwant:\n%s", out, wantClose)
	}

	for _, unchanged := range []string{"plain.json", "migrated.json"} {
		if raw, out := applyFixture(t, unchanged); string(raw) != string(out) {
			t.Fatalf("%s changed:\n%s", unchanged, out)
		}
	}
}

// otherEntries lists the commands that aren't old scripts or ntm entries, by
// event, in order.
func otherEntries(t hookTB, settings map[string]any) []string {
	t.Helper()
	entries, _, err := settingsHookEntries(settings)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if ClassifyOldHookCommand(e.command) == OldHookNone && !IsStopHookCommand(e.command) {
			out = append(out, e.event+": "+e.command)
		}
	}
	return out
}

// oldScriptHookGen draws a command hook that is sometimes an old script.
func oldScriptHookGen() *rapid.Generator[any] {
	return rapid.Custom(func(rt *rapid.T) any {
		command := rapid.SampledFrom([]string{
			"~/.claude/hooks/check_critical_on_stop.sh", "/home/u/.claude/hooks/check_mail_on_close.sh",
			"~/.claude/hooks/ensure_window_id.sh", "/old/ntm agent-hook stop", "echo hi", "bd sync",
		}).Draw(rt, "command")
		hook := map[string]any{"type": "command", "command": command}
		if rapid.Bool().Draw(rt, "timeout") {
			hook["timeout"] = float64(rapid.IntRange(1, 9).Draw(rt, "seconds"))
		}
		return hook
	})
}

func hooksSettingsGen() *rapid.Generator[map[string]any] {
	return rapid.Custom(func(rt *rapid.T) map[string]any {
		hooks := map[string]any{}
		for _, event := range []string{"SessionStart", "PostToolUse", "Stop"} {
			if !rapid.Bool().Draw(rt, "has-"+event) {
				continue
			}
			groups := []any{}
			for g, n := 0, rapid.IntRange(1, 3).Draw(rt, "groups"); g < n; g++ {
				list := []any{}
				for h, m := 0, rapid.IntRange(1, 3).Draw(rt, "hooks"); h < m; h++ {
					list = append(list, oldScriptHookGen().Draw(rt, "hook"))
				}
				groups = append(groups, map[string]any{"matcher": "", "hooks": list})
			}
			hooks[event] = groups
		}
		settings := map[string]any{"hooks": hooks}
		if rapid.Bool().Draw(rt, "model") {
			settings["model"] = "opus"
		}
		return settings
	})
}

// Loop 8, Behavior 9 properties: applying is idempotent, every other entry
// stays in order, and a file with old scripts ends with one ntm Stop entry.
func TestApplyHookMigration_Properties(t *testing.T) {
	const newCmd = "~/.local/bin/ntm agent-hook stop"
	rapid.Check(t, func(rt *rapid.T) {
		settings := hooksSettingsGen().Draw(rt, "settings")
		indent := rapid.SampledFrom([]string{"  ", "    ", "\t"}).Draw(rt, "indent")
		raw, _ := json.MarshalIndent(settings, "", indent)
		plan, err := PlanHookMigration(settings, newCmd)
		if err != nil {
			rt.Fatal(err)
		}
		once, err := ApplyHookMigration(raw, plan)
		if err != nil {
			rt.Fatalf("apply: %v\n%s", err, raw)
		}
		after, err := decodeSettings(once)
		if err != nil {
			rt.Fatalf("result isn't JSON: %v\n%s", err, once)
		}
		again, err := PlanHookMigration(after, newCmd)
		if err != nil || again.HasChanges() {
			rt.Fatalf("second plan = %+v, %v\n%s", again, err, once)
		}
		twice, _ := ApplyHookMigration(once, again)
		if string(twice) != string(once) {
			rt.Fatalf("not idempotent:\n%s\n%s", once, twice)
		}
		if !reflect.DeepEqual(otherEntries(rt, after), otherEntries(rt, settings)) {
			rt.Fatalf("other entries changed:\n%v\n%v", otherEntries(rt, settings), otherEntries(rt, after))
		}
		if plan.HasChanges() {
			// The old installer registered the stop script under Stop; a
			// file that had it there ends with exactly one ntm Stop entry.
			hadStop := false
			before, _, _ := settingsHookEntries(settings)
			for _, e := range before {
				if e.event == "Stop" && ClassifyOldHookCommand(e.command) == OldHookStop {
					hadStop = true
				}
			}
			ntm := 0
			entries, _, _ := settingsHookEntries(after)
			for _, e := range entries {
				if e.event == "Stop" && IsStopHookCommand(e.command) {
					ntm++
				}
				if kind := ClassifyOldHookCommand(e.command); kind == OldHookStop || kind == OldHookClose {
					rt.Fatalf("old script left: %s", e.command)
				}
			}
			if hadStop && ntm != 1 {
				rt.Fatalf("ntm Stop entries = %d:\n%s", ntm, once)
			}
		}
	})
}

// Loop 8, Behavior 14: the guide's "Roll back" section says reverting the
// migration commit restores the file, and the scripts were never touched.
func TestHookMigration_RollbackRestoresFile(t *testing.T) {
	const newCmd = "~/.local/bin/ntm agent-hook stop"
	repo := t.TempDir()
	gitInit(t, repo)
	original, err := os.ReadFile(filepath.Join("testdata", "migrate", "agentmail.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(repo, ".claude", "settings.json")
	scripts := filepath.Join(repo, ".claude", "hooks")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, original, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{OldStopScript, OldCloseScript, OldWindowScript} {
		if err := os.WriteFile(filepath.Join(scripts, script), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".claude")
	gitRun(t, repo, "commit", "-q", "-m", "hooks")

	decoded, err := decodeSettings(original)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanHookMigration(decoded, newCmd)
	if err != nil || !plan.HasChanges() {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if err := WriteMigratedSettings(settings, original, plan, newCmd); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "commit", "-q", "-am", "Move Agent Mail hooks to ntm agent-hook stop")
	if status := gitRun(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("migration touched more than the settings file:\n%s", status)
	}

	gitRun(t, repo, "revert", "--no-edit", "HEAD")
	if got, _ := os.ReadFile(settings); string(got) != string(original) {
		t.Fatalf("after git revert the file is:\n%s", got)
	}
	if changed := gitRun(t, repo, "diff", "--name-only", "HEAD~2", "--", ".claude/hooks"); changed != "" {
		t.Fatalf("scripts changed: %s", changed)
	}
}
