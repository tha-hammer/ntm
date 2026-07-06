package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func newRespawnCmd() *cobra.Command {
	var force bool
	var panes string
	var agentType string
	var all bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:     "respawn <session>",
		Aliases: []string{"restart"},
		Short:   "Kill and restart worker agents in a session",
		Long: `Kill and restart worker agents in a tmux session.

This command uses tmux's respawn-pane -k to kill each selected pane's
process and restore a fresh shell, then relaunches the pane's agent CLI
(agent CLIs are started by keystroke after spawn, so a bare respawn
would otherwise leave an empty shell) and waits for it to become ready.

By default, only agent panes are restarted (not the user pane at index 0).
Use --all to include all panes, or --panes to target specific indices.

Examples:
  ntm respawn myproject              # Restart all agent panes (prompts for confirmation)
  ntm respawn myproject --force      # No confirmation
  ntm respawn myproject --panes=1,2  # Restart only panes 1 and 2
  ntm respawn myproject --type=cc    # Restart only Claude agents
  ntm respawn myproject --all        # Include user pane (index 0)
  ntm respawn myproject --dry-run    # Preview which panes would be restarted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRespawn(args[0], force, panes, agentType, all, dryRun)
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "skip confirmation")
	cmd.Flags().StringVarP(&panes, "panes", "p", "", "comma-separated pane indices to restart (e.g., 1,2,3)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "", "filter by agent type (cc, claude, cod, codex, gmi, gemini, agy, antigravity)")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include all panes (including user pane)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview which panes would be restarted")

	return cmd
}

func runRespawn(session string, force bool, panesFlag string, agentType string, all bool, dryRun bool) error {
	if err := tmux.EnsureInstalled(); err != nil {
		return err
	}

	res, err := ResolveSession(session, nil)
	if err != nil {
		return err
	}
	if res.Session == "" {
		return fmt.Errorf("session is required")
	}
	session = res.Session

	if !tmux.SessionExists(session) {
		return fmt.Errorf("session '%s' not found", session)
	}

	// Parse pane filter
	var paneFilter []string
	if panesFlag != "" {
		paneFilter = strings.Split(panesFlag, ",")
		for i := range paneFilter {
			paneFilter[i] = strings.TrimSpace(paneFilter[i])
		}
	}

	// Get panes to determine targets
	panes, err := tmux.GetPanes(session)
	if err != nil {
		return fmt.Errorf("failed to get panes: %w", err)
	}

	// Build filter map
	paneFilterMap := make(map[string]bool)
	for _, p := range paneFilter {
		paneFilterMap[p] = true
	}
	targetPanes := selectRespawnTargets(panes, paneFilterMap, agentType, all)

	if len(targetPanes) == 0 {
		fmt.Println("No panes matched the filter criteria.")
		return nil
	}

	// Dry-run mode
	if dryRun {
		fmt.Printf("Would restart %d pane(s) in session '%s':\n", len(targetPanes), session)
		for _, pane := range targetPanes {
			agentType := respawnPaneAgentType(pane)
			fmt.Printf("  - Pane %d: %s (%s)\n", pane.Index, pane.ID, agentType)
		}
		return nil
	}

	// Confirmation
	if !force {
		title := fmt.Sprintf("Restart %d pane(s)?", len(targetPanes))
		desc := fmt.Sprintf("Agents in session '%s' will be killed and relaunched.", session)
		if !confirmHuh(title, desc) {
			fmt.Println("Aborted.")
			return nil
		}
	}

	// Restart targets via the shared robot engine, which relaunches agent
	// CLIs after the respawn and ready-gates them (#187) — respawn-pane -k
	// alone only restores the pane's default command (the login shell).
	fmt.Printf("Restarting %d pane(s) (relaunching agent CLIs)...\n", len(targetPanes))
	out, err := robot.GetRestartPane(robot.RestartPaneOptions{
		Session: session,
		Panes:   paneFilter,
		Type:    agentType,
		All:     all,
	})
	if err != nil {
		return err
	}

	// Report results
	if len(out.Restarted) > 0 {
		fmt.Printf("Restarted panes: %s\n", strings.Join(out.Restarted, ", "))
		for _, paneKey := range out.Restarted {
			if relaunched, ok := out.AgentRelaunched[paneKey]; ok {
				status := "agent relaunched"
				if !relaunched {
					status = "agent relaunch FAILED (pane left at a shell)"
				}
				fmt.Printf("  - Pane %s: %s\n", paneKey, status)
			}
		}
	}
	if len(out.Failed) > 0 {
		fmt.Printf("Failed to restart:\n")
		for _, f := range out.Failed {
			fmt.Printf("  - %s: %s\n", f.Pane, f.Reason)
		}
		return fmt.Errorf("%d pane(s) failed to restart cleanly", len(out.Failed))
	}

	return nil
}

func selectRespawnTargets(panes []tmux.Pane, paneFilterMap map[string]bool, agentType string, all bool) []tmux.Pane {
	hasPaneFilter := len(paneFilterMap) > 0
	targetType := normalizeAgentType(agentType)

	var targetPanes []tmux.Pane
	for _, pane := range panes {
		paneKey := fmt.Sprintf("%d", pane.Index)

		if hasPaneFilter && !paneFilterMap[paneKey] && !paneFilterMap[pane.ID] {
			continue
		}

		currentType := respawnPaneAgentType(pane)
		if targetType != "" && targetType != currentType {
			continue
		}

		// By default only restart agent panes. Explicit pane filters and --all opt out.
		if !all && !hasPaneFilter && targetType == "" {
			if pane.Index == 0 && currentType == "unknown" {
				continue
			}
			if currentType == "user" {
				continue
			}
		}

		targetPanes = append(targetPanes, pane)
	}

	return targetPanes
}

func respawnPaneAgentType(pane tmux.Pane) string {
	if resolved := normalizeAgentType(string(pane.Type)); resolved != "" && resolved != "unknown" {
		return resolved
	}
	return normalizeAgentType(robot.DetectAgentType(pane.Title))
}

// normalizeAgentType normalizes agent type aliases to canonical form.
func normalizeAgentType(t string) string {
	return robot.ResolveAgentType(t)
}
