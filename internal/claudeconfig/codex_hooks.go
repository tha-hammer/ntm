package claudeconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Dicklesworthstone/ntm/internal/git"
)

// Codex reads the same hook shape as Claude Code from a project file, and
// keeps its approval of each hook in the user's config.toml. The formats here
// were checked against Codex 0.167.0 (evidence record in
// thoughts/searchable/shared/plans/2026-11-06-attention-feed-message-bus/07-codex-turn-end-push-tdd.md).

// CodexHooksPath is Codex's project hook file, relative to the work folder.
const CodexHooksPath = ".codex/hooks.json"

// codexDefaultHookTimeout is the timeout Codex uses for a hook with none.
const codexDefaultHookTimeout = 600

// ErrCodexHooksTracked means the project commits .codex/hooks.json, so ntm
// leaves that shared file alone.
var ErrCodexHooksTracked = errors.New("Codex hook file is tracked by git")

// CodexStopHookResult is what InstallCodexStopHook did.
type CodexStopHookResult struct {
	HooksPath string
	Changed   bool
	Excluded  bool // false when the folder isn't in a git repo
}

// InstallCodexStopHook writes the ntm Stop entry into workDir's Codex hook
// file and keeps that file out of git status. Codex reads the same hook shape
// as Claude Code, so the merge is shared with InstallStopHook. A file the
// project commits is left alone (ErrCodexHooksTracked). The command holds
// paths only, never a token.
func InstallCodexStopHook(workDir, ntmPath, projectKey string) (CodexStopHookResult, error) {
	result := CodexStopHookResult{HooksPath: filepath.Join(workDir, filepath.FromSlash(CodexHooksPath))}
	if err := refuseHomeFolder(workDir); err != nil {
		return result, err
	}
	tracked, err := git.IsTracked(workDir, CodexHooksPath)
	if err != nil {
		return result, err
	}
	if tracked {
		return result, fmt.Errorf("%w: %s is committed to git, so ntm leaves it alone. Add the ntm Stop entry yourself, or rely on the idle reminder", ErrCodexHooksTracked, result.HooksPath)
	}
	result.Changed, err = WriteStopHookFile(result.HooksPath, StopHookCommand(ntmPath, projectKey))
	if err != nil {
		return result, err
	}
	result.Excluded, err = git.EnsureExcluded(workDir, CodexHooksPath)
	return result, err
}

// InstallAndApproveCodexStopHook writes ntm's Codex Stop entry into workDir,
// then saves Codex's approval for it in codexHome, so a Codex agent launched
// there doesn't stop at Codex's "Hooks need review" screen. notice is "" when
// the hook will run, and otherwise one line telling you what to do. err is a
// failure to write the hook file; the approval's own failure is in notice.
func InstallAndApproveCodexStopHook(workDir, ntmPath, projectKey, codexHome string) (result CodexStopHookResult, notice string, err error) {
	result, err = InstallCodexStopHook(workDir, ntmPath, projectKey)
	if err != nil {
		return result, "", err
	}
	approval, approveErr := ApproveCodexStopHook(codexHome, result.HooksPath, StopHookCommand(ntmPath, projectKey))
	return result, codexHookNotice(workDir, approval, approveErr), nil
}

// codexHookNotice is what you need to know about ntm's Codex hook in folder,
// or "" when Codex will run it.
func codexHookNotice(folder string, approval CodexApproval, err error) string {
	switch {
	case approval.State == ApprovalDisabled:
		return fmt.Sprintf("ntm's Codex hook is turned off in /hooks for %s; turn it on there to get turn-end reminders.", folder)
	case approval.State != ApprovalGranted:
		reason := "its approval couldn't be saved"
		if err != nil {
			reason = err.Error()
		}
		return fmt.Sprintf("Codex hook not approved in %s: %s. Codex will show its \"Hooks need review\" screen; choose \"Trust all and continue\".", folder, reason)
	case !approval.FolderTrusted:
		return fmt.Sprintf("Codex will ask you to trust %s before ntm's turn-end hook can run there. Accept it when you attach.", folder)
	}
	return ""
}

