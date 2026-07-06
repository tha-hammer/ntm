package agentsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeProvider(t *testing.T) {
	cases := map[string]string{
		"cc":              "claude",
		"claude":          "claude",
		"claude-code":     "claude",
		"CC":              "claude",
		"cod":             "codex",
		"codex":           "codex",
		"gmi":             "gemini",
		"gemini":          "gemini",
		"agy":             "antigravity",
		"antigravity":     "antigravity",
		"antigravity-cli": "antigravity",
		"AGY":             "antigravity",
		"user":            "",
		"cursor":          "",
		"":                "",
		"  cc  ":          "claude",
	}
	for in, want := range cases {
		if got := ResumeProvider(in); got != want {
			t.Errorf("ResumeProvider(%q) = %q, want %q", in, got, want)
		}
	}

	// gmi and agy are distinct providers (shared ~/.gemini parent, different
	// stores) and must never collapse into the same provider name.
	if ResumeProvider("gmi") == ResumeProvider("agy") {
		t.Errorf("gmi and agy must map to distinct providers, both = %q", ResumeProvider("gmi"))
	}
}

func TestEncodeClaudeProjectDir(t *testing.T) {
	// Claude Code encodes a cwd as `cwd.replace(/[^a-zA-Z0-9]/g, "-")` — ALL
	// non-alphanumerics (including '_' and '.') collapse to '-'. Expectations are
	// verified against the real ~/.claude/projects session dirs (e.g.
	// /data/projects/beads_rust -> -data-projects-beads-rust, which holds the
	// actual .jsonl sessions; the underscore variant is an empty stub).
	cases := map[string]string{
		"/data/projects/ntm":            "-data-projects-ntm",
		"/home/u/my.app":                "-home-u-my-app",
		"/a/b_c":                        "-a-b-c",
		"/data/projects/mcp_agent_mail": "-data-projects-mcp-agent-mail",
		"/data/projects/coding_agent_session_search": "-data-projects-coding-agent-session-search",
		"/data/projects/jeffreys-skills.md":          "-data-projects-jeffreys-skills-md",
		"/data/projects/ntm/":                        "-data-projects-ntm", // trailing slash cleaned
	}
	for in, want := range cases {
		if got := encodeClaudeProjectDir(in); got != want {
			t.Errorf("encodeClaudeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}

	// Explicit regression guard for the underscore bug (#175): an interim fix
	// wrongly PRESERVED '_', which resolved to a non-existent (or a different
	// project's) directory and resumed the wrong Claude session. Underscores must
	// collapse to '-' to match Claude Code.
	if got := encodeClaudeProjectDir("/data/projects/coding_agent_session_search"); strings.Contains(got, "_") {
		t.Errorf("encodeClaudeProjectDir must collapse underscores to '-': got %q", got)
	}
}

func TestResumeCommandNative(t *testing.T) {
	// Force the native path by making casr unavailable.
	orig := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookPath = orig }()

	cases := []struct {
		provider string
		id       string
		prefer   bool
		want     string
	}{
		{"claude", "abc-123", true, "claude --resume 'abc-123'"},
		{"codex", "r1", false, "codex resume 'r1'"},
		{"gemini", "g9", true, "gemini --resume 'g9'"},
		{"antigravity", "uuid-9", false, "agy --conversation 'uuid-9' --model 'Gemini 3.1 Pro (High)'"},
		{"antigravity", "uuid-9", true, "agy --conversation 'uuid-9' --model 'Gemini 3.1 Pro (High)'"},
		{"claude", "", true, ""},
		{"unknown", "x", true, ""},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.provider, c.id, c.prefer); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,%v) = %q, want %q", c.provider, c.id, c.prefer, got, c.want)
		}
	}
}

func TestResumeCommandCASR(t *testing.T) {
	// Make casr "available" so the casr path is taken.
	orig := lookPath
	lookPath = func(name string) (string, error) {
		if name == "casr" {
			return "/usr/bin/casr", nil
		}
		return "", os.ErrNotExist
	}
	defer func() { lookPath = orig }()

	cases := []struct {
		provider string
		id       string
		want     string
	}{
		{"claude", "abc-123", "casr -cc 'abc-123'"},
		{"codex", "r1", "casr -cod 'r1'"},
		{"gemini", "g9", "casr -gmi 'g9'"},
		// Antigravity has no casr short-flag; even with preferCASR=true and
		// casr available it must fall through to its native agy command.
		{"antigravity", "uuid-9", "agy --conversation 'uuid-9' --model 'Gemini 3.1 Pro (High)'"},
	}
	for _, c := range cases {
		if got := ResumeCommand(c.provider, c.id, true); got != c.want {
			t.Errorf("ResumeCommand(%q,%q,casr) = %q, want %q", c.provider, c.id, got, c.want)
		}
	}

	// preferCASR=false must still use native even when casr is available.
	if got := ResumeCommand("claude", "x", false); got != "claude --resume 'x'" {
		t.Errorf("native override failed: got %q", got)
	}
}

