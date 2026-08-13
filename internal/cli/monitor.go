package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/archive"
	"github.com/Dicklesworthstone/ntm/internal/ensemble"
	"github.com/Dicklesworthstone/ntm/internal/events"
	"github.com/Dicklesworthstone/ntm/internal/plugins"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/summary"
	"github.com/Dicklesworthstone/ntm/internal/supervisor"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/webhook"
)

func newMonitorCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "internal-monitor <session>",
		Short:  "Run the resilience monitor for a session (internal use)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMonitor(args[0])
		},
	}
}

func runMonitor(session string) error {
	// Load manifest
	manifest, err := resilience.LoadManifest(session)
	if err != nil {
		return fmt.Errorf("loading manifest: %w", err)
	}

	// Close the singleton ensemble state store on exit so the underlying
	// SQLite database is not leaked.
	defer ensemble.CloseDefaultStateStore()

	// Register signal handler early so signals during startup are handled
	// gracefully instead of causing an abrupt exit.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Ensure session exists (retry a few times for transient tmux failures)
	const startupRetries = 3
	const startupRetryDelay = 2 * time.Second
	sessionFound := false
	for i := 0; i < startupRetries; i++ {
		if tmux.SessionExists(session) {
			sessionFound = true
			break
		}
		if i < startupRetries-1 {
			fmt.Fprintf(os.Stderr, "Session '%s' not found on startup attempt %d/%d, retrying in %v...\n",
				session, i+1, startupRetries, startupRetryDelay)
			time.Sleep(startupRetryDelay)
		}
	}
	if !sessionFound {
		fmt.Fprintf(os.Stderr, "Session '%s' missing after %d startup checks (%s)\n",
			session, startupRetries, detectSessionTerminationCause(session))
		events.DefaultEmitter().Emit(events.NewWebhookEvent(
			events.WebhookSessionEnded,
			session,
			"",
			"",
			fmt.Sprintf("Session %s ended before monitor start", session),
			map[string]string{
				"project_dir": manifest.ProjectDir,
			},
		))
		// Don't delete manifest on startup failure — session may be transiently unavailable
		return nil
	}

	// Enable project webhooks (if configured) for this session so monitor-driven
	// agent lifecycle events (crash/restart/rate_limit, etc) can fan out.
	if cfg != nil {
		redactCfg := cfg.Redaction.ToRedactionLibConfig()
		bridge, err := webhook.StartBridgeFromProjectConfig(manifest.ProjectDir, session, events.DefaultBus, &redactCfg)
		if err != nil {
			slog.Default().Debug("webhook bridge init failed", "session", session, "error", err)
		} else if bridge != nil {
			defer bridge.Close()
		}
	}

	// Initialize Supervisor
	sup, err := supervisor.New(supervisor.Config{
		SessionID:  session,
		ProjectDir: manifest.ProjectDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize supervisor: %v\n", err)
	} else {
		// Start default daemons. Agent Mail is external by default: ntm may
		// use the configured MCP URL, but it must not start, stop, restart, or
		// compete with a user-owned `am` unless explicitly opted in.
		amSupervised := shouldSuperviseAgentMailDaemon()
		for _, spec := range supervisor.DefaultSpecs() {
			if spec.Name == "am" && !amSupervised {
				fmt.Printf("Skipping daemon: am (Agent Mail is externally managed; set [agent_mail].supervisor_enabled = true to let ntm own it)\n")
				continue
			}
			if err := sup.Start(spec); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start daemon %s: %v\n", spec.Name, err)
			} else {
				fmt.Printf("Started daemon: %s\n", spec.Name)
			}
		}
		defer sup.Shutdown()
	}

	// Load plugins to populate config
	pluginsDir := filepath.Join(selectedConfigDir(), "agents")
	if loadedPlugins, err := plugins.LoadAgentPlugins(pluginsDir); err == nil && cfg != nil {
		if cfg.Agents.Plugins == nil {
			cfg.Agents.Plugins = make(map[string]string)
		}
		for _, p := range loadedPlugins {
			cfg.Agents.Plugins[p.Name] = p.Command
		}
	}

	// Initialize resilience monitor
	monitor := resilience.NewMonitor(session, manifest.ProjectDir, cfg, manifest.AutoRestart)

	// Register agents
	for _, agent := range manifest.Agents {
		monitor.RegisterAgent(agent.PaneID, agent.PaneIndex, 0, agent.Type, agent.Model, agent.Command)
	}

	// Start monitoring
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	monitor.Start(ctx)

	// Initialize archiver for background CASS capture
	archiverOpts := archive.DefaultArchiverOptions(session)
	archiver, err := archive.NewArchiver(archiverOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize archiver: %v\n", err)
	} else {
		fmt.Printf("Starting archiver for session %s\n", session)
		go func() {
			if err := archiver.Run(ctx); err != nil && err != context.Canceled {
				fmt.Fprintf(os.Stderr, "Archiver error: %v\n", err)
			}
		}()
		defer archiver.Close()
	}

	// Poll for session existence periodically to exit if session is killed.
	// Use consecutive-miss counting to tolerate transient tmux failures.
	const maxMisses = 5 // ~25 seconds at 5s interval before giving up
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Snapshot output periodically to generate summary on exit
	snapshotTicker := time.NewTicker(30 * time.Second)
	defer snapshotTicker.Stop()
	lastOutputs := make(map[string]string)

	fmt.Printf("Monitoring session '%s' for resilience...\n", session)

	missCount := 0
	for {
		select {
		case <-sigChan:
			fmt.Println("Monitor stopping...")
			monitor.Stop()
			// Try to generate summary on signal too (recover from panics
			// to ensure graceful exit)
			func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(os.Stderr, "Panic in session summary generation: %v\n", r)
					}
				}()
				generateEndSessionSummary(session, lastOutputs, manifest)
			}()
			return nil
		case <-ticker.C:
			if !tmux.SessionExists(session) {
				missCount++
				cause := detectSessionTerminationCause(session)
				if missCount < maxMisses {
					fmt.Fprintf(os.Stderr, "Session '%s' not found (%d/%d consecutive misses, cause: %s)\n",
						session, missCount, maxMisses, cause)
					continue
				}
				// Confirmed permanently gone after maxMisses consecutive failures
				fmt.Printf("Session ended (%d consecutive misses), stopping monitor (%s)\n", missCount, cause)
				events.DefaultEmitter().Emit(events.NewWebhookEvent(
					events.WebhookSessionEnded,
					session,
					"",
					"",
					fmt.Sprintf("Session %s ended", session),
					map[string]string{
						"project_dir": manifest.ProjectDir,
					},
				))
				monitor.Stop()
				func() {
					defer func() {
						if r := recover(); r != nil {
							fmt.Fprintf(os.Stderr, "Panic in session summary generation: %v\n", r)
						}
					}()
					generateEndSessionSummary(session, lastOutputs, manifest)
				}()
				_ = resilience.DeleteManifest(session)
				return nil
			}
			// Session found — reset miss counter
			if missCount > 0 {
				fmt.Printf("Session '%s' recovered after %d miss(es)\n", session, missCount)
				missCount = 0
			}
		case <-snapshotTicker.C:
			captureSessionOutputs(session, lastOutputs)
		}
	}
}

