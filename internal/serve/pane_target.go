package serve

import (
	"context"
	"fmt"

	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// resolvePaneTarget maps an NTM pane index to an unambiguous tmux target.
//
// tmux interprets "session:N" as a WINDOW index, but NTM places every agent as a
// PANE inside a single window. Addressing "session:paneIdx" therefore points at a
// non-existent window for panes 1..N (only pane 0 accidentally works, matching
// window 0). We resolve the index against the live pane list and return the
// pane's unique id (%N), which tmux accepts for capture/send/stream. If the pane
// list is unavailable or the index is absent, we fall back to the legacy
// "session:idx" form so behavior never regresses below the previous state.
func resolvePaneTarget(panes []tmux.Pane, sessionID string, paneIdx int) string {
	for _, p := range panes {
		if p.Index == paneIdx {
			return p.ID
		}
	}
	return fmt.Sprintf("%s:%d", sessionID, paneIdx)
}

// paneTargetForIndex resolves the tmux target for a session pane, reading the
// live pane list. It never returns an error: on lookup failure it yields the
// legacy "session:idx" target, preserving prior behavior.
func paneTargetForIndex(ctx context.Context, sessionID string, paneIdx int) string {
	panes, err := tmux.GetPanesContext(ctx, sessionID)
	if err != nil {
		return fmt.Sprintf("%s:%d", sessionID, paneIdx)
	}
	return resolvePaneTarget(panes, sessionID, paneIdx)
}
