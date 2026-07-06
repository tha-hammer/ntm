package ensemble

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewCheckpointStore(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}
	if store == nil {
		t.Fatal("store is nil")
	}

	// Verify directory was created
	checkpointDir := filepath.Join(tmpDir, checkpointDirName)
	if _, err := os.Stat(checkpointDir); os.IsNotExist(err) {
		t.Error("checkpoint directory was not created")
	}

	t.Logf("TEST: %s - assertion: checkpoint store created successfully", t.Name())
}

func TestCheckpointStore_SaveAndLoadCheckpoint(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-1"
	checkpoint := ModeCheckpoint{
		ModeID: "deductive",
		Output: &ModeOutput{
			ModeID: "deductive",
			Thesis: "Test thesis",
		},
		Status:      string(AssignmentDone),
		CapturedAt:  time.Now().UTC(),
		ContextHash: "abc123",
		TokensUsed:  1000,
	}

	// Save checkpoint
	if err := store.SaveCheckpoint(runID, checkpoint); err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	// Load checkpoint
	loaded, err := store.LoadCheckpoint(runID, "deductive")
	if err != nil {
		t.Fatalf("LoadCheckpoint failed: %v", err)
	}

	if loaded.ModeID != checkpoint.ModeID {
		t.Errorf("ModeID = %q, want %q", loaded.ModeID, checkpoint.ModeID)
	}
	if loaded.Status != checkpoint.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, checkpoint.Status)
	}
	if loaded.TokensUsed != checkpoint.TokensUsed {
		t.Errorf("TokensUsed = %d, want %d", loaded.TokensUsed, checkpoint.TokensUsed)
	}
	if loaded.Output == nil || loaded.Output.Thesis != checkpoint.Output.Thesis {
		t.Error("Output not loaded correctly")
	}

	t.Logf("TEST: %s - assertion: checkpoint save/load works", t.Name())
}

func TestCheckpointStore_SaveAndLoadMetadata(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	meta := CheckpointMetadata{
		SessionName: "test-session",
		Question:    "What is the meaning of life?",
		RunID:       "test-run-2",
		Status:      EnsembleActive,
		CreatedAt:   time.Now().UTC(),
		ContextHash: "def456",
		PendingIDs:  []string{"deductive", "inductive"},
		TotalModes:  2,
	}

	// Save metadata
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Load metadata
	loaded, err := store.LoadMetadata("test-run-2")
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if loaded.SessionName != meta.SessionName {
		t.Errorf("SessionName = %q, want %q", loaded.SessionName, meta.SessionName)
	}
	if loaded.Question != meta.Question {
		t.Errorf("Question = %q, want %q", loaded.Question, meta.Question)
	}
	if len(loaded.PendingIDs) != len(meta.PendingIDs) {
		t.Errorf("PendingIDs count = %d, want %d", len(loaded.PendingIDs), len(meta.PendingIDs))
	}

	t.Logf("TEST: %s - assertion: metadata save/load works", t.Name())
}

func TestCheckpointStore_LoadMetadata_RejectsRunIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "expected-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(CheckpointMetadata{RunID: "other-run"})
	if err != nil {
		t.Fatalf("marshal metadata failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, checkpointMetaFile), data, 0o644); err != nil {
		t.Fatalf("write metadata failed: %v", err)
	}

	_, err = store.LoadMetadata(runID)
	if err == nil {
		t.Fatal("LoadMetadata() error = nil, want run ID mismatch")
	}
	if !strings.Contains(err.Error(), "metadata run ID mismatch") {
		t.Fatalf("LoadMetadata() error = %v, want run ID mismatch", err)
	}
}

// bd-du7e5: NormalizeCheckpointRunID is the load-bearing guard
// against path traversal in every cli/ensemble caller that joins a
// runID into a filesystem path. The function exists and is called
// throughout (including from cli/ensemble.go::resolveEnsembleCheckpoint
// StoreForRunID) but had no focused unit test pinning the rejection
// contract — a future refactor that loosens the validation could go
// unnoticed. This test pins every documented-malformed shape.
func TestNormalizeCheckpointRunID_RejectsTraversalAndPathSeparators(t *testing.T) {
	t.Parallel()
	rejectCases := []struct {
		name  string
		runID string
	}{
		{"empty", ""},
		{"whitespace_only", "   "},
		{"single_dot", "."},
		{"double_dot", ".."},
		{"parent_then_subdir", "../foo"},
		{"deep_traversal", "../../../etc/passwd"},
		{"absolute_unix", "/abs/path"},
		{"absolute_root", "/"},
		{"contains_forward_slash", "a/b"},
		{"contains_backslash", "a\\b"},
		{"contains_nul_byte", "run\x00id"},
		{"trailing_slash", "trailing/"},
		{"leading_slash", "/leading"},
		{"dot_dot_in_middle", "foo/../bar"},
	}
	for _, c := range rejectCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeCheckpointRunID(c.runID)
			if err == nil {
				t.Errorf("NormalizeCheckpointRunID(%q) = (%q, nil); want non-nil error", c.runID, got)
			}
		})
	}

	acceptCases := []struct {
		name  string
		runID string
		want  string
	}{
		{"alphanumeric", "abc123", "abc123"},
		{"with_dash", "run-2026-05-08", "run-2026-05-08"},
		{"with_underscore", "run_42", "run_42"},
		{"leading_dot_dot_basename", "..hidden_id", "..hidden_id"},
		{"trimmed_whitespace", "   ok   ", "ok"},
		{"single_dot_in_middle", "v1.0.beta", "v1.0.beta"},
	}
	for _, c := range acceptCases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizeCheckpointRunID(c.runID)
			if err != nil {
				t.Errorf("NormalizeCheckpointRunID(%q) error = %v, want nil", c.runID, err)
			}
			if got != c.want {
				t.Errorf("NormalizeCheckpointRunID(%q) = %q, want %q", c.runID, got, c.want)
			}
		})
	}
}

