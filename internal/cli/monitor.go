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

	// Poll for session existence and refresh the orphan-candidate process
	// snapshot via the extracted, independently-testable session-monitor
	// loop. runMonitor stays the production assembler: it owns signals,
	// supervisor daemons, plugins, webhook bridge, resilience monitoring,
	// and archiving, and wires the loop's confirmed-death effects below.
	// The signal path is deliberately kept outside the loop and never
	// reaps or deletes a potentially still-live session manifest.
	lastOutputs := make(map[string]string)
	loopOptions := monitorLoopOptions{
		PollInterval:           monitorDefaultPollInterval,
		OutputSnapshotInterval: monitorDefaultOutputSnapshotInterval,
		MaxMisses:              monitorDefaultMaxMisses,
		ReapGrace:              orphanReapGrace,
	}
	loopDeps := monitorLoopDependencies{
		Observe: tmux.GetPanesContext,
		CaptureOutput: func(session string) {
			captureSessionOutputs(session, lastOutputs)
		},
		SnapshotDeps: productionOrphanSnapshotDeps(),
		Ready:        func(orphanProcessSnapshot) {},
		OnConfirmedDeath: func(dctx context.Context, snap orphanProcessSnapshot) {
			deathDeps := productionConfirmedDeathDeps(session, manifest, monitor, loopOptions.ReapGrace, lastOutputs)
			// TODO(bd-vc07s / Behavior 1): read manifest.ReapOrphansOnExit
			// once Behavior 1 lands the config+manifest policy surface.
			// Hardcoded false is the fail-safe default until then — never
			// reap by default before the policy toggle actually exists.
			if err := handleConfirmedSessionDeath(dctx, false, snap, deathDeps); err != nil {
				fmt.Fprintf(os.Stderr, "confirmed-death handler error: %v\n", err)
			}
		},
	}

	fmt.Printf("Monitoring session '%s' for resilience...\n", session)

	loopDone := make(chan error, 1)
	go func() {
		loopDone <- runSessionMonitorLoop(ctx, manifest, loopOptions, loopDeps)
	}()

	select {
	case <-sigChan:
		fmt.Println("Monitor stopping...")
		cancel()
		<-loopDone
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
	case err := <-loopDone:
		if err != nil {
			fmt.Fprintf(os.Stderr, "Monitor loop error: %v\n", err)
		}
		return nil
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

// Production defaults for the session-monitor loop's cadence.
const (
	monitorDefaultPollInterval           = 5 * time.Second
	monitorDefaultOutputSnapshotInterval = 30 * time.Second
	monitorDefaultMaxMisses              = 5 // ~25s at the default poll interval before confirmed death
)

// monitorLoopOptions bounds the session-observation loop's timing. Every
// production caller uses the monitorDefault* constants; tests use short
// named values instead of scattering duration literals.
type monitorLoopOptions struct {
	PollInterval           time.Duration
	OutputSnapshotInterval time.Duration
	MaxMisses              int
	ReapGrace              time.Duration
}

// validate reports whether options are safe to build tickers and invoke
// dependencies from. It is checked before any ticker or callback exists.
func (o monitorLoopOptions) validate() error {
	if o.PollInterval <= 0 {
		return fmt.Errorf("monitor loop: poll interval must be positive, got %v", o.PollInterval)
	}
	if o.OutputSnapshotInterval <= 0 {
		return fmt.Errorf("monitor loop: output snapshot interval must be positive, got %v", o.OutputSnapshotInterval)
	}
	if o.MaxMisses < 1 {
		return fmt.Errorf("monitor loop: max misses must be at least 1, got %d", o.MaxMisses)
	}
	return nil
}

// monitorLoopDependencies carries every external operation
// runSessionMonitorLoop needs: live observation, output capture, the
// process-snapshot capture dependencies, the one-shot readiness callback,
// and the confirmed-death callback. Production wiring lives in runMonitor;
// tests supply deterministic fakes so the loop can be driven without a real
// tmux session or OS process table.
type monitorLoopDependencies struct {
	// Observe fetches live panes for the session, or an error to classify
	// via tmux.ClassifyCommandError.
	Observe func(ctx context.Context, session string) ([]tmux.Pane, error)

	// CaptureOutput captures per-pane output on the separate
	// output-snapshot cadence — independent of process-snapshot refresh
	// even though both share the loop's select statement. Production
	// supplies a closure over its own retained output map.
	CaptureOutput func(session string)

	// SnapshotDeps supplies the process-table operations
	// captureOrphanProcessSnapshot needs to refresh the process snapshot on
	// every successful, usable live poll.
	SnapshotDeps orphanSnapshotDeps

	// Ready is invoked exactly once, after the first usable live capture,
	// with a defensive copy of the current snapshot. Production supplies a
	// no-op; tests wait on it instead of sleeping.
	Ready func(snap orphanProcessSnapshot)

	// OnConfirmedDeath is invoked exactly once, with the retained snapshot,
	// when the miss streak reaches options.MaxMisses. The loop returns
	// immediately afterward without further mutation.
	OnConfirmedDeath func(ctx context.Context, snap orphanProcessSnapshot)

	// PollTicks and OutputTicks let tests drive ticks through an explicit
	// channel instead of waiting on real timers. When nil (the production
	// default), runSessionMonitorLoop builds real time.Tickers from
	// options — only after validation, never unconditionally.
	PollTicks   <-chan time.Time
	OutputTicks <-chan time.Time
}

// monitorLoopState is the session-observation loop's retained state across
// ticks: the last-good process snapshot, the consecutive definite-missing
// streak, and whether readiness has already fired.
type monitorLoopState struct {
	snapshot   orphanProcessSnapshot
	missCount  int
	readyFired bool
}

// copyOrphanProcessSnapshot returns a defensive copy of snap: the Roots
// slice and Candidates map are independent backing storage, so a Ready
// recipient can never mutate the loop's own retained state.
func copyOrphanProcessSnapshot(snap orphanProcessSnapshot) orphanProcessSnapshot {
	cp := snap
	if snap.Roots != nil {
		cp.Roots = append([]int(nil), snap.Roots...)
	}
	if snap.Candidates != nil {
		cp.Candidates = make(map[process.ProcessIdentity]struct{}, len(snap.Candidates))
		for id := range snap.Candidates {
			cp.Candidates[id] = struct{}{}
		}
	}
	return cp
}

// applyMonitorObservation advances state given one poll's outcome, per the
// locked transition table:
//
//   - A definite-missing tmux error (session not found / no server)
//     advances the miss streak and retains the snapshot.
//   - Any other tmux error is ambiguous: it resets/breaks the miss streak —
//     an ambiguous failure must never accumulate toward destructive
//     confirmation — and retains the snapshot.
//   - A successful poll always resets the miss streak. If the pane list is
//     unusable (empty, or the manifest has agents but none resolve to an
//     owned pane), the snapshot is retained. A zero-agent manifest is
//     always usable, even with zero panes.
//   - A usable poll captures a fresh snapshot. A capture failure (any
//     enumeration or identity-lookup error beyond an expected vanished
//     child) retains the previous generation instead of committing a
//     partial replacement.
//   - A usable capture replaces the snapshot and advances its generation,
//     whether or not it produced any candidates. A live pane respawn under
//     the same PaneID falls under this same replacement, since it is
//     captured fresh from whatever PID currently backs that PaneID.
//
// It returns the (possibly unchanged) state and whether this observation
// produced a usable live capture, which the caller uses to decide whether
// to fire readiness.
func applyMonitorObservation(ctx context.Context, manifest *resilience.SpawnManifest, panes []tmux.Pane, obsErr error, state monitorLoopState, deps monitorLoopDependencies) (monitorLoopState, bool) {
	if obsErr != nil {
		class := tmux.ClassifyCommandError(obsErr)
		switch class.Kind {
		case tmux.CommandErrorSessionNotFound, tmux.CommandErrorNoServer:
			state.missCount++
		default:
			state.missCount = 0
		}
		return state, false
	}

	state.missCount = 0

	roots := manifestOwnedPaneRoots(manifest, panes)
	usable := len(manifest.Agents) == 0 || len(roots) > 0
	if !usable {
		return state, false
	}

	snap, err := captureOrphanProcessSnapshot(ctx, manifest, panes, deps.SnapshotDeps)
	if err != nil {
		return state, false
	}
	snap.Generation = state.snapshot.Generation + 1
	state.snapshot = snap
	return state, true
}

// pollMonitorLoopOnce performs one live observation and advances state
// through applyMonitorObservation. It is the loop's single seam for "what
// happens on one tick" — used for both the pre-readiness synchronous
// observation and every subsequent poll tick.
func pollMonitorLoopOnce(ctx context.Context, manifest *resilience.SpawnManifest, state monitorLoopState, deps monitorLoopDependencies) (monitorLoopState, bool) {
	panes, err := deps.Observe(ctx, manifest.Session)
	return applyMonitorObservation(ctx, manifest, panes, err, state, deps)
}

// runSessionMonitorLoop owns session observation, process-snapshot state
// transitions, readiness, and confirmed-death dispatch for one spawned
// session. runMonitor remains the production assembler for signals,
// supervisor daemons, plugins, webhook bridge, resilience monitoring,
// archiving, and summary state; this loop owns only what the locked
// transition table describes.
//
// It performs one synchronous observation before signaling readiness
// (rather than waiting out the first tick), then polls on
// options.PollInterval and refreshes the process snapshot on every
// successful, usable live poll. Output is captured separately on
// options.OutputSnapshotInterval. Context cancellation returns promptly
// without running confirmed-death effects.
func runSessionMonitorLoop(ctx context.Context, manifest *resilience.SpawnManifest, options monitorLoopOptions, deps monitorLoopDependencies) error {
	if err := options.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return nil
	}

	advance := func(state monitorLoopState) monitorLoopState {
		newState, usable := pollMonitorLoopOnce(ctx, manifest, state, deps)
		if usable && !newState.readyFired {
			deps.Ready(copyOrphanProcessSnapshot(newState.snapshot))
			newState.readyFired = true
		}
		return newState
	}

	state := advance(monitorLoopState{})
	if state.missCount >= options.MaxMisses {
		deps.OnConfirmedDeath(ctx, state.snapshot)
		return nil
	}

	pollTicks := deps.PollTicks
	if pollTicks == nil {
		pollTicker := time.NewTicker(options.PollInterval)
		defer pollTicker.Stop()
		pollTicks = pollTicker.C
	}
	outputTicks := deps.OutputTicks
	if outputTicks == nil {
		outputTicker := time.NewTicker(options.OutputSnapshotInterval)
		defer outputTicker.Stop()
		outputTicks = outputTicker.C
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pollTicks:
			state = advance(state)
			if state.missCount >= options.MaxMisses {
				deps.OnConfirmedDeath(ctx, state.snapshot)
				return nil
			}
		case <-outputTicks:
			deps.CaptureOutput(manifest.Session)
		}
	}
}

