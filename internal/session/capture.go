package session

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// Capture captures the current state of a tmux session.
func Capture(sessionName string) (state *SessionState, err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.capture", map[string]interface{}{
		"phase":          "start",
		"session":        sessionName,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        sessionName,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if state != nil {
			payload["panes"] = len(state.Panes)
			payload["agents"] = state.Agents.Total()
			payload["layout"] = state.Layout
			payload["work_dir"] = state.WorkDir
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.capture", payload, nil)
	}()

	session, err := tmux.GetSession(sessionName)
	if err != nil {
		return nil, err
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil {
		return nil, err
	}

	// Count agents by type
	agents := countAgents(panes)

	// Detect working directory from active pane, first pane, or process
	cwd := detectWorkDir(sessionName, panes)

	// Map pane states (enriches each agent pane with its resumable session id)
	paneStates := mapPaneStates(panes, cwd)

	// Get git info if in a repo
	gitBranch, gitRemote, gitCommit := getGitInfo(cwd)

	// Get layout (whole-session fallback) and per-window fidelity metadata.
	layout := getLayout(sessionName)
	windows := captureWindows(sessionName)

	// Parse session creation time (tmux format varies, try common formats)
	var createdAt time.Time
	if session.Created != "" {
		// Try parsing various tmux date formats
		formats := []string{
			"Mon Jan 2 15:04:05 2006",
			"Mon Jan _2 15:04:05 2006",
			time.UnixDate,
			time.ANSIC,
		}
		for _, format := range formats {
			if t, err := time.Parse(format, session.Created); err == nil {
				createdAt = t.UTC()
				break
			}
		}
	}

	state = &SessionState{
		Name:      sessionName,
		SavedAt:   time.Now().UTC(),
		WorkDir:   cwd,
		GitBranch: gitBranch,
		GitRemote: gitRemote,
		GitCommit: gitCommit,
		Agents:    agents,
		Panes:     paneStates,
		Layout:    layout,
		Windows:   windows,
		CreatedAt: createdAt,
		Version:   StateVersion,
	}

	return state, nil
}

// captureWindows records per-window metadata (name, exact geometry, active and
// zoom state) so restore can reproduce the full topology faithfully, not just
// the active window's layout. Returns nil if the session cannot be listed; the
// caller then relies on the whole-session Layout fallback.
func captureWindows(sessionName string) []WindowState {
	// Use the same printable delimiter as GetPanes: tmux escapes non-printable
	// bytes (e.g. \x1f) in format output, so a control-char separator would not
	// survive. Window names/layouts will not contain this token.
	sep := tmux.FieldSeparator
	format := "#{window_index}" + sep + "#{window_name}" + sep +
		"#{window_active}" + sep + "#{window_zoomed_flag}" + sep + "#{window_layout}"
	output, err := tmux.DefaultClient.Run("list-windows", "-t", sessionName, "-F", format)
	if err != nil {
		return nil
	}
	return parseWindowList(output, sep)
}

// parseWindowList parses the sep-delimited `list-windows` output produced by
// captureWindows into WindowState records. Split out as a pure function so the
// parsing is unit-testable without a live tmux server. Lines that are blank,
// under-delimited, or carry a non-numeric window index are skipped.
func parseWindowList(output, sep string) []WindowState {
	var windows []WindowState
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, sep, 5)
		if len(fields) < 5 {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSpace(fields[0]))
		if err != nil {
			continue
		}
		windows = append(windows, WindowState{
			Index:  idx,
			Name:   fields[1],
			Active: strings.TrimSpace(fields[2]) == "1",
			Zoomed: strings.TrimSpace(fields[3]) == "1",
			Layout: strings.TrimSpace(fields[4]),
		})
	}
	return windows
}