func TestCheckpointStore_RejectsInvalidRunID(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	err = store.SaveMetadata(CheckpointMetadata{
		RunID:       "../escape",
		SessionName: "test-session",
		Question:    "bad run id",
	})
	if err == nil {
		t.Fatal("SaveMetadata() error = nil, want invalid run ID")
	}
	if !strings.Contains(err.Error(), "invalid run ID") {
		t.Fatalf("SaveMetadata() error = %v, want invalid run ID", err)
	}

	if _, err := store.LoadMetadata("../escape"); err == nil {
		t.Fatal("LoadMetadata() error = nil, want invalid run ID")
	} else if !strings.Contains(err.Error(), "invalid run ID") {
		t.Fatalf("LoadMetadata() error = %v, want invalid run ID", err)
	}
}

func TestCheckpointStore_RejectsInvalidModeID(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	err = store.SaveCheckpoint("valid-run", ModeCheckpoint{
		ModeID: "../escape",
		Status: string(AssignmentDone),
	})
	if err == nil {
		t.Fatal("SaveCheckpoint() error = nil, want invalid mode ID")
	}
	if !strings.Contains(err.Error(), "invalid mode ID") {
		t.Fatalf("SaveCheckpoint() error = %v, want invalid mode ID", err)
	}

	if _, err := store.LoadCheckpoint("valid-run", "../escape"); err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want invalid mode ID")
	} else if !strings.Contains(err.Error(), "invalid mode ID") {
		t.Fatalf("LoadCheckpoint() error = %v, want invalid mode ID", err)
	}
}

func TestCheckpointStore_SaveCheckpoint_RejectsOutputModeIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	err = store.SaveCheckpoint("valid-run", ModeCheckpoint{
		ModeID: "deductive",
		Output: &ModeOutput{ModeID: "other-mode"},
		Status: string(AssignmentDone),
	})
	if err == nil {
		t.Fatal("SaveCheckpoint() error = nil, want output mode ID mismatch")
	}
	if !strings.Contains(err.Error(), "checkpoint output mode ID mismatch") {
		t.Fatalf("SaveCheckpoint() error = %v, want output mode ID mismatch", err)
	}
}

func TestCheckpointStore_SaveAndLoadSynthesisCheckpoint(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-synth-run"
	checkpoint := SynthesisCheckpoint{
		RunID:       runID,
		SessionName: "test-session",
		LastIndex:   7,
		Error:       "context canceled",
		CreatedAt:   time.Now().UTC(),
	}

	if err := store.SaveSynthesisCheckpoint(runID, checkpoint); err != nil {
		t.Fatalf("SaveSynthesisCheckpoint failed: %v", err)
	}

	loaded, err := store.LoadSynthesisCheckpoint(runID)
	if err != nil {
		t.Fatalf("LoadSynthesisCheckpoint failed: %v", err)
	}

	if loaded.LastIndex != checkpoint.LastIndex {
		t.Errorf("LastIndex = %d, want %d", loaded.LastIndex, checkpoint.LastIndex)
	}
	if loaded.Error != checkpoint.Error {
		t.Errorf("Error = %q, want %q", loaded.Error, checkpoint.Error)
	}
	if loaded.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero")
	}

	t.Logf("TEST: %s - assertion: synthesis checkpoint save/load works", t.Name())
}

func TestCheckpointStore_LoadSynthesisCheckpoint_RejectsRunIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "expected-synth-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(SynthesisCheckpoint{RunID: "other-run"})
	if err != nil {
		t.Fatalf("marshal synthesis checkpoint failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, checkpointSynthesisFile), data, 0o644); err != nil {
		t.Fatalf("write synthesis checkpoint failed: %v", err)
	}

	_, err = store.LoadSynthesisCheckpoint(runID)
	if err == nil {
		t.Fatal("LoadSynthesisCheckpoint() error = nil, want run ID mismatch")
	}
	if !strings.Contains(err.Error(), "synthesis checkpoint run ID mismatch") {
		t.Fatalf("LoadSynthesisCheckpoint() error = %v, want run ID mismatch", err)
	}
}

