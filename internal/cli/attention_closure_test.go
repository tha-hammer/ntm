package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentmail"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/serve"
	"github.com/Dicklesworthstone/ntm/internal/state"
	"github.com/Dicklesworthstone/ntm/internal/tui/dashboard"
	"github.com/gorilla/websocket"
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

	closureContentionSession = "contention"

	closureInboxArgSep = "|"
)

// closureEnv is an isolated config, home and working folder for helper processes.
type closureEnv struct {
	configDir string
	workDir   string
	vars      map[string]string
	binary    string // the helper binary to run; "" means os.Args[0]
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
		"CODEX_HOME":            filepath.Join(root, "codex-home"),
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

// closureRun is one finished helper process.
type closureRun struct {
	stdout, stderr string
	code           int
	err            error
}

// helperCommand builds one helper process with the environment's variables.
func (e *closureEnv) helperCommand(mode, stdin string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	binary := os.Args[0]
	if e.binary != "" {
		binary = e.binary
	}
	cmd := exec.Command(binary)
	cmd.Dir = e.workDir
	cmd.Env = append(os.Environ(),
		closureHelperModeEnv+"="+mode,
		closureHelperArgsEnv+"="+strings.Join(args, closureHelperArgSep),
	)
	for key, value := range e.vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	return cmd, &stdout, &stderr
}

// finishRun waits for a started helper and records how it ended.
func finishRun(mode string, cmd *exec.Cmd, stdout, stderr *bytes.Buffer) closureRun {
	err := cmd.Wait()
	run := closureRun{stdout: stdout.String(), stderr: stderr.String(), code: cmd.ProcessState.ExitCode()}
	if err != nil {
		run.err = fmt.Errorf("helper %q: %w\nstderr: %s", mode, err, run.stderr)
	}
	return run
}

// runHelper starts one helper process with stdin and waits for it.
func (e *closureEnv) runHelper(mode, stdin string, args ...string) closureRun {
	cmd, stdout, stderr := e.helperCommand(mode, stdin, args...)
	if err := cmd.Start(); err != nil {
		return closureRun{code: -1, err: fmt.Errorf("start helper %q: %w", mode, err)}
	}
	return finishRun(mode, cmd, stdout, stderr)
}

// runParallel starts count helpers before waiting for any, and fails the test
// if any of them fails. It returns each helper's stdout.
func (e *closureEnv) runParallel(t *testing.T, count int, stdin, mode string, args ...string) []string {
	t.Helper()
	runs := make([]closureRun, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		cmd, stdout, stderr := e.helperCommand(mode, stdin, args...)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start helper %d: %v", i, err)
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runs[i] = finishRun(mode, cmd, stdout, stderr)
		}(i)
	}
	wg.Wait()
	outs := make([]string, count)
	for i, run := range runs {
		if run.err != nil {
			t.Errorf("helper %d: %v\nstdout: %s", i, run.err, run.stdout)
		}
		outs[i] = run.stdout
	}
	if t.Failed() {
		t.FailNow()
	}
	return outs
}

// tryRun starts one helper process and returns its stdout and any failure.
func (e *closureEnv) tryRun(mode string, args ...string) (string, error) {
	run := e.runHelper(mode, "", args...)
	return run.stdout, run.err
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
	Session string `json:"session"`
	Type    string `json:"type"`
	Summary string `json:"summary"`

	RecipientProject string `json:"recipient_project"`
	RecipientAgent   string `json:"recipient_agent"`
	MessageID        int64  `json:"message_id"`
}

// readAllCursors pages through --robot-events in new processes and returns the
// cursors of one session's events. The reader's own startup can add other events.
func readAllCursors(t *testing.T, env *closureEnv, session string) []int64 {
	t.Helper()
	const maxPages = 100
	cursors := []int64{}
	since := int64(0)
	for page := 0; page < maxPages; page++ {
		out := env.run(t, "execute", "--robot-events", fmt.Sprintf("--since-cursor=%d", since), "--events-limit=100")
		for _, event := range parseRobotEvents(t, out) {
			if event.Session == session {
				cursors = append(cursors, event.Cursor)
			}
		}
		var paging struct {
			NextCursor int64 `json:"next_cursor"`
			HasMore    bool  `json:"has_more"`
		}
		if err := json.Unmarshal([]byte(out), &paging); err != nil {
			t.Fatalf("parse paging fields: %v\noutput: %s", err, out)
		}
		if !paging.HasMore {
			return cursors
		}
		since = paging.NextCursor
	}
	t.Fatalf("still more events after %d pages", maxPages)
	return nil
}

