package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// migrateFixture copies a settings fixture from internal/claudeconfig/testdata/migrate.
func migrateFixture(t *testing.T, name, dest string) {
	t.Helper()
	writeFile(t, dest, string(mustReadFile(t, filepath.Join("..", "claudeconfig", "testdata", "migrate", name))))
}

// useBinaryUnderHome hard-links the test binary to <home>/.local/bin/<name>
// and runs the helpers from there, so os.Executable reports a path under the
// env's home, as an ntm installed in ~/.local/bin would.
func (e *closureEnv) useBinaryUnderHome(t *testing.T, name string) string {
	t.Helper()
	dest := filepath.Join(e.vars["HOME"], ".local", "bin", name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(os.Args[0], dest); err != nil {
		src, err := os.Open(os.Args[0])
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(out, src); err != nil {
			t.Fatal(err)
		}
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
	}
	e.binary = dest
	return dest
}

// migrateTree is a fixture tree under the env's home: Dev/agentmail (the
// real mcp_agent_mail shape, committed), its worktree Dev/agentmail-wt,
// Dev/plain (no old scripts), a match inside node_modules, and
// ntm_Dev/zk with a local settings file.
type migrateTree struct {
	home, agentmail, worktree, plain, zk string
}

func newMigrateTree(t *testing.T, env *closureEnv) migrateTree {
	t.Helper()
	home := env.vars["HOME"]
	tree := migrateTree{
		home:      home,
		agentmail: filepath.Join(home, "Dev", "agentmail"),
		worktree:  filepath.Join(home, "Dev", "agentmail-wt"),
		plain:     filepath.Join(home, "Dev", "plain"),
		zk:        filepath.Join(home, "ntm_Dev", "zk"),
	}
	gitInitForTest(t, tree.agentmail)
	migrateFixture(t, "agentmail.json", filepath.Join(tree.agentmail, ".claude", "settings.json"))
	for _, script := range []string{"check_critical_on_stop.sh", "check_mail_on_close.sh", "ensure_window_id.sh"} {
		writeFile(t, filepath.Join(tree.agentmail, ".claude", "hooks", script), "#!/bin/sh\n")
	}
	gitForTest(t, tree.agentmail, "add", ".claude")
	gitForTest(t, tree.agentmail, "commit", "-q", "-m", "hooks")
	gitForTest(t, tree.agentmail, "worktree", "add", "-q", tree.worktree)

	gitInitForTest(t, tree.plain)
	migrateFixture(t, "plain.json", filepath.Join(tree.plain, ".claude", "settings.json"))
	migrateFixture(t, "project_dir.json", filepath.Join(home, "Dev", "proj", "node_modules", "x", ".claude", "settings.json"))
	migrateFixture(t, "project_dir.json", filepath.Join(tree.zk, ".claude", "settings.local.json"))
	return tree
}

// fileSnapshot is a file's bytes and modification time.
type fileSnapshot struct {
	data    string
	modTime time.Time
}

func snapshotSettings(t *testing.T, home string) map[string]fileSnapshot {
	t.Helper()
	snap := map[string]fileSnapshot{}
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Base(filepath.Dir(p)) == ".claude" {
			info, _ := d.Info()
			snap[p] = fileSnapshot{string(mustReadFile(t, p)), info.ModTime()}
		}
		return nil
	})
	return snap
}

// Loop 8, Behavior 4: no folders means ~/Dev and ~/ntm_Dev, missing ones left
// out; named folders replace them and must exist.
func TestMigrationRoots(t *testing.T) {
	home := t.TempDir()
	if got, err := migrationRoots(nil, home); err != nil || len(got) != 0 {
		t.Fatalf("no defaults present: %v, %v", got, err)
	}
	for _, dir := range []string{"Dev", "ntm_Dev"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Join(home, "Dev"), filepath.Join(home, "ntm_Dev")}
	if got, err := migrationRoots(nil, home); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %v, %v", got, err)
	}
	named := t.TempDir()
	if got, err := migrationRoots([]string{named}, home); err != nil || !reflect.DeepEqual(got, []string{named}) {
		t.Fatalf("named = %v, %v", got, err)
	}
	if _, err := migrationRoots([]string{filepath.Join(named, "missing")}, home); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing named folder err = %v", err)
	}
}

