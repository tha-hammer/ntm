package claudeconfig

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
	"pgregory.net/rapid"
)

func codexHooksFile(dir string) string {
	return filepath.Join(dir, ".codex", "hooks.json")
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=ntm test", "-c", "user.email=ntm-test@example.invalid"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

// Loop 7, Behavior 1: a clean folder gets one ntm Stop entry, in Claude's
// shape, with no token in it.
func TestInstallCodexStopHook_NewFile(t *testing.T) {
	dir := t.TempDir()
	result, err := InstallCodexStopHook(dir, "/opt/ntm bin/ntm", "/home/user/proj")
	if err != nil {
		t.Fatal(err)
	}
	if result.HooksPath != codexHooksFile(dir) || !result.Changed || result.Excluded {
		t.Fatalf("result = %+v", result)
	}
	command := StopHookCommand("/opt/ntm bin/ntm", "/home/user/proj")
	want := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": command, "timeout": float64(StopHookTimeoutSeconds)},
	}}}}}
	if got := readSettingsJSON(t, result.HooksPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks file = %v", got)
	}
	info, err := os.Stat(result.HooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(result.HooksPath)
	for _, secret := range []string{"registration_token", "bearer", "Bearer", "eyJ"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("hooks file holds %q: %s", secret, data)
		}
	}
}

// Loop 7, Behavior 1 property: the written command gives the shell back the
// same binary and project paths, whatever quotes and spaces they hold.
func TestInstallCodexStopHook_CommandRoundTripProperty(t *testing.T) {
	parent := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		dir := filepath.Join(parent, fmt.Sprintf("work-%d", run))
		ntm := "/" + rapid.StringMatching(`[a-z '"$ ]{1,20}`).Draw(rt, "ntm")
		key := "/" + rapid.StringMatching(`[a-z '"$ ]{1,20}`).Draw(rt, "key")
		result, err := InstallCodexStopHook(dir, ntm, key)
		if err != nil {
			rt.Fatal(err)
		}
		commands := ntmStopCommands(readSettingsJSON(rt, result.HooksPath))
		if len(commands) != 1 {
			rt.Fatalf("ntm entries = %q", commands)
		}
		out, err := exec.Command("sh", "-c", "printf '%s\\n' "+commands[0]).Output()
		if err != nil {
			rt.Fatal(err)
		}
		want := strings.Join([]string{ntm, "agent-hook", "stop", "--project", key}, "\n") + "\n"
		if string(out) != want {
			rt.Fatalf("sh splits %q into %q, want %q", commands[0], out, want)
		}
	})
}

// Loop 7, Behavior 2: other events, other Stop entries and other keys are
// kept, and a re-run doesn't write.
func TestInstallCodexStopHook_MergeKeepsOthers(t *testing.T) {
	dir := t.TempDir()
	path := codexHooksFile(dir)
	original := map[string]any{
		"description": "team hooks",
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "./check.sh"}}}},
			"Stop":       []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo done"}}}},
		},
	}
	writeSettingsJSON(t, path, original)
	command := StopHookCommand("/bin/ntm", "/proj")
	if _, err := InstallCodexStopHook(dir, "/bin/ntm", "/proj"); err != nil {
		t.Fatal(err)
	}
	got := readSettingsJSON(t, path)
	want := map[string]any{
		"description": "team hooks",
		"hooks": map[string]any{
			"PreToolUse": original["hooks"].(map[string]any)["PreToolUse"],
			"Stop": []any{
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo done"}}},
				map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": float64(StopHookTimeoutSeconds)}}},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks file = %v", got)
	}

	before, _ := os.Stat(path)
	result, err := InstallCodexStopHook(dir, "/bin/ntm", "/proj")
	if err != nil || result.Changed {
		t.Fatalf("second run: %+v, %v", result, err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) || after.Sys().(*syscall.Stat_t).Ino != before.Sys().(*syscall.Stat_t).Ino {
		t.Fatal("an unchanged file was rewritten")
	}
}

// Loop 7, Behavior 2 property: the Codex file merges like the Claude file:
// idempotent, one ntm entry, every other entry kept.
func TestInstallCodexStopHook_IdempotentProperty(t *testing.T) {
	parent := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		dir := filepath.Join(parent, fmt.Sprintf("work-%d", run))
		original := settingsObjectGen().Draw(rt, "hooks")
		writeSettingsJSON(rt, codexHooksFile(dir), original)
		bin := rapid.SampledFrom([]string{"/usr/local/bin/ntm", "/home/x/ntm dev/ntm"}).Draw(rt, "bin")

		if _, err := InstallCodexStopHook(dir, bin, "/proj"); err != nil {
			rt.Fatal(err)
		}
		once := readSettingsJSON(rt, codexHooksFile(dir))
		if result, err := InstallCodexStopHook(dir, bin, "/proj"); err != nil || result.Changed {
			rt.Fatalf("second run: %+v, %v", result, err)
		}
		if got := ntmStopCommands(once); len(got) != 1 || got[0] != StopHookCommand(bin, "/proj") {
			rt.Fatalf("ntm entries = %q", got)
		}
		if !reflect.DeepEqual(withoutNtmEntries(once), withoutNtmEntries(original)) {
			rt.Fatalf("other entries changed:\nbefore %v\nafter  %v", withoutNtmEntries(original), withoutNtmEntries(once))
		}
	})
}