func allDistinct(cursors []int64) bool {
	seen := make(map[int64]struct{}, len(cursors))
	for _, cursor := range cursors {
		if _, duplicate := seen[cursor]; duplicate {
			return false
		}
		seen[cursor] = struct{}{}
	}
	return true
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
	case "append-events":
		return closureHelperAppendEvents(args)
	case "publish-inbox":
		return closureHelperPublishInbox(args)
	case "seed-registry":
		return closureHelperSeedRegistry(args)
	case "load-registry":
		return closureHelperLoadRegistry(args)
	case "argv":
		// A hook command names this binary, so its own arguments are the
		// ntm command line, as they are for the real binary.
		return closureHelperExecute(os.Args[1:])
	case "sleep":
		// Stands in for a process by its command line only, such as a monitor
		// started as "<binary> internal-monitor <session>".
		time.Sleep(60 * time.Second)
		return 0
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

// closureHelperAppendEvents goes through real startup and appends count events.
// It exits non-zero if its feed ended up memory-only, which is how a silent drop
// to memory shows up in the parent.
func closureHelperAppendEvents(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "append-events needs a count")
		return 2
	}
	count, err := strconv.Atoi(args[0])
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

	feed := robot.GetAttentionFeed()
	for i := 0; i < count; i++ {
		feed.Append(robot.AttentionEvent{
			Session:       closureContentionSession,
			Category:      robot.EventCategorySystem,
			Type:          robot.EventTypeSystemHealthChange,
			Source:        "closure-test",
			Actionability: robot.ActionabilityBackground,
			Severity:      robot.SeverityInfo,
			Summary:       fmt.Sprintf("writer %d event %d", os.Getpid(), i),
		})
	}
	if health := feed.StoreHealth(); !health.Durable {
		fmt.Fprintf(os.Stderr, "feed is memory-only: %+v\n", health)
		return 1
	}
	return 0
}

// closureHelperPublishInbox goes through ntm's real startup for the dashboard
// command, then hands the real model one poll's inboxes. Each arg is
// "<message id>|<agent>|<subject>". It fails if the feed ended up memory-only.
func closureHelperPublishInbox(args []string) int {
	os.Args = []string{"ntm", "dashboard"}
	if err := rootCmd.PersistentPreRunE(newDashboardCmd(), nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer closeRobotPersistence()

	inboxes := map[string][]agentmail.InboxMessage{}
	agents := map[string]string{}
	for _, arg := range args {
		parts := strings.SplitN(arg, closureInboxArgSep, 3)
		if len(parts) != 3 {
			fmt.Fprintf(os.Stderr, "bad inbox arg %q\n", arg)
			return 2
		}
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "bad inbox arg %q: %v\n", arg, err)
			return 2
		}
		paneID := "%" + parts[1]
		agents[paneID] = parts[1]
		inboxes[paneID] = append(inboxes[paneID], agentmail.InboxMessage{ID: id, From: "Sender", Subject: parts[2]})
	}

	project := os.Getenv(closureHelperProjectEnv)
	model := dashboard.New("closure", project)
	model.Update(dashboard.AgentMailInboxSummaryMsg{
		ProjectKey: project,
		Inboxes:    inboxes,
		AgentMap:   agents,
		Gen:        1,
	})
	if health := robot.GetAttentionFeed().StoreHealth(); !health.Durable {
		fmt.Fprintf(os.Stderr, "feed is memory-only: %+v\n", health)
		return 1
	}
	return 0
}

