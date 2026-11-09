package tmux

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"pgregory.net/rapid"
)

func TestDetectAgentFromCommand_FalsePositives(t *testing.T) {
	tests := []struct {
		command  string
		expected agent.AgentType
	}{
		{"cursor", agent.AgentTypeCursor},
		{"/usr/bin/cursor", agent.AgentTypeCursor},
		{"cursor run", agent.AgentTypeCursor}, // If pane_current_command includes args (rare)

		// Potential false positives with simple Contains
		{"my-cursor-script.sh", agent.AgentTypeUser},
		{"vim cursor.c", agent.AgentTypeUser}, // If tmux reports "vim cursor.c"
		{"libncurses", agent.AgentTypeUser},   // "curses" contains "curs"? No, but "cursor" contains "curs"
		{"recursor", agent.AgentTypeUser},

		{"windsurf", agent.AgentTypeWindsurf},
		{"/opt/windsurf/bin/windsurf", agent.AgentTypeWindsurf},
		{"tailwindsurfing", agent.AgentTypeUser}, // False positive candidate
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := detectAgentFromCommand(tt.command)
			if got != tt.expected {
				t.Errorf("detectAgentFromCommand(%q) = %v, want %v", tt.command, got, tt.expected)
			}
		})
	}
}

// fakeTree stands in for detectAgentFromProcessTree, finding found under any pid.
func fakeTree(found agent.AgentType) func(pid, depth int) agent.AgentType {
	return func(pid, depth int) agent.AgentType {
		if pid <= 0 {
			return agent.AgentTypeUser
		}
		return found
	}
}

// Loop 5, Behavior 6: the running agent comes from the pane's command and
// process tree, never from its title, which outlives the agent.
func TestPaneAgentProcess(t *testing.T) {
	tests := []struct {
		name    string
		command string
		pid     int
		tree    agent.AgentType
		want    agent.AgentType
	}{
		{"claude command", "claude", 10, agent.AgentTypeUser, agent.AgentTypeClaudeCode},
		{"codex command", "codex", 10, agent.AgentTypeUser, agent.AgentTypeCodex},
		{"bare shell", "bash", 10, agent.AgentTypeUser, agent.AgentTypeUser},
		{"node wrapper over codex", "node", 10, agent.AgentTypeCodex, agent.AgentTypeCodex},
		{"shell with a claude child", "bash", 10, agent.AgentTypeClaudeCode, agent.AgentTypeClaudeCode},
		{"empty command", "", 10, agent.AgentTypeClaudeCode, agent.AgentTypeUser},
		{"wrapper with no pid", "bash", 0, agent.AgentTypeClaudeCode, agent.AgentTypeUser},
		{"other program", "vim", 10, agent.AgentTypeClaudeCode, agent.AgentTypeUser},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pane := Pane{Title: "proj__cc_1", Type: agent.AgentTypeClaudeCode, Command: tt.command, PID: tt.pid}
			if got := paneAgentProcessWith(pane, fakeTree(tt.tree)); got != tt.want {
				t.Fatalf("paneAgentProcessWith(%q, tree %v) = %v, want %v", tt.command, tt.tree, got, tt.want)
			}
		})
	}
}

// Behavior 6 property: changing a pane's title or type never changes the result.
func TestPaneAgentProcess_IgnoresTitleProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		pane := Pane{
			Command: rapid.SampledFrom([]string{"claude", "codex", "bash", "node", "zsh", "vim", ""}).Draw(rt, "command"),
			PID:     rapid.IntRange(0, 3).Draw(rt, "pid"),
		}
		tree := fakeTree(rapid.SampledFrom([]agent.AgentType{agent.AgentTypeUser, agent.AgentTypeClaudeCode, agent.AgentTypeCodex}).Draw(rt, "tree"))
		a, b := pane, pane
		a.Title, a.Type = rapid.String().Draw(rt, "title a"), agent.AgentTypeClaudeCode
		b.Title, b.Type = rapid.String().Draw(rt, "title b"), agent.AgentTypeCodex
		if paneAgentProcessWith(a, tree) != paneAgentProcessWith(b, tree) {
			rt.Fatalf("title or type changed the result for %+v", pane)
		}
	})
}
