package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"pgregory.net/rapid"
)

// screenAgents are stand-in Claude and Codex agents for the idle nudge's
// closure tests. Each launched agent gets a folder named by its pid, where it:
//   - shows the file "screen" (a saved real capture) and redraws it whenever
//     the test asks with setScreen,
//   - appends every line typed into it to "typed",
//   - exits when the test calls quit, leaving the pane's shell behind.
//
// The executables are named claude and codex, so the live-agent check sees an
// agent process, as it would with the real ones.
type screenAgents struct {
	dir string
}

// screenAgentScript is the stand-in's body. $1 is the capture folder and $2
// the agent type.
const screenAgentScript = `#!/bin/bash
d="$1/$$"
mkdir -p "$d"
echo "$2" > "$d/type"
env > "$d/env"
touch "$d/started"
while :; do
	if [ -f "$d/quit" ]; then
		exit 0
	fi
	if [ -f "$d/redraw" ]; then
		rm -f "$d/redraw"
		clear
		cat "$d/screen"
	fi
	if IFS= read -r -t 0.2 line; then
		printf '%s\n' "$line" >> "$d/typed"
	fi
done
`

// screenAgentConfig writes claude and codex stand-ins into <root>/bin, and the
// helpers' config.toml with them as the agent commands, Agent Mail on at the
// fake server, a 1 second nudge poll, and a 2 second keystroke quiet period.
// agentMail holds more lines for the [agent_mail] table.
func (e *closureEnv) screenAgentConfig(t *testing.T, mail *fakeAgentMail, agentMail string) *screenAgents {
	t.Helper()
	root := filepath.Dir(e.workDir)
	agents := &screenAgents{dir: filepath.Join(root, "agents")}
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{agents.dir, bin, e.projectsBase()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commands := map[string]string{}
	for _, agentType := range []string{"claude", "codex"} {
		script := filepath.Join(bin, agentType)
		if err := os.WriteFile(script, []byte(screenAgentScript), 0o755); err != nil {
			t.Fatalf("write stand-in agent: %v", err)
		}
		commands[agentType] = shellQuoteForTest(script) + " " + shellQuoteForTest(agents.dir) + " " + agentType
	}
	e.useAgentMail(mail)
	e.writeConfig(t, fmt.Sprintf(`projects_base = %q

[agents]
claude = %q
codex = %q

[agent_mail]
enabled = true
idle_nudge_interval_seconds = 1
human_typing_quiet_seconds = 2
%s
[cass.context]
enabled = false

[recovery]
enabled = false
auto_inject_on_spawn = false
include_agent_mail = false
include_beads_context = false
include_cm_memories = false

[gemini_setup]
auto_select_pro_model = false

[spawn_pacing.headroom]
enabled = false

[integrations.dcg]
enabled = false

[integrations.rch]
enabled = false
`, e.projectsBase(), commands["claude"], commands["codex"], agentMail))
	return agents
}

func (e *closureEnv) configPath() string {
	return filepath.Join(e.configDir, "ntm", "config.toml")
}

// waitStarted waits for count stand-ins to start and returns their pid folders
// in start order.
func (a *screenAgents) waitStarted(t *testing.T, count int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		pids := a.started()
		if len(pids) >= count {
			return pids
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d stand-in agents started within %s, want %d", len(pids), within, count)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (a *screenAgents) started() []string {
	entries, _ := os.ReadDir(a.dir)
	type startedAgent struct {
		pid string
		at  time.Time
	}
	var found []startedAgent
	for _, entry := range entries {
		info, err := os.Stat(filepath.Join(a.dir, entry.Name(), "started"))
		if err == nil {
			found = append(found, startedAgent{entry.Name(), info.ModTime()})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at.Before(found[j].at) })
	pids := make([]string, 0, len(found))
	for _, agent := range found {
		pids = append(pids, agent.pid)
	}
	return pids
}

// setScreen shows a saved capture from internal/agent/testdata in the agent's pane.
func (a *screenAgents) setScreen(t *testing.T, pid, fixture string) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	content, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "agent", "testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	writeFile(t, filepath.Join(a.dir, pid, "screen"), string(content))
	writeFile(t, filepath.Join(a.dir, pid, "redraw"), "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(a.dir, pid, "redraw")); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stand-in %s didn't redraw within 5s", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (a *screenAgents) typed(t *testing.T, pid string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(a.dir, pid, "typed"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(content)
}

// waitTyped waits until the agent's typed file holds want and then stays the
// same for 2 seconds, which covers the double Enter that ends a send, and
// returns it.
func (a *screenAgents) waitTyped(t *testing.T, pid, want string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := a.typed(t, pid)
		if strings.Contains(got, want) {
			for settled := time.Now(); time.Since(settled) < 2*time.Second; time.Sleep(100 * time.Millisecond) {
				if now := a.typed(t, pid); now != got {
					got, settled = now, time.Now()
				}
			}
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("stand-in %s: no %q typed within %s (typed %q)", pid, want, within, got)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (a *screenAgents) quit(t *testing.T, pid string) {
	t.Helper()
	writeFile(t, filepath.Join(a.dir, pid, "quit"), "")
}

// agentEnv returns one variable from the stand-in's environment at launch.
func (a *screenAgents) agentEnv(t *testing.T, pid, key string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(a.dir, pid, "env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	t.Fatalf("stand-in %s has no %s", pid, key)
	return ""
}

// startMonitor runs `internal-monitor <session>` as a long-running helper, with
// PATH narrowed to the stand-ins and the system folders so the monitor starts
// no real daemons. Its output goes to monitor.log next to the work folder. The
// returned function stops it with SIGINT, as ntm's own signal path does.
func (e *closureEnv) startMonitor(t *testing.T, session string) (stop func()) {
	t.Helper()
	root := filepath.Dir(e.workDir)
	logFile, err := os.OpenFile(filepath.Join(root, session+"-monitor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, _ := e.helperCommand("execute", "", "internal-monitor", session)
	cmd.Env = append(cmd.Env, "PATH="+filepath.Join(root, "bin")+":/usr/bin:/bin")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start monitor: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(exited)
	}()
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	t.Cleanup(stop)
	return stop
}

// monitorLog returns what the session's monitor helper has written so far.
func (e *closureEnv) monitorLog(t *testing.T, session string) string {
	t.Helper()
	content, _ := os.ReadFile(filepath.Join(filepath.Dir(e.workDir), session+"-monitor.log"))
	return string(content)
}

// told reads the told record for one message from the helpers' state.db.
func (e *closureEnv) told(t *testing.T, project, agent string, messageID int64) *state.ToldRecord {
	t.Helper()
	store, err := state.Open(e.stateDB())
	if err != nil {
		t.Fatalf("open state.db: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate state.db: %v", err)
	}
	rec, err := store.GetToldRecord(project, agent, messageID)
	if err != nil {
		t.Fatalf("read told record: %v", err)
	}
	return rec
}

// spawnNudgeAgent spawns one stand-in Claude agent in a git repo, shows the
// idle screen, and returns its pid folder and Agent Mail name. spawnArgs are
// added to the spawn command.
func spawnNudgeAgent(t *testing.T, env *closureEnv, agents *screenAgents, session string, spawnArgs ...string) (pid, agent string) {
	t.Helper()
	gitInitForTest(t, env.projectDir(session))
	env.run(t, "execute", append([]string{"spawn", session, "--cc=1"}, spawnArgs...)...)
	pid = agents.waitStarted(t, 1, 20*time.Second)[0]
	agents.setScreen(t, pid, "cc_idle.txt")
	return pid, agents.agentEnv(t, pid, "AGENT_NAME")
}

// Behavior 2: an idle agent with two new messages gets one reminder listing
// both, and later polls type nothing more.
func TestClosure_IdleAgentNudgedOnce(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "nudgetest")
	project := env.projectDir("nudgetest")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 71, From: "RedStone", Subject: "first"})
	mail.addMessage(agent, agentmail.InboxMessage{ID: 72, From: "RedStone", Subject: "second"})

	env.startMonitor(t, "nudgetest")

	got := agents.waitTyped(t, pid, "to read them", 15*time.Second)
	if strings.Count(got, "You have 2 new messages") != 1 ||
		!strings.Contains(got, "from RedStone: first") || !strings.Contains(got, "from RedStone: second") ||
		!strings.Contains(got, fmt.Sprintf("agent_name %q", agent)) {
		t.Fatalf("typed = %q", got)
	}
	for _, id := range []int64{71, 72} {
		rec := env.told(t, project, agent, id)
		if rec == nil || rec.ToldBy != state.ToldByIdleNudge {
			t.Fatalf("told record for %d = %+v", id, rec)
		}
	}
	// A real agent shows its idle screen again after a reminder, so only the
	// told records can keep later polls from typing it again.
	agents.setScreen(t, pid, "cc_idle.txt")
	time.Sleep(3 * time.Second)
	if again := agents.typed(t, pid); again != got {
		t.Fatalf("typed more after the first reminder: %q", strings.TrimPrefix(again, got))
	}
}

// runStopHook runs the Stop hook command spawn wrote into the session's folder,
// as Claude Code runs it at a turn end, and returns its stdout.
func runStopHook(t *testing.T, env *closureEnv, session, agent string) string {
	t.Helper()
	dir := env.projectDir(session)
	command := onlyNtmStopEntry(t, readSettingsFile(t, dir)).Command
	return env.runShell(t, dir, command, "{}", "AGENT_NAME="+agent, closureHelperModeEnv+"=argv", closureHelperArgsEnv+"=")
}

// Behavior 3: a message is told once, by the nudge or by the Stop hook,
// whichever comes first.
func TestClosure_ToldOnceAcrossHookAndNudge(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "toldonce")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 71, From: "RedStone", Subject: "first"})
	env.startMonitor(t, "toldonce")
	nudged := agents.waitTyped(t, pid, "from RedStone: first", 15*time.Second)
	agents.setScreen(t, pid, "cc_idle.txt")

	if out := runStopHook(t, env, "toldonce", agent); out != "" {
		t.Fatalf("the hook repeated what the nudge told: %q", out)
	}

	agents.setScreen(t, pid, "cc_working.txt")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 73, From: "RedStone", Subject: "third"})
	time.Sleep(3 * time.Second)
	if typed := agents.typed(t, pid); typed != nudged {
		t.Fatalf("typed into a busy agent: %q", strings.TrimPrefix(typed, nudged))
	}
	if rec := env.told(t, env.projectDir("toldonce"), agent, 73); rec != nil {
		t.Fatalf("claimed while busy: %+v", rec)
	}
	decision := parseStopDecision(t, runStopHook(t, env, "toldonce", agent))
	if !strings.Contains(decision.Reason, "from RedStone: third") {
		t.Fatalf("hook reason = %q", decision.Reason)
	}

	agents.setScreen(t, pid, "cc_idle.txt")
	time.Sleep(3 * time.Second)
	if strings.Contains(agents.typed(t, pid), "third") {
		t.Fatal("the nudge repeated what the hook told")
	}
}