// Loop 7, Behavior 3: ntm entries naming another binary are replaced, and a
// command that only mentions the words is kept.
func TestInstallCodexStopHook_ReplacesOldBinary(t *testing.T) {
	dir := t.TempDir()
	path := codexHooksFile(dir)
	writeSettingsJSON(t, path, map[string]any{"hooks": map[string]any{"Stop": []any{
		map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "echo 'agent-hook stop'"},
			map[string]any{"type": "command", "command": "/old/ntm agent-hook stop --project /p"},
		}},
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "'/tmp/ntm x' agent-hook stop"}}},
	}}})
	if _, err := InstallCodexStopHook(dir, "/new/ntm", "/p"); err != nil {
		t.Fatal(err)
	}
	got := readSettingsJSON(t, path)
	want := map[string]any{"hooks": map[string]any{"Stop": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo 'agent-hook stop'"}}},
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": StopHookCommand("/new/ntm", "/p"), "timeout": float64(StopHookTimeoutSeconds)}}},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hooks file = %v", got)
	}
}

// Loop 7, Behavior 4: a file that isn't the expected shape is left byte for
// byte, and the error names the Codex hook file.
func TestInstallCodexStopHook_UnparseableUnchanged(t *testing.T) {
	for _, content := range []string{`{bad`, `[]`, `{"hooks":[]}`, `{"hooks":{"Stop":{}}}`, `{"hooks":{"Stop":[1]}}`} {
		dir := t.TempDir()
		path := codexHooksFile(dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := InstallCodexStopHook(dir, "/bin/ntm", dir)
		if !errors.Is(err, ErrUnparseableSettings) || result.Changed || !strings.Contains(err.Error(), path) {
			t.Fatalf("content %s: %+v, err %v", content, result, err)
		}
		if after, _ := os.ReadFile(path); string(after) != content {
			t.Fatalf("content %s changed to %s", content, after)
		}
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".codex"), []byte("a file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodexStopHook(dir, "/bin/ntm", dir); err == nil || !strings.Contains(err.Error(), ".codex") {
		t.Fatalf(".codex as a file: err = %v", err)
	}
}

// Loop 7, Behavior 4 property: any bytes that aren't a hooks object stay as
// they were.
func TestInstallCodexStopHook_UnparseableProperty(t *testing.T) {
	parent := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		raw := rapid.SliceOf(rapid.Byte()).Filter(func(b []byte) bool {
			parsed, err := decodeSettings(b)
			if err != nil {
				return true
			}
			_, err = mergeStopHook(parsed, "x")
			return err != nil
		}).Draw(rt, "raw")
		dir := filepath.Join(parent, fmt.Sprintf("work-%d", run))
		path := codexHooksFile(dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			rt.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			rt.Fatal(err)
		}
		_, _ = InstallCodexStopHook(dir, "/bin/ntm", "/p")
		if after, _ := os.ReadFile(path); string(after) != string(raw) {
			rt.Fatalf("%q changed to %q", raw, after)
		}
	})
}

