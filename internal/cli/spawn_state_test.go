package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestNewSpawnState(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)

	if state.BatchID != "batch-123" {
		t.Errorf("expected BatchID 'batch-123', got %s", state.BatchID)
	}
	if state.StaggerSeconds != 90 {
		t.Errorf("expected StaggerSeconds 90, got %d", state.StaggerSeconds)
	}
	if state.TotalAgents != 3 {
		t.Errorf("expected TotalAgents 3, got %d", state.TotalAgents)
	}
	if state.StartedAt.IsZero() {
		t.Error("expected non-zero StartedAt")
	}
}

func TestSpawnStateAddPrompt(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)
	scheduledAt := time.Now().Add(90 * time.Second)

	state.AddPrompt("proj__cc_1", "pane-1", 1, scheduledAt)

	if len(state.Prompts) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(state.Prompts))
	}

	p := state.Prompts[0]
	if p.Pane != "proj__cc_1" {
		t.Errorf("expected pane 'proj__cc_1', got %s", p.Pane)
	}
	if p.PaneID != "pane-1" {
		t.Errorf("expected pane ID 'pane-1', got %s", p.PaneID)
	}
	if p.Order != 1 {
		t.Errorf("expected order 1, got %d", p.Order)
	}
	if p.Sent {
		t.Error("expected sent to be false")
	}
}

func TestSpawnStateMarkSent(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 2)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(90*time.Second))

	// Mark first prompt as sent
	state.MarkSent("pane-1")

	if !state.Prompts[0].Sent {
		t.Error("expected first prompt to be marked as sent")
	}
	if state.Prompts[0].SentAt.IsZero() {
		t.Error("expected SentAt to be set")
	}
	if state.Prompts[1].Sent {
		t.Error("expected second prompt to not be sent yet")
	}

	// Mark second prompt as sent - should complete the spawn
	state.MarkSent("pane-2")

	if !state.Prompts[1].Sent {
		t.Error("expected second prompt to be marked as sent")
	}
	if state.CompletedAt.IsZero() {
		t.Error("expected CompletedAt to be set when all prompts sent")
	}
}

func TestSpawnStatePendingCount(t *testing.T) {
	state := NewSpawnState("batch-123", 90, 3)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(90*time.Second))
	state.AddPrompt("proj__cc_3", "pane-3", 3, now.Add(180*time.Second))

	if state.PendingCount() != 3 {
		t.Errorf("expected 3 pending, got %d", state.PendingCount())
	}

	state.MarkSent("pane-1")
	if state.PendingCount() != 2 {
		t.Errorf("expected 2 pending, got %d", state.PendingCount())
	}

	state.MarkSent("pane-2")
	state.MarkSent("pane-3")
	if state.PendingCount() != 0 {
		t.Errorf("expected 0 pending, got %d", state.PendingCount())
	}
}

func TestSpawnStateSaveAndLoad(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create and populate spawn state
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()
	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(60*time.Second))
	state.MarkSent("pane-1")

	// Save state
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify file exists
	path := filepath.Join(tmpDir, ".ntm", "spawn-state.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("spawn state file not created")
	}

	// Load state
	loaded, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Fatalf("failed to load state: %v", err)
	}
	if loaded == nil {
		t.Fatal("loaded state is nil")
	}

	// Verify loaded state
	if loaded.BatchID != "batch-test" {
		t.Errorf("expected BatchID 'batch-test', got %s", loaded.BatchID)
	}
	if loaded.StaggerSeconds != 60 {
		t.Errorf("expected StaggerSeconds 60, got %d", loaded.StaggerSeconds)
	}
	if len(loaded.Prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d", len(loaded.Prompts))
	}
	if !loaded.Prompts[0].Sent {
		t.Error("expected first prompt to be sent")
	}
	if loaded.Prompts[1].Sent {
		t.Error("expected second prompt to not be sent")
	}
}

func TestLoadSpawnStateNotExists(t *testing.T) {
	tmpDir := t.TempDir()

	state, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Errorf("expected no error for missing file, got %v", err)
	}
	if state != nil {
		t.Error("expected nil state for missing file")
	}
}

func TestLoadSpawnState_ExpiresCompletedStateAfterGracePeriod(t *testing.T) {
	tmpDir := t.TempDir()

	state := NewSpawnState("batch-test", 60, 1)
	state.MarkComplete()
	state.CompletedAt = time.Now().Add(-(spawnStateCompletionGracePeriod + time.Second))
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save expired state: %v", err)
	}

	loaded, err := LoadSpawnState(tmpDir)
	if err != nil {
		t.Fatalf("LoadSpawnState() error = %v", err)
	}
	if loaded != nil {
		t.Fatalf("LoadSpawnState() = %#v, want nil for expired state", loaded)
	}
	if SpawnStateExists(tmpDir) {
		t.Fatal("expected expired spawn state file to be removed")
	}
}