// Behavior 4: a new monitor process doesn't repeat what an earlier one told.
func TestClosure_MonitorRestartDoesNotRepeat(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "restart")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 71, From: "RedStone", Subject: "first"})
	mail.addMessage(agent, agentmail.InboxMessage{ID: 72, From: "RedStone", Subject: "second"})

	stop := env.startMonitor(t, "restart")
	first := agents.waitTyped(t, pid, "to read them", 15*time.Second)
	// A real agent shows its idle screen again after a reminder, so only the
	// told records can keep it from being told twice.
	agents.setScreen(t, pid, "cc_idle.txt")
	stop()
	env.startMonitor(t, "restart")
	time.Sleep(4 * time.Second)
	if again := agents.typed(t, pid); again != first {
		t.Fatalf("the second monitor typed: %q", strings.TrimPrefix(again, first))
	}
}

// Behavior 5: a pane whose agent has exited gets nothing typed at its shell
// prompt, no claim, and one log line.
func TestClosure_ShellPaneNeverTyped(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "shellpane")
	agents.quit(t, pid)
	time.Sleep(time.Second) // the stand-in checks for quit every 0.2s
	mail.addMessage(agent, agentmail.InboxMessage{ID: 81, From: "RedStone", Subject: "not for a shell"})

	env.startMonitor(t, "shellpane")
	time.Sleep(4 * time.Second)

	screen := env.runShell(t, env.workDir, "tmux capture-pane -p -t shellpane", "")
	if strings.Contains(screen, "not for a shell") || strings.Contains(screen, "new message") {
		t.Fatalf("reminder typed at a shell prompt:\n%s", screen)
	}
	if rec := env.told(t, env.projectDir("shellpane"), agent, 81); rec != nil {
		t.Fatalf("claimed for a shell pane: %+v", rec)
	}
	if n := strings.Count(env.monitorLog(t, "shellpane"), "no agent running in pane"); n != 1 {
		t.Fatalf("log lines = %d, want 1:\n%s", n, env.monitorLog(t, "shellpane"))
	}
}

