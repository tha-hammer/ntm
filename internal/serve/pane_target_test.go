package serve

import (
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// tmux addresses "session:N" as a WINDOW index. Real NTM sessions place every
// agent as a PANE inside a single window, so "session:paneIdx" points at a
// non-existent window for panes 1..N. resolvePaneTarget must return the pane's
// unambiguous tmux id (%N) instead. See bead 6zj.
func TestResolvePaneTargetUsesPaneID(t *testing.T) {
	panes := []tmux.Pane{
		{ID: "%6", Index: 0, WindowIndex: 0},
		{ID: "%7", Index: 1, WindowIndex: 0},
		{ID: "%8", Index: 2, WindowIndex: 0},
	}

	cases := []struct {
		paneIdx int
		want    string
	}{
		{0, "%6"},
		{1, "%7"},
		{2, "%8"},
	}
	for _, c := range cases {
		if got := resolvePaneTarget(panes, "silmari-agentfield-system", c.paneIdx); got != c.want {
			t.Fatalf("resolvePaneTarget(idx=%d) = %q, want %q", c.paneIdx, got, c.want)
		}
	}
}

// When the pane list can't be read (nil/empty) or the index is absent, fall back
// to the legacy "session:idx" target so behavior never regresses below today's.
func TestResolvePaneTargetFallsBackWhenUnknown(t *testing.T) {
	if got := resolvePaneTarget(nil, "sess", 3); got != "sess:3" {
		t.Fatalf("nil panes => %q, want sess:3", got)
	}
	panes := []tmux.Pane{{ID: "%6", Index: 0, WindowIndex: 0}}
	if got := resolvePaneTarget(panes, "sess", 9); got != "sess:9" {
		t.Fatalf("absent index => %q, want sess:9", got)
	}
}