func TestClearSpawnState(t *testing.T) {
	tmpDir := t.TempDir()

	// Create state file
	state := NewSpawnState("batch-test", 60, 1)
	if err := state.Save(tmpDir); err != nil {
		t.Fatalf("failed to save state: %v", err)
	}

	// Verify file exists
	if !SpawnStateExists(tmpDir) {
		t.Fatal("spawn state should exist")
	}

	// Clear state
	if err := ClearSpawnState(tmpDir); err != nil {
		t.Fatalf("failed to clear state: %v", err)
	}

	// Verify file is gone
	if SpawnStateExists(tmpDir) {
		t.Error("spawn state should not exist after clear")
	}
}

func TestSpawnStateIsComplete(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 1)
	state.AddPrompt("proj__cc_1", "pane-1", 1, time.Now())

	if state.IsComplete() {
		t.Error("expected incomplete before marking sent")
	}

	state.MarkSent("pane-1")

	if !state.IsComplete() {
		t.Error("expected complete after marking all sent")
	}
}

func TestSpawnStateMarkComplete(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	state.AddPrompt("proj__cc_1", "pane-1", 1, time.Now())
	state.AddPrompt("proj__cc_2", "pane-2", 2, time.Now())

	if state.IsComplete() {
		t.Error("expected incomplete before MarkComplete")
	}

	state.MarkComplete()

	if !state.IsComplete() {
		t.Error("expected complete after MarkComplete")
	}
}

func TestTimeUntilNextPrompt(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()

	// All sent - should return 0
	state.AddPrompt("proj__cc_1", "pane-1", 1, now.Add(-10*time.Second)) // Already past
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(30*time.Second))  // 30s from now

	state.MarkSent("pane-1") // Mark first as sent

	// Second prompt is still pending, 30s from now
	remaining := state.TimeUntilNextPrompt()
	if remaining <= 0 || remaining > 31*time.Second {
		t.Errorf("expected remaining ~30s, got %v", remaining)
	}

	state.MarkSent("pane-2")

	// All sent - should return 0
	remaining = state.TimeUntilNextPrompt()
	if remaining != 0 {
		t.Errorf("expected 0 when all sent, got %v", remaining)
	}
}

func TestGetPromptStatuses(t *testing.T) {
	state := NewSpawnState("batch-test", 60, 2)
	now := time.Now()

	state.AddPrompt("proj__cc_1", "pane-1", 1, now)
	state.AddPrompt("proj__cc_2", "pane-2", 2, now.Add(60*time.Second))

	statuses := state.GetPromptStatuses()

	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Verify it's a copy
	state.MarkSent("pane-1")
	if statuses[0].Sent {
		t.Error("copy should not be affected by original changes")
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// was written. Write volume is tiny (one warning line), so no drain
// goroutine is needed.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)
	return string(out)
}

// --- Pane record hooks (per-pane provider session id record) ---

func TestSpawnWritesPaneRecord(t *testing.T) {
	prev := paneRecordWriteHook
	t.Cleanup(func() { paneRecordWriteHook = prev })
	var gotName string
	var gotPanes []tmux.Pane
	paneRecordWriteHook = func(name string, panes []tmux.Pane) error {
		gotName, gotPanes = name, panes
		return errors.New("synthetic record failure")
	}
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "sess__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "sess__cod_1", Type: tmux.AgentCodex},
	}

	origStdout := os.Stdout
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = outW
	var hookErr error
	stderr := captureStderr(t, func() {
		hookErr = recordSpawnPanes("sess", panes)
	})
	_ = outW.Close()
	os.Stdout = origStdout
	outBytes, _ := io.ReadAll(outR)

	if hookErr != nil {
		t.Errorf("recordSpawnPanes returned %v: a failing record write must never fail spawn", hookErr)
	}
	if gotName != "sess" {
		t.Errorf("hook session = %q, want sess", gotName)
	}
	if len(gotPanes) != 2 {
		t.Errorf("hook panes = %d, want 2 (finalPanes)", len(gotPanes))
	}
	if len(outBytes) != 0 {
		t.Errorf("stdout got %q: hook output must stay on stderr so --json stays parseable", outBytes)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr got %q, want a warning", stderr)
	}
}