func inboxArg(id int, agent, subject string) string {
	return strings.Join([]string{strconv.Itoa(id), agent, subject}, closureInboxArgSep)
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

func TestClosure_EventsRecipientListsOneAgent(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", inboxArg(41, "BlueLake", "for blue"), inboxArg(42, "RedStone", "for red"))

	for _, name := range []string{"RedStone", "redstone"} {
		events := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient="+name))
		if len(events) != 1 {
			t.Fatalf("%s: got %d events: %+v", name, len(events), events)
		}
		got := events[0]
		if got.RecipientAgent != "RedStone" || got.RecipientProject != env.workDir || got.MessageID != 42 || !strings.Contains(got.Summary, "for red") {
			t.Fatalf("%s: event = %+v", name, got)
		}
	}

	filtered := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=RedStone", "--events-category=agent"))
	if len(filtered) != 0 {
		t.Fatalf("category filter ignored: %+v", filtered)
	}
}

func TestClosure_BusyFeedKeepsRecipientEvents(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", inboxArg(42, "RedStone", "for red"))
	env.run(t, "append-events", "500")

	events := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient=RedStone", "--events-limit=10"))
	if len(events) != 1 || events[0].MessageID != 42 {
		t.Fatalf("events = %+v", events)
	}
}

func TestClosure_UnknownRecipientReturnsEmptyList(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", inboxArg(42, "RedStone", "for red"))

	out := env.run(t, "execute", "--robot-events", "--events-recipient=NoSuchAgent")
	var response struct {
		Success bool            `json:"success"`
		Events  json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}
	if !response.Success || string(response.Events) != "[]" {
		t.Fatalf("response = %s", out)
	}

	all := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-recipient= "))
	if len(all) == 0 {
		t.Fatal("a blank recipient should mean no filter")
	}
}

func closureInboxOfTen() []string {
	args := make([]string, 0, 10)
	for id := 1; id <= 10; id++ {
		args = append(args, inboxArg(id, "RedStone", fmt.Sprintf("message %d", id)))
	}
	return args
}

// readRecipientCursors pages --events-recipient in new processes and returns
// the cursors in order.
func readRecipientCursors(t *testing.T, env *closureEnv, agent string) []int64 {
	t.Helper()
	const maxPages = 20
	cursors := []int64{}
	since := int64(0)
	for page := 0; page < maxPages; page++ {
		out := env.run(t, "execute", "--robot-events", "--events-recipient="+agent,
			fmt.Sprintf("--since-cursor=%d", since), "--events-limit=5")
		for _, event := range parseRobotEvents(t, out) {
			cursors = append(cursors, event.Cursor)
		}
		var paging struct {
			NextCursor int64 `json:"next_cursor"`
			HasMore    bool  `json:"has_more"`
		}
		if err := json.Unmarshal([]byte(out), &paging); err != nil {
			t.Fatalf("parse paging fields: %v\noutput: %s", err, out)
		}
		if !paging.HasMore {
			return cursors
		}
		since = paging.NextCursor
	}
	t.Fatalf("still more events after %d pages", maxPages)
	return nil
}

func TestClosure_DashboardRestartWritesNoNewEvents(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", closureInboxOfTen()...)
	before := readRecipientCursors(t, env, "RedStone")
	if len(before) != 10 {
		t.Fatalf("first process recorded %d events, want 10", len(before))
	}

	env.run(t, "publish-inbox", closureInboxOfTen()...)
	after := readRecipientCursors(t, env, "RedStone")
	if !slices.Equal(after, before) {
		t.Fatalf("cursors changed after restart: %v -> %v", before, after)
	}
}

func TestClosure_ManyProcessesPublishOneMessage(t *testing.T) {
	const writers = 8
	env := newClosureEnv(t)
	env.runParallel(t, writers, "", "publish-inbox", closureInboxOfTen()...)

	cursors := readRecipientCursors(t, env, "RedStone")
	if len(cursors) != 10 || !allDistinct(cursors) {
		t.Fatalf("cursors = %v, want 10 distinct", cursors)
	}
}

func TestClosure_NewMessageAfterRestartGetsOneEvent(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-inbox", closureInboxOfTen()...)
	before := readRecipientCursors(t, env, "RedStone")

	env.run(t, "publish-inbox", append(closureInboxOfTen(), inboxArg(11, "RedStone", "new one"))...)
	after := readRecipientCursors(t, env, "RedStone")
	if len(after) != 11 || !slices.Equal(after[:10], before) || after[10] <= before[len(before)-1] {
		t.Fatalf("cursors %v -> %v", before, after)
	}
}

func TestSendLogsToFile(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	previousSlog := slog.Default()
	previousWriter := log.Writer()
	previousFlags := log.Flags()

	restore := sendLogsToFile("logsess-dashboard.log")
	slog.Warn("structured line for the file")
	log.Print("plain line for the file")
	restore()

	if slog.Default() != previousSlog {
		t.Fatal("slog default wasn't restored")
	}
	if log.Writer() != previousWriter || log.Flags() != previousFlags {
		t.Fatal("log package output wasn't restored")
	}
	data, err := os.ReadFile(filepath.Join(resilience.LogDir(), "logsess-dashboard.log"))
	if err != nil {
		t.Fatalf("read dashboard log: %v", err)
	}
	for _, want := range []string{"structured line for the file", "plain line for the file"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("dashboard log lacks %q: %s", want, data)
		}
	}
}

