// Package testutil — path helpers for tests that compare temp-directory
// paths against values produced by production code (which typically calls
// os.Getwd, exec'd git commands, or filepath.Abs — none of which resolve
// macOS's /var → /private/var symlink).
//
// On macOS, os.TempDir() returns "/var/folders/..." (the user-visible form),
// but os.Getwd() after chdir, or `git rev-parse --show-toplevel`, returns
// the canonical "/private/var/folders/..." form. Equality checks across
// the two forms then fail on macOS-latest while passing everywhere else.
//
// CanonicalTempDir wraps t.TempDir with a single filepath.EvalSymlinks pass
// so all subsequent comparisons share the canonical form.
package testutil

import (
	"path/filepath"
	"testing"
)

// CanonicalTempDir returns t.TempDir() with all symlinks resolved.
//
// Use this instead of t.TempDir() whenever the returned path will be
// compared against another path that may have been canonicalized by the
// OS (e.g. via os.Getwd after chdir, or by `git rev-parse --show-toplevel`).
// This is the standard fix for the macOS /var → /private/var symlink issue.
//
// On Linux/Windows EvalSymlinks is effectively a no-op for tempdirs, so
// callers can use this unconditionally without changing platform behavior.
func CanonicalTempDir(t *testing.T) string {
	t.Helper()
	raw := t.TempDir()
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", raw, err)
	}
	return resolved
}

// CanonicalPath resolves symlinks in path. If the path does not exist
// (e.g. it's a future tempdir child the caller hasn't created yet), it
// walks up to the deepest existing ancestor, canonicalizes that, and
// re-joins the missing suffix.
//
// Use this when the test pre-computes a path like
// filepath.Join(tempDir, "child") and needs the canonical form before
// the child has been created on disk.
func CanonicalPath(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	// Path may not yet exist — canonicalize the deepest existing prefix
	// and re-attach the missing tail.
	dir, file := filepath.Split(path)
	dir = filepath.Clean(dir)
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return path
	}
	resolvedDir := CanonicalPath(t, dir)
	return filepath.Join(resolvedDir, file)
}
