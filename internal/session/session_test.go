package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// --- AgentConfig Tests ---

func TestAgentConfig_Total(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config AgentConfig
		want   int
	}{
		{
			name:   "empty",
			config: AgentConfig{},
			want:   0,
		},
		{
			name:   "claude only",
			config: AgentConfig{Claude: 3},
			want:   3,
		},
		{
			name:   "all types",
			config: AgentConfig{Claude: 2, Codex: 1, Gemini: 1, User: 1},
			want:   5,
		},
		{
			name:   "typical setup",
			config: AgentConfig{Claude: 2, Codex: 1, User: 1},
			want:   4,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.config.Total()
			if got != tt.want {
				t.Errorf("Total() = %d, want %d", got, tt.want)
			}
		})
	}
}

// --- sanitizeFilename Tests ---

func TestSanitizeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"with spaces", "with spaces"},
		{"with/slash", "with-slash"},
		{"with\\backslash", "with-backslash"},
		{"with:colon", "with-colon"},
		{"with*asterisk", "with_asterisk"},
		{"with?question", "with_question"},
		{"with\"quote", "with_quote"},
		{"with<less", "with_less"},
		{"with>greater", "with_greater"},
		{"with|pipe", "with_pipe"},
		{"complex/path:name*test?.json", "complex-path-name_test_.json"},
		{"", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := sanitizeFilename(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- StorageDir Tests ---

func TestStorageDir_XDGDataHome(t *testing.T) {
	// Cannot run in parallel due to environment variable manipulation

	// Save and restore original XDG_DATA_HOME
	original := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", original)

	// Set XDG_DATA_HOME and HOME (StorageDir should ignore XDG_DATA_HOME now)
	tmpDir := t.TempDir()
	homeDir := t.TempDir()
	os.Setenv("XDG_DATA_HOME", tmpDir)
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", homeDir)

	got := StorageDir()
	want := filepath.Join(homeDir, ".ntm", "sessions")

	if got != want {
		t.Errorf("StorageDir() = %q, want %q", got, want)
	}
}

func TestStorageDir_Default(t *testing.T) {
	// Cannot run in parallel due to environment variable manipulation

	// Save and restore original XDG_DATA_HOME
	original := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", original)

	// Clear XDG_DATA_HOME
	os.Setenv("XDG_DATA_HOME", "")

	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	homeDir := t.TempDir()
	os.Setenv("HOME", homeDir)

	got := StorageDir()

	// Should be under home directory
	expected := filepath.Join(homeDir, ".ntm", "sessions")
	if got != expected {
		t.Errorf("StorageDir() = %q, want %q", got, expected)
	}
}

// --- Storage Operations Tests ---

// setupTestStorage sets up an isolated storage directory for testing.
func setupTestStorage(t *testing.T) (string, func()) {
	t.Helper()

	// Save original env vars
	originalXDG := os.Getenv("XDG_DATA_HOME")
	originalHome := os.Getenv("HOME")

	// Create temp directory and set it as HOME (StorageDir now uses ~/.ntm)
	tmpDir := t.TempDir()
	os.Setenv("HOME", tmpDir)
	os.Setenv("XDG_DATA_HOME", tmpDir)

	// Return cleanup function
	cleanup := func() {
		os.Setenv("XDG_DATA_HOME", originalXDG)
		os.Setenv("HOME", originalHome)
	}

	return tmpDir, cleanup
}

func createTestState(name string) *SessionState {
	return &SessionState{
		Name:      name,
		SavedAt:   time.Now().UTC(),
		WorkDir:   "/test/project",
		GitBranch: "main",
		GitCommit: "abc123",
		Agents:    AgentConfig{Claude: 2, Codex: 1},
		Panes: []PaneState{
			{Title: "cc_1", Index: 0, AgentType: "cc", Active: true},
			{Title: "cc_2", Index: 1, AgentType: "cc", Active: false},
			{Title: "cod_1", Index: 2, AgentType: "cod", Active: false},
		},
		Layout:  "tiled",
		Version: StateVersion,
	}
}

func TestSave_Basic(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("test-session")
	opts := SaveOptions{Overwrite: true}

	path, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("Save() created file %s but it doesn't exist", path)
	}

	// Verify filename
	expectedFilename := "test-session.json"
	if filepath.Base(path) != expectedFilename {
		t.Errorf("Save() filename = %s, want %s", filepath.Base(path), expectedFilename)
	}
}

func TestSave_CustomName(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("original-name")
	opts := SaveOptions{Name: "custom-name", Overwrite: true}

	path, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	expectedFilename := "custom-name.json"
	if filepath.Base(path) != expectedFilename {
		t.Errorf("Save() filename = %s, want %s", filepath.Base(path), expectedFilename)
	}
}

func TestSave_NilState(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	if _, err := Save(nil, SaveOptions{Name: "nil-state", Overwrite: true}); err == nil {
		t.Fatal("Save() with nil state should fail")
	}
}

func TestSave_NoOverwrite(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("no-overwrite-test")
	opts := SaveOptions{Overwrite: true}

	// Save first time
	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("First Save() error = %v", err)
	}

	// Try to save again without overwrite
	opts.Overwrite = false
	_, err = Save(state, opts)
	if err == nil {
		t.Errorf("Save() without overwrite should fail, but succeeded")
	}
}

func TestSave_Overwrite(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("overwrite-test")
	opts := SaveOptions{Overwrite: true}

	// Save first time
	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("First Save() error = %v", err)
	}

	// Modify state
	state.GitBranch = "develop"

	// Save again with overwrite
	_, err = Save(state, opts)
	if err != nil {
		t.Fatalf("Second Save() with overwrite error = %v", err)
	}

	// Verify the change was saved
	loaded, err := Load("overwrite-test")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.GitBranch != "develop" {
		t.Errorf("Load().GitBranch = %s, want develop", loaded.GitBranch)
	}
}

