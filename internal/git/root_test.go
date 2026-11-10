package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindProjectRoot(t *testing.T) {
	// Create a temporary directory
	tmpDir, err := os.MkdirTemp("", "ntm-git-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skip("git init failed, skipping test:", err)
	}

	// Create a subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test from root
	root, err := FindProjectRoot(tmpDir)
	if err != nil {
		t.Errorf("FindProjectRoot(root) error: %v", err)
	}
	// On Mac/Linux, /tmp might be a symlink to /private/tmp, so resolve symlinks
	realTmp, _ := filepath.EvalSymlinks(tmpDir)
	realRoot, _ := filepath.EvalSymlinks(root)
	if realRoot != realTmp {
		t.Errorf("expected root %s, got %s", realTmp, realRoot)
	}

	// Test from subdir
	root, err = FindProjectRoot(subDir)
	if err != nil {
		t.Errorf("FindProjectRoot(subdir) error: %v", err)
	}
	realRoot, _ = filepath.EvalSymlinks(root)
	if realRoot != realTmp {
		t.Errorf("expected root %s, got %s", realTmp, realRoot)
	}
}

const testExcludePattern = ".claude/settings.local.json"

// excludeLines counts the lines equal to pattern in the repo's info/exclude.
func excludeLines(t *testing.T, dir, pattern string) (int, string) {
	t.Helper()
	path := gitOutput(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == pattern {
			count++
		}
	}
	return count, string(data)
}

func writeSettingsFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, testExcludePattern), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureExcluded_OnceAndNotARepo(t *testing.T) {
	t.Run("repo with no info folder yet", func(t *testing.T) {
		repo := t.TempDir()
		gitOutput(t, repo, "init", "-q")
		if err := os.RemoveAll(filepath.Join(repo, ".git", "info")); err != nil {
			t.Fatal(err)
		}
		writeSettingsFile(t, repo)
		for run := 1; run <= 2; run++ {
			if ok, err := EnsureExcluded(repo, testExcludePattern); err != nil || !ok {
				t.Fatalf("run %d: ok %v, err %v", run, ok, err)
			}
		}
		if n, data := excludeLines(t, repo, testExcludePattern); n != 1 {
			t.Fatalf("exclude lines = %d:\n%s", n, data)
		}
		if status := gitOutput(t, repo, "status", "--porcelain", "--untracked-files=all"); strings.Contains(status, "settings.local.json") {
			t.Fatalf("git status lists the file:\n%s", status)
		}
	})

	t.Run("exclude file with no trailing newline", func(t *testing.T) {
		repo := t.TempDir()
		gitOutput(t, repo, "init", "-q")
		path := filepath.Join(repo, ".git", "info", "exclude")
		if err := os.WriteFile(path, []byte("# comment\n*.log"), 0o644); err != nil {
			t.Fatal(err)
		}
		if ok, err := EnsureExcluded(repo, testExcludePattern); err != nil || !ok {
			t.Fatalf("ok %v, err %v", ok, err)
		}
		data, _ := os.ReadFile(path)
		if string(data) != "# comment\n*.log\n"+testExcludePattern+"\n" {
			t.Fatalf("exclude file = %q", data)
		}
	})

	t.Run("linked worktree uses the common exclude file", func(t *testing.T) {
		repo := t.TempDir()
		gitOutput(t, repo, "init", "-q")
		gitOutput(t, repo, "-c", "user.name=ntm test", "-c", "user.email=ntm-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "first")
		worktree := filepath.Join(t.TempDir(), "wt")
		gitOutput(t, repo, "worktree", "add", "-q", worktree)
		writeSettingsFile(t, worktree)
		if ok, err := EnsureExcluded(worktree, testExcludePattern); err != nil || !ok {
			t.Fatalf("ok %v, err %v", ok, err)
		}
		if n, data := excludeLines(t, repo, testExcludePattern); n != 1 {
			t.Fatalf("main repo exclude lines = %d:\n%s", n, data)
		}
		if status := gitOutput(t, worktree, "status", "--porcelain", "--untracked-files=all"); strings.Contains(status, "settings.local.json") {
			t.Fatalf("worktree git status lists the file:\n%s", status)
		}
	})

	t.Run("plain folder", func(t *testing.T) {
		dir := t.TempDir()
		if ok, err := EnsureExcluded(dir, testExcludePattern); err != nil || ok {
			t.Fatalf("ok %v, err %v, want false and no error", ok, err)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("plain folder gained files: %v", entries)
		}
	})
}

// TestIsTracked: a committed or staged file is tracked. An untracked or
// ignored file, or a folder outside git, is not. (Loop 7, Behavior 7)
func TestIsTracked(t *testing.T) {
	const path = ".codex/hooks.json"
	write := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	newRepo := func(t *testing.T) string {
		t.Helper()
		repo := t.TempDir()
		gitOutput(t, repo, "init", "-q")
		return repo
	}

	t.Run("committed", func(t *testing.T) {
		repo := newRepo(t)
		write(t, repo)
		gitOutput(t, repo, "add", path)
		gitOutput(t, repo, "-c", "user.name=ntm test", "-c", "user.email=ntm-test@example.invalid", "commit", "-q", "-m", "hooks")
		if tracked, err := IsTracked(repo, path); err != nil || !tracked {
			t.Fatalf("tracked %v, err %v", tracked, err)
		}
	})
	t.Run("staged only", func(t *testing.T) {
		repo := newRepo(t)
		write(t, repo)
		gitOutput(t, repo, "add", path)
		if tracked, err := IsTracked(repo, path); err != nil || !tracked {
			t.Fatalf("tracked %v, err %v", tracked, err)
		}
	})
	t.Run("untracked", func(t *testing.T) {
		repo := newRepo(t)
		write(t, repo)
		if tracked, err := IsTracked(repo, path); err != nil || tracked {
			t.Fatalf("tracked %v, err %v", tracked, err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		repo := newRepo(t)
		if tracked, err := IsTracked(repo, path); err != nil || tracked {
			t.Fatalf("tracked %v, err %v", tracked, err)
		}
	})
	t.Run("ignored", func(t *testing.T) {
		repo := newRepo(t)
		write(t, repo)
		if _, err := EnsureExcluded(repo, path); err != nil {
			t.Fatal(err)
		}
		if tracked, err := IsTracked(repo, path); err != nil || tracked {
			t.Fatalf("tracked %v, err %v", tracked, err)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir)
		if tracked, err := IsTracked(dir, path); err != nil || tracked {
			t.Fatalf("tracked %v, err %v", tracked, err)
		}
	})
}

// TestMainWorktree: a linked worktree names its main worktree; the main
// worktree and a plain folder don't. (Loop 8, Behavior 5)
func TestMainWorktree(t *testing.T) {
	repo := t.TempDir()
	gitOutput(t, repo, "init", "-q")
	gitOutput(t, repo, "-c", "user.name=ntm test", "-c", "user.email=ntm-test@example.invalid", "commit", "-q", "--allow-empty", "-m", "first")
	worktree := filepath.Join(t.TempDir(), "wt")
	gitOutput(t, repo, "worktree", "add", "-q", worktree)
	sub := filepath.Join(worktree, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	realRepo, _ := filepath.EvalSymlinks(repo)

	for _, dir := range []string{worktree, sub} {
		main, linked, err := MainWorktree(dir)
		if err != nil || !linked || main != realRepo {
			t.Fatalf("MainWorktree(%s) = %q, %v, %v; want %q, true", dir, main, linked, err, realRepo)
		}
	}
	if main, linked, err := MainWorktree(repo); err != nil || linked || main != realRepo {
		t.Fatalf("MainWorktree(main) = %q, %v, %v", main, linked, err)
	}
	if main, linked, err := MainWorktree(t.TempDir()); err != nil || linked || main != "" {
		t.Fatalf("MainWorktree(plain) = %q, %v, %v", main, linked, err)
	}
}
