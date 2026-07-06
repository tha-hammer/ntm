package dashboard

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/bv"
	"github.com/Dicklesworthstone/ntm/internal/status"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/internal/tracker"
	"github.com/Dicklesworthstone/ntm/internal/tui/styles"
	"github.com/Dicklesworthstone/ntm/internal/tui/theme"
)

// ---------------------------------------------------------------------------
// GetTokens
// ---------------------------------------------------------------------------

func TestGetTokens(t *testing.T) {

	tests := []struct {
		name  string
		width int
	}{
		{"narrow", 40},
		{"medium", 100},
		{"wide", 180},
		{"ultra_wide", 300},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GetTokens(tc.width)
			// Should match direct call to styles.TokensForWidth
			want := styles.TokensForWidth(tc.width)
			if got.Spacing != want.Spacing {
				t.Errorf("GetTokens(%d).Spacing = %v, want %v", tc.width, got.Spacing, want.Spacing)
			}
			if got.Size != want.Size {
				t.Errorf("GetTokens(%d).Size = %v, want %v", tc.width, got.Size, want.Size)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// renderDetailContextBar
// ---------------------------------------------------------------------------

func TestRenderDetailContextBar(t *testing.T) {
	th := theme.Current()

	tests := []struct {
		name    string
		percent float64
		width   int
		tick    int
		wantFn  func(string) bool // validation function
	}{
		{
			name:    "zero_percent",
			percent: 0, width: 20, tick: 0,
			wantFn: func(s string) bool {
				return strings.Contains(s, "░") && !strings.Contains(s, "█")
			},
		},
		{
			name:    "fifty_percent_green",
			percent: 50, width: 20, tick: 0,
			wantFn: func(s string) bool {
				return strings.Contains(s, "█") && strings.Contains(s, "░")
			},
		},
		{
			name:    "hundred_percent_full",
			percent: 100, width: 20, tick: 0,
			wantFn: func(s string) bool {
				return strings.Contains(s, "█") && !strings.Contains(s, "░")
			},
		},
		{
			name:    "over_hundred_clamped",
			percent: 150, width: 20, tick: 0,
			wantFn: func(s string) bool {
				return strings.Contains(s, "█") && !strings.Contains(s, "░")
			},
		},
		{
			name:    "negative_clamped",
			percent: -10, width: 20, tick: 0,
			wantFn: func(s string) bool {
				return strings.Contains(s, "░") && !strings.Contains(s, "█")
			},
		},
		{
			name:    "high_context_shimmer",
			percent: 85, width: 20, tick: 3,
			wantFn: func(s string) bool {
				// At >= 80%, shimmer is applied; just check output is non-empty
				return len(s) > 0 && strings.Contains(s, "[")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderDetailContextBar(tc.percent, tc.width, th, tc.tick)
			if !tc.wantFn(got) {
				t.Errorf("renderDetailContextBar(%v, %d, th, %d) = %q, did not pass validation",
					tc.percent, tc.width, tc.tick, got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RenderContextMiniBar
// ---------------------------------------------------------------------------

func TestRenderContextMiniBar_EdgeCases(t *testing.T) {
	th := theme.Current()

	tests := []struct {
		name    string
		percent float64
		width   int
		tick    int
	}{
		{"zero", 0, 20, 0},
		{"low", 30, 20, 0},
		{"warning_80", 80, 20, 1},
		{"critical_90", 90, 20, 2},
		{"over_100_clamped", 120, 20, 0},
		{"negative_clamped", -5, 20, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderContextMiniBar(tc.percent, tc.width, tc.tick, th)
			if got == "" {
				t.Error("RenderContextMiniBar returned empty string")
			}
			// Strip ANSI for content checks (Shimmer wraps each char)
			plain := status.StripANSI(got)
			// >= 90% should have "!!" suffix indicator
			if tc.percent >= 90 && tc.percent <= 100 {
				if !strings.Contains(plain, "!!") {
					t.Errorf("expected critical indicator '!!' for %.0f%%, got plain=%q", tc.percent, plain)
				}
			}
			// >= 80% and < 90% should have "!" suffix
			if tc.percent >= 80 && tc.percent < 90 {
				if !strings.Contains(plain, "!") {
					t.Errorf("expected warning indicator '!' for %.0f%%, got plain=%q", tc.percent, plain)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// statusTokenCount — count extraction (prefers parsed TokensUsed)
// ---------------------------------------------------------------------------

func TestStatusTokenCount(t *testing.T) {
	t.Run("prefers_parsed_tokens_used", func(t *testing.T) {
		st := status.AgentStatus{
			TokensUsed: 12345,
			LastOutput: strings.Repeat("noise ", 100),
		}
		if got := statusTokenCount(st); got != 12345 {
			t.Errorf("statusTokenCount() = %d, want 12345 (TokensUsed)", got)
		}
	})

	t.Run("falls_back_to_estimate_when_no_tokens_used", func(t *testing.T) {
		st := status.AgentStatus{LastOutput: strings.Repeat("hello world ", 100)}
		if got := statusTokenCount(st); got <= 0 {
			t.Errorf("statusTokenCount() = %d, want > 0 (estimate fallback)", got)
		}
	})

	t.Run("empty_output_and_no_tokens_returns_zero", func(t *testing.T) {
		if got := statusTokenCount(status.AgentStatus{}); got != 0 {
			t.Errorf("statusTokenCount() = %d, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// tokenVelocityRate — genuine delta-over-window rate (pure core)
// ---------------------------------------------------------------------------

func TestTokenVelocityRate(t *testing.T) {
	base := time.Now()

	t.Run("no_prior_sample_returns_zero", func(t *testing.T) {
		// (a) First sample / no prior → 0, never a snapshot spike.
		if got := tokenVelocityRate(velocitySample{}, false, 100_000, base); got != 0 {
			t.Errorf("tokenVelocityRate(no prior) = %v, want 0", got)
		}
	})

	t.Run("idle_same_count_returns_zero", func(t *testing.T) {
		// (b) Idle: identical count across two samples → 0, NOT a huge value.
		prev := velocitySample{tokens: 50_000, sampledAt: base}
		got := tokenVelocityRate(prev, true, 50_000, base.Add(1*time.Minute))
		if got != 0 {
			t.Errorf("tokenVelocityRate(idle) = %v, want 0", got)
		}
	})

	t.Run("genuine_growth_returns_rate", func(t *testing.T) {
		// (c) Growth: +2000 tokens over 2 minutes → ~1000 tok/min.
		prev := velocitySample{tokens: 10_000, sampledAt: base}
		got := tokenVelocityRate(prev, true, 12_000, base.Add(2*time.Minute))
		if got < 999.9 || got > 1000.1 {
			t.Errorf("tokenVelocityRate(growth) = %v, want ~1000", got)
		}
	})

	t.Run("shrinking_count_returns_zero", func(t *testing.T) {
		// (d) Shrinking snapshot (tokensNow < prevTokens) → 0, not negative/spike.
		prev := velocitySample{tokens: 80_000, sampledAt: base}
		got := tokenVelocityRate(prev, true, 30_000, base.Add(1*time.Minute))
		if got != 0 {
			t.Errorf("tokenVelocityRate(shrink) = %v, want 0", got)
		}
	})

	t.Run("zero_or_negative_window_returns_zero", func(t *testing.T) {
		prev := velocitySample{tokens: 10_000, sampledAt: base}
		// Same instant (zero window).
		if got := tokenVelocityRate(prev, true, 12_000, base); got != 0 {
			t.Errorf("tokenVelocityRate(zero window) = %v, want 0", got)
		}
		// Negative window (clock skew).
		if got := tokenVelocityRate(prev, true, 12_000, base.Add(-1*time.Minute)); got != 0 {
			t.Errorf("tokenVelocityRate(negative window) = %v, want 0", got)
		}
	})
}

// ---------------------------------------------------------------------------
// paneTokenVelocity — stateful per-pane sampling against the persistent Model
// ---------------------------------------------------------------------------

func TestPaneTokenVelocity(t *testing.T) {
	m := &Model{velocityByPaneID: make(map[string]velocitySample)}

	// First observation establishes a baseline; no prior window → 0 (no spike),
	// even though the snapshot is large and "recently active".
	first := status.AgentStatus{
		PaneID:     "%0",
		TokensUsed: 100_000,
		LastActive: time.Now(),
	}
	if got := m.paneTokenVelocity(first); got != 0 {
		t.Fatalf("paneTokenVelocity(first sample) = %v, want 0", got)
	}

	// Idle: same cumulative count on the next tick → 0, not the old ~296k spike.
	idle := first
	if got := m.paneTokenVelocity(idle); got != 0 {
		t.Errorf("paneTokenVelocity(idle, unchanged count) = %v, want 0", got)
	}

	// Backfill the prior sample to a known time so the next call has a real
	// window to rate against, then observe genuine growth.
	m.velocityByPaneID["%0"] = velocitySample{
		tokens:    100_000,
		sampledAt: time.Now().Add(-2 * time.Minute),
	}
	grown := status.AgentStatus{PaneID: "%0", TokensUsed: 104_000}
	got := m.paneTokenVelocity(grown)
	if got < 1500 || got > 2500 {
		t.Errorf("paneTokenVelocity(growth +4000/~2min) = %v, want ~2000", got)
	}

	// A pane with no ID cannot persist a window → 0.
	if got := m.paneTokenVelocity(status.AgentStatus{TokensUsed: 1_000_000}); got != 0 {
		t.Errorf("paneTokenVelocity(no pane id) = %v, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// BuildPaneTableRow
// ---------------------------------------------------------------------------

func TestBuildPaneTableRow(t *testing.T) {

	t.Run("basic_fields", func(t *testing.T) {
		pane := tmux.Pane{
			Index:   1,
			Type:    tmux.AgentClaude,
			Variant: "opus",
			Title:   "test-agent",
			Command: "claude --model opus",
		}
		ps := PaneStatus{
			State:          "working",
			ContextPercent: 55.5,
			ContextModel:   "opus-4",
		}

		row := BuildPaneTableRow(pane, ps, nil, nil)

		if row.Index != 1 {
			t.Errorf("Index = %d, want 1", row.Index)
		}
		if row.Type != string(tmux.AgentClaude) {
			t.Errorf("Type = %q, want %q", row.Type, tmux.AgentClaude)
		}
		if row.Variant != "opus" {
			t.Errorf("Variant = %q, want %q", row.Variant, "opus")
		}
		if row.ModelVariant != "opus" {
			t.Errorf("ModelVariant = %q, want %q", row.ModelVariant, "opus")
		}
		if row.Title != "test-agent" {
			t.Errorf("Title = %q, want %q", row.Title, "test-agent")
		}
		if row.Status != "working" {
			t.Errorf("Status = %q, want %q", row.Status, "working")
		}
		if row.ContextPct != 55.5 {
			t.Errorf("ContextPct = %v, want 55.5", row.ContextPct)
		}
		if row.Model != "opus-4" {
			t.Errorf("Model = %q, want %q", row.Model, "opus-4")
		}
		if row.IsCompacted {
			t.Error("IsCompacted should be false for non-compacted state")
		}
	})

	t.Run("compacted_state", func(t *testing.T) {
		pane := tmux.Pane{Index: 2, Type: tmux.AgentCodex, Title: "agent-2"}
		ps := PaneStatus{State: "compacted"}

		row := BuildPaneTableRow(pane, ps, nil, nil)

		if !row.IsCompacted {
			t.Error("IsCompacted should be true for compacted state")
		}
	})

	t.Run("model_variant_from_context", func(t *testing.T) {
		pane := tmux.Pane{Index: 3, Type: tmux.AgentGemini, Title: "agent-3"}
		ps := PaneStatus{ContextModel: "gemini-2.5"}

		row := BuildPaneTableRow(pane, ps, nil, nil)

		if row.ModelVariant != "gemini-2.5" {
			t.Errorf("ModelVariant = %q, want %q (fallback from ContextModel)", row.ModelVariant, "gemini-2.5")
		}
	})

	t.Run("with_beads", func(t *testing.T) {
		pane := tmux.Pane{Index: 4, Type: tmux.AgentClaude, Title: "agent-4"}
		ps := PaneStatus{}
		beads := []bv.BeadPreview{
			{ID: "bd-123", Title: "Fix bug"},
			{ID: "bd-456", Title: "Add feature"},
		}

		row := BuildPaneTableRow(pane, ps, beads, nil)

		if row.CurrentBead != "bd-123" {
			t.Errorf("CurrentBead = %q, want %q", row.CurrentBead, "bd-123")
		}
		if row.CurrentBeadTitle != "Fix bug" {
			t.Errorf("CurrentBeadTitle = %q, want %q", row.CurrentBeadTitle, "Fix bug")
		}
	})

	t.Run("with_file_changes", func(t *testing.T) {
		pane := tmux.Pane{
			Index: 5,
			Type:  tmux.AgentClaude,
			Title: "agent-5",
			ID:    "%5",
		}
		ps := PaneStatus{}
		changes := []tracker.RecordedFileChange{
			{Agents: []string{"agent-5"}},
			{Agents: []string{"agent-5", "other"}},
			{Agents: []string{"other"}},
		}

		row := BuildPaneTableRow(pane, ps, nil, changes)

		if row.FileChanges != 2 {
			t.Errorf("FileChanges = %d, want 2", row.FileChanges)
		}
	})

	t.Run("token_velocity_from_command", func(t *testing.T) {
		pane := tmux.Pane{
			Index:   6,
			Type:    tmux.AgentCodex,
			Title:   "agent-6",
			Command: "a significant command with many tokens that should estimate to more than zero",
		}
		ps := PaneStatus{}

		row := BuildPaneTableRow(pane, ps, nil, nil)

		if row.TokenVelocity <= 0 {
			t.Errorf("TokenVelocity = %v, want > 0 for non-empty command", row.TokenVelocity)
		}
	})

	t.Run("no_beads_returns_empty", func(t *testing.T) {
		pane := tmux.Pane{Index: 7, Type: tmux.AgentClaude, Title: "agent-7"}
		ps := PaneStatus{}

		row := BuildPaneTableRow(pane, ps, nil, nil)

		if row.CurrentBead != "" {
			t.Errorf("CurrentBead = %q, want empty", row.CurrentBead)
		}
	})
}

// ---------------------------------------------------------------------------
// RenderPaneDetail
// ---------------------------------------------------------------------------

func TestRenderPaneDetail(t *testing.T) {
	th := theme.Current()

	t.Run("basic_pane", func(t *testing.T) {
		pane := tmux.Pane{
			Index: 1,
			Type:  tmux.AgentClaude,
			Title: "my-claude-agent",
			Width: 80, Height: 24,
		}
		ps := PaneStatus{State: "working"}
		dims := CalculateLayout(140, 30)

		got := RenderPaneDetail(pane, ps, dims, th, 0)

		if !strings.Contains(got, "my-claude-agent") {
			t.Error("expected pane title in detail output")
		}
		if !strings.Contains(got, "1") {
			t.Error("expected index in detail output")
		}
		if !strings.Contains(got, "Status") {
			t.Error("expected Status section header")
		}
		if !strings.Contains(got, "working") {
			t.Error("expected working state in output")
		}
	})

	t.Run("with_variant", func(t *testing.T) {
		pane := tmux.Pane{
			Index:   2,
			Type:    tmux.AgentCodex,
			Title:   "codex-agent",
			Variant: "gpt-4o",
			Width:   80, Height: 24,
		}
		ps := PaneStatus{State: "idle"}
		dims := CalculateLayout(140, 30)

		got := RenderPaneDetail(pane, ps, dims, th, 0)

		if !strings.Contains(got, "gpt-4o") {
			t.Error("expected variant/model in detail output")
		}
		if !strings.Contains(got, "Model:") {
			t.Error("expected Model: label")
		}
	})

	t.Run("with_context_usage", func(t *testing.T) {
		pane := tmux.Pane{
			Index: 3,
			Type:  tmux.AgentGemini,
			Title: "gemini-agent",
			Width: 80, Height: 24,
		}
		ps := PaneStatus{
			State:          "working",
			ContextTokens:  50000,
			ContextLimit:   100000,
			ContextPercent: 50.0,
		}
		dims := CalculateLayout(140, 30)

		got := RenderPaneDetail(pane, ps, dims, th, 0)

		if !strings.Contains(got, "Context Usage") {
			t.Error("expected Context Usage section")
		}
		if !strings.Contains(got, "50000") {
			t.Error("expected token count in output")
		}
		if !strings.Contains(got, "100000") {
			t.Error("expected token limit in output")
		}
	})

	t.Run("with_compaction", func(t *testing.T) {
		pane := tmux.Pane{
			Index: 4, Type: tmux.AgentClaude, Title: "compact-agent",
			Width: 80, Height: 24,
		}
		now := time.Now()
		ps := PaneStatus{
			State:          "compacted",
			LastCompaction: &now,
			RecoverySent:   true,
		}
		dims := CalculateLayout(140, 30)

		got := RenderPaneDetail(pane, ps, dims, th, 0)

		if !strings.Contains(got, "compaction") {
			t.Error("expected compaction warning in output")
		}
		if !strings.Contains(got, "Recovery") {
			t.Error("expected recovery prompt indicator")
		}
	})

	t.Run("with_command", func(t *testing.T) {
		pane := tmux.Pane{
			Index: 5, Type: tmux.AgentCodex, Title: "cmd-agent",
			Width: 80, Height: 24,
			Command: "npm run build",
		}
		ps := PaneStatus{State: "working"}
		dims := CalculateLayout(140, 30)

		got := RenderPaneDetail(pane, ps, dims, th, 0)

		if !strings.Contains(got, "Command") {
			t.Error("expected Command section header")
		}
		if !strings.Contains(got, "npm run build") {
			t.Error("expected command text in output")
		}
	})

	t.Run("narrow_detail_width", func(t *testing.T) {
		pane := tmux.Pane{
			Index: 6, Type: tmux.AgentClaude, Title: "narrow-agent",
			Width: 80, Height: 24,
		}
		ps := PaneStatus{State: "idle"}
		dims := LayoutDimensions{DetailWidth: 8} // below minimum

		got := RenderPaneDetail(pane, ps, dims, th, 0)

		if got == "" {
			t.Error("expected non-empty output even with narrow width")
		}
	})
}

// ---------------------------------------------------------------------------
// RenderLayoutIndicator - additional modes
// ---------------------------------------------------------------------------

func TestRenderLayoutIndicator_AllModes(t *testing.T) {
	th := theme.Current()

	tests := []struct {
		mode LayoutMode
		want string // expected icon substring
	}{
		{LayoutMobile, "📱"},
		{LayoutCompact, "🖥"},
		{LayoutSplit, "◫"},
		{LayoutWide, "▭"},
		{LayoutUltraWide, "⬚"},
	}

	for _, tc := range tests {
		t.Run(tc.mode.String(), func(t *testing.T) {
			got := RenderLayoutIndicator(tc.mode, th)
			if !strings.Contains(got, tc.want) {
				t.Errorf("RenderLayoutIndicator(%v) = %q, missing icon %q", tc.mode, got, tc.want)
			}
			if !strings.Contains(got, tc.mode.String()) {
				t.Errorf("RenderLayoutIndicator(%v) = %q, missing mode name %q", tc.mode, got, tc.mode.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// BuildPaneTableRows (partially covered at 69.6%)
// ---------------------------------------------------------------------------

func TestBuildPaneTableRows_WithHealthStates(t *testing.T) {
	th := theme.Current()

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, Title: "agent-a", Command: "work"},
		{ID: "%2", Index: 2, Type: tmux.AgentCodex, Title: "agent-b"},
	}

	statuses := map[string]status.AgentStatus{
		"%1": {State: status.StateWorking, AgentType: "cc"},
	}

	paneStatus := map[int]PaneStatus{
		1: {State: "working", ContextPercent: 42.0, TokenVelocity: 100},
		2: {State: "idle"},
	}

	rows := BuildPaneTableRows(panes, statuses, paneStatus, nil, nil, nil, 5, th)

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	// First row should have status from AgentStatus
	if rows[0].Status != "working" {
		t.Errorf("rows[0].Status = %q, want %q", rows[0].Status, "working")
	}
	if rows[0].ModelVariant != "opus" {
		// Variant is empty, so it uses AgentType "cc" as fallback
		// Actually the Variant is "" and AgentType is "cc"
		if rows[0].ModelVariant != "cc" {
			t.Errorf("rows[0].ModelVariant = %q, want %q (from AgentType fallback)", rows[0].ModelVariant, "cc")
		}
	}
	if rows[0].Tick != 5 {
		t.Errorf("rows[0].Tick = %d, want 5", rows[0].Tick)
	}

	// Second row should fall back to PaneStatus state
	if rows[1].Status != "idle" {
		t.Errorf("rows[1].Status = %q, want %q", rows[1].Status, "idle")
	}
}

func TestBuildPaneTableRows_Empty(t *testing.T) {
	th := theme.Current()

	rows := BuildPaneTableRows(nil, nil, nil, nil, nil, nil, 0, th)
	if len(rows) != 0 {
		t.Errorf("expected 0 rows for nil panes, got %d", len(rows))
	}
}

func TestBuildPaneTableRows_FileChangesByPaneID(t *testing.T) {
	th := theme.Current()

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, Title: "agent-a"},
	}

	changes := []tracker.RecordedFileChange{
		{Agents: []string{"%1"}},
		{Agents: []string{"%1", "agent-a", "other"}},
		{Agents: []string{"agent-a"}},
	}

	rows := BuildPaneTableRows(panes, nil, nil, nil, changes, nil, 0, th)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].FileChanges != 3 {
		t.Fatalf("rows[0].FileChanges = %d, want 3", rows[0].FileChanges)
	}
}

func TestBuildPaneTableRows_ContextHistoryCopied(t *testing.T) {
	th := theme.Current()

	panes := []tmux.Pane{
		{ID: "%1", Index: 1, Type: tmux.AgentClaude, Title: "agent-a"},
	}

	sourceHistory := []float64{18, 27, 41, 56}
	paneStatus := map[int]PaneStatus{
		1: {
			State:          "working",
			ContextPercent: 56,
			ContextHistory: append([]float64(nil), sourceHistory...),
		},
	}

	rows := BuildPaneTableRows(panes, nil, paneStatus, nil, nil, nil, 0, th)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if len(rows[0].ContextHistory) != len(sourceHistory) {
		t.Fatalf("len(rows[0].ContextHistory) = %d, want %d", len(rows[0].ContextHistory), len(sourceHistory))
	}
	for i, want := range sourceHistory {
		if rows[0].ContextHistory[i] != want {
			t.Fatalf("rows[0].ContextHistory[%d] = %v, want %v", i, rows[0].ContextHistory[i], want)
		}
	}

	paneStatus[1].ContextHistory[0] = 99
	if rows[0].ContextHistory[0] == 99 {
		t.Fatal("expected row context history to be copied, not aliased")
	}
}

func TestRenderPaneRow_ContextHistoryTriggersWideSecondLine(t *testing.T) {
	th := theme.Current()

	row := PaneTableRow{
		Index:          1,
		Type:           "cc",
		Title:          "context-agent",
		Status:         "working",
		ContextPct:     72,
		ContextHistory: []float64{18, 31, 46, 59, 72},
	}
	dims := CalculateLayout(160, 30)

	rendered := status.StripANSI(RenderPaneRow(row, dims, th))
	if !strings.Contains(rendered, "\n") {
		t.Fatalf("expected wide row with context history to render a second line, got %q", rendered)
	}
	if !strings.Contains(rendered, "ctx") {
		t.Fatalf("expected wide row to include context trend label, got %q", rendered)
	}
}

// ---------------------------------------------------------------------------
// currentBeadForPane (75% covered — test nil beads edge case)
// ---------------------------------------------------------------------------

func TestCurrentBeadForPane_NilAndEmpty(t *testing.T) {

	pane := tmux.Pane{Title: "agent-1", ID: "%1"}

	// nil beads
	if got := currentBeadForPane(pane, nil); got != "" {
		t.Errorf("currentBeadForPane(nil) = %q, want empty", got)
	}

	// unavailable beads
	if got := currentBeadForPane(pane, &bv.BeadsSummary{Available: false}); got != "" {
		t.Errorf("currentBeadForPane(unavailable) = %q, want empty", got)
	}

	// available but no in-progress items
	if got := currentBeadForPane(pane, &bv.BeadsSummary{Available: true}); got != "" {
		t.Errorf("currentBeadForPane(empty list) = %q, want empty", got)
	}

	// available with matching assignee (case insensitive)
	beads := &bv.BeadsSummary{
		Available: true,
		InProgressList: []bv.BeadInProgress{
			{ID: "bd-abc", Title: "Some task", Assignee: "Agent-1"},
		},
	}
	got := currentBeadForPane(pane, beads)
	if !strings.Contains(got, "bd-abc") {
		t.Errorf("currentBeadForPane(matched) = %q, expected bd-abc", got)
	}

	// available with matching by ID
	beads2 := &bv.BeadsSummary{
		Available: true,
		InProgressList: []bv.BeadInProgress{
			{ID: "bd-xyz", Title: "By ID", Assignee: "%1"},
		},
	}
	got2 := currentBeadForPane(pane, beads2)
	if !strings.Contains(got2, "bd-xyz") {
		t.Errorf("currentBeadForPane(by ID) = %q, expected bd-xyz", got2)
	}

	// empty assignee skipped
	beads3 := &bv.BeadsSummary{
		Available: true,
		InProgressList: []bv.BeadInProgress{
			{ID: "bd-999", Title: "No Assignee", Assignee: ""},
		},
	}
	if got := currentBeadForPane(pane, beads3); got != "" {
		t.Errorf("currentBeadForPane(empty assignee) = %q, want empty", got)
	}
}