func TestLoad_Basic(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	original := createTestState("load-test")
	opts := SaveOptions{Overwrite: true}

	_, err := Save(original, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load("load-test")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify key fields
	if loaded.Name != original.Name {
		t.Errorf("Load().Name = %s, want %s", loaded.Name, original.Name)
	}
	if loaded.WorkDir != original.WorkDir {
		t.Errorf("Load().WorkDir = %s, want %s", loaded.WorkDir, original.WorkDir)
	}
	if loaded.GitBranch != original.GitBranch {
		t.Errorf("Load().GitBranch = %s, want %s", loaded.GitBranch, original.GitBranch)
	}
	if loaded.Agents.Total() != original.Agents.Total() {
		t.Errorf("Load().Agents.Total() = %d, want %d", loaded.Agents.Total(), original.Agents.Total())
	}
	if len(loaded.Panes) != len(original.Panes) {
		t.Errorf("Load() pane count = %d, want %d", len(loaded.Panes), len(original.Panes))
	}
	if loaded.Version != original.Version {
		t.Errorf("Load().Version = %d, want %d", loaded.Version, original.Version)
	}
}

func TestLoad_NotFound(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	_, err := Load("nonexistent-session")
	if err == nil {
		t.Errorf("Load() for nonexistent session should fail")
	}
}

func TestStorageOperations_RejectEmptySanitizedName(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("")
	if _, err := Save(state, SaveOptions{Overwrite: true}); err == nil {
		t.Fatal("Save() with empty state name should fail")
	}

	state = createTestState("ignored")
	if _, err := Save(state, SaveOptions{Name: "   ", Overwrite: true}); err == nil {
		t.Fatal("Save() with empty sanitized custom name should fail")
	}
	if _, err := Save(state, SaveOptions{Name: ".", Overwrite: true}); err == nil {
		t.Fatal("Save() with '.' custom name should fail")
	}
	if _, err := Save(state, SaveOptions{Name: "..", Overwrite: true}); err == nil {
		t.Fatal("Save() with '..' custom name should fail")
	}

	if _, err := Load(""); err == nil {
		t.Fatal("Load() with empty name should fail")
	}
	if _, err := Load("   "); err == nil {
		t.Fatal("Load() with empty sanitized name should fail")
	}
	if _, err := Load("."); err == nil {
		t.Fatal("Load() with '.' should fail")
	}
	if _, err := Load(".."); err == nil {
		t.Fatal("Load() with '..' should fail")
	}

	if err := Delete(""); err == nil {
		t.Fatal("Delete() with empty name should fail")
	}
	if err := Delete("   "); err == nil {
		t.Fatal("Delete() with empty sanitized name should fail")
	}
	if err := Delete("."); err == nil {
		t.Fatal("Delete() with '.' should fail")
	}
	if err := Delete(".."); err == nil {
		t.Fatal("Delete() with '..' should fail")
	}

	if Exists("") {
		t.Fatal("Exists() with empty name should be false")
	}
	if Exists("   ") {
		t.Fatal("Exists() with empty sanitized name should be false")
	}
	if Exists(".") {
		t.Fatal("Exists() with '.' should be false")
	}
	if Exists("..") {
		t.Fatal("Exists() with '..' should be false")
	}
}

func TestRestore_NilState(t *testing.T) {
	t.Parallel()

	if err := Restore(nil, RestoreOptions{Name: "nil-state"}); err == nil {
		t.Fatal("Restore(nil, ...) should fail")
	}
}

func TestRestoreAgents_NilState(t *testing.T) {
	t.Parallel()

	if err := RestoreAgents("nil-state", nil, AgentCommands{}); err == nil {
		t.Fatal("RestoreAgents with nil state should fail")
	}
}

func TestDelete_Basic(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("delete-test")
	opts := SaveOptions{Overwrite: true}

	path, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("File should exist before delete")
	}

	// Delete
	err = Delete("delete-test")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify file is gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("File should not exist after delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	err := Delete("nonexistent-session")
	if err == nil {
		t.Errorf("Delete() for nonexistent session should fail")
	}
}

func TestExists_True(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("exists-test")
	opts := SaveOptions{Overwrite: true}

	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if !Exists("exists-test") {
		t.Errorf("Exists() = false, want true")
	}
}

func TestExists_False(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	if Exists("nonexistent-session") {
		t.Errorf("Exists() = true, want false")
	}
}

func TestList_Empty(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(sessions) != 0 {
		t.Errorf("List() returned %d sessions, want 0", len(sessions))
	}
}

func TestList_Multiple(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	// Create multiple sessions
	for _, name := range []string{"session-a", "session-b", "session-c"} {
		state := createTestState(name)
		opts := SaveOptions{Overwrite: true}
		if _, err := Save(state, opts); err != nil {
			t.Fatalf("Save(%s) error = %v", name, err)
		}
		// Small delay to ensure different timestamps
		time.Sleep(10 * time.Millisecond)
	}

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(sessions) != 3 {
		t.Fatalf("List() returned %d sessions, want 3", len(sessions))
	}

	// Verify sorted by time (newest first)
	for i := 0; i < len(sessions)-1; i++ {
		if sessions[i].SavedAt.Before(sessions[i+1].SavedAt) {
			t.Errorf("List() not sorted by time (newest first)")
		}
	}
}

func TestList_SessionInfo(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	state := createTestState("info-test")
	state.WorkDir = "/home/user/project"
	state.GitBranch = "feature-branch"
	opts := SaveOptions{Overwrite: true}

	_, err := Save(state, opts)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	sessions, err := List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(sessions) != 1 {
		t.Fatalf("List() returned %d sessions, want 1", len(sessions))
	}

	s := sessions[0]
	if s.Name != "info-test" {
		t.Errorf("List()[0].Name = %s, want info-test", s.Name)
	}
	if s.WorkDir != "/home/user/project" {
		t.Errorf("List()[0].WorkDir = %s, want /home/user/project", s.WorkDir)
	}
	if s.GitBranch != "feature-branch" {
		t.Errorf("List()[0].GitBranch = %s, want feature-branch", s.GitBranch)
	}
	if s.Agents != state.Agents.Total() {
		t.Errorf("List()[0].Agents = %d, want %d", s.Agents, state.Agents.Total())
	}
	if s.FileSize == 0 {
		t.Errorf("List()[0].FileSize = 0, want > 0")
	}
}

// --- Sanitize Roundtrip Test ---

func TestSanitize_Roundtrip(t *testing.T) {
	_, cleanup := setupTestStorage(t)
	defer cleanup()

	// Test that sanitized names work for save/load
	names := []string{
		"simple",
		"with-hyphen",
		"with_underscore",
		"with.period",
		"with spaces",
		"project/branch", // Will be sanitized to project-branch
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			state := createTestState(name)
			opts := SaveOptions{Overwrite: true}

			_, err := Save(state, opts)
			if err != nil {
				t.Fatalf("Save(%s) error = %v", name, err)
			}

			sanitized := sanitizeFilename(name)
			loaded, err := Load(sanitized)
			if err != nil {
				t.Fatalf("Load(%s) error = %v", sanitized, err)
			}

			if loaded.Name != name {
				t.Errorf("Load().Name = %s, want %s", loaded.Name, name)
			}
		})
	}
}

