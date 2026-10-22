package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agentsession"
	"github.com/Dicklesworthstone/ntm/internal/audit"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	sessionDirName = "sessions"
	archiveDirName = "archived"
	fileExtension  = ".json"
)

// StorageDir returns the path to the session storage directory.
// Uses ~/.ntm/sessions by default.
// Falls back to temp directory if home directory is unavailable.
func StorageDir() string {
	ntmDir, err := util.NTMDir()
	if err != nil || ntmDir == "" {
		// Fallback to temp directory to ensure an absolute path
		// (relative paths would fragment sessions across working directories)
		return filepath.Join(os.TempDir(), "ntm", sessionDirName)
	}
	return filepath.Join(ntmDir, sessionDirName)
}

// ArchiveDir returns the path to the archived-session storage directory
// (~/.ntm/sessions/archived). Archiving moves a saved session here so it no
// longer appears in the active `ntm sessions list` while remaining restorable.
func ArchiveDir() string {
	return filepath.Join(StorageDir(), archiveDirName)
}

// Save writes a session state to disk.
func Save(state *SessionState, opts SaveOptions) (path string, err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sessionName := ""
	if state != nil {
		sessionName = state.Name
	}
	saveName := opts.Name
	if saveName == "" {
		saveName = sessionName
	}
	_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.save", map[string]interface{}{
		"phase":           "start",
		"session":         sessionName,
		"name":            saveName,
		"overwrite":       opts.Overwrite,
		"include_git":     opts.IncludeGit,
		"description_set": opts.Description != "",
		"correlation_id":  correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"session":        sessionName,
			"name":           saveName,
			"overwrite":      opts.Overwrite,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if state != nil {
			payload["agents"] = state.Agents.Total()
			payload["panes"] = len(state.Panes)
			payload["work_dir"] = state.WorkDir
		}
		if path != "" {
			payload["path"] = path
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(sessionName, audit.EventTypeCommand, audit.ActorSystem, "session.save", payload, nil)
	}()

	if state == nil {
		return "", fmt.Errorf("session state cannot be nil")
	}

	unlock, err := acquireLock()
	if err != nil {
		return "", err
	}
	defer unlock()

	dir := StorageDir()

	// Ensure directory exists
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create sessions directory: %w", err)
	}

	// Determine filename
	name := opts.Name
	if name == "" {
		name = state.Name
	}

	// Sanitize filename
	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return "", err
	}
	filename := name + fileExtension
	path = filepath.Join(dir, filename)

	// Check if file exists
	if !opts.Overwrite {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("session '%s' already saved (use --overwrite to replace)", name)
		}
	}

	// Marshal state
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize session state: %w", err)
	}

	// Atomic write using utility function
	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("writing session file: %w", err)
	}

	// Persist the reconciled pane record (if Capture produced one) keyed by
	// the live session name, never the --name save-as alias. Best-effort:
	// a refresh failure never fails the save.
	refreshPaneRecordAfterSave(dir, state.Name, state.refreshedPaneRecords)

	return path, nil
}

// Load reads a session state from disk.
func Load(name string) (state *SessionState, err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sanitized := sanitizeFilename(name)
	_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.load", map[string]interface{}{
		"phase":          "start",
		"requested":      name,
		"sanitized":      sanitized,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"requested":      name,
			"sanitized":      sanitized,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if state != nil {
			payload["session"] = state.Name
			payload["agents"] = state.Agents.Total()
			payload["panes"] = len(state.Panes)
			payload["work_dir"] = state.WorkDir
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.load", payload, nil)
	}()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(StorageDir(), name+fileExtension)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved session named '%s'", name)
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var parsed SessionState
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	state = &parsed
	return state, nil
}

