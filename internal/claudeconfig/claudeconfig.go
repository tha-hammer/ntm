// Package claudeconfig provides read/write access to the persistent Claude
// Code CLI configuration file (~/.claude/settings.json), plus snapshot /
// restore helpers that let a swarm invocation save the user's pre-launch
// model selection, mutate it mid-run (to steer swarm agents), and then put
// the original value back when the swarm finishes.
//
// Design goals:
//
//   - Zero data loss on crash. The snapshot file is the source of truth; if
//     the supervisor dies between Snapshot and Restore, the next invocation
//     can reconcile from disk.
//   - Zero surprise for the user. If no snapshot file exists on shutdown —
//     fresh install, corrupt state, the user explicitly cleared it — we
//     leave the current value alone rather than writing a guess.
//   - Zero config churn if the user has not set a model. Settings file is
//     only written when the model field actually needs to change; we never
//     materialize a settings.json that wasn't there before.
//
// Intended wiring (issue #110):
//
//   - Call Snapshot(...) once, before the swarm launches any agents, with a
//     state-dir-relative path such as
//     <XDG_STATE_HOME>/ntm/<swarm-id>/pre-swarm-claude-model.json.
//   - The existing mid-run `claude --model X` path keeps working unchanged
//     — Claude Code persists --model invocations into settings.json itself,
//     which is the behavior this package is intended to unwind.
//   - Call Restore(...) from every swarm-shutdown path: GracefulShutdown,
//     force-kill teardown, signal handlers. Restore is idempotent and
//     safe to call even if Snapshot never ran (it returns nil).
package claudeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/ntm/internal/git"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// SnapshotFilename is the on-disk name of the pre-swarm model snapshot
// written by Snapshot() and consumed by Restore(). Exposed as a constant so
// callers outside the package can reason about the file (e.g. pruning stale
// snapshots as part of reconciliation without re-opening them).
const SnapshotFilename = "pre-swarm-claude-model.json"

// ResolveSnapshotPath returns the canonical absolute path for the pre-swarm
// model snapshot. The path lives under $XDG_STATE_HOME/ntm/ (falling back to
// ~/.local/state/ntm/ when XDG_STATE_HOME is unset), matching the convention
// used elsewhere in NTM for persistent per-user state (see pane_identity.go).
//
// A single snapshot file is deliberately shared across swarms: there is only
// one ~/.claude/settings.json file for the user, so only one pre-swarm model
// value exists to preserve. If a second swarm starts while the first is
// still running, EnsureSnapshot leaves the existing snapshot alone (the
// first swarm's Snapshot call already captured the user's pre-any-swarm
// value; re-snapshotting now would just re-record the running swarm's
// injected value and lose the original).
func ResolveSnapshotPath() (string, error) {
	stateDir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if stateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home dir: %w", err)
		}
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "ntm", SnapshotFilename), nil
}

// EnsureSnapshot captures the current Claude Code model value into
// snapshotPath iff no snapshot file already exists. This is the intended
// pre-swarm entry point for callers: it is safe to invoke once per swarm
// launch, on every swarm launch, without risking clobbering an earlier
// swarm's captured pre-launch value.
//
// Returns (created, nil) where created=true means a fresh snapshot was
// written, and created=false means a prior snapshot was already present and
// left untouched.
//
// A non-nil error is returned only for structural failures (disk, JSON).
// Callers should treat snapshot failures as non-fatal for the swarm launch
// itself — i.e., log a warning and continue, since an unsnapshotted swarm
// still works and only degrades the post-run restore behavior.
func EnsureSnapshot(settingsPath, snapshotPath string) (bool, error) {
	if _, err := os.Stat(snapshotPath); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("stat snapshot: %w", err)
	}
	if err := Snapshot(settingsPath, snapshotPath); err != nil {
		return false, err
	}
	return true, nil
}