func TestCheckpointStore_SaveMetadata_RejectsSymlinkedRunDir(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	targetDir := filepath.Join(tmpDir, "outside-run")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir failed: %v", err)
	}

	runID := "symlink-run"
	runPath := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.Symlink(targetDir, runPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	err = store.SaveMetadata(CheckpointMetadata{RunID: runID})
	if err == nil {
		t.Fatal("SaveMetadata() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "checkpoint run path must not be a symlink") {
		t.Fatalf("SaveMetadata() error = %v, want run dir symlink rejection", err)
	}
}

func TestCheckpointStore_LoadMetadata_RejectsSymlinkedMetadataFile(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "symlink-meta-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	target := filepath.Join(tmpDir, "outside-meta.json")
	if err := os.WriteFile(target, []byte(`{"run_id":"outside"}`), 0o644); err != nil {
		t.Fatalf("write target metadata failed: %v", err)
	}

	metaPath := filepath.Join(runDir, checkpointMetaFile)
	if err := os.Symlink(target, metaPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err = store.LoadMetadata(runID)
	if err == nil {
		t.Fatal("LoadMetadata() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "metadata file must not be a symlink") {
		t.Fatalf("LoadMetadata() error = %v, want metadata symlink rejection", err)
	}
}

func TestCheckpointStore_LoadCheckpoint_RejectsSymlinkedCheckpointFile(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "symlink-checkpoint-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	target := filepath.Join(tmpDir, "outside-checkpoint.json")
	if err := os.WriteFile(target, []byte(`{"mode_id":"deductive","status":"done"}`), 0o644); err != nil {
		t.Fatalf("write target checkpoint failed: %v", err)
	}

	cpPath := filepath.Join(runDir, "deductive.json")
	if err := os.Symlink(target, cpPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err = store.LoadCheckpoint(runID, "deductive")
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "checkpoint file must not be a symlink") {
		t.Fatalf("LoadCheckpoint() error = %v, want checkpoint symlink rejection", err)
	}
}

func TestCheckpointStore_LoadCheckpoint_RejectsModeIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "mode-mismatch-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(ModeCheckpoint{ModeID: "other-mode", Status: string(AssignmentDone)})
	if err != nil {
		t.Fatalf("marshal checkpoint failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "expected-mode.json"), data, 0o644); err != nil {
		t.Fatalf("write checkpoint failed: %v", err)
	}

	_, err = store.LoadCheckpoint(runID, "expected-mode")
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want mode ID mismatch")
	}
	if !strings.Contains(err.Error(), "checkpoint mode ID mismatch") {
		t.Fatalf("LoadCheckpoint() error = %v, want mode ID mismatch", err)
	}
}

func TestCheckpointStore_LoadCheckpoint_RejectsOutputModeIDMismatch(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "output-mode-mismatch-run"
	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir run dir failed: %v", err)
	}

	data, err := json.Marshal(ModeCheckpoint{
		ModeID: "expected-mode",
		Output: &ModeOutput{ModeID: "other-mode"},
		Status: string(AssignmentDone),
	})
	if err != nil {
		t.Fatalf("marshal checkpoint failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "expected-mode.json"), data, 0o644); err != nil {
		t.Fatalf("write checkpoint failed: %v", err)
	}

	_, err = store.LoadCheckpoint(runID, "expected-mode")
	if err == nil {
		t.Fatal("LoadCheckpoint() error = nil, want output mode ID mismatch")
	}
	if !strings.Contains(err.Error(), "checkpoint output mode ID mismatch") {
		t.Fatalf("LoadCheckpoint() error = %v, want output mode ID mismatch", err)
	}
}

func TestCheckpoint_SaveRestore(t *testing.T) {
	input := map[string]any{"run": "run-save-restore", "mode": "deductive"}
	logTestStartCheckpoint(t, input)

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	assertNoErrorCheckpoint(t, "new checkpoint store", err)

	checkpoint := ModeCheckpoint{
		ModeID: "deductive",
		Output: &ModeOutput{ModeID: "deductive", Thesis: "Checkpoint thesis"},
		Status: string(AssignmentDone),
	}
	assertNoErrorCheckpoint(t, "save checkpoint", store.SaveCheckpoint("run-save-restore", checkpoint))

	loaded, err := store.LoadCheckpoint("run-save-restore", "deductive")
	logTestResultCheckpoint(t, loaded)
	assertNoErrorCheckpoint(t, "load checkpoint", err)
	assertEqualCheckpoint(t, "loaded thesis", loaded.Output.Thesis, checkpoint.Output.Thesis)
}

func TestCheckpoint_PartialFailure(t *testing.T) {
	input := map[string]any{"run": "", "mode": ""}
	logTestStartCheckpoint(t, input)

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	assertNoErrorCheckpoint(t, "new checkpoint store", err)

	err = store.SaveCheckpoint("", ModeCheckpoint{})
	logTestResultCheckpoint(t, err)
	assertTrueCheckpoint(t, "error on missing run id", err != nil)
}

func TestCheckpoint_Cleanup(t *testing.T) {
	input := map[string]any{"run": "run-cleanup"}
	logTestStartCheckpoint(t, input)

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	assertNoErrorCheckpoint(t, "new checkpoint store", err)

	checkpoint := ModeCheckpoint{ModeID: "mode-a", Status: string(AssignmentDone)}
	assertNoErrorCheckpoint(t, "save checkpoint", store.SaveCheckpoint("run-cleanup", checkpoint))
	assertNoErrorCheckpoint(t, "delete run", store.DeleteRun("run-cleanup"))

	_, err = store.LoadCheckpoint("run-cleanup", "mode-a")
	logTestResultCheckpoint(t, err)
	assertTrueCheckpoint(t, "checkpoint removed", err != nil)
}

func TestCheckpoint_Concurrent(t *testing.T) {
	input := map[string]any{"run": "run-concurrent", "modes": []string{"mode-a", "mode-b", "mode-c"}}
	logTestStartCheckpoint(t, input)

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	assertNoErrorCheckpoint(t, "new checkpoint store", err)

	errs := make(chan error, 3)
	modes := []string{"mode-a", "mode-b", "mode-c"}
	for _, modeID := range modes {
		modeID := modeID
		go func() {
			errs <- store.SaveCheckpoint("run-concurrent", ModeCheckpoint{ModeID: modeID, Status: string(AssignmentDone)})
		}()
	}

	for range modes {
		err := <-errs
		assertTrueCheckpoint(t, "concurrent save ok", err == nil)
	}

	for _, modeID := range modes {
		loaded, err := store.LoadCheckpoint("run-concurrent", modeID)
		logTestResultCheckpoint(t, loaded)
		assertNoErrorCheckpoint(t, "load checkpoint", err)
		assertEqualCheckpoint(t, "loaded mode id", loaded.ModeID, modeID)
	}
}

func logTestStartCheckpoint(t *testing.T, input any) {
	t.Helper()
	t.Logf("TEST: %s - starting with input: %v", t.Name(), input)
}

func logTestResultCheckpoint(t *testing.T, result any) {
	t.Helper()
	t.Logf("TEST: %s - got result: %v", t.Name(), result)
}

func assertNoErrorCheckpoint(t *testing.T, desc string, err error) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if err != nil {
		t.Fatalf("%s: %v", desc, err)
	}
}

func assertTrueCheckpoint(t *testing.T, desc string, ok bool) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if !ok {
		t.Fatalf("assertion failed: %s", desc)
	}
}

