package util

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// NTMDir returns the path to the ~/.ntm directory.
func NTMDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".ntm"), nil
}

// ExpandPath expands a leading "~/" (or "~\\") to the current user's home directory.
//
// It intentionally does not expand "~user/..." (which is shell-specific).
func ExpandPath(path string) string {
	if path == "" {
		return path
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}

	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	if strings.HasPrefix(path, "~\\") {
		return filepath.Join(home, path[2:])
	}

	return path
}

// EnsureDir ensures that a directory exists, creating it if necessary.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// FindGitRoot attempts to find the root of the git repository
// containing the given directory. Returns empty string if not found.
func FindGitRoot(startDir string) (string, error) {
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

// FindProjectConfigRoot walks up from startDir looking for a local .ntm/config.toml.
// It returns the directory that owns the project config.
func FindProjectConfigRoot(startDir string) (string, error) {
	return walkUpForMarker(startDir, func(dir string) bool {
		info, err := os.Stat(filepath.Join(dir, ".ntm", "config.toml"))
		return err == nil && !info.IsDir()
	}, "project config")
}

// FindBeadsRoot walks up from startDir looking for a local .beads directory.
// It returns the directory that owns the beads state.
func FindBeadsRoot(startDir string) (string, error) {
	return walkUpForMarker(startDir, func(dir string) bool {
		info, err := os.Stat(filepath.Join(dir, ".beads"))
		return err == nil && info.IsDir()
	}, "beads root")
}

// walkUpForMarker returns the nearest folder at or above startDir for which
// hasMarker is true. The walk stops at the user's home folder without checking
// it: a .beads or .ntm/config.toml there is the user's global store, not a
// project, and a folder under home must not resolve to home (bd-nftwx).
func walkUpForMarker(startDir string, hasMarker func(dir string) bool, what string) (string, error) {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		startDir = cwd
	}

	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	home := userHomeDir()
	for dir != home {
		if hasMarker(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("%s not found from %s", what, startDir)
}

// userHomeDir is the cleaned home folder, or "" when there is none.
func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Clean(home)
}

// ResolveProjectDir prefers an explicit project config root, then a beads root,
// then a git root, and finally falls back to the starting directory itself.
func ResolveProjectDir(startDir string) string {
	if strings.TrimSpace(startDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		startDir = cwd
	}

	absDir, err := filepath.Abs(startDir)
	if err == nil {
		startDir = absDir
	}

	if projectDir, err := FindProjectConfigRoot(startDir); err == nil && projectDir != "" {
		return projectDir
	}
	if beadsRoot, err := FindBeadsRoot(startDir); err == nil && beadsRoot != "" {
		return beadsRoot
	}
	if gitRoot, err := FindGitRoot(startDir); err == nil && gitRoot != "" {
		return gitRoot
	}
	return filepath.Clean(startDir)
}

func normalizeProjectCandidate(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}

	path = ExpandPath(path)
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}

	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return ResolveProjectDir(path)
	}

	return filepath.Clean(path)
}

func pathHasDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathHasFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ProjectDirScore returns a confidence score for how strongly a path looks like
// a real ntm project root. Higher scores indicate better candidates.
func ProjectDirScore(path string) int {
	path = normalizeProjectCandidate(path)
	if path == "" {
		return 0
	}

	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return 0
	}

	score := 1 // Existing directory

	if pathHasFile(filepath.Join(path, ".ntm", "config.toml")) {
		score += 8
	}
	if pathHasDir(filepath.Join(path, ".beads")) {
		score += 6
	}
	if pathHasDir(filepath.Join(path, ".git")) {
		score += 4
	}
	if gitRoot, err := FindGitRoot(path); err == nil && gitRoot != "" {
		score += 2
		if filepath.Clean(gitRoot) == filepath.Clean(path) {
			score++
		}
	}

	return score
}

// BestProjectDir selects the strongest candidate project directory from the
// provided paths. Ties preserve the first candidate order.
func BestProjectDir(candidates ...string) string {
	best := ""
	bestScore := -1
	seen := make(map[string]struct{})

	for _, candidate := range candidates {
		normalized := normalizeProjectCandidate(candidate)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}

		score := ProjectDirScore(normalized)
		if score > bestScore {
			best = normalized
			bestScore = score
		}
	}

	return best
}
