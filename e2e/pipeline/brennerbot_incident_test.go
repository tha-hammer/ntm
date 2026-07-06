package pipeline

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pipelinepkg "github.com/Dicklesworthstone/ntm/internal/pipeline"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestBrennerbotIncidentPipelineRunsEndToEndAgainstMocks(t *testing.T) {
	const (
		sessionName          = "incident-session"
		configuredWallBudget = 5 * time.Second
	)

	workflowPath := filepath.Join("testdata", "brennerbot-incident.yaml")
	workflow, validation, err := pipelinepkg.LoadAndValidate(workflowPath)
	if err != nil {
		t.Fatalf("LoadAndValidate() error = %v", err)
	}
	if !validation.Valid {
		t.Fatalf("workflow validation failed: %+v", validation.Errors)
	}

	workspace := copyIncidentWorkspaceFixture(t)
	mockTmux := pipelinepkg.NewMockTmuxClient()
	mockTmux.AddPane(sessionName, tmux.Pane{
		ID:       "%1",
		Index:    1,
		NTMIndex: 1,
		Title:    sessionName + "__cc_1[triage]",
		Type:     tmux.AgentClaude,
		Tags:     []string{"triage"},
	})
	mockTmux.AddPane(sessionName, tmux.Pane{
		ID:       "%2",
		Index:    2,
		NTMIndex: 2,
		Title:    sessionName + "__cod_1[evidence]",
		Type:     tmux.AgentCodex,
		Tags:     []string{"evidence"},
	})
	scripter := pipelinepkg.NewAgentScripter().
		Match("H-001", "MO-04a result: H-001 evidence captured\n").
		Match("H-002", "MO-04a result: H-002 evidence captured\n")
	mockTmux.SetAgentScripter(scripter)
	t.Cleanup(mockTmux.Reset)

	var logBuf bytes.Buffer
	restoreLogs := captureSlog(t, &logBuf)
	defer restoreLogs()

	cfg := pipelinepkg.DefaultExecutorConfig(sessionName)
	cfg.ProjectDir = workspace
	cfg.WorkflowFile = workflowPath
	cfg.DefaultTimeout = time.Second
	cfg.GlobalTimeout = configuredWallBudget
	cfg.RunID = "e2e-brennerbot-incident"

	executor := pipelinepkg.NewExecutor(cfg)
	executor.SetTmuxClient(mockTmux)

	started := time.Now()
	state, err := executor.Run(context.Background(), workflow, nil, nil)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if state.Status != pipelinepkg.StatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, pipelinepkg.StatusCompleted)
	}
	if elapsed > 5*configuredWallBudget {
		t.Fatalf("run took %s, want under %s", elapsed, 5*configuredWallBudget)
	}

	assertTopLevelStepsCompleted(t, state, workflow.Steps)
	assertStepCompleted(t, state, "phase_4_inline_dispatch_iter0_template")
	assertStepCompleted(t, state, "phase_4_inline_dispatch_iter1_template")
	assertFileContains(t, filepath.Join(workspace, ".brenner_workspace", "phase_3_complete.flag"), "ready")
	assertFileContains(t, filepath.Join(workspace, "deliverables", "INCIDENT-VERDICT.md"), "MO-04a")

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scripter.Wait(waitCtx, 2); err != nil {
		t.Fatalf("scripted agent responses were not produced: %v", err)
	}
	assertDispatchHistory(t, mockTmux, "%1", "MO-04a Investigate", "H-001")
	assertDispatchHistory(t, mockTmux, "%2", "MO-04a Investigate", "H-002")
	assertPaneOutputContains(t, mockTmux, "%1", "MO-04a result: H-001 evidence captured")
	assertPaneOutputContains(t, mockTmux, "%2", "MO-04a result: H-002 evidence captured")
	assertDispatchGolden(t, workspace, filepath.Join("testdata", "goldens", "brennerbot-incident-dispatch.golden"))

	events := parseJSONLEvents(t, &logBuf)
	assertLogEvent(t, events, "command step starting", "step_id", "phase_0_scope", "agent_type", "command")
	assertLogEvent(t, events, "foreach step starting", "step_id", "phase_4_inline_dispatch", "agent_type", "foreach")
	assertLogEvent(t, events, "template step starting", "step_id", "phase_4_inline_dispatch_iter0_template", "pane_id", "%1")
	assertLogEvent(t, events, "template step starting", "step_id", "phase_4_inline_dispatch_iter1_template", "pane_id", "%2")
	assertLogEvent(t, events, "foreach step completed", "step_id", "phase_4_inline_dispatch", "dispatched", "2", "failed", "0")
}

