package health

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// =============================================================================
// detectActivity edge cases
// =============================================================================

func TestDetectActivity_ZeroTimestampNoPrompt(t *testing.T) {
	t.Parallel()
	// Zero lastActivity + no idle prompt → ActivityUnknown
	got := detectActivity("some random output without prompt", time.Time{}, "unknown")
	if got != ActivityUnknown {
		t.Errorf("detectActivity(no prompt, zero time) = %v, want ActivityUnknown", got)
	}
}

func TestDetectActivity_ZeroTimestampWithPrompt(t *testing.T) {
	t.Parallel()
	// Zero lastActivity + idle prompt → ActivityIdle (prompt detection wins)
	got := detectActivity("> ", time.Time{}, "user")
	if got != ActivityIdle {
		t.Errorf("detectActivity(prompt, zero time) = %v, want ActivityIdle", got)
	}
}

func TestDetectActivity_RecentWithNoPrompt(t *testing.T) {
	t.Parallel()
	got := detectActivity("building code...", time.Now().Add(-30*time.Second), "cc")
	if got != ActivityActive {
		t.Errorf("detectActivity(recent, no prompt) = %v, want ActivityActive", got)
	}
}

func TestDetectActivity_StaleNoPrompt(t *testing.T) {
	t.Parallel()
	got := detectActivity("building code...", time.Now().Add(-10*time.Minute), "cc")
	if got != ActivityStale {
		t.Errorf("detectActivity(stale, no prompt) = %v, want ActivityStale", got)
	}
}

func TestDetectActivity_ExactlyFiveMinutes(t *testing.T) {
	t.Parallel()
	// Exactly at 5-minute boundary should be Stale (>= 5 min)
	got := detectActivity("output", time.Now().Add(-5*time.Minute-time.Second), "user")
	if got != ActivityStale {
		t.Errorf("detectActivity(5min+1s) = %v, want ActivityStale", got)
	}
}

func TestDetectActivity_JustUnderFiveMinutes(t *testing.T) {
	t.Parallel()
	got := detectActivity("output", time.Now().Add(-4*time.Minute-59*time.Second), "user")
	if got != ActivityActive {
		t.Errorf("detectActivity(4m59s) = %v, want ActivityActive", got)
	}
}

// =============================================================================
// detectProcessStatus edge cases
// =============================================================================

func TestDetectProcessStatus_PIDWithNoChildren(t *testing.T) {
	t.Parallel()
	// Use our own PID as the "shell" — the Go test binary itself should
	// have no child processes (or at least we can check the logic).
	// We test with a PID of a process we *know* has no children.
	// This is hard to guarantee, so let's use a very large PID
	// that doesn't exist — process.HasChildAlive returns false for non-existent PIDs.
	got := detectProcessStatus("some output", "python", 999999999)
	if got != ProcessExited {
		t.Errorf("detectProcessStatus(nonexistent PID) = %v, want ProcessExited", got)
	}
}

func TestDetectProcessStatus_PIDWithChildren(t *testing.T) {
	t.Parallel()
	// Use our own PID + a long-lived child instead of PID 1. On
	// macOS-latest CI runners, launchd's children are not visible via
	// pgrep to the unprivileged test user. The 30s sleep keeps the
	// child alive well past the detectProcessStatus call even when
	// the suite is heavily parallel-loaded.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn child for the PID-has-children scenario: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	got := detectProcessStatus("exit status 1", "python", os.Getpid())
	if got != ProcessRunning {
		t.Errorf("detectProcessStatus(current PID with children) = %v, want ProcessRunning", got)
	}
}

func TestDetectProcessStatus_TextFallbackMultiplePatterns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		output  string
		command string
		want    ProcessStatus
	}{
		{"process exited pattern", "process exited with code 1", "node", ProcessExited},
		{"exited with pattern", "exited with status 0", "ruby", ProcessExited},
		{"mixed case exit", "Session Ended gracefully", "tmux", ProcessExited},
		{"shell-like fish", "some output", "fish", ProcessRunning}, // contains "sh"
		{"non-shell no exit", "compiling...", "rustc", ProcessRunning},
		{"empty output empty command", "", "", ProcessRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := detectProcessStatus(tt.output, tt.command, 0)
			if got != tt.want {
				t.Errorf("detectProcessStatus(%q, %q, 0) = %v, want %v", tt.output, tt.command, got, tt.want)
			}
		})
	}
}

// =============================================================================
// detectErrors edge cases
// =============================================================================

func TestDetectErrors_EmptyOutput(t *testing.T) {
	t.Parallel()
	issues := detectErrors("")
	if len(issues) != 0 {
		t.Errorf("detectErrors(\"\") returned %d issues, want 0", len(issues))
	}
}