// --- Session Recovery Helper Tests ---

func TestGetAgentCommand(t *testing.T) {
	t.Parallel()

	cmds := AgentCommands{
		Claude:   "claude --flag",
		Codex:    "codex-cli run",
		Gemini:   "gemini start",
		Cursor:   "cursor agent",
		Windsurf: "windsurf start",
		Aider:    "aider --watch",
		Opencode: "opencode run",
		Ollama:   "ollama serve",
	}

	tests := []struct {
		agentType string
		want      string
	}{
		{"cc", "claude --flag"},
		{"claude", "claude --flag"},
		{"claude_code", "claude --flag"},
		{"cod", "codex-cli run"},
		{"codex", "codex-cli run"},
		{"openai-codex", "codex-cli run"},
		{"gmi", "gemini start"},
		{"gemini", "gemini start"},
		{"google-gemini", "gemini start"},
		{"cursor", "cursor agent"},
		{"windsurf", "windsurf start"},
		{"ws", "windsurf start"},
		{"aider", "aider --watch"},
		{"oc", "opencode run"},
		{"opencode", "opencode run"},
		{"ollama", "ollama serve"},
		{"unknown", ""},
		{"", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.agentType, func(t *testing.T) {
			t.Parallel()
			got := getAgentCommand(tt.agentType, cmds)
			if got != tt.want {
				t.Errorf("getAgentCommand(%q) = %q, want %q", tt.agentType, got, tt.want)
			}
		})
	}
}

func TestShouldCreateDir(t *testing.T) {
	// Cannot run in parallel due to home directory dependency

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot get home directory")
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "two levels under home",
			path: filepath.Join(home, "Developer", "project"),
			want: true,
		},
		{
			name: "three levels under home",
			path: filepath.Join(home, "Developer", "org", "project"),
			want: true,
		},
		{
			name: "one level under home",
			path: filepath.Join(home, "project"),
			want: false,
		},
		{
			name: "home itself",
			path: home,
			want: false,
		},
		{
			name: "root",
			path: "/",
			want: false,
		},
		{
			name: "outside home",
			path: "/tmp/project",
			want: false,
		},
		{
			name: "etc dir",
			path: "/etc/something",
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := shouldCreateDir(tt.path)
			if got != tt.want {
				t.Errorf("shouldCreateDir(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// --- RestoreOptions and SaveOptions Tests ---

func TestRestoreOptions_Defaults(t *testing.T) {
	t.Parallel()

	opts := RestoreOptions{}

	// Verify defaults
	if opts.Name != "" {
		t.Errorf("RestoreOptions.Name default = %q, want empty", opts.Name)
	}
	if opts.SkipGitCheck {
		t.Errorf("RestoreOptions.SkipGitCheck default = true, want false")
	}
	if opts.Force {
		t.Errorf("RestoreOptions.Force default = true, want false")
	}
}

func TestSaveOptions_Defaults(t *testing.T) {
	t.Parallel()

	opts := SaveOptions{}

	// Verify defaults
	if opts.Name != "" {
		t.Errorf("SaveOptions.Name default = %q, want empty", opts.Name)
	}
	if opts.Overwrite {
		t.Errorf("SaveOptions.Overwrite default = true, want false")
	}
	if opts.IncludeGit {
		t.Errorf("SaveOptions.IncludeGit default = true, want false")
	}
	if opts.Description != "" {
		t.Errorf("SaveOptions.Description default = %q, want empty", opts.Description)
	}
}

// --- countAgents Tests ---

func TestCountAgents(t *testing.T) {
	t.Parallel()

	t.Run("empty panes", func(t *testing.T) {
		t.Parallel()
		cfg := countAgents(nil)
		if cfg.Total() != 0 {
			t.Errorf("Total() = %d, want 0", cfg.Total())
		}
	})

	t.Run("one of each type", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentCodex},
			{Type: tmux.AgentGemini},
			{Type: tmux.AgentCursor},
			{Type: tmux.AgentWindsurf},
			{Type: tmux.AgentAider},
			{Type: tmux.AgentUser},
		}
		cfg := countAgents(panes)
		if cfg.Claude != 1 {
			t.Errorf("Claude = %d, want 1", cfg.Claude)
		}
		if cfg.Codex != 1 {
			t.Errorf("Codex = %d, want 1", cfg.Codex)
		}
		if cfg.Gemini != 1 {
			t.Errorf("Gemini = %d, want 1", cfg.Gemini)
		}
		if cfg.Cursor != 1 {
			t.Errorf("Cursor = %d, want 1", cfg.Cursor)
		}
		if cfg.Windsurf != 1 {
			t.Errorf("Windsurf = %d, want 1", cfg.Windsurf)
		}
		if cfg.Aider != 1 {
			t.Errorf("Aider = %d, want 1", cfg.Aider)
		}
		if cfg.User != 1 {
			t.Errorf("User = %d, want 1", cfg.User)
		}
		if cfg.Total() != 7 {
			t.Errorf("Total() = %d, want 7", cfg.Total())
		}
	})

	t.Run("multiple of same type", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentClaude},
			{Type: tmux.AgentCodex},
		}
		cfg := countAgents(panes)
		if cfg.Claude != 3 {
			t.Errorf("Claude = %d, want 3", cfg.Claude)
		}
		if cfg.Codex != 1 {
			t.Errorf("Codex = %d, want 1", cfg.Codex)
		}
		if cfg.Total() != 4 {
			t.Errorf("Total() = %d, want 4", cfg.Total())
		}
	})

	t.Run("unknown type ignored", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Type: tmux.AgentUnknown},
			{Type: tmux.AgentClaude},
		}
		cfg := countAgents(panes)
		if cfg.Claude != 1 {
			t.Errorf("Claude = %d, want 1", cfg.Claude)
		}
		if cfg.Total() != 1 {
			t.Errorf("Total() = %d, want 1 (unknown should not count)", cfg.Total())
		}
	})
}