// Loop 8, Behavior 5: each file says what you commit, and where.
func TestCommitStatus(t *testing.T) {
	repo := gitInitForTest(t, filepath.Join(t.TempDir(), "repo"))
	writeFile(t, filepath.Join(repo, ".claude", "settings.json"), "{}")
	gitForTest(t, repo, "add", ".claude")
	gitForTest(t, repo, "commit", "-q", "-m", "x")
	worktree := filepath.Join(t.TempDir(), "wt")
	gitForTest(t, repo, "worktree", "add", "-q", worktree)
	plain := t.TempDir()
	realRepo, _ := filepath.EvalSymlinks(repo)

	for path, want := range map[string]string{
		filepath.Join(repo, ".claude", "settings.json"):       "commit in " + repo,
		filepath.Join(repo, ".claude", "settings.local.json"): "nothing to commit (local file)",
		filepath.Join(worktree, ".claude", "settings.json"):   "shared with " + realRepo + "; commit once there",
		filepath.Join(plain, ".claude", "settings.json"):      "nothing to commit (not in a git repo)",
	} {
		repoOf := migrationRepo(path)
		got, sharedWith := commitStatus(path, repoOf)
		if got != want {
			t.Fatalf("commitStatus(%s) = %q, want %q", path, got, want)
		}
		if wantShared := strings.HasPrefix(want, "shared with "); (sharedWith == realRepo) != wantShared {
			t.Fatalf("commitStatus(%s) sharedWith = %q", path, sharedWith)
		}
	}
}

// migrateJSON is the part of --json output these tests read.
type migrateJSON struct {
	Success bool `json:"success"`
	Applied bool `json:"applied"`
	Files   []struct {
		Path         string `json:"path"`
		Repo         string `json:"repo"`
		CommitStatus string `json:"commit_status"`
		Changes      []struct {
			Action  string `json:"action"`
			Event   string `json:"event"`
			Matcher string `json:"matcher"`
			Old     string `json:"old"`
			New     string `json:"new"`
		} `json:"changes"`
	} `json:"files"`
	Failures []struct {
		Path  string `json:"path"`
		Error string `json:"error"`
	} `json:"failures"`
	Totals struct {
		Files  int `json:"files"`
		Repos  int `json:"repos"`
		Failed int `json:"failed"`
	} `json:"totals"`
}

// Loop 8, Behavior 6: the preview lists every old registration, grouped by
// repo with totals, and writes nothing.
func TestClosure_MigratePreview(t *testing.T) {
	env := newClosureEnv(t)
	env.useBinaryUnderHome(t, "ntm-test")
	tree := newMigrateTree(t, env)
	before := snapshotSettings(t, tree.home)
	statusBefore := gitForTest(t, tree.agentmail, "status", "--porcelain", "--untracked-files=all")
	const newCmd = "~/.local/bin/ntm-test agent-hook stop"

	out := env.run(t, "execute", "agent-hook", "migrate")
	for _, want := range []string{
		tree.agentmail + "\n",
		".claude/settings.json (commit in " + tree.agentmail + ")",
		"replace Stop: /home/user/Dev/mcp_agent_mail/.claude/hooks/check_critical_on_stop.sh",
		"with " + newCmd,
		"remove PostToolUse (Bash): /home/user/Dev/mcp_agent_mail/.claude/hooks/check_mail_on_close.sh (and its now empty group)",
		"keep SessionStart: /home/user/Dev/mcp_agent_mail/.claude/hooks/ensure_window_id.sh (needed for the breadcrumb lookup)",
		tree.worktree + "\n",
		"; commit once there)",
		tree.zk + "\n",
		".claude/settings.local.json (nothing to commit (local file))",
		// The worktree's copy is committed in agentmail, so it isn't another repo
		// (Sprint 1 human test finding 3).
		"Preview only: 3 files would change in 2 repos (1 is a shared worktree copy). Run with --apply to write them.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{tree.plain, "node_modules"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("preview lists %q:\n%s", unwanted, out)
		}
	}

	named := env.run(t, "execute", "agent-hook", "migrate", filepath.Join(tree.home, "ntm_Dev"))
	if !strings.Contains(named, tree.zk) || strings.Contains(named, tree.agentmail) {
		t.Fatalf("named folder preview:\n%s", named)
	}

	var report migrateJSON
	if err := json.Unmarshal([]byte(env.run(t, "execute", "--json", "agent-hook", "migrate")), &report); err != nil {
		t.Fatalf("--json output isn't JSON: %v", err)
	}
	if !report.Success || report.Applied || len(report.Files) != 3 || report.Totals.Files != 3 || report.Totals.Repos != 2 || len(report.Failures) != 0 {
		t.Fatalf("json report = %+v", report)
	}
	if f := report.Files[0]; f.Repo != tree.agentmail || len(f.Changes) != 3 || f.Changes[2].Action != "replace" || f.Changes[2].New != newCmd {
		t.Fatalf("first file = %+v", f)
	}

	if nothing := env.run(t, "execute", "agent-hook", "migrate", tree.plain); strings.TrimSpace(nothing) != "Nothing to migrate." {
		t.Fatalf("plain folder preview = %q", nothing)
	}

	if after := snapshotSettings(t, tree.home); !reflect.DeepEqual(after, before) {
		t.Fatalf("the preview changed files")
	}
	if status := gitForTest(t, tree.agentmail, "status", "--porcelain", "--untracked-files=all"); status != statusBefore {
		t.Fatalf("git status changed:\n%s\nwas\n%s", status, statusBefore)
	}
}