func TestRecordAddedPanes(t *testing.T) {
	prev := paneRecordAppendHook
	t.Cleanup(func() { paneRecordAppendHook = prev })
	var appended []tmux.Pane
	appendErr := error(nil)
	paneRecordAppendHook = func(name string, pane tmux.Pane) error {
		if name != "sess" {
			t.Errorf("hook session = %q, want sess", name)
		}
		appended = append(appended, pane)
		return appendErr
	}
	live := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "sess__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "sess__cod_1", Type: tmux.AgentCodex},
	}
	getPanes := func(session string) ([]tmux.Pane, error) {
		if session != "sess" {
			t.Errorf("getPanes session = %q, want sess", session)
		}
		return live, nil
	}

	// Exactly one append per new pane id; unknown ids are skipped.
	if err := recordAddedPanes("sess", []string{"%1", "%bogus"}, getPanes); err != nil {
		t.Fatalf("recordAddedPanes: %v", err)
	}
	if len(appended) != 1 || appended[0].ID != "%1" {
		t.Fatalf("appended = %+v, want only pane %%1", appended)
	}

	// A failing append warns on stderr but never fails add.
	appendErr = errors.New("synthetic append failure")
	var hookErr error
	stderr := captureStderr(t, func() {
		hookErr = recordAddedPanes("sess", []string{"%2"}, getPanes)
	})
	if hookErr != nil {
		t.Errorf("recordAddedPanes returned %v, want nil", hookErr)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr got %q, want a warning", stderr)
	}

	// No new panes means no pane listing at all.
	called := false
	if err := recordAddedPanes("sess", nil, func(string) ([]tmux.Pane, error) {
		called = true
		return nil, nil
	}); err != nil || called {
		t.Errorf("empty ids: err = %v, getPanes called = %v", err, called)
	}

	// A failed listing warns on stderr but never fails add.
	var listErr error
	stderr = captureStderr(t, func() {
		listErr = recordAddedPanes("sess", []string{"%1"}, func(string) ([]tmux.Pane, error) {
			return nil, errors.New("no tmux")
		})
	})
	if listErr != nil {
		t.Errorf("listing failure returned %v, want nil", listErr)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr got %q, want a warning", stderr)
	}
}

// TestRecordAddedPanesNeverFails extends TestRecordAddedPanes with the
// Behavior 6 failure-injection cases it does not already pin: a partial
// append failure must not stop the remaining panes, and neither an append
// failure nor a listing failure may write anything to stdout (so --json
// output stays parseable). The identity writer is pinned too: it returns
// nothing by construction (tmux.SetPaneIdentity swallows tmux errors), so
// stubbing the seam and calling it for several panes must simply complete.
func TestRecordAddedPanesNeverFails(t *testing.T) {
	prevAppend := paneRecordAppendHook
	t.Cleanup(func() { paneRecordAppendHook = prevAppend })
	prevIdentity := setPaneIdentityHook
	t.Cleanup(func() { setPaneIdentityHook = prevIdentity })

	live := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "sess__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "sess__cod_1", Type: tmux.AgentCodex},
	}
	getPanes := func(string) ([]tmux.Pane, error) { return live, nil }

	// Partial failure: %1's append fails, %2 must still be attempted.
	var attempted []string
	paneRecordAppendHook = func(_ string, pane tmux.Pane) error {
		attempted = append(attempted, pane.ID)
		if pane.ID == "%1" {
			return errors.New("synthetic append failure")
		}
		return nil
	}
	var addErr error
	var stderr string
	stdout, _ := captureStdout(t, func() error {
		stderr = captureStderr(t, func() {
			addErr = recordAddedPanes("sess", []string{"%1", "%2"}, getPanes)
		})
		return nil
	})
	if addErr != nil {
		t.Errorf("partial append failure returned %v, want nil (add must never fail)", addErr)
	}
	if len(attempted) != 2 || attempted[0] != "%1" || attempted[1] != "%2" {
		t.Errorf("attempted = %v, want both panes attempted in order despite the first failing", attempted)
	}
	if stdout != "" {
		t.Errorf("stdout got %q, want empty so --json output stays parseable", stdout)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr got %q, want a warning", stderr)
	}

	// Listing failure: nil error and stdout stays clean.
	var listErr error
	stdout, _ = captureStdout(t, func() error {
		stderr = captureStderr(t, func() {
			listErr = recordAddedPanes("sess", []string{"%1", "%2"}, func(string) ([]tmux.Pane, error) {
				return nil, errors.New("no tmux")
			})
		})
		return nil
	})
	if listErr != nil {
		t.Errorf("listing failure returned %v, want nil", listErr)
	}
	if stdout != "" {
		t.Errorf("stdout got %q, want empty so --json output stays parseable", stdout)
	}
	if !strings.Contains(stderr, "warning") {
		t.Errorf("stderr got %q, want a warning", stderr)
	}

	// Identity write: the seam mirrors tmux.SetPaneIdentity, which returns
	// nothing and swallows tmux errors, so there is no error to propagate.
	// Calling the writer for several panes must complete and hit the seam
	// once per pane.
	var identityCalls int
	setPaneIdentityHook = func(string, string, int, string) { identityCalls++ }
	writeAddPaneIdentity("%1", FlatAgent{Type: AgentTypeClaude, Model: "opus"}, 1)
	writeAddPaneIdentity("%2", FlatAgent{Type: AgentTypeCodex}, 1)
	if identityCalls != 2 {
		t.Errorf("identity hook calls = %d, want 2 (one per added pane)", identityCalls)
	}
}