// --- mapPaneStates Tests ---

func TestMapPaneStates(t *testing.T) {
	t.Parallel()

	t.Run("empty panes", func(t *testing.T) {
		t.Parallel()
		states := mapPaneStates(nil, "")
		if len(states) != 0 {
			t.Errorf("expected empty for nil input, got len=%d", len(states))
		}
	})

	t.Run("single pane preserves fields", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{
				ID:      "%5",
				Index:   2,
				Title:   "myproject__cc_1_opus",
				Type:    tmux.AgentClaude,
				Variant: "opus",
				Active:  true,
				Width:   120,
				Height:  40,
			},
		}
		states := mapPaneStates(panes, "")
		if len(states) != 1 {
			t.Fatalf("len = %d, want 1", len(states))
		}
		s := states[0]
		if s.Title != "myproject__cc_1_opus" {
			t.Errorf("Title = %q", s.Title)
		}
		if s.Index != 2 {
			t.Errorf("Index = %d, want 2", s.Index)
		}
		if s.AgentType != string(tmux.AgentClaude) {
			t.Errorf("AgentType = %q", s.AgentType)
		}
		if s.Model != "opus" {
			t.Errorf("Model = %q, want opus", s.Model)
		}
		if !s.Active {
			t.Error("Active should be true")
		}
		if s.Width != 120 {
			t.Errorf("Width = %d, want 120", s.Width)
		}
		if s.Height != 40 {
			t.Errorf("Height = %d, want 40", s.Height)
		}
		if s.PaneID != "%5" {
			t.Errorf("PaneID = %q, want %%5", s.PaneID)
		}
	})

	t.Run("multiple panes preserve order", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{Index: 0, Type: tmux.AgentUser, Title: "bash"},
			{Index: 1, Type: tmux.AgentClaude, Title: "proj__cc_1"},
			{Index: 2, Type: tmux.AgentCodex, Title: "proj__cod_1"},
		}
		states := mapPaneStates(panes, "")
		if len(states) != 3 {
			t.Fatalf("len = %d, want 3", len(states))
		}
		for i, s := range states {
			if s.Index != i {
				t.Errorf("states[%d].Index = %d, want %d", i, s.Index, i)
			}
		}
		if states[0].AgentType != string(tmux.AgentUser) {
			t.Errorf("states[0].AgentType = %q, want user", states[0].AgentType)
		}
		if states[1].AgentType != string(tmux.AgentClaude) {
			t.Errorf("states[1].AgentType = %q, want cc", states[1].AgentType)
		}
		if states[2].AgentType != string(tmux.AgentCodex) {
			t.Errorf("states[2].AgentType = %q, want cod", states[2].AgentType)
		}
	})
}

// TestWindowCreationOrder verifies the distinct window indices are returned in
// first-appearance order (the order Restore creates windows), which is how
// restoreWindowFidelity maps saved windows to freshly-created tmux windows.
func TestWindowCreationOrder(t *testing.T) {
	t.Parallel()

	t.Run("distinct in first-appearance order", func(t *testing.T) {
		t.Parallel()
		// Already sorted by (WindowIndex, Index) as Restore sorts before calling.
		panes := []PaneState{
			{WindowIndex: 0, Index: 0},
			{WindowIndex: 0, Index: 1},
			{WindowIndex: 2, Index: 0},
			{WindowIndex: 2, Index: 1},
			{WindowIndex: 5, Index: 0},
		}
		got := windowCreationOrder(panes)
		want := []int{0, 2, 5}
		if len(got) != len(want) {
			t.Fatalf("len = %d (%v), want %d (%v)", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("order[%d] = %d, want %d", i, got[i], want[i])
			}
		}
	})

	t.Run("empty input", func(t *testing.T) {
		t.Parallel()
		if got := windowCreationOrder(nil); len(got) != 0 {
			t.Errorf("nil panes -> %v, want empty", got)
		}
	})
}

// TestParseWindowList verifies the `list-windows` output parser used by capture
// (ntm-r3k0/ntm-fphu). It must extract index/name/active/zoom/layout per line
// and skip blank, under-delimited, or non-numeric-index lines. The separator is
// a printable token (tmux escapes control bytes in format output).
func TestParseWindowList(t *testing.T) {
	t.Parallel()

	const sep = "_NTM_SEP_"
	out := "1" + sep + "editor" + sep + "1" + sep + "0" + sep + "d4cd,200x50,0,0{100x50,0,0,1,99x50,101,0,2}\n" +
		"2" + sep + "logs" + sep + "0" + sep + "1" + sep + "b4ca,200x50,0,0[200x25,0,0,3,200x24,0,26,4]\n" +
		"\n" + // blank line -> skipped
		"x" + sep + "bad" + sep + "0" + sep + "0" + sep + "layout" + "\n" + // non-numeric index -> skipped
		"3" + sep + "tooFew" // under-delimited -> skipped

	got := parseWindowList(out, sep)
	if len(got) != 2 {
		t.Fatalf("parsed %d windows, want 2: %+v", len(got), got)
	}

	w0 := got[0]
	if w0.Index != 1 || w0.Name != "editor" || !w0.Active || w0.Zoomed ||
		w0.Layout != "d4cd,200x50,0,0{100x50,0,0,1,99x50,101,0,2}" {
		t.Errorf("window[0] = %+v", w0)
	}

	w1 := got[1]
	if w1.Index != 2 || w1.Name != "logs" || w1.Active || !w1.Zoomed ||
		w1.Layout != "b4ca,200x50,0,0[200x25,0,0,3,200x24,0,26,4]" {
		t.Errorf("window[1] = %+v", w1)
	}

	if got := parseWindowList("", sep); len(got) != 0 {
		t.Errorf("empty output -> %v, want no windows", got)
	}
}