func assertEqualCheckpoint(t *testing.T, desc string, got, want any) {
	t.Helper()
	t.Logf("TEST: %s - assertion: %s", t.Name(), desc)
	if got != want {
		t.Fatalf("%s: got %v want %v", desc, got, want)
	}
}

func TestCheckpointStore_LoadCheckpoint_NotFound(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	_, err = store.LoadCheckpoint("nonexistent-run", "nonexistent-mode")
	if err == nil {
		t.Error("expected error for nonexistent checkpoint")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}

	t.Logf("TEST: %s - assertion: not found error returned", t.Name())
}

func TestCheckpointStore_LoadAllCheckpoints(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-3"
	modes := []string{"deductive", "inductive", "causal"}

	for _, mode := range modes {
		checkpoint := ModeCheckpoint{
			ModeID: mode,
			Status: string(AssignmentDone),
		}
		if err := store.SaveCheckpoint(runID, checkpoint); err != nil {
			t.Fatalf("SaveCheckpoint failed for %s: %v", mode, err)
		}
	}

	// Load all
	checkpoints, err := store.LoadAllCheckpoints(runID)
	if err != nil {
		t.Fatalf("LoadAllCheckpoints failed: %v", err)
	}

	if len(checkpoints) != len(modes) {
		t.Errorf("got %d checkpoints, want %d", len(checkpoints), len(modes))
	}

	t.Logf("TEST: %s - assertion: all checkpoints loaded", t.Name())
}