// Loop 5, Behavior 13: an agent added with ntm add while the monitor runs is
// nudged, with no monitor restart, and the first agent gets nothing.
func TestClosure_AddedAgentNudged(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	firstPID, _ := spawnNudgeAgent(t, env, agents, "addnudge")
	env.startMonitor(t, "addnudge")

	env.run(t, "execute", "add", "addnudge", "--cc=1")
	started := agents.waitStarted(t, 2, 20*time.Second)
	addedPID := started[1]
	agents.setScreen(t, addedPID, "cc_idle.txt")
	added := agents.agentEnv(t, addedPID, "AGENT_NAME")
	mail.addMessage(added, agentmail.InboxMessage{ID: 91, From: "RedStone", Subject: "added nudge"})

	if got := agents.waitTyped(t, addedPID, "added nudge", 15*time.Second); !strings.Contains(got, fmt.Sprintf("agent_name %q", added)) {
		t.Fatalf("added agent typed = %q", got)
	}
	if got := agents.typed(t, firstPID); strings.Contains(got, "new message") {
		t.Fatalf("the first agent got a reminder: %q", got)
	}
}

// attachTypingClient attaches a terminal client to session through script, as
// a person's terminal would be, shows the agent's pane in it, and returns a
// function that types keys into it. A control-mode client won't do: its
// commands don't move client_activity (Loop 5 Sprint 3 evidence).
func (e *closureEnv) attachTypingClient(t *testing.T, session string) (typeKeys func(keys string) error) {
	t.Helper()
	paneID := ""
	panes := e.runShell(t, e.workDir, "tmux list-panes -t "+session+" -F '#{pane_id} #{@ntm_type}'", "")
	for line := range strings.SplitSeq(panes, "\n") {
		if id, agentType, _ := strings.Cut(strings.TrimSpace(line), " "); agentType == "cc" {
			paneID = id
		}
	}
	if paneID == "" {
		t.Fatalf("no agent pane in session %s:\n%s", session, panes)
	}
	e.runShell(t, e.workDir, "tmux select-pane -t "+paneID, "")

	cmd := exec.Command("script", "-qfc", "tmux attach -t "+session, "/dev/null")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	for key, value := range e.vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start script client: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(e.runShell(t, e.workDir, "tmux list-clients -F '#{pane_id}'", ""), paneID) {
		if time.Now().After(deadline) {
			t.Fatalf("client didn't attach to %s within 5s", paneID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return func(keys string) error {
		_, err := io.WriteString(stdin, keys)
		return err
	}
}

// Loop 5, Behavior 20: a person typing in the agent's pane holds the nudge
// back, with no told record, until their keystrokes have been quiet for the
// quiet period (2 seconds here).
func TestClosure_TypingHoldsNudge(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Fatalf("script is required for this closure test: %v", err)
	}
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "typehold")
	typeKeys := env.attachTypingClient(t, "typehold")

	stopTyping := make(chan struct{})
	typingErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopTyping:
				typingErr <- nil
				return
			case <-ticker.C:
				// A key and its erase, so the agent's input line stays empty.
				if err := typeKeys("x\x7f"); err != nil {
					typingErr <- err
					return
				}
			}
		}
	}()
	mail.addMessage(agent, agentmail.InboxMessage{ID: 95, From: "RedStone", Subject: "typing test"})
	env.startMonitor(t, "typehold")

	time.Sleep(4 * time.Second)
	held := agents.typed(t, pid)
	close(stopTyping)
	if err := <-typingErr; err != nil {
		t.Fatalf("typing into the client: %v", err)
	}
	if strings.Contains(held, "typing test") || strings.Contains(held, "new message") {
		t.Fatalf("reminder typed while a person was typing: %q", held)
	}
	if rec := env.told(t, env.projectDir("typehold"), agent, 95); rec != nil {
		t.Fatalf("claimed while a person was typing: %+v", rec)
	}

	agents.waitTyped(t, pid, "typing test", 10*time.Second)
	if n := strings.Count(env.monitorLog(t, "typehold"), "idle nudge: held, person typing"); n != 1 {
		t.Fatalf("held logged %d times, want 1:\n%s", n, env.monitorLog(t, "typehold"))
	}
}

// Loop 5, Behavior 23: a real Claude screen with unsent typed text holds the
// nudge however quiet the keys are, and a real screen with only preview text
// doesn't. The stand-in draws the captures with their styles, so the monitor's
// capture-pane -e sees what it would see on a real pane.
func TestClosure_TypedTextHoldsNudge(t *testing.T) {
	mail := newFakeAgentMail(t)
	env := newClosureEnv(t)
	env.privateTmux(t)
	agents := env.screenAgentConfig(t, mail, "")
	pid, agent := spawnNudgeAgent(t, env, agents, "drafthold")
	agents.setScreen(t, pid, "cc_input_typed.ansi")
	mail.addMessage(agent, agentmail.InboxMessage{ID: 97, From: "RedStone", Subject: "draft test"})
	env.startMonitor(t, "drafthold")

	time.Sleep(3 * time.Second)
	if held := agents.typed(t, pid); strings.Contains(held, "draft test") || strings.Contains(held, "new message") {
		t.Fatalf("reminder typed over a person's unsent text: %q", held)
	}
	if rec := env.told(t, env.projectDir("drafthold"), agent, 97); rec != nil {
		t.Fatalf("claimed while unsent text was on the input line: %+v", rec)
	}

	agents.setScreen(t, pid, "cc_input_preview.ansi")
	got := agents.waitTyped(t, pid, "draft test", 10*time.Second)
	if strings.Contains(got, "add a second line") {
		t.Fatalf("the preview text was submitted with the reminder: %q", got)
	}
}