// confirmedDeathDeps carries the effects handleConfirmedSessionDeath
// sequences: emitting the ended event, stopping/joining resilience
// monitoring, the identity-safe reap and its structured log, attempting
// the session summary, and deleting the manifest. Production wiring is
// productionConfirmedDeathDeps; tests supply recording/failing fakes.
type confirmedDeathDeps struct {
	EmitEnded      func(ctx context.Context)
	StopResilience func()
	Reap           func(ctx context.Context, candidates []process.ProcessIdentity) orphanReapResult
	LogReapResult  func(enabled bool, snap orphanProcessSnapshot, result orphanReapResult)
	Summary        func()
	DeleteManifest func() error
}

// handleConfirmedSessionDeath sequences the confirmed-death effects in the
// locked, flat order: emit the ended event, synchronously stop/join
// resilience monitoring, then — only when enabled — identity-safely reap
// the retained snapshot's candidates and log the exact result. An enabled
// policy with a valid empty snapshot still reaps and logs a zero-count
// record, so disabled is observably distinct from "reaper received an
// empty list." It then attempts the session summary behind a panic-safe
// boundary and deletes the manifest regardless of what the summary did —
// deletion is never skipped or hidden by a caller's fallback cleanup.
func handleConfirmedSessionDeath(ctx context.Context, enabled bool, snap orphanProcessSnapshot, deps confirmedDeathDeps) error {
	deps.EmitEnded(ctx)
	deps.StopResilience()

	if enabled {
		candidates := make([]process.ProcessIdentity, 0, len(snap.Candidates))
		for id := range snap.Candidates {
			candidates = append(candidates, id)
		}
		result := deps.Reap(ctx, candidates)
		deps.LogReapResult(enabled, snap, result)
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in session summary generation: %v\n", r)
			}
		}()
		deps.Summary()
	}()

	return deps.DeleteManifest()
}