func TestCheckpointStore_LoadAllCheckpoints_SkipsSynthesis(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-skip-synthesis"

	// Save a mode checkpoint
	modeCP := ModeCheckpoint{
		ModeID: "deductive",
		Status: string(AssignmentDone),
	}
	if err := store.SaveCheckpoint(runID, modeCP); err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	// Save a synthesis checkpoint (should be skipped by LoadAllCheckpoints)
	synthCP := SynthesisCheckpoint{
		RunID:     runID,
		LastIndex: 5,
	}
	if err := store.SaveSynthesisCheckpoint(runID, synthCP); err != nil {
		t.Fatalf("SaveSynthesisCheckpoint failed: %v", err)
	}

	// LoadAllCheckpoints should only return the mode checkpoint, not the synthesis
	checkpoints, err := store.LoadAllCheckpoints(runID)
	if err != nil {
		t.Fatalf("LoadAllCheckpoints failed: %v", err)
	}

	if len(checkpoints) != 1 {
		t.Errorf("got %d checkpoints, want 1 (synthesis.json should be skipped)", len(checkpoints))
	}
	if len(checkpoints) > 0 && checkpoints[0].ModeID != "deductive" {
		t.Errorf("expected deductive mode, got %q", checkpoints[0].ModeID)
	}

	t.Logf("TEST: %s - assertion: synthesis checkpoint correctly skipped", t.Name())
}