// Loop 8, Behavior 7: a bad file or a binary outside home fails alone, and
// the exit code is non-zero.
func TestClosure_MigratePreviewFailures(t *testing.T) {
	env := newClosureEnv(t)
	env.useBinaryUnderHome(t, "ntm-test")
	tree := newMigrateTree(t, env)
	// It names an old script, so it must parse. (A broken file naming none is
	// skipped, finding 2.)
	broken := filepath.Join(tree.home, "Dev", "broken", ".claude", "settings.json")
	writeFile(t, broken, `{bad "~/.claude/hooks/check_critical_on_stop.sh"`)

	out, err := env.tryRun("execute", "agent-hook", "migrate")
	if err == nil {
		t.Fatalf("a failed file still exited 0:\n%s", out)
	}
	if !strings.Contains(out, "Failed:\n  "+broken+": ") || !strings.Contains(out, tree.agentmail+"\n") || !strings.Contains(out, tree.zk+"\n") {
		t.Fatalf("output:\n%s", out)
	}

	outside := newClosureEnv(t) // runs os.Args[0], which isn't under its home
	outsideTree := newMigrateTree(t, outside)
	out, err = outside.tryRun("execute", "agent-hook", "migrate")
	if err == nil {
		t.Fatalf("a binary outside home still exited 0:\n%s", out)
	}
	for _, repo := range []string{outsideTree.agentmail, outsideTree.zk} {
		if !strings.Contains(out, "  "+repo+": ntm binary is outside your home folder") {
			t.Fatalf("no binary error for %s:\n%s", repo, out)
		}
	}
}

// Loop 8, Sprint 1 human test findings 1 and 2: unreadable folders and
// settings files that don't name an old script are skipped, not failures.
func TestClosure_MigrateSkipsUnreadableAndUnrelated(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("this test needs a non-root user, because root reads every folder")
	}
	env := newClosureEnv(t)
	env.useBinaryUnderHome(t, "ntm-test")
	tree := newMigrateTree(t, env)
	// An old-style Stop entry with no Agent Mail script, as in silmari-oracle/rust.
	writeFile(t, filepath.Join(tree.home, "Dev", "oracle", "rust", ".claude", "settings.json"), `{"hooks":{"Stop":[{"command":"cargo fmt"}]}}`)
	writeFile(t, filepath.Join(tree.home, "Dev", "unrelated-broken", ".claude", "settings.json"), `{bad`)
	locked := filepath.Join(tree.home, "Dev", "data-node", "journal")
	writeFile(t, filepath.Join(locked, "x"), "")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	out := env.run(t, "execute", "agent-hook", "migrate") // exit 0
	if strings.Contains(out, "Failed:") || strings.Contains(out, "oracle") || strings.Contains(out, "unrelated-broken") {
		t.Fatalf("skipped files reported:\n%s", out)
	}
	if strings.Count(out, "Skipped 1 folder that couldn't be read.") != 1 || !strings.Contains(out, tree.agentmail+"\n") {
		t.Fatalf("output:\n%s", out)
	}
}

// commitMigrateRepo makes dir a git repo holding the agentmail fixture and
// its scripts, committed.
func commitMigrateRepo(t *testing.T, dir string) string {
	t.Helper()
	repo := gitInitForTest(t, dir)
	migrateFixture(t, "agentmail.json", filepath.Join(repo, ".claude", "settings.json"))
	for _, script := range []string{"check_critical_on_stop.sh", "check_mail_on_close.sh", "ensure_window_id.sh"} {
		writeFile(t, filepath.Join(repo, ".claude", "hooks", script), "#!/bin/sh\n")
	}
	gitForTest(t, repo, "add", ".claude")
	gitForTest(t, repo, "commit", "-q", "-m", "hooks")
	return repo
}