// productionConfirmedDeathDeps wires confirmedDeathDeps to runMonitor's
// real session state: the existing ended-event/webhook emission, the
// resilience monitor's Stop, the identity-safe reaper from send.go, a
// structured slog record (no raw PID lists), the existing summary
// generator, and manifest deletion.
func productionConfirmedDeathDeps(session string, manifest *resilience.SpawnManifest, monitor *resilience.Monitor, reapGrace time.Duration, lastOutputs map[string]string) confirmedDeathDeps {
	return confirmedDeathDeps{
		EmitEnded: func(context.Context) {
			cause := detectSessionTerminationCause(session)
			fmt.Printf("Session ended, stopping monitor (%s)\n", cause)
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
		},
		StopResilience: monitor.Stop,
		Reap: func(ctx context.Context, candidates []process.ProcessIdentity) orphanReapResult {
			return reapIdentifiedOrphanProcesses(ctx, candidates, reapGrace, productionOrphanReapDeps())
		},
		LogReapResult: func(enabled bool, snap orphanProcessSnapshot, result orphanReapResult) {
			slog.Default().Info("orphan reap",
				"session", session,
				"enabled", enabled,
				"generation", snap.Generation,
				"age", time.Since(snap.CapturedAt).String(),
				"captured", result.Captured,
				"matched_before_term", result.MatchedBeforeTERM,
				"term_signaled", result.TERMSignaled,
				"matched_before_kill", result.MatchedBeforeKILL,
				"kill_signaled", result.KILLSignaled,
				"skipped_stale", result.SkippedStale,
				"skipped_exited", result.SkippedExited,
				"skipped_excluded", result.SkippedExcluded,
				"lookup_errors", result.LookupErrors,
				"signal_errors", result.SignalErrors,
			)
		},
		Summary: func() {
			generateEndSessionSummary(session, lastOutputs, manifest)
		},
		DeleteManifest: func() error {
			return resilience.DeleteManifest(session)
		},
	}
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