// Loop 7, Behavior 5: ~/.codex/hooks.json is your global Codex hook file, so
// the home folder is refused.
func TestInstallCodexStopHook_RefusesHomeFolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, dir := range []string{home, home + "/"} {
		if _, err := InstallCodexStopHook(dir, "/bin/ntm", dir); !errors.Is(err, ErrHomeFolder) {
			t.Fatalf("InstallCodexStopHook(%q) err = %v, want ErrHomeFolder", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("a .codex folder was created in the home folder: %v", err)
	}
	below := filepath.Join(home, "Dev", "x")
	if _, err := InstallCodexStopHook(below, "/bin/ntm", below); err != nil {
		t.Fatalf("a folder below the home folder was refused: %v", err)
	}
}

// Loop 7, Behavior 6: the Codex file is excluded from git once, also from a
// linked worktree, and a plain folder skips the step.
func TestInstallCodexStopHook_ExcludedFromGit(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	if _, err := InstallStopHook(repo, "/bin/ntm", repo); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		result, err := InstallCodexStopHook(repo, "/bin/ntm", repo)
		if err != nil || !result.Excluded {
			t.Fatalf("run %d: %+v, %v", run, result, err)
		}
	}
	exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if n := strings.Count(string(exclude), CodexHooksPath+"\n"); n != 1 || !strings.Contains(string(exclude), LocalSettingsPath+"\n") {
		t.Fatalf("exclude file:\n%s", exclude)
	}
	if status := gitRun(t, repo, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("git status:\n%s", status)
	}

	gitRun(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	worktree := filepath.Join(t.TempDir(), "wt")
	gitRun(t, repo, "worktree", "add", "-q", worktree)
	if result, err := InstallCodexStopHook(worktree, "/bin/ntm", repo); err != nil || !result.Excluded {
		t.Fatalf("worktree: %+v, %v", result, err)
	}
	if status := gitRun(t, worktree, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("worktree git status:\n%s", status)
	}

	plain := t.TempDir()
	if result, err := InstallCodexStopHook(plain, "/bin/ntm", plain); err != nil || result.Excluded {
		t.Fatalf("plain folder: %+v, %v", result, err)
	}
}

// Loop 7, Behavior 7 (4c decision): a committed or staged hooks.json is
// left alone, with an error that says what to do.
func TestInstallCodexStopHook_TrackedFileUntouched(t *testing.T) {
	for _, commit := range []bool{true, false} {
		repo := t.TempDir()
		gitInit(t, repo)
		path := codexHooksFile(repo)
		content := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo team"}]}]}}`
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, repo, "add", CodexHooksPath)
		if commit {
			gitRun(t, repo, "commit", "-q", "-m", "team hooks")
		}
		result, err := InstallCodexStopHook(repo, "/bin/ntm", repo)
		if !errors.Is(err, ErrCodexHooksTracked) || result.Changed {
			t.Fatalf("commit %v: %+v, err %v", commit, result, err)
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "idle reminder") {
			t.Fatalf("error text = %v", err)
		}
		if after, _ := os.ReadFile(path); string(after) != content {
			t.Fatalf("committed file changed to %s", after)
		}
		exclude, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
		if strings.Contains(string(exclude), CodexHooksPath) {
			t.Fatalf("exclude line added for a tracked file:\n%s", exclude)
		}
	}
}

// --- Behavior 11: approval ---

// evidenceHookCommand and evidenceHookHash come from a real /hooks approval in
// Codex 0.167.0 on 2026-11-10 (evidence record in 07-codex-turn-end-push-tdd.md).
const (
	evidenceHookCommand = "/mnt/agent-tmp/claude-1000/-home-debian-Dev-ntm/ca971f3a-bdb1-4b10-9208-206026adcd94/scratchpad/ev7/hook.sh"
	evidenceHookHash    = "sha256:c105e293c0401ff02815736be8ba46df2ddfd9dafee09fdde792fdd3fd5908c7"
)

