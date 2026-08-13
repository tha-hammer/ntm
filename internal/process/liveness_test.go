package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

func TestLivenessHelperProcess(t *testing.T) {
	if os.Getenv("NTM_PROCESS_HELPER") != "exit-immediately" {
		return
	}
	os.Exit(0)
}

func TestIsAlive_CurrentProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	if !IsAlive(pid) {
		t.Errorf("IsAlive(%d) = false, want true for current process", pid)
	}
}

func TestIsAlive_ZombieProcessReturnsFalse(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("zombie-state check relies on /proc state semantics")
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestLivenessHelperProcess")
	cmd.Env = append(os.Environ(), "NTM_PROCESS_HELPER=exit-immediately")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	defer func() {
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _, err := GetProcessState(cmd.Process.Pid)
		if err == nil && state == "Z" {
			if IsAlive(cmd.Process.Pid) {
				t.Fatalf("IsAlive(%d) = true, want false for zombie process", cmd.Process.Pid)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("helper process %d never reached zombie state", cmd.Process.Pid)
}

func TestIsAlive_InvalidPID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pid  int
	}{
		{"zero", 0},
		{"negative", -1},
		{"very large", 999999999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if IsAlive(tt.pid) {
				t.Errorf("IsAlive(%d) = true, want false", tt.pid)
			}
		})
	}
}

func TestGetChildPID_InvalidParent(t *testing.T) {
	t.Parallel()
	if pid := GetChildPID(0); pid != 0 {
		t.Errorf("GetChildPID(0) = %d, want 0", pid)
	}
	if pid := GetChildPID(-1); pid != 0 {
		t.Errorf("GetChildPID(-1) = %d, want 0", pid)
	}
}

func TestHasChildAlive_InvalidPID(t *testing.T) {
	t.Parallel()
	if HasChildAlive(0) {
		t.Error("HasChildAlive(0) = true, want false")
	}
	if HasChildAlive(-1) {
		t.Error("HasChildAlive(-1) = true, want false")
	}
}

func TestIsChildAlive_Alias(t *testing.T) {
	t.Parallel()
	// IsChildAlive should behave identically to HasChildAlive
	if IsChildAlive(0) {
		t.Error("IsChildAlive(0) = true, want false")
	}
	if IsChildAlive(-1) {
		t.Error("IsChildAlive(-1) = true, want false")
	}
}

func TestGetProcessState_CurrentProcess(t *testing.T) {
	t.Parallel()
	pid := os.Getpid()
	state, name, err := GetProcessState(pid)
	if err != nil {
		t.Fatalf("GetProcessState(%d) error = %v", pid, err)
	}
	// The current test process should report a live, non-terminal state.
	// Under load Linux can legitimately report D (disk sleep) or other
	// non-terminal codes, so don't overfit this to only R/S.
	if state == "" || state == "Z" || state == "X" || state == "x" {
		t.Errorf("GetProcessState(%d) state = %q, want a live non-terminal state", pid, state)
	}
	if name == "" {
		t.Error("GetProcessState() name should not be empty")
	}
}

func TestGetProcessState_InvalidPID(t *testing.T) {
	t.Parallel()
	_, _, err := GetProcessState(0)
	if err == nil {
		t.Error("GetProcessState(0) should return error")
	}
	_, _, err = GetProcessState(-1)
	if err == nil {
		t.Error("GetProcessState(-1) should return error")
	}
}

func TestGetProcessState_NonExistentProcess(t *testing.T) {
	t.Parallel()
	_, _, err := GetProcessState(999999999)
	if err == nil {
		t.Error("GetProcessState(999999999) should return error for non-existent process")
	}
}

func TestGetChildPID_CurrentProcess(t *testing.T) {
	t.Parallel()
	// The test process likely has no child processes
	pid := os.Getpid()
	child := GetChildPID(pid)
	// Just verify it doesn't panic; child may be 0 or a valid PID
	if child < 0 {
		t.Errorf("GetChildPID(%d) = %d, should not be negative", pid, child)
	}
}

func TestHasChildAlive_NonExistentProcess(t *testing.T) {
	t.Parallel()
	if HasChildAlive(999999999) {
		t.Error("HasChildAlive(999999999) = true, want false")
	}
}

func TestCaptureProcessIdentity_CurrentProcessStable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pid := os.Getpid()

	first, err := CaptureProcessIdentity(ctx, pid)
	if err != nil {
		t.Fatalf("CaptureProcessIdentity(%d) first call error = %v", pid, err)
	}
	if first.PID != pid {
		t.Errorf("first.PID = %d, want %d", first.PID, pid)
	}
	if first.CreateTimeMillis == 0 {
		t.Error("first.CreateTimeMillis = 0, want nonzero")
	}

	second, err := CaptureProcessIdentity(ctx, pid)
	if err != nil {
		t.Fatalf("CaptureProcessIdentity(%d) second call error = %v", pid, err)
	}
	if second != first {
		t.Errorf("second capture = %+v, want identical to first %+v", second, first)
	}
}

func TestCaptureProcessIdentity_ExitedProcess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	cmd := exec.Command(os.Args[0], "-test.run=TestLivenessHelperProcess")
	cmd.Env = append(os.Environ(), "NTM_PROCESS_HELPER=exit-immediately")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait helper: %v", err)
	}

	if _, err := CaptureProcessIdentity(ctx, pid); !errors.Is(err, ErrProcessNotRunning) {
		t.Errorf("CaptureProcessIdentity(%d) after exit+wait error = %v, want ErrProcessNotRunning", pid, err)
	}
}

