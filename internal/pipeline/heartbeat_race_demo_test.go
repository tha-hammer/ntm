package pipeline

import (
	"context"
	"testing"
	"time"
)

// TestExecuteCommand_HeartbeatRaceFreeOnNoisyStdout is the regression test
// for bd-1vhq5: previously the heartbeat goroutine read stdoutBuf.Len()
// while exec.Cmd's writer goroutine wrote to the same cappedWriter,
// triggering a data race on bytes.Buffer. cappedWriter now serialises
// access via its internal mutex; this test would fail under -race before
// that fix.
func TestExecuteCommand_HeartbeatRaceFreeOnNoisyStdout(t *testing.T) {
	prev := commandHeartbeatInterval
	commandHeartbeatInterval = 5 * time.Millisecond
	t.Cleanup(func() { commandHeartbeatInterval = prev })

	e := newCommandTestExecutor(t)
	step := &Step{
		ID:      "noisy",
		Command: "for i in $(seq 1 200); do echo line-$i; sleep 0.005; done",
	}
	result := e.executeCommand(context.Background(), step, &Workflow{Name: "test"})
	if result.Status != StatusCompleted {
		t.Fatalf("Status = %q, want %q; error = %+v", result.Status, StatusCompleted, result.Error)
	}
}
