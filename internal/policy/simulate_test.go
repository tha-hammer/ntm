package policy

import (
	"strings"
	"testing"
)

func TestSimulatePlanAllowBlockApprovalAndInvalid(t *testing.T) {
	report := SimulatePlan(DefaultPolicy(), []string{
		"git status",
		"git reset --hard HEAD~1",
		"git commit --amend",
		"  ",
	})

	if report.SafeToRun {
		t.Fatal("SafeToRun = true, want false for blocked/approval/invalid plan")
	}
	if report.Summary.TotalSteps != 4 {
		t.Fatalf("TotalSteps = %d, want 4", report.Summary.TotalSteps)
	}
	if report.Summary.AllowedSteps != 1 {
		t.Fatalf("AllowedSteps = %d, want 1", report.Summary.AllowedSteps)
	}
	if report.Summary.BlockedSteps != 1 {
		t.Fatalf("BlockedSteps = %d, want 1", report.Summary.BlockedSteps)
	}
	if report.Summary.ApprovalSteps != 1 {
		t.Fatalf("ApprovalSteps = %d, want 1", report.Summary.ApprovalSteps)
	}
	if report.Summary.InvalidSteps != 1 {
		t.Fatalf("InvalidSteps = %d, want 1", report.Summary.InvalidSteps)
	}

	blocked := report.Steps[1]
	if blocked.Decision != SimulationDecisionBlock {
		t.Fatalf("blocked decision = %q, want %q", blocked.Decision, SimulationDecisionBlock)
	}
	if blocked.Policy == nil || blocked.Policy.Pattern == "" {
		t.Fatalf("blocked policy provenance missing: %+v", blocked)
	}
	if len(blocked.SaferAlternatives) == 0 {
		t.Fatalf("blocked step missing safer alternatives: %+v", blocked)
	}

	approval := report.Steps[2]
	if approval.Decision != SimulationDecisionApproval || !approval.RequiresApproval {
		t.Fatalf("approval step = %+v, want approval_required with RequiresApproval=true", approval)
	}

	invalid := report.Steps[3]
	if invalid.Decision != SimulationDecisionInvalid || invalid.Error == "" {
		t.Fatalf("invalid step = %+v, want invalid with error", invalid)
	}
}

func TestSimulatePlanPolicyPrecedenceAllowsForceWithLease(t *testing.T) {
	report := SimulatePlan(DefaultPolicy(), []string{"git push origin main --force-with-lease"})
	if !report.SafeToRun {
		t.Fatalf("SafeToRun = false, want true: %+v", report)
	}
	if got := report.Steps[0].Decision; got != SimulationDecisionAllow {
		t.Fatalf("decision = %q, want %q", got, SimulationDecisionAllow)
	}
	if report.Steps[0].Policy == nil {
		t.Fatalf("expected explicit allow policy provenance: %+v", report.Steps[0])
	}
}

func TestSimulatePlanSLBApprovalProvenance(t *testing.T) {
	report := SimulatePlan(DefaultPolicy(), []string{"ntm force_release internal/auth/**"})
	step := report.Steps[0]
	if step.Decision != SimulationDecisionApproval {
		t.Fatalf("decision = %q, want approval_required", step.Decision)
	}
	if !step.RequiresSLB {
		t.Fatalf("RequiresSLB = false, want true: %+v", step)
	}
	if report.Summary.SLBRequiredSteps != 1 {
		t.Fatalf("SLBRequiredSteps = %d, want 1", report.Summary.SLBRequiredSteps)
	}
}

func TestSimulatePlanEmptyPlanIsUnsafe(t *testing.T) {
	report := SimulatePlan(DefaultPolicy(), nil)
	if report.SafeToRun {
		t.Fatal("empty plan should not be safe to run")
	}
	if report.Summary.InvalidSteps != 1 {
		t.Fatalf("InvalidSteps = %d, want 1", report.Summary.InvalidSteps)
	}
	if len(report.Notes) == 0 {
		t.Fatal("expected explanatory note for empty plan")
	}
}

// bd-gg7x4: the destructive-reset suggestion list must offer a backup
// pattern that captures the WORKING TREE (uncommitted edits), not just
// committed state. The previous worktree-add HEAD suggestion was broken
// because git worktree only checks out HEAD — uncommitted edits would
// be lost the moment the user proceeded with the destructive operation.
// Also, no suggestion may use a fixed /tmp path (collides on a second
// concurrent invocation), and stash is forbidden by project policy.
func TestSimulatePlanResetHardSuggestsWorkingTreeBackup(t *testing.T) {
	report := SimulatePlan(DefaultPolicy(), []string{"git reset --hard HEAD~1"})
	if len(report.Steps) == 0 {
		t.Fatal("no steps reported")
	}
	step := report.Steps[0]
	if step.Decision != SimulationDecisionBlock {
		t.Fatalf("decision = %q, want %q", step.Decision, SimulationDecisionBlock)
	}
	if len(step.SaferAlternatives) == 0 {
		t.Fatalf("missing safer alternatives: %+v", step)
	}

	joined := strings.Join(step.SaferAlternatives, "\n")

	// Stash is forbidden by project policy and must not be suggested.
	if strings.Contains(joined, "git stash") {
		t.Errorf("safer alternatives suggest forbidden 'git stash': %v", step.SaferAlternatives)
	}

	// Fixed /tmp/safety-backup path would collide between agents — the
	// suggestion must use a unique path. Allow the timestamped variant.
	for _, alt := range step.SaferAlternatives {
		if strings.Contains(alt, "/tmp/safety-backup ") || strings.HasSuffix(alt, "/tmp/safety-backup") {
			t.Errorf("safer alternative uses fixed collision-prone path: %q", alt)
		}
	}

	// At least one suggestion must actually back up the WORKING TREE
	// (not just committed state). git-worktree-add HEAD does NOT count.
	hasWorkingTreeBackup := false
	for _, alt := range step.SaferAlternatives {
		if strings.Contains(alt, "cp ") || strings.Contains(alt, "tar ") || strings.Contains(alt, "rsync ") {
			hasWorkingTreeBackup = true
			break
		}
	}
	if !hasWorkingTreeBackup {
		t.Errorf("no suggestion backs up the working tree (cp/tar/rsync); only had: %v", step.SaferAlternatives)
	}
}

func TestSimulatePlanCompilesConstructedPolicy(t *testing.T) {
	p := &Policy{
		Blocked: []Rule{
			{Pattern: `dangerous\s+thing`, Reason: "constructed policy should compile during simulation"},
		},
	}

	report := SimulatePlan(p, []string{"dangerous thing"})
	if report.SafeToRun {
		t.Fatal("constructed policy blocked rule should make the plan unsafe")
	}
	if got := report.Steps[0].Decision; got != SimulationDecisionBlock {
		t.Fatalf("decision = %q, want %q", got, SimulationDecisionBlock)
	}
	if report.Steps[0].Policy == nil || report.Steps[0].Policy.Reason == "" {
		t.Fatalf("expected matched constructed policy provenance: %+v", report.Steps[0])
	}
}
