package git

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var initGitCommandTimeout = 10 * time.Second

// InitRepository makes dir a git repository with one empty commit so agents
// can run `git worktree add` immediately (worktrees need a HEAD commit). A
// dir already inside a repository is left untouched and reports false. No
// remote is configured.
func InitRepository(dir string) (bool, error) {
	if dir == "" {
		return false, fmt.Errorf("init repository: empty directory")
	}
	if IsGitRepository(dir) {
		return false, nil
	}
	if out, err := runInitGit(dir, "init"); err != nil {
		return false, fmt.Errorf("git init: %w: %s", err, out)
	}

	// Fall back to a placeholder identity only when none is configured, so
	// the initial commit never fails on a fresh machine.
	var identity []string
	if out, err := runInitGit(dir, "config", "user.email"); err != nil || strings.TrimSpace(out) == "" {
		identity = append(identity, "-c", "user.email=ntm@localhost")
	}
	if out, err := runInitGit(dir, "config", "user.name"); err != nil || strings.TrimSpace(out) == "" {
		identity = append(identity, "-c", "user.name=ntm")
	}
	args := append(identity, "commit", "--allow-empty", "--no-verify", "-m", "Initial commit")
	if out, err := runInitGit(dir, args...); err != nil {
		return true, fmt.Errorf("git commit: %w: %s", err, out)
	}
	return true, nil
}

func runInitGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), initGitCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