func TestCaptureProcessIdentity_InvalidPID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, pid := range []int{0, -1} {
		if _, err := CaptureProcessIdentity(ctx, pid); !errors.Is(err, ErrProcessNotRunning) {
			t.Errorf("CaptureProcessIdentity(%d) error = %v, want ErrProcessNotRunning", pid, err)
		}
	}
}

func TestCaptureProcessIdentity_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := CaptureProcessIdentity(ctx, os.Getpid()); !errors.Is(err, context.Canceled) {
		t.Errorf("CaptureProcessIdentity with cancelled context error = %v, want context.Canceled", err)
	}
}

func TestProcessIdentity_RejectsDifferentCreateTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pid := os.Getpid()

	real, err := CaptureProcessIdentity(ctx, pid)
	if err != nil {
		t.Fatalf("CaptureProcessIdentity(%d) error = %v", pid, err)
	}

	mismatched := ProcessIdentity{PID: real.PID, CreateTimeMillis: real.CreateTimeMillis + 1}
	if real == mismatched {
		t.Errorf("identity %+v unexpectedly equals mismatched-create-time identity %+v", real, mismatched)
	}

	// A stale stored identity for a since-exited PID must not validate
	// against a fresh capture of whatever now holds that PID slot.
	exitedCmd := exec.Command(os.Args[0], "-test.run=TestLivenessHelperProcess")
	exitedCmd.Env = append(os.Environ(), "NTM_PROCESS_HELPER=exit-immediately")
	if err := exitedCmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	exitedPID := exitedCmd.Process.Pid
	if err := exitedCmd.Wait(); err != nil {
		t.Fatalf("wait helper: %v", err)
	}
	staleStored := ProcessIdentity{PID: exitedPID, CreateTimeMillis: 1}
	fresh, err := CaptureProcessIdentity(ctx, exitedPID)
	if !errors.Is(err, ErrProcessNotRunning) {
		t.Fatalf("CaptureProcessIdentity(%d) after exit error = %v, want ErrProcessNotRunning", exitedPID, err)
	}
	if fresh == staleStored {
		t.Errorf("fresh capture of exited pid unexpectedly produced a valid identity matching the stale stored one")
	}
}

func TestGetChildPIDsContext_ReturnsErrorAndHonorsLimit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("invalid parent", func(t *testing.T) {
		t.Parallel()
		for _, pid := range []int{0, -1} {
			if children, err := GetChildPIDsContext(ctx, pid, 8); err == nil {
				t.Errorf("GetChildPIDsContext(%d) error = nil, want error; children = %v", pid, children)
			}
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		t.Parallel()
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := GetChildPIDsContext(cancelledCtx, os.Getpid(), 8); !errors.Is(err, context.Canceled) {
			t.Errorf("GetChildPIDsContext with cancelled context error = %v, want context.Canceled", err)
		}
	})

	t.Run("current process no error", func(t *testing.T) {
		t.Parallel()
		// The current test process may or may not have children, but
		// enumeration itself must not fail.
		if _, err := GetChildPIDsContext(ctx, os.Getpid(), 8); err != nil {
			t.Errorf("GetChildPIDsContext(%d) error = %v, want nil", os.Getpid(), err)
		}
	})

	t.Run("honors limit", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("relies on /proc task-children enumeration")
		}
		// Fork children through a real shell (single main thread), not
		// exec.Command from the Go test binary directly: Go's runtime can
		// run ForkExec on a non-main OS thread, which would make the
		// spawned PID invisible under the parent's own
		// /proc/<pid>/task/<pid>/children file and flake this assertion.
		// A shell's own children always show up there, matching how this
		// enumeration is actually used against real pane shell PIDs.
		shellCmd := exec.Command("sh", "-c", "sleep 5 & sleep 5 & sleep 5 & sleep 5 & sleep 5 & wait")
		if err := shellCmd.Start(); err != nil {
			t.Fatalf("start shell: %v", err)
		}
		defer func() {
			_ = shellCmd.Process.Kill()
			_ = shellCmd.Wait()
		}()
		shellPID := shellCmd.Process.Pid

		const limit = 2
		deadline := time.Now().Add(2 * time.Second)
		var children []int
		var err error
		for time.Now().Before(deadline) {
			children, err = GetChildPIDsContext(ctx, shellPID, limit)
			if err != nil {
				t.Fatalf("GetChildPIDsContext error = %v", err)
			}
			if len(children) >= limit {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if len(children) != limit {
			t.Errorf("len(children) = %d, want exactly limit %d", len(children), limit)
		}
	})
}

func TestProcessStateNames(t *testing.T) {
	t.Parallel()
	// Verify the map covers common states
	expected := map[string]string{
		"R": "running",
		"S": "sleeping",
		"D": "disk sleep",
		"Z": "zombie",
		"T": "stopped",
		"I": "idle",
	}
	for code, name := range expected {
		if got := processStateNames[code]; got != name {
			t.Errorf("processStateNames[%q] = %q, want %q", code, got, name)
		}
	}
}
