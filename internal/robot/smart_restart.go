// Package robot provides machine-readable output for AI agents.
// smart_restart.go implements the --robot-smart-restart command for safe agent restarts.
package robot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

const (
	// shellReturnTimeout bounds the post-exit poll for the shell to return.
	// Agents like Claude can take several seconds to tear down, so a fixed
	// short sleep produced false SHELL_NOT_RETURNED failures (#187).
	shellReturnTimeout = 12 * time.Second
	// shellReturnPollInterval is the shell-return poll cadence.
	shellReturnPollInterval = 400 * time.Millisecond
	// smartRestartMinReadyTimeout is the floor for the post-launch ready-gate.
	smartRestartMinReadyTimeout = 10 * time.Second
)

// =============================================================================
// Robot Smart-Restart Command (bd-2c7f4)
// =============================================================================
//
// This is the SAFE restart mechanism that embodies the core principle:
// **NEVER interrupt agents doing useful work!!!**
//
// Unlike a naive restart that blindly kills and relaunches, smart-restart:
// 1. Checks first - Calls is-working before any action
// 2. Refuses if working - Returns SKIPPED, does NOT interrupt
// 3. Handles rate limits - Knows to wait rather than immediately restart
// 4. Verifies success - Confirms new agent actually launched

// RestartActionType represents the action taken (or not taken) for a pane.
type RestartActionType string

const (
	// ActionRestarted indicates the agent was successfully restarted.
	ActionRestarted RestartActionType = "RESTARTED"

	// ActionSkipped indicates restart was skipped (agent working or other reason).
	ActionSkipped RestartActionType = "SKIPPED"

	// ActionWaiting indicates agent is rate-limited and should wait.
	ActionWaiting RestartActionType = "WAITING"

	// ActionFailed indicates restart attempt failed.
	ActionFailed RestartActionType = "FAILED"

	// ActionWouldRestart indicates restart would occur (dry-run mode).
	ActionWouldRestart RestartActionType = "WOULD_RESTART"
)

// SmartRestartOptions configures the smart-restart command.
type SmartRestartOptions struct {
	Session       string        // Session name (required)
	Panes         []int         // Pane indices to restart (empty = all non-control panes)
	Force         bool          // Force restart even if working (dangerous!)
	DryRun        bool          // Show what would happen without doing it
	Prompt        string        // Optional prompt to send after restart
	LinesCaptured int           // Lines to capture for pre-check (default: 100)
	Verbose       bool          // Include extra debugging info
	PostWaitTime  time.Duration // Max time to wait for agent readiness after launch (ready-gate polls and exits early; floored at 10s, #187)
	HardKill      bool          // Use hard kill (kill -9) as fallback if soft exit fails (bd-bh74z)
	HardKillOnly  bool          // Skip soft exit entirely and use kill -9 immediately
}

// DefaultSmartRestartOptions returns sensible defaults.
func DefaultSmartRestartOptions() SmartRestartOptions {
	return SmartRestartOptions{
		LinesCaptured: 100,
		PostWaitTime:  6 * time.Second,
	}
}

// PreCheckInfo contains the pre-restart state assessment.
type PreCheckInfo struct {
	Recommendation   string   `json:"recommendation"`
	IsWorking        bool     `json:"is_working"`
	IsIdle           bool     `json:"is_idle"`
	IsRateLimited    bool     `json:"is_rate_limited"`
	IsContextLow     bool     `json:"is_context_low"`
	ContextRemaining *float64 `json:"context_remaining,omitempty"`
	Confidence       float64  `json:"confidence"`
	AgentType        string   `json:"agent_type"`
}

// RestartSequence documents the restart execution steps.
type RestartSequence struct {
	ExitMethod     string          `json:"exit_method"`
	ExitDurationMs int             `json:"exit_duration_ms"`
	ShellConfirmed bool            `json:"shell_confirmed"`
	AgentLaunched  bool            `json:"agent_launched"`
	AgentType      string          `json:"agent_type"`
	PromptSent     bool            `json:"prompt_sent,omitempty"`
	HardKillUsed   bool            `json:"hard_kill_used,omitempty"`   // True if hard kill was needed (bd-bh74z)
	HardKillResult *HardKillResult `json:"hard_kill_result,omitempty"` // Details of hard kill operation
}

