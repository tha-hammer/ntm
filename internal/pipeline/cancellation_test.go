package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecuteCommand_GlobalTimeoutCancelsWithPartialOutput(t *testing.T) {
	e := newCommandTestExecutor(t)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	step := &Step{
		ID:      "global-timeout",
		Command: "printf started; sleep 10",
		Timeout: Duration{Duration: 30 * time.Second},
	}

	start := time.Now()
	result := e.executeCommand(ctx, step, &Workflow{Name: "test"})
	elapsed := time.Since(start)

	if result.Status != StatusCancelled {
		t.Fatalf("Status = %q, want %q; error=%+v", result.Status, StatusCancelled, result.Error)
	}
	if result.Error == nil || result.Error.Type != "timeout" {
		t.Fatalf("Error.Type = %v, want timeout", result.Error)
	}
	if result.Output != "started" {
		t.Fatalf("Output = %q, want partial output %q", result.Output, "started")
	}
	if elapsed > 6*time.Second {
		t.Fatalf("executeCommand took %s, want cancellation within grace period", elapsed)
	}
}

func TestExecutor_Run_GlobalTimeoutRunsOnCancelSteps(t *testing.T) {
	tmpDir := t.TempDir()
	cleanupPath := filepath.Join(tmpDir, "cleanup.txt")

	cfg := DefaultExecutorConfig("test-cancel")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "cancel-cleanup",
		Settings: WorkflowSettings{
			Timeout: Duration{Duration: 150 * time.Millisecond},
			OnError: ErrorActionFail,
			OnCancel: []Step{
				{
					ID:      "cleanup",
					Command: "printf cleanup > " + strconv.Quote(cleanupPath),
				},
			},
		},
		Steps: []Step{
			{
				ID:      "slow",
				Command: "sleep 10",
			},
		},
	}

	state, err := e.Run(context.Background(), workflow, nil, nil)
	if err == nil {
		t.Fatal("Run() error = nil, want timeout cancellation error")
	}
	if state.Status != StatusCancelled {
		t.Fatalf("state.Status = %q, want %q", state.Status, StatusCancelled)
	}
	if state.CancelledAt == nil || state.CancelledAt.IsZero() {
		t.Fatal("state.CancelledAt is nil or zero")
	}
	if state.FinishedAt.IsZero() {
		t.Fatal("state.FinishedAt is zero")
	}

	slow := state.Steps["slow"]
	if slow.Status != StatusCancelled {
		t.Fatalf("slow.Status = %q, want %q; error=%+v", slow.Status, StatusCancelled, slow.Error)
	}
	cleanup := state.Steps["cleanup"]
	if cleanup.Status != StatusCompleted {
		t.Fatalf("cleanup.Status = %q, want %q; error=%+v", cleanup.Status, StatusCompleted, cleanup.Error)
	}

	data, err := os.ReadFile(cleanupPath)
	if err != nil {
		t.Fatalf("cleanup file was not written: %v", err)
	}
	if strings.TrimSpace(string(data)) != "cleanup" {
		t.Fatalf("cleanup file = %q, want cleanup", string(data))
	}
}

// TestExecuteCommand_WaitNoneCancellation_MarksRerunOnResume covers
// bd-yrnue: when a fire-and-forget (WaitNone) command's background process
// is killed by cancellation cleanup AFTER executeCommand has returned
// StatusCompleted, the persisted step result must be flagged for rerun on
// resume so the sidecar gets relaunched instead of skipped.
func TestExecuteCommand_WaitNoneCancellation_MarksRerunOnResume(t *testing.T) {
	e := newCommandTestExecutor(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	step := &Step{
		ID:      "sidecar",
		Command: "sleep 30",
		Wait:    WaitNone,
	}

	result := e.executeCommand(ctx, step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q (WaitNone returns Completed synchronously)", result.Status, StatusCompleted)
	}
	if result.RerunOnResume {
		t.Fatalf("RerunOnResume = true on synchronous return; want false (no cancellation yet)")
	}

	// Simulate executeWorkflow recording the result before cancellation fires.
	e.stateMu.Lock()
	e.state.Steps[step.ID] = result
	e.stateMu.Unlock()

	cancel()

	// The cleanup goroutine acquires stateMu, sets RerunOnResume, and persists.
	// Allow up to 5s for waitCommandWithProcessGroupCleanup + grace period.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.stateMu.RLock()
		flagged := e.state.Steps[step.ID].RerunOnResume
		e.stateMu.RUnlock()
		if flagged {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	e.stateMu.RLock()
	got := e.state.Steps[step.ID]
	e.stateMu.RUnlock()
	if !got.RerunOnResume {
		t.Fatalf("RerunOnResume = false after cancellation; want true so resume relaunches sidecar")
	}
	if !shouldRerunStep(got) {
		t.Fatalf("shouldRerunStep(%+v) = false; want true so applyResumeState reruns the sidecar", got)
	}
}

// TestShouldRerunStep_RerunOnResumeFlagWins covers the resume contract for
// fire-and-forget commands flagged after cancellation cleanup: the explicit
// RerunOnResume flag overrides the stale persisted Completed status.
func TestShouldRerunStep_RerunOnResumeFlagWins(t *testing.T) {
	completed := StepResult{Status: StatusCompleted}
	if shouldRerunStep(completed) {
		t.Fatalf("shouldRerunStep(plain completed) = true; want false")
	}
	flagged := StepResult{Status: StatusCompleted, RerunOnResume: true}
	if !shouldRerunStep(flagged) {
		t.Fatalf("shouldRerunStep(completed+RerunOnResume) = false; want true")
	}
}

// TestExecutor_RunOnCancelSteps_HungCleanupRespectsTimeout covers
// bd-new9w: a misbehaving cleanup step (sleep N seconds) must not block
// the executor forever. The OnCancelTimeout caps each cleanup step's
// wall-clock budget; the test sets it well below the cleanup sleep so
// the cleanup phase returns within seconds rather than minutes.
func TestExecutor_RunOnCancelSteps_HungCleanupRespectsTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := DefaultExecutorConfig("hung-cleanup")
	cfg.ProjectDir = tmpDir
	e := NewExecutor(cfg)

	workflow := &Workflow{
		SchemaVersion: SchemaVersion,
		Name:          "hung-cleanup",
		Settings: WorkflowSettings{
			Timeout:         Duration{Duration: 150 * time.Millisecond},
			OnError:         ErrorActionFail,
			OnCancelTimeout: Duration{Duration: 500 * time.Millisecond},
			OnCancel: []Step{
				{
					ID:      "hung",
					Command: "sleep 30",
				},
			},
		},
		Steps: []Step{
			{ID: "slow", Command: "sleep 10"},
		},
	}

	start := time.Now()
	state, _ := e.Run(context.Background(), workflow, nil, nil)
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("Run() blocked for %s — OnCancelTimeout did not bound the hung cleanup", elapsed)
	}
	if state == nil || state.Status != StatusCancelled {
		t.Fatalf("state.Status = %v, want cancelled", state)
	}
	hung := state.Steps["hung"]
	if hung.Status == StatusCompleted {
		t.Fatalf("hung cleanup completed despite 30s sleep vs 500ms cap; status=%q error=%+v", hung.Status, hung.Error)
	}
}