func TestResumeLatestCommand(t *testing.T) {
	cases := map[string]string{
		"antigravity": "agy --continue --model 'Gemini 3.1 Pro (High)'",
		"agy":         "", // ResumeLatestCommand takes a provider name, not an agent type alias
		"gemini":      "", // no id-less native resume for the Gemini CLI
		"claude":      "",
		"codex":       "",
		"":            "",
	}
	for in, want := range cases {
		if got := ResumeLatestCommand(in); got != want {
			t.Errorf("ResumeLatestCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"abc":      "'abc'",
		"":         "''",
		"a'b":      `'a'\''b'`,
		"uuid-123": "'uuid-123'",
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverClaude(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/demo"
	projDir := filepath.Join(home, ".claude", "projects", encodeClaudeProjectDir(workDir))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write two session files; the newer one should win.
	older := filepath.Join(projDir, "old-session.jsonl")
	newer := filepath.Join(projDir, "new-session.jsonl")
	if err := os.WriteFile(older, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte(`{"type":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make newer file genuinely newer.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("cc", workDir)
	if info == nil {
		t.Fatal("expected to discover a claude session, got nil")
	}
	if info.SessionID != "new-session" {
		t.Errorf("SessionID = %q, want new-session", info.SessionID)
	}
	if info.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", info.Provider)
	}
	if info.SourcePath != newer {
		t.Errorf("SourcePath = %q, want %q", info.SourcePath, newer)
	}
}

func TestDiscoverClaudeNoSession(t *testing.T) {
	home := t.TempDir()
	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	if info := Discover("cc", "/no/such/project"); info != nil {
		t.Errorf("expected nil for missing project, got %+v", info)
	}
}

func TestDiscoverNonResumableAgent(t *testing.T) {
	if info := Discover("user", "/data/projects/demo"); info != nil {
		t.Errorf("expected nil for user pane, got %+v", info)
	}
	if info := Discover("cursor", "/data/projects/demo"); info != nil {
		t.Errorf("expected nil for cursor pane, got %+v", info)
	}
}

func TestDiscoverCodex(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/codexdemo"
	dayDir := filepath.Join(home, ".codex", "sessions", "2026", "06", "07")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	roll := filepath.Join(dayDir, "rollout-7c3a.jsonl")
	content := `{"type":"session_meta","cwd":"` + workDir + `"}` + "\n"
	if err := os.WriteFile(roll, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("cod", workDir)
	if info == nil {
		t.Fatal("expected to discover a codex session, got nil")
	}
	if info.SessionID != "7c3a" {
		t.Errorf("SessionID = %q, want 7c3a", info.SessionID)
	}
	if info.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", info.Provider)
	}

	// A rollout for a different cwd must not match.
	if info := Discover("cod", "/some/other/dir"); info != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", info)
	}
}

func TestDiscoverGemini(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/gemdemo"
	chatsDir := filepath.Join(home, ".gemini", "tmp", "abcdef", "chats")
	if err := os.MkdirAll(chatsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sess := filepath.Join(chatsDir, "session-42.json")
	content := `{"workspace":"` + workDir + `"}`
	if err := os.WriteFile(sess, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("gmi", workDir)
	if info == nil {
		t.Fatal("expected to discover a gemini session, got nil")
	}
	if info.SessionID != "42" {
		t.Errorf("SessionID = %q, want 42", info.SessionID)
	}
}

// writeAgyConversation creates a synthetic agy conversation database. Real agy
// DBs are stock SQLite, but discovery only relies on the cwd appearing as a
// substring in the file (the cheap cwd-affinity check), so a payload that embeds
// the cwd faithfully exercises the discovery path without a sqlite dependency.
func writeAgyConversation(t *testing.T, dir, uuid, cwd string, modAge time.Duration) string {
	t.Helper()
	path := filepath.Join(dir, uuid+".db")
	// Pad before the cwd so it lands past a naive small-prefix scan, matching
	// real DBs where the path lives ~16KB in.
	payload := append([]byte("SQLite format 3\x00"), make([]byte, 20*1024)...)
	payload = append(payload, []byte("...cwd="+cwd+"...")...)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if modAge != 0 {
		ts := time.Now().Add(-modAge)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestDiscoverAntigravity(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/agydemo"
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Two conversations for the SAME cwd; the newer one (by mtime) must win.
	writeAgyConversation(t, convDir, "11111111-1111-1111-1111-111111111111", workDir, time.Hour)
	newer := writeAgyConversation(t, convDir, "22222222-2222-2222-2222-222222222222", workDir, 0)
	// A conversation for a DIFFERENT cwd must be ignored even though it's newest.
	writeAgyConversation(t, convDir, "33333333-3333-3333-3333-333333333333", "/some/other/dir", -time.Hour)

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	info := Discover("agy", workDir)
	if info == nil {
		t.Fatal("expected to discover an agy session, got nil")
	}
	if info.SessionID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("SessionID = %q, want the newer uuid", info.SessionID)
	}
	if info.Provider != "antigravity" {
		t.Errorf("Provider = %q, want antigravity", info.Provider)
	}
	if info.AgentType != "agy" {
		t.Errorf("AgentType = %q, want agy", info.AgentType)
	}
	if info.SourcePath != newer {
		t.Errorf("SourcePath = %q, want %q", info.SourcePath, newer)
	}

	// A cwd with no matching conversation yields no session.
	if info := Discover("agy", "/no/such/agy/project"); info != nil {
		t.Errorf("expected nil for non-matching cwd, got %+v", info)
	}
}

// TestDiscoverAgyGmiDisambiguation proves the two providers that share the
// ~/.gemini parent never leak into each other: a gmi session under
// ~/.gemini/tmp/<hash>/chats is NOT reported as agy, and an agy conversation
// under ~/.gemini/antigravity-cli/conversations is NOT reported as gemini.
func TestDiscoverAgyGmiDisambiguation(t *testing.T) {
	home := t.TempDir()
	workDir := "/data/projects/sharedcwd"

	// Lay down a gmi (legacy Gemini CLI) session.
	gmiChats := filepath.Join(home, ".gemini", "tmp", "deadbeef", "chats")
	if err := os.MkdirAll(gmiChats, 0o755); err != nil {
		t.Fatal(err)
	}
	gmiSess := filepath.Join(gmiChats, "session-77.json")
	if err := os.WriteFile(gmiSess, []byte(`{"workspace":"`+workDir+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Lay down an agy conversation for the SAME cwd.
	convDir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(convDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAgyConversation(t, convDir, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", workDir, 0)

	orig := homeDir
	homeDir = func() (string, error) { return home, nil }
	defer func() { homeDir = orig }()

	// gmi discovery must find ONLY the legacy session, never the agy uuid.
	gmiInfo := Discover("gmi", workDir)
	if gmiInfo == nil {
		t.Fatal("expected gmi to discover its legacy session, got nil")
	}
	if gmiInfo.Provider != "gemini" || gmiInfo.SessionID != "77" {
		t.Errorf("gmi discovery leaked: got provider=%q id=%q, want gemini/77", gmiInfo.Provider, gmiInfo.SessionID)
	}

	// agy discovery must find ONLY the conversation db, never the gmi session.
	agyInfo := Discover("agy", workDir)
	if agyInfo == nil {
		t.Fatal("expected agy to discover its conversation, got nil")
	}
	if agyInfo.Provider != "antigravity" || agyInfo.SessionID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Errorf("agy discovery leaked: got provider=%q id=%q, want antigravity/uuid", agyInfo.Provider, agyInfo.SessionID)
	}

	// Cross-check: removing the agy store entirely must NOT make gmi vanish,
	// and an agy lookup must never pick up the gmi tmp session.
	gmiOnlyHome := t.TempDir()
	gmiOnlyChats := filepath.Join(gmiOnlyHome, ".gemini", "tmp", "cafef00d", "chats")
	if err := os.MkdirAll(gmiOnlyChats, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gmiOnlyChats, "session-99.json"),
		[]byte(`{"workspace":"`+workDir+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir = func() (string, error) { return gmiOnlyHome, nil }
	if info := Discover("agy", workDir); info != nil {
		t.Errorf("agy must not discover a gmi tmp session, got %+v", info)
	}
}
