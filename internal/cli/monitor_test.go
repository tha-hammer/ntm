package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
)

// TestCaptureOrphanProcessSnapshot_FiltersManifestPanesAndDeduplicates covers
// Behavior 3 of the periodic orphan-sweep TDD plan: manifest-scoped,
// generation-aware, exact-set live snapshot capture.
func TestCaptureOrphanProcessSnapshot_FiltersManifestPanesAndDeduplicates(t *testing.T) {
	t.Parallel()

	t.Run("two roots, overlap, vanished child, zero/foreign/duplicate excluded", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{
			Agents: []resilience.AgentConfig{
				{PaneID: "pane-a"},
				{PaneID: "pane-b"},
				{PaneID: "pane-zero"},
				{PaneID: "pane-dup"},
			},
		}
		panes := []tmux.Pane{
			{ID: "pane-a", PID: 100},
			{ID: "pane-b", PID: 200},
			{ID: "pane-user", PID: 300}, // foreign/user pane, not manifest-owned
			{ID: "pane-zero", PID: 0},   // unresolved PID, excluded as a root
			{ID: "pane-dup", PID: 100},  // duplicate root (same PID as pane-a)
		}

		// 101 is a descendant of BOTH root 100 and root 200 (overlapping
		// subtrees); 102 is a depth-2 descendant of 101; 103 is a child of
		// root 100 that has already exited by identity-capture time.
		childrenOf := map[int][]int{
			100: {101, 103},
			200: {101},
			101: {102},
		}
		identities := map[int]process.ProcessIdentity{
			101: {PID: 101, CreateTimeMillis: 1101},
			102: {PID: 102, CreateTimeMillis: 1102},
		}
		notRunning := map[int]bool{103: true}
		var captureCalls []int

		deps := orphanSnapshotDeps{
			childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
				return childrenOf[parentPID], nil
			},
			captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
				captureCalls = append(captureCalls, pid)
				if notRunning[pid] {
					return process.ProcessIdentity{}, process.ErrProcessNotRunning
				}
				id, ok := identities[pid]
				if !ok {
					t.Fatalf("unexpected captureIdentity call for pid %d", pid)
				}
				return id, nil
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, deps)
		if err != nil {
			t.Fatalf("captureOrphanProcessSnapshot error = %v", err)
		}
		if !snap.Valid {
			t.Fatal("snap.Valid = false, want true")
		}

		wantRoots := map[int]struct{}{100: {}, 200: {}}
		if len(snap.Roots) != len(wantRoots) {
			t.Errorf("len(Roots) = %d, want %d (roots = %v)", len(snap.Roots), len(wantRoots), snap.Roots)
		}
		for _, r := range snap.Roots {
			if _, ok := wantRoots[r]; !ok {
				t.Errorf("unexpected root %d in %v", r, snap.Roots)
			}
		}

		wantCandidates := map[process.ProcessIdentity]struct{}{
			{PID: 101, CreateTimeMillis: 1101}: {},
			{PID: 102, CreateTimeMillis: 1102}: {},
		}
		if len(snap.Candidates) != len(wantCandidates) {
			t.Errorf("len(Candidates) = %d, want %d (candidates = %v)", len(snap.Candidates), len(wantCandidates), snap.Candidates)
		}
		for id := range wantCandidates {
			if _, ok := snap.Candidates[id]; !ok {
				t.Errorf("expected candidate %+v missing from %v", id, snap.Candidates)
			}
		}
		for excludedPID := range map[int]struct{}{100: {}, 200: {}, 300: {}, 0: {}, 103: {}} {
			for id := range snap.Candidates {
				if id.PID == excludedPID {
					t.Errorf("candidate set unexpectedly contains excluded/root pid %d: %+v", excludedPID, id)
				}
			}
		}

		// 101 is reachable from two roots; identity capture (and the
		// recursive walk beneath it) must happen exactly once.
		seen101 := 0
		for _, pid := range captureCalls {
			if pid == 101 {
				seen101++
			}
		}
		if seen101 != 1 {
			t.Errorf("pid 101 identity captured %d times via overlapping roots, want exactly 1", seen101)
		}
	})

	t.Run("zero-agent manifest is a valid empty snapshot", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{}
		deps := orphanSnapshotDeps{
			childPIDs: func(context.Context, int, int) ([]int, error) {
				t.Fatal("childPIDs should not be called when there are no roots")
				return nil, nil
			},
			captureIdentity: func(context.Context, int) (process.ProcessIdentity, error) {
				t.Fatal("captureIdentity should not be called when there are no roots")
				return process.ProcessIdentity{}, nil
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), manifest, nil, deps)
		if err != nil {
			t.Fatalf("captureOrphanProcessSnapshot error = %v", err)
		}
		if !snap.Valid {
			t.Error("snap.Valid = false, want true (a valid empty snapshot differs from no-snapshot-yet)")
		}
		if len(snap.Roots) != 0 || len(snap.Candidates) != 0 {
			t.Errorf("expected empty roots/candidates, got roots=%v candidates=%v", snap.Roots, snap.Candidates)
		}
	})

	t.Run("enumeration error rejects the whole refresh", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{PaneID: "pane-a"}}}
		panes := []tmux.Pane{{ID: "pane-a", PID: 100}}
		wantErr := errors.New("enumeration boom")
		deps := orphanSnapshotDeps{
			childPIDs: func(context.Context, int, int) ([]int, error) {
				return nil, wantErr
			},
			captureIdentity: func(context.Context, int) (process.ProcessIdentity, error) {
				t.Fatal("captureIdentity should not be reached after an enumeration error")
				return process.ProcessIdentity{}, nil
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, deps)
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want wrapping %v", err, wantErr)
		}
		if snap.Valid || len(snap.Roots) != 0 || len(snap.Candidates) != 0 || !snap.CapturedAt.IsZero() {
			t.Errorf("expected zero-value snapshot on error, got %+v", snap)
		}
	})

	t.Run("identity lookup error rejects the whole refresh", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{PaneID: "pane-a"}}}
		panes := []tmux.Pane{{ID: "pane-a", PID: 100}}
		wantErr := errors.New("identity lookup boom")
		deps := orphanSnapshotDeps{
			childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
				if parentPID == 100 {
					return []int{101}, nil
				}
				return nil, nil
			},
			captureIdentity: func(context.Context, int) (process.ProcessIdentity, error) {
				return process.ProcessIdentity{}, wantErr
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, deps)
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want wrapping %v", err, wantErr)
		}
		if snap.Valid {
			t.Errorf("expected invalid zero-value snapshot on identity error, got %+v", snap)
		}
	})
}

func TestProductionOrphanSnapshotDeps_WiresRealProcessPackage(t *testing.T) {
	t.Parallel()
	deps := productionOrphanSnapshotDeps()
	if deps.childPIDs == nil || deps.captureIdentity == nil {
		t.Fatalf("productionOrphanSnapshotDeps() returned nil dependency: %+v", deps)
	}
}