// CodexHomeDir is Codex's config folder: $CODEX_HOME, then ~/.codex.
func CodexHomeDir() string {
	if dir := strings.TrimSpace(os.Getenv("CODEX_HOME")); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// CodexApprovalState is whether Codex will run ntm's hook.
type CodexApprovalState int

const (
	// ApprovalUnknown: ntm couldn't read or save the approval.
	ApprovalUnknown CodexApprovalState = iota
	// ApprovalNeeded: Codex will show its "Hooks need review" screen.
	ApprovalNeeded
	// ApprovalGranted: Codex's saved hash matches ntm's entry.
	ApprovalGranted
	// ApprovalDisabled: the user turned ntm's hook off in /hooks.
	ApprovalDisabled
)

func (s CodexApprovalState) String() string {
	switch s {
	case ApprovalNeeded:
		return "needs approval"
	case ApprovalGranted:
		return "granted"
	case ApprovalDisabled:
		return "turned off"
	default:
		return "unknown"
	}
}

// CodexApproval is Codex's approval of ntm's entry in one hook file.
type CodexApproval struct {
	State         CodexApprovalState
	Changed       bool // ApproveCodexStopHook wrote config.toml
	FolderTrusted bool // Codex has trusted the folder, so it reads .codex/ at all
	ConfigPath    string
	Key           string
}

// codexConfig is the part of Codex's config.toml that ntm reads.
type codexConfig struct {
	Hooks struct {
		State map[string]codexHookState `toml:"state"`
	} `toml:"hooks"`
	Projects map[string]struct {
		TrustLevel string `toml:"trust_level"`
	} `toml:"projects"`
}

type codexHookState struct {
	Enabled     *bool  `toml:"enabled"`
	TrustedHash string `toml:"trusted_hash"`
}

// codexHookKey is Codex's state key for a hook: the hook file, the event, and
// the group and handler positions.
func codexHookKey(hooksPath string, group, handler int) string {
	return fmt.Sprintf("%s:stop:%d:%d", hooksPath, group, handler)
}

// codexHookHash is Codex's trusted_hash for a plain Stop command hook: SHA-256
// of compact JSON with sorted keys, without HTML escaping, as Codex's
// hook_hash and version_for_toml build it (codex-rs/hooks/src/engine/discovery.rs,
// codex-rs/config/src/fingerprint.rs).
func codexHookHash(command string, timeout int) string {
	type handler struct { // fields in sorted key order
		Async   bool   `json:"async"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
		Type    string `json:"type"`
	}
	identity := struct {
		EventName string    `json:"event_name"`
		Hooks     []handler `json:"hooks"`
	}{"stop", []handler{{Command: command, Timeout: timeout, Type: "command"}}}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(identity) // only strings, bools and ints: can't fail
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// findCodexStopEntry finds the handler for command under Stop in hooksPath and
// returns its key and Codex's hash for it. An entry with fields ntm doesn't
// write (a matcher, a status message) is an error, because ntm can't be sure
// of Codex's hash for it.
func findCodexStopEntry(hooksPath, command string) (key, hash string, err error) {
	// #nosec G304 -- the path is an agent work folder's hook file.
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", hooksPath, err)
	}
	parsed, err := decodeSettings(raw)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s: %v", ErrUnparseableSettings, hooksPath, err)
	}
	hooks, _ := parsed["hooks"].(map[string]any)
	groups, _ := hooks["Stop"].([]any)
	for g, group := range groups {
		groupMap, _ := group.(map[string]any)
		list, _ := groupMap["hooks"].([]any)
		for h, entry := range list {
			entryMap, _ := entry.(map[string]any)
			if entryMap["command"] != command {
				continue
			}
			if len(groupMap) != 1 || !onlyKeys(entryMap, "type", "command", "timeout") || entryMap["type"] != "command" {
				return "", "", fmt.Errorf("ntm's Codex Stop entry in %s has fields ntm doesn't write, so its approval hash isn't known", hooksPath)
			}
			timeout := codexDefaultHookTimeout
			if seconds, ok := entryMap["timeout"].(float64); ok {
				timeout = max(int(seconds), 1)
			}
			return codexHookKey(hooksPath, g, h), codexHookHash(command, timeout), nil
		}
	}
	return "", "", fmt.Errorf("no ntm Stop entry for %q in %s", command, hooksPath)
}

func onlyKeys(m map[string]any, keys ...string) bool {
	allowed := 0
	for _, key := range keys {
		if _, ok := m[key]; ok {
			allowed++
		}
	}
	return allowed == len(m)
}

// CodexHookApproval reads, without changing anything, whether Codex has
// approved ntm's entry for command in hooksPath.
func CodexHookApproval(codexHome, hooksPath, command string) (CodexApproval, error) {
	approval := CodexApproval{ConfigPath: filepath.Join(codexHome, "config.toml")}
	key, hash, err := findCodexStopEntry(hooksPath, command)
	if err != nil {
		return approval, err
	}
	approval.Key = key
	// #nosec G304 -- Codex's own config file.
	raw, err := os.ReadFile(approval.ConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		approval.State = ApprovalNeeded
		return approval, nil
	}
	if err != nil {
		return approval, fmt.Errorf("read %s: %w", approval.ConfigPath, err)
	}
	var config codexConfig
	if _, err := toml.Decode(string(raw), &config); err != nil {
		return approval, fmt.Errorf("parse %s: %w", approval.ConfigPath, err)
	}
	folder := filepath.Dir(filepath.Dir(hooksPath))
	approval.FolderTrusted = config.Projects[folder].TrustLevel == "trusted"
	state, ok := config.Hooks.State[key]
	switch {
	case ok && state.Enabled != nil && !*state.Enabled:
		approval.State = ApprovalDisabled
	case ok && state.TrustedHash == hash:
		approval.State = ApprovalGranted
	default:
		approval.State = ApprovalNeeded
	}
	return approval, nil
}

// ApproveCodexStopHook saves Codex's approval of ntm's own entry for command
// in hooksPath, so Codex starts without stopping at its "Hooks need review"
// screen. It changes nothing else: other hooks keep their own review, and an
// entry you turned off stays off. config.toml is edited as text, so your
// comments and order survive, and the result is checked before it replaces
// the file. An approval already saved isn't written again.
func ApproveCodexStopHook(codexHome, hooksPath, command string) (CodexApproval, error) {
	approval, err := CodexHookApproval(codexHome, hooksPath, command)
	if err != nil || approval.State != ApprovalNeeded {
		return approval, err
	}
	_, hash, err := findCodexStopEntry(hooksPath, command)
	if err != nil {
		approval.State = ApprovalUnknown
		return approval, err
	}

	// #nosec G304 -- Codex's own config file.
	raw, err := os.ReadFile(approval.ConfigPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		approval.State = ApprovalUnknown
		return approval, fmt.Errorf("read %s: %w", approval.ConfigPath, err)
	}
	mode := fs.FileMode(0o600)
	if info, statErr := os.Stat(approval.ConfigPath); statErr == nil {
		mode = info.Mode().Perm()
	}

	updated, err := setCodexTrustedHash(string(raw), approval.Key, hash)
	if err == nil {
		err = checkOnlyKeyChanged(string(raw), updated, approval.Key, hash)
	}
	if err == nil {
		err = writeFileAtomic(approval.ConfigPath, []byte(updated), mode)
	}
	if err != nil {
		approval.State = ApprovalUnknown
		return approval, fmt.Errorf("save Codex approval in %s: %w", approval.ConfigPath, err)
	}
	approval.State = ApprovalGranted
	approval.Changed = true
	return approval, nil
}

// tomlBasicString quotes s as a TOML basic string, as Codex writes its keys.
func tomlBasicString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// setCodexTrustedHash sets trusted_hash in key's [hooks.state."<key>"] table:
// it replaces the line, or adds it under the table's header, or appends the
// table at the end of the file.
func setCodexTrustedHash(raw, key, hash string) (string, error) {
	header := "[hooks.state." + tomlBasicString(key) + "]"
	line := "trusted_hash = " + tomlBasicString(hash)
	lines := strings.SplitAfter(raw, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != header {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "[") {
				end = j
				break
			}
		}
		for j := i + 1; j < end; j++ {
			field := strings.TrimSpace(lines[j])
			if name, _, ok := strings.Cut(field, "="); ok && strings.TrimSpace(name) == "trusted_hash" {
				lines[j] = line + "\n"
				return strings.Join(lines, ""), nil
			}
		}
		if !strings.HasSuffix(lines[i], "\n") {
			lines[i] += "\n"
		}
		lines = append(lines[:i+1], append([]string{line + "\n"}, lines[i+1:]...)...)
		return strings.Join(lines, ""), nil
	}

	var config codexConfig
	if _, err := toml.Decode(raw, &config); err == nil {
		if _, exists := config.Hooks.State[key]; exists {
			return "", fmt.Errorf("ntm's hook state is in a form ntm doesn't edit")
		}
	}
	var b strings.Builder
	b.WriteString(raw)
	if raw != "" {
		if !strings.HasSuffix(raw, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(header + "\n" + line + "\n")
	return b.String(), nil
}

// checkOnlyKeyChanged decodes both texts and confirms that the only difference
// is key's state, now holding hash.
func checkOnlyKeyChanged(before, after, key, hash string) error {
	var newConfig codexConfig
	if _, err := toml.Decode(after, &newConfig); err != nil {
		return fmt.Errorf("edited config doesn't parse: %w", err)
	}
	if newConfig.Hooks.State[key].TrustedHash != hash {
		return fmt.Errorf("edited config doesn't hold the approval")
	}
	var oldTree, newTree map[string]any
	if _, err := toml.Decode(before, &oldTree); err != nil {
		return fmt.Errorf("config doesn't parse: %w", err)
	}
	if _, err := toml.Decode(after, &newTree); err != nil {
		return fmt.Errorf("edited config doesn't parse: %w", err)
	}
	dropHookState(oldTree, key)
	dropHookState(newTree, key)
	if !reflect.DeepEqual(oldTree, newTree) {
		return fmt.Errorf("edit would change more than ntm's hook state")
	}
	return nil
}

// dropHookState removes hooks.state[key], and tables that leaves empty.
func dropHookState(tree map[string]any, key string) {
	hooks, _ := tree["hooks"].(map[string]any)
	state, _ := hooks["state"].(map[string]any)
	delete(state, key)
	if hooks != nil && len(state) == 0 {
		delete(hooks, "state")
	}
	if tree != nil && hooks != nil && len(hooks) == 0 {
		delete(tree, "hooks")
	}
}

// writeFileAtomic writes data through a temp file in the same folder and a
// rename, with the given mode, creating the folder (0700) if needed.
func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ntm-tmp-*")
	if err != nil {
		return fmt.Errorf("create tmpfile: %w", err)
	}
	tmpName := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("%s: %w", step, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail("chmod tmpfile", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("write tmpfile", err)
	}
	if err := tmp.Sync(); err != nil {
		return fail("sync tmpfile", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close tmpfile: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename tmpfile: %w", err)
	}
	return nil
}