// assertNoNudge runs the session's monitor for 3 seconds and checks it made no
// fetch_inbox call and typed no reminder, while its other work still started.
func assertNoNudge(t *testing.T, env *closureEnv, mail *fakeAgentMail, agents *screenAgents, pid, session string) {
	t.Helper()
	fetchesBefore := len(mail.callsFor("fetch_inbox"))
	env.startMonitor(t, session)
	time.Sleep(3 * time.Second)
	if fetches := mail.callsFor("fetch_inbox"); len(fetches) != fetchesBefore {
		t.Fatalf("the monitor asked Agent Mail for the inbox: %+v", fetches[fetchesBefore:])
	}
	if typed := agents.typed(t, pid); strings.Contains(typed, "new message") {
		t.Fatalf("the monitor typed a reminder: %q", typed)
	}
	if log := env.monitorLog(t, session); !strings.Contains(log, "Monitoring session '"+session+"'") {
		t.Fatalf("the monitor didn't start its other work:\n%s", log)
	}
}

// Loop 5, Behavior 26: with the nudge switch off, whether in the default
// config, in a config passed to spawn with --config, or by Agent Mail being
// off, the monitor neither asks Agent Mail nor types. The monitor's own
// command line carries no --config, so it must read the switch spawn froze.
func TestClosure_NudgeSwitchOff(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "idle_nudge_enabled = false\n")
		pid, agent := spawnNudgeAgent(t, env, agents, "nonudge")
		mail.addMessage(agent, agentmail.InboxMessage{ID: 101, From: "RedStone", Subject: "no nudge"})
		assertNoNudge(t, env, mail, agents, pid, "nonudge")
	})
	t.Run("spawn --config", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "")
		content, err := os.ReadFile(env.configPath())
		if err != nil {
			t.Fatal(err)
		}
		offConfig := filepath.Join(filepath.Dir(env.workDir), "nudge-off.toml")
		writeFile(t, offConfig, strings.Replace(string(content), "[agent_mail]\n", "[agent_mail]\nidle_nudge_enabled = false\n", 1))
		pid, agent := spawnNudgeAgent(t, env, agents, "nonudgecfg", "--config", offConfig)
		mail.addMessage(agent, agentmail.InboxMessage{ID: 102, From: "RedStone", Subject: "no nudge"})
		assertNoNudge(t, env, mail, agents, pid, "nonudgecfg")
	})
	t.Run("agent mail off", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "")
		content, err := os.ReadFile(env.configPath())
		if err != nil {
			t.Fatal(err)
		}
		env.writeConfig(t, strings.Replace(string(content), "[agent_mail]\nenabled = true\n", "[agent_mail]\nenabled = false\n", 1))
		gitInitForTest(t, env.projectDir("nomail"))
		env.run(t, "execute", "spawn", "nomail", "--cc=1")
		pid := agents.waitStarted(t, 1, 20*time.Second)[0]
		agents.setScreen(t, pid, "cc_idle.txt")
		assertNoNudge(t, env, mail, agents, pid, "nomail")
	})
}

// Loop 5, Behavior 27: the Stop hook's switch and the nudge's switch are
// independent. Each feature reads only its own.
func TestClosure_HookAndNudgeSwitchesIndependent(t *testing.T) {
	settingsFile := func(env *closureEnv, session string) string {
		return filepath.Join(env.projectDir(session), ".claude", "settings.local.json")
	}
	t.Run("hook off, nudge on", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "stop_hook_enabled = false\n")
		pid, agent := spawnNudgeAgent(t, env, agents, "hookoff")
		if _, err := os.Stat(settingsFile(env, "hookoff")); !os.IsNotExist(err) {
			t.Fatalf("hook file written with the hook off: %v", err)
		}
		mail.addMessage(agent, agentmail.InboxMessage{ID: 111, From: "RedStone", Subject: "nudged anyway"})
		env.startMonitor(t, "hookoff")
		agents.waitTyped(t, pid, "nudged anyway", 15*time.Second)
	})
	t.Run("nudge off, hook on", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "idle_nudge_enabled = false\n")
		pid, agent := spawnNudgeAgent(t, env, agents, "nudgeoff")
		mail.addMessage(agent, agentmail.InboxMessage{ID: 112, From: "RedStone", Subject: "hook tells it"})
		assertNoNudge(t, env, mail, agents, pid, "nudgeoff")
		decision := parseStopDecision(t, runStopHook(t, env, "nudgeoff", agent))
		if !strings.Contains(decision.Reason, "from RedStone: hook tells it") {
			t.Fatalf("hook reason = %q", decision.Reason)
		}
	})
	t.Run("both off", func(t *testing.T) {
		mail := newFakeAgentMail(t)
		env := newClosureEnv(t)
		env.privateTmux(t)
		agents := env.screenAgentConfig(t, mail, "stop_hook_enabled = false\nidle_nudge_enabled = false\n")
		pid, agent := spawnNudgeAgent(t, env, agents, "bothoff")
		if _, err := os.Stat(settingsFile(env, "bothoff")); !os.IsNotExist(err) {
			t.Fatalf("hook file written with the hook off: %v", err)
		}
		mail.addMessage(agent, agentmail.InboxMessage{ID: 113, From: "RedStone", Subject: "nobody tells it"})
		assertNoNudge(t, env, mail, agents, pid, "bothoff")
	})
}

func testRegistry(paneAgents map[string]string) *agentmail.SessionAgentRegistry {
	return &agentmail.SessionAgentRegistry{PaneIDMap: paneAgents, Agents: map[string]string{}}
}

