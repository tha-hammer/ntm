package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard"
)

// Closure tests in this file cross real process boundaries: they re-exec the
// test binary as helper processes that go through ntm's real startup. They never
// skip. A helper that fails, or output with nothing to assert on, fails the test.

const (
	closureHelperModeEnv    = "NTM_CLOSURE_HELPER"
	closureHelperArgsEnv    = "NTM_CLOSURE_ARGS"
	closureHelperProjectEnv = "NTM_CLOSURE_PROJECT"
	closureHelperArgSep     = "\x1f"

	// closureClosedAgentMailURL points at a port nothing listens on, so a helper
	// can never reach a real Agent Mail server on the machine running the tests.
	closureClosedAgentMailURL = "http://127.0.0.1:1/mcp/"

	closureMailSubject = "durable feed test"
)

// closureEnv is an isolated config, home and working folder for helper processes.
type closureEnv struct {
	configDir string
	workDir   string
	vars      map[string]string
}

func newClosureEnv(t *testing.T) *closureEnv {
	t.Helper()
	root := t.TempDir()
	env := &closureEnv{
		configDir: filepath.Join(root, "config"),
		workDir:   filepath.Join(root, "work"),
	}
	for _, dir := range []string{env.configDir, env.workDir, filepath.Join(root, "home"), filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	env.vars = map[string]string{
		"HOME":                  filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":       env.configDir,
		"XDG_DATA_HOME":         filepath.Join(root, "data"),
		"NTM_CONFIG":            "",
		"AGENT_MAIL_URL":        closureClosedAgentMailURL,
		closureHelperProjectEnv: env.workDir,
	}
	return env
}

func (e *closureEnv) set(key, value string) {
	e.vars[key] = value
}

func (e *closureEnv) stateDB() string {
	return filepath.Join(e.configDir, "ntm", "state.db")
}

// tryRun starts one helper process and returns its stdout and any failure.
func (e *closureEnv) tryRun(mode string, args ...string) (string, error) {
	cmd := exec.Command(os.Args[0])
	cmd.Dir = e.workDir
	cmd.Env = append(os.Environ(),
		closureHelperModeEnv+"="+mode,
		closureHelperArgsEnv+"="+strings.Join(args, closureHelperArgSep),
	)
	for key, value := range e.vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("helper %q: %w\nstderr: %s", mode, err, stderr.String())
	}
	return stdout.String(), nil
}

func (e *closureEnv) run(t *testing.T, mode string, args ...string) string {
	t.Helper()
	out, err := e.tryRun(mode, args...)
	if err != nil {
		t.Fatalf("%v\nstdout: %s", err, out)
	}
	return out
}

type closureRobotEvent struct {
	Cursor  int64  `json:"cursor"`
	Type    string `json:"type"`
	Summary string `json:"summary"`
}

