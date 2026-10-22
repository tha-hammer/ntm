package session

import (
	"context"
	"os"
	"os/exec"
	"sort"
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

	// Map pane states (enriches each agent pane with its resumable session id).
	// Recorded ids win; new panes are lazily discovered and deduped. The
	// refreshed record is stashed for Save to persist after a successful
	// write (keyed by the live session name).
	paneStates, refreshed := reconcilePaneIDs(panes, resolvePaneCwds(panes, cwd), loadPaneRecord(StorageDir(), sessionName), agentsession.Discover)

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
	state.refreshedPaneRecords = refreshed

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
// With no record it behaves as before (discover per pane) plus dedupe, so
// same-cwd same-provider panes never save the same id twice.
func mapPaneStates(panes []tmux.Pane, sessionCwd string) []PaneState {
	states, _ := reconcilePaneIDs(panes, resolvePaneCwds(panes, sessionCwd), nil, agentsession.Discover)
	return states
}

// resolvePaneCwds reads each pane's current working directory via tmux,
// falling back to the session cwd when it cannot be read.
func resolvePaneCwds(panes []tmux.Pane, sessionCwd string) []string {
	cwds := make([]string, len(panes))
	for i, p := range panes {
		cwd := paneCurrentPath(p.ID)
		if cwd == "" {
			cwd = sessionCwd
		}
		cwds[i] = cwd
	}
	return cwds
}

// reconcilePaneIDs is the pure core behind save-time pane enrichment. It
// matches live panes against the record by stable identity, fills matched
// panes from the record (no Discover call), lazily discovers ids for panes
// with no recorded id, and dedupes so the same id is never assigned twice
// within a provider. It returns the saved states in live input order plus
// the refreshed record (live matched panes only, in sorted order; a
// refreshed filled id is never regressed to empty).
func reconcilePaneIDs(panes []tmux.Pane, paneCwds []string, record []PaneRecordEntry, discover func(agentType, cwd string) *agentsession.Info) ([]PaneState, []PaneRecordEntry) {
	stored := make(map[paneRecordKey]PaneRecordEntry, len(record))
	for _, e := range record {
		k := paneRecordKeyOfEntry(e)
		if _, dup := stored[k]; !dup {
			stored[k] = e
		}
	}

	// Process a copy sorted by (WindowIndex, Index) — mirroring restore —
	// so first-in-order wins deterministically regardless of list-panes
	// ordering. Output still follows live input order.
	order := make([]int, len(panes))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		pa, pb := panes[order[a]], panes[order[b]]
		if pa.WindowIndex != pb.WindowIndex {
			return pa.WindowIndex < pb.WindowIndex
		}
		return pa.Index < pb.Index
	})

	cwdOf := func(i int) string {
		if i >= 0 && i < len(paneCwds) {
			return paneCwds[i]
		}
		return ""
	}

	type providerID struct {
		provider string
		id       string
	}
	// Recorded ids seed the used-set before lazy fill runs, so lazy fill
	// can never steal an id already recorded for another pane.
	used := map[providerID]bool{}
	for _, idx := range order {
		p := panes[idx]
		provider := agentsession.ResumeProvider(string(p.Type))
		if provider == "" {
			continue
		}
		if e, ok := stored[paneRecordKeyOfPane(p)]; ok && e.SessionID != "" {
			used[providerID{provider, e.SessionID}] = true
		}
	}

	type assignment struct {
		id       string
		provider string
		file     string
	}
	assigned := make([]assignment, len(panes))
	claimed := map[providerID]bool{}

	// Recorded panes keep their recorded ids (first in sorted order wins a
	// recorded duplicate); no Discover call for any recorded pane.
	for _, idx := range order {
		p := panes[idx]
		provider := agentsession.ResumeProvider(string(p.Type))
		if provider == "" {
			continue
		}
		e, ok := stored[paneRecordKeyOfPane(p)]
		if !ok || e.SessionID == "" {
			continue
		}
		key := providerID{provider, e.SessionID}
		if claimed[key] {
			continue // recorded duplicate: saved state gets empty, record keeps old id
		}
		claimed[key] = true
		assigned[idx] = assignment{id: e.SessionID, provider: e.SessionProvider, file: e.SessionFile}
	}

	// Lazy fill: panes with no recorded id share one memoized discovery
	// result per (provider, cwd); an already-used id assigns empty (fresh
	// launch on resume beats a harmful duplicate resume).
	type memoKey struct {
		provider string
		cwd      string
	}
	memo := map[memoKey]*agentsession.Info{}
	discovered := map[memoKey]bool{}
	for _, idx := range order {
		p := panes[idx]
		if assigned[idx].id != "" {
			continue
		}
		provider := agentsession.ResumeProvider(string(p.Type))
		if provider == "" {
			continue
		}
		if e, ok := stored[paneRecordKeyOfPane(p)]; ok && e.SessionID != "" {
			continue // recorded-duplicate loser: stays empty, keeps old id in refresh
		}
		cwd := cwdOf(idx)
		mk := memoKey{provider, cwd}
		if !discovered[mk] {
			discovered[mk] = true
			if discover != nil {
				memo[mk] = discover(string(p.Type), cwd)
			}
		}
		info := memo[mk]
		if info == nil || info.SessionID == "" {
			continue
		}
		key := providerID{provider, info.SessionID}
		if used[key] || claimed[key] {
			continue
		}
		claimed[key] = true
		assigned[idx] = assignment{id: info.SessionID, provider: info.Provider, file: info.SourcePath}
	}

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
		if a := assigned[i]; a.id != "" {
			states[i].SessionID = a.id
			states[i].SessionProvider = a.provider
			states[i].SessionFile = a.file
		}
	}

	refreshed := make([]PaneRecordEntry, 0, len(order))
	for _, idx := range order {
		p := panes[idx]
		if agentsession.ResumeProvider(string(p.Type)) == "" {
			continue
		}
		e := PaneRecordEntry{
			WindowIndex: p.WindowIndex,
			AgentType:   string(p.Type),
			NTMIndex:    p.NTMIndex,
		}
		if a := assigned[idx]; a.id != "" {
			e.SessionID, e.SessionProvider, e.SessionFile = a.id, a.provider, a.file
		} else if s, ok := stored[paneRecordKeyOfPane(p)]; ok && s.SessionID != "" {
			// Never regress a filled id to empty: keep the old linkage
			// when this cycle yielded nothing (idle agent, nil discovery).
			e.SessionID, e.SessionProvider, e.SessionFile = s.SessionID, s.SessionProvider, s.SessionFile
		}
		refreshed = append(refreshed, e)
	}
	return states, refreshed
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