// Loop 5, Behavior 14: targets come from the registry, each agent matched to
// its own pane by pane ID.
func TestNudgeTargets(t *testing.T) {
	registry := testRegistry(map[string]string{"%1": "BlueLake", "%2": "GreenCastle"})
	panes := []tmux.Pane{
		{ID: "%2", Type: tmux.AgentCodex, Command: "codex"},
		{ID: "%1", Type: tmux.AgentClaude, Command: "claude"},
	}
	got := nudgeTargets(registry, panes)
	if len(got) != 2 {
		t.Fatalf("targets = %+v", got)
	}
	for _, target := range got {
		if registry.PaneIDMap[target.PaneID] != target.Agent || target.Pane.ID != target.PaneID {
			t.Fatalf("target %+v matched the wrong pane", target)
		}
	}
	// A title-keyed entry with no pane ID entry isn't used: titles can repeat.
	registry.Agents["proj__cc_3"] = "RedStone"
	if got := nudgeTargets(registry, append(panes, tmux.Pane{ID: "%3", Title: "proj__cc_3", Type: tmux.AgentClaude})); len(got) != 2 {
		t.Fatalf("a title-only entry became a target: %+v", got)
	}
}

// Behavior 14 property: however the pane list is ordered, each target's name
// is the registry's name for its pane.
func TestNudgeTargets_ShuffleProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(1, 6).Draw(rt, "agents")
		registry := testRegistry(map[string]string{})
		var panes []tmux.Pane
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("%%%d", i)
			registry.PaneIDMap[id] = fmt.Sprintf("Agent%d", i)
			panes = append(panes, tmux.Pane{ID: id, Type: tmux.AgentClaude})
		}
		shuffled := rapid.Permutation(panes).Draw(rt, "order")
		got := nudgeTargets(registry, shuffled)
		if len(got) != count {
			rt.Fatalf("targets = %d, want %d", len(got), count)
		}
		for _, target := range got {
			if registry.PaneIDMap[target.PaneID] != target.Agent {
				rt.Fatalf("target %+v has another pane's name", target)
			}
		}
	})
}

// Loop 5, Behavior 15: a registry entry whose pane is gone is skipped.
func TestNudgeTargets_MissingPaneSkipped(t *testing.T) {
	registry := testRegistry(map[string]string{"%1": "BlueLake", "%9": "Gone"})
	got := nudgeTargets(registry, []tmux.Pane{{ID: "%1", Type: tmux.AgentClaude}})
	if len(got) != 1 || got[0].Agent != "BlueLake" {
		t.Fatalf("targets = %+v", got)
	}
}

// Behavior 15: a session with no registry yet gives no targets and an error
// the poll logs once, not on every poll, until the registry appears.
func TestIdleNudgePoll_MissingRegistryLogsOnce(t *testing.T) {
	logs := captureNudgeLogs(t)
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.failStep["targets"] = errNoAgentRegistry
	nudger := fake.nudger()
	for i := 0; i < 3; i++ {
		nudger.poll(context.Background())
	}
	if n := strings.Count(logs.String(), errNoAgentRegistry.Error()); n != 1 {
		t.Fatalf("logged %d times, want 1:\n%s", n, logs.String())
	}
	if fake.count("fetch") != 0 {
		t.Fatalf("fetched without targets: %v", fake.calls)
	}
	delete(fake.failStep, "targets")
	nudger.poll(context.Background())
	if fake.count("send") != 1 {
		t.Fatalf("after the registry appeared: %v", fake.calls)
	}
}

// Loop 5, Behavior 16: only Claude and Codex panes are nudged, by the type
// ntm set on the pane.
func TestNudgeTargets_AgentTypes(t *testing.T) {
	registry := testRegistry(map[string]string{"%1": "A", "%2": "B", "%3": "C", "%4": "D", "%5": "E"})
	panes := []tmux.Pane{
		{ID: "%1", Type: tmux.AgentClaude},
		{ID: "%2", Type: tmux.AgentCodex},
		{ID: "%3", Type: tmux.AgentGemini},
		{ID: "%4", Type: tmux.AgentOllama},
		{ID: "%5", Type: tmux.AgentUser},
	}
	var names []string
	for _, target := range nudgeTargets(registry, panes) {
		names = append(names, target.Agent)
	}
	sort.Strings(names)
	if !slices.Equal(names, []string{"A", "B"}) {
		t.Fatalf("targets = %v, want only the Claude and Codex panes", names)
	}
}

// Loop 5, Behavior 18: the keystroke guard holds a pane that an attached
// client showing it has typed into within the quiet period, and holds on a
// read error.
func TestRecentHumanActivity(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	quiet := 60 * time.Second
	client := func(pane string, ago time.Duration) tmux.ClientActivity {
		return tmux.ClientActivity{Name: "/dev/pts/1", PaneID: pane, Activity: now.Add(-ago)}
	}
	cases := []struct {
		name    string
		clients []tmux.ClientActivity
		readErr error
		want    bool
	}{
		{"active 20s ago on the pane", []tmux.ClientActivity{client("%a", 20*time.Second)}, nil, true},
		{"quiet 90s on the pane", []tmux.ClientActivity{client("%a", 90*time.Second)}, nil, false},
		{"exactly the quiet period", []tmux.ClientActivity{client("%a", quiet)}, nil, false},
		{"active on another pane", []tmux.ClientActivity{client("%b", 5*time.Second)}, nil, false},
		{"one quiet and one active", []tmux.ClientActivity{client("%a", 5*time.Minute), client("%a", 10*time.Second)}, nil, true},
		{"detached", nil, nil, false},
		{"read error", nil, errors.New("list-clients failed"), true},
		{"read error with a quiet client", []tmux.ClientActivity{client("%a", time.Hour)}, errors.New("boom"), true},
		{"activity in the future", []tmux.ClientActivity{client("%a", -5*time.Second)}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recentHumanActivity(tc.clients, "%a", now, quiet, tc.readErr); got != tc.want {
				t.Fatalf("recentHumanActivity = %v, want %v", got, tc.want)
			}
		})
	}
}

