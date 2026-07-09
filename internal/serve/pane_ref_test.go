package serve

import (
	"errors"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

func TestParsePaneRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantWindow int
		wantPane   int
		wantErr    bool
	}{
		{name: "bare index", raw: "2", wantWindow: -1, wantPane: 2},
		{name: "bare zero", raw: "0", wantWindow: -1, wantPane: 0},
		{name: "window.pane", raw: "1.2", wantWindow: 1, wantPane: 2},
		{name: "window.pane zero", raw: "0.0", wantWindow: 0, wantPane: 0},
		{name: "trims spaces", raw: "  3  ", wantWindow: -1, wantPane: 3},
		{name: "empty", raw: "", wantErr: true},
		{name: "non-numeric", raw: "abc", wantErr: true},
		{name: "negative bare", raw: "-1", wantErr: true},
		{name: "negative window", raw: "-1.0", wantErr: true},
		{name: "bad window.pane", raw: "1.x", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, p, err := parsePaneRef(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePaneRef(%q) = (%d,%d,nil); want error", tt.raw, w, p)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePaneRef(%q) unexpected error: %v", tt.raw, err)
			}
			if w != tt.wantWindow || p != tt.wantPane {
				t.Errorf("parsePaneRef(%q) = (%d,%d); want (%d,%d)", tt.raw, w, p, tt.wantWindow, tt.wantPane)
			}
		})
	}
}

func TestMatchPaneByRef(t *testing.T) {
	t.Parallel()

	// Single window: bare index is unique → resolves to the right pane-id.
	singleWindow := []tmux.Pane{
		{ID: "%0", Index: 0, WindowIndex: 0},
		{ID: "%1", Index: 1, WindowIndex: 0},
		{ID: "%2", Index: 2, WindowIndex: 0},
	}
	// Multi-window (window-per-agent): pane index collides across windows.
	multiWindow := []tmux.Pane{
		{ID: "%10", Index: 0, WindowIndex: 1},
		{ID: "%00", Index: 0, WindowIndex: 0},
		{ID: "%20", Index: 0, WindowIndex: 2},
	}

	t.Run("single window bare index resolves", func(t *testing.T) {
		p, err := matchPaneByRef(singleWindow, "1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "%1" {
			t.Errorf("got %q, want %%1", p.ID)
		}
	})

	t.Run("bare index ambiguous across windows fails loud", func(t *testing.T) {
		_, err := matchPaneByRef(multiWindow, "0")
		if err == nil {
			t.Fatal("expected ambiguity error, got nil")
		}
		var amb *ambiguousPaneError
		if !errors.As(err, &amb) {
			t.Fatalf("expected *ambiguousPaneError, got %T: %v", err, err)
		}
	})

	t.Run("window.pane disambiguates unordered list", func(t *testing.T) {
		// The list is intentionally unsorted; the sort must make resolution
		// deterministic regardless of tmux's listing order.
		p, err := matchPaneByRef(multiWindow, "2.0")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "%20" {
			t.Errorf("got %q, want %%20", p.ID)
		}
	})

	t.Run("window.pane not found", func(t *testing.T) {
		_, err := matchPaneByRef(multiWindow, "5.0")
		if err == nil {
			t.Fatal("expected not-found error")
		}
		var amb *ambiguousPaneError
		if errors.As(err, &amb) {
			t.Fatal("not-found should not be an ambiguity error")
		}
	})

	t.Run("bare index not found", func(t *testing.T) {
		_, err := matchPaneByRef(singleWindow, "9")
		if err == nil {
			t.Fatal("expected not-found error")
		}
	})
}
