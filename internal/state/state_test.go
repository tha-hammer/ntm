package state

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/sqliteutil"
	"pgregory.net/rapid"
)

// testStore creates a test store with in-memory SQLite and runs migrations.
func testStore(t *testing.T) *Store {
	t.Helper()

	// Use in-memory SQLite for tests - no WAL mode needed
	db, err := sql.Open(sqliteutil.DriverName, sqliteutil.MemoryDSN("foreign_keys(1)"))
	if err != nil {
		t.Fatalf("Open in-memory db error: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() {
		_ = db.Close()
	})

	store := &Store{db: db, path: ":memory:"}

	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}

	return store
}

func TestOpenMemoryStore(t *testing.T) {
	t.Parallel()

	store, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) error: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}

	if store.Path() != ":memory:" {
		t.Fatalf("Path() = %q, want %q", store.Path(), ":memory:")
	}
}

// ======================
// Schema Types Tests
// ======================

func TestSessionStatusConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status SessionStatus
		want   string
	}{
		{SessionActive, "active"},
		{SessionPaused, "paused"},
		{SessionTerminated, "terminated"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("SessionStatus %v = %q, want %q", tt.status, string(tt.status), tt.want)
		}
	}
}

func TestAgentStatusConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status AgentStatus
		want   string
	}{
		{AgentIdle, "idle"},
		{AgentWorking, "working"},
		{AgentError, "error"},
		{AgentCrashed, "crashed"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("AgentStatus %v = %q, want %q", tt.status, string(tt.status), tt.want)
		}
	}
}

func TestAgentTypeConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		agentType AgentType
		want      string
	}{
		{AgentTypeClaude, "cc"},
		{AgentTypeCodex, "cod"},
		{AgentTypeGemini, "gmi"},
	}

	for _, tt := range tests {
		if string(tt.agentType) != tt.want {
			t.Errorf("AgentType %v = %q, want %q", tt.agentType, string(tt.agentType), tt.want)
		}
	}
}

func TestTaskStatusConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status TaskStatus
		want   string
	}{
		{TaskPending, "pending"},
		{TaskAssigned, "assigned"},
		{TaskWorking, "working"},
		{TaskCompleted, "completed"},
		{TaskFailed, "failed"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("TaskStatus %v = %q, want %q", tt.status, string(tt.status), tt.want)
		}
	}
}

func TestTaskResultConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		result TaskResult
		want   string
	}{
		{TaskResultSuccess, "success"},
		{TaskResultFailure, "failure"},
		{TaskResultPartial, "partial"},
	}

	for _, tt := range tests {
		if string(tt.result) != tt.want {
			t.Errorf("TaskResult %v = %q, want %q", tt.result, string(tt.result), tt.want)
		}
	}
}

func TestApprovalStatusConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status ApprovalStatus
		want   string
	}{
		{ApprovalPending, "pending"},
		{ApprovalApproved, "approved"},
		{ApprovalDenied, "denied"},
		{ApprovalExpired, "expired"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("ApprovalStatus %v = %q, want %q", tt.status, string(tt.status), tt.want)
		}
	}
}

// ======================
// Store Basic Tests
// ======================

func TestOpenDefault(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("XDG_CONFIG_HOME", "")
	// bd-ev740: clear ambient NTM_CONFIG so DefaultPath does not route the
	// state DB into a hostile or non-writable path that an outer
	// CI/agent shell may have exported (e.g. /nonexistent/config.toml).
	t.Setenv("NTM_CONFIG", "")

	store, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") error: %v", err)
	}
	defer store.Close()

	wantPath := filepath.Join(tmpDir, ".config", "ntm", "state.db")
	if store.Path() != wantPath {
		t.Errorf("Path() = %q, want %q", store.Path(), wantPath)
	}
}

func TestOpenDefaultRespectsXDGConfigHome(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmpDir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg-config"))
	// bd-ev740: clear ambient NTM_CONFIG; otherwise the state path resolver
	// honors NTM_CONFIG ahead of XDG_CONFIG_HOME and the test's XDG path
	// expectation breaks.
	t.Setenv("NTM_CONFIG", "")

	store, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") error: %v", err)
	}
	defer store.Close()

	wantPath := filepath.Join(tmpDir, "xdg-config", "ntm", "state.db")
	if store.Path() != wantPath {
		t.Errorf("Path() = %q, want %q", store.Path(), wantPath)
	}
}

func TestOpenDefaultRespectsNTMConfigPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmpDir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg-config"))
	t.Setenv("NTM_CONFIG", filepath.Join(tmpDir, "custom-root", "custom.toml"))

	store, err := Open("")
	if err != nil {
		t.Fatalf("Open(\"\") error: %v", err)
	}
	defer store.Close()

	wantPath := filepath.Join(tmpDir, "custom-root", "state.db")
	if store.Path() != wantPath {
		t.Errorf("Path() = %q, want %q", store.Path(), wantPath)
	}
}

func TestOpenCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom", "db.sqlite")

	store, err := Open(customPath)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", customPath, err)
	}
	defer store.Close()

	if store.Path() != customPath {
		t.Errorf("Path() = %q, want %q", store.Path(), customPath)
	}

	// Verify file was created
	if _, err := os.Stat(customPath); os.IsNotExist(err) {
		t.Errorf("Database file was not created at %q", customPath)
	}
}

func TestMigrate(t *testing.T) {
	store := testStore(t)

	// First, let's see what tables actually exist
	rows, err := store.db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatalf("Query sqlite_master error: %v", err)
	}
	defer rows.Close()

	var existingTables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("Scan error: %v", err)
		}
		existingTables = append(existingTables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() error: %v", err)
	}
	t.Logf("Existing tables: %v", existingTables)

	// Verify tables exist by trying to query them
	tables := []string{"sessions", "agents", "tasks", "reservations", "approvals", "context_packs", "tool_health", "event_log", "ensemble_sessions", "mode_assignments", "_migrations"}
	for _, table := range tables {
		r, err := store.db.Query("SELECT 1 FROM " + table + " LIMIT 1")
		if err != nil {
			t.Errorf("Table %q should exist after migration: %v", table, err)
		} else {
			r.Close()
		}
	}
}

func TestTransaction(t *testing.T) {
	store := testStore(t)

	// Create a session within a transaction
	sess := &Session{
		ID:          "tx-sess-1",
		Name:        "tx-session",
		ProjectPath: "/test/project",
		CreatedAt:   time.Now(),
		Status:      SessionActive,
	}

	err := store.Transaction(func(tx *Tx) error {
		_, err := tx.tx.Exec(`
			INSERT INTO sessions (id, name, project_path, created_at, status)
			VALUES (?, ?, ?, ?, ?)`,
			sess.ID, sess.Name, sess.ProjectPath, sess.CreatedAt, sess.Status,
		)
		return err
	})
	if err != nil {
		t.Fatalf("Transaction error: %v", err)
	}

	// Verify session was created
	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got == nil || got.ID != sess.ID {
		t.Errorf("Session not found after transaction")
	}
}

func TestTransactionRollback(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID:          "tx-rollback-1",
		Name:        "rollback-session",
		ProjectPath: "/test/rollback",
		CreatedAt:   time.Now(),
		Status:      SessionActive,
	}

	// Transaction that returns an error should rollback
	err := store.Transaction(func(tx *Tx) error {
		_, err := tx.tx.Exec(`
			INSERT INTO sessions (id, name, project_path, created_at, status)
			VALUES (?, ?, ?, ?, ?)`,
			sess.ID, sess.Name, sess.ProjectPath, sess.CreatedAt, sess.Status,
		)
		if err != nil {
			return err
		}
		// Return error to trigger rollback
		return sql.ErrNoRows // Using this as a test error
	})
	if err == nil {
		t.Fatal("Transaction should have returned error")
	}

	// Verify session was NOT created
	got, _ := store.GetSession(sess.ID)
	if got != nil {
		t.Errorf("Session should not exist after rollback")
	}
}

func TestTransactionRollbackOnPanic(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID:          "tx-panic-1",
		Name:        "panic-session",
		ProjectPath: "/test/panic",
		CreatedAt:   time.Now(),
		Status:      SessionActive,
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Transaction should re-panic")
		}

		got, err := store.GetSession(sess.ID)
		if err != nil {
			t.Fatalf("GetSession error after panic: %v", err)
		}
		if got != nil {
			t.Fatalf("session should not exist after panic rollback")
		}

		if err := store.CreateSession(&Session{
			ID:          "tx-after-panic",
			Name:        "after-panic",
			ProjectPath: "/test/after-panic",
			CreatedAt:   time.Now(),
			Status:      SessionActive,
		}); err != nil {
			t.Fatalf("store should remain usable after panic rollback: %v", err)
		}
	}()

	_ = store.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`
			INSERT INTO sessions (id, name, project_path, created_at, status)
			VALUES (?, ?, ?, ?, ?)`,
			sess.ID, sess.Name, sess.ProjectPath, sess.CreatedAt, sess.Status,
		); err != nil {
			return err
		}
		panic("boom")
	})
}

// ======================
// Session Operations Tests
// ======================

func TestSessionCRUD(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID:               "sess-1",
		Name:             "test-session",
		ProjectPath:      "/test/project",
		CreatedAt:        time.Now().UTC().Round(time.Second),
		Status:           SessionActive,
		ConfigSnapshot:   `{"agents": 2}`,
		CoordinatorAgent: "GreenLake",
	}

	// Create
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Read
	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got == nil {
		t.Fatal("GetSession returned nil")
	}
	if got.Name != sess.Name || got.Status != sess.Status {
		t.Errorf("GetSession = %+v, want name=%q status=%v", got, sess.Name, sess.Status)
	}

	// Update
	sess.Status = SessionPaused
	sess.ConfigSnapshot = `{"agents": 3}`
	if err := store.UpdateSession(sess); err != nil {
		t.Fatalf("UpdateSession error: %v", err)
	}

	got, _ = store.GetSession(sess.ID)
	if got.Status != SessionPaused {
		t.Errorf("After update, Status = %v, want %v", got.Status, SessionPaused)
	}

	// Delete
	if err := store.DeleteSession(sess.ID); err != nil {
		t.Fatalf("DeleteSession error: %v", err)
	}

	got, _ = store.GetSession(sess.ID)
	if got != nil {
		t.Error("Session should be nil after delete")
	}
}

func TestGetSessionNotFound(t *testing.T) {
	store := testStore(t)

	got, err := store.GetSession("nonexistent")
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got != nil {
		t.Errorf("GetSession(nonexistent) = %v, want nil", got)
	}
}