// Delete removes a saved session.
func Delete(name string) (err error) {
	correlationID := audit.NewCorrelationID()
	auditStart := time.Now()
	sanitized := sanitizeFilename(name)
	_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.delete", map[string]interface{}{
		"phase":          "start",
		"requested":      name,
		"sanitized":      sanitized,
		"correlation_id": correlationID,
	}, nil)
	defer func() {
		payload := map[string]interface{}{
			"phase":          "finish",
			"requested":      name,
			"sanitized":      sanitized,
			"deleted":        err == nil,
			"success":        err == nil,
			"duration_ms":    time.Since(auditStart).Milliseconds(),
			"correlation_id": correlationID,
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = audit.LogEvent(name, audit.EventTypeCommand, audit.ActorSystem, "session.delete", payload, nil)
	}()

	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return err
	}
	path := filepath.Join(StorageDir(), name+fileExtension)

	err = os.Remove(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("no saved session named '%s'", name)
	}
	if err == nil {
		removePaneRecordSidecar(StorageDir(), name)
	}
	return err
}

// SavedSession represents a saved session entry.
type SavedSession struct {
	Name      string    `json:"name"`
	SavedAt   time.Time `json:"saved_at"`
	WorkDir   string    `json:"cwd"`
	Agents    int       `json:"agents"`
	GitBranch string    `json:"git_branch,omitempty"`
	FilePath  string    `json:"file_path"`
	FileSize  int64     `json:"file_size"`
}

// List returns all saved sessions.
func List() ([]SavedSession, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	dir := StorageDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SavedSession{}, nil
		}
		return nil, fmt.Errorf("failed to read sessions directory: %w", err)
	}

	var sessions []SavedSession
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), fileExtension) {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), fileExtension)
		path := filepath.Join(dir, entry.Name())

		// Load minimal info
		state, err := Load(name)
		if err != nil {
			continue // Skip corrupted files
		}

		info, _ := entry.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}

		sessions = append(sessions, SavedSession{
			// List/save/show/delete address saved sessions by the on-disk entry name (filename),
			// not by the original tmux session name captured in the state.
			Name:      name,
			SavedAt:   state.SavedAt,
			WorkDir:   state.WorkDir,
			Agents:    state.Agents.Total(),
			GitBranch: state.GitBranch,
			FilePath:  path,
			FileSize:  size,
		})
	}

	// Sort by save time (newest first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SavedAt.After(sessions[j].SavedAt)
	})

	return sessions, nil
}

// Exists checks if a saved session exists.
func Exists(name string) bool {
	name, err := normalizeSavedSessionName(name)
	if err != nil {
		return false
	}
	path := filepath.Join(StorageDir(), name+fileExtension)
	_, err = os.Stat(path)
	return err == nil
}

// sanitizeFilename removes or replaces characters not suitable for filenames.
func sanitizeFilename(name string) string {
	// Replace problematic characters
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "_",
		"?", "_",
		"\"", "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	return replacer.Replace(name)
}

// Archive moves a saved session from the active store into the archived store.
// The session remains restorable via ListArchived/Unarchive but no longer
// appears in List(). Returns the new archived file path.
func Archive(name string) (archivedPath string, err error) {
	unlock, err := acquireLock()
	if err != nil {
		return "", err
	}
	defer unlock()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return "", err
	}

	src := filepath.Join(StorageDir(), name+fileExtension)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no saved session named '%s'", name)
		}
		return "", err
	}

	archiveDir := ArchiveDir()
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create archive directory: %w", err)
	}

	dst := filepath.Join(archiveDir, name+fileExtension)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("session '%s' is already archived", name)
	}

	if err := moveFile(src, dst); err != nil {
		return "", fmt.Errorf("archiving session: %w", err)
	}
	movePaneRecordSidecar(StorageDir(), ArchiveDir(), name)
	return dst, nil
}