// SettingsFilename is the Claude Code CLI's settings file inside the
// CLAUDE_CONFIG_DIR (or ~/.claude by default). Public so tests can override.
const SettingsFilename = "settings.json"

// ModelKey is the JSON field Claude Code uses to persist the user's model
// selection. Public so tests can override if Claude Code ever renames it.
const ModelKey = "model"

// ResolveClaudeSettingsPath returns the absolute path of the Claude Code
// settings file, honoring CLAUDE_CONFIG_DIR in the same way the upstream
// CLI does (see coding_agent_usage_tracker#6 for the upstream-CLI contract).
// Returns the path and whether it was resolved from the env var override.
func ResolveClaudeSettingsPath() (string, bool, error) {
	if trimmed := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); trimmed != "" {
		return filepath.Join(trimmed, SettingsFilename), true, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".claude", SettingsFilename), false, nil
}

// ReadModel returns the currently-persisted Claude Code model value.
//   - (value, true, nil)  — settings.json exists and has a non-empty string `model` field
//   - ("",    false, nil) — settings.json absent, unreadable, or missing `model` field
//   - ("",    false, err) — structural error the caller should surface (JSON syntax, IO error)
//
// The distinction between "no model set" and "file absent" collapses on
// purpose: both cases tell Snapshot that there is nothing to stash, and
// both tell Restore that the user never had a persistent selection so
// writing one now would be a novel change rather than a restoration.
func ReadModel(settingsPath string) (string, bool, error) {
	// #nosec G304 -- caller-supplied path; this is a library function and
	// trusts the caller to pass a settings-file path they chose (typically
	// via ResolveClaudeSettingsPath).
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", settingsPath, err)
	}
	if len(raw) == 0 {
		return "", false, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false, fmt.Errorf("parse %s: %w", settingsPath, err)
	}
	v, ok := parsed[ModelKey]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false, nil
	}
	return s, true, nil
}

// WriteModel updates the `model` field in settings.json, preserving every
// other field verbatim. Creates the settings file (and its parent dir) if
// missing. Writes are atomic: marshalled JSON is written to a tmpfile in
// the same directory and renamed into place, so a crash mid-write cannot
// leave a truncated settings.json behind.
//
// If `model` is the empty string, the field is removed from settings.json
// rather than written as "". If that leaves the settings object empty and
// the file did not exist to begin with, the empty file is not created.
func WriteModel(settingsPath, model string) error {
	// #nosec G304 -- caller-supplied path (library function).
	raw, err := os.ReadFile(settingsPath)
	existed := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}

	parsed := map[string]any{}
	if existed && len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("parse %s: %w", settingsPath, err)
		}
		// JSON `null` unmarshals into a map[string]any by setting the map
		// to nil (not an error). A later `parsed[key] = value` on a nil
		// map panics with "assignment to entry in nil map", so reset to
		// an empty non-nil map and treat `null` as "no pre-existing
		// object to merge with" — the subsequent write will replace the
		// `null` content with either `{}` (for model == "") or
		// `{"model": "..."}`.
		if parsed == nil {
			parsed = map[string]any{}
		}
	}

	if model == "" {
		delete(parsed, ModelKey)
	} else {
		parsed[ModelKey] = model
	}

	// Do not materialize a brand-new settings.json just to hold an empty
	// object — leaves the Claude Code config in exactly the shape the user
	// had before.
	if !existed && len(parsed) == 0 {
		return nil
	}

	return writeSettingsAtomic(settingsPath, parsed)
}

// writeSettingsAtomic writes a settings object through a temp file in the same
// folder and a rename, creating the folder if needed, so a crash mid-write
// can't leave a truncated settings file behind.
func writeSettingsAtomic(settingsPath string, parsed map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("ensure parent dir: %w", err)
	}
	encoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(settingsPath), ".settings-tmp-*")
	if err != nil {
		return fmt.Errorf("create tmpfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write tmpfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync tmpfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmpfile: %w", err)
	}
	if err := os.Rename(tmpName, settingsPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename tmpfile to settings: %w", err)
	}
	return nil
}