func TestSendLogsToFile_UnwritableDirStaysOffScreen(t *testing.T) {
	// A file where the log folder should go makes the log file impossible to open.
	dataDir := t.TempDir()
	blocker := filepath.Join(dataDir, "ntm")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	t.Setenv("XDG_DATA_HOME", dataDir)
	previousWriter := log.Writer()

	restore := sendLogsToFile("logsess-dashboard.log")
	defer restore()
	if log.Writer() == previousWriter {
		t.Fatal("log output still goes to the terminal, which would garble the screen")
	}
}

func TestClosure_ManyWritersOneStateDB(t *testing.T) {
	const (
		writers         = 8
		eventsPerWriter = 50
	)
	env := newClosureEnv(t)
	var wg sync.WaitGroup
	errs := make(chan string, writers)
	start := time.Now()
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := env.tryRun("append-events", strconv.Itoa(eventsPerWriter)); err != nil {
				errs <- fmt.Sprintf("%v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	writeTime := time.Since(start)
	close(errs)
	for failure := range errs {
		t.Errorf("writer failed: %s", failure)
	}

	cursors := readAllCursors(t, env, closureContentionSession)
	t.Logf("%d writers, %d events, written in %s", writers, len(cursors), writeTime)
	if len(cursors) != writers*eventsPerWriter || !allDistinct(cursors) {
		t.Fatalf("got %d events (distinct: %v), want %d distinct", len(cursors), allDistinct(cursors), writers*eventsPerWriter)
	}
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

// closureServe is a running `ntm serve` helper process.
type closureServe struct {
	baseURL string
}

// freeLocalPort returns a port that was free on 127.0.0.1 a moment ago.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// startServe runs `ntm serve` on a free port as a helper process and waits until
// it answers. Cleanup sends SIGINT, then kills the process after 5 seconds.
func (e *closureEnv) startServe(t *testing.T) *closureServe {
	t.Helper()
	port := freeLocalPort(t)
	cmd, _, stderr := e.helperCommand("execute", "", "serve", "--port", strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})

	serve := &closureServe{baseURL: fmt.Sprintf("http://127.0.0.1:%d", port)}
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-exited:
			t.Fatalf("serve exited before answering: %v\nstderr: %s", err, stderr.String())
		default:
		}
		resp, err := http.Get(serve.baseURL + "/api/v1/attention/events")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return serve
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("serve didn't answer within 10s (last error %v)", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// eventReader collects attention events that a background reader receives.
type eventReader struct {
	events chan robot.AttentionEvent
}

// next returns the next event that matches, and fails the test at the deadline.
// Events that don't match are dropped.
func (r *eventReader) next(t *testing.T, match func(robot.AttentionEvent) bool, within time.Duration) robot.AttentionEvent {
	t.Helper()
	timeout := time.After(within)
	for {
		select {
		case event, ok := <-r.events:
			if !ok {
				t.Fatal("stream ended while waiting for an event")
			}
			if match(event) {
				return event
			}
		case <-timeout:
			t.Fatalf("no matching event within %s", within)
		}
	}
}

// none fails the test if a matching event arrives within the wait.
func (r *eventReader) none(t *testing.T, match func(robot.AttentionEvent) bool, wait time.Duration) {
	t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case event, ok := <-r.events:
			if !ok {
				t.Fatal("stream ended early")
			}
			if match(event) {
				t.Fatalf("unexpected event: %+v", event)
			}
		case <-timeout:
			return
		}
	}
}

// openAttentionStream reads the server's SSE attention stream in the background,
// and returns once the server has sent its connected event, which it sends after
// subscribing to the feed.
func (s *closureServe) openAttentionStream(t *testing.T, sinceCursor int64) *eventReader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url := fmt.Sprintf("%s/api/v1/attention/stream?since_cursor=%d", s.baseURL, sinceCursor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("open stream: status %d", resp.StatusCode)
	}

	reader := &eventReader{events: make(chan robot.AttentionEvent, 1000)}
	connected := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		defer close(reader.events)
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		name := ""
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && name == "connected":
				close(connected)
			case strings.HasPrefix(line, "data: ") && name == "attention":
				var event robot.AttentionEvent
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
					reader.events <- event
				}
			}
		}
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("stream sent no connected event within 5s")
	}
	return reader
}

