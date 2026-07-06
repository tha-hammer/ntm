// Package agentsession discovers the per-pane agent CLI session ID for a given
// working directory and agent type. This is used by `ntm sessions save` to
// record which provider session ran in each pane so it can later be resumed via
// casr (Cross Agent Session Resumer) or the agent's native `--resume <id>`.
//
// ntm owns the tmux topology capture/restore; per-pane provider-session resume
// is intentionally delegated to casr / native --resume rather than
// reimplementing provider-specific session formats here. This package only
// needs to find the *id* of the most-recent session for a directory; casr does
// the rest.
package agentsession

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// claudeNonAlnumPattern matches every character Claude Code rewrites when it
// derives a project directory name from a cwd. Claude Code's encoder is
// `cwd.replace(/[^a-zA-Z0-9]/g, "-")` — it collapses ALL non-alphanumeric
// characters (including '_', '.', '/', spaces, '+', ':') to '-'.
var claudeNonAlnumPattern = regexp.MustCompile(`[^a-zA-Z0-9]`)

// Info describes a discovered agent CLI session for a pane.
type Info struct {
	// AgentType is the canonical ntm agent type ("cc", "cod", "gmi").
	AgentType string `json:"agent_type"`
	// SessionID is the provider session id (e.g. the Claude Code UUID).
	SessionID string `json:"session_id"`
	// Provider is the casr/native provider name ("claude", "codex", "gemini").
	Provider string `json:"provider"`
	// SourcePath is the on-disk session file the id was discovered from.
	SourcePath string `json:"source_path,omitempty"`
	// UpdatedAt is the modification time of the session file.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// ResumeProvider maps an ntm agent type to the casr/provider name used by
// `casr <provider> resume` and `casr -<flag>`. Returns "" for agent types that
// have no provider-session resume path (user panes, editor agents, etc.).
//
// Note: "gmi"/"gemini" (the retired Gemini CLI) and "agy"/"antigravity" (its
// successor, the Antigravity CLI) are DISTINCT providers with distinct on-disk
// session stores under the shared ~/.gemini parent (~/.gemini/tmp for gmi vs
// ~/.gemini/antigravity-cli for agy). They must never be conflated.
func ResumeProvider(agentType string) string {
	switch strings.ToLower(strings.TrimSpace(agentType)) {
	case "cc", "claude", "claude-code", "claudecode":
		return "claude"
	case "cod", "codex":
		return "codex"
	case "gmi", "gemini":
		return "gemini"
	case "agy", "antigravity", "antigravity-cli":
		return "antigravity"
	default:
		return ""
	}
}

// homeDir is overridable in tests.
var homeDir = os.UserHomeDir

// Discover finds the most-recently-active agent CLI session for the given
// working directory and agent type. It returns nil (no error) when no session
// can be located, since session capture must remain best-effort: a pane without
// a discoverable id is still recorded topologically, just without a resume id.
func Discover(agentType, workDir string) *Info {
	provider := ResumeProvider(agentType)
	if provider == "" || workDir == "" {
		return nil
	}
	home, err := homeDir()
	if err != nil || home == "" {
		return nil
	}

	switch provider {
	case "claude":
		return discoverClaude(home, workDir)
	case "codex":
		return discoverCodex(home, workDir)
	case "gemini":
		return discoverGemini(home, workDir)
	case "antigravity":
		return discoverAntigravity(home, workDir)
	default:
		return nil
	}
}

// encodeClaudeProjectDir reproduces Claude Code's project-directory encoding:
// the absolute cwd with path separators and dots replaced by '-'. Underscores
// are PRESERVED (Claude Code does not transform them), e.g.
//
//	/data/projects/ntm                -> -data-projects-ntm
//	/data/projects/mcp_agent_mail     -> -data-projects-mcp-agent-mail
//	/data/projects/jeffreys-skills.md -> -data-projects-jeffreys-skills-md
//
// This must match Claude Code's own encoder exactly (`replace(/[^a-zA-Z0-9]/g,
// "-")`). An earlier revision preserved '_', which made discoverClaude look in a
// non-existent (or, worse, a different project's hyphen-variant) directory and
// resume the wrong session — verified against the real `~/.claude/projects/`
// session dirs, where every underscore cwd maps to a hyphenated directory.
func encodeClaudeProjectDir(workDir string) string {
	cleaned := filepath.Clean(workDir)
	return claudeNonAlnumPattern.ReplaceAllString(cleaned, "-")
}

// discoverClaude locates the newest *.jsonl under
// ~/.claude/projects/<encoded-cwd>/ and treats the filename stem as the id.
func discoverClaude(home, workDir string) *Info {
	projDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	path, mod := newestFileWithExt(projDir, ".jsonl")
	if path == "" {
		return nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "cc",
		SessionID:  id,
		Provider:   "claude",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// discoverCodex scans ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl for the
// newest rollout whose recorded cwd matches workDir. Codex does not key its
// session store by directory, so we match on the embedded cwd line.
func discoverCodex(home, workDir string) *Info {
	root := filepath.Join(home, ".codex", "sessions")
	path, mod := newestCodexRolloutForCwd(root, filepath.Clean(workDir))
	if path == "" {
		return nil
	}
	id := codexSessionID(path)
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "cod",
		SessionID:  id,
		Provider:   "codex",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// discoverGemini scans ~/.gemini/tmp/<hash>/chats/session-*.json. Gemini hashes
// the workspace path, so we cannot reverse it cheaply; we fall back to the
// newest session whose chat file references the workDir.
func discoverGemini(home, workDir string) *Info {
	root := filepath.Join(home, ".gemini", "tmp")
	path, mod := newestGeminiSessionForCwd(root, filepath.Clean(workDir))
	if path == "" {
		return nil
	}
	base := strings.TrimSuffix(filepath.Base(path), ".json")
	id := strings.TrimPrefix(base, "session-")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "gmi",
		SessionID:  id,
		Provider:   "gemini",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// discoverAntigravity locates the newest agy conversation database under
// ~/.gemini/antigravity-cli/conversations/<uuid>.db whose embedded cwd matches
// workDir. agy stores one stock-SQLite database per conversation and records
// the working directory inside it; the <uuid> filename stem is exactly the id
// accepted by `agy --conversation <uuid>`.
//
// This path is kept strictly disjoint from discoverGemini: gmi lives in
// ~/.gemini/tmp/<hash>/chats/session-*.json, agy lives in
// ~/.gemini/antigravity-cli/conversations/<uuid>.db. The two scans never look in
// each other's directory, so a gmi session is never reported as agy and vice
// versa even though both hang off the shared ~/.gemini parent.
func discoverAntigravity(home, workDir string) *Info {
	root := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	path, mod := newestAntigravityConversationForCwd(root, filepath.Clean(workDir))
	if path == "" {
		return nil
	}
	id := strings.TrimSuffix(filepath.Base(path), ".db")
	if id == "" {
		return nil
	}
	return &Info{
		AgentType:  "agy",
		SessionID:  id,
		Provider:   "antigravity",
		SourcePath: path,
		UpdatedAt:  mod,
	}
}

// newestFileWithExt returns the path and modtime of the most-recently-modified
// file with the given extension directly inside dir.
func newestFileWithExt(dir, ext string) (string, time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", time.Time{}
	}
	var bestPath string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestMod) {
			bestPath = filepath.Join(dir, e.Name())
			bestMod = info.ModTime()
		}
	}
	return bestPath, bestMod
}

// fileMentionsCwd reports whether the first scanLimit bytes of the file contain
// the workDir string. Used as a cheap cwd-affinity check for providers that do
// not key their store by directory.
func fileMentionsCwd(path, workDir string, scanLimit int) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, scanLimit)
	n, _ := f.Read(buf)
	return n > 0 && strings.Contains(string(buf[:n]), workDir)
}

