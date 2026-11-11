//go:build e2e

package e2e

// Loop 9 (release journey): the attention feed message bus end to end, with
// the real ntm binary, the detached internal-monitor that ntm spawn starts,
// real tmux, a shell running the hook commands spawn wrote, and a fake Agent
// Mail. See thoughts/searchable/shared/plans/2026-11-06-attention-feed-message-bus/09-release-journey-tdd.md.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// claudeStopInput is what Claude Code sends a Stop hook on stdin.
const claudeStopInput = `{"session_id":"e2e-journey","hook_event_name":"Stop","stop_hook_active":false}`

// codexStopInput is a real Codex Stop input, saved by Loop 7.
func codexStopInput(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	repo, err := findRepoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "internal", "cli", "testdata", "codex_stop_input.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Behavior 1: a message reaches the agent at turn end, through the Stop
// command ntm spawn wrote, for Claude and for Codex. Another process then
// reads the event once.
func TestE2E_MessageToHookOutput(t *testing.T) {
	for _, tool := range []struct {
		name, agentType, hookFile string
		input                     func(*testing.T) string
	}{
		{"claude", "cc", ".claude/settings.local.json", func(*testing.T) string { return claudeStopInput }},
		{"codex", "cod", ".codex/hooks.json", codexStopInput},
	} {
		t.Run(tool.name, func(t *testing.T) {
			env := newJourneyEnv(t, "")
			_, agent, dir := env.spawnAgent(t, "hook-"+tool.name, tool.agentType)
			env.mail.addMessage(agent, 101, "RedStone", "journey hook")
			command := ntmStopCommand(t, filepath.Join(dir, tool.hookFile))
			input := tool.input(t)

			start := time.Now()
			requireBlock(t, env.shell(t, dir, command, input, "AGENT_NAME="+agent), "[#101] from RedStone: journey hook")
			first := time.Since(start)
			t.Logf("%s hook, first (cold) run with mail: %s", tool.name, first.Round(time.Millisecond))
			if first > hookSpeedLimit {
				t.Fatalf("first hook run took %s, want under %s", first, hookSpeedLimit)
			}
			if again := env.shell(t, dir, command, input, "AGENT_NAME="+agent); again != "" {
				t.Fatalf("second turn end printed %q", again)
			}
			env.requireEventsOnce(t, agent, 101)
			timeQuietHookRuns(t, env, tool.name, dir, command, input, agent)
		})
	}
}

// hookSpeedLimit is SCOPE.md section 6's hook speed target with no new mail.
const hookSpeedLimit = time.Second

// timeQuietHookRuns runs the hook 20 times with no new mail. Each must print
// nothing and finish under hookSpeedLimit. It logs the median and maximum.
func timeQuietHookRuns(t *testing.T, env *journeyEnv, name, dir, command, input, agent string) {
	t.Helper()
	var took []time.Duration
	for i := 1; i <= 20; i++ {
		start := time.Now()
		if out := env.shell(t, dir, command, input, "AGENT_NAME="+agent); out != "" {
			t.Fatalf("quiet hook run %d printed %q", i, out)
		}
		took = append(took, time.Since(start))
		if took[i-1] > hookSpeedLimit {
			t.Fatalf("hook run %d took %s, want under %s", i, took[i-1], hookSpeedLimit)
		}
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	t.Logf("%s hook, 20 runs with no new mail: median %s, max %s", name, took[9].Round(time.Millisecond), took[19].Round(time.Millisecond))
}

// Behavior 4: with Agent Mail down, the hook returns at once and silently,
// the monitor keeps running and types nothing, and once Agent Mail is back
// the waiting message is announced once.
func TestE2E_AgentMailOutage(t *testing.T) {
	env := newJourneyEnv(t, "")
	pid, agent, dir := env.spawnAgent(t, "outage", "cc")
	baseline := env.agents.waitTyped(t, pid, spawnFirstPrompt, 15*time.Second)
	command := ntmStopCommand(t, filepath.Join(dir, ".claude", "settings.local.json"))

	env.mail.stop()
	env.mail.addMessage(agent, 301, "RedStone", "after outage")
	start := time.Now()
	if out := env.shell(t, dir, command, claudeStopInput, "AGENT_NAME="+agent); out != "" {
		t.Fatalf("hook during the outage printed %q", out)
	}
	took := time.Since(start)
	t.Logf("hook during the outage: %s", took.Round(time.Millisecond))
	if took > outageHookLimit {
		t.Fatalf("hook during the outage took %s, want under %s", took, outageHookLimit)
	}
	requireUnchanged(t, env, pid, baseline, 3*time.Second)
	if monitors := env.monitors(); len(monitors) != 1 {
		t.Fatalf("%d monitors running during the outage, want 1", len(monitors))
	}
	if ids := env.recipientEvents(t, agent); len(ids) != 0 {
		t.Fatalf("events recorded while Agent Mail was down: %v", ids)
	}

	env.mail.restart(t)
	env.agents.firstTyped(t, pid, "after outage", 15*time.Second)
	typed := env.agents.waitTyped(t, pid, "after outage", time.Second)
	if strings.Count(typed, "after outage") != 1 {
		t.Fatalf("message announced more than once: %q", typed)
	}
	if out := env.shell(t, dir, command, claudeStopInput, "AGENT_NAME="+agent); out != "" {
		t.Fatalf("hook after the nudge printed %q", out)
	}
	env.requireEventsOnce(t, agent, 301)
}

// outageHookLimit is how long a turn end may take while Agent Mail is down:
// it must not hold the agent up.
const outageHookLimit = 5 * time.Second

// Behavior 5 (SCOPE.md hook file safety): spawning 3 times in a folder whose
// hook files hold other entries leaves one ntm entry in each, and the others
// unchanged.
func TestE2E_RespawnKeepsHookFiles(t *testing.T) {
	const (
		claudeKept = "echo kept"
		codexKept  = "echo codex-kept"
	)
	env := newJourneyEnv(t, "")
	dir := filepath.Join(env.projects, "safe")
	claudeFile := filepath.Join(dir, ".claude", "settings.local.json")
	codexFile := filepath.Join(dir, ".codex", "hooks.json")
	for path, content := range map[string]string{
		claudeFile: `{"model": "opus", "hooks": {"PostToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "` + claudeKept + `"}]}]}}`,
		codexFile:  `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "` + codexKept + `"}]}]}}`,
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}

	for run := 1; run <= 3; run++ {
		before := len(env.agents.started())
		env.ntm(t, dir, "spawn", "safe", "--cc=1", "--cod=1")
		env.agents.waitStarted(t, before+2, 30*time.Second)
		env.killMonitors(t)
		env.killSession(t, "safe")
	}

	ntmStopCommand(t, claudeFile) // exactly one ntm entry
	ntmStopCommand(t, codexFile)
	var claude struct {
		Model string `json:"model"`
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	data, _ := os.ReadFile(claudeFile)
	if err := json.Unmarshal(data, &claude); err != nil {
		t.Fatal(err)
	}
	post := claude.Hooks["PostToolUse"]
	if claude.Model != "opus" || len(post) != 1 || post[0].Matcher != "Bash" || len(post[0].Hooks) != 1 || post[0].Hooks[0].Command != claudeKept {
		t.Fatalf("other Claude settings changed:\n%s", data)
	}
	kept := 0
	for _, command := range stopCommands(t, codexFile) {
		if command == codexKept {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("Codex's other Stop entry appears %d times", kept)
	}
	exclude, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
	seen := map[string]bool{}
	for _, line := range strings.Split(string(exclude), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if seen[line] {
			t.Fatalf(".git/info/exclude lists %q twice:\n%s", line, exclude)
		}
		seen[line] = true
	}
	t.Logf(".git/info/exclude entries: %v", seen)
}

// Behaviors 2 and 3: an idle agent is nudged once by the monitor ntm spawn
// started; a snoozed message waits for its time (Loop 6); and after the
// monitor and session are killed and spawned again in the same folder, every
// event is still there once and nothing is announced again.
func TestE2E_MessageToIdleNudge(t *testing.T) {
	env := newJourneyEnv(t, "")
	pid, agent, dir := env.spawnAgent(t, "nudge", "cc")
	if monitors := env.monitors(); len(monitors) != 1 {
		t.Fatalf("ntm spawn started %d internal-monitor processes, want 1", len(monitors))
	}

	env.mail.addMessage(agent, 201, "RedStone", "while idle")
	delay := env.agents.firstTyped(t, pid, "while idle", 15*time.Second)
	t.Logf("idle reminder delay: %s (1s poll)", delay.Round(time.Millisecond))
	if delay > idleReminderLimit {
		t.Fatalf("idle reminder took %s, want under %s (2 polls plus 2s)", delay, idleReminderLimit)
	}
	typed := env.agents.waitTyped(t, pid, "while idle", time.Second)
	if !strings.Contains(typed, "fetch_inbox") {
		t.Fatalf("reminder doesn't say how to read the mail: %q", typed)
	}
	requireUnchanged(t, env, pid, typed, 5*time.Second)

	// Snooze (Loop 6). The busy screen holds the monitor off while the
	// message arrives and is snoozed.
	env.agents.setScreen(t, pid, "cc_working.txt")
	env.mail.addMessage(agent, 202, "RedStone", "later")
	env.ntmAs(t, agent, dir, "mail", "snooze", "202", "--until", "4s", "--project", dir)
	snoozedAt := time.Now()
	env.agents.setScreen(t, pid, "cc_idle.txt")
	requireUnchanged(t, env, pid, typed, 2*time.Second)
	env.agents.firstTyped(t, pid, "later", 15*time.Second)
	if since := time.Since(snoozedAt); since < 3*time.Second {
		t.Fatalf("snoozed message nudged after %s, before its 4s snooze ended", since)
	}
	typed = env.agents.waitTyped(t, pid, "later", time.Second)
	if strings.Count(typed, "while idle") != 1 {
		t.Fatalf("an announced message came back with the snoozed one: %q", typed)
	}
	requireUnchanged(t, env, pid, typed, 3*time.Second)

	// One more message, told by the hook at turn end.
	env.mail.addMessage(agent, 203, "RedStone", "at turn end")
	command := ntmStopCommand(t, filepath.Join(dir, ".claude", "settings.local.json"))
	requireBlock(t, env.shell(t, dir, command, claudeStopInput, "AGENT_NAME="+agent), "[#203] from RedStone: at turn end")
	env.requireEventsOnce(t, agent, 201, 202, 203)

	// Behavior 3: restart.
	if killed := env.killMonitors(t); killed != 1 {
		t.Fatalf("killed %d monitors, want 1", killed)
	}
	env.killSession(t, "nudge")
	newPid, newAgent, _ := env.spawnAgent(t, "nudge", "cc")
	if newAgent != agent {
		t.Fatalf("the respawned agent is %q, was %q", newAgent, agent)
	}
	if monitors := env.monitors(); len(monitors) != 1 {
		t.Fatalf("respawn started %d internal-monitor processes, want 1", len(monitors))
	}
	env.requireEventsOnce(t, agent, 201, 202, 203)
	if out := env.shell(t, dir, command, claudeStopInput, "AGENT_NAME="+agent); out != "" {
		t.Fatalf("hook after restart printed %q", out)
	}
	// Spawn types its first prompt; after that, nothing is typed.
	firstPrompt := env.agents.waitTyped(t, newPid, spawnFirstPrompt, 15*time.Second)
	requireUnchanged(t, env, newPid, firstPrompt, 5*time.Second)
}

// spawnFirstPrompt is part of the prompt ntm spawn types into a new agent.
const spawnFirstPrompt = "register with agent mail"

// idleReminderLimit is SCOPE.md section 6's idle reminder delay (one poll
// after the agent is idle) at the tests' 1 second poll: 2 polls plus 2
// seconds for the monitor's first pass and the typing.
const idleReminderLimit = 4 * time.Second

// requireUnchanged checks the agent's typed text stays want for the duration.
func requireUnchanged(t *testing.T, env *journeyEnv, pid, want string, duration time.Duration) {
	t.Helper()
	for end := time.Now().Add(duration); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if got := env.agents.typed(t, pid); got != want {
			t.Fatalf("stand-in %s was typed into again:\nnow %q\nwas %q", pid, got, want)
		}
	}
}
