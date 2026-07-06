package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecuteCommandRetryPolicyRetriesExitCodeFailure(t *testing.T) {
	executor := newCommandTestExecutor(t)
	attemptsPath := filepath.Join(executor.config.ProjectDir, "retry-attempts.txt")
	workflow := &Workflow{
		Name:     "command-retry",
		Settings: DefaultWorkflowSettings(),
		Steps: []Step{{
			ID:         "retry_exit",
			Command:    `printf 'attempt\n' >> retry-attempts.txt; exit 7`,
			OnError:    ErrorActionRetry,
			RetryCount: 3,
			RetryDelay: Duration{Duration: time.Millisecond},
		}},
	}
	executor.graph = NewDependencyGraph(workflow)

	result := executor.executeStep(context.Background(), &workflow.Steps[0], workflow)
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, StatusFailed)
	}
	if result.Attempts != 4 {
		t.Fatalf("attempts = %d, want initial attempt plus 3 retries", result.Attempts)
	}
	if result.Error == nil || result.Error.Type != "exit" {
		t.Fatalf("error = %#v, want exit error", result.Error)
	}
	if !strings.Contains(result.Error.Details, "exit_code=7") {
		t.Fatalf("error details = %q, want exit_code=7", result.Error.Details)
	}

	content, err := os.ReadFile(attemptsPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", attemptsPath, err)
	}
	if got := strings.Count(string(content), "attempt\n"); got != 4 {
		t.Fatalf("recorded attempts = %d, want 4; content = %q", got, content)
	}
}

func TestExecuteCommandContextCancellationReturnsCancelled(t *testing.T) {
	executor := newCommandTestExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	step := &Step{
		ID:      "cancel_command",
		Command: "sleep 60",
		Timeout: Duration{Duration: 30 * time.Second},
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result := executor.executeCommand(ctx, step, &Workflow{Name: "command-cancel"})
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q, want %q; error = %#v", result.Status, StatusCancelled, result.Error)
	}
	if result.Error == nil || result.Error.Type != "cancelled" {
		t.Fatalf("error = %#v, want cancelled error", result.Error)
	}
}