// Behavior 18 property: the guard holds exactly when the read failed, or some
// client on the pane typed less than the quiet period ago.
func TestRecentHumanActivity_Property(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	quiet := 60 * time.Second
	rapid.Check(t, func(rt *rapid.T) {
		count := rapid.IntRange(0, 4).Draw(rt, "clients")
		clients := make([]tmux.ClientActivity, 0, count)
		for i := range count {
			clients = append(clients, tmux.ClientActivity{
				Name:     fmt.Sprintf("/dev/pts/%d", i),
				PaneID:   rapid.SampledFrom([]string{"%a", "%b"}).Draw(rt, "pane"),
				Activity: now.Add(-time.Duration(rapid.IntRange(0, 200).Draw(rt, "age")) * time.Second),
			})
		}
		var readErr error
		if rapid.Bool().Draw(rt, "read fails") {
			readErr = errors.New("list-clients failed")
		}
		want := readErr != nil || slices.ContainsFunc(clients, func(c tmux.ClientActivity) bool {
			return c.PaneID == "%a" && now.Sub(c.Activity) < quiet
		})
		if got := recentHumanActivity(clients, "%a", now, quiet, readErr); got != want {
			rt.Fatalf("got %v, want %v for %+v, error %v", got, want, clients, readErr)
		}
	})
}

// Loop 5, Behavior 19: while a person is typing, the poll claims and sends
// nothing and logs once for each batch of messages; once they're quiet, the
// next poll sends.
func TestIdleNudgePoll_HeldThenSent(t *testing.T) {
	logs := captureNudgeLogs(t)
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.addUnread("BlueLake", 72, "second")
	fake.state.RecentHumanActivity = true
	nudger := fake.nudger()
	held := func() int { return strings.Count(logs.String(), "idle nudge: held, person typing") }

	for range 3 {
		nudger.poll(context.Background())
	}
	if fake.count("claim") != 0 || fake.count("send") != 0 {
		t.Fatalf("claimed or sent while held: %v", fake.calls)
	}
	if held() != 1 {
		t.Fatalf("held logged %d times over 3 polls, want 1:\n%s", held(), logs.String())
	}

	fake.addUnread("BlueLake", 73, "third") // the batch changes while held
	nudger.poll(context.Background())
	if held() != 2 {
		t.Fatalf("held logged %d times after the batch changed, want 2:\n%s", held(), logs.String())
	}

	fake.state.RecentHumanActivity = false
	nudger.poll(context.Background())
	if fake.count("send") != 1 || !strings.Contains(fake.lastSent(), "third") {
		t.Fatalf("after the person went quiet: %v", fake.calls)
	}

	fake.addUnread("BlueLake", 74, "fourth")
	fake.state.RecentHumanActivity = true
	nudger.poll(context.Background())
	nudger.poll(context.Background())
	if held() != 3 {
		t.Fatalf("held logged %d times for the new batch, want 3:\n%s", held(), logs.String())
	}
	if fake.count("send") != 1 {
		t.Fatalf("sent while held again: %v", fake.calls)
	}
}

// Loop 5, Behavior 22: the Human Typing Guard holds for unsent typed text or
// recent keystrokes, and a failed pane read holds too.
func TestIdleNudgePoll_TypedTextGuard(t *testing.T) {
	cases := []struct {
		name      string
		typed     agent.TypedText
		recent    bool
		readFails bool
		wantSend  bool
	}{
		{"typed text, keys quiet for hours", agent.TypedTextPresent, false, false, false},
		{"no typed text", agent.TypedTextAbsent, false, false, true},
		{"unknown, keys quiet", agent.TypedTextUnknown, false, false, true},
		{"unknown, recent keys", agent.TypedTextUnknown, true, false, false},
		{"no typed text, recent keys", agent.TypedTextAbsent, true, false, false},
		{"pane read fails", agent.TypedTextAbsent, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := typedTextGuardSends(t, tc.typed, tc.recent, tc.readFails); got != tc.wantSend {
				t.Fatalf("sent = %v, want %v", got, tc.wantSend)
			}
		})
	}
}

// Behavior 22 property: a reminder is sent only when the pane read worked, no
// typed text is present, and no keys are recent. The input space is 12
// combinations, so every one is checked rather than a random sample.
func TestIdleNudgePoll_TypedTextGuardProperty(t *testing.T) {
	for _, typed := range []agent.TypedText{agent.TypedTextUnknown, agent.TypedTextAbsent, agent.TypedTextPresent} {
		for _, recent := range []bool{false, true} {
			for _, readFails := range []bool{false, true} {
				want := !readFails && typed != agent.TypedTextPresent && !recent
				if got := typedTextGuardSends(t, typed, recent, readFails); got != want {
					t.Errorf("typed %v, recent %v, read fails %v: sent = %v, want %v", typed, recent, readFails, got, want)
				}
			}
		}
	}
}

// typedTextGuardSends runs one poll for an idle, live agent with one message
// and the given pane state, and reports whether a reminder was sent.
func typedTextGuardSends(t *testing.T, typed agent.TypedText, recent, readFails bool) bool {
	t.Helper()
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.state.TypedText = typed
	fake.state.RecentHumanActivity = recent
	if readFails {
		fake.failStep["state"] = errors.New("capture-pane failed")
	}
	fake.nudger().poll(context.Background())
	return fake.count("send") == 1
}

// fakeNudge is an idle nudger over fakes, with a real state store on a temp
// file. It records each dependency call in order, such as "fetch BlueLake",
// "state %1", "claim 71", "record 71", "send %1" and "release 71".
type fakeNudge struct {
	t         *testing.T
	mu        sync.Mutex
	targets   []nudgeTarget
	unread    map[string][]agentmail.InboxMessage
	state     paneNudgeState
	store     *state.Store
	failStep  map[string]error // "targets", "fetch", "state", "store", "claim", "send"
	failAgent string           // when set, only this agent's steps fail
	onState   func()
	calls     []string
	sent      []string
	releases  int
	now       time.Time
}

func newFakeNudge(t *testing.T, targets ...nudgeTarget) *fakeNudge {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		targets = []nudgeTarget{{PaneID: "%1", AgentType: tmux.AgentClaude, Agent: "BlueLake"}}
	}
	return &fakeNudge{
		t:        t,
		targets:  targets,
		unread:   map[string][]agentmail.InboxMessage{},
		state:    paneNudgeState{Live: true, Idle: true},
		store:    store,
		failStep: map[string]error{},
		now:      time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
	}
}

