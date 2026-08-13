package resilience

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// ShouldStartInternalMonitor reports whether it's safe to launch a real
// detached internal-monitor process right now. Under `go test`,
// os.Executable() points at a `*.test` binary — spawning "internal-monitor"
// via that binary would re-run the entire test suite recursively
// (detached), which can quickly fork-bomb the machine, so every caller
// (CLI `ntm spawn`, and — behind its own opt-in — `--robot-spawn`) must
// check this before calling NewInternalMonitorCommand.
// NTM_DISABLE_INTERNAL_MONITOR is an explicit escape hatch for any other
// context that wants the same protection.
func ShouldStartInternalMonitor() bool {
	if flag.Lookup("test.v") != nil {
		return false
	}
	if os.Getenv("NTM_DISABLE_INTERNAL_MONITOR") != "" {
		return false
	}
	return true
}

// CurrentExecutablePath resolves and validates the absolute path to the
// currently running ntm binary, so a detached child (the internal-monitor
// process) can be launched via the same executable regardless of how ntm
// itself was invoked (PATH lookup, relative path, symlink, ...).
func CurrentExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve current executable: %w", err)
	}
	exe = filepath.Clean(exe)
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("current executable path must be absolute: %q", exe)
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("stat current executable: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("current executable path is a directory: %q", exe)
	}
	return exe, nil
}

// NewInternalMonitorCommand builds the `<ntm> internal-monitor <session>`
// command used to launch the detached resilience-monitor process for a
// session. Shared by every spawn path (CLI `ntm spawn`, and — behind an
// explicit opt-in — `--robot-spawn`) so there is exactly one place that
// knows how to launch this process.
func NewInternalMonitorCommand(session string) (*exec.Cmd, error) {
	if err := tmux.ValidateSessionName(session); err != nil {
		return nil, fmt.Errorf("invalid session name: %w", err)
	}
	exe, err := CurrentExecutablePath()
	if err != nil {
		return nil, err
	}
	return exec.Command(exe, "internal-monitor", session), nil
}

// SetDetachedProcess is implemented in platform-specific files:
//   - monitor_launch_unix.go for Unix systems (new session, Setsid)
//   - monitor_launch_other.go for non-Unix platforms (no-op)