func TestCheckpointStore_LoadAllCheckpoints_FailsOnInvalidCheckpointFile(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "invalid-checkpoint-run"
	if err := store.SaveCheckpoint(runID, ModeCheckpoint{
		ModeID: "good-mode",
		Status: string(AssignmentDone),
	}); err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	runDir := filepath.Join(tmpDir, checkpointDirName, runID)
	target := filepath.Join(tmpDir, "outside-invalid-checkpoint.json")
	if err := os.WriteFile(target, []byte(`{"mode_id":"bad-mode","status":"done"}`), 0o644); err != nil {
		t.Fatalf("write target checkpoint failed: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(runDir, "bad-mode.json")); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	_, err = store.LoadAllCheckpoints(runID)
	if err == nil {
		t.Fatal("LoadAllCheckpoints() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), `load checkpoint "bad-mode"`) || !strings.Contains(err.Error(), "checkpoint file must not be a symlink") {
		t.Fatalf("LoadAllCheckpoints() error = %v, want invalid checkpoint file rejection", err)
	}
}

func TestCheckpointStore_ListRuns(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	// Create multiple runs
	runs := []string{"run-a", "run-b", "run-c"}
	for _, runID := range runs {
		meta := CheckpointMetadata{
			RunID:     runID,
			CreatedAt: time.Now().UTC(),
		}
		if err := store.SaveMetadata(meta); err != nil {
			t.Fatalf("SaveMetadata failed for %s: %v", runID, err)
		}
	}

	// List runs
	listed, err := store.ListRuns()
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}

	if len(listed) != len(runs) {
		t.Errorf("got %d runs, want %d", len(listed), len(runs))
	}

	t.Logf("TEST: %s - assertion: all runs listed", t.Name())
}

func TestCheckpointStore_DeleteRun(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-delete"
	meta := CheckpointMetadata{
		RunID:     runID,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Verify exists
	if !store.RunExists(runID) {
		t.Error("run should exist before delete")
	}

	// Delete
	if err := store.DeleteRun(runID); err != nil {
		t.Fatalf("DeleteRun failed: %v", err)
	}

	// Verify gone
	if store.RunExists(runID) {
		t.Error("run should not exist after delete")
	}

	t.Logf("TEST: %s - assertion: run deleted successfully", t.Name())
}

func TestCheckpointStore_RunExists(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	if store.RunExists("nonexistent") {
		t.Error("nonexistent run should return false")
	}

	runID := "existing-run"
	meta := CheckpointMetadata{RunID: runID}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	if !store.RunExists(runID) {
		t.Error("existing run should return true")
	}

	t.Logf("TEST: %s - assertion: RunExists works correctly", t.Name())
}

func TestCheckpointStore_RunExists_RejectsSymlinkedRunDir(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	targetDir := filepath.Join(tmpDir, "outside-run")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir failed: %v", err)
	}

	runID := "symlink-run"
	runPath := filepath.Join(tmpDir, checkpointDirName, runID)
	if err := os.Symlink(targetDir, runPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if store.RunExists(runID) {
		t.Fatal("RunExists() = true, want false for symlinked run dir")
	}
}

func TestCheckpointStore_UpdateModeStatus(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-status"
	meta := CheckpointMetadata{
		RunID:      runID,
		PendingIDs: []string{"mode-a", "mode-b"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Update mode-a to done
	if err := store.UpdateModeStatus(runID, "mode-a", string(AssignmentDone)); err != nil {
		t.Fatalf("UpdateModeStatus failed: %v", err)
	}

	// Verify
	loaded, err := store.LoadMetadata(runID)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if len(loaded.CompletedIDs) != 1 || loaded.CompletedIDs[0] != "mode-a" {
		t.Errorf("CompletedIDs = %v, want [mode-a]", loaded.CompletedIDs)
	}
	if len(loaded.PendingIDs) != 1 || loaded.PendingIDs[0] != "mode-b" {
		t.Errorf("PendingIDs = %v, want [mode-b]", loaded.PendingIDs)
	}

	t.Logf("TEST: %s - assertion: mode status updated correctly", t.Name())
}

func TestCheckpointStore_GetCompletedOutputs(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-run-outputs"

	// Save completed checkpoint
	completedCP := ModeCheckpoint{
		ModeID: "completed-mode",
		Output: &ModeOutput{ModeID: "completed-mode", Thesis: "Done"},
		Status: string(AssignmentDone),
	}
	if err := store.SaveCheckpoint(runID, completedCP); err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	// Save error checkpoint
	errorCP := ModeCheckpoint{
		ModeID: "error-mode",
		Status: string(AssignmentError),
		Error:  "something failed",
	}
	if err := store.SaveCheckpoint(runID, errorCP); err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	// Get completed outputs
	outputs, err := store.GetCompletedOutputs(runID)
	if err != nil {
		t.Fatalf("GetCompletedOutputs failed: %v", err)
	}

	if len(outputs) != 1 {
		t.Errorf("got %d completed outputs, want 1", len(outputs))
	}
	if outputs[0].ModeID != "completed-mode" {
		t.Errorf("output ModeID = %q, want completed-mode", outputs[0].ModeID)
	}

	t.Logf("TEST: %s - assertion: only completed outputs returned", t.Name())
}

func TestCheckpointManager_Initialize(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	manager := NewCheckpointManager(store, "test-manager-run")

	session := &EnsembleSession{
		SessionName: "test-session",
		Question:    "Test question?",
		Assignments: []ModeAssignment{
			{ModeID: "mode-1"},
			{ModeID: "mode-2"},
		},
		Status:    EnsembleActive,
		CreatedAt: time.Now().UTC(),
	}

	if err := manager.Initialize(session, "context-hash-123"); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Verify metadata was created
	meta, err := store.LoadMetadata("test-manager-run")
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}

	if meta.SessionName != session.SessionName {
		t.Errorf("SessionName = %q, want %q", meta.SessionName, session.SessionName)
	}
	if len(meta.PendingIDs) != 2 {
		t.Errorf("PendingIDs count = %d, want 2", len(meta.PendingIDs))
	}

	t.Logf("TEST: %s - assertion: checkpoint manager initialized", t.Name())
}

func TestCheckpointManager_RecordOutput(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "test-record-run"
	manager := NewCheckpointManager(store, runID)

	// Initialize with metadata
	meta := CheckpointMetadata{
		RunID:      runID,
		PendingIDs: []string{"mode-1"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Record output
	output := &ModeOutput{
		ModeID: "mode-1",
		Thesis: "Test output",
	}
	if err := manager.RecordOutput("mode-1", output, 500, "ctx-hash"); err != nil {
		t.Fatalf("RecordOutput failed: %v", err)
	}

	// Verify checkpoint was saved
	cp, err := store.LoadCheckpoint(runID, "mode-1")
	if err != nil {
		t.Fatalf("LoadCheckpoint failed: %v", err)
	}

	if cp.Status != string(AssignmentDone) {
		t.Errorf("Status = %q, want %q", cp.Status, string(AssignmentDone))
	}
	if cp.TokensUsed != 500 {
		t.Errorf("TokensUsed = %d, want 500", cp.TokensUsed)
	}

	t.Logf("TEST: %s - assertion: output recorded successfully", t.Name())
}

func TestCheckpointManager_IsResumable(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	// Test with pending modes
	runID1 := "resumable-run"
	meta1 := CheckpointMetadata{
		RunID:      runID1,
		PendingIDs: []string{"mode-1"},
	}
	if err := store.SaveMetadata(meta1); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	manager1 := NewCheckpointManager(store, runID1)
	if !manager1.IsResumable() {
		t.Error("run with pending modes should be resumable")
	}

	// Test with all completed
	runID2 := "complete-run"
	meta2 := CheckpointMetadata{
		RunID:        runID2,
		CompletedIDs: []string{"mode-1"},
	}
	if err := store.SaveMetadata(meta2); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	manager2 := NewCheckpointManager(store, runID2)
	if manager2.IsResumable() {
		t.Error("fully completed run should not be resumable")
	}

	t.Logf("TEST: %s - assertion: IsResumable works correctly", t.Name())
}

func TestCheckpointStore_NilReceiver(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	var store *CheckpointStore

	if _, err := store.LoadCheckpoint("run", "mode"); err == nil {
		t.Error("LoadCheckpoint on nil should return error")
	}
	if _, err := store.LoadMetadata("run"); err == nil {
		t.Error("LoadMetadata on nil should return error")
	}
	if err := store.SaveCheckpoint("run", ModeCheckpoint{}); err == nil {
		t.Error("SaveCheckpoint on nil should return error")
	}
	if err := store.SaveMetadata(CheckpointMetadata{}); err == nil {
		t.Error("SaveMetadata on nil should return error")
	}
	if store.RunExists("run") {
		t.Error("RunExists on nil should return false")
	}

	t.Logf("TEST: %s - assertion: nil receiver handling works", t.Name())
}

func TestSliceContains(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	slice := []string{"a", "b", "c"}

	if !sliceContains(slice, "a") {
		t.Error("sliceContains should return true for existing item")
	}
	if !sliceContains(slice, "c") {
		t.Error("sliceContains should return true for existing item")
	}
	if sliceContains(slice, "d") {
		t.Error("sliceContains should return false for non-existing item")
	}
	if sliceContains(nil, "a") {
		t.Error("sliceContains should return false for nil slice")
	}
	if sliceContains([]string{}, "a") {
		t.Error("sliceContains should return false for empty slice")
	}

	t.Logf("TEST: %s - assertion: sliceContains works correctly", t.Name())
}

func TestRemoveFromSlice(t *testing.T) {
	t.Logf("TEST: %s - starting", t.Name())

	tests := []struct {
		slice  []string
		item   string
		expect []string
	}{
		{[]string{"a", "b", "c"}, "b", []string{"a", "c"}},
		{[]string{"a", "b", "c"}, "a", []string{"b", "c"}},
		{[]string{"a", "b", "c"}, "c", []string{"a", "b"}},
		{[]string{"a", "b", "c"}, "d", []string{"a", "b", "c"}},
		{[]string{}, "a", []string{}},
		{nil, "a", []string{}},
	}

	for _, tt := range tests {
		result := removeFromSlice(tt.slice, tt.item)
		if len(result) != len(tt.expect) {
			t.Errorf("removeFromSlice(%v, %q) = %v, want %v", tt.slice, tt.item, result, tt.expect)
		}
	}

	t.Logf("TEST: %s - assertion: removeFromSlice works correctly", t.Name())
}

func TestCheckpointStore_WithLogger(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	// Should return self for chaining
	result := store.WithLogger(nil)
	if result != store {
		t.Error("WithLogger should return store for chaining")
	}

	// Should work with non-nil logger
	result = store.WithLogger(store.logger)
	if result != store {
		t.Error("WithLogger should return store for chaining")
	}
}

func TestCheckpointStore_CleanOld(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	// Create old run - manually write metadata with old timestamp
	oldRunID := "old-run"
	oldRunDir := filepath.Join(tmpDir, checkpointDirName, oldRunID)
	if err := os.MkdirAll(oldRunDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	oldMeta := CheckpointMetadata{
		RunID:     oldRunID,
		CreatedAt: time.Now().Add(-48 * time.Hour),
		UpdatedAt: time.Now().Add(-48 * time.Hour),
	}
	oldData, _ := json.Marshal(oldMeta)
	if err := os.WriteFile(filepath.Join(oldRunDir, checkpointMetaFile), oldData, 0o644); err != nil {
		t.Fatalf("write old metadata failed: %v", err)
	}

	// Create new run
	newMeta := CheckpointMetadata{
		RunID:     "new-run",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.SaveMetadata(newMeta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Clean runs older than 24 hours
	removed, err := store.CleanOld(24 * time.Hour)
	if err != nil {
		t.Fatalf("CleanOld failed: %v", err)
	}

	if removed != 1 {
		t.Errorf("CleanOld removed %d, want 1", removed)
	}

	// Old run should be gone
	if store.RunExists(oldRunID) {
		t.Error("old run should be removed")
	}

	// New run should still exist
	if !store.RunExists("new-run") {
		t.Error("new run should still exist")
	}
}

func TestCheckpointStore_CleanOld_NilStore(t *testing.T) {
	var store *CheckpointStore
	_, err := store.CleanOld(24 * time.Hour)
	if err == nil {
		t.Error("CleanOld on nil should return error")
	}
}

func TestCheckpointStore_GetPendingModeIDs(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "pending-test"
	meta := CheckpointMetadata{
		RunID:      runID,
		PendingIDs: []string{"mode-1", "mode-2", "mode-3"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	pending, err := store.GetPendingModeIDs(runID)
	if err != nil {
		t.Fatalf("GetPendingModeIDs failed: %v", err)
	}

	if len(pending) != 3 {
		t.Errorf("GetPendingModeIDs returned %d, want 3", len(pending))
	}
}

func TestCheckpointManager_WithLogger(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	manager := NewCheckpointManager(store, "test-run")

	// Should return self for chaining
	result := manager.WithLogger(nil)
	if result != manager {
		t.Error("WithLogger should return manager for chaining")
	}

	// Should work with non-nil logger
	result = manager.WithLogger(manager.logger)
	if result != manager {
		t.Error("WithLogger should return manager for chaining")
	}
}

func TestCheckpointManager_RecordError(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "error-test"
	manager := NewCheckpointManager(store, runID)

	// Initialize metadata
	meta := CheckpointMetadata{
		RunID:      runID,
		PendingIDs: []string{"failing-mode"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Record error
	testErr := os.ErrNotExist
	if err := manager.RecordError("failing-mode", testErr); err != nil {
		t.Fatalf("RecordError failed: %v", err)
	}

	// Verify checkpoint was saved
	cp, err := store.LoadCheckpoint(runID, "failing-mode")
	if err != nil {
		t.Fatalf("LoadCheckpoint failed: %v", err)
	}

	if cp.Status != string(AssignmentError) {
		t.Errorf("Status = %q, want %q", cp.Status, string(AssignmentError))
	}
	if cp.Error == "" {
		t.Error("Error message should be set")
	}

	// Verify metadata was updated
	loaded, err := store.LoadMetadata(runID)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}
	if len(loaded.ErrorIDs) != 1 || loaded.ErrorIDs[0] != "failing-mode" {
		t.Errorf("ErrorIDs = %v, want [failing-mode]", loaded.ErrorIDs)
	}
}

func TestCheckpointManager_RecordError_NilManager(t *testing.T) {
	var manager *CheckpointManager
	err := manager.RecordError("mode", os.ErrNotExist)
	if err == nil {
		t.Error("RecordError on nil should return error")
	}
}

func TestCheckpointManager_MarkComplete(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "complete-test"
	manager := NewCheckpointManager(store, runID)

	// Initialize metadata
	meta := CheckpointMetadata{
		RunID:        runID,
		Status:       EnsembleActive,
		CompletedIDs: []string{"mode-1"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Mark complete without cleanup
	if err := manager.MarkComplete(false); err != nil {
		t.Fatalf("MarkComplete failed: %v", err)
	}

	// Verify status was updated
	loaded, err := store.LoadMetadata(runID)
	if err != nil {
		t.Fatalf("LoadMetadata failed: %v", err)
	}
	if loaded.Status != EnsembleComplete {
		t.Errorf("Status = %q, want %q", loaded.Status, EnsembleComplete)
	}

	// Run should still exist
	if !store.RunExists(runID) {
		t.Error("run should still exist without cleanup")
	}
}

func TestCheckpointManager_MarkComplete_WithCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "cleanup-test"
	manager := NewCheckpointManager(store, runID)

	// Initialize metadata
	meta := CheckpointMetadata{
		RunID:        runID,
		Status:       EnsembleActive,
		CompletedIDs: []string{"mode-1"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Mark complete with cleanup
	if err := manager.MarkComplete(true); err != nil {
		t.Fatalf("MarkComplete with cleanup failed: %v", err)
	}

	// Run should be deleted
	if store.RunExists(runID) {
		t.Error("run should be deleted with cleanup")
	}
}

func TestCheckpointManager_MarkComplete_NilManager(t *testing.T) {
	var manager *CheckpointManager
	err := manager.MarkComplete(false)
	if err == nil {
		t.Error("MarkComplete on nil should return error")
	}
}

func TestCheckpointManager_GetResumeState(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewCheckpointStore(tmpDir)
	if err != nil {
		t.Fatalf("NewCheckpointStore failed: %v", err)
	}

	runID := "resume-test"
	manager := NewCheckpointManager(store, runID)

	// Initialize metadata
	meta := CheckpointMetadata{
		RunID:        runID,
		SessionName:  "test-session",
		Question:     "Test question?",
		PendingIDs:   []string{"mode-2"},
		CompletedIDs: []string{"mode-1"},
	}
	if err := store.SaveMetadata(meta); err != nil {
		t.Fatalf("SaveMetadata failed: %v", err)
	}

	// Save a completed checkpoint
	cp := ModeCheckpoint{
		ModeID: "mode-1",
		Output: &ModeOutput{ModeID: "mode-1", Thesis: "Result"},
		Status: string(AssignmentDone),
	}
	if err := store.SaveCheckpoint(runID, cp); err != nil {
		t.Fatalf("SaveCheckpoint failed: %v", err)
	}

	// Get resume state
	resumeMeta, outputs, err := manager.GetResumeState()
	if err != nil {
		t.Fatalf("GetResumeState failed: %v", err)
	}

	if resumeMeta == nil {
		t.Fatal("resumeMeta should not be nil")
	}
	if resumeMeta.SessionName != "test-session" {
		t.Errorf("SessionName = %q, want test-session", resumeMeta.SessionName)
	}
	if len(resumeMeta.PendingIDs) != 1 {
		t.Errorf("PendingIDs count = %d, want 1", len(resumeMeta.PendingIDs))
	}
	if len(outputs) != 1 {
		t.Errorf("outputs count = %d, want 1", len(outputs))
	}
	if outputs[0].Thesis != "Result" {
		t.Errorf("output Thesis = %q, want Result", outputs[0].Thesis)
	}
}

func TestCheckpointManager_GetResumeState_NilManager(t *testing.T) {
	var manager *CheckpointManager
	_, _, err := manager.GetResumeState()
	if err == nil {
		t.Error("GetResumeState on nil should return error")
	}
}
