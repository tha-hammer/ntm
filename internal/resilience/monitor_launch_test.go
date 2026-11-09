package resilience

import (
	"path/filepath"
	"testing"
)

func TestNewInternalMonitorCommand_ValidatesSessionAndExecutable(t *testing.T) {
	cmd, err := NewInternalMonitorCommand("proj")
	if err != nil {
		t.Fatalf("NewInternalMonitorCommand(valid) error = %v", err)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("command path = %q, want absolute path", cmd.Path)
	}
	if got, want := cmd.Args, []string{cmd.Path, "internal-monitor", "proj"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("command args = %#v, want %#v", got, want)
	}

	if _, err := NewInternalMonitorCommand("bad:name"); err == nil {
		t.Fatal("expected invalid session name to be rejected")
	}
}

func TestShouldStartInternalMonitor_IsDisabledUnderGoTest(t *testing.T) {
	if ShouldStartInternalMonitor() {
		t.Fatal("expected internal monitor to be disabled under go test")
	}
}

// Loop 5, Behavior 12: spawn saves the manifest even where it doesn't launch
// the monitor, such as under go test, unless the monitor is switched off.
func TestShouldSaveSpawnManifest(t *testing.T) {
	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "")
	if !ShouldSaveSpawnManifest() {
		t.Fatal("expected the manifest to be saved under go test")
	}
	t.Setenv("NTM_DISABLE_INTERNAL_MONITOR", "1")
	if ShouldSaveSpawnManifest() {
		t.Fatal("expected NTM_DISABLE_INTERNAL_MONITOR to stop the manifest save")
	}
}

func TestCurrentExecutablePath_ReturnsAbsoluteExistingFile(t *testing.T) {
	exe, err := CurrentExecutablePath()
	if err != nil {
		t.Fatalf("CurrentExecutablePath() error = %v", err)
	}
	if !filepath.IsAbs(exe) {
		t.Fatalf("CurrentExecutablePath() = %q, want an absolute path", exe)
	}
}