// Loop 7, Behavior 11 oracle: ntm's hash matches the ones Codex saved. The
// second command has quotes, spaces and &<>, which Go would HTML-escape by
// default and Codex doesn't (real "Trust all" approval, 2026-11-10).
func TestCodexHookHash_MatchesCodex(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{evidenceHookCommand, evidenceHookHash},
		{`'/tmp/ntm x' agent-hook stop --project '/p&q<r>'`, "sha256:c6187086a90f4760eafb10d066ba8f488cd312be99ca826bbec0c7e9958e6ce4"},
	} {
		if got := codexHookHash(tc.command, StopHookTimeoutSeconds); got != tc.want {
			t.Fatalf("codexHookHash(%q) = %s, want %s", tc.command, got, tc.want)
		}
	}
}

// approvalFixture writes a work folder with ntm's Codex entry and a
// CODEX_HOME holding config (when not nil).
func approvalFixture(t hookTB, parent string, config *string) (codexHome, workDir, hooksPath, command string) {
	t.Helper()
	workDir = filepath.Join(parent, "work")
	codexHome = filepath.Join(parent, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := InstallCodexStopHook(workDir, "/bin/ntm", "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if config != nil {
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(*config), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return codexHome, workDir, result.HooksPath, StopHookCommand("/bin/ntm", "/proj")
}

func tomlKey(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func ptr(s string) *string { return &s }

// Loop 7, Behavior 11: ntm saves Codex's approval for its own entry only.
func TestApproveCodexStopHook(t *testing.T) {
	userSettings := "# my settings\nmodel = \"gpt-6.1-sol\"\n\n[mcp_servers.mail]\nurl = \"http://127.0.0.1:8765/api/\"\n\n[hooks.state.\"/elsewhere/.codex/hooks.json:stop:0:0\"]\ntrusted_hash = \"sha256:abc\"\n"

	t.Run("no config file", func(t *testing.T) {
		home, work, hooks, command := approvalFixture(t, t.TempDir(), nil)
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err != nil || got.State != ApprovalGranted || !got.Changed || got.FolderTrusted {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		want := "[hooks.state." + tomlKey(hooks+":stop:0:0") + "]\ntrusted_hash = \"" + codexHookHash(command, StopHookTimeoutSeconds) + "\"\n"
		if string(data) != want {
			t.Fatalf("config.toml = %q, want %q", data, want)
		}
		if info, _ := os.Stat(filepath.Join(home, "config.toml")); info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", info.Mode().Perm())
		}
		if check, _ := CodexHookApproval(home, hooks, command); check.State != ApprovalGranted {
			t.Fatalf("check after approval = %+v", check)
		}
		_ = work
	})

	t.Run("keeps every other byte, then does nothing", func(t *testing.T) {
		parent := t.TempDir()
		home, work, hooks, command := approvalFixture(t, parent, nil)
		config := userSettings + "\n[projects." + tomlKey(work) + "]\ntrust_level = \"trusted\"\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o640); err != nil {
			t.Fatal(err)
		}
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err != nil || got.State != ApprovalGranted || !got.Changed || !got.FolderTrusted {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		if !strings.HasPrefix(string(data), config) {
			t.Fatalf("original bytes not kept:\n%s", data)
		}
		if info, _ := os.Stat(filepath.Join(home, "config.toml")); info.Mode().Perm() != 0o640 {
			t.Fatalf("mode changed to %v", info.Mode().Perm())
		}
		before, _ := os.Stat(filepath.Join(home, "config.toml"))
		again, err := ApproveCodexStopHook(home, hooks, command)
		if err != nil || again.State != ApprovalGranted || again.Changed {
			t.Fatalf("second approval = %+v, %v", again, err)
		}
		after, _ := os.Stat(filepath.Join(home, "config.toml"))
		if !after.ModTime().Equal(before.ModTime()) || after.Sys().(*syscall.Stat_t).Ino != before.Sys().(*syscall.Stat_t).Ino {
			t.Fatal("an approved config was rewritten")
		}
	})

	t.Run("older hash is replaced in place", func(t *testing.T) {
		parent := t.TempDir()
		home, _, hooks, command := approvalFixture(t, parent, nil)
		config := "[hooks.state." + tomlKey(hooks+":stop:0:0") + "]\ntrusted_hash = \"sha256:old\"\n\n[tui]\nx = 1\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err != nil || got.State != ApprovalGranted || !got.Changed {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		want := strings.Replace(config, "sha256:old", codexHookHash(command, StopHookTimeoutSeconds), 1)
		if string(data) != want {
			t.Fatalf("config.toml = %q, want %q", data, want)
		}
	})

	t.Run("enabled = false is respected", func(t *testing.T) {
		parent := t.TempDir()
		home, _, hooks, command := approvalFixture(t, parent, nil)
		config := "[hooks.state." + tomlKey(hooks+":stop:0:0") + "]\nenabled = false\n"
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err != nil || got.State != ApprovalDisabled || got.Changed {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		if data, _ := os.ReadFile(filepath.Join(home, "config.toml")); string(data) != config {
			t.Fatalf("config changed to %q", data)
		}
	})

	t.Run("unparseable config is left alone", func(t *testing.T) {
		home, _, hooks, command := approvalFixture(t, t.TempDir(), ptr("[hooks\nbroken = "))
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err == nil || got.State != ApprovalUnknown || got.Changed {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		if data, _ := os.ReadFile(filepath.Join(home, "config.toml")); string(data) != "[hooks\nbroken = " {
			t.Fatalf("config changed to %q", data)
		}
	})

	t.Run("unwritable config folder", func(t *testing.T) {
		home, _, hooks, command := approvalFixture(t, t.TempDir(), ptr("model = \"x\"\n"))
		if err := os.Chmod(home, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err == nil || got.State != ApprovalUnknown || got.Changed {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		if data, _ := os.ReadFile(filepath.Join(home, "config.toml")); string(data) != "model = \"x\"\n" {
			t.Fatalf("config changed to %q", data)
		}
	})

	t.Run("ntm entry not in the hooks file", func(t *testing.T) {
		parent := t.TempDir()
		home, _, hooks, _ := approvalFixture(t, parent, nil)
		got, err := ApproveCodexStopHook(home, hooks, StopHookCommand("/other/ntm", "/proj"))
		if err == nil || got.State != ApprovalUnknown {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		if _, statErr := os.Stat(filepath.Join(home, "config.toml")); !os.IsNotExist(statErr) {
			t.Fatalf("config written for a missing entry: %v", statErr)
		}
	})

	t.Run("key uses the ntm group's position", func(t *testing.T) {
		parent := t.TempDir()
		work := filepath.Join(parent, "work")
		writeSettingsJSON(t, codexHooksFile(work), map[string]any{"hooks": map[string]any{"Stop": []any{
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo one"}}},
			map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "echo two"}}},
		}}})
		result, err := InstallCodexStopHook(work, "/bin/ntm", "/proj")
		if err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(parent, "codex-home")
		got, err := ApproveCodexStopHook(home, result.HooksPath, StopHookCommand("/bin/ntm", "/proj"))
		if err != nil || got.State != ApprovalGranted {
			t.Fatalf("approval = %+v, %v", got, err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		if !strings.Contains(string(data), tomlKey(result.HooksPath+":stop:2:0")) {
			t.Fatalf("config.toml = %s", data)
		}
	})
}

// codexConfigGen draws config.toml text that parses: plain keys, comments,
// tables and other hooks' state entries.
func codexConfigGen() *rapid.Generator[string] {
	return rapid.Custom(func(rt *rapid.T) string {
		var b strings.Builder
		if rapid.Bool().Draw(rt, "comment") {
			b.WriteString("# " + rapid.StringMatching(`[a-z ]{0,20}`).Draw(rt, "text") + "\n")
		}
		if rapid.Bool().Draw(rt, "model") {
			b.WriteString("model = \"" + rapid.StringMatching(`[a-z0-9.-]{1,10}`).Draw(rt, "m") + "\"\n")
		}
		for i, n := 0, rapid.IntRange(0, 3).Draw(rt, "tables"); i < n; i++ {
			switch rapid.IntRange(0, 2).Draw(rt, "kind") {
			case 0:
				fmt.Fprintf(&b, "\n[tui%d]\nflag = %v\n", i, rapid.Bool().Draw(rt, "flag"))
			case 1:
				fmt.Fprintf(&b, "\n[projects.\"/p/%d\"]\ntrust_level = \"trusted\"\n", i)
			default:
				fmt.Fprintf(&b, "\n[hooks.state.\"/other/%d/.codex/hooks.json:stop:0:0\"]\ntrusted_hash = \"sha256:%x\"\n", i, rapid.IntRange(0, 1<<20).Draw(rt, "h"))
			}
		}
		return b.String()
	})
}

// Loop 7, Behavior 11 property: approval only adds ntm's table, the check
// then reads granted, and a second approval doesn't write.
func TestApproveCodexStopHook_KeepsOtherBytesProperty(t *testing.T) {
	parent := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		raw := codexConfigGen().Draw(rt, "config")
		home, _, hooks, command := approvalFixture(rt, filepath.Join(parent, fmt.Sprintf("run-%d", run)), &raw)
		var before map[string]any
		if _, err := toml.Decode(raw, &before); err != nil {
			rt.Fatalf("generator made bad TOML: %v\n%s", err, raw)
		}
		got, err := ApproveCodexStopHook(home, hooks, command)
		if err != nil || got.State != ApprovalGranted {
			rt.Fatalf("approval = %+v, %v", got, err)
		}
		data, _ := os.ReadFile(filepath.Join(home, "config.toml"))
		if !strings.HasPrefix(string(data), raw) {
			rt.Fatalf("original bytes not kept:\n%s", data)
		}
		added := strings.TrimPrefix(string(data), raw)
		if !strings.Contains(added, tomlKey(hooks+":stop:0:0")) || strings.Count(added, "[") != 1 {
			rt.Fatalf("added more than ntm's table: %q", added)
		}
		if check, _ := CodexHookApproval(home, hooks, command); check.State != ApprovalGranted {
			rt.Fatalf("check = %+v", check)
		}
		if again, _ := ApproveCodexStopHook(home, hooks, command); again.Changed {
			rt.Fatal("second approval wrote")
		}
	})
}

// Loop 7, Behavior 11: the check never writes, and only an exact hash is
// granted.
func TestCodexHookApproval_ReadOnly(t *testing.T) {
	parent := t.TempDir()
	home, _, hooks, command := approvalFixture(t, parent, nil)
	key := tomlKey(hooks + ":stop:0:0")
	for _, tc := range []struct {
		name   string
		config *string
		want   CodexApprovalState
	}{
		{"no file", nil, ApprovalNeeded},
		{"exact hash", ptr("[hooks.state." + key + "]\ntrusted_hash = \"" + codexHookHash(command, StopHookTimeoutSeconds) + "\"\n"), ApprovalGranted},
		{"older hash", ptr("[hooks.state." + key + "]\ntrusted_hash = \"sha256:old\"\n"), ApprovalNeeded},
		{"disabled", ptr("[hooks.state." + key + "]\nenabled = false\ntrusted_hash = \"" + codexHookHash(command, StopHookTimeoutSeconds) + "\"\n"), ApprovalDisabled},
		{"bad toml", ptr("[[["), ApprovalUnknown},
	} {
		path := filepath.Join(home, "config.toml")
		_ = os.Remove(path)
		if tc.config != nil {
			if err := os.WriteFile(path, []byte(*tc.config), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		before, _ := os.ReadFile(path)
		got, _ := CodexHookApproval(home, hooks, command)
		if got.State != tc.want {
			t.Fatalf("%s: state = %v, want %v", tc.name, got.State, tc.want)
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatalf("%s: the check changed config.toml", tc.name)
		}
	}
}

// Loop 7, Behavior 11: CODEX_HOME wins, then ~/.codex.
func TestCodexHomeDir(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("CODEX_HOME", "")
	if got := CodexHomeDir(); got != "/home/user/.codex" {
		t.Fatalf("CodexHomeDir() = %q", got)
	}
	t.Setenv("CODEX_HOME", "/tmp/ch")
	if got := CodexHomeDir(); got != "/tmp/ch" {
		t.Fatalf("CodexHomeDir() = %q", got)
	}
}