// orphanProcessSnapshot is the CLI session-observation loop's own record of
// manifest-owned pane-shell descendant identities, captured while tmux is
// still live. It is the only safe source of orphan-reap candidates: by the
// time a session is confirmed dead, survivors have already reparented and
// there is nothing left to walk from the (now-gone) pane shell PID.
//
// Generation is intentionally not set by captureOrphanProcessSnapshot — it
// is assigned only when a caller commits a successful replacement over the
// loop's retained state, so a capture helper can be tested in isolation
// from the loop's generation bookkeeping.
type orphanProcessSnapshot struct {
	Valid      bool
	Generation int
	CapturedAt time.Time
	Roots      []int
	Candidates map[process.ProcessIdentity]struct{}
}

// orphanSnapshotDeps carries the process-table operations
// captureOrphanProcessSnapshot needs, so tests can supply deterministic
// fakes instead of depending on the real OS process table.
type orphanSnapshotDeps struct {
	childPIDs       func(ctx context.Context, parentPID, limit int) ([]int, error)
	captureIdentity func(ctx context.Context, pid int) (process.ProcessIdentity, error)
}

// productionOrphanSnapshotDeps wires orphanSnapshotDeps to the real
// internal/process package.
func productionOrphanSnapshotDeps() orphanSnapshotDeps {
	return orphanSnapshotDeps{
		childPIDs:       process.GetChildPIDsContext,
		captureIdentity: process.CaptureProcessIdentity,
	}
}

// manifestOwnedPaneRoots returns the deduplicated, nonzero pane-shell PIDs
// of every pane whose ID matches a manifest-owned agent pane. User panes,
// foreign panes, supervisors, the archiver, the monitor itself, and any
// pane with an unresolved (zero) PID are never traversal roots.
func manifestOwnedPaneRoots(manifest *resilience.SpawnManifest, panes []tmux.Pane) []int {
	owned := make(map[string]struct{}, len(manifest.Agents))
	for _, agent := range manifest.Agents {
		owned[agent.PaneID] = struct{}{}
	}

	seen := make(map[int]struct{})
	var roots []int
	for _, p := range panes {
		if p.PID <= 0 {
			continue
		}
		if _, ok := owned[p.ID]; !ok {
			continue
		}
		if _, ok := seen[p.PID]; ok {
			continue
		}
		seen[p.PID] = struct{}{}
		roots = append(roots, p.PID)
	}
	return roots
}