func (f *fakeNudge) addUnread(agent string, id int, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unread[agent] = append(f.unread[agent], agentmail.InboxMessage{ID: id, From: "RedStone", Subject: subject})
}

func (f *fakeNudge) note(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeNudge) fail(step, agent string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failStep[step]; err != nil && (f.failAgent == "" || f.failAgent == agent) {
		return err
	}
	return nil
}

// fakeNudgeStore is the real store with claims that can fail on demand.
type fakeNudgeStore struct{ f *fakeNudge }

func (s fakeNudgeStore) ClaimToldRecord(rec state.ToldRecord) (bool, error) {
	s.f.note(fmt.Sprintf("claim %d", rec.MessageID))
	if err := s.f.fail("claim", rec.AgentName); err != nil {
		return false, err
	}
	return s.f.store.ClaimToldRecord(rec)
}

func (s fakeNudgeStore) ReleaseToldRecord(rec state.ToldRecord) error {
	s.f.note(fmt.Sprintf("release %d", rec.MessageID))
	s.f.mu.Lock()
	s.f.releases++
	s.f.mu.Unlock()
	return s.f.store.ReleaseToldRecord(rec)
}

func (f *fakeNudge) nudger() *idleNudger {
	return newIdleNudger(idleNudgeDeps{
		ProjectKey: "/proj",
		Targets: func(context.Context) ([]nudgeTarget, error) {
			f.note("targets")
			if err := f.fail("targets", ""); err != nil {
				return nil, err
			}
			return f.targets, nil
		},
		Fetch: func(_ context.Context, agent string) ([]agentmail.InboxMessage, error) {
			f.note("fetch " + agent)
			if err := f.fail("fetch", agent); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]agentmail.InboxMessage(nil), f.unread[agent]...), nil
		},
		PaneState: func(_ context.Context, target nudgeTarget) (paneNudgeState, error) {
			f.note("state " + target.PaneID)
			if f.onState != nil {
				f.onState()
			}
			if err := f.fail("state", target.Agent); err != nil {
				return paneNudgeState{}, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.state, nil
		},
		Store: func() (idleNudgeStore, error) {
			if err := f.fail("store", ""); err != nil {
				return nil, err
			}
			return fakeNudgeStore{f}, nil
		},
		Record: func(agent string, msg agentmail.InboxMessage) {
			f.note(fmt.Sprintf("record %d", msg.ID))
		},
		Send: func(target nudgeTarget, text string) error {
			f.note("send " + target.PaneID)
			if err := f.fail("send", target.Agent); err != nil {
				return err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.sent = append(f.sent, target.PaneID+": "+text)
			return nil
		},
		Now: func() time.Time { return f.now },
	})
}

func (f *fakeNudge) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeNudge) lastSent() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return ""
	}
	return f.sent[len(f.sent)-1]
}

// Behavior 7: a busy agent gets no send and no claim; once idle, one send.
func TestIdleNudgePoll_BusyThenIdle(t *testing.T) {
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.addUnread("BlueLake", 72, "second")
	fake.state = paneNudgeState{Live: true, Idle: false}
	nudger := fake.nudger()

	nudger.poll(context.Background())
	if fake.count("send") != 0 || fake.count("claim") != 0 || fake.count("record") != 0 {
		t.Fatalf("busy: %v", fake.calls)
	}
	fake.state.Idle = true
	nudger.poll(context.Background())
	if fake.count("send") != 1 || fake.count("claim") != 2 {
		t.Fatalf("idle: %v", fake.calls)
	}
	if sent := fake.lastSent(); !strings.Contains(sent, "from RedStone: first") || !strings.Contains(sent, "from RedStone: second") {
		t.Fatalf("sent %q", sent)
	}
}

// Behavior 7 edge case: no unread mail means no pane check at all.
func TestIdleNudgePoll_NoMailNoPaneCheck(t *testing.T) {
	fake := newFakeNudge(t)
	read := agentmail.FlexTime{Time: time.Now()}
	fake.unread["BlueLake"] = []agentmail.InboxMessage{{ID: 70, From: "RedStone", Subject: "old", ReadAt: &read}}
	fake.nudger().poll(context.Background())
	if fake.count("state") != 0 || fake.count("send") != 0 {
		t.Fatalf("calls = %v", fake.calls)
	}
}

// Behavior 7 property: over any run of busy and idle polls, there is one send
// if any poll found the agent idle, and none otherwise.
func TestIdleNudgePoll_BusyIdleProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		fake := newFakeNudge(t)
		fake.addUnread("BlueLake", 71, "first")
		nudger := fake.nudger()
		idle := rapid.SliceOfN(rapid.Bool(), 1, 10).Draw(rt, "idle")
		for _, isIdle := range idle {
			fake.state.Idle = isIdle
			nudger.poll(context.Background())
		}
		want := 0
		if slices.Contains(idle, true) {
			want = 1
		}
		if got := fake.count("send"); got != want {
			rt.Fatalf("sends = %d, want %d for %v", got, want, idle)
		}
	})
}

// Behavior 8: claims come after the idle and live checks, and mail that
// arrives after the fetch waits for the next poll.
func TestIdleNudgePoll_Order(t *testing.T) {
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.addUnread("BlueLake", 72, "second")
	added := false
	fake.onState = func() {
		if !added {
			added = true
			fake.addUnread("BlueLake", 73, "late")
		}
	}
	nudger := fake.nudger()
	nudger.poll(context.Background())
	want := []string{"targets", "fetch BlueLake", "state %1", "claim 71", "claim 72", "record 71", "record 72", "send %1"}
	if !slices.Equal(fake.calls, want) {
		t.Fatalf("order = %v, want %v", fake.calls, want)
	}
	if strings.Contains(fake.lastSent(), "late") {
		t.Fatal("a message added after the fetch went into this reminder")
	}
	nudger.poll(context.Background())
	if sent := fake.lastSent(); !strings.Contains(sent, "late") || strings.Contains(sent, "first") {
		t.Fatalf("next poll sent %q, want only the late message", sent)
	}
}