func copyIncidentWorkspaceFixture(t *testing.T) string {
	t.Helper()

	workspace := t.TempDir()
	source := filepath.Join("testdata", "brennerbot-fixtures", "incident-workspace", ".brenner_workspace", "phase0_scope_decision.md")
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture %q: %v", source, err)
	}

	destDir := filepath.Join(workspace, ".brenner_workspace")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("create workspace fixture dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "phase0_scope_decision.md"), content, 0o644); err != nil {
		t.Fatalf("write workspace fixture: %v", err)
	}
	return workspace
}

func assertTopLevelStepsCompleted(t *testing.T, state *pipelinepkg.ExecutionState, steps []pipelinepkg.Step) {
	t.Helper()

	for _, step := range steps {
		assertStepCompleted(t, state, step.ID)
	}
}

func assertStepCompleted(t *testing.T, state *pipelinepkg.ExecutionState, stepID string) {
	t.Helper()

	result, ok := state.Steps[stepID]
	if !ok {
		t.Fatalf("state missing step %q", stepID)
	}
	if result.Status != pipelinepkg.StatusCompleted {
		t.Fatalf("step %q status = %q, want %q; error = %+v", stepID, result.Status, pipelinepkg.StatusCompleted, result.Error)
	}
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if !strings.Contains(string(content), want) {
		t.Fatalf("%q missing %q:\n%s", path, want, string(content))
	}
}

func assertDispatchHistory(t *testing.T, mockTmux *pipelinepkg.MockTmuxClient, paneID string, wants ...string) {
	t.Helper()

	history, err := mockTmux.PasteHistory(paneID)
	if err != nil {
		t.Fatalf("PasteHistory(%s) error = %v", paneID, err)
	}
	if len(history) != 1 {
		t.Fatalf("PasteHistory(%s) length = %d, want 1: %+v", paneID, len(history), history)
	}
	for _, want := range wants {
		if !strings.Contains(history[0].Content, want) {
			t.Fatalf("PasteHistory(%s) missing %q:\n%s", paneID, want, history[0].Content)
		}
	}
	if !history[0].Enter {
		t.Fatalf("PasteHistory(%s) Enter = false, want true", paneID)
	}
}

func assertPaneOutputContains(t *testing.T, mockTmux *pipelinepkg.MockTmuxClient, paneID, want string) {
	t.Helper()

	output, err := mockTmux.CapturePaneOutput(paneID, 0)
	if err != nil {
		t.Fatalf("CapturePaneOutput(%s) error = %v", paneID, err)
	}
	if !strings.Contains(output, want) {
		t.Fatalf("CapturePaneOutput(%s) missing %q:\n%s", paneID, want, output)
	}
}

func captureSlog(t *testing.T, w io.Writer) func() {
	t.Helper()

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return func() {
		slog.SetDefault(previous)
	}
}

func parseJSONLEvents(t *testing.T, r io.Reader) []map[string]any {
	t.Helper()

	scanner := bufio.NewScanner(r)
	var events []map[string]any
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("parse slog JSON line %q: %v", string(line), err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan slog JSONL: %v", err)
	}
	return events
}

func assertLogEvent(t *testing.T, events []map[string]any, name string, kvPairs ...string) {
	t.Helper()

	if len(kvPairs)%2 != 0 {
		t.Fatalf("assertLogEvent requires key/value pairs, got %d values", len(kvPairs))
	}
	for _, event := range events {
		if fmtEventValue(event["msg"]) != name {
			continue
		}
		matched := true
		for i := 0; i < len(kvPairs); i += 2 {
			key, want := kvPairs[i], kvPairs[i+1]
			if fmtEventValue(event[key]) != want {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("log event %q with %v not found in %#v", name, kvPairs, events)
}

func fmtEventValue(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case float64:
		return fmt.Sprintf("%g", value)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