func TestListSessions(t *testing.T) {
	store := testStore(t)

	// Create test sessions
	sessions := []*Session{
		{ID: "list-1", Name: "session-1", ProjectPath: "/p1", CreatedAt: time.Now(), Status: SessionActive},
		{ID: "list-2", Name: "session-2", ProjectPath: "/p2", CreatedAt: time.Now(), Status: SessionPaused},
		{ID: "list-3", Name: "session-3", ProjectPath: "/p3", CreatedAt: time.Now(), Status: SessionActive},
	}
	for _, s := range sessions {
		if err := store.CreateSession(s); err != nil {
			t.Fatalf("CreateSession error: %v", err)
		}
	}

	// List all
	all, err := store.ListSessions("")
	if err != nil {
		t.Fatalf("ListSessions(\"\") error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListSessions(\"\") returned %d sessions, want 3", len(all))
	}

	// List by status
	active, err := store.ListSessions("active")
	if err != nil {
		t.Fatalf("ListSessions(active) error: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("ListSessions(active) returned %d sessions, want 2", len(active))
	}

	paused, err := store.ListSessions("paused")
	if err != nil {
		t.Fatalf("ListSessions(paused) error: %v", err)
	}
	if len(paused) != 1 {
		t.Errorf("ListSessions(paused) returned %d sessions, want 1", len(paused))
	}
}

func TestUpdateSessionNotFound(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID:          "nonexistent",
		Name:        "ghost",
		ProjectPath: "/ghost",
		Status:      SessionActive,
	}

	err := store.UpdateSession(sess)
	if err == nil {
		t.Error("UpdateSession should error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Error should mention 'not found': %v", err)
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	store := testStore(t)

	err := store.DeleteSession("nonexistent")
	if err == nil {
		t.Error("DeleteSession should error for nonexistent session")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Error should mention 'not found': %v", err)
	}
}

// ======================
// Agent Operations Tests
// ======================

func TestAgentCRUD(t *testing.T) {
	store := testStore(t)

	// Create session first (foreign key)
	sess := &Session{
		ID: "agent-sess", Name: "agent-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	now := time.Now().UTC().Round(time.Second)
	agent := &Agent{
		ID:              "agent-1",
		SessionID:       sess.ID,
		Name:            "BlueTiger",
		Type:            AgentTypeClaude,
		Model:           "claude-4",
		TmuxPaneID:      "%0",
		LastSeen:        &now,
		Status:          AgentIdle,
		CurrentTaskID:   "task-1",
		PerformanceData: `{"tokens": 1000}`,
	}

	// Create
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	// Read by ID
	got, err := store.GetAgent(agent.ID)
	if err != nil {
		t.Fatalf("GetAgent error: %v", err)
	}
	if got == nil || got.Name != agent.Name {
		t.Errorf("GetAgent = %+v, want name=%q", got, agent.Name)
	}

	// Read by name
	got, err = store.GetAgentByName(sess.ID, "BlueTiger")
	if err != nil {
		t.Fatalf("GetAgentByName error: %v", err)
	}
	if got == nil || got.ID != agent.ID {
		t.Errorf("GetAgentByName = %+v, want id=%q", got, agent.ID)
	}

	// Update
	agent.Status = AgentWorking
	if err := store.UpdateAgent(agent); err != nil {
		t.Fatalf("UpdateAgent error: %v", err)
	}

	got, _ = store.GetAgent(agent.ID)
	if got.Status != AgentWorking {
		t.Errorf("After update, Status = %v, want %v", got.Status, AgentWorking)
	}
}

func TestListAgents(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "list-agent-sess", Name: "list-agents", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	agents := []*Agent{
		{ID: "la-1", SessionID: sess.ID, Name: "Alpha", Type: AgentTypeClaude, Status: AgentIdle},
		{ID: "la-2", SessionID: sess.ID, Name: "Beta", Type: AgentTypeCodex, Status: AgentWorking},
	}
	for _, a := range agents {
		if err := store.CreateAgent(a); err != nil {
			t.Fatalf("CreateAgent error: %v", err)
		}
	}

	list, err := store.ListAgents(sess.ID)
	if err != nil {
		t.Fatalf("ListAgents error: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("ListAgents returned %d agents, want 2", len(list))
	}
}

// ======================
// Task Operations Tests
// ======================

func TestTaskCRUD(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "task-sess", Name: "task-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Create agent for FK reference (agent_id can be NULL, but empty string fails FK check)
	agent := &Agent{
		ID: "task-agent", SessionID: sess.ID, Name: "TaskAgent",
		Type: AgentTypeClaude, Status: AgentIdle,
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	task := &Task{
		ID:            "task-1",
		SessionID:     sess.ID,
		AgentID:       agent.ID,
		BeadID:        "bead-123",
		CorrelationID: "corr-456",
		Status:        TaskPending,
		CreatedAt:     time.Now().UTC().Round(time.Second),
	}

	// Create
	if err := store.CreateTask(task); err != nil {
		t.Fatalf("CreateTask error: %v", err)
	}

	// Read by ID
	got, err := store.GetTask(task.ID)
	if err != nil {
		t.Fatalf("GetTask error: %v", err)
	}
	if got == nil || got.BeadID != task.BeadID {
		t.Errorf("GetTask = %+v, want bead_id=%q", got, task.BeadID)
	}

	// Read by correlation ID
	got, err = store.GetTaskByCorrelation(task.CorrelationID)
	if err != nil {
		t.Fatalf("GetTaskByCorrelation error: %v", err)
	}
	if got == nil || got.ID != task.ID {
		t.Errorf("GetTaskByCorrelation = %+v, want id=%q", got, task.ID)
	}

	// Update
	now := time.Now()
	task.Status = TaskCompleted
	task.CompletedAt = &now
	result := TaskResultSuccess
	task.Result = &result
	if err := store.UpdateTask(task); err != nil {
		t.Fatalf("UpdateTask error: %v", err)
	}

	got, _ = store.GetTask(task.ID)
	if got.Status != TaskCompleted {
		t.Errorf("After update, Status = %v, want %v", got.Status, TaskCompleted)
	}
}

func TestListTasks(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "list-task-sess", Name: "list-tasks", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Create agent for FK reference
	agent := &Agent{
		ID: "list-task-agent", SessionID: sess.ID, Name: "ListTaskAgent",
		Type: AgentTypeClaude, Status: AgentIdle,
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	tasks := []*Task{
		{ID: "lt-1", SessionID: sess.ID, AgentID: agent.ID, Status: TaskPending, CreatedAt: time.Now()},
		{ID: "lt-2", SessionID: sess.ID, AgentID: agent.ID, Status: TaskCompleted, CreatedAt: time.Now()},
		{ID: "lt-3", SessionID: sess.ID, AgentID: agent.ID, Status: TaskPending, CreatedAt: time.Now()},
	}
	for _, task := range tasks {
		if err := store.CreateTask(task); err != nil {
			t.Fatalf("CreateTask error: %v", err)
		}
	}

	// List all
	all, err := store.ListTasks(sess.ID, "")
	if err != nil {
		t.Fatalf("ListTasks(\"\") error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListTasks(\"\") returned %d tasks, want 3", len(all))
	}

	// List by status
	pending, err := store.ListTasks(sess.ID, "pending")
	if err != nil {
		t.Fatalf("ListTasks(pending) error: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("ListTasks(pending) returned %d tasks, want 2", len(pending))
	}
}

// ======================
// Reservation Operations Tests
// ======================

func TestReservationCRUD(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "res-sess", Name: "res-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	agent := &Agent{
		ID: "res-agent", SessionID: sess.ID, Name: "ResAgent",
		Type: AgentTypeClaude, Status: AgentIdle,
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	res := &Reservation{
		SessionID:   sess.ID,
		AgentID:     agent.ID,
		PathPattern: "internal/**/*.go",
		Exclusive:   true,
		Reason:      "refactoring",
		ExpiresAt:   time.Now().Add(time.Hour),
	}

	// Create
	if err := store.CreateReservation(res); err != nil {
		t.Fatalf("CreateReservation error: %v", err)
	}
	if res.ID == 0 {
		t.Error("Reservation ID should be set after create")
	}

	// Read
	got, err := store.GetReservation(res.ID)
	if err != nil {
		t.Fatalf("GetReservation error: %v", err)
	}
	if got == nil || got.PathPattern != res.PathPattern {
		t.Errorf("GetReservation = %+v, want path_pattern=%q", got, res.PathPattern)
	}

	// Update (release)
	now := time.Now()
	res.ReleasedAt = &now
	if err := store.UpdateReservation(res); err != nil {
		t.Fatalf("UpdateReservation error: %v", err)
	}

	got, _ = store.GetReservation(res.ID)
	if got.ReleasedAt == nil {
		t.Error("ReleasedAt should be set after update")
	}
}

func TestListReservations(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "list-res-sess", Name: "list-res", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	agent := &Agent{
		ID: "list-res-agent", SessionID: sess.ID, Name: "ListResAgent",
		Type: AgentTypeClaude, Status: AgentIdle,
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	// Create reservations: one active, one expired, one released
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	// Active reservation (future expiry, not released)
	activeRes := &Reservation{SessionID: sess.ID, AgentID: agent.ID, PathPattern: "active/*", Exclusive: true, ExpiresAt: future}
	if err := store.CreateReservation(activeRes); err != nil {
		t.Fatalf("CreateReservation (active) error: %v", err)
	}

	// Expired reservation (past expiry, not released)
	expiredRes := &Reservation{SessionID: sess.ID, AgentID: agent.ID, PathPattern: "expired/*", Exclusive: true, ExpiresAt: past}
	if err := store.CreateReservation(expiredRes); err != nil {
		t.Fatalf("CreateReservation (expired) error: %v", err)
	}

	// Released reservation (future expiry, but released)
	releasedRes := &Reservation{SessionID: sess.ID, AgentID: agent.ID, PathPattern: "released/*", Exclusive: true, ExpiresAt: future}
	if err := store.CreateReservation(releasedRes); err != nil {
		t.Fatalf("CreateReservation (released) error: %v", err)
	}
	releasedRes.ReleasedAt = &now
	if err := store.UpdateReservation(releasedRes); err != nil {
		t.Fatalf("UpdateReservation error: %v", err)
	}

	// List all
	all, err := store.ListReservations(sess.ID, false)
	if err != nil {
		t.Fatalf("ListReservations(all) error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListReservations(all) returned %d, want 3", len(all))
	}

	// List active only
	active, err := store.ListReservations(sess.ID, true)
	if err != nil {
		t.Fatalf("ListReservations(active) error: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("ListReservations(active) returned %d, want 1", len(active))
	}
}

func TestFindConflicts(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "conflict-sess", Name: "conflict-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	agent := &Agent{
		ID: "conflict-agent", SessionID: sess.ID, Name: "ConflictAgent",
		Type: AgentTypeClaude, Status: AgentIdle,
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	// Create exclusive reservation
	res := &Reservation{
		SessionID:   sess.ID,
		AgentID:     agent.ID,
		PathPattern: "internal/*",
		Exclusive:   true,
		ExpiresAt:   time.Now().Add(time.Hour),
	}
	if err := store.CreateReservation(res); err != nil {
		t.Fatalf("CreateReservation error: %v", err)
	}

	// Check for conflicts
	conflicts, err := store.FindConflicts(sess.ID, "internal/cli/main.go")
	if err != nil {
		t.Fatalf("FindConflicts error: %v", err)
	}
	if len(conflicts) != 1 {
		t.Errorf("FindConflicts returned %d conflicts, want 1", len(conflicts))
	}

	// Non-conflicting path
	noConflicts, err := store.FindConflicts(sess.ID, "cmd/main.go")
	if err != nil {
		t.Fatalf("FindConflicts error: %v", err)
	}
	if len(noConflicts) != 0 {
		t.Errorf("FindConflicts for non-matching path returned %d, want 0", len(noConflicts))
	}
}

// ======================
// Approval Operations Tests
// ======================

func TestApprovalCRUD(t *testing.T) {
	store := testStore(t)

	appr := &Approval{
		ID:          "appr-1",
		Action:      "git push --force",
		Resource:    "origin/main",
		Reason:      "rebase cleanup",
		RequestedBy: "BlueTiger",
		RequiresSLB: true,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Hour),
		Status:      ApprovalPending,
	}

	// Create
	if err := store.CreateApproval(appr); err != nil {
		t.Fatalf("CreateApproval error: %v", err)
	}

	// Read
	got, err := store.GetApproval(appr.ID)
	if err != nil {
		t.Fatalf("GetApproval error: %v", err)
	}
	if got == nil || got.Action != appr.Action {
		t.Errorf("GetApproval = %+v, want action=%q", got, appr.Action)
	}

	// Update (approve)
	appr.Status = ApprovalApproved
	appr.ApprovedBy = "human"
	now := time.Now()
	appr.ApprovedAt = &now
	if err := store.UpdateApproval(appr); err != nil {
		t.Fatalf("UpdateApproval error: %v", err)
	}

	got, _ = store.GetApproval(appr.ID)
	if got.Status != ApprovalApproved {
		t.Errorf("After update, Status = %v, want %v", got.Status, ApprovalApproved)
	}
}

func TestListPendingApprovals(t *testing.T) {
	store := testStore(t)

	now := time.Now().UTC()
	approvals := []*Approval{
		{ID: "pa-1", Action: "action1", Resource: "r1", RequestedBy: "a1", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Status: ApprovalPending},
		{ID: "pa-2", Action: "action2", Resource: "r2", RequestedBy: "a2", CreatedAt: now, ExpiresAt: now.Add(time.Hour), Status: ApprovalApproved},
		{ID: "pa-3", Action: "action3", Resource: "r3", RequestedBy: "a3", CreatedAt: now, ExpiresAt: now.Add(-time.Hour), Status: ApprovalPending}, // expired
	}
	for _, a := range approvals {
		if err := store.CreateApproval(a); err != nil {
			t.Fatalf("CreateApproval error: %v", err)
		}
	}

	pending, err := store.ListPendingApprovals()
	if err != nil {
		t.Fatalf("ListPendingApprovals error: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("ListPendingApprovals returned %d, want 1", len(pending))
	}
}

// ======================
// Tool Health Operations Tests
// ======================

func TestToolHealthUpsert(t *testing.T) {
	store := testStore(t)

	now := time.Now()
	th := &ToolHealth{
		Tool:         "cass",
		Version:      "1.0.0",
		Capabilities: `["search", "index"]`,
		LastOK:       &now,
	}

	// Insert
	if err := store.UpsertToolHealth(th); err != nil {
		t.Fatalf("UpsertToolHealth (insert) error: %v", err)
	}

	got, err := store.GetToolHealth("cass")
	if err != nil {
		t.Fatalf("GetToolHealth error: %v", err)
	}
	if got == nil || got.Version != "1.0.0" {
		t.Errorf("GetToolHealth = %+v, want version=1.0.0", got)
	}

	// Update
	th.Version = "2.0.0"
	th.LastError = "connection timeout"
	if err := store.UpsertToolHealth(th); err != nil {
		t.Fatalf("UpsertToolHealth (update) error: %v", err)
	}

	got, _ = store.GetToolHealth("cass")
	if got.Version != "2.0.0" || got.LastError != "connection timeout" {
		t.Errorf("After upsert, got %+v, want version=2.0.0", got)
	}
}

func TestListToolHealth(t *testing.T) {
	store := testStore(t)

	tools := []*ToolHealth{
		{Tool: "bv", Version: "1.0.0"},
		{Tool: "cm", Version: "0.5.0"},
		{Tool: "ubs", Version: "2.0.0"},
	}
	for _, th := range tools {
		if err := store.UpsertToolHealth(th); err != nil {
			t.Fatalf("UpsertToolHealth error: %v", err)
		}
	}

	list, err := store.ListToolHealth()
	if err != nil {
		t.Fatalf("ListToolHealth error: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("ListToolHealth returned %d, want 3", len(list))
	}
}

// ======================
// Context Pack Operations Tests
// ======================

func TestContextPackCRUD(t *testing.T) {
	store := testStore(t)

	cp := &ContextPack{
		ID:             "cp-1",
		BeadID:         "bead-123",
		AgentType:      AgentTypeClaude,
		RepoRev:        "abc123",
		CorrelationID:  "corr-1",
		CreatedAt:      time.Now(),
		TokenCount:     5000,
		RenderedPrompt: "Please implement...",
	}

	// Create
	if err := store.CreateContextPack(cp); err != nil {
		t.Fatalf("CreateContextPack error: %v", err)
	}

	// Read
	got, err := store.GetContextPack(cp.ID)
	if err != nil {
		t.Fatalf("GetContextPack error: %v", err)
	}
	if got == nil || got.TokenCount != 5000 {
		t.Errorf("GetContextPack = %+v, want token_count=5000", got)
	}
}

// ======================
// Event Log Operations Tests
// ======================

func TestEventLogCRUD(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "event-sess", Name: "event-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	entry := &EventLogEntry{
		SessionID:     sess.ID,
		EventType:     "task_assigned",
		EventData:     `{"task_id": "t1", "agent_id": "a1"}`,
		CorrelationID: "corr-1",
	}

	// Log event
	if err := store.LogEvent(entry); err != nil {
		t.Fatalf("LogEvent error: %v", err)
	}
	if entry.ID == 0 {
		t.Error("Event ID should be set after log")
	}

	// List events
	events, err := store.ListEvents(sess.ID, 10)
	if err != nil {
		t.Fatalf("ListEvents error: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("ListEvents returned %d events, want 1", len(events))
	}
}

func TestReplayEvents(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "replay-sess", Name: "replay-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Log multiple events
	for i := 0; i < 5; i++ {
		entry := &EventLogEntry{
			SessionID: sess.ID,
			EventType: "test_event",
			EventData: `{"seq": ` + string(rune('0'+i)) + `}`,
		}
		if err := store.LogEvent(entry); err != nil {
			t.Fatalf("LogEvent error: %v", err)
		}
	}

	// Replay from ID 2 (should get events 3, 4, 5)
	var replayed []EventLogEntry
	err := store.ReplayEvents(sess.ID, 2, func(e EventLogEntry) error {
		replayed = append(replayed, e)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplayEvents error: %v", err)
	}
	if len(replayed) != 3 {
		t.Errorf("ReplayEvents from ID 2 returned %d events, want 3", len(replayed))
	}
}

func TestReplayEvents_HandlerMayWriteToStore(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "replay-write-sess", Name: "replay-write-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	for i := 0; i < 2; i++ {
		entry := &EventLogEntry{
			SessionID: sess.ID,
			EventType: "test_event",
			EventData: `{"seq": 1}`,
		}
		if err := store.LogEvent(entry); err != nil {
			t.Fatalf("LogEvent error: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- store.ReplayEvents(sess.ID, 0, func(e EventLogEntry) error {
			if e.ID != 1 {
				return nil
			}
			sess.Status = SessionPaused
			return store.UpdateSession(sess)
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReplayEvents error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReplayEvents deadlocked when handler wrote to store")
	}

	got, err := store.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession error: %v", err)
	}
	if got == nil {
		t.Fatal("session should still exist after replay handler update")
	}
	if got.Status != SessionPaused {
		t.Fatalf("session status = %q, want %q", got.Status, SessionPaused)
	}
}

// ======================
// Migration Tests
// ======================

func TestGetMigrationFiles(t *testing.T) {
	t.Parallel()

	files, err := GetMigrationFiles()
	if err != nil {
		t.Fatalf("GetMigrationFiles error: %v", err)
	}
	if len(files) == 0 {
		t.Error("GetMigrationFiles returned no files")
	}
	// Should have at least the initial migration
	if !strings.HasPrefix(files[0], "001_") {
		t.Errorf("First migration should be 001_*, got %q", files[0])
	}
}

func TestReadMigration(t *testing.T) {
	t.Parallel()

	content, err := ReadMigration("001_initial.sql")
	if err != nil {
		t.Fatalf("ReadMigration error: %v", err)
	}
	if !strings.Contains(content, "CREATE TABLE") {
		t.Error("Migration content should contain CREATE TABLE")
	}
}

func TestReadMigrationNotFound(t *testing.T) {
	t.Parallel()

	_, err := ReadMigration("999_nonexistent.sql")
	if err == nil {
		t.Error("ReadMigration should error for nonexistent file")
	}
}

func TestApplyMigrationsIdempotent(t *testing.T) {
	store := testStore(t)

	// Migrate is already called in testStore, call again to test idempotency
	if err := store.Migrate(); err != nil {
		t.Fatalf("Second Migrate() error: %v", err)
	}

	// Get expected migration count
	migrationFiles, err := GetMigrationFiles()
	if err != nil {
		t.Fatalf("GetMigrationFiles error: %v", err)
	}

	// Verify _migrations table has entry for each migration file
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM _migrations").Scan(&count)
	if err != nil {
		t.Fatalf("Query migrations count error: %v", err)
	}
	if count != len(migrationFiles) {
		t.Errorf("Migrations count = %d, want %d", count, len(migrationFiles))
	}
}

// ======================
// Bead Status Constants Tests
// ======================

func TestBeadStatusConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status BeadStatus
		want   string
	}{
		{BeadStatusAssigned, "assigned"},
		{BeadStatusWorking, "working"},
		{BeadStatusCompleted, "completed"},
		{BeadStatusFailed, "failed"},
		{BeadStatusReassigned, "reassigned"},
	}

	for _, tt := range tests {
		if string(tt.status) != tt.want {
			t.Errorf("BeadStatus %v = %q, want %q", tt.status, string(tt.status), tt.want)
		}
	}
}

// ======================
// Bead History Operations Tests
// ======================

func TestBeadHistoryCRUD(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "bead-hist-sess", Name: "bead-hist-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	agent := &Agent{
		ID: "bead-hist-agent", SessionID: sess.ID, Name: "BeadHistAgent",
		Type: AgentTypeClaude, Status: AgentIdle,
	}
	if err := store.CreateAgent(agent); err != nil {
		t.Fatalf("CreateAgent error: %v", err)
	}

	entry := &BeadHistoryEntry{
		SessionID:    sess.ID,
		BeadID:       "bead-123",
		BeadTitle:    "Test bead",
		ToStatus:     BeadStatusAssigned,
		AgentID:      agent.ID,
		AgentType:    "cc",
		AgentName:    "BeadHistAgent",
		Pane:         1,
		Trigger:      "user_assign",
		PromptSent:   "Please implement feature X",
		TransitionAt: time.Now().UTC(),
	}

	// Record initial assignment
	if err := store.RecordBeadHistory(entry); err != nil {
		t.Fatalf("RecordBeadHistory error: %v", err)
	}
	if entry.ID == 0 {
		t.Error("BeadHistoryEntry ID should be set after record")
	}

	// Record working transition
	workingEntry := &BeadHistoryEntry{
		SessionID:  sess.ID,
		BeadID:     "bead-123",
		BeadTitle:  "Test bead",
		FromStatus: BeadStatusAssigned,
		ToStatus:   BeadStatusWorking,
		AgentID:    agent.ID,
		AgentType:  "cc",
		AgentName:  "BeadHistAgent",
		Pane:       1,
		Trigger:    "agent_start",
	}
	if err := store.RecordBeadHistory(workingEntry); err != nil {
		t.Fatalf("RecordBeadHistory (working) error: %v", err)
	}

	// Get bead history
	history, err := store.GetBeadHistory("bead-123")
	if err != nil {
		t.Fatalf("GetBeadHistory error: %v", err)
	}
	if len(history) != 2 {
		t.Errorf("GetBeadHistory returned %d entries, want 2", len(history))
	}
	if history[0].ToStatus != BeadStatusAssigned {
		t.Errorf("First entry ToStatus = %v, want %v", history[0].ToStatus, BeadStatusAssigned)
	}
	if history[1].FromStatus != BeadStatusAssigned || history[1].ToStatus != BeadStatusWorking {
		t.Errorf("Second entry transition incorrect: %v -> %v", history[1].FromStatus, history[1].ToStatus)
	}
}

func TestGetLatestBeadStatus(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "latest-status-sess", Name: "latest-status-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Record multiple transitions (using empty session_id to avoid FK issues - table allows NULL)
	transitions := []BeadStatus{BeadStatusAssigned, BeadStatusWorking, BeadStatusCompleted}
	for i, status := range transitions {
		entry := &BeadHistoryEntry{
			BeadID:       "bead-latest",
			ToStatus:     status,
			Trigger:      "test",
			TransitionAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}
		if i > 0 {
			entry.FromStatus = transitions[i-1]
		}
		if err := store.RecordBeadHistory(entry); err != nil {
			t.Fatalf("RecordBeadHistory error: %v", err)
		}
	}

	latest, err := store.GetLatestBeadStatus("bead-latest")
	if err != nil {
		t.Fatalf("GetLatestBeadStatus error: %v", err)
	}
	if latest == nil {
		t.Fatal("GetLatestBeadStatus returned nil")
	}
	if latest.ToStatus != BeadStatusCompleted {
		t.Errorf("GetLatestBeadStatus ToStatus = %v, want %v", latest.ToStatus, BeadStatusCompleted)
	}
}

func TestGetLatestBeadStatusNotFound(t *testing.T) {
	store := testStore(t)

	latest, err := store.GetLatestBeadStatus("nonexistent-bead")
	if err != nil {
		t.Fatalf("GetLatestBeadStatus error: %v", err)
	}
	if latest != nil {
		t.Errorf("GetLatestBeadStatus for nonexistent bead = %+v, want nil", latest)
	}
}

func TestGetBeadHistoryBySession(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "by-session-sess", Name: "by-session-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Create entries for multiple beads (no session_id to avoid FK issues, query by empty)
	beads := []string{"bead-a", "bead-b", "bead-c"}
	for _, beadID := range beads {
		entry := &BeadHistoryEntry{
			BeadID:   beadID,
			ToStatus: BeadStatusAssigned,
			Trigger:  "test",
		}
		if err := store.RecordBeadHistory(entry); err != nil {
			t.Fatalf("RecordBeadHistory error: %v", err)
		}
	}

	// Query with empty session - should return entries with NULL session_id
	history, err := store.GetBeadHistoryBySession("", 10)
	if err != nil {
		t.Fatalf("GetBeadHistoryBySession error: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("GetBeadHistoryBySession returned %d entries, want 3", len(history))
	}
}

func TestGetBeadHistoryByStatus(t *testing.T) {
	store := testStore(t)

	sess := &Session{
		ID: "by-status-sess", Name: "by-status-test", ProjectPath: "/test",
		CreatedAt: time.Now(), Status: SessionActive,
	}
	if err := store.CreateSession(sess); err != nil {
		t.Fatalf("CreateSession error: %v", err)
	}

	// Create entries with different statuses (no session_id to avoid FK issues)
	statuses := []BeadStatus{BeadStatusAssigned, BeadStatusWorking, BeadStatusCompleted, BeadStatusFailed}
	for i, status := range statuses {
		entry := &BeadHistoryEntry{
			BeadID:   "bead-" + string(rune('0'+i)),
			ToStatus: status,
			Trigger:  "test",
		}
		if err := store.RecordBeadHistory(entry); err != nil {
			t.Fatalf("RecordBeadHistory error: %v", err)
		}
	}

	// Query by specific status with empty session_id
	failed, err := store.GetBeadHistoryByStatus("", BeadStatusFailed, 10)
	if err != nil {
		t.Fatalf("GetBeadHistoryByStatus error: %v", err)
	}
	if len(failed) != 1 {
		t.Errorf("GetBeadHistoryByStatus(failed) returned %d entries, want 1", len(failed))
	}
}

func TestCountBeadTransitions(t *testing.T) {
	store := testStore(t)

	// Record 5 transitions for a bead (no session_id needed)
	for i := 0; i < 5; i++ {
		entry := &BeadHistoryEntry{
			BeadID:   "bead-count",
			ToStatus: BeadStatusWorking,
			Trigger:  "test",
		}
		if err := store.RecordBeadHistory(entry); err != nil {
			t.Fatalf("RecordBeadHistory error: %v", err)
		}
	}

	count, err := store.CountBeadTransitions("bead-count")
	if err != nil {
		t.Fatalf("CountBeadTransitions error: %v", err)
	}
	if count != 5 {
		t.Errorf("CountBeadTransitions = %d, want 5", count)
	}
}

func TestGetBeadHistoryStats(t *testing.T) {
	store := testStore(t)

	// Create diverse history entries (no session_id to avoid FK issues)
	entries := []struct {
		beadID    string
		status    BeadStatus
		agentName string
		reason    string
	}{
		{"bead-1", BeadStatusAssigned, "StatsAgent", ""},
		{"bead-1", BeadStatusWorking, "StatsAgent", ""},
		{"bead-1", BeadStatusCompleted, "StatsAgent", ""},
		{"bead-2", BeadStatusAssigned, "StatsAgent", ""},
		{"bead-2", BeadStatusWorking, "StatsAgent", ""},
		{"bead-2", BeadStatusFailed, "StatsAgent", "timeout"},
		{"bead-3", BeadStatusAssigned, "OtherAgent", ""},
		{"bead-3", BeadStatusFailed, "OtherAgent", "crash"},
	}

	for _, e := range entries {
		entry := &BeadHistoryEntry{
			BeadID:    e.beadID,
			ToStatus:  e.status,
			AgentName: e.agentName,
			Reason:    e.reason,
			Trigger:   "test",
		}
		if err := store.RecordBeadHistory(entry); err != nil {
			t.Fatalf("RecordBeadHistory error: %v", err)
		}
	}

	// Query stats with empty session_id
	stats, err := store.GetBeadHistoryStats("")
	if err != nil {
		t.Fatalf("GetBeadHistoryStats error: %v", err)
	}

	if stats.TotalTransitions != 8 {
		t.Errorf("TotalTransitions = %d, want 8", stats.TotalTransitions)
	}
	if stats.ByStatus["completed"] != 1 {
		t.Errorf("ByStatus[completed] = %d, want 1", stats.ByStatus["completed"])
	}
	if stats.ByStatus["failed"] != 2 {
		t.Errorf("ByStatus[failed] = %d, want 2", stats.ByStatus["failed"])
	}
	if stats.ByAgent["StatsAgent"] != 6 {
		t.Errorf("ByAgent[StatsAgent] = %d, want 6", stats.ByAgent["StatsAgent"])
	}
	if stats.FailureReasons["timeout"] != 1 {
		t.Errorf("FailureReasons[timeout] = %d, want 1", stats.FailureReasons["timeout"])
	}
}

func TestStoreDB(t *testing.T) {
	t.Parallel()
	store := testStore(t)

	db := store.DB()
	if db == nil {
		t.Fatal("DB() returned nil")
	}

	// Verify it's a working connection
	if err := db.Ping(); err != nil {
		t.Fatalf("DB().Ping(): %v", err)
	}
}

func TestRunGCPrunesStaleRuntimeData(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC()
	staleAfter := now.Add(-10 * time.Minute)

	if err := store.UpsertRuntimeSession(&RuntimeSession{
		Name:        "stale-session",
		CollectedAt: now.Add(-20 * time.Minute),
		StaleAfter:  staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeSession(stale): %v", err)
	}
	if err := store.UpsertRuntimeAgent(&RuntimeAgent{
		ID:          "stale-session:%1",
		SessionName: "stale-session",
		Pane:        "%1",
		AgentType:   "claude",
		State:       AgentStateIdle,
		CollectedAt: now.Add(-20 * time.Minute),
		StaleAfter:  staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeAgent(stale): %v", err)
	}
	if err := store.UpsertRuntimeWork(&RuntimeWork{
		BeadID:      "bd-stale",
		Title:       "stale",
		Status:      "open",
		CollectedAt: now.Add(-20 * time.Minute),
		StaleAfter:  staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeWork(stale): %v", err)
	}
	if err := store.UpsertRuntimeCoordination(&RuntimeCoordination{
		AgentName:   "BlueLake",
		CollectedAt: now.Add(-20 * time.Minute),
		StaleAfter:  staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeCoordination(stale): %v", err)
	}
	if err := store.UpsertRuntimeHandoff(&RuntimeHandoff{
		SessionName: "stale-session",
		CollectedAt: now.Add(-20 * time.Minute),
		StaleAfter:  staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeHandoff(stale): %v", err)
	}
	if err := store.UpsertRuntimeQuota(&RuntimeQuota{
		Provider:      "anthropic",
		Account:       "acct-1",
		UsedPct:       97.0,
		UsedPctKnown:  true,
		UsedPctSource: RuntimeQuotaUsedPctSourceProvider,
		CollectedAt:   now.Add(-20 * time.Minute),
		StaleAfter:    staleAfter,
	}); err != nil {
		t.Fatalf("UpsertRuntimeQuota(stale): %v", err)
	}
	if err := store.UpsertSourceHealth(&SourceHealth{
		SourceName:  "old-source",
		Available:   true,
		Healthy:     false,
		Reason:      "old check",
		LastCheckAt: now.Add(-25 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(old-source): %v", err)
	}
	if err := store.UpsertSourceHealth(&SourceHealth{
		SourceName:  "fresh-source",
		Available:   true,
		Healthy:     true,
		LastCheckAt: now,
	}); err != nil {
		t.Fatalf("UpsertSourceHealth(fresh-source): %v", err)
	}

	oldResolvedAt := now.Add(-8 * 24 * time.Hour)
	if err := store.CreateIncident(&Incident{
		ID:          "inc-resolved-old",
		Title:       "old resolved incident",
		Fingerprint: "source.outage:mail",
		Family:      "source.outage",
		Category:    "availability",
		Status:      IncidentStatusResolved,
		Severity:    SeverityError,
		AlertCount:  1,
		StartedAt:   oldResolvedAt.Add(-time.Hour),
		LastEventAt: oldResolvedAt,
		ResolvedAt:  &oldResolvedAt,
		ResolvedBy:  "operator",
	}); err != nil {
		t.Fatalf("CreateIncident(old resolved): %v", err)
	}
	if err := store.CreateIncident(&Incident{
		ID:          "inc-open-fresh",
		Title:       "fresh open incident",
		Fingerprint: "source.outage:tmux",
		Family:      "source.outage",
		Category:    "availability",
		Status:      IncidentStatusOpen,
		Severity:    SeverityWarning,
		AlertCount:  1,
		StartedAt:   now.Add(-time.Minute),
		LastEventAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateIncident(fresh open): %v", err)
	}

	expiredAt := now.Add(-time.Minute)
	if _, err := store.AppendAttentionEvent(&StoredAttentionEvent{
		Ts:            now.Add(-2 * time.Minute),
		Category:      "system",
		EventType:     "expired",
		Source:        "test",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "expired",
		ExpiresAt:     &expiredAt,
	}); err != nil {
		t.Fatalf("AppendAttentionEvent(expired): %v", err)
	}
	liveExpiry := now.Add(time.Hour)
	if _, err := store.AppendAttentionEvent(&StoredAttentionEvent{
		Ts:            now,
		Category:      "system",
		EventType:     "live",
		Source:        "test",
		Actionability: ActionabilityBackground,
		Severity:      SeverityInfo,
		Summary:       "live",
		ExpiresAt:     &liveExpiry,
	}); err != nil {
		t.Fatalf("AppendAttentionEvent(live): %v", err)
	}

	result, err := store.RunGC(DefaultRuntimeGCConfig())
	if err != nil {
		t.Fatalf("RunGC() error: %v", err)
	}
	if result.StaleSessions != 1 || result.StaleAgents != 1 || result.StaleWork != 1 || result.StaleCoordination != 1 || result.StaleHandoff != 1 || result.StaleQuota != 1 {
		t.Fatalf("unexpected stale runtime GC result: %+v", result)
	}
	if result.StaleSourceHealth != 1 {
		t.Fatalf("StaleSourceHealth = %d, want 1", result.StaleSourceHealth)
	}
	if result.ExpiredIncidents != 1 {
		t.Fatalf("ExpiredIncidents = %d, want 1", result.ExpiredIncidents)
	}
	if result.ExpiredAttentionEvents != 1 {
		t.Fatalf("ExpiredAttentionEvents = %d, want 1", result.ExpiredAttentionEvents)
	}

	sessions, err := store.GetFreshRuntimeSessions()
	if err != nil {
		t.Fatalf("GetFreshRuntimeSessions() error: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no fresh runtime sessions after GC, got %+v", sessions)
	}

	agents, err := store.GetRuntimeAgentsBySession("stale-session")
	if err != nil {
		t.Fatalf("GetRuntimeAgentsBySession() error: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected stale runtime agents to be pruned, got %+v", agents)
	}

	quota, err := store.ListFreshRuntimeQuota("")
	if err != nil {
		t.Fatalf("ListFreshRuntimeQuota() error: %v", err)
	}
	if len(quota) != 0 {
		t.Fatalf("expected stale runtime quota rows to be pruned, got %+v", quota)
	}

	oldHealth, err := store.GetSourceHealth("old-source")
	if err != nil {
		t.Fatalf("GetSourceHealth(old-source) error: %v", err)
	}
	if oldHealth != nil {
		t.Fatalf("expected old source health to be pruned, got %+v", oldHealth)
	}

	freshHealth, err := store.GetSourceHealth("fresh-source")
	if err != nil {
		t.Fatalf("GetSourceHealth(fresh-source) error: %v", err)
	}
	if freshHealth == nil || !freshHealth.Healthy {
		t.Fatalf("expected fresh source health to remain, got %+v", freshHealth)
	}

	oldIncident, err := store.GetIncident("inc-resolved-old")
	if err != nil {
		t.Fatalf("GetIncident(old resolved) error: %v", err)
	}
	if oldIncident != nil {
		t.Fatalf("expected old resolved incident to be pruned, got %+v", oldIncident)
	}

	freshIncident, err := store.GetIncident("inc-open-fresh")
	if err != nil {
		t.Fatalf("GetIncident(fresh open) error: %v", err)
	}
	if freshIncident == nil || freshIncident.Status != IncidentStatusOpen {
		t.Fatalf("expected fresh open incident to remain, got %+v", freshIncident)
	}

	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsSince() error: %v", err)
	}
	if len(events) != 1 || events[0].EventType != "live" {
		t.Fatalf("expected only live attention event to remain, got %+v", events)
	}
}

func TestCreateOrUpdateIncidentDeduplicatesByFingerprint(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC()

	first, err := store.CreateOrUpdateIncident(&Incident{
		Title:        "mail outage",
		Fingerprint:  "source.outage:mail",
		Family:       "source.outage",
		Category:     "availability",
		Status:       IncidentStatusOpen,
		Severity:     SeverityError,
		SessionNames: `["ntm--vibe-cockpit"]`,
		AgentIDs:     `["BlueLake"]`,
		AlertCount:   1,
		StartedAt:    now.Add(-2 * time.Minute),
		LastEventAt:  now.Add(-2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateIncident(first) error: %v", err)
	}

	second, err := store.CreateOrUpdateIncident(&Incident{
		Title:        "mail outage recurring",
		Fingerprint:  "source.outage:mail",
		Family:       "source.outage",
		Category:     "availability",
		Status:       IncidentStatusOpen,
		Severity:     SeverityCritical,
		SessionNames: `["ntm--vibe-cockpit"]`,
		AgentIDs:     `["BlueLake","RedStone"]`,
		AlertCount:   1,
		LastEventAt:  now,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateIncident(second) error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected deduped incident ID %q, got %q", first.ID, second.ID)
	}
	if second.AlertCount != 2 {
		t.Fatalf("AlertCount = %d, want 2", second.AlertCount)
	}
	if second.Severity != SeverityCritical {
		t.Fatalf("Severity = %s, want %s", second.Severity, SeverityCritical)
	}
	if second.Category != "availability" {
		t.Fatalf("Category = %q, want availability", second.Category)
	}

	gotByFingerprint, err := store.GetIncidentByFingerprint("source.outage:mail")
	if err != nil {
		t.Fatalf("GetIncidentByFingerprint() error: %v", err)
	}
	if gotByFingerprint == nil || gotByFingerprint.ID != first.ID {
		t.Fatalf("GetIncidentByFingerprint() = %+v, want incident %q", gotByFingerprint, first.ID)
	}

	open, err := store.ListOpenIncidents()
	if err != nil {
		t.Fatalf("ListOpenIncidents() error: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("expected 1 open incident, got %d", len(open))
	}
	if open[0].Fingerprint != "source.outage:mail" {
		t.Fatalf("Fingerprint = %q, want source.outage:mail", open[0].Fingerprint)
	}
}

// A failed window read makes the feed fall back to its in-memory journal, which
// silently hides other processes' events, so this runs against a real file.
func TestGetAttentionReplayWindow_WithRowsOnRealFile(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate error: %v", err)
	}

	empty, err := store.GetAttentionReplayWindow()
	if err != nil {
		t.Fatalf("window on an empty table: %v", err)
	}
	if empty.EventCount != 0 || empty.NewestCursor != 0 || empty.LastEventAt != nil {
		t.Fatalf("empty window = %+v", empty)
	}

	older := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	newer := older.Add(30 * time.Second)
	expires := newer.Add(time.Hour)
	var cursors []int64
	for _, ts := range []time.Time{older, newer} {
		cursor, err := store.AppendAttentionEvent(&StoredAttentionEvent{
			Ts:            ts,
			Category:      "mail",
			EventType:     "mail.received",
			Source:        "test",
			Actionability: ActionabilityBackground,
			Severity:      SeverityInfo,
			Summary:       "window test",
			DedupCount:    1,
			ExpiresAt:     &expires,
		})
		if err != nil {
			t.Fatalf("AppendAttentionEvent error: %v", err)
		}
		cursors = append(cursors, cursor)
	}

	window, err := store.GetAttentionReplayWindow()
	if err != nil {
		t.Fatalf("window with rows: %v", err)
	}
	if window.OldestCursor != cursors[0] || window.NewestCursor != cursors[1] || window.EventCount != 2 {
		t.Fatalf("window = %+v, want cursors %v and 2 events", window, cursors)
	}
	if window.LastEventAt == nil || !window.LastEventAt.Equal(newer) {
		t.Fatalf("LastEventAt = %v, want %v", window.LastEventAt, newer)
	}
}

func TestAttentionReplayQueriesByIncidentAndTimeRange(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.CreateIncident(&Incident{
		ID:          "inc-replay",
		Title:       "incident replay",
		Fingerprint: "incident.replay:test",
		Family:      "incident.replay",
		Category:    "testing",
		Status:      IncidentStatusOpen,
		Severity:    SeverityWarning,
		StartedAt:   now.Add(-time.Minute),
		LastEventAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateIncident() error: %v", err)
	}

	liveExpiry := now.Add(time.Hour)
	expiredAt := now.Add(-time.Minute)
	appendEvent := func(ts time.Time, eventType, summary string, expiresAt *time.Time) int64 {
		t.Helper()
		cursor, err := store.AppendAttentionEvent(&StoredAttentionEvent{
			Ts:            ts,
			SessionName:   "ntm--vibe-cockpit",
			Category:      "incident",
			EventType:     eventType,
			Source:        "test",
			Actionability: ActionabilityInteresting,
			Severity:      SeverityWarning,
			Summary:       summary,
			ExpiresAt:     expiresAt,
		})
		if err != nil {
			t.Fatalf("AppendAttentionEvent(%s) error: %v", eventType, err)
		}
		return cursor
	}

	earlyCursor := appendEvent(now.Add(-40*time.Second), "incident.promoted", "early", &liveExpiry)
	expiredCursor := appendEvent(now.Add(-30*time.Second), "incident.noise", "expired", &expiredAt)
	lateCursor := appendEvent(now.Add(-20*time.Second), "incident.recurred", "late", &liveExpiry)
	otherCursor := appendEvent(now.Add(-10*time.Second), "incident.opened", "other", &liveExpiry)

	for _, cursor := range []int64{earlyCursor, expiredCursor, lateCursor} {
		if err := store.LinkEventToIncident("inc-replay", cursor); err != nil {
			t.Fatalf("LinkEventToIncident(%d) error: %v", cursor, err)
		}
	}

	incidentEvents, err := store.GetEventsForIncident("inc-replay", 10)
	if err != nil {
		t.Fatalf("GetEventsForIncident() error: %v", err)
	}
	if len(incidentEvents) != 2 {
		t.Fatalf("len(GetEventsForIncident()) = %d, want 2", len(incidentEvents))
	}
	if incidentEvents[0].Cursor != earlyCursor || incidentEvents[1].Cursor != lateCursor {
		t.Fatalf("unexpected incident event cursors: %+v", incidentEvents)
	}

	rangedEvents, err := store.GetAttentionEventsInTimeRange(now.Add(-25*time.Second), now.Add(-5*time.Second), 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsInTimeRange() error: %v", err)
	}
	if len(rangedEvents) != 2 {
		t.Fatalf("len(GetAttentionEventsInTimeRange()) = %d, want 2", len(rangedEvents))
	}
	if rangedEvents[0].Cursor != lateCursor || rangedEvents[1].Cursor != otherCursor {
		t.Fatalf("unexpected ranged event cursors: %+v", rangedEvents)
	}

	empty, err := store.GetAttentionEventsInTimeRange(now, now.Add(-time.Second), 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsInTimeRange(reversed) error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected reversed time range to return no events, got %+v", empty)
	}
}

func TestCreateOrUpdateIncidentReopensRecentlyResolvedFingerprint(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC()

	created, err := store.CreateOrUpdateIncident(&Incident{
		Title:       "quota exceeded",
		Fingerprint: "quota.exceeded:anthropic",
		Family:      "quota.exceeded",
		Category:    "quota",
		Status:      IncidentStatusOpen,
		Severity:    SeverityError,
		AlertCount:  1,
		StartedAt:   now.Add(-10 * time.Minute),
		LastEventAt: now.Add(-10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateIncident(create) error: %v", err)
	}
	if err := store.UpdateIncidentStatus(created.ID, IncidentStatusResolved, "operator", "cleared"); err != nil {
		t.Fatalf("UpdateIncidentStatus(resolved) error: %v", err)
	}

	reopened, err := store.CreateOrUpdateIncident(&Incident{
		Title:       "quota exceeded again",
		Fingerprint: "quota.exceeded:anthropic",
		Family:      "quota.exceeded",
		Category:    "quota",
		Status:      IncidentStatusOpen,
		Severity:    SeverityCritical,
		AlertCount:  1,
		LastEventAt: now,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateIncident(reopen) error: %v", err)
	}
	if reopened.ID != created.ID {
		t.Fatalf("expected reopened incident ID %q, got %q", created.ID, reopened.ID)
	}
	if reopened.Status != IncidentStatusOpen {
		t.Fatalf("Status = %s, want %s", reopened.Status, IncidentStatusOpen)
	}
	if reopened.ResolvedAt != nil || reopened.ResolvedBy != "" {
		t.Fatalf("expected resolved fields to be cleared, got %+v", reopened)
	}
	if !strings.Contains(reopened.Notes, "reopened by fingerprint recurrence") {
		t.Fatalf("expected reopen note in %q", reopened.Notes)
	}
}

func TestCreateOrUpdateIncidentCreatesNewRowAfterResolvedWindow(t *testing.T) {
	store := testStore(t)
	now := time.Now().UTC()

	created, err := store.CreateOrUpdateIncident(&Incident{
		Title:       "source outage",
		Fingerprint: "source.outage:mail",
		Family:      "source.outage",
		Category:    "availability",
		Status:      IncidentStatusOpen,
		Severity:    SeverityError,
		AlertCount:  1,
		StartedAt:   now.Add(-3 * time.Hour),
		LastEventAt: now.Add(-3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateIncident(create) error: %v", err)
	}
	if err := store.UpdateIncidentStatus(created.ID, IncidentStatusResolved, "operator", "recovered"); err != nil {
		t.Fatalf("UpdateIncidentStatus(resolved) error: %v", err)
	}
	oldResolvedAt := now.Add(-2 * DefaultIncidentReopenWindow)
	if _, err := store.DB().Exec(`UPDATE incidents SET resolved_at = ?, last_event_at = ? WHERE id = ?`, oldResolvedAt, oldResolvedAt, created.ID); err != nil {
		t.Fatalf("force old resolved timestamp: %v", err)
	}

	fresh, err := store.CreateOrUpdateIncident(&Incident{
		Title:       "source outage again",
		Fingerprint: "source.outage:mail",
		Family:      "source.outage",
		Category:    "availability",
		Status:      IncidentStatusOpen,
		Severity:    SeverityCritical,
		AlertCount:  1,
		LastEventAt: now,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateIncident(new after window) error: %v", err)
	}
	if fresh.ID == created.ID {
		t.Fatalf("expected a new incident after reopen window, got reused ID %q", fresh.ID)
	}
}

func TestGCExpiredEvents_Boundary(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name     string
		expiry   time.Time
		wantGone bool
	}{
		{name: "expired two seconds ago", expiry: now.Add(-2 * time.Second), wantGone: true},
		{name: "expires in two seconds", expiry: now.Add(2 * time.Second), wantGone: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testRuntimeAttentionStore(t)
			expiry := tt.expiry
			appendRangeEvent(t, store, now.Add(-time.Hour), "boundary", &expiry)

			removed, err := store.GCExpiredEvents()
			if err != nil {
				t.Fatalf("GCExpiredEvents() error: %v", err)
			}
			if got := removed == 1; got != tt.wantGone {
				t.Fatalf("removed = %d, wantGone %v", removed, tt.wantGone)
			}
		})
	}
}

func TestGetLatestAttentionEventsInTimeRange(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	for _, offset := range []time.Duration{-50, -40, -30, -20, -10} {
		appendRangeEvent(t, store, now.Add(offset*time.Second), offset.String(), nil)
	}

	tests := []struct {
		name  string
		start time.Time
		end   time.Time
		limit int
		want  []string
	}{
		{name: "keeps the newest, oldest first", start: now.Add(-time.Minute), end: now, limit: 2, want: []string{"-20ns", "-10ns"}},
		{name: "end bound excludes later events", start: now.Add(-time.Minute), end: now.Add(-25 * time.Second), limit: 2, want: []string{"-40ns", "-30ns"}},
		{name: "limit above the row count", start: now.Add(-35 * time.Second), end: now, limit: 10, want: []string{"-30ns", "-20ns", "-10ns"}},
		{name: "reversed range", start: now, end: now.Add(-time.Minute), limit: 10, want: []string{}},
		{name: "nothing in range", start: now.Add(time.Minute), end: now.Add(2 * time.Minute), limit: 10, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := store.GetLatestAttentionEventsInTimeRange(tt.start, tt.end, tt.limit)
			if err != nil {
				t.Fatalf("GetLatestAttentionEventsInTimeRange() error: %v", err)
			}
			got := make([]string, 0, len(events))
			for _, event := range events {
				got = append(got, event.Summary)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("summaries = %v, want %v", got, tt.want)
			}
		})
	}
}

func testRuntimeAttentionStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	return store
}

func appendRangeEvent(t *testing.T, store *Store, ts time.Time, summary string, expiresAt *time.Time) {
	t.Helper()
	if _, err := store.AppendAttentionEvent(&StoredAttentionEvent{
		Ts:            ts,
		Category:      "system",
		EventType:     "system.health_change",
		Source:        "test",
		Actionability: "background",
		Severity:      "info",
		Summary:       summary,
		ExpiresAt:     expiresAt,
	}); err != nil {
		t.Fatalf("AppendAttentionEvent() error: %v", err)
	}
}

// applyMigrationsThrough applies migration files up to and including a version,
// with the same function production uses.
func applyMigrationsThrough(t *testing.T, store *Store, lastVersion int) {
	t.Helper()
	if _, err := store.DB().Exec(`
		CREATE TABLE IF NOT EXISTS _migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	files, err := GetMigrationFiles()
	if err != nil {
		t.Fatalf("GetMigrationFiles() error: %v", err)
	}
	for _, filename := range files {
		var version int
		if _, err := fmt.Sscanf(filename, "%03d_", &version); err != nil {
			t.Fatalf("parse version from %s: %v", filename, err)
		}
		if version > lastVersion {
			return
		}
		content, err := ReadMigration(filename)
		if err != nil {
			t.Fatalf("ReadMigration(%s) error: %v", filename, err)
		}
		if err := applyMigration(store.DB(), version, filename, content); err != nil {
			t.Fatalf("applyMigration(%s) error: %v", filename, err)
		}
	}
}

func TestMigration016_KeepsRowsWithEmptyRecipient(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	applyMigrationsThrough(t, store, 15)

	olderBuildInsert := func(summary string) {
		t.Helper()
		// An older build's insert names none of the new columns.
		if _, err := store.DB().Exec(`
			INSERT INTO attention_events (ts, category, event_type, source, actionability, severity, summary)
			VALUES (?, 'system', 'system.health_change', 'old', 'background', 'info', ?)`,
			time.Now().UTC(), summary); err != nil {
			t.Fatalf("insert %q: %v", summary, err)
		}
	}
	olderBuildInsert("old row")

	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("second Migrate() error: %v", err)
	}
	olderBuildInsert("older build after the upgrade")

	var project, agent string
	var messageID *int64
	if err := store.DB().QueryRow(`
		SELECT recipient_project, recipient_agent, message_id FROM attention_events WHERE summary = 'old row'`,
	).Scan(&project, &agent, &messageID); err != nil {
		t.Fatalf("read recipient columns: %v", err)
	}
	if project != "" || agent != "" || messageID != nil {
		t.Fatalf("old row has recipient %q %q %v, want empty", project, agent, messageID)
	}

	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsSince() error: %v", err)
	}
	if len(events) != 2 || events[0].Summary != "old row" {
		t.Fatalf("events = %+v, want both rows", events)
	}
	if got := events[0]; got.RecipientProject != "" || got.RecipientAgent != "" || got.MessageID != nil {
		t.Fatalf("recipient fields should be empty: %+v", got)
	}
}

func addressedStoredEvent(ts time.Time, project, agent string, messageID int64) *StoredAttentionEvent {
	return &StoredAttentionEvent{
		Ts:               ts,
		Category:         "mail",
		EventType:        "mail.received",
		Source:           "agent_mail",
		Actionability:    "interesting",
		Severity:         "info",
		Summary:          "mail for " + agent,
		RecipientProject: project,
		RecipientAgent:   agent,
		MessageID:        &messageID,
	}
}

func TestAttentionRecipient_EveryReadPathKeepsFields(t *testing.T) {
	now := time.Now().UTC()
	assertFields := func(t *testing.T, events []StoredAttentionEvent, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("events = %+v, want one", events)
		}
		got := events[0]
		if got.RecipientProject != "/proj" || got.RecipientAgent != "RedStone" || got.MessageID == nil || *got.MessageID != 42 {
			t.Fatalf("recipient fields lost: project %q agent %q message %v", got.RecipientProject, got.RecipientAgent, got.MessageID)
		}
	}

	tests := []struct {
		name string
		read func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error)
	}{
		{name: "since cursor", read: func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error) {
			return store.GetAttentionEventsSince(0, 10)
		}},
		{name: "time range", read: func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error) {
			return store.GetAttentionEventsInTimeRange(now.Add(-time.Minute), now.Add(time.Minute), 10)
		}},
		{name: "latest in time range", read: func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error) {
			return store.GetLatestAttentionEventsInTimeRange(now.Add(-time.Minute), now.Add(time.Minute), 10)
		}},
		{name: "for recipient", read: func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error) {
			return store.GetAttentionEventsForRecipient("RedStone", 0, 10)
		}},
		{name: "for recipient in another letter case", read: func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error) {
			return store.GetAttentionEventsForRecipient("redstone", 0, 10)
		}},
		{name: "for incident", read: func(t *testing.T, store *Store, cursor int64) ([]StoredAttentionEvent, error) {
			t.Helper()
			if err := store.CreateIncident(&Incident{
				ID: "inc-recipient", Title: "recipient", Status: IncidentStatusOpen, Severity: SeverityWarning,
				StartedAt: now, LastEventAt: now,
			}); err != nil {
				t.Fatalf("CreateIncident() error: %v", err)
			}
			if err := store.LinkEventToIncident("inc-recipient", cursor); err != nil {
				t.Fatalf("LinkEventToIncident() error: %v", err)
			}
			return store.GetEventsForIncident("inc-recipient", 10)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := testRuntimeAttentionStore(t)
			cursor, err := store.AppendAttentionEvent(addressedStoredEvent(now, "/proj", "RedStone", 42))
			if err != nil {
				t.Fatalf("AppendAttentionEvent() error: %v", err)
			}
			events, err := tt.read(t, store, cursor)
			assertFields(t, events, err)
		})
	}

	t.Run("insert inside a transaction", func(t *testing.T) {
		store := testRuntimeAttentionStore(t)
		if err := store.Transaction(func(tx *Tx) error {
			_, err := tx.AppendAttentionEvent(addressedStoredEvent(now, "/proj", "RedStone", 42))
			return err
		}); err != nil {
			t.Fatalf("Transaction() error: %v", err)
		}
		events, err := store.GetAttentionEventsSince(0, 10)
		assertFields(t, events, err)
	})
}

func TestGetAttentionEventsForRecipient_FiltersAndPages(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	add := func(event *StoredAttentionEvent) int64 {
		t.Helper()
		cursor, err := store.AppendAttentionEvent(event)
		if err != nil {
			t.Fatalf("AppendAttentionEvent() error: %v", err)
		}
		return cursor
	}
	first := add(addressedStoredEvent(now, "/one", "RedStone", 1))
	add(addressedStoredEvent(now, "/one", "BlueLake", 2))
	second := add(addressedStoredEvent(now, "/two", "RedStone", 1))
	past := now.Add(-time.Minute)
	expired := addressedStoredEvent(now, "/one", "RedStone", 3)
	expired.ExpiresAt = &past
	add(expired)

	all, err := store.GetAttentionEventsForRecipient("RedStone", 0, 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsForRecipient() error: %v", err)
	}
	if len(all) != 2 || all[0].Cursor != first || all[1].Cursor != second {
		t.Fatalf("events = %+v, want both projects and not the expired row", all)
	}

	page, err := store.GetAttentionEventsForRecipient("RedStone", first, 1)
	if err != nil {
		t.Fatalf("GetAttentionEventsForRecipient() error: %v", err)
	}
	if len(page) != 1 || page[0].Cursor != second {
		t.Fatalf("page after the first cursor = %+v", page)
	}

	none, err := store.GetAttentionEventsForRecipient("NoSuchAgent", 0, 10)
	if err != nil {
		t.Fatalf("GetAttentionEventsForRecipient() error: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("unknown name returned %+v", none)
	}
}

func TestAppendAddressed_OneRowPerKeyProperty(t *testing.T) {
	dir := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		store, err := Open(filepath.Join(dir, fmt.Sprintf("state-%d.db", run)))
		if err != nil {
			rt.Fatal(err)
		}
		defer store.Close()
		if err := store.Migrate(); err != nil {
			rt.Fatal(err)
		}

		seen := map[string]int64{}
		now := time.Now().UTC()
		steps := rapid.IntRange(1, 50).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			project := rapid.SampledFrom([]string{"/one", "/two", "/three"}).Draw(rt, "project")
			agent := rapid.SampledFrom([]string{"RedStone", "redstone", "BlueLake"}).Draw(rt, "agent")
			id := int64(rapid.IntRange(1, 5).Draw(rt, "id"))
			key := fmt.Sprintf("%s|%s|%d", project, strings.ToLower(agent), id)

			cursor, created, err := store.AppendAddressedAttentionEvent(addressedStoredEvent(now, project, agent, id))
			if err != nil {
				rt.Fatal(err)
			}
			first, repeat := seen[key]
			if created == repeat {
				rt.Fatalf("created = %v for key %s, seen before: %v", created, key, repeat)
			}
			if repeat && cursor != first {
				rt.Fatalf("repeat cursor = %d, want %d", cursor, first)
			}
			seen[key] = cursor
		}
		var rows int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM attention_events`).Scan(&rows); err != nil {
			rt.Fatal(err)
		}
		if rows != len(seen) {
			rt.Fatalf("rows = %d, keys = %d", rows, len(seen))
		}
	})
}

func TestAddressed_DistinctKeysGetTheirOwnRows(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	for _, event := range []*StoredAttentionEvent{
		addressedStoredEvent(now, "/one", "RedStone", 42),
		addressedStoredEvent(now, "/one", "BlueLake", 42), // same message, other recipient
		addressedStoredEvent(now, "/two", "RedStone", 42), // same id, other project
	} {
		if _, created, err := store.AppendAddressedAttentionEvent(event); err != nil || !created {
			t.Fatalf("insert %s %s: created %v, err %v", event.RecipientProject, event.RecipientAgent, created, err)
		}
	}
	if _, created, err := store.AppendAddressedAttentionEvent(addressedStoredEvent(now, "/one", "REDSTONE", 42)); err != nil || created {
		t.Fatalf("name in another letter case: created %v, err %v, want the existing row", created, err)
	}
}

func TestAddressed_NullMessageIDNeverConflicts(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	for i := 0; i < 2; i++ {
		event := addressedStoredEvent(now, "/one", "RedStone", 0)
		event.MessageID = nil
		if _, err := store.AppendAttentionEvent(event); err != nil {
			t.Fatalf("plain insert %d with no message id: %v", i, err)
		}
	}
	event := addressedStoredEvent(now, "/one", "RedStone", 0)
	event.MessageID = nil
	if _, _, err := store.AppendAddressedAttentionEvent(event); err == nil {
		t.Fatal("an addressed insert with no message id should fail")
	}
}

func TestAddressed_ExpiredRowCountsUntilRemoved(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	expired := addressedStoredEvent(now.Add(-8*24*time.Hour), "/one", "RedStone", 42)
	expired.ExpiresAt = &past
	first, created, err := store.AppendAddressedAttentionEvent(expired)
	if err != nil || !created {
		t.Fatalf("first insert: created %v, err %v", created, err)
	}

	if cursor, created, err := store.AppendAddressedAttentionEvent(addressedStoredEvent(now, "/one", "RedStone", 42)); err != nil || created || cursor != first {
		t.Fatalf("before cleanup: cursor %d created %v err %v, want the expired row %d", cursor, created, err, first)
	}
	if _, err := store.GCExpiredEvents(); err != nil {
		t.Fatalf("GCExpiredEvents() error: %v", err)
	}
	if cursor, created, err := store.AppendAddressedAttentionEvent(addressedStoredEvent(now, "/one", "RedStone", 42)); err != nil || !created || cursor == first {
		t.Fatalf("after cleanup: cursor %d created %v err %v, want a new row", cursor, created, err)
	}
}

func TestGetAddressedAttentionEvent(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	none, err := store.GetAddressedAttentionEvent("/one", "RedStone", 42)
	if err != nil || none != nil {
		t.Fatalf("no row: event %+v, err %v", none, err)
	}
	cursor, _, err := store.AppendAddressedAttentionEvent(addressedStoredEvent(time.Now().UTC(), "/one", "RedStone", 42))
	if err != nil {
		t.Fatal(err)
	}
	found, err := store.GetAddressedAttentionEvent("/one", "redstone", 42)
	if err != nil || found == nil || found.Cursor != cursor || found.RecipientAgent != "RedStone" {
		t.Fatalf("found %+v, err %v, want cursor %d", found, err, cursor)
	}
}

func TestMigration017_RemovesSprintOneDuplicates(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	applyMigrationsThrough(t, store, 16)

	now := time.Now().UTC()
	insert := func(event *StoredAttentionEvent) int64 {
		t.Helper()
		cursor, err := store.AppendAttentionEvent(event)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
		return cursor
	}
	oldest := insert(addressedStoredEvent(now, "/one", "RedStone", 42))
	insert(addressedStoredEvent(now, "/one", "RedStone", 42))
	insert(addressedStoredEvent(now, "/one", "redstone", 42))
	blue := insert(addressedStoredEvent(now, "/one", "BlueLake", 42))
	unaddressed := addressedStoredEvent(now, "", "", 0)
	unaddressed.MessageID = nil
	plain := insert(unaddressed)
	plainAgain := insert(unaddressed)

	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	events, err := store.GetAttentionEventsSince(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	var cursors []int64
	for _, event := range events {
		cursors = append(cursors, event.Cursor)
	}
	want := []int64{oldest, blue, plain, plainAgain}
	if fmt.Sprint(cursors) != fmt.Sprint(want) {
		t.Fatalf("cursors after 017 = %v, want %v", cursors, want)
	}
}

func toldRecord(project, agent string, messageID int64, toldAt time.Time) ToldRecord {
	return ToldRecord{ProjectKey: project, AgentName: agent, MessageID: messageID, ToldBy: ToldByStopHook, ToldAt: toldAt}
}

func TestClaimToldRecord_OneWinnerProperty(t *testing.T) {
	dir := t.TempDir()
	var run int
	rapid.Check(t, func(rt *rapid.T) {
		run++
		store, err := Open(filepath.Join(dir, fmt.Sprintf("state-%d.db", run)))
		if err != nil {
			rt.Fatal(err)
		}
		defer store.Close()
		if err := store.Migrate(); err != nil {
			rt.Fatal(err)
		}

		seen := map[string]bool{}
		now := time.Now().UTC()
		steps := rapid.IntRange(1, 50).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			project := rapid.SampledFrom([]string{"/one", "/two", "/three"}).Draw(rt, "project")
			base := rapid.SampledFrom([]string{"RedStone", "BlueLake", "GreenCastle"}).Draw(rt, "agent")
			agent := randomCase(rt, base)
			id := int64(rapid.IntRange(1, 5).Draw(rt, "id"))
			key := fmt.Sprintf("%s|%s|%d", project, strings.ToLower(agent), id)

			won, err := store.ClaimToldRecord(toldRecord(project, agent, id, now))
			if err != nil {
				rt.Fatal(err)
			}
			if won == seen[key] {
				rt.Fatalf("claim %s won = %v, seen before: %v", key, won, seen[key])
			}
			seen[key] = true
		}
	})
}

// randomCase changes the letter case of each letter in name at random.
func randomCase(rt *rapid.T, name string) string {
	var b strings.Builder
	for i, r := range name {
		if rapid.Bool().Draw(rt, fmt.Sprintf("upper-%d", i)) {
			b.WriteString(strings.ToUpper(string(r)))
		} else {
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

// Reminders don't repeat after expiry: mailToAnnounce skips messages older than
// ToldRecordRetention (Loop 10).
func TestClaimToldRecord_ExpiredClaimWinsAgain(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	if won, err := store.ClaimToldRecord(toldRecord("/one", "BlueLake", 42, now.Add(-8*24*time.Hour))); err != nil || !won {
		t.Fatalf("old claim: won %v, err %v", won, err)
	}
	if won, err := store.ClaimToldRecord(toldRecord("/one", "bluelake", 42, now)); err != nil || !won {
		t.Fatalf("claim after expiry: won %v, err %v, want a win", won, err)
	}
	if won, err := store.ClaimToldRecord(toldRecord("/one", "BlueLake", 42, now)); err != nil || won {
		t.Fatalf("repeat of a fresh claim: won %v, err %v, want no win", won, err)
	}
}

func TestClaimToldRecord_DistinctKeysAndBadInput(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	for _, rec := range []ToldRecord{
		toldRecord("/one", "BlueLake", 42, now),
		toldRecord("/one", "RedStone", 42, now), // same message, other agent
		toldRecord("/two", "BlueLake", 42, now), // same agent and message, other project
	} {
		if won, err := store.ClaimToldRecord(rec); err != nil || !won {
			t.Fatalf("claim %+v: won %v, err %v", rec, won, err)
		}
	}
	for _, rec := range []ToldRecord{
		toldRecord("/one", "BlueLake", 0, now),
		toldRecord("", "BlueLake", 1, now),
		toldRecord("/one", " ", 1, now),
	} {
		if won, err := store.ClaimToldRecord(rec); err == nil || won {
			t.Fatalf("claim %+v: won %v, err %v, want an error", rec, won, err)
		}
	}
}

// Loop 5, Behavior 10: a release removes only the same writer's claim from the
// same moment, so a failed nudge can be retried without undoing the hook's
// claim or an earlier one.
func TestReleaseToldRecord_OnlyOwnClaim(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	t0 := time.Now().UTC().Truncate(time.Second)
	hook := toldRecord("/p", "BlueLake", 1, t0)
	hook.ToldBy = ToldByStopHook
	nudge := toldRecord("/p", "BlueLake", 2, t0)
	nudge.ToldBy = ToldByIdleNudge
	for _, rec := range []ToldRecord{hook, nudge} {
		if won, err := store.ClaimToldRecord(rec); err != nil || !won {
			t.Fatalf("claim %+v: won %v, err %v", rec, won, err)
		}
	}

	notMine := hook
	notMine.ToldBy = ToldByIdleNudge
	if err := store.ReleaseToldRecord(notMine); err != nil {
		t.Fatal(err)
	}
	if won, _ := store.ClaimToldRecord(hook); won {
		t.Fatal("released the hook's claim")
	}

	earlier := nudge
	earlier.ToldAt = t0.Add(-time.Minute)
	if err := store.ReleaseToldRecord(earlier); err != nil {
		t.Fatal(err)
	}
	if won, _ := store.ClaimToldRecord(nudge); won {
		t.Fatal("a release from another moment removed the claim")
	}

	if err := store.ReleaseToldRecord(nudge); err != nil {
		t.Fatal(err)
	}
	if won, err := store.ClaimToldRecord(nudge); err != nil || !won {
		t.Fatalf("own claim not released: won %v, err %v", won, err)
	}
	missing := toldRecord("/p", "BlueLake", 99, t0)
	if err := store.ReleaseToldRecord(missing); err != nil {
		t.Fatalf("releasing a record that doesn't exist: %v", err)
	}
}

// Behavior 10 property: after any sequence of claims and releases by the two
// writers, a message is held exactly when its last winning claim wasn't
// released by the same writer at the same moment.
func TestReleaseToldRecord_Property(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		store := testRuntimeAttentionStore(t)
		t0 := time.Now().UTC().Truncate(time.Second)
		type held struct {
			by string
			at time.Time
		}
		model := map[int64]*held{}
		steps := rapid.IntRange(1, 20).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			rec := toldRecord("/p", "BlueLake", int64(rapid.IntRange(1, 3).Draw(rt, "message")), t0.Add(time.Duration(rapid.IntRange(0, 2).Draw(rt, "minute"))*time.Minute))
			rec.ToldBy = rapid.SampledFrom([]string{ToldByStopHook, ToldByIdleNudge}).Draw(rt, "by")
			if rapid.Bool().Draw(rt, "release") {
				if err := store.ReleaseToldRecord(rec); err != nil {
					rt.Fatal(err)
				}
				if h := model[rec.MessageID]; h != nil && h.by == rec.ToldBy && h.at.Equal(rec.ToldAt) {
					model[rec.MessageID] = nil
				}
				continue
			}
			won, err := store.ClaimToldRecord(rec)
			if err != nil {
				rt.Fatal(err)
			}
			if wantWin := model[rec.MessageID] == nil; won != wantWin {
				rt.Fatalf("claim %+v won %v, model says %v", rec, won, wantWin)
			}
			if won {
				model[rec.MessageID] = &held{by: rec.ToldBy, at: rec.ToldAt}
			}
		}
	})
}

func TestGetToldRecord(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	rec := toldRecord("/one", "BlueLake", 42, now)
	rec.ToldBy = ToldByIdleNudge
	if won, err := store.ClaimToldRecord(rec); err != nil || !won {
		t.Fatalf("claim: won %v, err %v", won, err)
	}

	got, err := store.GetToldRecord("/one", "bluelake", 42) // names compare without case
	if err != nil || got == nil {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if got.ToldBy != ToldByIdleNudge || !got.ToldAt.Equal(now) || got.AgentName != "BlueLake" {
		t.Fatalf("got %+v, want told_by %q at %s", got, ToldByIdleNudge, now)
	}
	if missing, err := store.GetToldRecord("/one", "BlueLake", 43); err != nil || missing != nil {
		t.Fatalf("untold message: %+v, %v", missing, err)
	}
}

func TestRunGC_RemovesOldToldRecords(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	for i, toldAt := range []time.Time{now.Add(-8 * 24 * time.Hour), now.Add(-10 * 24 * time.Hour), now.Add(-time.Hour)} {
		if _, err := store.ClaimToldRecord(toldRecord("/one", "BlueLake", int64(i+1), toldAt)); err != nil {
			t.Fatal(err)
		}
	}
	result, err := store.RunGC(DefaultRuntimeGCConfig())
	if err != nil {
		t.Fatalf("RunGC() error: %v", err)
	}
	if result.ExpiredToldRecords != 2 {
		t.Fatalf("ExpiredToldRecords = %d, want 2", result.ExpiredToldRecords)
	}
	var left int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM told_records`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("rows left = %d, err %v, want 1", left, err)
	}
}

func TestMigration018_CreatesToldRecords(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	applyMigrationsThrough(t, store, 17)
	if _, err := store.DB().Exec(`SELECT 1 FROM told_records`); err == nil {
		t.Fatal("told_records exists before migration 018")
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate() error: %v", err)
	}
	if won, err := store.ClaimToldRecord(toldRecord("/one", "BlueLake", 42, time.Now().UTC())); err != nil || !won {
		t.Fatalf("claim after 018: won %v, err %v", won, err)
	}
}

func TestReserveAttentionCursor(t *testing.T) {
	store := testRuntimeAttentionStore(t)
	now := time.Now().UTC()
	nextCursor := func() int64 {
		t.Helper()
		cursor, err := store.AppendAttentionEvent(&StoredAttentionEvent{
			Ts:            now,
			Category:      "system",
			EventType:     "system.health_change",
			Source:        "test",
			Actionability: "background",
			Severity:      "info",
			Summary:       "reserve",
		})
		if err != nil {
			t.Fatalf("AppendAttentionEvent() error: %v", err)
		}
		return cursor
	}

	steps := []struct {
		name    string
		reserve int64
		want    int64
	}{
		{name: "empty table with no sequence row", reserve: 10, want: 11},
		{name: "never lowers", reserve: 5, want: 12},
		{name: "equal to the current value", reserve: 12, want: 13},
		{name: "raises an existing row", reserve: 50, want: 51},
		{name: "zero does nothing", reserve: 0, want: 52},
	}
	for _, step := range steps {
		if err := store.ReserveAttentionCursor(step.reserve); err != nil {
			t.Fatalf("%s: ReserveAttentionCursor() error: %v", step.name, err)
		}
		if got := nextCursor(); got != step.want {
			t.Fatalf("%s: next cursor = %d, want %d", step.name, got, step.want)
		}
	}
}

// TestOpen_WaitsWhenNewFileIsBusy covers the switch to WAL mode, which fails
// straight away when another connection is writing, without using busy_timeout.
func TestOpen_WaitsWhenNewFileIsBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	other, err := sql.Open(sqliteutil.DriverName, sqliteutil.FileDSN(path))
	if err != nil {
		t.Fatalf("open other connection: %v", err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	if _, err := other.Exec(`CREATE TABLE holder (id INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := other.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin: %v", err)
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, err := other.Exec(`COMMIT`)
		released <- err
	}()

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() while the file is busy: %v", err)
	}
	defer store.Close()
	if err := <-released; err != nil {
		t.Fatalf("commit: %v", err)
	}
	var mode string
	if err := store.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}

func TestApplyMigrations_ConcurrentFirstUse(t *testing.T) {
	const writers = 8
	files, err := GetMigrationFiles()
	if err != nil {
		t.Fatalf("GetMigrationFiles() error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "state.db")
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			defer store.Close()
			errs <- store.Migrate()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent first use: %v", err)
		}
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer store.Close()
	var applied int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM _migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(files) {
		t.Fatalf("applied %d migrations, want %d", applied, len(files))
	}
}