// openAttentionSocket connects to the server's WebSocket, subscribes to the
// attention topic from now, and returns once the server acknowledges it.
func (s *closureServe) openAttentionSocket(t *testing.T) *eventReader {
	t.Helper()
	url := "ws" + strings.TrimPrefix(s.baseURL, "http") + "/api/v1/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	subscribe := serve.WSMessage{
		Type:      serve.WSMsgSubscribe,
		RequestID: "closure-sub",
		Data:      map[string]interface{}{"topics": []string{"attention"}, "since_cursor": -1},
	}
	if err := conn.WriteJSON(subscribe); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	reader := &eventReader{events: make(chan robot.AttentionEvent, 1000)}
	acked := make(chan struct{})
	go func() {
		defer close(reader.events)
		for {
			var frame struct {
				Type serve.WSMessageType `json:"type"`
				Data json.RawMessage     `json:"data"`
			}
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			switch frame.Type {
			case serve.WSMsgAck:
				select {
				case <-acked:
				default:
					close(acked)
				}
			case serve.WSMsgEvent:
				var event robot.AttentionEvent
				if json.Unmarshal(frame.Data, &event) == nil {
					reader.events <- event
				}
			}
		}
	}()
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("no subscribe acknowledgement within 5s")
	}
	return reader
}

func isMail42(event robot.AttentionEvent) bool {
	return event.Type == robot.EventTypeMailReceived && event.MessageID == 42
}

// Behavior 1: a dashboard in another process writes a mail event, and the
// `ntm serve` stream shows it once.
func TestClosure_ServeStreamsOtherProcessMail(t *testing.T) {
	env := newClosureEnv(t)
	env.privateTmux(t)
	serve := env.startServe(t)
	stream := serve.openAttentionStream(t, -1)

	env.run(t, "publish-dashboard-mail")

	got := stream.next(t, isMail42, 5*time.Second)
	stream.none(t, isMail42, 3*time.Second)

	stored := parseRobotEvents(t, env.run(t, "execute", "--robot-events", "--events-category=mail"))
	if len(stored) != 1 || stored[0].Cursor != got.Cursor {
		t.Fatalf("streamed cursor %d, stored %+v", got.Cursor, stored)
	}
}

// Behavior 2: a WebSocket client on the attention topic gets another
// process's mail event once.
func TestClosure_ServeWebSocketGetsOtherProcessMail(t *testing.T) {
	env := newClosureEnv(t)
	env.privateTmux(t)
	serve := env.startServe(t)
	socket := serve.openAttentionSocket(t)

	env.run(t, "publish-dashboard-mail")

	socket.next(t, isMail42, 5*time.Second)
	socket.none(t, isMail42, 3*time.Second)
}

// Behavior 4: events from writers running at once reach the stream once each,
// in cursor order, and match what was stored.
func TestClosure_ServeStreamsParallelWritersInOrder(t *testing.T) {
	const writers, each = 4, 20 // 80 stays under the SSE handler's 100-event buffer
	env := newClosureEnv(t)
	env.privateTmux(t)
	serve := env.startServe(t)
	stream := serve.openAttentionStream(t, -1)

	env.runParallel(t, writers, "", "append-events", strconv.Itoa(each))

	inSession := func(event robot.AttentionEvent) bool { return event.Session == closureContentionSession }
	got := make([]int64, 0, writers*each)
	for len(got) < writers*each {
		got = append(got, stream.next(t, inSession, 10*time.Second).Cursor)
	}
	stream.none(t, inSession, 2*time.Second)
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("streamed cursors out of order: %v", got)
		}
	}
	if want := readAllCursors(t, env, closureContentionSession); !slices.Equal(got, want) {
		t.Fatalf("streamed %v, stored %v", got, want)
	}
}

// Behavior 7 support check: short commands on an existing state.db exit
// promptly. No short command subscribes, so none starts the poll.
func TestClosure_ShortCommandsExitPromptly(t *testing.T) {
	env := newClosureEnv(t)
	env.run(t, "publish-dashboard-mail") // creates state.db with an event in it
	for _, args := range [][]string{
		{"execute", "--robot-events"},
		{"execute", "agent-hook", "stop", "--project", env.workDir},
	} {
		start := time.Now()
		run := env.runHelper(args[0], "{}", args[1:]...)
		if run.err != nil {
			t.Fatalf("%v: %v", args[1:], run.err)
		}
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("%v took %s, want under 2s", args[1:], took)
		}
	}
}