// PostStateInfo contains the verified state after restart.
type PostStateInfo struct {
	AgentRunning bool    `json:"agent_running"`
	AgentType    string  `json:"agent_type"`
	Confidence   float64 `json:"confidence"`
}

// WaitInfo provides details about rate-limit waiting.
type WaitInfo struct {
	ResetsAt    string `json:"resets_at,omitempty"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// RestartAction documents the action taken for a single pane.
type RestartAction struct {
	Action          RestartActionType `json:"action"`
	Reason          string            `json:"reason"`
	Warning         string            `json:"warning,omitempty"`
	PreCheck        *PreCheckInfo     `json:"pre_check,omitempty"`
	RestartSequence *RestartSequence  `json:"restart_sequence,omitempty"`
	PostState       *PostStateInfo    `json:"post_state,omitempty"`
	WaitInfo        *WaitInfo         `json:"wait_info,omitempty"`
	Error           string            `json:"error,omitempty"`
	// StructuredError provides detailed error context for failure diagnosis (bd-3vc3s).
	StructuredError *StructuredError `json:"structured_error,omitempty"`
}

// RestartSummary aggregates results across all panes.
type RestartSummary struct {
	Restarted     int              `json:"restarted"`
	Skipped       int              `json:"skipped"`
	Waiting       int              `json:"waiting"`
	Failed        int              `json:"failed"`
	WouldRestart  int              `json:"would_restart,omitempty"`
	PanesByAction map[string][]int `json:"panes_by_action"`
}

// SmartRestartOutput is the response for --robot-smart-restart.
type SmartRestartOutput struct {
	RobotResponse
	Session   string                   `json:"session"`
	Timestamp string                   `json:"timestamp"`
	DryRun    bool                     `json:"dry_run"`
	Force     bool                     `json:"force"`
	Actions   map[string]RestartAction `json:"actions"`
	Summary   RestartSummary           `json:"summary"`
}

// PrintSmartRestart outputs the smart restart result in JSON format.
// This is a thin wrapper around GetSmartRestart() for CLI output.
func PrintSmartRestart(opts SmartRestartOptions) error {
	output, err := GetSmartRestart(opts)
	if err != nil {
		return err
	}
	return encodeJSON(output)
}

// GetSmartRestart performs intelligent agent restart with safety checks.
// This function returns the data struct directly, enabling CLI/REST parity.
func GetSmartRestart(opts SmartRestartOptions) (*SmartRestartOutput, error) {
	output := &SmartRestartOutput{
		RobotResponse: NewRobotResponse(true),
		Session:       opts.Session,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		DryRun:        opts.DryRun,
		Force:         opts.Force,
		Actions:       make(map[string]RestartAction),
		Summary: RestartSummary{
			PanesByAction: make(map[string][]int),
		},
	}

	// Step 1: Pre-check all panes using IsWorking
	isWorkingOpts := IsWorkingOptions{
		Session:       opts.Session,
		Panes:         opts.Panes,
		LinesCaptured: opts.LinesCaptured,
		Verbose:       opts.Verbose,
	}

	isWorkingResult, err := GetIsWorking(isWorkingOpts)
	if err != nil {
		return output, err
	}
	if !isWorkingResult.Success {
		output.Success = false
		output.Error = isWorkingResult.Error
		output.ErrorCode = isWorkingResult.ErrorCode
		output.Hint = isWorkingResult.Hint
		return output, nil
	}

	// Step 2: Process each pane
	for paneStr, workStatus := range isWorkingResult.Panes {
		// The is-working map key is "window.pane" on multi-window sessions and a
		// bare pane index on single-window sessions (#172). Parse both so the
		// restart targets the real window instead of defaulting to window 0/1
		// (the old `strconv.Atoi("1.2")` -> 0 mis-fire that ctrl-c'd a
		// nonexistent pane while the envelope claimed success).
		restartWin, paneNum := parseRestartPaneKey(paneStr)

		action := RestartAction{
			PreCheck: &PreCheckInfo{
				Recommendation:   workStatus.Recommendation,
				IsWorking:        workStatus.IsWorking,
				IsIdle:           workStatus.IsIdle,
				IsRateLimited:    workStatus.IsRateLimited,
				IsContextLow:     workStatus.IsContextLow,
				ContextRemaining: workStatus.ContextRemaining,
				Confidence:       workStatus.Confidence,
				AgentType:        workStatus.AgentType,
			},
		}

		// Determine action based on pre-check
		shouldRestart, reason, warning := decideRestart(&workStatus, opts.Force)

		switch {
		case workStatus.IsRateLimited && !opts.Force:
			action.Action = ActionWaiting
			action.Reason = "Rate limited - wait for reset"
			action.WaitInfo = buildWaitInfo(&workStatus)
			output.Summary.Waiting++
			appendPaneToAction(output.Summary.PanesByAction, "WAITING", paneNum)

		case !shouldRestart:
			action.Action = ActionSkipped
			action.Reason = reason
			output.Summary.Skipped++
			appendPaneToAction(output.Summary.PanesByAction, "SKIPPED", paneNum)

		case opts.DryRun:
			action.Action = ActionWouldRestart
			action.Reason = reason
			if warning != "" {
				action.Warning = warning
			}
			output.Summary.WouldRestart++
			appendPaneToAction(output.Summary.PanesByAction, "WOULD_RESTART", paneNum)

		default:
			// Actually perform restart
			if warning != "" {
				action.Warning = warning
			}
			restartResult, restartErr := executeRestart(opts.Session, restartWin, paneNum, workStatus.AgentType, opts)
			if restartErr != nil {
				action.Action = ActionFailed
				action.Reason = reason
				action.Error = restartErr.Error()
				// Capture structured error if available (bd-3vc3s)
				if structErr, ok := restartErr.(*StructuredError); ok {
					action.StructuredError = structErr
				}
				output.Summary.Failed++
				appendPaneToAction(output.Summary.PanesByAction, "FAILED", paneNum)
			} else {
				action.Action = ActionRestarted
				action.Reason = reason
				action.RestartSequence = restartResult
				action.PostState = verifyRestart(opts.Session, restartWin, paneNum, opts)
				output.Summary.Restarted++
				appendPaneToAction(output.Summary.PanesByAction, "RESTARTED", paneNum)
			}
		}

		output.Actions[paneStr] = action
	}

	// Fail loud (#172) instead of silently reporting success:true when the
	// restart accomplished nothing useful. Two dangerous classes are caught:
	//
	//   1. No restartable target resolved. On multi-window / window-per-agent
	//      layouts a window-local `--panes=N` filter can match nothing (or match
	//      a pane address that does not exist), leaving the action set empty or
	//      populated only with not-found error panes. Previously the top-level
	//      response still said success:true.
	//   2. One or more individual restart actions FAILED (e.g. executeRestart
	//      could not find the pane, or the agent never relaunched). Previously
	//      these were recorded under the action but the envelope stayed
	//      success:true.
	//
	// In dry-run mode no restart is attempted, so a non-empty would-restart set
	// is a successful preview and must keep success:true.
	if !opts.DryRun {
		restartableTargets := output.Summary.Restarted + output.Summary.Failed +
			output.Summary.Skipped + output.Summary.Waiting
		switch {
		case output.Summary.Failed > 0:
			output.Success = false
			output.ErrorCode = ErrCodeInternalError
			output.Error = fmt.Sprintf("%d of %d targeted pane(s) failed to restart", output.Summary.Failed, len(output.Actions))
			output.Hint = smartRestartTargetingHint(opts, output)
		case restartableTargets == 0:
			output.Success = false
			output.ErrorCode = ErrCodePaneNotFound
			output.Error = "no restartable panes matched the request"
			output.Hint = smartRestartTargetingHint(opts, output)
		}
	}

	return output, nil
}

// smartRestartTargetingHint builds an actionable remediation hint for the
// fail-loud paths. On multi-window layouts a bare pane index is window-local and
// may need a `window.pane` address; we surface the panes that were actually
// found so the caller can re-target precisely.
func smartRestartTargetingHint(opts SmartRestartOptions, output *SmartRestartOutput) string {
	found := make([]string, 0, len(output.Actions))
	for paneKey := range output.Actions {
		found = append(found, paneKey)
	}
	sort.Strings(found)

	var b strings.Builder
	if len(opts.Panes) > 0 {
		b.WriteString("On multi-window / window-per-agent layouts a bare --panes index is window-local; ")
		b.WriteString("the pane may need a window.pane address. ")
	}
	if len(found) > 0 {
		b.WriteString("Panes evaluated: ")
		b.WriteString(strings.Join(found, ", "))
		b.WriteString(". ")
	} else {
		b.WriteString("No panes were evaluated for this session. ")
	}
	b.WriteString("Run 'ntm --robot-is-working=")
	b.WriteString(opts.Session)
	b.WriteString("' to see live pane addresses and states.")
	return b.String()
}

// decideRestart determines whether a pane should be restarted based on its state.
// Returns (shouldRestart, reason, warning).
func decideRestart(status *PaneWorkStatus, force bool) (bool, string, string) {
	rec := status.Recommendation
	var warning string

	// CRITICAL: Never restart working agents unless forced
	if status.IsWorking && !force {
		return false, "Agent is actively working", ""
	}

	// Handle force on working agent
	if status.IsWorking && force {
		warning = "FORCED restart of working agent - data may be lost!"
	}

	switch rec {
	case "DO_NOT_INTERRUPT":
		if force {
			return true, "FORCED restart of working agent", warning
		}
		return false, "Agent is actively working", ""

	case "SAFE_TO_RESTART":
		return true, "Agent is idle", ""

	case "CONTEXT_LOW_CONTINUE":
		if status.IsWorking {
			if force {
				return true, "FORCED restart of working agent with low context", warning
			}
			return false, "Working with low context - let finish", ""
		}
		if status.ContextRemaining != nil {
			return true, formatRestartReason("Idle with low context (%.0f%%)", *status.ContextRemaining), ""
		}
		return true, "Idle with low context", ""

	case "RATE_LIMITED_WAIT":
		if force {
			return true, "FORCED restart despite rate limit", "Restarting won't help - still rate limited"
		}
		return false, "Rate limited - waiting for reset", ""

	case "ERROR_STATE":
		return true, "Agent in error state", ""

	default:
		if force {
			return true, "FORCED restart of unknown state", "Unknown state - results unpredictable"
		}
		return false, "Unknown state - manual inspection needed", ""
	}
}

// formatRestartReason formats a reason string with a value.
func formatRestartReason(format string, value float64) string {
	// Simple percentage formatting without fmt
	return formatReasonWithPercent(format, value)
}

// formatReasonWithPercent inserts a rounded percentage into a format string.
func formatReasonWithPercent(format string, pct float64) string {
	if !strings.Contains(format, "%") {
		return format
	}
	return fmt.Sprintf(format, pct)
}

// buildWaitInfo constructs wait information for rate-limited agents.
func buildWaitInfo(status *PaneWorkStatus) *WaitInfo {
	info := &WaitInfo{
		Suggestion: "Consider caam account switch",
	}

	// If we have rate limit info from indicators, use it
	// For now, provide generic guidance
	info.WaitSeconds = 3600 // Default 1 hour estimate

	return info
}

// parseRestartPaneKey decodes the is-working response-map key into a
// (window, pane) pair. On multi-window sessions the key is "window.pane"
// (#172); on single-window sessions it is a bare pane index. For the bare-index
// case the window is reported as -1 ("unknown") so executeRestart/verifyRestart
// fall back to the session's first window — preserving single-window behavior
// while fixing the multi-window mis-fire where strconv.Atoi("1.2") yielded 0
// and ctrl-c'd a nonexistent pane.
func parseRestartPaneKey(key string) (win, pane int) {
	if w, p, ok := strings.Cut(key, "."); ok {
		wi, errW := strconv.Atoi(strings.TrimSpace(w))
		pi, errP := strconv.Atoi(strings.TrimSpace(p))
		if errW == nil && errP == nil {
			return wi, pi
		}
	}
	p, _ := strconv.Atoi(strings.TrimSpace(key))
	return -1, p
}

// appendPaneToAction adds a pane number to the action's pane list.
func appendPaneToAction(panesByAction map[string][]int, action string, pane int) {
	if panesByAction[action] == nil {
		panesByAction[action] = []int{}
	}
	panesByAction[action] = append(panesByAction[action], pane)
}

// newRestartError creates a StructuredError for restart failures with context.
func newRestartError(code, message, phase string, pane int, agentType string, attemptedActions []string, lastOutput string) *StructuredError {
	details := NewErrorDetails().
		WithAgentType(agentType).
		WithAttemptedActions(attemptedActions...)

	if lastOutput != "" {
		details.WithLastOutput(lastOutput, 500)
	}

	return NewStructuredError(code, message).
		WithPhase(phase).
		WithPane(pane).
		WithDetails(details)
}

// executeRestart performs the actual restart sequence for a pane.
func executeRestart(session string, win, pane int, agentType string, opts SmartRestartOptions) (*RestartSequence, error) {
	seq := &RestartSequence{
		AgentType: agentType,
	}
	var attemptedActions []string
	var softExitFailed bool

	// win < 0 means "single-window session, window unknown" — fall back to the
	// session's first window. On multi-window sessions win is the pane's real
	// window index (#172), so the target addresses the correct window instead
	// of always window 1.
	if win < 0 {
		fw, err := tmux.GetFirstWindow(session)
		if err != nil {
			fw = 1 // fallback
		}
		win = fw
	}
	target := fmt.Sprintf("%s:%d.%d", session, win, pane)

	// Step 1: Exit the current agent using agent-specific method (unless HardKillOnly)
	if !opts.HardKillOnly {
		attemptedActions = append(attemptedActions, "exit-agent-"+agentType)
		exitErr := exitAgent(session, win, pane, agentType, seq)
		if exitErr != nil {
			if opts.HardKill {
				// Soft exit failed, but we're allowed to try hard kill
				softExitFailed = true
			} else {
				structErr := newRestartError(
					ErrCodeSoftExitFailed,
					"Agent did not respond to exit within timeout: "+exitErr.Error(),
					"soft_exit",
					pane,
					agentType,
					attemptedActions,
					"",
				).WithRecoveryHint(fmt.Sprintf("Try ntm --robot-smart-restart=%s --panes=%d --hard-kill to use kill -9 fallback", session, pane))
				return seq, structErr
			}
		}

		// Steps 2+3: Poll for the shell to return after the soft exit.
		// Confirmation is primarily by PROCESS DEATH (no agent child under
		// the pane shell): a fixed sleep plus prompt-glyph scraping produced
		// false SHELL_NOT_RETURNED during multi-second agent teardown and
		// false RESTARTED when a live agent's own "❯" input line satisfied
		// the glyph heuristic (#187).
		attemptedActions = append(attemptedActions, "wait-shell-return")
		waitStart := time.Now()
		shellReturned, lastOutput := waitForShellReturn(session, win, pane, shellReturnTimeout)
		seq.ExitDurationMs = int(time.Since(waitStart).Milliseconds())
		seq.ShellConfirmed = shellReturned
		if !shellReturned {
			if !opts.HardKill {
				structErr := newRestartError(
					ErrCodeShellNotReturned,
					fmt.Sprintf("Shell did not return within %s after exit - agent may still be running", shellReturnTimeout),
					"post_exit",
					pane,
					agentType,
					attemptedActions,
					lastOutput,
				).WithRecoveryHint(fmt.Sprintf("Try ntm --robot-smart-restart=%s --panes=%d --hard-kill, or manually kill the process", session, pane))
				return seq, structErr
			}
			softExitFailed = true
		}
	}

	// Step 3b: Hard kill fallback if soft exit failed or HardKillOnly (bd-bh74z)
	if opts.HardKillOnly || (opts.HardKill && softExitFailed) {
		attemptedActions = append(attemptedActions, "hard-kill")
		hardKillResult, err := hardKillAgent(session, win, pane, seq)
		seq.HardKillUsed = true
		seq.HardKillResult = hardKillResult

		if err != nil {
			structErr := newRestartError(
				ErrCodeHardKillFailed,
				"Hard kill (kill -9) failed: "+err.Error(),
				"hard_kill",
				pane,
				agentType,
				attemptedActions,
				"",
			)
			if hardKillResult != nil && hardKillResult.ShellPID > 0 {
				structErr.Details.WithChildPID(hardKillResult.ChildPID)
				structErr.Details.SetExtra("shell_pid", hardKillResult.ShellPID)
			}
			structErr.WithRecoveryHint("Manual intervention required - check process state with ps aux | grep <pid>")
			return seq, structErr
		}

		// Poll for the shell to return after hard kill — same process-death
		// confirmation as the soft-exit path (#187).
		attemptedActions = append(attemptedActions, "wait-shell-return-after-kill")
		shellReturned, lastOutput := waitForShellReturn(session, win, pane, shellReturnTimeout)
		seq.ShellConfirmed = shellReturned
		if !shellReturned {
			structErr := newRestartError(
				ErrCodeShellNotReturned,
				fmt.Sprintf("Shell did not return within %s after hard kill", shellReturnTimeout),
				"post_hard_kill",
				pane,
				agentType,
				attemptedActions,
				lastOutput,
			).WithRecoveryHint("Shell may be in unexpected state - try manually running 'reset' in the pane")
			return seq, structErr
		}
	}

	// Step 4: Launch new agent using the canonical agent alias/command name.
	alias := restartLaunchAlias(agentType)

	attemptedActions = append(attemptedActions, "launch-"+alias)
	launchErr := sendKeys(session, win, pane, alias+"\n")
	if launchErr != nil {
		structErr := newRestartError(
			ErrCodeCCLaunchFailed,
			"Failed to launch agent: "+launchErr.Error(),
			"launch",
			pane,
			agentType,
			attemptedActions,
			"", // No pane output available at launch step
		).WithRecoveryHint("Verify the agent CLI is installed and in PATH")
		return seq, structErr
	}

	// Step 5: Ready-gate — poll until the agent TUI is actually up instead of
	// sleeping a fixed interval, so the prompt is never typed into a bare
	// shell when the agent boots slowly (#187).
	readyTimeout := opts.PostWaitTime
	if readyTimeout < smartRestartMinReadyTimeout {
		readyTimeout = smartRestartMinReadyTimeout
	}
	attemptedActions = append(attemptedActions, "wait-agent-ready")
	shellPID, pidErr := getShellPID(session, win, pane)
	if pidErr != nil {
		shellPID = 0 // ready-gate falls back to content-only detection
	}
	if !waitForPaneAgentReady(target, shellPID, agentType, readyTimeout) {
		lastOutput, _ := tmux.CapturePaneOutput(target, 10)
		structErr := newRestartError(
			ErrCodeCCLaunchFailed,
			fmt.Sprintf("Agent did not become ready within %s after launch", readyTimeout),
			"launch",
			pane,
			agentType,
			attemptedActions,
			lastOutput,
		).WithRecoveryHint("Verify the agent CLI is installed and in PATH, then check the pane with ntm status")
		return seq, structErr
	}

	// Step 6: Send prompt if provided — use the double-Enter submission
	// protocol (same as ntm send / robot-send) so the prompt is reliably
	// submitted to the agent TUI rather than left typed-but-unsubmitted.
	if opts.Prompt != "" {
		attemptedActions = append(attemptedActions, "send-prompt")
		promptErr := tmux.SendKeysForAgentDoubleEnter(target, opts.Prompt, tmux.AgentType(agentType))
		if promptErr != nil {
			// Non-fatal - agent launched but prompt failed
			seq.AgentLaunched = true
			seq.PromptSent = false
			return seq, nil
		}
		seq.PromptSent = true
	}

	seq.AgentLaunched = true
	_ = attemptedActions // Used for error reporting in failure paths
	return seq, nil
}

// waitForShellReturn polls a pane until its shell has returned after an agent
// exit, primarily by PROCESS DEATH: the shell has returned iff the pane shell
// has no live child process. Pane-content heuristics are only used when the
// process state is unknowable (#187). Returns confirmation plus the last
// captured pane content for diagnostics.
func waitForShellReturn(session string, win, pane int, timeout time.Duration) (bool, string) {
	target := formatTargetWin(session, win, pane)
	deadline := time.Now().Add(timeout)
	lastOutput := ""
	for {
		if content, err := tmux.CapturePaneOutput(target, 10); err == nil {
			lastOutput = content
		}
		childAlive := false
		childKnown := false
		if shellPID, err := getShellPID(session, win, pane); err == nil && shellPID > 0 {
			childKnown = true
			childAlive = process.HasChildAlive(shellPID)
		}
		if confirmShellReturned(childAlive, childKnown, lastOutput) {
			return true, lastOutput
		}
		if time.Now().After(deadline) {
			return false, lastOutput
		}
		time.Sleep(shellReturnPollInterval)
	}
}

// confirmShellReturned reports whether a single observation confirms the pane
// shell has returned. The primary signal is process death: when the child
// state is known, the shell has returned iff no agent child is alive. Only
// when process state is unknowable does it fall back to the prompt-glyph
// heuristic — and then rejects frames the agent parser still classifies as a
// live agent, because a busy Claude pane renders its own "❯" input line which
// satisfies the glyph heuristic (#187).
func confirmShellReturned(childAlive, childKnown bool, paneContent string) bool {
	if childKnown {
		return !childAlive
	}
	if !looksLikeShellPrompt(paneContent) {
		return false
	}
	return !paneShowsLiveAgent(paneContent)
}

// paneShowsLiveAgent reports whether pane content is classified by the agent
// parser as a recognizable agent TUI (the approach verifyRestart uses).
func paneShowsLiveAgent(content string) bool {
	parser := agent.NewParser()
	state, err := parser.Parse(content)
	if err != nil || state == nil {
		return false
	}
	return state.Type != agent.AgentTypeUnknown && state.Type != agent.AgentTypeUser
}

func restartCanonicalAgentType(agentType string) agent.AgentType {
	switch canonical := agent.AgentType(agentType).Canonical(); canonical {
	case agent.AgentTypeClaudeCode,
		agent.AgentTypeCodex,
		agent.AgentTypeGemini,
		agent.AgentTypeAntigravity,
		agent.AgentTypeCursor,
		agent.AgentTypeWindsurf,
		agent.AgentTypeAider,
		agent.AgentTypeOpencode,
		agent.AgentTypeOllama,
		agent.AgentTypeUser,
		agent.AgentTypeUnknown:
		return canonical
	default:
		return agent.AgentTypeUnknown
	}
}

func restartLaunchAlias(agentType string) string {
	switch canonical := restartCanonicalAgentType(agentType); canonical {
	case "", agent.AgentTypeUnknown:
		return string(agent.AgentTypeClaudeCode)
	default:
		return string(canonical)
	}
}

// verifyRestart checks the post-restart state of a pane. win is the pane's real
// window index (#172); win < 0 falls back to the session's first window for the
// single-window case.
func verifyRestart(session string, win, pane int, opts SmartRestartOptions) *PostStateInfo {
	if win < 0 {
		fw, err := tmux.GetFirstWindow(session)
		if err != nil {
			fw = 1 // fallback
		}
		win = fw
	}
	// Capture current state
	target := fmt.Sprintf("%s:%d.%d", session, win, pane)
	content, err := tmux.CapturePaneOutput(target, 50)
	if err != nil {
		return &PostStateInfo{
			AgentRunning: false,
			Confidence:   0.0,
		}
	}

	// Parse the output to determine agent state
	parser := agent.NewParser()
	state, err := parser.Parse(content)
	if err != nil {
		return &PostStateInfo{
			AgentRunning: false,
			Confidence:   0.0,
		}
	}

	return &PostStateInfo{
		AgentRunning: state.Type != agent.AgentTypeUnknown,
		AgentType:    string(state.Type),
		Confidence:   state.Confidence,
	}
}

// looksLikeShellPrompt checks if output appears to be at a shell prompt.
func looksLikeShellPrompt(output string) bool {
	// Look for common shell prompt indicators
	shellIndicators := []string{
		"$ ",
		"% ",
		"# ",
		"❯ ",
		"→ ",
		"> ",
	}

	// Check last few lines for prompt
	lines := splitLines(output)
	if len(lines) == 0 {
		return false
	}

	// Check last non-empty line
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-5; i-- {
		line := trimSpace(lines[i])
		if line == "" {
			continue
		}
		for _, indicator := range shellIndicators {
			if containsSuffix(line, indicator) || containsSuffix(line, trimSpace(indicator)) {
				return true
			}
		}
		// Also check if line ends with these characters
		lastChar := line[len(line)-1]
		if lastChar == '$' || lastChar == '%' || lastChar == '#' || lastChar == '>' {
			return true
		}
		break // Only check last non-empty line
	}

	return false
}

// containsSuffix checks if s ends with suffix.
func containsSuffix(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

// trimSpace removes leading and trailing whitespace.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// isSpace checks if a character is whitespace.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// Error helpers to avoid fmt import for performance
func newError(msg string) error {
	return &robotError{msg: msg}
}

func wrapError(prefix string, err error) error {
	return &robotError{msg: prefix + ": " + err.Error()}
}

type robotError struct {
	msg string
}

func (e *robotError) Error() string {
	return e.msg
}