// Unarchive moves an archived session back into the active store, making it
// appear in List() again. Returns the restored active file path.
func Unarchive(name string) (restoredPath string, err error) {
	unlock, err := acquireLock()
	if err != nil {
		return "", err
	}
	defer unlock()

	name, err = normalizeSavedSessionName(name)
	if err != nil {
		return "", err
	}

	src := filepath.Join(ArchiveDir(), name+fileExtension)
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no archived session named '%s'", name)
		}
		return "", err
	}

	dir := StorageDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("failed to create sessions directory: %w", err)
	}

	dst := filepath.Join(dir, name+fileExtension)
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("an active session named '%s' already exists", name)
	}

	if err := moveFile(src, dst); err != nil {
		return "", fmt.Errorf("unarchiving session: %w", err)
	}
	movePaneRecordSidecar(ArchiveDir(), StorageDir(), name)
	return dst, nil
}

// ListArchived returns all archived saved sessions.
func ListArchived() ([]SavedSession, error) {
	unlock, err := acquireLock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	dir := ArchiveDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []SavedSession{}, nil
		}
		return nil, fmt.Errorf("failed to read archive directory: %w", err)
	}

	var sessions []SavedSession
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), fileExtension) {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), fileExtension)
		path := filepath.Join(dir, entry.Name())

		state, err := loadFrom(path)
		if err != nil {
			continue // Skip corrupted files
		}

		info, _ := entry.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}

		sessions = append(sessions, SavedSession{
			Name:      name,
			SavedAt:   state.SavedAt,
			WorkDir:   state.WorkDir,
			Agents:    state.Agents.Total(),
			GitBranch: state.GitBranch,
			FilePath:  path,
			FileSize:  size,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SavedAt.After(sessions[j].SavedAt)
	})

	return sessions, nil
}

// IsArchived reports whether an archived session with the given name exists.
func IsArchived(name string) bool {
	name, err := normalizeSavedSessionName(name)
	if err != nil {
		return false
	}
	path := filepath.Join(ArchiveDir(), name+fileExtension)
	_, err = os.Stat(path)
	return err == nil
}

// loadFrom reads and parses a session state file at an explicit path. Unlike
// Load, it does not assume the file lives in the active StorageDir, so it works
// for archived sessions too.
func loadFrom(path string) (*SessionState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed SessionState
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}
	return &parsed, nil
}

// moveFile relocates a file from src to dst, falling back to copy+remove when
// the two paths are on different filesystems (cross-device rename).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := util.AtomicWriteFile(dst, data, 0600); err != nil {
		return err
	}
	return os.Remove(src)
}

func normalizeSavedSessionName(name string) (string, error) {
	sanitized := strings.TrimSpace(sanitizeFilename(name))
	if sanitized == "" {
		return "", fmt.Errorf("session name cannot be empty")
	}
	if sanitized == "." || sanitized == ".." {
		return "", fmt.Errorf("session name cannot be '.' or '..'")
	}
	return sanitized, nil
}

// --- Per-pane provider session id record ---
//
// Rationale: agentsession.Discover returns only the single newest session per
// (agentType, workDir), so same-cwd panes would otherwise all save the same
// id. The record stores one entry per agent pane keyed by stable identity
// (window index, agent type, NTM index — never the tmux %N id, which changes
// on restore). Spawn writes the roster with empty ids; save reconciles the
// recorded ids, lazily fills new panes, and dedupes within a provider (see
// reconcilePaneIDs in capture.go). All record I/O is best-effort and never
// fails spawn, add, or save.

const paneRecordsDirName = "pane-records"

const paneRecordVersion = 1

// PaneRecordEntry is one roster row: a pane's stable identity plus its
// provider session linkage. Readers ignore unknown fields; every session_*
// field is omitempty, and session_file always changes in lockstep with
// session_id wherever an id is assigned or cleared.
type PaneRecordEntry struct {
	WindowIndex     int    `json:"window_index"`
	AgentType       string `json:"agent_type"`
	NTMIndex        int    `json:"ntm_index"`
	SessionID       string `json:"session_id,omitempty"`
	SessionProvider string `json:"session_provider,omitempty"`
	SessionFile     string `json:"session_file,omitempty"`
}