// TestSessionState_WindowsRoundTrip verifies the per-window fidelity metadata
// (ntm-r3k0/ntm-fphu) and the per-pane rendered Command (ntm-boi0) survive the
// JSON serialization that backs saved-session files.
func TestSessionState_WindowsRoundTrip(t *testing.T) {
	t.Parallel()

	orig := &SessionState{
		Name: "demo",
		Panes: []PaneState{
			{Index: 0, WindowIndex: 0, AgentType: "cc", Model: "opus", Command: "claude --model opus", Active: true},
		},
		Layout: "tiled",
		Windows: []WindowState{
			{Index: 0, Name: "main", Layout: "abcd,80x24,0,0,1", Active: true, Zoomed: true},
		},
		Version: StateVersion,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SessionState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(got.Windows) != 1 {
		t.Fatalf("windows lost in round-trip: %+v", got.Windows)
	}
	w := got.Windows[0]
	if w.Name != "main" || w.Layout != "abcd,80x24,0,0,1" || !w.Active || !w.Zoomed {
		t.Errorf("window round-trip mismatch: %+v", w)
	}
	if got.Panes[0].Command != "claude --model opus" {
		t.Errorf("pane Command lost in round-trip: %q", got.Panes[0].Command)
	}
}

// --- Pane record (per-pane provider session id) tests ---
//
// The pane record works around agentsession.Discover returning only the
// single newest session per (agentType, workDir): same-cwd panes would
// otherwise all save the same id. Spawn writes the roster with empty ids;
// save reconciles and lazily fills them.

func TestWritePaneRecordAtSpawn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
		{ID: "%0", Index: 2, WindowIndex: 0, Title: "bash", Type: tmux.AgentUser},
	}

	if err := writePaneRecord(dir, "proj", panes); err != nil {
		t.Fatalf("writePaneRecord: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "pane-records", "proj.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	if strings.Contains(string(raw), "session_id") {
		t.Errorf("spawn-written record must omit session fields, got:\n%s", raw)
	}

	var rec PaneRecordFile
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if rec.Version != 1 {
		t.Errorf("Version = %d, want 1", rec.Version)
	}
	if len(rec.Entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (user pane skipped)", len(rec.Entries))
	}
	wantKeys := []paneRecordKey{
		{windowIndex: 0, agentType: "cc", ntmIndex: 1},
		{windowIndex: 0, agentType: "cc", ntmIndex: 2},
	}
	for i, want := range wantKeys {
		e := rec.Entries[i]
		if got := (paneRecordKey{windowIndex: e.WindowIndex, agentType: e.AgentType, ntmIndex: e.NTMIndex}); got != want {
			t.Errorf("entries[%d] key = %+v, want %+v", i, got, want)
		}
		if e.SessionID != "" || e.SessionProvider != "" || e.SessionFile != "" {
			t.Errorf("entries[%d] must start empty, got %+v", i, e)
		}
	}
}

func TestWritePaneRecordZeroAgentPanes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	panes := []tmux.Pane{
		{ID: "%0", Index: 0, WindowIndex: 0, Title: "bash", Type: tmux.AgentUser},
	}
	if err := writePaneRecord(dir, "empty", panes); err != nil {
		t.Fatalf("writePaneRecord: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pane-records", "empty.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var rec PaneRecordFile
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if rec.Version != 1 {
		t.Errorf("Version = %d, want 1", rec.Version)
	}
	if rec.Entries == nil || len(rec.Entries) != 0 {
		t.Errorf("entries = %+v, want empty non-nil array", rec.Entries)
	}
}

func TestAppendPaneRecordOnAdd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seed := []PaneRecordEntry{
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "id-1", SessionProvider: "claude", SessionFile: "/f/1"},
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 2},
	}
	if err := storePaneRecord(dir, "proj", seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	newPane := tmux.Pane{ID: "%3", Index: 2, WindowIndex: 0, NTMIndex: 3, Title: "proj__cc_3", Type: tmux.AgentClaude}
	if err := appendPaneRecord(dir, "proj", newPane); err != nil {
		t.Fatalf("appendPaneRecord: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "pane-records", "proj.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	// Exact on-wire shape (golden): envelope plus one object per entry.
	wantJSON := `{
  "version": 1,
  "entries": [
    {
      "window_index": 0,
      "agent_type": "cc",
      "ntm_index": 1,
      "session_id": "id-1",
      "session_provider": "claude",
      "session_file": "/f/1"
    },
    {
      "window_index": 0,
      "agent_type": "cc",
      "ntm_index": 2
    },
    {
      "window_index": 0,
      "agent_type": "cc",
      "ntm_index": 3
    }
  ]
}`
	if string(raw) != wantJSON {
		t.Errorf("record JSON mismatch.\ngot:\n%s\nwant:\n%s", raw, wantJSON)
	}

	var rec PaneRecordFile
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("parse record: %v", err)
	}
	if len(rec.Entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3", len(rec.Entries))
	}
	for i, want := range seed {
		if rec.Entries[i] != want {
			t.Errorf("entries[%d] = %+v, want %+v (old entries must be untouched)", i, rec.Entries[i], want)
		}
	}
	last := rec.Entries[2]
	if last.WindowIndex != 0 || last.AgentType != "cc" || last.NTMIndex != 3 {
		t.Errorf("new entry key = %+v, want (0 cc 3)", last)
	}
	if last.SessionID != "" || last.SessionProvider != "" || last.SessionFile != "" {
		t.Errorf("new entry must start empty, got %+v", last)
	}
}

func TestAppendPaneRecordEdges(t *testing.T) {
	t.Parallel()

	t.Run("missing record creates single entry", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		pane := tmux.Pane{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude}
		if err := appendPaneRecord(dir, "proj", pane); err != nil {
			t.Fatalf("appendPaneRecord: %v", err)
		}
		got := loadPaneRecord(dir, "proj")
		if len(got) != 1 {
			t.Fatalf("len = %d, want 1", len(got))
		}
		if got[0].NTMIndex != 1 || got[0].SessionID != "" {
			t.Errorf("entry = %+v, want fresh empty cc_1", got[0])
		}
	})

	t.Run("user pane leaves record untouched", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		user := tmux.Pane{ID: "%0", Index: 0, WindowIndex: 0, Title: "bash", Type: tmux.AgentUser}
		if err := appendPaneRecord(dir, "proj", user); err != nil {
			t.Fatalf("appendPaneRecord: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "pane-records", "proj.json")); !os.IsNotExist(err) {
			t.Errorf("user-pane append must not create a record file")
		}
	})

	t.Run("re-added pane never inherits killed pane id", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		seed := []PaneRecordEntry{
			{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "dead-id", SessionProvider: "claude", SessionFile: "/f/dead"},
			{WindowIndex: 0, AgentType: "cc", NTMIndex: 2, SessionID: "id-2", SessionProvider: "claude", SessionFile: "/f/2"},
		}
		if err := storePaneRecord(dir, "proj", seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		readded := tmux.Pane{ID: "%9", Index: 5, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude}
		if err := appendPaneRecord(dir, "proj", readded); err != nil {
			t.Fatalf("appendPaneRecord: %v", err)
		}
		got := loadPaneRecord(dir, "proj")
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (replace, not duplicate)", len(got))
		}
		if got[1].NTMIndex != 1 || got[1].SessionID != "" || got[1].SessionFile != "" {
			t.Errorf("re-added entry = %+v, want empty cc_1, not dead-id", got[1])
		}
		if got[0] != seed[1] {
			t.Errorf("unrelated entry changed: %+v, want %+v", got[0], seed[1])
		}
	})
}

func TestAppendPaneRecordConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := storePaneRecord(dir, "proj", []PaneRecordEntry{
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 1},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	newPanes := []tmux.Pane{
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
		{ID: "%3", Index: 2, WindowIndex: 0, NTMIndex: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(newPanes))
	for i, pane := range newPanes {
		wg.Add(1)
		go func(i int, pane tmux.Pane) {
			defer wg.Done()
			unlock, err := acquireLockAt(dir)
			if err != nil {
				errs[i] = err
				return
			}
			defer unlock()
			errs[i] = appendPaneRecord(dir, "proj", pane)
		}(i, pane)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	got := loadPaneRecord(dir, "proj")
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (no lost appends)", len(got))
	}
	seen := map[paneRecordKey]bool{}
	for _, e := range got {
		seen[paneRecordKeyOfEntry(e)] = true
	}
	for _, want := range []paneRecordKey{
		{windowIndex: 0, agentType: "cc", ntmIndex: 1},
		{windowIndex: 0, agentType: "cc", ntmIndex: 2},
		{windowIndex: 0, agentType: "cod", ntmIndex: 1},
	} {
		if !seen[want] {
			t.Errorf("missing entry for key %+v in %+v", want, got)
		}
	}
}

func TestWritePaneRecordRerunPreservesFilledIDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seed := []PaneRecordEntry{
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "id-1", SessionProvider: "claude", SessionFile: "/f/1"},
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 2},
	}
	if err := storePaneRecord(dir, "proj", seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A spawn re-run sees only empty-id panes; filled ids must survive.
	rerun := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
	}
	if err := writePaneRecord(dir, "proj", rerun); err != nil {
		t.Fatalf("writePaneRecord: %v", err)
	}
	got := loadPaneRecord(dir, "proj")
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0] != seed[0] {
		t.Errorf("filled entry regressed: %+v, want %+v", got[0], seed[0])
	}
}

func TestPaneRecordLifecycle(t *testing.T) {
	// Mutates HOME to redirect StorageDir; cannot run in parallel.
	home := t.TempDir()
	oldHome := os.Getenv("HOME")
	defer os.Setenv("HOME", oldHome)
	os.Setenv("HOME", home)

	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
	}
	activeSidecar := filepath.Join(StorageDir(), "pane-records", "proj.json")
	archivedSidecar := filepath.Join(ArchiveDir(), "pane-records", "proj.json")
	saveState := func() {
		t.Helper()
		state := &SessionState{Name: "proj", SavedAt: time.Now().UTC(), Version: StateVersion}
		if _, err := Save(state, SaveOptions{Overwrite: true}); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	if err := WritePaneRecord("proj", panes); err != nil {
		t.Fatalf("WritePaneRecord: %v", err)
	}
	if !exists(activeSidecar) {
		t.Fatalf("sidecar not written at %s", activeSidecar)
	}

	// Delete drops the sidecar.
	saveState()
	if err := Delete("proj"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if exists(activeSidecar) {
		t.Errorf("Delete left the sidecar behind")
	}

	// Archive moves the sidecar alongside the archived state.
	if err := WritePaneRecord("proj", panes); err != nil {
		t.Fatalf("WritePaneRecord: %v", err)
	}
	saveState()
	if _, err := Archive("proj"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if exists(activeSidecar) {
		t.Errorf("Archive left the sidecar in the active store")
	}
	if !exists(archivedSidecar) {
		t.Errorf("Archive did not move the sidecar to %s", archivedSidecar)
	}

	// Unarchive restores it.
	if _, err := Unarchive("proj"); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if !exists(activeSidecar) {
		t.Errorf("Unarchive did not restore the sidecar")
	}
	if exists(archivedSidecar) {
		t.Errorf("Unarchive left the sidecar in the archive store")
	}
}

func TestReconcilePaneRecord(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
		{ID: "%3", Index: 2, WindowIndex: 0, NTMIndex: 1, Title: "proj__cod_1", Type: tmux.AgentCodex},
		{ID: "%4", Index: 3, WindowIndex: 0, NTMIndex: 2, Title: "proj__cod_2", Type: tmux.AgentCodex},
	}
	cwds := []string{"/proj", "/proj", "/shared", "/shared"}
	record := []PaneRecordEntry{
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "id-a", SessionProvider: "claude", SessionFile: "/f/a"},
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 2, SessionID: "id-b", SessionProvider: "claude", SessionFile: "/f/b"},
	}
	calls := map[string]int{}
	fakeDiscover := func(agentType, cwd string) *agentsession.Info {
		calls[agentType+"|"+cwd]++
		return &agentsession.Info{AgentType: agentType, SessionID: "disc-x", Provider: "codex", SourcePath: "/f/disc"}
	}

	states, refreshed, _ := reconcilePaneIDs(panes, cwds, record, fakeDiscover)

	if len(calls) != 1 || calls["cod|/shared"] != 1 {
		t.Errorf("discover calls = %v, want exactly one cod|/shared call (recorded panes need none; same provider+cwd memoizes)", calls)
	}
	if len(states) != 4 {
		t.Fatalf("len(states) = %d, want 4", len(states))
	}
	if states[0].SessionID != "id-a" || states[0].SessionProvider != "claude" || states[0].SessionFile != "/f/a" {
		t.Errorf("states[0] = %+v, want recorded id-a", states[0])
	}
	if states[1].SessionID != "id-b" {
		t.Errorf("states[1] = %+v, want recorded id-b", states[1])
	}
	if states[2].SessionID != "disc-x" {
		t.Errorf("states[2] = %+v, want lazily filled disc-x", states[2])
	}
	if states[3].SessionID != "" || states[3].SessionFile != "" {
		t.Errorf("states[3] = %+v, want empty (deduped duplicate of disc-x)", states[3])
	}
	if len(refreshed) != 4 {
		t.Errorf("len(refreshed) = %d, want 4", len(refreshed))
	}
}

