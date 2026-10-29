package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestInitRepositoryEnablesWorktrees pins the reason InitRepository exists:
// a freshly created work directory must accept `git worktree add` right away,
// which requires a HEAD commit, not just `git init`.
func TestInitRepositoryEnablesWorktrees(t *testing.T) {
	// No git identity anywhere: the initial commit must still succeed.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	dir := t.TempDir()
	created, err := InitRepository(dir)
	if err != nil {
		t.Fatalf("InitRepository error = %v", err)
	}
	if !created {
		t.Fatal("InitRepository created = false, want true for a plain directory")
	}
	if got := gitOutput(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("commit count = %q, want 1", got)
	}
	if got := gitOutput(t, dir, "remote"); got != "" {
		t.Fatalf("remotes = %q, want none", got)
	}

	wt := filepath.Join(t.TempDir(), "agent_1")
	gitOutput(t, dir, "worktree", "add", "-b", "agent-1", wt)
	if !IsGitRepository(wt) {
		t.Fatalf("worktree %s is not a git repository", wt)
	}
}

// TestInitRepositoryLeavesExistingRepoAlone guards against nesting a new
// repository inside one that already covers the directory.
func TestInitRepositoryLeavesExistingRepoAlone(t *testing.T) {
	t.Parallel()

	repo := setupGitRepo(t)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	created, err := InitRepository(sub)
	if err != nil {
		t.Fatalf("InitRepository error = %v", err)
	}
	if created {
		t.Fatal("InitRepository created = true, want false inside an existing repo")
	}
	if _, err := os.Stat(filepath.Join(sub, ".git")); !os.IsNotExist(err) {
		t.Fatalf("nested .git was created in %s", sub)
	}
}

func TestInitRepositoryRejectsEmptyDir(t *testing.T) {
	t.Parallel()

	if _, err := InitRepository(""); err == nil {
		t.Fatal("InitRepository(\"\") error = nil, want error")
	}
}