const (
	// StopHookTimeoutSeconds is HookRegistration's timeout for the ntm Stop entry.
	StopHookTimeoutSeconds = 5

	// LocalSettingsPath is the per-folder Claude Code settings file, relative
	// to the agent's work folder. Claude Code reads it at startup, and it's
	// meant to stay out of git.
	LocalSettingsPath = ".claude/settings.local.json"

	// stopHookMarker follows the binary path in every ntm Stop entry.
	stopHookMarker = " agent-hook stop"
)

// ErrUnparseableSettings means a settings file was left unchanged because it
// isn't the JSON shape the writer can merge into.
var ErrUnparseableSettings = errors.New("settings file can't be parsed")

// StopHookCommand is the command Claude Code runs at turn end. Hooks don't
// get an interactive PATH, so the binary is absolute. Both paths are shell
// quoted, because Claude Code runs the command with sh -c.
func StopHookCommand(ntmPath, projectKey string) string {
	return tmux.ShellQuote(ntmPath) + stopHookMarker + " --project " + tmux.ShellQuote(projectKey)
}

// IsStopHookCommand reports whether a hook command is an ntm Stop entry,
// whatever binary path it names: one shell word, then " agent-hook stop",
// then the end or a space.
func IsStopHookCommand(command string) bool {
	rest, ok := afterFirstShellWord(command)
	if !ok || !strings.HasPrefix(rest, stopHookMarker) {
		return false
	}
	rest = strings.TrimPrefix(rest, stopHookMarker)
	return rest == "" || rest[0] == ' '
}

// afterFirstShellWord skips one word, plain or single quoted as ShellQuote
// writes it, and returns the rest of the command. ShellQuote writes a quote
// inside the word as a closing quote, a backslash-escaped quote and an
// opening quote, so a backslash skips the character after it.
func afterFirstShellWord(command string) (string, bool) {
	i := 0
	for i < len(command) && command[i] != ' ' {
		switch command[i] {
		case '\\':
			i += 2
		case '\'':
			end := strings.IndexByte(command[i+1:], '\'')
			if end < 0 {
				return "", false
			}
			i += end + 2
		default:
			i++
		}
	}
	if i > len(command) {
		return "", false
	}
	if i == 0 {
		return "", false
	}
	return command[i:], true
}

// WriteStopHook makes <workDir>/.claude/settings.local.json hold exactly one
// ntm Stop entry with this command: it removes any ntm entries, whatever
// binary they name, drops groups that removal emptied, and appends one group.
// Every other field and hook is kept. A file that isn't the expected shape is
// left unchanged and ErrUnparseableSettings is returned. It reports the path
// and whether the file was written; a run that would change nothing doesn't
// write.
func WriteStopHook(workDir, command string) (path string, changed bool, err error) {
	path = filepath.Join(workDir, filepath.FromSlash(LocalSettingsPath))
	// #nosec G304 -- the path is the agent work folder's settings file.
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return path, false, fmt.Errorf("read %s: %w", path, err)
	}
	existed := err == nil

	parsed := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return path, false, fmt.Errorf("%w: %s: %v", ErrUnparseableSettings, path, err)
		}
		if parsed == nil { // JSON null
			parsed = map[string]any{}
		}
	}

	merged, err := mergeStopHook(parsed, command)
	if err != nil {
		return path, false, fmt.Errorf("%w: %s: %v", ErrUnparseableSettings, path, err)
	}
	if existed && sameJSON(parsed, merged) {
		return path, false, nil
	}
	if err := writeSettingsAtomic(path, merged); err != nil {
		return path, false, fmt.Errorf("write %s: %w", path, err)
	}
	return path, true, nil
}

// StopHookResult is what InstallStopHook did.
type StopHookResult struct {
	SettingsPath string
	Changed      bool
	Excluded     bool // false when the folder isn't in a git repo
}

