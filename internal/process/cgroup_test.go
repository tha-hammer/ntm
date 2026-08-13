package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCgroupProcFileContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		wantPath string
		wantOK   bool
	}{
		{
			name:     "unified cgroup v2 line",
			content:  "0::/user.slice/user-1000.slice/session-1.scope\n",
			wantPath: "/user.slice/user-1000.slice/session-1.scope",
			wantOK:   true,
		},
		{
			name:    "empty content",
			content: "",
			wantOK:  false,
		},
		{
			name:    "legacy hybrid mount with a non-zero hierarchy line",
			content: "1:memory:/user.slice\n0::/user.slice/session-1.scope\n",
			wantOK:  false,
		},
		{
			name:    "malformed line missing colons",
			content: "not-a-cgroup-line\n",
			wantOK:  false,
		},
		{
			name:    "duplicate 0:: lines",
			content: "0::/a\n0::/b\n",
			wantOK:  false,
		},
		{
			name:    "0:: line with an empty path",
			content: "0::\n",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path, ok := parseCgroupProcFileContent(tt.content)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && path != tt.wantPath {
				t.Fatalf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}

// setCgroupRootForTest overrides the package-level cgroupRoot for the
// duration of a test and returns a func to restore it. Callers must not run
// in parallel with each other since cgroupRoot is shared mutable state.
func setCgroupRootForTest(t *testing.T, dir string) func() {
	t.Helper()
	prev := cgroupRoot
	cgroupRoot = dir
	return func() { cgroupRoot = prev }
}

func TestReadCgroupMemoryPeakBytes_MemoryPeakPresent(t *testing.T) {
	dir := t.TempDir()
	scope := "/test.scope"
	scopeDir := filepath.Join(dir, scope)
	if err := os.MkdirAll(scopeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "memory.peak"), []byte("104857600\n"), 0644); err != nil {
		t.Fatalf("write memory.peak: %v", err)
	}
	defer setCgroupRootForTest(t, dir)()

	got, ok := ReadCgroupMemoryPeakBytes(scope)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if got != 104857600 {
		t.Fatalf("bytes = %d, want 104857600", got)
	}
}

func TestReadCgroupMemoryPeakBytes_FallsBackToMemoryCurrent(t *testing.T) {
	dir := t.TempDir()
	scope := "/test.scope"
	scopeDir := filepath.Join(dir, scope)
	if err := os.MkdirAll(scopeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "memory.current"), []byte("52428800\n"), 0644); err != nil {
		t.Fatalf("write memory.current: %v", err)
	}
	defer setCgroupRootForTest(t, dir)()

	got, ok := ReadCgroupMemoryPeakBytes(scope)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if got != 52428800 {
		t.Fatalf("bytes = %d, want 52428800", got)
	}
}

func TestReadCgroupMemoryPeakBytes_BothMissing(t *testing.T) {
	dir := t.TempDir()
	scope := "/test.scope"
	if err := os.MkdirAll(filepath.Join(dir, scope), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	defer setCgroupRootForTest(t, dir)()

	if _, ok := ReadCgroupMemoryPeakBytes(scope); ok {
		t.Fatalf("ok = true, want false when neither file exists")
	}
}

func TestReadCgroupMemoryPeakBytes_UnreadableMemoryPeakFallsBackToCurrent(t *testing.T) {
	dir := t.TempDir()
	scope := "/test.scope"
	scopeDir := filepath.Join(dir, scope)
	if err := os.MkdirAll(scopeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A directory named memory.peak makes os.ReadFile fail deterministically
	// regardless of the test's UID (chmod 0000 is ignored when running as
	// root, which some CI/agent environments do), simulating any
	// unreadable-file failure such as permission denied.
	if err := os.MkdirAll(filepath.Join(scopeDir, "memory.peak"), 0755); err != nil {
		t.Fatalf("mkdir memory.peak: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "memory.current"), []byte("1024\n"), 0644); err != nil {
		t.Fatalf("write memory.current: %v", err)
	}
	defer setCgroupRootForTest(t, dir)()

	got, ok := ReadCgroupMemoryPeakBytes(scope)
	if !ok {
		t.Fatalf("ok = false, want true (should fall back to memory.current)")
	}
	if got != 1024 {
		t.Fatalf("bytes = %d, want 1024", got)
	}
}

func TestReadCgroupMemoryPeakBytes_NonNumericContent(t *testing.T) {
	dir := t.TempDir()
	scope := "/test.scope"
	scopeDir := filepath.Join(dir, scope)
	if err := os.MkdirAll(scopeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "memory.peak"), []byte("max\n"), 0644); err != nil {
		t.Fatalf("write memory.peak: %v", err)
	}
	defer setCgroupRootForTest(t, dir)()

	if _, ok := ReadCgroupMemoryPeakBytes(scope); ok {
		t.Fatalf("ok = true, want false for non-numeric content")
	}
}

func TestReadCgroupMemoryPeakBytes_OverflowValueRejected(t *testing.T) {
	dir := t.TempDir()
	scope := "/test.scope"
	scopeDir := filepath.Join(dir, scope)
	if err := os.MkdirAll(scopeDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scopeDir, "memory.peak"), []byte("999999999999999999999999\n"), 0644); err != nil {
		t.Fatalf("write memory.peak: %v", err)
	}
	defer setCgroupRootForTest(t, dir)()

	if _, ok := ReadCgroupMemoryPeakBytes(scope); ok {
		t.Fatalf("ok = true, want false for an unsigned-overflow value")
	}
}