// PaneRecordFile is the on-disk envelope. An empty record is an envelope
// with an empty array; the file is always kept so the add-path append always
// has a target.
type PaneRecordFile struct {
	Version int               `json:"version"`
	Entries []PaneRecordEntry `json:"entries"`
}

// paneRecordKey is the stable pane identity shared by reconcile and
// stale-drop so both agree on what "same pane" means.
type paneRecordKey struct {
	windowIndex int
	agentType   string
	ntmIndex    int
}

func paneRecordKeyOfPane(p tmux.Pane) paneRecordKey {
	return paneRecordKey{windowIndex: p.WindowIndex, agentType: string(p.Type), ntmIndex: p.NTMIndex}
}

func paneRecordKeyOfEntry(e PaneRecordEntry) paneRecordKey {
	return paneRecordKey{windowIndex: e.WindowIndex, agentType: e.AgentType, ntmIndex: e.NTMIndex}
}

// isAgentPaneForRecord reports whether a pane gets a record entry. Panes
// with no provider-session resume path (user/editor panes) are skipped.
func isAgentPaneForRecord(p tmux.Pane) bool {
	return agentsession.ResumeProvider(string(p.Type)) != ""
}

// paneRecordPath resolves the sidecar path for a live tmux session name,
// always through normalizeSavedSessionName (never a --name save-as alias).
func paneRecordPath(dir, sessionName string) (string, error) {
	name, err := normalizeSavedSessionName(sessionName)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, paneRecordsDirName, name+fileExtension), nil
}

// loadPaneRecord reads the record. A missing or corrupt file behaves like an
// empty record: save falls back to lazy fill for every pane.
func loadPaneRecord(dir, sessionName string) []PaneRecordEntry {
	path, err := paneRecordPath(dir, sessionName)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec PaneRecordFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	return rec.Entries
}

func storePaneRecord(dir, sessionName string, entries []PaneRecordEntry) error {
	path, err := paneRecordPath(dir, sessionName)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []PaneRecordEntry{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create pane-records directory: %w", err)
	}
	data, err := json.MarshalIndent(PaneRecordFile{Version: paneRecordVersion, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize pane record: %w", err)
	}
	if err := util.AtomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing pane record file: %w", err)
	}
	return nil
}

// upsertPaneRecordEntries merges entries over the stored record. Stored
// non-empty ids for matched keys are never overwritten with empties
// (merge-only: a spawn/save race or re-run cannot clobber a filled id), and
// stored keys absent from entries are dropped (stale panes are forgotten).
// Incoming entries are first deduped by key (a filled SessionID beats an
// empty one regardless of order; otherwise the first in list order wins)
// so no writer can persist duplicate keys.
func upsertPaneRecordEntries(dir, sessionName string, entries []PaneRecordEntry) error {
	stored := loadPaneRecord(dir, sessionName)
	byKey := make(map[paneRecordKey]PaneRecordEntry, len(stored))
	for _, e := range stored {
		k := paneRecordKeyOfEntry(e)
		if _, dup := byKey[k]; !dup {
			byKey[k] = e
		}
	}
	deduped := make([]PaneRecordEntry, 0, len(entries))
	dedupPos := make(map[paneRecordKey]int, len(entries))
	for _, e := range entries {
		k := paneRecordKeyOfEntry(e)
		pos, dup := dedupPos[k]
		if !dup {
			dedupPos[k] = len(deduped)
			deduped = append(deduped, e)
			continue
		}
		if deduped[pos].SessionID == "" && e.SessionID != "" {
			deduped[pos] = e
		}
	}
	merged := make([]PaneRecordEntry, 0, len(deduped))
	for _, e := range deduped {
		if e.SessionID == "" {
			if s, ok := byKey[paneRecordKeyOfEntry(e)]; ok && s.SessionID != "" {
				e.SessionID = s.SessionID
				e.SessionProvider = s.SessionProvider
				e.SessionFile = s.SessionFile
			}
		}
		merged = append(merged, e)
	}
	return storePaneRecord(dir, sessionName, merged)
}

