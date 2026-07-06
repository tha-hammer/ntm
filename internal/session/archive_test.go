package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withTempStorage points session storage at a temp HOME so archive tests don't
// touch the real ~/.ntm. It returns a cleanup function.
func withTempStorage(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp) // windows
	t.Setenv("NTM_HOME", "")
}

func saveTestSession(t *testing.T, name string) {
	t.Helper()
	state := &SessionState{
		Name:    name,
		SavedAt: time.Now().UTC(),
		WorkDir: "/data/projects/" + name,
		Agents:  AgentConfig{Claude: 1},
		Panes: []PaneState{
			{Index: 0, AgentType: "user", Title: "bash"},
			{Index: 1, AgentType: "cc", Title: name + "__cc_1", SessionID: "id-1", SessionProvider: "claude"},
		},
		Version: StateVersion,
	}
	if _, err := Save(state, SaveOptions{Name: name}); err != nil {
		t.Fatalf("Save(%s): %v", name, err)
	}
}

func TestArchiveAndUnarchive(t *testing.T) {
	withTempStorage(t)
	saveTestSession(t, "proj1")

	// Initially present in active list, absent from archived list.
	active, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active list len = %d, want 1", len(active))
	}
	archived, err := ListArchived()
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 0 {
		t.Fatalf("archived list len = %d, want 0", len(archived))
	}

	// Archive it.
	path, err := Archive("proj1")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if filepath.Dir(path) != ArchiveDir() {
		t.Errorf("archived path %q not under ArchiveDir %q", path, ArchiveDir())
	}
	if !IsArchived("proj1") {
		t.Error("IsArchived should be true after archive")
	}

	// Now absent from active, present in archived.
	active, _ = List()
	if len(active) != 0 {
		t.Errorf("active list len = %d after archive, want 0", len(active))
	}
	archived, _ = ListArchived()
	if len(archived) != 1 {
		t.Fatalf("archived list len = %d, want 1", len(archived))
	}
	if archived[0].Name != "proj1" {
		t.Errorf("archived name = %q, want proj1", archived[0].Name)
	}

	// The original active file should be gone.
	if Exists("proj1") {
		t.Error("active save should not exist after archive")
	}

	// Unarchive restores it.
	if _, err := Unarchive("proj1"); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if !Exists("proj1") {
		t.Error("active save should exist after unarchive")
	}
	if IsArchived("proj1") {
		t.Error("IsArchived should be false after unarchive")
	}
}

func TestArchiveMissing(t *testing.T) {
	withTempStorage(t)
	if _, err := Archive("nope"); err == nil {
		t.Error("expected error archiving missing session")
	}
	if _, err := Unarchive("nope"); err == nil {
		t.Error("expected error unarchiving missing session")
	}
}

func TestArchiveDuplicate(t *testing.T) {
	withTempStorage(t)
	saveTestSession(t, "dup")
	if _, err := Archive("dup"); err != nil {
		t.Fatal(err)
	}
	// Save a new active session with the same name, then archiving again must
	// fail because an archived copy already exists.
	saveTestSession(t, "dup")
	if _, err := Archive("dup"); err == nil {
		t.Error("expected error archiving when an archived copy already exists")
	}
}

func TestUnarchiveConflict(t *testing.T) {
	withTempStorage(t)
	saveTestSession(t, "conf")
	if _, err := Archive("conf"); err != nil {
		t.Fatal(err)
	}
	// Recreate an active session with the same name.
	saveTestSession(t, "conf")
	if _, err := Unarchive("conf"); err == nil {
		t.Error("expected error unarchiving over an existing active session")
	}
}

func TestListSkipsArchiveSubdir(t *testing.T) {
	withTempStorage(t)
	saveTestSession(t, "keep")
	if _, err := Archive("keep"); err != nil {
		t.Fatal(err)
	}
	// The archive subdir lives inside StorageDir; List must not surface it or
	// its contents as an active session.
	if _, err := os.Stat(ArchiveDir()); err != nil {
		t.Fatalf("archive dir should exist: %v", err)
	}
	active, _ := List()
	if len(active) != 0 {
		t.Errorf("active list should be empty, got %d", len(active))
	}
}

func TestResumeFromState_Serialization(t *testing.T) {
	withTempStorage(t)
	// Round-trip a state with session ids through save/load to prove the new
	// fields serialize and the resume linkage survives persistence.
	saveTestSession(t, "ser")
	loaded, err := Load("ser")
	if err != nil {
		t.Fatal(err)
	}
	var cc *PaneState
	for i := range loaded.Panes {
		if loaded.Panes[i].AgentType == "cc" {
			cc = &loaded.Panes[i]
		}
	}
	if cc == nil {
		t.Fatal("cc pane not found after load")
	}
	if cc.SessionID != "id-1" || cc.SessionProvider != "claude" {
		t.Errorf("session linkage lost: %+v", cc)
	}
}
