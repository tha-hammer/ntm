package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FindProjectRoot attempts to find the root of the git repository
// containing the given directory. Returns empty string if not found.
func FindProjectRoot(startDir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = startDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// MainWorktree returns the main worktree of dir's repo, and whether dir is in
// a linked worktree of it. A folder that isn't in a git repo returns "" and
// false with no error.
func MainWorktree(dir string) (main string, linked bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", false, nil // not a git repo
		}
		return "", false, fmt.Errorf("find the main worktree of %s: %w", dir, err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) != 2 {
		return "", false, fmt.Errorf("find the main worktree of %s: unexpected git output %q", dir, output)
	}
	gitDir, commonDir := filepath.Clean(lines[0]), filepath.Clean(lines[1])
	return filepath.Dir(commonDir), gitDir != commonDir, nil
}

// IsTracked reports whether git tracks path in dir's repo: committed or
// staged. path is relative to dir. A folder that isn't in a git repo reports
// false with no error.
func IsTracked(dir, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--", path)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 {
			return false, nil // not a git repo
		}
		return false, fmt.Errorf("check whether git tracks %s in %s: %w", path, dir, err)
	}
	return len(output) > 0, nil
}

// EnsureExcluded adds pattern to the repo's info/exclude unless a line already
// equals it, so it's safe to call again. In a linked worktree git reports the
// common exclude file, which applies to every worktree. It reports false with
// no error for a folder that isn't in a git repo.
func EnsureExcluded(dir, pattern string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil // not a git repo
		}
		return false, fmt.Errorf("find info/exclude for %s: %w", dir, err)
	}
	path := strings.TrimSpace(string(output))

	// #nosec G304 -- the path comes from git for this repo.
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return true, nil
		}
	}

	addition := pattern + "\n"
	if len(data) > 0 && data[len(data)-1] != '\n' {
		addition = "\n" + addition
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := file.WriteString(addition); err != nil {
		_ = file.Close()
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", path, err)
	}
	return true, nil
}
