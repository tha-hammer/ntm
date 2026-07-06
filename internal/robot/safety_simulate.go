package robot

import (
	"fmt"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/policy"
)

// SafetySimulationOptions configures --robot-safety-simulate.
type SafetySimulationOptions struct {
	Command string
	Steps   []string
}

// SafetySimulationOutput is the robot envelope for a no-execution policy simulation.
type SafetySimulationOutput struct {
	RobotResponse
	GeneratedAt string                   `json:"generated_at"`
	Commands    []string                 `json:"commands"`
	Steps       []policy.SimulationStep  `json:"steps"`
	Summary     policy.SimulationSummary `json:"summary"`
	SafeToRun   bool                     `json:"safe_to_run"`
	Notes       []string                 `json:"notes,omitempty"`
}

// GetSafetySimulation evaluates a proposed command plan without executing it.
func GetSafetySimulation(opts SafetySimulationOptions) (*SafetySimulationOutput, error) {
	meta, finish := StartResponseMeta("robot-safety-simulate")
	defer finish()

	commands := normalizeSafetySimulationCommands(opts)
	p, err := policy.LoadOrDefault()
	if err != nil {
		output := &SafetySimulationOutput{
			RobotResponse: NewErrorResponse(
				err,
				ErrCodeInternalError,
				"Fix the NTM safety policy file, then rerun the simulation.",
			),
			GeneratedAt: FormatTimestamp(time.Now()),
			Commands:    commands,
			Steps:       []policy.SimulationStep{},
			SafeToRun:   false,
			Notes:       []string{"simulation only; no command was executed"},
		}
		output.Meta = meta.WithExitCode(1)
		return output, nil
	}

	report := policy.SimulatePlan(p, commands)
	output := &SafetySimulationOutput{
		RobotResponse: NewRobotResponseWithMeta(true, meta.WithExitCode(0)),
		GeneratedAt:   FormatTimestamp(report.GeneratedAt),
		Commands:      commands,
		Steps:         report.Steps,
		Summary:       report.Summary,
		SafeToRun:     report.SafeToRun,
		Notes:         report.Notes,
	}
	if output.Commands == nil {
		output.Commands = []string{}
	}
	if output.Steps == nil {
		output.Steps = []policy.SimulationStep{}
	}
	return output, nil
}

func normalizeSafetySimulationCommands(opts SafetySimulationOptions) []string {
	commands := make([]string, 0, len(opts.Steps)+1)
	if strings.TrimSpace(opts.Command) != "" {
		commands = append(commands, opts.Command)
	}
	commands = append(commands, opts.Steps...)
	return commands
}

// PrintSafetySimulation emits a robot JSON simulation report.
func PrintSafetySimulation(opts SafetySimulationOptions) error {
	output, err := GetSafetySimulation(opts)
	if err != nil {
		return fmt.Errorf("safety simulation: %w", err)
	}
	return outputJSON(output)
}