// InstallStopHook writes the ntm Stop entry into workDir's local settings and
// keeps that file out of git status. projectKey is the Agent Mail project,
// which differs from workDir in a worktree. The command holds paths only,
// never a token.
func InstallStopHook(workDir, ntmPath, projectKey string) (StopHookResult, error) {
	path, changed, err := WriteStopHook(workDir, StopHookCommand(ntmPath, projectKey))
	result := StopHookResult{SettingsPath: path, Changed: changed}
	if err != nil {
		return result, err
	}
	result.Excluded, err = git.EnsureExcluded(workDir, LocalSettingsPath)
	return result, err
}

// mergeStopHook returns a copy of settings with ntm's Stop entries replaced
// by one entry for command. It only reads settings.
func mergeStopHook(settings map[string]any, command string) (map[string]any, error) {
	hooks := map[string]any{}
	switch existing := settings["hooks"].(type) {
	case nil:
	case map[string]any:
		for event, value := range existing {
			hooks[event] = value
		}
	default:
		return nil, fmt.Errorf("hooks is %T, not an object", existing)
	}

	var groups []any
	switch existing := hooks["Stop"].(type) {
	case nil:
	case []any:
		groups = existing
	default:
		return nil, fmt.Errorf("hooks.Stop is %T, not a list", existing)
	}

	kept := make([]any, 0, len(groups)+1)
	for i, group := range groups {
		groupMap, ok := group.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hooks.Stop[%d] is %T, not an object", i, group)
		}
		list, ok := groupMap["hooks"].([]any)
		if !ok {
			return nil, fmt.Errorf("hooks.Stop[%d].hooks is %T, not a list", i, groupMap["hooks"])
		}
		remaining := make([]any, 0, len(list))
		for j, hook := range list {
			hookMap, ok := hook.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("hooks.Stop[%d].hooks[%d] is %T, not an object", i, j, hook)
			}
			if existingCommand, _ := hookMap["command"].(string); IsStopHookCommand(existingCommand) {
				continue
			}
			remaining = append(remaining, hook)
		}
		if len(remaining) == 0 && len(list) > 0 {
			continue // the group held only ntm entries
		}
		copied := make(map[string]any, len(groupMap))
		for key, value := range groupMap {
			copied[key] = value
		}
		copied["hooks"] = remaining
		kept = append(kept, copied)
	}
	kept = append(kept, map[string]any{"hooks": []any{map[string]any{
		"type":    "command",
		"command": command,
		"timeout": StopHookTimeoutSeconds,
	}}})
	hooks["Stop"] = kept

	merged := make(map[string]any, len(settings)+1)
	for key, value := range settings {
		merged[key] = value
	}
	merged["hooks"] = hooks
	return merged, nil
}

// sameJSON reports whether two settings objects encode to the same JSON.
func sameJSON(a, b map[string]any) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(encodedA) == string(encodedB)
}