// countAgents counts agents by type from pane list.
func countAgents(panes []tmux.Pane) AgentConfig {
	config := AgentConfig{}
	for _, p := range panes {
		switch p.Type {
		case tmux.AgentClaude:
			config.Claude++
		case tmux.AgentCodex:
			config.Codex++
		case tmux.AgentGemini:
			config.Gemini++
		case tmux.AgentAntigravity:
			config.Antigravity++
		case tmux.AgentCursor:
			config.Cursor++
		case tmux.AgentWindsurf:
			config.Windsurf++
		case tmux.AgentAider:
			config.Aider++
		case tmux.AgentOpencode:
			config.Opencode++
		case tmux.AgentOllama:
			config.Ollama++
		case tmux.AgentUser:
			config.User++
		}
	}
	return config
}

// mapPaneStates converts tmux panes to PaneState. sessionCwd is the session's
// detected working directory, used as a fallback when a pane's own current path
// cannot be read, for discovering each agent pane's resumable session id.
func mapPaneStates(panes []tmux.Pane, sessionCwd string) []PaneState {
	states := make([]PaneState, len(panes))
	for i, p := range panes {
		states[i] = PaneState{
			Title:       p.Title,
			Index:       p.Index,
			WindowIndex: p.WindowIndex,
			AgentType:   string(p.Type),
			Model:       p.Variant,
			Active:      p.Active,
			Width:       p.Width,
			Height:      p.Height,
			PaneID:      p.ID,
		}

		// Best-effort: link each agent pane to its provider session id so it
		// can be resumed later. User/editor panes return no provider.
		if agentsession.ResumeProvider(string(p.Type)) == "" {
			continue
		}
		paneCwd := paneCurrentPath(p.ID)
		if paneCwd == "" {
			paneCwd = sessionCwd
		}
		if info := agentsession.Discover(string(p.Type), paneCwd); info != nil {
			states[i].SessionID = info.SessionID
			states[i].SessionProvider = info.Provider
			states[i].SessionFile = info.SourcePath
		}
	}
	return states
}

// paneCurrentPath reads a single pane's current working directory via tmux.
// Returns "" on any failure.
func paneCurrentPath(paneID string) string {
	output, err := tmux.DefaultClient.Run("display-message", "-t", paneID, "-p", "#{pane_current_path}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

// detectWorkDir attempts to detect the working directory for the session.
func detectWorkDir(sessionName string, panes []tmux.Pane) string {
	// Try to get the active pane's current path via tmux
	for _, p := range panes {
		if p.Active {
			output, err := tmux.DefaultClient.Run("display-message", "-t", p.ID, "-p", "#{pane_current_path}")
			if err == nil && len(output) > 0 {
				path := strings.TrimSpace(output)
				if path != "" {
					return path
				}
			}
			break
		}
	}

	// Fallback: try the first pane if no active pane or it failed
	if len(panes) > 0 {
		output, err := tmux.DefaultClient.Run("display-message", "-t", panes[0].ID, "-p", "#{pane_current_path}")
		if err == nil && len(output) > 0 {
			path := strings.TrimSpace(output)
			if path != "" {
				return path
			}
		}
	}

	// Fallback: try to determine from current process working directory
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}

	// Final fallback: user home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		return homeDir
	}

	return ""
}

// getGitInfo extracts git branch, remote, and commit from a directory.
func getGitInfo(dir string) (branch, remote, commit string) {
	return getGitInfoWithTimeout(dir, 5*time.Second)
}

func getGitInfoWithTimeout(dir string, timeout time.Duration) (branch, remote, commit string) {
	if dir == "" {
		return "", "", ""
	}

	branch = runGitInfoCommand(dir, timeout, "rev-parse", "--abbrev-ref", "HEAD")
	remote = runGitInfoCommand(dir, timeout, "remote", "get-url", "origin")
	commit = runGitInfoCommand(dir, timeout, "rev-parse", "--short", "HEAD")

	return branch, remote, commit
}

func runGitInfoCommand(dir string, timeout time.Duration, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// getLayout gets the current tmux layout for the session.
func getLayout(sessionName string) string {
	output, err := tmux.DefaultClient.Run("display-message", "-t", sessionName, "-p", "#{window_layout}")
	if err != nil {
		return "tiled" // Default fallback
	}
	// Return the layout string as-is. tmux select-layout accepts both
	// named layouts (tiled, even-horizontal) and serialized geometry strings.
	return strings.TrimSpace(output)
}