func TestLazyFillDedupesAcrossSameProviderPanes(t *testing.T) {
	t.Parallel()
	// Fed in reverse (WindowIndex, Index) order: D sorts after C.
	panes := []tmux.Pane{
		{ID: "%4", Index: 1, WindowIndex: 0, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
		{ID: "%3", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
	}
	cwds := []string{"/proj", "/proj"}
	fakeDiscover := func(agentType, cwd string) *agentsession.Info {
		return &agentsession.Info{AgentType: agentType, SessionID: "disc-x", Provider: "claude", SourcePath: "/f/disc"}
	}

	states, refreshed, _ := reconcilePaneIDs(panes, cwds, nil, fakeDiscover)

	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	// Output follows input order (D first), but the id goes to the
	// sorted-first pane (C).
	if states[0].SessionID != "" {
		t.Errorf("states[0] (D, sorted-second) = %q, want empty", states[0].SessionID)
	}
	if states[1].SessionID != "disc-x" {
		t.Errorf("states[1] (C, sorted-first) = %q, want disc-x", states[1].SessionID)
	}
	if len(refreshed) != 2 || refreshed[0].SessionID != "disc-x" || refreshed[1].SessionID != "" {
		t.Errorf("refreshed = %+v, want [disc-x \"\"] in sorted order", refreshed)
	}
}

func TestDedupeKeysByProviderNotType(t *testing.T) {
	t.Parallel()
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__gmi_1", Type: tmux.AgentGemini},
		{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "proj__agy_1", Type: tmux.AgentAntigravity},
	}
	cwds := []string{"/proj", "/proj"}
	fakeDiscover := func(agentType, cwd string) *agentsession.Info {
		return &agentsession.Info{AgentType: agentType, SessionID: "shared-raw", Provider: "p-" + agentType, SourcePath: "/f/" + agentType}
	}

	states, _, _ := reconcilePaneIDs(panes, cwds, nil, fakeDiscover)

	if len(states) != 2 {
		t.Fatalf("len(states) = %d, want 2", len(states))
	}
	// gmi and agy are distinct providers: the same raw id must NOT dedupe.
	if states[0].SessionID != "shared-raw" {
		t.Errorf("states[0] (gmi) = %q, want shared-raw", states[0].SessionID)
	}
	if states[1].SessionID != "shared-raw" {
		t.Errorf("states[1] (agy) = %q, want shared-raw", states[1].SessionID)
	}
}

func TestStaleRecordEntriesDroppedOnSave(t *testing.T) {
	t.Parallel()
	record := []PaneRecordEntry{
		{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "id-a", SessionProvider: "claude", SessionFile: "/f/a"},
		{WindowIndex: 9, AgentType: "cc", NTMIndex: 9, SessionID: "id-z", SessionProvider: "claude", SessionFile: "/f/z"},
	}
	panes := []tmux.Pane{
		{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
	}
	cwds := []string{"/proj"}
	discoverCalls := 0
	nilDiscover := func(agentType, cwd string) *agentsession.Info {
		discoverCalls++
		return nil // idle agent: nothing discoverable this cycle
	}

	states, refreshed, _ := reconcilePaneIDs(panes, cwds, record, nilDiscover)

	if discoverCalls != 0 {
		t.Errorf("recorded pane triggered %d discover calls, want 0", discoverCalls)
	}
	if len(states) != 1 || states[0].SessionID != "id-a" {
		t.Errorf("states = %+v, want [id-a]", states)
	}
	// The killed pane Z is dropped, and A's filled id is never regressed to
	// empty even though lazy fill yielded nothing this cycle.
	if len(refreshed) != 1 {
		t.Fatalf("refreshed = %+v, want only A", refreshed)
	}
	if refreshed[0] != record[0] {
		t.Errorf("refreshed[0] = %+v, want %+v (old id intact)", refreshed[0], record[0])
	}

	// File level: the refresh rewrite drops Z from the stored record.
	dir := t.TempDir()
	if err := storePaneRecord(dir, "proj", record); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := upsertPaneRecordEntries(dir, "proj", refreshed); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got := loadPaneRecord(dir, "proj")
	if len(got) != 1 || got[0] != record[0] {
		t.Errorf("stored after refresh = %+v, want [%+v]", got, record[0])
	}
}

func TestReconcilePaneIDsDedupesCollidingKeys(t *testing.T) {
	t.Parallel()

	t.Run("colliding pair keeps sorted-first winner and drops loser", func(t *testing.T) {
		t.Parallel()
		// Fed reversed: %1 sorts first by (WindowIndex, Index) and wins.
		panes := []tmux.Pane{
			{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "RubyDog", Type: tmux.AgentClaude},
			{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
		}
		cwds := []string{"/proj", "/proj"}
		// Already-duplicated stored record as persisted by the buggy save.
		record := []PaneRecordEntry{
			{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "sess-1", SessionProvider: "claude", SessionFile: "/f/1"},
			{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "sess-1", SessionProvider: "claude", SessionFile: "/f/1"},
		}
		fakeDiscover := func(agentType, cwd string) *agentsession.Info {
			return &agentsession.Info{AgentType: agentType, SessionID: "disc-x", Provider: "claude", SourcePath: "/f/disc"}
		}

		states, refreshed, collisions := reconcilePaneIDs(panes, cwds, record, fakeDiscover)

		if len(states) != 2 {
			t.Fatalf("len(states) = %d, want 2", len(states))
		}
		// Input order preserved: states[0] is the loser (%2), states[1] winner (%1).
		if states[0].SessionID != "" || states[0].SessionProvider != "" || states[0].SessionFile != "" {
			t.Errorf("loser state = %+v, want all session fields empty", states[0])
		}
		if states[1].SessionID != "sess-1" || states[1].SessionProvider != "claude" || states[1].SessionFile != "/f/1" {
			t.Errorf("winner state = %+v, want sess-1/claude//f/1", states[1])
		}
		if len(refreshed) != 1 {
			t.Fatalf("len(refreshed) = %d, want exactly 1 entry for the colliding key: %+v", len(refreshed), refreshed)
		}
		if refreshed[0] != record[0] {
			t.Errorf("refreshed[0] = %+v, want winner entry %+v", refreshed[0], record[0])
		}
		if len(collisions) != 1 {
			t.Fatalf("len(collisions) = %d, want 1: %+v", len(collisions), collisions)
		}
		wantKey := paneRecordKey{windowIndex: 0, agentType: "cc", ntmIndex: 1}
		if collisions[0].Key != wantKey {
			t.Errorf("collision key = %+v, want %+v", collisions[0].Key, wantKey)
		}
		if collisions[0].WinnerPaneID != "%1" || collisions[0].LoserPaneID != "%2" {
			t.Errorf("collision = %+v, want winner %%1 loser %%2", collisions[0])
		}
	})

	t.Run("distinct-cwd lazy-fill pair dedupes by key and clears loser", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
			{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 1, Title: "RubyDog", Type: tmux.AgentClaude},
		}
		cwds := []string{"/a", "/b"}
		fakeDiscover := func(agentType, cwd string) *agentsession.Info {
			if cwd == "/a" {
				return &agentsession.Info{AgentType: agentType, SessionID: "sess-a", Provider: "claude", SourcePath: "/f/a"}
			}
			return &agentsession.Info{AgentType: agentType, SessionID: "sess-b", Provider: "claude", SourcePath: "/f/b"}
		}

		states, refreshed, collisions := reconcilePaneIDs(panes, cwds, nil, fakeDiscover)

		if len(states) != 2 {
			t.Fatalf("len(states) = %d, want 2", len(states))
		}
		if states[0].SessionID != "sess-a" || states[0].SessionProvider != "claude" || states[0].SessionFile != "/f/a" {
			t.Errorf("winner state = %+v, want sess-a/claude//f/a", states[0])
		}
		if states[1].SessionID != "" || states[1].SessionProvider != "" || states[1].SessionFile != "" {
			t.Errorf("loser state = %+v, want all session fields empty (discovered sess-b discarded)", states[1])
		}
		if len(refreshed) != 1 || refreshed[0].SessionID != "sess-a" {
			t.Fatalf("refreshed = %+v, want exactly one entry with sess-a", refreshed)
		}
		if len(collisions) != 1 || collisions[0].WinnerPaneID != "%1" || collisions[0].LoserPaneID != "%2" {
			t.Errorf("collisions = %+v, want one %%1->%%2 collision", collisions)
		}
	})

	t.Run("distinct keys never warn or drop", func(t *testing.T) {
		t.Parallel()
		panes := []tmux.Pane{
			{ID: "%1", Index: 0, WindowIndex: 0, NTMIndex: 1, Title: "proj__cc_1", Type: tmux.AgentClaude},
			{ID: "%2", Index: 1, WindowIndex: 0, NTMIndex: 2, Title: "proj__cc_2", Type: tmux.AgentClaude},
		}
		cwds := []string{"/proj", "/proj"}
		record := []PaneRecordEntry{
			{WindowIndex: 0, AgentType: "cc", NTMIndex: 1, SessionID: "id-a", SessionProvider: "claude", SessionFile: "/f/a"},
			{WindowIndex: 0, AgentType: "cc", NTMIndex: 2, SessionID: "id-b", SessionProvider: "claude", SessionFile: "/f/b"},
		}
		fakeDiscover := func(agentType, cwd string) *agentsession.Info { return nil }

		states, refreshed, collisions := reconcilePaneIDs(panes, cwds, record, fakeDiscover)

		if len(collisions) != 0 {
			t.Errorf("collisions = %+v, want none for distinct keys", collisions)
		}
		if len(refreshed) != 2 {
			t.Fatalf("len(refreshed) = %d, want 2", len(refreshed))
		}
		if states[0].SessionID != "id-a" || states[1].SessionID != "id-b" {
			t.Errorf("states = %+v, want id-a and id-b preserved", states)
		}
	})
}

func TestUpsertPaneRecordEntriesDedupesByKey(t *testing.T) {
	t.Parallel()
	key := PaneRecordEntry{WindowIndex: 0, AgentType: "cc", NTMIndex: 1}
	filled1 := key
	filled1.SessionID, filled1.SessionProvider, filled1.SessionFile = "id-1", "claude", "/f/1"
	filled2 := key
	filled2.SessionID, filled2.SessionProvider, filled2.SessionFile = "id-2", "claude", "/f/2"
	empty := key

	tests := []struct {
		name    string
		entries []PaneRecordEntry
		want    PaneRecordEntry
	}{
		{name: "empty then filled: filled wins", entries: []PaneRecordEntry{empty, filled1}, want: filled1},
		{name: "filled then empty: filled wins", entries: []PaneRecordEntry{filled1, empty}, want: filled1},
		{name: "both filled: first wins", entries: []PaneRecordEntry{filled1, filled2}, want: filled1},
		{name: "both empty: first wins", entries: []PaneRecordEntry{empty, empty}, want: empty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := upsertPaneRecordEntries(dir, "proj", tc.entries); err != nil {
				t.Fatalf("upsertPaneRecordEntries: %v", err)
			}
			got := loadPaneRecord(dir, "proj")
			if len(got) != 1 {
				t.Fatalf("len(stored) = %d, want exactly 1 entry for the key: %+v", len(got), got)
			}
			if got[0] != tc.want {
				t.Errorf("stored[0] = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}