func TestDetectErrors_MultipleErrors(t *testing.T) {
	t.Parallel()
	// Output with both rate limit and authentication errors
	output := "Rate limit exceeded\nAuthentication failed\npanic: nil pointer"
	issues := detectErrors(output)
	if len(issues) < 2 {
		t.Errorf("detectErrors with multiple errors returned %d issues, want >= 2", len(issues))
	}
}

func TestDetectErrors_ConnectionError(t *testing.T) {
	t.Parallel()
	output := "connection refused"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "network_error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect connection error as network_error")
	}
}

func TestDetectErrors_GenericError(t *testing.T) {
	t.Parallel()
	output := "npm ERR! something went wrong"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect npm error as generic error")
	}
}

func TestDetectErrors_CrashOnly(t *testing.T) {
	t.Parallel()
	output := "panic: runtime error: index out of range"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "crash" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect panic as crash")
	}
}

func TestDetectErrors_AuthOnly(t *testing.T) {
	t.Parallel()
	output := "Authentication failed: invalid API key"
	issues := detectErrors(output)
	found := false
	for _, issue := range issues {
		if issue.Type == "auth_error" {
			found = true
			break
		}
	}
	if !found {
		t.Error("detectErrors should detect auth failure")
	}
}

// =============================================================================
// calculateStatus edge cases
// =============================================================================

func TestCalculateStatus_ExitedWithIssues(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessExited,
		Activity:      ActivityActive,
		Issues:        []Issue{{Type: "crash"}},
	}
	got := calculateStatus(h)
	if got != StatusError {
		t.Errorf("calculateStatus(exited+crash) = %v, want StatusError", got)
	}
}

func TestCalculateStatus_RunningActiveNoIssues(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
		Issues:        nil,
	}
	got := calculateStatus(h)
	if got != StatusOK {
		t.Errorf("calculateStatus(running+active) = %v, want StatusOK", got)
	}
}

func TestCalculateStatus_RateLimitOnly(t *testing.T) {
	t.Parallel()
	h := AgentHealth{
		ProcessStatus: ProcessRunning,
		Activity:      ActivityActive,
		Issues:        []Issue{{Type: "rate_limit", Message: "429"}},
	}
	got := calculateStatus(h)
	if got != StatusWarning {
		t.Errorf("calculateStatus(rate_limit) = %v, want StatusWarning", got)
	}
}

// =============================================================================
// parseWaitTime edge cases
// =============================================================================

func TestParseWaitTime_MultipleNumbers(t *testing.T) {
	t.Parallel()
	// Should find the first number associated with time
	got := parseWaitTime("retry after 30s, max 3 attempts")
	if got != 30 {
		t.Errorf("parseWaitTime(multiple numbers) = %d, want 30", got)
	}
}

func TestParseWaitTime_NoNumber(t *testing.T) {
	t.Parallel()
	got := parseWaitTime("please wait")
	if got != 0 {
		t.Errorf("parseWaitTime(no number) = %d, want 0", got)
	}
}

// =============================================================================
// SessionNotFoundError
// =============================================================================

func TestSessionNotFoundError_Is(t *testing.T) {
	t.Parallel()
	err := &SessionNotFoundError{Session: "test"}
	msg := err.Error()
	if msg != "session 'test' not found" {
		t.Errorf("Error() = %q, want \"session 'test' not found\"", msg)
	}
}

// =============================================================================
// AgentHealth struct fields
// =============================================================================

func TestAgentHealth_ShellPID(t *testing.T) {
	t.Parallel()
	h := AgentHealth{ShellPID: 12345}
	if h.ShellPID != 12345 {
		t.Errorf("ShellPID = %d, want 12345", h.ShellPID)
	}
}

func TestAgentHealth_DefaultValues(t *testing.T) {
	t.Parallel()
	var h AgentHealth
	if h.Status != "" {
		t.Errorf("default Status = %q, want empty", h.Status)
	}
	if h.ProcessStatus != "" {
		t.Errorf("default ProcessStatus = %q, want empty", h.ProcessStatus)
	}
	if h.Activity != "" {
		t.Errorf("default Activity = %q, want empty", h.Activity)
	}
	if h.ShellPID != 0 {
		t.Errorf("default ShellPID = %d, want 0", h.ShellPID)
	}
}

// =============================================================================
// detectProcessStatus with current process PID
// =============================================================================

func TestDetectProcessStatus_CurrentProcess(t *testing.T) {
	t.Parallel()
	// Our own PID — the test process itself. Whether it has children depends
	// on the test runner, so just verify it doesn't panic.
	pid := os.Getpid()
	got := detectProcessStatus("output", "go", pid)
	// Should be either Running or Exited depending on child processes
	if got != ProcessRunning && got != ProcessExited {
		t.Errorf("detectProcessStatus(own PID) = %v, want Running or Exited", got)
	}
}
