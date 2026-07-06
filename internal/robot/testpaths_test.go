package robot

import (
	"path/filepath"
	"testing"
)

// tempDirCanonical returns t.TempDir() with symlinks resolved.
//
// On macOS, t.TempDir() returns "/var/folders/..." but os.Getwd() (after
// chdir) and `git rev-parse --show-toplevel` return the canonical
// "/private/var/folders/..." form. Equality checks across the two forms
// fail only on macOS-latest CI. Resolving symlinks up-front fixes every
// callsite without per-test duplication.
func tempDirCanonical(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(d); err == nil {
		return resolved
	}
	return d
}