// fileIdentity is a file's inode and modification time, to show it wasn't
// rewritten.
func fileIdentity(t *testing.T, path string) [2]int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return [2]int64{int64(info.Sys().(*syscall.Stat_t).Ino), info.ModTime().UnixNano()}
}

// Loop 8, Behavior 10: --apply writes minimal diffs, keeps the scripts and
// the file mode, never commits, skips a worktree's shared copy, writes
// through a symlink, and a re-run writes nothing.
func TestClosure_MigrateApply(t *testing.T) {
	env := newClosureEnv(t)
	env.useBinaryUnderHome(t, "ntm-test")
	tree := newMigrateTree(t, env)
	const newCmd = "~/.local/bin/ntm-test agent-hook stop"
	settings := filepath.Join(tree.agentmail, ".claude", "settings.json")
	if err := os.Chmod(settings, 0o640); err != nil {
		t.Fatal(err)
	}
	head := gitForTest(t, tree.agentmail, "rev-parse", "HEAD")
	worktreeSettings := filepath.Join(tree.worktree, ".claude", "settings.json")
	worktreeBefore := string(mustReadFile(t, worktreeSettings))
	// zk's local file is a symlink to a file kept elsewhere.
	local := filepath.Join(tree.zk, ".claude", "settings.local.json")
	target := filepath.Join(tree.home, "ntm_Dev", "zk-settings.json")
	if err := os.Rename(local, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, local); err != nil {
		t.Fatal(err)
	}

	out := env.run(t, "execute", "agent-hook", "migrate", "--apply")
	for _, want := range []string{
		".claude/settings.json (commit in " + tree.agentmail + ")",
		".claude/settings.json (skipped: migrate and commit " + tree.agentmail + ", then update this worktree)",
		".claude/settings.local.json (nothing to commit (local file))",
		"Changed 2 files in 2 repos.",
		"git -C " + tree.agentmail + " add .claude/settings.json && git -C " + tree.agentmail + ` commit -m "Move Agent Mail hooks to ntm agent-hook stop"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("--apply output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "git -C "+tree.worktree) || strings.Contains(out, "git -C "+tree.zk) || strings.Contains(out, "Preview only") {
		t.Fatalf("--apply output:\n%s", out)
	}
	// The skipped copy shows no change lines, which would read as if it
	// changed (Sprint 2 human test note).
	worktreeBlock := out[strings.Index(out, tree.worktree+"\n"):]
	worktreeBlock = worktreeBlock[:strings.Index(worktreeBlock, "\n\n")]
	if strings.Contains(worktreeBlock, "replace ") || strings.Contains(worktreeBlock, "remove ") || strings.Contains(worktreeBlock, "keep ") {
		t.Fatalf("skipped worktree copy lists changes:\n%s", worktreeBlock)
	}

	got := string(mustReadFile(t, settings))
	for _, want := range []string{`"command": "` + newCmd + `",`, "ensure_window_id.sh", "file_reservations active", "file_reservations soon", "file_reservations list"} {
		if !strings.Contains(got, want) {
			t.Fatalf("migrated file lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "check_mail_on_close.sh") || strings.Contains(got, "check_critical_on_stop.sh") || strings.Contains(got, `"matcher": "Bash"`) {
		t.Fatalf("old entries left:\n%s", got)
	}
	// The command line changes, and the Bash group's 11 lines go.
	if numstat := gitForTest(t, tree.agentmail, "diff", "--numstat", "--", ".claude/settings.json"); !strings.HasPrefix(numstat, "1\t11\t") {
		t.Fatalf("numstat = %q\n%s", numstat, gitForTest(t, tree.agentmail, "diff"))
	}
	for _, script := range []string{"check_critical_on_stop.sh", "check_mail_on_close.sh", "ensure_window_id.sh"} {
		if _, err := os.Stat(filepath.Join(tree.agentmail, ".claude", "hooks", script)); err != nil {
			t.Fatalf("script %s: %v", script, err)
		}
	}
	if now := gitForTest(t, tree.agentmail, "rev-parse", "HEAD"); now != head {
		t.Fatalf("--apply committed: HEAD %s, was %s", now, head)
	}
	if info, err := os.Stat(settings); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	if string(mustReadFile(t, worktreeSettings)) != worktreeBefore {
		t.Fatal("the worktree's shared copy was written")
	}
	if info, err := os.Lstat(local); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v, %v", info, err)
	}
	if linked := string(mustReadFile(t, target)); !strings.Contains(linked, newCmd) || strings.Contains(linked, "check_critical_on_stop.sh") {
		t.Fatalf("symlink target:\n%s", linked)
	}

	// A second run writes nothing; the worktree copy is still skipped.
	ids := map[string][2]int64{settings: fileIdentity(t, settings), target: fileIdentity(t, target)}
	again := env.run(t, "execute", "agent-hook", "migrate", "--apply")
	if strings.Contains(again, tree.agentmail+"\n") || strings.Contains(again, tree.zk) || !strings.Contains(again, "No files changed.") {
		t.Fatalf("second --apply:\n%s", again)
	}
	for path, id := range ids {
		if fileIdentity(t, path) != id {
			t.Fatalf("second --apply rewrote %s", path)
		}
	}
	if only := env.run(t, "execute", "agent-hook", "migrate", "--apply", tree.agentmail); strings.TrimSpace(only) != "Nothing to migrate." {
		t.Fatalf("--apply on the migrated repo = %q", only)
	}

	// Following the skip note: commit in the main repo, update the worktree,
	// and nothing is left.
	gitForTest(t, tree.agentmail, "commit", "-q", "-am", "Move Agent Mail hooks to ntm agent-hook stop")
	gitForTest(t, tree.worktree, "merge", "-q", "--ff-only", gitForTest(t, tree.agentmail, "rev-parse", "HEAD"))
	if done := env.run(t, "execute", "agent-hook", "migrate", "--apply"); strings.TrimSpace(done) != "Nothing to migrate." {
		t.Fatalf("after committing and updating the worktree:\n%s", done)
	}

	var report migrateJSON
	if err := json.Unmarshal([]byte(env.run(t, "execute", "--json", "agent-hook", "migrate", "--apply")), &report); err != nil || !report.Success || !report.Applied {
		t.Fatalf("--json --apply = %+v, %v", report, err)
	}
}

// Loop 8, Behavior 11: a failed write leaves its repo alone and the others
// migrated; a binary outside home writes nothing. Both exit 1.
func TestClosure_MigrateApplyFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("this test needs a non-root user, because root writes into read-only folders")
	}
	env := newClosureEnv(t)
	env.useBinaryUnderHome(t, "ntm-test")
	dev := filepath.Join(env.vars["HOME"], "Dev")
	first := commitMigrateRepo(t, filepath.Join(dev, "a"))
	middle := commitMigrateRepo(t, filepath.Join(dev, "b"))
	third := commitMigrateRepo(t, filepath.Join(dev, "c"))
	middleSettings := filepath.Join(middle, ".claude", "settings.json")
	middleBefore := string(mustReadFile(t, middleSettings))
	if err := os.Chmod(filepath.Dir(middleSettings), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(middleSettings), 0o755) })

	out, err := env.tryRun("execute", "agent-hook", "migrate", "--apply")
	if err == nil {
		t.Fatalf("a failed write still exited 0:\n%s", out)
	}
	if !strings.Contains(out, "Failed:\n  "+middleSettings+": ") || !strings.Contains(out, "Changed 2 files in 2 repos.") {
		t.Fatalf("output:\n%s", out)
	}
	for _, repo := range []string{first, third} {
		if got := string(mustReadFile(t, filepath.Join(repo, ".claude", "settings.json"))); strings.Contains(got, "check_critical_on_stop.sh") {
			t.Fatalf("%s wasn't migrated", repo)
		}
	}
	if string(mustReadFile(t, middleSettings)) != middleBefore {
		t.Fatal("the failed repo's file changed")
	}
	if entries, _ := os.ReadDir(filepath.Dir(middleSettings)); len(entries) != 2 { // settings.json and hooks
		t.Fatalf("leftover files in the failed .claude folder: %v", entries)
	}

	outside := newClosureEnv(t) // runs os.Args[0], which isn't under its home
	repo := commitMigrateRepo(t, filepath.Join(outside.vars["HOME"], "Dev", "a"))
	before := snapshotSettings(t, outside.vars["HOME"])
	out, err = outside.tryRun("execute", "agent-hook", "migrate", "--apply")
	if err == nil || !strings.Contains(out, "  "+repo+": ntm binary is outside your home folder") {
		t.Fatalf("binary outside home: %v\n%s", err, out)
	}
	if after := snapshotSettings(t, outside.vars["HOME"]); !reflect.DeepEqual(after, before) {
		t.Fatal("--apply with a binary outside home changed files")
	}
}