// Behavior 9: a failed send releases the claims, and the next poll retries.
func TestIdleNudgePoll_SendFailureReleases(t *testing.T) {
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.addUnread("BlueLake", 72, "second")
	fake.failStep["send"] = errors.New("tmux gone")
	nudger := fake.nudger()
	nudger.poll(context.Background())
	if fake.releases != 2 {
		t.Fatalf("releases = %d, want 2: %v", fake.releases, fake.calls)
	}
	delete(fake.failStep, "send")
	fake.now = fake.now.Add(time.Second)
	nudger.poll(context.Background())
	if sent := fake.lastSent(); !strings.Contains(sent, "You have 2 new messages") {
		t.Fatalf("retry sent %q", sent)
	}
}

// Behavior 11: each failure is logged, nothing is typed for that agent, the
// other agent is still nudged, and the next poll works once it clears.
func TestIdleNudgePoll_Failures(t *testing.T) {
	for _, step := range []string{"fetch", "state", "store", "claim"} {
		t.Run(step, func(t *testing.T) {
			logs := captureNudgeLogs(t)
			fake := newFakeNudge(t,
				nudgeTarget{PaneID: "%1", AgentType: tmux.AgentClaude, Agent: "BlueLake"},
				nudgeTarget{PaneID: "%2", AgentType: tmux.AgentCodex, Agent: "GreenCastle"})
			fake.addUnread("BlueLake", 71, "first")
			fake.addUnread("BlueLake", 72, "second")
			fake.addUnread("GreenCastle", 81, "for green")
			fake.failStep[step] = errors.New("injected " + step + " failure")
			if step != "store" {
				fake.failAgent = "BlueLake"
			}
			nudger := fake.nudger()
			nudger.poll(context.Background())

			for _, sent := range fake.sent {
				if strings.HasPrefix(sent, "%1") {
					t.Fatalf("%s failure: BlueLake was nudged: %q", step, sent)
				}
			}
			if step != "store" && !strings.HasPrefix(fake.lastSent(), "%2") {
				t.Fatalf("%s failure stopped GreenCastle's nudge: %v", step, fake.calls)
			}
			if !strings.Contains(logs.String(), "idle nudge") || !strings.Contains(logs.String(), "injected "+step+" failure") {
				t.Fatalf("no warning for the %s failure:\n%s", step, logs.String())
			}
			if rec, _ := fake.store.GetToldRecord("/proj", "BlueLake", 71); rec != nil {
				t.Fatalf("%s failure left a claim: %+v", step, rec)
			}

			delete(fake.failStep, step)
			fake.now = fake.now.Add(time.Second)
			nudger.poll(context.Background())
			if fake.count("send %1") == 0 {
				t.Fatalf("after the %s failure cleared, BlueLake wasn't nudged: %v", step, fake.calls)
			}
		})
	}
}

// Behavior 11: a failure listing targets is logged, and the next poll works.
func TestIdleNudgePoll_TargetsFailure(t *testing.T) {
	logs := captureNudgeLogs(t)
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.failStep["targets"] = errors.New("injected targets failure")
	nudger := fake.nudger()
	nudger.poll(context.Background())
	if fake.count("fetch") != 0 || !strings.Contains(logs.String(), "injected targets failure") {
		t.Fatalf("calls %v, logs:\n%s", fake.calls, logs.String())
	}
	delete(fake.failStep, "targets")
	nudger.poll(context.Background())
	if fake.count("send") != 1 {
		t.Fatalf("calls = %v", fake.calls)
	}
}

// A reminder that was sent is logged once, with its pane, agent and messages,
// so the monitor's log shows what it did, not only what it skipped.
func TestIdleNudgePoll_SentIsLogged(t *testing.T) {
	logs := captureNudgeLogs(t)
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.addUnread("BlueLake", 72, "second")
	nudger := fake.nudger()
	nudger.poll(context.Background())
	nudger.poll(context.Background())
	out := logs.String()
	if n := strings.Count(out, "idle nudge: reminder sent"); n != 1 {
		t.Fatalf("logged %d sends, want 1:\n%s", n, out)
	}
	for _, want := range []string{"pane=%1", "agent=BlueLake", "messages=\"[71 72]\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("send log lacks %s:\n%s", want, out)
		}
	}
}

// Behavior 5 (unit): a pane with no running agent gets nothing, and is logged
// once, not on every poll.
func TestIdleNudgePoll_NotLiveLoggedOnce(t *testing.T) {
	logs := captureNudgeLogs(t)
	fake := newFakeNudge(t)
	fake.addUnread("BlueLake", 71, "first")
	fake.state = paneNudgeState{Live: false, Idle: true}
	nudger := fake.nudger()
	for i := 0; i < 3; i++ {
		nudger.poll(context.Background())
	}
	if fake.count("claim") != 0 || fake.count("send") != 0 {
		t.Fatalf("calls = %v", fake.calls)
	}
	if n := strings.Count(logs.String(), "no agent running in pane"); n != 1 {
		t.Fatalf("logged %d times, want 1:\n%s", n, logs.String())
	}
}

// captureNudgeLogs sends slog to a buffer for the test.
func captureNudgeLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Behavior 12: the loop polls on each tick and returns when its context ends.
func TestRunIdleNudgeLoop_TicksAndStops(t *testing.T) {
	ticks := make(chan time.Time)
	var interval time.Duration
	stopped := false
	newTicker := func(d time.Duration) (<-chan time.Time, func()) {
		interval = d
		return ticks, func() { stopped = true }
	}
	fake := newFakeNudge(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runIdleNudgeLoop(ctx, 7*time.Second, newTicker, fake.nudger())
		close(done)
	}()
	for i := 0; i < 3; i++ {
		ticks <- time.Now()
	}
	ticks <- time.Now() // taken only after the third poll returned
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the loop didn't stop within 1s of its context ending")
	}
	if interval != 7*time.Second || !stopped {
		t.Fatalf("interval %s, ticker stopped %v", interval, stopped)
	}
	if got := fake.count("targets"); got < 3 {
		t.Fatalf("polls = %d, want at least 3", got)
	}
}
