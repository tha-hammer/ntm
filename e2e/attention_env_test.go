//go:build e2e

package e2e

// The attention journey tests (Loop 9) run the real ntm binary against a fake
// Agent Mail server, real tmux and stand-in agents, in a throwaway home and
// config folder, so the person's state.db, tmux sessions and Agent Mail are
// never touched. The setup copies internal/cli's closureEnv, fakeAgentMail
// and stand-in agents, which live in that package's test files and can't be
// imported here.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
)

// journeyEnv is one isolated environment for the built ntm binary.
type journeyEnv struct {
	root     string
	ntmBin   string
	vars     map[string]string
	mail     *journeyAgentMail
	agents   *standInAgents
	tmuxDir  string
	projects string
}

// newJourneyEnv makes the environment: a fake Agent Mail, claude and codex
// stand-ins, a private tmux server, and config.toml with Agent Mail on, a 1
// second nudge poll and a 2 second keystroke quiet period. agentMail holds
// more lines for the [agent_mail] table. It fails, never skips, when tmux or
// bash is missing.
func newJourneyEnv(t *testing.T, agentMail string) *journeyEnv {
	t.Helper()
	for _, tool := range []string{"tmux", "bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required for the attention journey tests: %v", tool, err)
		}
	}
	bin, err := ensureE2ENTMBin()
	if err != nil {
		t.Fatalf("build ntm: %v", err)
	}
	root := t.TempDir()
	tmuxDir, err := os.MkdirTemp("/tmp", "ntmjx") // short: tmux socket paths have a length limit
	if err != nil {
		t.Fatal(err)
	}
	env := &journeyEnv{
		root:     root,
		ntmBin:   bin,
		mail:     newJourneyAgentMail(t),
		agents:   &standInAgents{dir: filepath.Join(root, "agents")},
		tmuxDir:  tmuxDir,
		projects: filepath.Join(root, "projects"),
	}
	standInBin := filepath.Join(root, "bin")
	for _, dir := range []string{"config", "home", "data", "codex-home", "agents", "bin", "projects"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env.vars = map[string]string{
		"HOME":                      filepath.Join(root, "home"),
		"CODEX_HOME":                filepath.Join(root, "codex-home"),
		"XDG_CONFIG_HOME":           filepath.Join(root, "config"),
		"XDG_DATA_HOME":             filepath.Join(root, "data"),
		"NTM_CONFIG":                "",
		"AGENT_MAIL_URL":            env.mail.url(),
		"TMUX_TMPDIR":               tmuxDir,
		"TMUX":                      "",
		"TMUX_PANE":                 "",
		"AGENT_NAME":                "",
		"MCP_AGENT_MAIL_AGENT_NAME": "",
		"MCP_AGENT_MAIL_PROJECT":    "",
		// Only the stand-ins, ntm and the system folders, so nothing starts a
		// real agent or daemon.
		"PATH": standInBin + ":" + filepath.Dir(bin) + ":/usr/bin:/bin",
	}
	commands := map[string]string{}
	for _, agentType := range []string{"claude", "codex"} {
		script := filepath.Join(standInBin, agentType)
		if err := os.WriteFile(script, []byte(standInAgentScript), 0o755); err != nil {
			t.Fatal(err)
		}
		commands[agentType] = shellQuote(script) + " " + shellQuote(env.agents.dir) + " " + agentType
	}
	env.writeConfig(t, fmt.Sprintf(`projects_base = %q

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
`, env.projects, commands["claude"], commands["codex"], agentMail))
	t.Cleanup(func() {
		env.killMonitors(t)
		socket := filepath.Join(tmuxDir, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
		_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
		time.Sleep(200 * time.Millisecond) // let panes exit before the temp folders go
		_ = os.RemoveAll(tmuxDir)
	})
	return env
}

func (e *journeyEnv) writeConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(e.vars["XDG_CONFIG_HOME"], "ntm", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// environ is the process environment with the env's variables last, so they
// win.
func (e *journeyEnv) environ(extra ...string) []string {
	env := os.Environ()
	for key, value := range e.vars {
		env = append(env, key+"="+value)
	}
	return append(env, extra...)
}

// ntm runs the built binary in dir and returns its stdout. It fails the test
// if ntm fails.
func (e *journeyEnv) ntm(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return e.ntmAs(t, "", dir, args...)
}

// ntmAs is ntm run as an agent: AGENT_NAME is set when agent isn't empty.
func (e *journeyEnv) ntmAs(t *testing.T, agent, dir string, args ...string) string {
	t.Helper()
	var extra []string
	if agent != "" {
		extra = append(extra, "AGENT_NAME="+agent)
	}
	stdout, stderr, err := e.tryNtm(dir, extra, args...)
	if err != nil {
		t.Fatalf("ntm %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func (e *journeyEnv) tryNtm(dir string, extra []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(e.ntmBin, args...)
	cmd.Dir = dir
	cmd.Env = e.environ(extra...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// shell runs command with sh -c in dir, as Claude Code and Codex run a hook,
// and returns its stdout. It fails on a non-zero exit or any stderr.
func (e *journeyEnv) shell(t *testing.T, dir, command, stdin string, extra ...string) string {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = e.environ(extra...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("sh -c %q: %v\nstdout: %s\nstderr: %s", command, err, out.String(), errOut.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("sh -c %q wrote to stderr: %s", command, errOut.String())
	}
	return out.String()
}

// spawnAgent spawns one agent ("cc" or "cod") in a new git repo under
// projects_base, waits for its stand-in, shows that tool's idle screen, and
// returns the stand-in's pid folder, the agent's Agent Mail name and the work
// folder.
func (e *journeyEnv) spawnAgent(t *testing.T, session, agentType string) (pid, agent, dir string) {
	t.Helper()
	dir = filepath.Join(e.projects, session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	before := len(e.agents.started())
	e.ntm(t, dir, "spawn", session, "--"+agentType+"=1")
	pid = e.agents.waitStarted(t, before+1, 30*time.Second)[before]
	e.agents.setScreen(t, pid, agentType+"_idle.txt")
	return pid, e.agents.agentEnv(t, pid, "AGENT_NAME"), dir
}

// killSession ends a tmux session on the private server.
func (e *journeyEnv) killSession(t *testing.T, session string) {
	t.Helper()
	cmd := exec.Command("tmux", "kill-session", "-t", session)
	cmd.Env = e.environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux kill-session %s: %v %s", session, err, out)
	}
}

// monitors lists the pids of internal-monitor processes run by this env's
// binary with this env's HOME.
func (e *journeyEnv) monitors() []int {
	entries, _ := os.ReadDir("/proc")
	var pids []int
	for _, entry := range entries {
		var pid int
		if _, err := fmt.Sscanf(entry.Name(), "%d", &pid); err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(args) < 2 || args[1] != "internal-monitor" {
			continue
		}
		environ, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
		if err != nil || !bytes.Contains(environ, []byte("\x00HOME="+e.vars["HOME"]+"\x00")) && !bytes.HasPrefix(environ, []byte("HOME="+e.vars["HOME"]+"\x00")) {
			continue
		}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids
}

// killMonitors stops this env's monitors with SIGINT, as ntm's own signal path
// does, then SIGKILL after 10 seconds, and returns how many there were.
func (e *journeyEnv) killMonitors(t *testing.T) int {
	t.Helper()
	pids := e.monitors()
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGINT)
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return len(pids)
}

// stopDecision is a Stop hook's JSON answer.
type stopDecision struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// requireBlock checks out is exactly one block decision whose reason holds want.
func requireBlock(t *testing.T, out, want string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var d stopDecision
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("hook output isn't JSON: %v\n%q", err, out)
	}
	if dec.More() {
		t.Fatalf("hook output has more than one JSON value: %q", out)
	}
	if d.Decision != "block" || !strings.Contains(d.Reason, want) {
		t.Fatalf("hook decision = %+v, want block with %q", d, want)
	}
}

// ntmStopCommand is the one ntm Stop command in a hook file (Claude's
// settings.local.json or Codex's hooks.json, which share a shape).
func ntmStopCommand(t *testing.T, path string) string {
	t.Helper()
	commands := stopCommands(t, path)
	var found []string
	for _, command := range commands {
		if strings.Contains(command, " agent-hook stop") {
			found = append(found, command)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ntm Stop entries in %s = %q, want exactly 1", path, found)
	}
	return found[0]
}

// stopCommands lists every Stop command in a hook file, in order.
func stopCommands(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("%s isn't JSON: %v", path, err)
	}
	var commands []string
	for _, group := range file.Hooks["Stop"] {
		for _, hook := range group.Hooks {
			commands = append(commands, hook.Command)
		}
	}
	return commands
}

// recipientEvents lists the message ids of the mail events --robot-events
// returns for an agent, run as its own process.
func (e *journeyEnv) recipientEvents(t *testing.T, agent string) []int64 {
	t.Helper()
	out := e.ntm(t, e.root, "--robot-events", "--events-recipient="+agent, "--since-cursor=0", "--events-limit=500")
	var resp struct {
		Success bool `json:"success"`
		Events  []struct {
			MessageID      int64  `json:"message_id"`
			RecipientAgent string `json:"recipient_agent"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil || !resp.Success {
		t.Fatalf("--robot-events: %v\n%s", err, out)
	}
	var ids []int64
	for _, event := range resp.Events {
		if event.MessageID != 0 {
			ids = append(ids, event.MessageID)
		}
	}
	return ids
}

// requireEventsOnce checks --robot-events lists each message once for agent.
func (e *journeyEnv) requireEventsOnce(t *testing.T, agent string, messageIDs ...int64) {
	t.Helper()
	got := e.recipientEvents(t, agent)
	for _, id := range messageIDs {
		count := 0
		for _, g := range got {
			if g == id {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("--robot-events --events-recipient=%s lists message %d %d times (all: %v)", agent, id, count, got)
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// journeyAgentMail is an in-test Agent Mail server, copied from
// internal/cli's fakeAgentMail. It can be stopped and started again on the
// same address, to stand for an outage.
type journeyAgentMail struct {
	t      *testing.T
	addr   string
	mu     sync.Mutex
	server *http.Server
	inbox  map[string][]agentmail.InboxMessage
	tokens map[string]string
}

func newJourneyAgentMail(t *testing.T) *journeyAgentMail {
	t.Helper()
	f := &journeyAgentMail{t: t, inbox: map[string][]agentmail.InboxMessage{}, tokens: map[string]string{}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = listener.Addr().String()
	f.serveOn(listener)
	t.Cleanup(f.stop)
	return f
}

func (f *journeyAgentMail) serveOn(listener net.Listener) {
	server := &http.Server{Handler: http.HandlerFunc(f.serve), ReadHeaderTimeout: 5 * time.Second}
	f.mu.Lock()
	f.server = server
	f.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
}

func (f *journeyAgentMail) url() string {
	return "http://" + f.addr + "/mcp/"
}

// stop closes the server and every open connection.
func (f *journeyAgentMail) stop() {
	f.mu.Lock()
	server := f.server
	f.server = nil
	f.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}

// restart listens again on the same address, with the same inbox and tokens.
func (f *journeyAgentMail) restart(t *testing.T) {
	t.Helper()
	var listener net.Listener
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if listener, err = net.Listen("tcp", f.addr); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatalf("listen again on %s: %v", f.addr, err)
	}
	f.serveOn(listener)
}

func (f *journeyAgentMail) addMessage(agent string, id int, from, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inbox[agent] = append(f.inbox[agent], agentmail.InboxMessage{
		ID: id, From: from, Subject: subject, Importance: "normal", Kind: "to",
		CreatedTS: agentmail.FlexTime{Time: time.Now().UTC()},
	})
}

func (f *journeyAgentMail) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/health") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	var rpc agentmail.JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	params, _ := rpc.Params.(map[string]interface{})
	name, _ := params["name"].(string)
	args, _ := params["arguments"].(map[string]interface{})
	agent, _ := args["agent_name"].(string)
	if agent == "" {
		agent, _ = args["name"].(string)
	}
	token, _ := args["registration_token"].(string)

	f.mu.Lock()
	var result interface{}
	var rpcErr *agentmail.JSONRPCError
	switch name {
	case "health_check":
		result = map[string]interface{}{"status": "ok"}
	case "ensure_project":
		result = map[string]interface{}{"id": 1, "slug": "fake", "human_key": args["human_key"]}
	case "register_agent", "create_agent_identity":
		if agent == "" {
			agent = fmt.Sprintf("Agent%d", len(f.tokens)+1)
		}
		if f.tokens[agent] == "" {
			f.tokens[agent] = "tok-" + agent
		}
		result = map[string]interface{}{"id": len(f.tokens), "name": agent, "registration_token": f.tokens[agent]}
	case "fetch_inbox":
		if want := f.tokens[agent]; want != "" && token != want {
			rpcErr = &agentmail.JSONRPCError{Code: -32000, Message: "registration_token does not match agent " + agent}
			break
		}
		messages := f.inbox[agent]
		if messages == nil {
			messages = []agentmail.InboxMessage{}
		}
		result = map[string]interface{}{"result": messages}
	default:
		result = map[string]interface{}{}
	}
	f.mu.Unlock()

	resp := agentmail.JSONRPCResponse{JSONRPC: "2.0", ID: rpc.ID, Error: rpcErr}
	if rpcErr == nil {
		raw, err := json.Marshal(result)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Result = raw
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(&resp)
}

// standInAgentScript is a stand-in agent, copied from internal/cli's
// screenAgentScript. $1 is the capture folder and $2 the agent type. It records
// its env, redraws a screen file on request, and appends each typed line to
// "typed".
const standInAgentScript = `#!/bin/bash
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

// standInAgents is the capture folder the stand-ins write into, one folder
// per process id.
type standInAgents struct {
	dir string
}

func (a *standInAgents) started() []string {
	entries, _ := os.ReadDir(a.dir)
	type startedAgent struct {
		pid string
		at  time.Time
	}
	var found []startedAgent
	for _, entry := range entries {
		if info, err := os.Stat(filepath.Join(a.dir, entry.Name(), "started")); err == nil {
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

func (a *standInAgents) waitStarted(t *testing.T, count int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if pids := a.started(); len(pids) >= count {
			return pids
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d stand-in agents started within %s, want %d", len(a.started()), within, count)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// setScreen shows a saved capture from internal/agent/testdata in the pane.
func (a *standInAgents) setScreen(t *testing.T, pid, fixture string) {
	t.Helper()
	wd, _ := os.Getwd()
	repo, err := findRepoRoot(wd)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(repo, "internal", "agent", "testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	if err := os.WriteFile(filepath.Join(a.dir, pid, "screen"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	redraw := filepath.Join(a.dir, pid, "redraw")
	if err := os.WriteFile(redraw, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(redraw); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stand-in %s didn't redraw within 5s", pid)
		}
	}
}

func (a *standInAgents) typed(t *testing.T, pid string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(a.dir, pid, "typed"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(content)
}

// waitTyped waits until the agent's typed file holds want, then until it stays
// the same for 2 seconds (the double Enter that ends a send), and returns it.
func (a *standInAgents) waitTyped(t *testing.T, pid, want string, within time.Duration) string {
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

// firstTyped waits until the agent's typed file holds want and returns how
// long that took.
func (a *standInAgents) firstTyped(t *testing.T, pid, want string, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for !strings.Contains(a.typed(t, pid), want) {
		if time.Since(start) > within {
			t.Fatalf("stand-in %s: no %q typed within %s (typed %q)", pid, want, within, a.typed(t, pid))
		}
		time.Sleep(50 * time.Millisecond)
	}
	return time.Since(start)
}

// agentEnv returns one variable from the stand-in's environment at launch.
func (a *standInAgents) agentEnv(t *testing.T, pid, key string) string {
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
