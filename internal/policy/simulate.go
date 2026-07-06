package policy

import (
	"strings"
	"time"
)

const (
	SimulationDecisionAllow    = "allow"
	SimulationDecisionBlock    = "block"
	SimulationDecisionApproval = "approval_required"
	SimulationDecisionInvalid  = "invalid"
)

// SimulationReport is a dry-run evaluation of a multi-step command plan.
type SimulationReport struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Steps       []SimulationStep  `json:"steps"`
	Summary     SimulationSummary `json:"summary"`
	SafeToRun   bool              `json:"safe_to_run"`
	Notes       []string          `json:"notes,omitempty"`
}

// SimulationStep is the simulated verdict for one proposed command.
type SimulationStep struct {
	Index             int              `json:"index"`
	Command           string           `json:"command"`
	Decision          string           `json:"decision"`
	Action            string           `json:"action"`
	WouldBeAllowed    bool             `json:"would_be_allowed"`
	RequiresApproval  bool             `json:"requires_approval"`
	RequiresSLB       bool             `json:"requires_slb,omitempty"`
	Policy            *SimulationMatch `json:"policy,omitempty"`
	Error             string           `json:"error,omitempty"`
	SaferAlternatives []string         `json:"safer_alternatives,omitempty"`
	PolicyProvenance  []string         `json:"policy_provenance,omitempty"`
}

// SimulationMatch records the policy rule that matched the command.
type SimulationMatch struct {
	Action  string `json:"action"`
	Pattern string `json:"pattern,omitempty"`
	Reason  string `json:"reason,omitempty"`
	SLB     bool   `json:"slb,omitempty"`
}

// SimulationSummary aggregates simulated verdicts.
type SimulationSummary struct {
	TotalSteps       int `json:"total_steps"`
	AllowedSteps     int `json:"allowed_steps"`
	BlockedSteps     int `json:"blocked_steps"`
	ApprovalSteps    int `json:"approval_steps"`
	InvalidSteps     int `json:"invalid_steps"`
	SLBRequiredSteps int `json:"slb_required_steps"`
}

// SimulatePlan evaluates commands against policy without executing anything.
func SimulatePlan(p *Policy, commands []string) SimulationReport {
	if p == nil {
		p = DefaultPolicy()
	}
	report := SimulationReport{
		GeneratedAt: time.Now().UTC(),
		Steps:       []SimulationStep{},
		SafeToRun:   true,
		Notes:       []string{"simulation only; no command was executed"},
	}
	if err := p.compile(); err != nil {
		report.SafeToRun = false
		report.Notes = append(report.Notes, "policy compilation failed: "+err.Error())
	}
	for i, raw := range commands {
		step := simulateStep(p, i+1, raw)
		report.Steps = append(report.Steps, step)
		report.Summary.TotalSteps++
		switch step.Decision {
		case SimulationDecisionAllow:
			report.Summary.AllowedSteps++
		case SimulationDecisionBlock:
			report.Summary.BlockedSteps++
			report.SafeToRun = false
		case SimulationDecisionApproval:
			report.Summary.ApprovalSteps++
			report.SafeToRun = false
		case SimulationDecisionInvalid:
			report.Summary.InvalidSteps++
			report.SafeToRun = false
		}
		if step.RequiresSLB {
			report.Summary.SLBRequiredSteps++
		}
	}
	if len(report.Steps) == 0 {
		report.SafeToRun = false
		report.Summary.InvalidSteps = 1
		report.Notes = append(report.Notes, "no commands were provided")
	}
	return report
}

func simulateStep(p *Policy, index int, raw string) SimulationStep {
	command := strings.TrimSpace(raw)
	step := SimulationStep{
		Index:   index,
		Command: command,
	}
	if command == "" {
		step.Decision = SimulationDecisionInvalid
		step.Action = SimulationDecisionInvalid
		step.Error = "empty command"
		step.SaferAlternatives = []string{"Provide a non-empty command string."}
		return step
	}

	match := p.Check(command)
	if match == nil {
		step.Decision = SimulationDecisionAllow
		step.Action = string(ActionAllow)
		step.WouldBeAllowed = true
		step.PolicyProvenance = []string{"implicit_allow:no_policy_match"}
		return step
	}

	step.Action = string(match.Action)
	step.Policy = &SimulationMatch{
		Action:  string(match.Action),
		Pattern: match.Pattern,
		Reason:  match.Reason,
		SLB:     match.SLB,
	}
	step.PolicyProvenance = []string{string(match.Action) + ":" + match.Pattern}
	step.SaferAlternatives = saferAlternatives(command, match)

	switch match.Action {
	case ActionAllow:
		step.Decision = SimulationDecisionAllow
		step.WouldBeAllowed = true
	case ActionBlock:
		step.Decision = SimulationDecisionBlock
	case ActionApprove:
		step.Decision = SimulationDecisionApproval
		step.RequiresApproval = true
		step.RequiresSLB = match.SLB
	default:
		step.Decision = SimulationDecisionInvalid
		step.Action = SimulationDecisionInvalid
		step.Error = "unknown policy action"
	}
	return step
}

func saferAlternatives(command string, match *Match) []string {
	if match == nil {
		return nil
	}
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "git reset") && strings.Contains(lower, "--hard"):
		return []string{
			"git status",
			"git diff",
			// Capture the WORKING TREE (uncommitted edits + staged changes)
			// to a unique /tmp path before the destructive operation.
			// 'git worktree add HEAD' would only capture committed state;
			// 'git stash' is forbidden by project policy. cp -a preserves
			// timestamps and modes; the timestamped path avoids colliding
			// with a parallel agent's backup in another pane.
			"cp -a . /tmp/ntm-safety-backup-$(date +%s)",
			"git reset --soft <ref>",
		}
	case strings.Contains(lower, "git clean"):
		return []string{
			"git clean -nd",
			"git status --short",
			"Move unwanted files to a backup directory after explicit approval.",
		}
	case strings.Contains(lower, "git push") && strings.Contains(lower, "--force"):
		return []string{
			"git push --force-with-lease",
			"git push --dry-run",
		}
	case strings.Contains(lower, "rm ") || strings.Contains(lower, "rm\t"):
		return []string{
			"List targets first with ls or find.",
			"Move files to a temporary backup directory after explicit approval.",
			"Ask the user for the exact deletion command if deletion is required.",
		}
	case match.Action == ActionApprove:
		return []string{
			"Request approval before running this command.",
			"Split the plan into reversible steps and re-simulate.",
		}
	default:
		return []string{"Review the matched policy reason before proceeding."}
	}
}