// parseRobotEvents requires the events array to be present, so output that
// isn't a robot response can't pass as an empty result.
func parseRobotEvents(t *testing.T, out string) []closureRobotEvent {
	t.Helper()
	var response struct {
		Success *bool                `json:"success"`
		Events  *[]closureRobotEvent `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("parse robot output: %v\noutput: %s", err, out)
	}
	if response.Success == nil || !*response.Success || response.Events == nil {
		t.Fatalf("robot response has no successful events array: %s", out)
	}
	return *response.Events
}

// runClosureHelper is the entry point for helper processes, called from TestMain.
func runClosureHelper(mode string) int {
	args := []string{}
	if raw := os.Getenv(closureHelperArgsEnv); raw != "" {
		args = strings.Split(raw, closureHelperArgSep)
	}
	switch mode {
	case "publish-dashboard-mail":
		return closureHelperPublishDashboardMail()
	case "execute":
		return closureHelperExecute(args)
	case "append-old-events":
		return closureHelperAppendOldEvents(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown closure helper mode %q\n", mode)
		return 2
	}
}

// closureHelperPublishDashboardMail goes through ntm's real startup for the
// dashboard command, then hands the real model a mail summary, as its poll does.
func closureHelperPublishDashboardMail() int {
	os.Args = []string{"ntm", "dashboard"}
	if err := rootCmd.PersistentPreRunE(newDashboardCmd(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeRobotPersistence()

	project := os.Getenv(closureHelperProjectEnv)
	model := dashboard.New("closure", project)
	model.Update(dashboard.AgentMailInboxSummaryMsg{
		ProjectKey: project,
		Inboxes: map[string][]agentmail.InboxMessage{
			"%1": {{ID: 42, From: "RedStone", Subject: closureMailSubject}},
		},
		AgentMap: map[string]string{"%1": "BlueLake"},
		Gen:      1,
	})
	return 0
}

// closureHelperAppendOldEvents goes through real startup, appends count events
// dated age in the past to the process's feed, and prints their cursors as JSON.
func closureHelperAppendOldEvents(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "append-old-events needs an age and a count")
		return 2
	}
	age, err := time.ParseDuration(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	count, err := strconv.Atoi(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	os.Args = []string{"ntm", "dashboard"}
	if err := rootCmd.PersistentPreRunE(newDashboardCmd(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeRobotPersistence()

	cursors := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		event := robot.GetAttentionFeed().Append(robot.AttentionEvent{
			Session:       "closure",
			Category:      robot.EventCategorySystem,
			Type:          robot.EventTypeSystemHealthChange,
			Source:        "closure-test",
			Actionability: robot.ActionabilityBackground,
			Severity:      robot.SeverityInfo,
			Summary:       fmt.Sprintf("old event %d", i),
			Ts:            time.Now().Add(-age).UTC().Format(time.RFC3339Nano),
		})
		cursors = append(cursors, event.Cursor)
	}
	if err := json.NewEncoder(os.Stdout).Encode(cursors); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// closureHelperExecute runs a real ntm command line through Execute.
func closureHelperExecute(args []string) int {
	os.Args = append([]string{"ntm"}, args...)
	if err := Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestClosure_DashboardMailReadableFromAnotherProcess(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-dashboard-mail")

	out := env.run(t, "execute", "--robot-events", "--events-category=mail")
	for _, event := range parseRobotEvents(t, out) {
		if event.Type == "mail.received" && strings.Contains(event.Summary, closureMailSubject) {
			return
		}
	}
	t.Fatalf("mail.received not readable from another process; output: %s", out)
}

func TestClosure_OldCursorNotExpired(t *testing.T) {
	env := newClosureEnv(t)
	var cursors []int64
	out := env.run(t, "append-old-events", "2h", "2")
	if err := json.Unmarshal([]byte(out), &cursors); err != nil {
		t.Fatalf("parse cursors: %v\noutput: %s", err, out)
	}
	if len(cursors) != 2 {
		t.Fatalf("cursors = %v, want 2", cursors)
	}

	out, err := env.tryRun("execute", "--robot-events", fmt.Sprintf("--since-cursor=%d", cursors[0]))
	if strings.Contains(out, "CURSOR_EXPIRED") {
		t.Fatalf("cursor expired: %s", out)
	}
	if err != nil {
		t.Fatalf("%v\nstdout: %s", err, out)
	}
	for _, event := range parseRobotEvents(t, out) {
		if event.Cursor == cursors[1] {
			return
		}
	}
	t.Fatalf("second event not returned: %s", out)
}

func TestClosure_VersionDoesNotOpenStore(t *testing.T) {
	env := newClosureEnv(t)
	out := env.run(t, "execute", "version")
	if strings.TrimSpace(out) == "" {
		t.Fatal("ntm version printed nothing, so the helper may not have run it")
	}
	if _, err := os.Stat(env.stateDB()); !os.IsNotExist(err) {
		t.Fatalf("state.db exists after ntm version: %v", err)
	}
}

func TestClosure_DashboardFeedRunsNoProjectionRefresh(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	env := newClosureEnv(t)
	env.set("AGENT_MAIL_URL", server.URL+"/mcp/")
	env.run(t, "publish-dashboard-mail")
	if got := calls.Load(); got != 0 {
		t.Fatalf("dashboard path made %d Agent Mail calls, want 0", got)
	}

	// A zero count only means something if the event really was published.
	env.set("AGENT_MAIL_URL", closureClosedAgentMailURL)
	out := env.run(t, "execute", "--robot-events", "--events-category=mail")
	if len(parseRobotEvents(t, out)) == 0 {
		t.Fatalf("no mail event was published, so the zero call count proves nothing: %s", out)
	}
}

func TestMain_IsolatesStateDB(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	realConfig := filepath.Join(home, ".config") + string(filepath.Separator)
	if path := state.DefaultPath(); strings.HasPrefix(path, realConfig) {
		t.Fatalf("CLI tests would use the real state.db: %s", path)
	}
}

func TestAttentionStoreOpenerWanted(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"no arguments", nil, true},
		{"dashboard", []string{"dashboard"}, true},
		{"robot status", []string{"--robot-status"}, true},
		{"overlay", []string{"--robot-overlay"}, false},
		{"overlay with value", []string{"--robot-overlay=x"}, false},
		{"overlay among others", []string{"--json", "--robot-overlay"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := attentionStoreOpenerWanted(tc.args); got != tc.want {
				t.Fatalf("attentionStoreOpenerWanted(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestProcessStateStore_ClosesAndReopens(t *testing.T) {
	closeRobotPersistence()
	t.Cleanup(closeRobotPersistence)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("NTM_CONFIG", "")

	first, err := openProcessStateStore()
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	again, err := openProcessStateStore()
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if again != first {
		t.Fatal("a second open in one process should return the same store")
	}

	closeRobotPersistence()
	if err := first.DB().Ping(); err == nil {
		t.Fatal("store is still open after closeRobotPersistence")
	}

	reopened, err := openProcessStateStore()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened == first {
		t.Fatal("reopen returned the closed store")
	}
	if err := reopened.DB().Ping(); err != nil {
		t.Fatalf("reopened store doesn't work: %v", err)
	}

	closeRobotPersistence()
	closeRobotPersistence() // Closing with nothing open does nothing.
}