// newestCodexRolloutForCwd walks the date-sharded codex session tree and
// returns the newest rollout file referencing workDir.
func newestCodexRolloutForCwd(root, workDir string) (string, time.Time) {
	var bestPath string
	var bestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort scan
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			return nil
		}
		if !fileMentionsCwd(path, workDir, 64*1024) {
			return nil
		}
		bestPath = path
		bestMod = info.ModTime()
		return nil
	})
	return bestPath, bestMod
}

// codexSessionID derives the casr-resumable id for a codex rollout. casr accepts
// the rollout filename stem (rollout-<id>) as the lookup key; we return the
// portion after the "rollout-" prefix.
func codexSessionID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return strings.TrimPrefix(base, "rollout-")
}

// newestGeminiSessionForCwd walks ~/.gemini/tmp/<hash>/chats and returns the
// newest session-*.json referencing workDir.
func newestGeminiSessionForCwd(root, workDir string) (string, time.Time) {
	var bestPath string
	var bestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // best-effort scan
		}
		name := d.Name()
		if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "chats" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			return nil
		}
		if !fileMentionsCwd(path, workDir, 64*1024) {
			return nil
		}
		bestPath = path
		bestMod = info.ModTime()
		return nil
	})
	return bestPath, bestMod
}

// newestAntigravityConversationForCwd returns the newest *.db directly inside
// ~/.gemini/antigravity-cli/conversations whose contents reference workDir. agy
// conversation databases are stock SQLite files that embed the working directory
// as plain text; we match on that substring as a cheap cwd-affinity check
// (mirroring the codex/gemini approach) rather than opening the SQLite file. The
// embedded cwd can appear well past the first few KB, so the whole (small) file
// is scanned instead of a capped prefix.
func newestAntigravityConversationForCwd(root, workDir string) (string, time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", time.Time{}
	}
	var bestPath string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if bestPath != "" && !info.ModTime().After(bestMod) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if !fileContainsCwd(path, workDir) {
			continue
		}
		bestPath = path
		bestMod = info.ModTime()
	}
	return bestPath, bestMod
}

// fileContainsCwd reports whether the entire file contains the workDir string.
// Used for agy conversation databases, where the embedded cwd may live past any
// reasonable fixed prefix limit. agy conversation DBs are small (sub-MB), so
// reading the whole file is cheap and avoids missing a deep match.
func fileContainsCwd(path, workDir string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), workDir)
}