// captureOrphanProcessSnapshot builds a fresh orphan-candidate snapshot from
// the manifest-owned pane-shell roots present in panes. It walks each
// root's descendant subtree — bounded by orphanReapMaxDepth/orphanReapFanout,
// the same bounds the existing direct-kill reap path uses — and captures the
// identity of every descendant found. Pane-shell roots are excluded from the
// candidate set (tmux reaps them directly), as is any orphanReapExcluded
// PID.
//
// A descendant that has already exited by the time its identity is captured
// is simply omitted — an expected race, not a failure. Any other
// enumeration or identity-lookup error aborts the whole capture: the
// snapshot is assembled entirely locally and only returned on full success,
// so a caller never commits a partial replacement over a good prior
// snapshot.
func captureOrphanProcessSnapshot(ctx context.Context, manifest *resilience.SpawnManifest, panes []tmux.Pane, deps orphanSnapshotDeps) (orphanProcessSnapshot, error) {
	roots := manifestOwnedPaneRoots(manifest, panes)

	seenPID := make(map[int]struct{})
	candidates := make(map[process.ProcessIdentity]struct{})

	var walk func(pid, depth int) error
	walk = func(pid, depth int) error {
		if depth > orphanReapMaxDepth {
			return nil
		}
		children, err := deps.childPIDs(ctx, pid, orphanReapFanout)
		if err != nil {
			return fmt.Errorf("enumerate children of pid %d: %w", pid, err)
		}
		for _, child := range children {
			if orphanReapExcluded(child) {
				continue
			}
			if _, ok := seenPID[child]; ok {
				continue
			}
			seenPID[child] = struct{}{}

			identity, err := deps.captureIdentity(ctx, child)
			switch {
			case errors.Is(err, process.ErrProcessNotRunning):
				// Vanished between enumeration and identity capture.
			case err != nil:
				return fmt.Errorf("capture identity of pid %d: %w", child, err)
			default:
				candidates[identity] = struct{}{}
			}

			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	for _, root := range roots {
		if err := walk(root, 1); err != nil {
			return orphanProcessSnapshot{}, err
		}
	}

	return orphanProcessSnapshot{
		Valid:      true,
		CapturedAt: time.Now(),
		Roots:      roots,
		Candidates: candidates,
	}, nil
}

func shouldSuperviseAgentMailDaemon() bool {
	if cfg == nil || !cfg.AgentMail.Enabled {
		return false
	}
	return cfg.AgentMail.SupervisorEnabledOrDefault()
}

func detectSessionTerminationCause(session string) string {
	output, err := tmux.DefaultClient.Run("list-sessions", "-F", "#{session_name}")
	if err != nil {
		errMsg := err.Error()
		switch {
		case strings.Contains(errMsg, "no server running"),
			strings.Contains(errMsg, "error connecting to"),
			strings.Contains(errMsg, "No such file or directory"):
			return "tmux server not running"
		case strings.Contains(errMsg, "no sessions"):
			return "tmux reports no sessions"
		default:
			return fmt.Sprintf("tmux error: %s", errMsg)
		}
	}

	if strings.TrimSpace(output) == "" {
		return "no tmux sessions found"
	}

	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == session {
			return "session still exists (race)"
		}
	}

	return "session not found in tmux list"
}

func captureSessionOutputs(session string, lastOutputs map[string]string) {
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return
	}
	for _, p := range panes {
		// Only capture known agent types or user panes if we want their input too
		// For now, capture all valid panes
		if p.Type == "" || p.Type == "unknown" {
			continue
		}
		// Capture reasonable amount of context
		out, err := tmux.CapturePaneOutput(p.ID, 1000)
		if err == nil {
			lastOutputs[p.ID] = out
		}
	}
}

func generateEndSessionSummary(session string, lastOutputs map[string]string, manifest *resilience.SpawnManifest) {
	if len(lastOutputs) == 0 {
		return
	}

	var outputs []summary.AgentOutput
	for _, agent := range manifest.Agents {
		if out, ok := lastOutputs[agent.PaneID]; ok {
			outputs = append(outputs, summary.AgentOutput{
				AgentID:   agent.PaneID,
				AgentType: agent.Type,
				Output:    out,
			})
		}
	}

	if len(outputs) == 0 {
		return
	}

	opts := summary.Options{
		Session:        session,
		Outputs:        outputs,
		Format:         summary.FormatHandoff, // Handoff format is good for end of session
		ProjectKey:     manifest.ProjectDir,
		ProjectDir:     manifest.ProjectDir,
		IncludeGitDiff: true,
	}

	s, err := summary.SummarizeSession(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to generate session summary: %v\n", err)
		return
	}

	// Store summary
	summaryDir := filepath.Join(manifest.ProjectDir, ".ntm", "summaries")
	if err := os.MkdirAll(summaryDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create summary dir: %v\n", err)
		return
	}

	timestamp := time.Now().Format("20060102-150405")
	filename := filepath.Join(summaryDir, fmt.Sprintf("%s-%s.json", session, timestamp))
	file, err := os.Create(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create summary file: %v\n", err)
		return
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(s); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write summary file: %v\n", err)
	} else {
		fmt.Printf("Session summary saved to %s\n", filename)
	}
}
