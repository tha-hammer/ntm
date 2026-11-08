package claudeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"pgregory.net/rapid"
)

// settingsValueGen draws JSON values as encoding/json decodes them.
func settingsValueGen() *rapid.Generator[any] {
	return rapid.OneOf(
		rapid.Map(rapid.StringMatching(`[a-z ]{0,8}`), func(s string) any { return s }),
		rapid.Map(rapid.IntRange(-5, 500), func(n int) any { return float64(n) }),
		rapid.Map(rapid.Bool(), func(b bool) any { return b }),
		rapid.Just[any](nil),
		rapid.Map(rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}`), 0, 3), func(items []string) any {
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = item
			}
			return out
		}),
	)
}

// commandHookGen draws one command hook, sometimes an ntm Stop entry from
// another binary path.
func commandHookGen() *rapid.Generator[any] {
	return rapid.Custom(func(rt *rapid.T) any {
		var command string
		if rapid.Bool().Draw(rt, "ntm") {
			bin := rapid.SampledFrom([]string{"/old/ntm", "/usr/local/bin/ntm", "/home/x/ntm dev/ntm"}).Draw(rt, "bin")
			command = StopHookCommand(bin, rapid.SampledFrom([]string{"/proj", "/other"}).Draw(rt, "project"))
		} else {
			command = rapid.SampledFrom([]string{"~/.claude/hooks/check_critical_on_stop.sh", "echo done", "bd sync"}).Draw(rt, "cmd")
		}
		hook := map[string]any{"type": "command", "command": command}
		if rapid.Bool().Draw(rt, "timeout") {
			hook["timeout"] = float64(rapid.IntRange(1, 60).Draw(rt, "seconds"))
		}
		return hook
	})
}

func settingsObjectGen() *rapid.Generator[map[string]any] {
	return rapid.Custom(func(rt *rapid.T) map[string]any {
		obj := map[string]any{}
		for i, n := 0, rapid.IntRange(0, 3).Draw(rt, "keys"); i < n; i++ {
			obj[rapid.SampledFrom([]string{"model", "env", "theme", "x"}).Draw(rt, "key")] = settingsValueGen().Draw(rt, "value")
		}
		if rapid.Bool().Draw(rt, "permissions") {
			obj["permissions"] = map[string]any{"allow": []any{"Bash(ls:*)"}}
		}
		if rapid.Bool().Draw(rt, "hooks") {
			hooks := map[string]any{}
			for i, n := 0, rapid.IntRange(0, 3).Draw(rt, "events"); i < n; i++ {
				event := rapid.SampledFrom([]string{"Stop", "PostToolUse", "Notification"}).Draw(rt, "event")
				groups := []any{}
				for j, g := 0, rapid.IntRange(0, 3).Draw(rt, "groups"); j < g; j++ {
					list := []any{}
					for k, h := 0, rapid.IntRange(0, 3).Draw(rt, "hooks"); k < h; k++ {
						list = append(list, commandHookGen().Draw(rt, "hook"))
					}
					group := map[string]any{"hooks": list}
					if event != "Stop" && rapid.Bool().Draw(rt, "matcher") {
						group["matcher"] = "Bash"
					}
					groups = append(groups, group)
				}
				hooks[event] = groups
			}
			obj["hooks"] = hooks
		}
		return obj
	})
}

// hookTB is what the settings test helpers need from *testing.T and *rapid.T.
type hookTB interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

func writeSettingsJSON(t hookTB, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettingsJSON(t hookTB, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed
}

// ntmStopCommands lists the commands of the ntm entries under hooks.Stop.
func ntmStopCommands(settings map[string]any) []string {
	var commands []string
	hooks, _ := settings["hooks"].(map[string]any)
	groups, _ := hooks["Stop"].([]any)
	for _, group := range groups {
		list, _ := group.(map[string]any)["hooks"].([]any)
		for _, hook := range list {
			if command, _ := hook.(map[string]any)["command"].(string); IsStopHookCommand(command) {
				commands = append(commands, command)
			}
		}
	}
	return commands
}

// withoutNtmEntries removes ntm Stop entries and the groups that removal
// emptied, then drops a Stop list or hooks map left empty. The writer adds one
// entry in its own group, so both sides of the comparison normalize the same.
func withoutNtmEntries(settings map[string]any) map[string]any {
	data, _ := json.Marshal(settings)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	hooks, ok := out["hooks"].(map[string]any)
	if !ok {
		return out
	}
	if groups, ok := hooks["Stop"].([]any); ok {
		kept := []any{}
		for _, group := range groups {
			groupMap := group.(map[string]any)
			list := groupMap["hooks"].([]any)
			remaining := []any{}
			for _, hook := range list {
				if command, _ := hook.(map[string]any)["command"].(string); !IsStopHookCommand(command) {
					remaining = append(remaining, hook)
				}
			}
			if len(remaining) == 0 && len(list) > 0 {
				continue
			}
			groupMap["hooks"] = remaining
			kept = append(kept, groupMap)
		}
		if len(kept) == 0 {
			delete(hooks, "Stop")
		} else {
			hooks["Stop"] = kept
		}
	}
	if len(hooks) == 0 {
		delete(out, "hooks")
	}
	return out
}

func TestWriteStopHook_MergeProperty(t *testing.T) {
	parent := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		dir := filepath.Join(parent, fmt.Sprintf("work-%d", run))
		path := filepath.Join(dir, ".claude", "settings.local.json")
		original := settingsObjectGen().Draw(rt, "settings")
		writeSettingsJSON(rt, path, original)
		bin := rapid.SampledFrom([]string{"/usr/local/bin/ntm", "/home/x/ntm dev/ntm"}).Draw(rt, "bin")
		command := StopHookCommand(bin, "/proj")

		if _, _, err := WriteStopHook(dir, command); err != nil {
			rt.Fatal(err)
		}
		once := readSettingsJSON(rt, path)
		if _, changed, err := WriteStopHook(dir, command); err != nil || changed {
			rt.Fatalf("second write: changed %v, err %v", changed, err)
		}
		twice := readSettingsJSON(rt, path)

		if !reflect.DeepEqual(once, twice) {
			rt.Fatalf("not idempotent:\n%v\n%v", once, twice)
		}
		if got := ntmStopCommands(once); len(got) != 1 || got[0] != command {
			rt.Fatalf("ntm entries = %q, want just %q", got, command)
		}
		if !reflect.DeepEqual(withoutNtmEntries(once), withoutNtmEntries(original)) {
			rt.Fatalf("other entries changed:\nbefore %v\nafter  %v", withoutNtmEntries(original), withoutNtmEntries(once))
		}
	})
}

func TestWriteStopHook_NewFileAndFolder(t *testing.T) {
	for _, content := range []string{"", "null"} {
		dir := t.TempDir()
		if content != "" {
			if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".claude", "settings.local.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		path, changed, err := WriteStopHook(dir, StopHookCommand("/bin/ntm", dir))
		if err != nil || !changed || path != filepath.Join(dir, ".claude", "settings.local.json") {
			t.Fatalf("content %q: path %q, changed %v, err %v", content, path, changed, err)
		}
		got := readSettingsJSON(t, path)
		want := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": StopHookCommand("/bin/ntm", dir), "timeout": float64(StopHookTimeoutSeconds)},
		}}}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("content %q: settings = %v", content, got)
		}
	}
}

func TestWriteStopHook_InvalidJSONUnchanged(t *testing.T) {
	for _, content := range []string{
		`{bad`,
		`[1,2]`,
		`{"hooks": "not an object"}`,
		`{"hooks": {"Stop": {"not": "a list"}}}`,
		`{"hooks": {"Stop": ["not a group"]}}`,
		`{"hooks": {"Stop": [{"hooks": "not a list"}]}}`,
		`{"hooks": {"Stop": [{"hooks": ["not a hook"]}]}}`,
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, ".claude", "settings.local.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		_, changed, err := WriteStopHook(dir, StopHookCommand("/bin/ntm", dir))
		if !errors.Is(err, ErrUnparseableSettings) || changed || !strings.Contains(err.Error(), path) {
			t.Fatalf("content %s: changed %v, err %v, want ErrUnparseableSettings naming the file", content, changed, err)
		}
		if after, _ := os.ReadFile(path); string(after) != content {
			t.Fatalf("content %s changed to %s", content, after)
		}
	}
}

func TestWriteStopHook_ReplacesOldPathAndKeepsOtherHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.local.json")
	old := StopHookCommand("/old/ntm", "/proj")
	writeSettingsJSON(t, path, map[string]any{"hooks": map[string]any{"Stop": []any{
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "~/.claude/hooks/check_critical_on_stop.sh"},
			map[string]any{"type": "command", "command": old, "timeout": float64(5)},
		}},
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": old}}},
	}}})

	command := StopHookCommand("/new/ntm", "/proj")
	if _, changed, err := WriteStopHook(dir, command); err != nil || !changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	got := readSettingsJSON(t, path)
	want := map[string]any{"hooks": map[string]any{"Stop": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "~/.claude/hooks/check_critical_on_stop.sh"}}},
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": float64(5)}}},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %v", got)
	}
}

func TestWriteStopHook_NoChangeNoWrite(t *testing.T) {
	dir := t.TempDir()
	command := StopHookCommand("/bin/ntm", dir)
	path, _, err := WriteStopHook(dir, command)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := WriteStopHook(dir, command); err != nil || changed {
		t.Fatalf("changed %v, err %v", changed, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Sys().(*syscall.Stat_t).Ino != before.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("an unchanged file was rewritten")
	}
}

// TestInstallStopHook_RefusesHomeFolder: in the home folder, the local
// settings file sits inside ~/.claude, next to the user's own settings, so a
// wrong project lookup must never put the hook there.
func TestInstallStopHook_RefusesHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{home, home + "/", filepath.Join(home, "sub", "..")} {
		_, err := InstallStopHook(dir, "/bin/ntm", dir)
		if !errors.Is(err, ErrHomeFolder) {
			t.Fatalf("InstallStopHook(%q) err = %v, want ErrHomeFolder", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatalf("a .claude folder was created in the home folder: %v", err)
	}

	below := filepath.Join(home, "proj")
	if _, err := InstallStopHook(below, "/bin/ntm", below); err != nil {
		t.Fatalf("a folder below the home folder was refused: %v", err)
	}
}

func TestStopHookCommand_QuotesPaths(t *testing.T) {
	for _, tc := range []struct{ bin, project string }{
		{"/usr/local/bin/ntm", "/home/x/proj"},
		{"/home/x/ntm dev/ntm", "/home/x/my project"},
		{"/tmp/it's/ntm", "/home/x/it's here"},
	} {
		command := StopHookCommand(tc.bin, tc.project)
		if !IsStopHookCommand(command) {
			t.Fatalf("IsStopHookCommand(%q) = false", command)
		}
		out, err := exec.Command("sh", "-c", "printf '%s\\n' "+command).Output()
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{tc.bin, "agent-hook", "stop", "--project", tc.project}, "\n") + "\n"
		if string(out) != want {
			t.Fatalf("sh splits %q into %q, want %q", command, out, want)
		}
	}
	for _, other := range []string{"echo 'agent-hook stop'", "~/.claude/hooks/check_critical_on_stop.sh", "ntm agent-hook stopper", "'unclosed agent-hook stop", " agent-hook stop", ""} {
		if IsStopHookCommand(other) {
			t.Fatalf("IsStopHookCommand(%q) = true", other)
		}
	}
}

func TestResolveClaudeSettingsPathHonorsEnvVar(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/claude-alt")
	got, fromEnv, err := ResolveClaudeSettingsPath()
	if err != nil {
		t.Fatalf("ResolveClaudeSettingsPath: %v", err)
	}
	if !fromEnv {
		t.Errorf("fromEnv = false; want true")
	}
	if want := "/tmp/claude-alt/settings.json"; got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestResolveClaudeSettingsPathIgnoresBlankEnvVar(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "   ")
	got, fromEnv, err := ResolveClaudeSettingsPath()
	if err != nil {
		t.Fatalf("ResolveClaudeSettingsPath: %v", err)
	}
	if fromEnv {
		t.Errorf("fromEnv = true; want false for blank env")
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".claude", "settings.json"); got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestReadModelAbsentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if hasModel || model != "" {
		t.Errorf("absent file -> (%q, %t); want (\"\", false)", model, hasModel)
	}
}

func TestReadModelEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if hasModel || model != "" {
		t.Errorf("empty file -> (%q, %t); want (\"\", false)", model, hasModel)
	}
}

func TestReadModelMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadModel(path)
	if err == nil {
		t.Errorf("ReadModel on malformed JSON: expected error, got nil")
	}
}

func TestReadModelMissingField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if hasModel || model != "" {
		t.Errorf("missing field -> (%q, %t); want (\"\", false)", model, hasModel)
	}
}

func TestReadModelPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "opus-4.7" {
		t.Errorf("got (%q, %t); want (\"opus-4.7\", true)", model, hasModel)
	}
}

func TestWriteModelPreservesOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark","font_size":14}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteModel(path, "sonnet-4.6"); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse post-write: %v", err)
	}
	if got["model"] != "sonnet-4.6" {
		t.Errorf("model = %v; want sonnet-4.6", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme not preserved: %v", got["theme"])
	}
	// json.Unmarshal parses numbers as float64 by default.
	if got["font_size"].(float64) != 14 {
		t.Errorf("font_size not preserved: %v", got["font_size"])
	}
}

func TestWriteModelEmptyStringRemovesField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteModel(path, ""); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["model"]; present {
		t.Errorf("model field should have been removed, got: %v", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme lost: %v", got["theme"])
	}
}

func TestWriteModelDoesNotCreateEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "settings.json")
	if err := WriteModel(path, ""); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s not to exist, got stat err=%v", path, err)
	}
}

func TestWriteModelCreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "settings.json")
	if err := WriteModel(path, "haiku-4.5"); err != nil {
		t.Fatalf("WriteModel: %v", err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "haiku-4.5" {
		t.Errorf("got (%q, %t); want (\"haiku-4.5\", true)", model, hasModel)
	}
}

func TestSnapshotRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "state", "pre.json")

	// User had model=opus before the swarm.
	if err := os.WriteFile(settings, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm mutates the model mid-run.
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("mid-run WriteModel: %v", err)
	}

	// End-of-swarm restore.
	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Verify the original value is back, and theme/other fields are intact.
	model, hasModel, err := ReadModel(settings)
	if err != nil {
		t.Fatal(err)
	}
	if !hasModel || model != "opus-4.7" {
		t.Errorf("post-restore model = (%q, %t); want (\"opus-4.7\", true)", model, hasModel)
	}
	raw, _ := os.ReadFile(settings)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["theme"] != "dark" {
		t.Errorf("theme lost after restore: %v", got["theme"])
	}

	// Snapshot should be consumed.
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Errorf("snapshot still present after restore: %v", err)
	}

	// Restore idempotency: second call is a no-op, not an error.
	if err := Restore(snap); err != nil {
		t.Errorf("Restore second call: %v", err)
	}
}

func TestSnapshotRestoreWhenUserHadNoModelField(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre.json")

	// User had a settings.json but no `model` field.
	if err := os.WriteFile(settings, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm sets the model.
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("mid-run WriteModel: %v", err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Restore must remove the model field rather than leave the swarm's value.
	_, hasModel, err := ReadModel(settings)
	if err != nil {
		t.Fatal(err)
	}
	if hasModel {
		t.Errorf("Restore must remove model field that user did not have pre-swarm")
	}
	raw, _ := os.ReadFile(settings)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	if got["theme"] != "dark" {
		t.Errorf("theme lost after restore: %v", got["theme"])
	}
}

func TestRestoreMissingSnapshotIsNoOp(t *testing.T) {
	dir := t.TempDir()
	snap := filepath.Join(dir, "does-not-exist.json")
	if err := Restore(snap); err != nil {
		t.Errorf("Restore on absent snapshot should be a no-op, got %v", err)
	}
}

func TestSnapshotRestoreWhenNoSettingsFileExistedPreSwarm(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre.json")

	// Pre-swarm: user has no settings.json at all.
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("expected no settings.json pre-snapshot, got stat err=%v", err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm creates settings.json with model set, as Claude Code does on
	// --model invocations.
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("swarm WriteModel: %v", err)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Fatalf("swarm should have created settings.json: %v", err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Post-restore: settings.json should be gone entirely — matches the
	// pre-swarm state where no file existed.
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("expected settings.json removed post-restore, got stat err=%v", err)
	}

	// Snapshot should also be consumed.
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Errorf("expected snapshot removed post-restore, got stat err=%v", err)
	}
}

func TestRestoreKeepsSettingsWithOtherFieldsWhenUserHadNoFile(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre.json")

	// Pre-swarm: no settings.json.
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Swarm (or something else) creates settings.json with model AND some
	// other fields (Claude Code might persist theme / MCP config changes
	// during the swarm). We should not nuke those.
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet-4.6","theme":"dark","mcp_servers":["a","b"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Post-restore: settings.json still exists (we don't know the provenance
	// of `theme` / `mcp_servers`), but `model` is gone.
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings should still exist: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, present := got["model"]; present {
		t.Errorf("model should be removed, got: %v", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme should be preserved, got: %v", got["theme"])
	}
	if servers, ok := got["mcp_servers"].([]any); !ok || len(servers) != 2 {
		t.Errorf("mcp_servers should be preserved, got: %v", got["mcp_servers"])
	}
}

func TestRemoveIfEmptyObjectNoOpsOnNonEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("non-empty file should not have been removed: %v", err)
	}
}

func TestRemoveIfEmptyObjectRemovesEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("empty file should have been removed, got stat err=%v", err)
	}
}

func TestRemoveIfEmptyObjectNoOpsOnMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.json")
	if err := removeIfEmptyObject(path); err != nil {
		t.Errorf("absent path should not error, got: %v", err)
	}
}

func TestRemoveIfEmptyObjectNoOpsOnNonJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("non-JSON file should not have been removed: %v", err)
	}
}

// JSON `null` unmarshals into a map as nil with len==0, which looks
// deceptively like an empty object. removeIfEmptyObject must NOT delete
// such a file — "null" is not `{}`, it's just unusual content we don't
// understand.
func TestRemoveIfEmptyObjectNoOpsOnJSONNull(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("'null' content file should not have been removed: %v", err)
	}
}

// Regression: if the user's settings.json contains literal `null` (valid
// JSON that Go unmarshals into a map as nil, NOT into an empty map), a
// subsequent `parsed[ModelKey] = model` assignment would panic with
// "assignment to entry in nil map". Fixed in WriteModel by resetting a
// nil parsed map to an empty one before assignment.
func TestWriteModelHandlesJSONNullSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Both paths — writing a model, and clearing the model — must not
	// panic on null content.
	if err := WriteModel(path, "sonnet-4.6"); err != nil {
		t.Fatalf("WriteModel with null settings: %v", err)
	}
	model, hasModel, err := ReadModel(path)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "sonnet-4.6" {
		t.Errorf("post-write-on-null got (%q, %t); want (\"sonnet-4.6\", true)", model, hasModel)
	}

	// Reset to `null` and exercise the clear-model path too.
	if err := os.WriteFile(path, []byte(`null`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteModel(path, ""); err != nil {
		t.Fatalf("WriteModel(empty) with null settings: %v", err)
	}
	// Clearing a field from a null-content file ends up writing `{}`.
	// Not ideal but safer than panicking.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Errorf("clear-on-null produced %q; expected \"{}\"", raw)
	}
}

func TestRemoveIfEmptyObjectNoOpsOnJSONArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeIfEmptyObject(path); err != nil {
		t.Fatalf("removeIfEmptyObject: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("'[]' content file should not have been removed: %v", err)
	}
}

// Adversarial: settings.json had a model pre-snapshot, but something
// (rogue swarm agent, external process) deleted the file entirely
// before Restore ran. Restore must put the model back — recreating
// the file with at least the model field. Other sibling fields the
// user had (theme, mcp_servers, etc.) are unrecoverable from this
// snapshot since Snapshot only captures the model value, but at
// minimum the Claude Code model selection itself must come back.
func TestRestoreRecreatesDeletedSettingsFile(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "snap.json")

	if err := os.WriteFile(settings, []byte(`{"model":"opus-4.7","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(settings, snap); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Adversarial: something removed the settings file entirely.
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}

	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	model, hasModel, err := ReadModel(settings)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "opus-4.7" {
		t.Errorf("expected (opus-4.7, true), got (%q, %t)", model, hasModel)
	}
}

func TestResolveSnapshotPathHonorsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	got, err := ResolveSnapshotPath()
	if err != nil {
		t.Fatalf("ResolveSnapshotPath: %v", err)
	}
	if want := filepath.Join("/tmp/xdg-state", "ntm", SnapshotFilename); got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestResolveSnapshotPathFallsBackToLocalState(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	got, err := ResolveSnapshotPath()
	if err != nil {
		t.Fatalf("ResolveSnapshotPath: %v", err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".local", "state", "ntm", SnapshotFilename); got != want {
		t.Errorf("path = %q; want %q", got, want)
	}
}

func TestEnsureSnapshotCreatesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "snap", "pre-swarm-claude-model.json")
	if err := os.WriteFile(settings, []byte(`{"model":"opus-4.7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := EnsureSnapshot(settings, snap)
	if err != nil {
		t.Fatalf("EnsureSnapshot: %v", err)
	}
	if !created {
		t.Errorf("created = false; want true on fresh run")
	}
	if _, err := os.Stat(snap); err != nil {
		t.Errorf("snapshot file missing after EnsureSnapshot: %v", err)
	}
}

func TestEnsureSnapshotSkipsWhenAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre-swarm-claude-model.json")

	// First swarm captured pre-launch state ("opus-4.7").
	if err := os.WriteFile(settings, []byte(`{"model":"opus-4.7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSnapshot(settings, snap); err != nil {
		t.Fatalf("first EnsureSnapshot: %v", err)
	}

	// Mid-run, the first swarm overwrote model -> "sonnet-4.6".
	if err := os.WriteFile(settings, []byte(`{"model":"sonnet-4.6"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Second overlapping swarm calls EnsureSnapshot; must NOT clobber the
	// original snapshot with the mid-run value.
	created, err := EnsureSnapshot(settings, snap)
	if err != nil {
		t.Fatalf("second EnsureSnapshot: %v", err)
	}
	if created {
		t.Errorf("created = true; want false when snapshot already present")
	}

	// Restore should put back "opus-4.7", not "sonnet-4.6".
	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	model, hasModel, err := ReadModel(settings)
	if err != nil {
		t.Fatalf("ReadModel: %v", err)
	}
	if !hasModel || model != "opus-4.7" {
		t.Errorf("Restore wrote %q; want \"opus-4.7\" (overlapping-swarm scenario)", model)
	}
}

func TestEnsureSnapshotThenRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	snap := filepath.Join(dir, "pre-swarm-claude-model.json")

	// Pre-swarm: user has haiku-4.5 + theme.
	if err := os.WriteFile(settings, []byte(`{"model":"haiku-4.5","theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Swarm launch: capture snapshot, then swarm sets model -> sonnet.
	if _, err := EnsureSnapshot(settings, snap); err != nil {
		t.Fatalf("EnsureSnapshot: %v", err)
	}
	if err := WriteModel(settings, "sonnet-4.6"); err != nil {
		t.Fatalf("mid-run WriteModel: %v", err)
	}

	// Swarm end: restore.
	if err := Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "haiku-4.5" {
		t.Errorf("post-restore model = %v; want haiku-4.5", got["model"])
	}
	if got["theme"] != "dark" {
		t.Errorf("theme lost across snapshot/mutate/restore cycle: %v", got["theme"])
	}

	// Restore twice is a no-op (snapshot file is gone).
	if err := Restore(snap); err != nil {
		t.Errorf("second Restore should be no-op, got: %v", err)
	}
}
