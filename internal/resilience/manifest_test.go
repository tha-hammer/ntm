package resilience

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestDir_WithXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")

	dir := ManifestDir()
	want := filepath.Join("/custom/data", "ntm", "manifests")
	if dir != want {
		t.Errorf("ManifestDir() = %q, want %q", dir, want)
	}
}

func TestManifestDir_WithoutXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	dir := ManifestDir()
	// Should use ~/.local/share/ntm/manifests (or temp fallback)
	if !strings.HasSuffix(dir, filepath.Join("ntm", "manifests")) {
		t.Errorf("ManifestDir() = %q, want suffix ntm/manifests", dir)
	}
}

func TestLogDir_WithXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")

	dir := LogDir()
	want := filepath.Join("/custom/data", "ntm", "logs")
	if dir != want {
		t.Errorf("LogDir() = %q, want %q", dir, want)
	}
}

func TestLogDir_WithoutXDGDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")

	dir := LogDir()
	if !strings.HasSuffix(dir, filepath.Join("ntm", "logs")) {
		t.Errorf("LogDir() = %q, want suffix ntm/logs", dir)
	}
}

func TestSaveLoadDeleteManifest_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifest := &SpawnManifest{
		Session:     "test-session",
		ProjectDir:  "/home/user/project",
		AutoRestart: true,
		Agents: []AgentConfig{
			{PaneID: "%0", PaneIndex: 0, Type: "cc", Model: "opus-4", Command: "claude"},
			{PaneID: "%1", PaneIndex: 1, Type: "cod", Model: "gpt-5", Command: "codex"},
		},
	}

	// Save
	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// Verify file exists
	expectedPath := filepath.Join(tmpDir, "ntm", "manifests", "test-session.json")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("manifest file not created at %s: %v", expectedPath, err)
	}

	// Load
	loaded, err := LoadManifest("test-session")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}

	if loaded.Session != "test-session" {
		t.Errorf("Session = %q, want test-session", loaded.Session)
	}
	if loaded.ProjectDir != "/home/user/project" {
		t.Errorf("ProjectDir = %q, want /home/user/project", loaded.ProjectDir)
	}
	if !loaded.AutoRestart {
		t.Error("AutoRestart should be true")
	}
	if len(loaded.Agents) != 2 {
		t.Fatalf("Agents count = %d, want 2", len(loaded.Agents))
	}
	if loaded.Agents[0].Type != "cc" {
		t.Errorf("Agents[0].Type = %q, want cc", loaded.Agents[0].Type)
	}
	if loaded.Agents[1].Model != "gpt-5" {
		t.Errorf("Agents[1].Model = %q, want gpt-5", loaded.Agents[1].Model)
	}

	// Delete
	if err := DeleteManifest("test-session"); err != nil {
		t.Fatalf("DeleteManifest: %v", err)
	}

	// Verify deleted
	if _, err := os.Stat(expectedPath); !os.IsNotExist(err) {
		t.Errorf("manifest file still exists after delete: %v", err)
	}
}

func TestLoadManifest_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	_, err := LoadManifest("nonexistent")
	if err == nil {
		t.Error("expected error for missing manifest")
	}
	if !strings.Contains(err.Error(), "reading manifest") {
		t.Errorf("error = %q, want 'reading manifest'", err.Error())
	}
}

func TestDeleteManifest_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	err := DeleteManifest("nonexistent")
	if err != nil {
		t.Errorf("DeleteManifest on non-existent manifest should be idempotent, got error: %v", err)
	}
}

// TestSaveLoadManifest_ReapOrphansOnExit covers Behavior 1 of the periodic
// orphan-sweep TDD plan: true and false both round-trip exactly, and the
// raw JSON always contains the key (no omitempty) so a legacy manifest
// without it is distinguishable from an explicit false.
func TestSaveLoadManifest_ReapOrphansOnExit(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	cases := []struct {
		name    string
		session string
		want    bool
	}{
		{"true", "reap-true", true},
		{"false", "reap-false", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := &SpawnManifest{
				Session:           tc.session,
				ProjectDir:        "/tmp/test",
				ReapOrphansOnExit: tc.want,
			}
			if err := SaveManifest(manifest); err != nil {
				t.Fatalf("SaveManifest: %v", err)
			}

			path := filepath.Join(tmpDir, "ntm", "manifests", tc.session+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read raw manifest: %v", err)
			}
			if !strings.Contains(string(raw), `"reap_orphans_on_exit"`) {
				t.Errorf("raw manifest JSON missing reap_orphans_on_exit key:\n%s", raw)
			}

			loaded, err := LoadManifest(tc.session)
			if err != nil {
				t.Fatalf("LoadManifest: %v", err)
			}
			if loaded.ReapOrphansOnExit != tc.want {
				t.Errorf("ReapOrphansOnExit = %v, want %v", loaded.ReapOrphansOnExit, tc.want)
			}
		})
	}
}

// TestLoadManifest_LegacyMissingReapOrphansOnExitDecodesFalse covers
// Behavior 1: a manifest saved before this field existed has no key at
// all, and must decode to the fail-safe false rather than the current
// default true — silently upgrading a legacy manifest to the destructive
// default would be unsafe.
func TestLoadManifest_LegacyMissingReapOrphansOnExitDecodesFalse(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	dir := filepath.Join(tmpDir, "ntm", "manifests")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacy := `{"session":"legacy","project_dir":"/tmp/test","agents":[],"auto_restart":true}`
	if err := os.WriteFile(filepath.Join(dir, "legacy.json"), []byte(legacy), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	loaded, err := LoadManifest("legacy")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.ReapOrphansOnExit {
		t.Error("legacy manifest missing reap_orphans_on_exit decoded true, want false (fail-safe default)")
	}
}

func TestSaveManifest_EmptyAgents(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	manifest := &SpawnManifest{
		Session:    "empty-agents",
		ProjectDir: "/tmp/test",
		Agents:     []AgentConfig{},
	}

	if err := SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	loaded, err := LoadManifest("empty-agents")
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(loaded.Agents) != 0 {
		t.Errorf("Agents count = %d, want 0", len(loaded.Agents))
	}
}
