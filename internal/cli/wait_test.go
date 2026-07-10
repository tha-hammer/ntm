package cli

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestIsValidCondition(t *testing.T) {
	tests := []struct {
		name      string
		condition WaitCondition
		want      bool
	}{
		{"idle valid", ConditionIdle, true},
		{"complete valid", ConditionComplete, true},
		{"generating valid", ConditionGenerating, true},
		{"healthy valid", ConditionHealthy, true},
		{"composed valid", "idle,healthy", true},
		{"invalid condition", "invalid", false},
		{"empty string", "", false},
		{"partial invalid", "idle,invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidCondition(tt.condition)
			if got != tt.want {
				t.Errorf("isValidCondition(%q) = %v, want %v", tt.condition, got, tt.want)
			}
		})
	}
}

func TestDetectAgentType(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		{"claude agent", "myproject__cc_1", "cc"},
		{"codex agent", "myproject__cod_2", "cod"},
		{"gemini agent", "myproject__gmi_3", "gmi"},
		{"session with embedded double underscore", "my__project__cc_1", "cc"},
		{"user pane", "myproject", ""},
		{"empty title", "", ""},
		{"no double underscore", "myproject_cc_1", ""},
		{"with variant", "myproject__cc_1_opus", "cc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectAgentType(tt.title)
			if got != tt.want {
				t.Errorf("detectAgentType(%q) = %q, want %q", tt.title, got, tt.want)
			}
		})
	}
}

func TestFilterPanesForWait_UsesParsedPaneType(t *testing.T) {

	panes := []tmux.Pane{
		{Index: 0, Type: tmux.AgentUser, Title: "project__cc_0"},
		{Index: 1, Type: tmux.AgentClaude, Title: "notes"},
		{Index: 2, Type: tmux.AgentType("openai-codex"), Title: "custom"},
	}

	filtered := filterPanesForWait(panes, WaitOptions{PaneIndex: -1, AgentType: "codex"})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 pane after codex filter, got %d", len(filtered))
	}
	if filtered[0].Index != 2 {
		t.Fatalf("filtered pane = %+v, want pane 2", filtered[0])
	}

	filtered = filterPanesForWait(panes, WaitOptions{PaneIndex: -1})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 agent panes with no filter, got %d", len(filtered))
	}
}

func TestMeetsSingleCondition(t *testing.T) {
	tests := []struct {
		name      string
		state     robot.AgentState
		condition WaitCondition
		want      bool
	}{
		{"waiting meets idle", robot.StateWaiting, ConditionIdle, true},
		{"generating meets generating", robot.StateGenerating, ConditionGenerating, true},
		{"waiting meets healthy", robot.StateWaiting, ConditionHealthy, true},
		{"thinking meets healthy", robot.StateThinking, ConditionHealthy, true},
		{"generating meets healthy", robot.StateGenerating, ConditionHealthy, true},
		{"error does not meet healthy", robot.StateError, ConditionHealthy, false},
		{"stalled does not meet healthy", robot.StateStalled, ConditionHealthy, false},
		{"generating does not meet idle", robot.StateGenerating, ConditionIdle, false},
		{"thinking does not meet idle", robot.StateThinking, ConditionIdle, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity := &robot.AgentActivity{
				State: tt.state,
			}
			got := meetsSingleCondition(activity, tt.condition)
			if got != tt.want {
				t.Errorf("meetsSingleCondition(state=%s, condition=%s) = %v, want %v",
					tt.state, tt.condition, got, tt.want)
			}
		})
	}
}

func TestWaitErrorTypes(t *testing.T) {
	t.Run("WaitTimeoutError", func(t *testing.T) {
		err := &WaitTimeoutError{Duration: 5000000000} // 5s
		if err.ExitCode() != 1 {
			t.Errorf("WaitTimeoutError.ExitCode() = %d, want 1", err.ExitCode())
		}
		if err.Error() == "" {
			t.Error("WaitTimeoutError.Error() should not be empty")
		}
	})

	t.Run("WaitErrorStateError", func(t *testing.T) {
		err := &WaitErrorStateError{Pane: "test__cc_1"}
		if err.ExitCode() != 3 {
			t.Errorf("WaitErrorStateError.ExitCode() = %d, want 3", err.ExitCode())
		}
		if err.Error() == "" {
			t.Error("WaitErrorStateError.Error() should not be empty")
		}
	})
}

func TestWaitConditionConstants(t *testing.T) {
	// Ensure condition constants have expected string values
	if string(ConditionIdle) != "idle" {
		t.Errorf("ConditionIdle = %q, want %q", ConditionIdle, "idle")
	}
	if string(ConditionComplete) != "complete" {
		t.Errorf("ConditionComplete = %q, want %q", ConditionComplete, "complete")
	}
	if string(ConditionGenerating) != "generating" {
		t.Errorf("ConditionGenerating = %q, want %q", ConditionGenerating, "generating")
	}
	if string(ConditionHealthy) != "healthy" {
		t.Errorf("ConditionHealthy = %q, want %q", ConditionHealthy, "healthy")
	}
}

// TestWaitDispatchSettleLatch covers the #1 fix: a terminal wait fired right
// after `ntm send` must not accept the pre-repaint idle screen as "done". It
// waits until an agent is seen working, or the settle window elapses.
func TestWaitConditionIsTerminal(t *testing.T) {
	cases := map[WaitCondition]bool{
		ConditionIdle:       true,
		ConditionComplete:   true,
		ConditionGenerating: false,
		ConditionHealthy:    false,
		"idle,healthy":      true, // any terminal part makes it terminal
		"generating,idle":   true, // ...even composed with a non-terminal
	}
	for cond, want := range cases {
		if got := waitConditionIsTerminal(cond); got != want {
			t.Errorf("waitConditionIsTerminal(%q) = %v, want %v", cond, got, want)
		}
	}
}

func TestWaitLatchSatisfied(t *testing.T) {
	const settle = 2 * time.Second
	tests := []struct {
		name        string
		terminal    bool
		seenWorking bool
		elapsed     time.Duration
		want        bool
	}{
		{"non-terminal accepts immediately", false, false, 0, true},
		{"terminal, not yet seen working, within settle -> hold", true, false, 500 * time.Millisecond, false},
		{"terminal, seen working -> accept (real work->idle)", true, true, 500 * time.Millisecond, true},
		{"terminal, no work, settle elapsed -> accept (instant no-op)", true, false, settle, true},
		{"terminal, no work, past settle -> accept", true, false, 5 * time.Second, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := waitLatchSatisfied(tt.terminal, tt.seenWorking, tt.elapsed, settle); got != tt.want {
				t.Errorf("waitLatchSatisfied(%v,%v,%v,%v) = %v, want %v",
					tt.terminal, tt.seenWorking, tt.elapsed, settle, got, tt.want)
			}
		})
	}
}
