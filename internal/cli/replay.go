package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/ntm/internal/history"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func resolveReplaySession(entrySession, sessionOverride string) (string, error) {
	session := strings.TrimSpace(entrySession)
	if sessionOverride != "" {
		session = strings.TrimSpace(sessionOverride)
	}
	if session == "" {
		return "", fmt.Errorf("history entry session is empty")
	}
	if err := tmux.ValidateSessionName(session); err != nil {
		return "", fmt.Errorf("invalid session name: %w", err)
	}
	return session, nil
}

func newReplayCmd() *cobra.Command {
	var (
		targetCC, targetCod, targetGmi, targetAgy, targetAll bool
		sessionOverride                                      string
		edit                                                 bool
		dryRun                                               bool
		noHistory                                            bool
		last                                                 bool
	)

	cmd := &cobra.Command{
		Use:   "replay [index|id]",
		Short: "Replay a prompt from history",
		Long: `Replay a previously sent prompt from history.

Arguments:
  - Number (1-N): Index from most recent (1 = last prompt)
  - String: ID prefix to match

Examples:
  ntm replay 1                    # Replay most recent prompt
  ntm replay --last               # Same as above
  ntm replay 3                    # Replay 3rd most recent
  ntm replay 01HXYZ               # Replay by ID prefix
  ntm replay 1 --edit             # Edit prompt before sending
  ntm replay 1 --dry-run          # Preview without sending
  ntm replay 1 --session=other    # Send to different session
  ntm replay 1 --cc               # Send to Claude agents only`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Get entries from history
			entries, err := history.ReadAll()
			if err != nil {
				return fmt.Errorf("reading history: %w", err)
			}
			if len(entries) == 0 {
				return fmt.Errorf("no history entries found")
			}

			// Find the target entry
			var entry *history.HistoryEntry

			if last || len(args) == 0 {
				// Use most recent
				entry = &entries[len(entries)-1]
			} else {
				arg := args[0]

				// Try as index first
				if idx, err := strconv.Atoi(arg); err == nil && idx > 0 {
					// Index is 1-based from most recent
					if idx > len(entries) {
						return fmt.Errorf("index %d out of range (have %d entries)", idx, len(entries))
					}
					entry = &entries[len(entries)-idx]
				} else {
					// Try as ID prefix
					for i := len(entries) - 1; i >= 0; i-- {
						if strings.HasPrefix(entries[i].ID, arg) {
							entry = &entries[i]
							break
						}
					}
					if entry == nil {
						return fmt.Errorf("no entry found matching ID prefix %q", arg)
					}
				}
			}

			// Determine session to use
			session, err := resolveReplaySession(entry.Session, sessionOverride)
			if err != nil {
				return err
			}

			// Get the prompt, optionally edit it
			prompt := entry.Prompt
			if edit {
				edited, err := editPrompt(prompt)
				if err != nil {
					return fmt.Errorf("editing prompt: %w", err)
				}
				prompt = edited
			}

			// Show what will be sent
			fmt.Printf("Replaying prompt from %s\n", entry.Timestamp.Format("2006-01-02 15:04:05"))
			fmt.Printf("Session: %s\n", session)
			fmt.Printf("Prompt: %s\n", truncatePrompt(prompt, 80))

			if dryRun {
				fmt.Println("\n(dry-run mode - not sending)")
				return nil
			}

			// Confirm before sending
			if !confirm("Send this prompt?") {
				fmt.Println("Cancelled.")
				return nil
			}

			// Check session exists
			if !tmux.SessionExists(session) {
				return fmt.Errorf("session %q not found", session)
			}

			// Determine target panes
			panes, err := tmux.GetPanes(session)
			if err != nil {
				return fmt.Errorf("getting panes: %w", err)
			}

			// Filter panes based on flags
			var targets []tmux.Pane
			noFilter := !targetCC && !targetCod && !targetGmi && !targetAgy && !targetAll

			for _, p := range panes {
				if targetAll {
					targets = append(targets, p)
					continue
				}

				if noFilter {
					// Default: all agent panes (exclude user panes)
					if p.Type != tmux.AgentUser {
						targets = append(targets, p)
					}
					continue
				}

				// Apply specific filters
				if matchesLegacySendTypeFilter(p, targetCC, targetCod, targetGmi) ||
					(targetAgy && tmux.AgentType(p.Type).Canonical() == tmux.AgentAntigravity) {
					targets = append(targets, p)
				}
			}

			if len(targets) == 0 {
				return fmt.Errorf("no matching panes found")
			}

			// Send to all targets
			var targetNames []string
			var targetAgentTypes []string
			for _, p := range targets {
				if err := sendPromptToPane(session, p, prompt); err != nil {
					return fmt.Errorf("sending to pane %d: %w", p.Index, err)
				}
				targetNames = append(targetNames, fmt.Sprintf("%d", p.Index))
				targetAgentTypes = append(targetAgentTypes, p.Type.String())
			}

			// Log to history (unless disabled)
			if !noHistory {
				newEntry := history.NewEntry(session, targetNames, prompt, history.SourceReplay)
				newEntry.SetAgentTypes(targetAgentTypes)
				newEntry.SetSuccess()
				if err := history.Append(newEntry); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to log replay: %v\n", err)
				}
			}

			fmt.Printf("Sent to %d pane(s)\n", len(targets))
			return nil
		},
	}

	cmd.Flags().BoolVar(&last, "last", false, "replay most recent prompt")
	cmd.Flags().BoolVar(&targetCC, "cc", false, "send to Claude agents only")
	cmd.Flags().BoolVar(&targetCod, "cod", false, "send to Codex agents only")
	cmd.Flags().BoolVar(&targetGmi, "gmi", false, "send to Gemini agents only")
	cmd.Flags().BoolVar(&targetAgy, "agy", false, "send to Antigravity agents only")
	cmd.Flags().BoolVar(&targetAll, "all", false, "send to all panes")
	cmd.Flags().StringVar(&sessionOverride, "session", "", "override target session")
	cmd.Flags().BoolVar(&edit, "edit", false, "edit prompt before sending")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be sent without sending")
	cmd.Flags().BoolVar(&noHistory, "no-history", false, "don't log this replay to history")

	return cmd
}

// editPrompt opens the prompt in an editor and returns the modified content.
func editPrompt(original string) (string, error) {
	// Create temp file
	f, err := os.CreateTemp("", "ntm-prompt-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())

	// Write original content
	if _, err := f.WriteString(original); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	// Run editor
	cmd, err := buildEditorCommandWithFallback(f.Name(), "vim")
	if err != nil {
		return "", fmt.Errorf("configuring editor: %w", err)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor failed: %w", err)
	}

	// Read modified content
	modified, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(modified)), nil
}