// Snapshot captures the current Claude Code model value into snapshotPath.
// It always writes snapshotPath (even when no model is set) so Restore can
// distinguish "user had no model set" from "snapshot never ran."
//
// The snapshot also records whether settings.json existed at all pre-swarm.
// That distinguishes three pre-swarm user states:
//
//   - No settings.json file at all         → SettingsFileExisted=false, HadModelField=false
//   - settings.json exists, no `model`     → SettingsFileExisted=true,  HadModelField=false
//   - settings.json exists, `model` set    → SettingsFileExisted=true,  HadModelField=true
//
// Restore needs the distinction so it can choose between "remove the
// settings.json the swarm caused to materialize" and "leave settings.json
// alone, just delete the model field."
//
// The snapshot JSON is stable and small; multiple swarms can read/write
// their own snapshots concurrently as long as they use distinct paths.
func Snapshot(settingsPath, snapshotPath string) error {
	model, hasModel, err := ReadModel(settingsPath)
	if err != nil {
		return fmt.Errorf("read pre-launch model: %w", err)
	}
	_, statErr := os.Stat(settingsPath)
	settingsFileExisted := statErr == nil
	snap := snapshotFile{
		Version:             snapshotFormatVersion,
		SettingsPath:        settingsPath,
		Model:               model,
		HadModelField:       hasModel,
		SettingsFileExisted: settingsFileExisted,
	}
	encoded, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
		return fmt.Errorf("ensure snapshot dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(snapshotPath), ".snap-tmp-*")
	if err != nil {
		return fmt.Errorf("create snapshot tmpfile: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write snapshot tmpfile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync snapshot tmpfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close snapshot tmpfile: %w", err)
	}
	if err := os.Rename(tmpName, snapshotPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename snapshot tmpfile: %w", err)
	}
	return nil
}

// Restore writes the pre-launch Claude Code model value back into settings.json
// and then deletes the snapshot file. Returns nil if snapshotPath is absent
// (Snapshot was never run for this swarm, or Restore has already run).
//
// Restore is idempotent: calling it twice after a successful restore is a
// no-op rather than an error. This makes it safe to wire into every
// shutdown code path (graceful, forced, signal) without coordination.
func Restore(snapshotPath string) error {
	// #nosec G304 -- caller-supplied snapshot path (library function).
	raw, err := os.ReadFile(snapshotPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read snapshot: %w", err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(raw, &snap); err != nil {
		return fmt.Errorf("parse snapshot: %w", err)
	}
	if snap.SettingsPath == "" {
		return fmt.Errorf("snapshot missing settings_path; refusing to guess")
	}

	switch {
	case snap.HadModelField:
		// User had a persisted model pre-swarm — put it back.
		if err := WriteModel(snap.SettingsPath, snap.Model); err != nil {
			return fmt.Errorf("restore model: %w", err)
		}
	default:
		// User had no persistent model selection — remove whatever the swarm wrote.
		if err := WriteModel(snap.SettingsPath, ""); err != nil {
			return fmt.Errorf("clear model: %w", err)
		}
		// If the user had NO settings.json pre-swarm but one exists now,
		// and clearing the model field reduced it to an empty object `{}`,
		// remove the file so the user's filesystem state matches pre-swarm.
		// (If the file now has other fields — Claude Code or the swarm
		// wrote them — we leave it alone rather than deleting content we
		// don't understand the provenance of.)
		if !snap.SettingsFileExisted {
			if err := removeIfEmptyObject(snap.SettingsPath); err != nil {
				return fmt.Errorf("remove empty post-restore settings: %w", err)
			}
		}
	}

	if err := os.Remove(snapshotPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove snapshot: %w", err)
	}
	return nil
}

// removeIfEmptyObject deletes `path` iff it parses as an empty JSON object
// `{}`. Any other content — parsed map has entries, non-JSON bytes, JSON
// `null` or non-object JSON, read error — is a no-op return-nil so we never
// destroy content that may matter to the user.
func removeIfEmptyObject(path string) error {
	// #nosec G304 -- caller-supplied path (library function).
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nil //nolint:nilerr // intentional: non-structural errors on this inspection path are not fatal to Restore.
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil //nolint:nilerr // content is non-JSON or structured differently — leave it alone.
	}
	// JSON `null` unmarshals into a map as nil (not an error, not an empty
	// map). Treat that as "some non-object content we don't understand" and
	// leave it alone, same as non-JSON bytes. Only an actually-empty object
	// `{}` — which json.Unmarshal represents as a non-nil zero-length map —
	// is safe to delete.
	if parsed == nil {
		return nil
	}
	if len(parsed) != 0 {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

const snapshotFormatVersion = 1

type snapshotFile struct {
	Version             int    `json:"version"`
	SettingsPath        string `json:"settings_path"`
	Model               string `json:"model"`
	HadModelField       bool   `json:"had_model_field"`
	SettingsFileExisted bool   `json:"settings_file_existed"`
}