// writePaneRecord writes one empty-id entry per agent pane (the spawn roster).
// Merge-only: existing non-empty ids survive a re-run.
func writePaneRecord(dir, sessionName string, panes []tmux.Pane) error {
	entries := make([]PaneRecordEntry, 0, len(panes))
	for _, p := range panes {
		if !isAgentPaneForRecord(p) {
			continue
		}
		entries = append(entries, PaneRecordEntry{
			WindowIndex: p.WindowIndex,
			AgentType:   string(p.Type),
			NTMIndex:    p.NTMIndex,
		})
	}
	return upsertPaneRecordEntries(dir, sessionName, entries)
}

// WritePaneRecord writes the spawn roster for a live session. It resolves
// StorageDir plus the normalized record name and serializes the
// read-modify-write under acquireLock.
func WritePaneRecord(sessionName string, panes []tmux.Pane) error {
	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()
	return writePaneRecord(StorageDir(), sessionName, panes)
}

// appendPaneRecord adds exactly one entry for a newly added pane. A same-key
// entry (e.g. a re-added pane reusing a killed pane's NTM index) is replaced
// with a fresh empty entry: the new pane never inherits the dead pane's id
// (stale-drop at save heals any key reuse).
func appendPaneRecord(dir, sessionName string, pane tmux.Pane) error {
	if !isAgentPaneForRecord(pane) {
		return nil
	}
	stored := loadPaneRecord(dir, sessionName)
	fresh := PaneRecordEntry{
		WindowIndex: pane.WindowIndex,
		AgentType:   string(pane.Type),
		NTMIndex:    pane.NTMIndex,
	}
	merged := make([]PaneRecordEntry, 0, len(stored)+1)
	for _, e := range stored {
		if paneRecordKeyOfEntry(e) == paneRecordKeyOfPane(pane) {
			continue
		}
		merged = append(merged, e)
	}
	return storePaneRecord(dir, sessionName, append(merged, fresh))
}

// AppendPaneRecord appends one record entry for a newly added pane, under
// acquireLock so concurrent adds never lose entries.
func AppendPaneRecord(sessionName string, pane tmux.Pane) error {
	unlock, err := acquireLock()
	if err != nil {
		return err
	}
	defer unlock()
	return appendPaneRecord(StorageDir(), sessionName, pane)
}

// refreshPaneRecordAfterSave persists the reconciled record produced by
// Capture. It runs only after a successful Save, inside the save path's
// acquireLock scope; failures are swallowed (best-effort, the save stands).
// A nil refreshed (Capture did not run) leaves the record untouched.
func refreshPaneRecordAfterSave(dir, liveSessionName string, refreshed []PaneRecordEntry) {
	if refreshed == nil {
		return
	}
	_ = upsertPaneRecordEntries(dir, liveSessionName, refreshed)
}

// removePaneRecordSidecar drops the sidecar on Delete. Best-effort: a
// missing sidecar is fine.
func removePaneRecordSidecar(dir, sessionName string) {
	path, err := paneRecordPath(dir, sessionName)
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// movePaneRecordSidecar carries the sidecar alongside the state file on
// Archive/Unarchive. Best-effort: no sidecar, no move.
func movePaneRecordSidecar(srcDir, dstDir, sessionName string) {
	src, err := paneRecordPath(srcDir, sessionName)
	if err != nil {
		return
	}
	if _, err := os.Stat(src); err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Join(dstDir, paneRecordsDirName), 0700); err != nil {
		return
	}
	dst, err := paneRecordPath(dstDir, sessionName)
	if err != nil {
		return
	}
	_ = moveFile(src, dst)
}
