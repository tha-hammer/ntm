package robot

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

func TestGetSafetySimulationReportsPlanWithoutExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	output, err := GetSafetySimulation(SafetySimulationOptions{
		Command: "git status",
		Steps: []string{
			"git reset --hard HEAD~1",
			"git commit --amend",
			"",
		},
	})
	if err != nil {
		t.Fatalf("GetSafetySimulation returned error: %v", err)
	}

	if !output.Success {
		t.Fatalf("Success = false, want true: %+v", output.RobotResponse)
	}
	if output.SafeToRun {
		t.Fatal("SafeToRun = true, want false for blocked/approval/invalid plan")
	}
	if len(output.Commands) != 4 {
		t.Fatalf("commands = %d, want 4", len(output.Commands))
	}
	if len(output.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(output.Steps))
	}
	if output.Summary.BlockedSteps != 1 || output.Summary.ApprovalSteps != 1 || output.Summary.InvalidSteps != 1 {
		t.Fatalf("summary = %+v, want one blocked, one approval, one invalid", output.Summary)
	}
	if output.Steps[1].Decision != policy.SimulationDecisionBlock {
		t.Fatalf("blocked decision = %q", output.Steps[1].Decision)
	}
	if output.Steps[1].Policy == nil || output.Steps[1].Policy.Pattern == "" {
		t.Fatalf("blocked step missing policy provenance: %+v", output.Steps[1])
	}
	if len(output.Steps[1].SaferAlternatives) == 0 {
		t.Fatalf("blocked step missing safer alternatives: %+v", output.Steps[1])
	}
}

func TestGetSafetySimulationEmptyPlanKeepsArraysPresent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	output, err := GetSafetySimulation(SafetySimulationOptions{})
	if err != nil {
		t.Fatalf("GetSafetySimulation returned error: %v", err)
	}

	if !output.Success {
		t.Fatalf("Success = false, want true: %+v", output.RobotResponse)
	}
	if output.Commands == nil {
		t.Fatal("Commands is nil, want empty array")
	}
	if output.Steps == nil {
		t.Fatal("Steps is nil, want empty array")
	}
	if output.SafeToRun {
		t.Fatal("empty plan should not be safe")
	}
	if output.Summary.InvalidSteps != 1 {
		t.Fatalf("InvalidSteps = %d, want 1", output.Summary.InvalidSteps)
	}
}
