package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/config"
	"github.com/Dicklesworthstone/ntm/internal/pressure"
	"github.com/Dicklesworthstone/ntm/internal/process"
	"github.com/Dicklesworthstone/ntm/internal/resilience"
	"github.com/Dicklesworthstone/ntm/internal/tmux"
	"github.com/Dicklesworthstone/ntm/tests/testutil"
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

// fakeMemorySampleStore is a deterministic pressure.MemorySampleStore for
// testing the monitor's production wiring without touching the real
// filesystem.
type fakeMemorySampleStore struct {
	appended []pressure.MemorySample
	err      error
}

func (f *fakeMemorySampleStore) Load(context.Context) ([]pressure.MemorySample, error) {
	return append([]pressure.MemorySample(nil), f.appended...), nil
}

func (f *fakeMemorySampleStore) Append(_ context.Context, sample pressure.MemorySample) error {
	if f.err != nil {
		return f.err
	}
	f.appended = append(f.appended, sample)
	return nil
}

func TestAgentScopePeakToMemorySample(t *testing.T) {
	t.Parallel()
	observedAt := time.Unix(1700000000, 0).UTC()
	peak := agentScopePeak{
		PaneID:     "pane-a",
		AgentType:  agent.AgentTypeClaudeCode,
		Identity:   process.ProcessIdentity{PID: 123, CreateTimeMillis: 456789},
		PeakBytes:  104857600,
		ObservedAt: observedAt,
	}
	got := agentScopePeakToMemorySample(peak)
	want := pressure.MemorySample{
		PaneID:     "pane-a",
		AgentType:  agent.AgentTypeClaudeCode,
		PeakBytes:  104857600,
		ObservedAt: observedAt,
		Generation: 456789,
	}
	if got != want {
		t.Errorf("agentScopePeakToMemorySample(%+v) = %+v, want %+v", peak, got, want)
	}
}

func TestProductionOnAgentGenerationEnded_PersistsToStore(t *testing.T) {
	t.Parallel()
	store := &fakeMemorySampleStore{}
	fn := productionOnAgentGenerationEnded(store)
	peak := agentScopePeak{PaneID: "pane-a", AgentType: agent.AgentTypeCodex, PeakBytes: 2048 * 1024 * 1024}
	fn(context.Background(), peak)

	if len(store.appended) != 1 || store.appended[0].PaneID != "pane-a" {
		t.Fatalf("store.appended = %+v, want one sample for pane-a", store.appended)
	}
}

func TestProductionOnAgentGenerationEnded_StoreErrorDoesNotPanic(t *testing.T) {
	t.Parallel()
	store := &fakeMemorySampleStore{err: errors.New("disk full")}
	fn := productionOnAgentGenerationEnded(store)
	fn(context.Background(), agentScopePeak{PaneID: "pane-a"}) // must not panic
}

func TestProductionFinalizeMemorySamples_PersistsEveryEntry(t *testing.T) {
	t.Parallel()
	store := &fakeMemorySampleStore{}
	fn := productionFinalizeMemorySamples(store)
	fn(context.Background(), map[string]agentScopePeak{
		"pane-a": {PaneID: "pane-a", AgentType: agent.AgentTypeClaudeCode},
		"pane-b": {PaneID: "pane-b", AgentType: agent.AgentTypeCodex},
	})
	if len(store.appended) != 2 {
		t.Fatalf("store.appended = %+v, want 2 samples", store.appended)
	}
}

func TestProductionFinalizeMemorySamples_EmptyMapIsNoOp(t *testing.T) {
	t.Parallel()
	store := &fakeMemorySampleStore{}
	fn := productionFinalizeMemorySamples(store)
	fn(context.Background(), map[string]agentScopePeak{})
	if len(store.appended) != 0 {
		t.Fatalf("store.appended = %+v, want none", store.appended)
	}
}

func TestProductionOrphanSnapshotDeps_WiresRealProcessPackage(t *testing.T) {
	t.Parallel()
	deps := productionOrphanSnapshotDeps()
	if deps.childPIDs == nil || deps.captureIdentity == nil || deps.cgroupPathForPID == nil || deps.readCgroupMemoryPeakBytes == nil {
		t.Fatalf("productionOrphanSnapshotDeps() returned nil dependency: %+v", deps)
	}
}

// TestCaptureOrphanProcessSnapshot_ScopePeaks covers Behavior 2 of the
// live-polled per-agent memory estimation TDD plan: typed per-pane
// scope-peak capture extending the existing orphan-candidate walk.
func TestCaptureOrphanProcessSnapshot_ScopePeaks(t *testing.T) {
	t.Parallel()

	baseManifest := func() *resilience.SpawnManifest {
		return &resilience.SpawnManifest{
			Agents: []resilience.AgentConfig{
				{PaneID: "pane-scoped", Type: "cc"},
				{PaneID: "pane-unscoped", Type: "cod"},
			},
		}
	}
	basePanes := []tmux.Pane{
		{ID: "pane-scoped", PID: 100},
		{ID: "pane-unscoped", PID: 200},
	}

	t.Run("scoped pane produces an entry, unscoped pane produces none", func(t *testing.T) {
		t.Parallel()

		deps := orphanSnapshotDeps{
			childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
				switch parentPID {
				case 100:
					return []int{101}, nil
				case 200:
					return []int{201}, nil
				}
				return nil, nil
			},
			captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
				return process.ProcessIdentity{PID: pid, CreateTimeMillis: int64(pid) * 10}, nil
			},
			cgroupPathForPID: func(pid int) (string, bool) {
				switch pid {
				case 100:
					return "/shell-100.scope", true
				case 101:
					return "/agent-101.scope", true // distinct from its shell -> scoped
				case 200:
					return "/shell-200.scope", true
				case 201:
					return "/shell-200.scope", true // same as its shell -> unscoped
				}
				return "", false
			},
			readCgroupMemoryPeakBytes: func(path string) (uint64, bool) {
				if path == "/agent-101.scope" {
					return 314572800, true
				}
				t.Fatalf("unexpected readCgroupMemoryPeakBytes call for path %q", path)
				return 0, false
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), baseManifest(), basePanes, deps)
		if err != nil {
			t.Fatalf("captureOrphanProcessSnapshot error = %v", err)
		}

		if len(snap.ScopePeaks) != 1 {
			t.Fatalf("len(ScopePeaks) = %d, want 1 (got %+v)", len(snap.ScopePeaks), snap.ScopePeaks)
		}
		got, ok := snap.ScopePeaks["pane-scoped"]
		if !ok {
			t.Fatalf("expected ScopePeaks entry for pane-scoped, got %+v", snap.ScopePeaks)
		}
		if got.PeakBytes != 314572800 {
			t.Errorf("PeakBytes = %d, want 314572800", got.PeakBytes)
		}
		if got.AgentType != agent.AgentTypeClaudeCode {
			t.Errorf("AgentType = %q, want %q", got.AgentType, agent.AgentTypeClaudeCode)
		}
		if got.Identity != (process.ProcessIdentity{PID: 101, CreateTimeMillis: 1010}) {
			t.Errorf("Identity = %+v, want {101 1010}", got.Identity)
		}
		if _, ok := snap.ScopePeaks["pane-unscoped"]; ok {
			t.Errorf("expected no ScopePeaks entry for pane-unscoped, got %+v", snap.ScopePeaks["pane-unscoped"])
		}
	})

	t.Run("a larger peak on a later poll updates, a smaller one does not regress it", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{PaneID: "pane-scoped", Type: "cc"}}}
		panes := []tmux.Pane{{ID: "pane-scoped", PID: 100}}

		newDeps := func(peakBytes uint64) orphanSnapshotDeps {
			return orphanSnapshotDeps{
				childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
					if parentPID == 100 {
						return []int{101}, nil
					}
					return nil, nil
				},
				captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
					return process.ProcessIdentity{PID: pid, CreateTimeMillis: 1010}, nil
				},
				cgroupPathForPID: func(pid int) (string, bool) {
					if pid == 100 {
						return "/shell.scope", true
					}
					return "/agent.scope", true
				},
				readCgroupMemoryPeakBytes: func(string) (uint64, bool) {
					return peakBytes, true
				},
			}
		}

		snap1, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, newDeps(1000))
		if err != nil {
			t.Fatalf("first capture error = %v", err)
		}
		if snap1.ScopePeaks["pane-scoped"].PeakBytes != 1000 {
			t.Fatalf("first PeakBytes = %d, want 1000", snap1.ScopePeaks["pane-scoped"].PeakBytes)
		}

		// A larger later reading for the same generation updates.
		snap2, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, newDeps(2000))
		if err != nil {
			t.Fatalf("second capture error = %v", err)
		}
		if snap2.ScopePeaks["pane-scoped"].PeakBytes != 2000 {
			t.Fatalf("second PeakBytes = %d, want 2000", snap2.ScopePeaks["pane-scoped"].PeakBytes)
		}
	})

	t.Run("identity mismatch between before/after read discards that one reading only", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{PaneID: "pane-scoped", Type: "cc"}}}
		panes := []tmux.Pane{{ID: "pane-scoped", PID: 100}}

		afterCallCount := 0
		deps := orphanSnapshotDeps{
			childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
				if parentPID == 100 {
					return []int{101}, nil
				}
				return nil, nil
			},
			captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
				afterCallCount++
				if afterCallCount == 1 {
					// The walk's own "before" identity capture.
					return process.ProcessIdentity{PID: pid, CreateTimeMillis: 1010}, nil
				}
				// captureScopePeak's "after" re-capture: PID reused by a
				// different process generation mid-read.
				return process.ProcessIdentity{PID: pid, CreateTimeMillis: 9999}, nil
			},
			cgroupPathForPID: func(pid int) (string, bool) {
				if pid == 100 {
					return "/shell.scope", true
				}
				return "/agent.scope", true
			},
			readCgroupMemoryPeakBytes: func(string) (uint64, bool) {
				return 555, true
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, deps)
		if err != nil {
			t.Fatalf("captureOrphanProcessSnapshot error = %v", err)
		}
		if _, ok := snap.ScopePeaks["pane-scoped"]; ok {
			t.Errorf("expected the mismatched reading to be discarded, got %+v", snap.ScopePeaks["pane-scoped"])
		}
		// The candidate itself (captured from the "before" identity) must
		// still be present — only the scope-peak reading is discarded.
		if _, ok := snap.Candidates[process.ProcessIdentity{PID: 101, CreateTimeMillis: 1010}]; !ok {
			t.Errorf("expected candidate 101/1010 to still be present despite the discarded scope-peak reading")
		}
	})

	t.Run("nil cgroup deps produce no ScopePeaks entries, existing behavior unaffected", func(t *testing.T) {
		t.Parallel()

		manifest := &resilience.SpawnManifest{Agents: []resilience.AgentConfig{{PaneID: "pane-scoped", Type: "cc"}}}
		panes := []tmux.Pane{{ID: "pane-scoped", PID: 100}}
		deps := orphanSnapshotDeps{
			childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
				if parentPID == 100 {
					return []int{101}, nil
				}
				return nil, nil
			},
			captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
				return process.ProcessIdentity{PID: pid, CreateTimeMillis: 1010}, nil
			},
		}

		snap, err := captureOrphanProcessSnapshot(context.Background(), manifest, panes, deps)
		if err != nil {
			t.Fatalf("captureOrphanProcessSnapshot error = %v", err)
		}
		if len(snap.ScopePeaks) != 0 {
			t.Errorf("expected no ScopePeaks entries with nil cgroup deps, got %+v", snap.ScopePeaks)
		}
		if _, ok := snap.Candidates[process.ProcessIdentity{PID: 101, CreateTimeMillis: 1010}]; !ok {
			t.Errorf("expected candidate 101/1010 to still be captured with nil cgroup deps")
		}
	})
}

// TestApplyMonitorObservation_OnAgentGenerationEnded covers Behavior 3 of
// the live-polled per-agent memory estimation TDD plan: a per-tick diff of
// ScopePeaks against the previous snapshot fires OnAgentGenerationEnded
// exactly once per pane whose scoped agent generation ended while the
// session itself stayed usable, and never fires on an unusable/errored
// tick — that is Behavior 4's confirmed-death path, not this one.
func TestApplyMonitorObservation_OnAgentGenerationEnded(t *testing.T) {
	t.Parallel()

	manifest := &resilience.SpawnManifest{
		Session: "s",
		Agents:  []resilience.AgentConfig{{PaneID: "pane-a", Type: "cc"}, {PaneID: "pane-b", Type: "cod"}},
	}
	panes := []tmux.Pane{
		{ID: "pane-a", PID: 100},
		{ID: "pane-b", PID: 200},
	}

	// Tick 1: both panes have a scoped, evidence-producing child.
	tick1Deps := orphanSnapshotDeps{
		childPIDs: func(_ context.Context, parentPID, _ int) ([]int, error) {
			switch parentPID {
			case 100:
				return []int{101}, nil
			case 200:
				return []int{201}, nil
			}
			return nil, nil
		},
		captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
			return process.ProcessIdentity{PID: pid, CreateTimeMillis: int64(pid) * 10}, nil
		},
		cgroupPathForPID: func(pid int) (string, bool) {
			switch pid {
			case 100:
				return "/shell-100.scope", true
			case 101:
				return "/agent-101.scope", true
			case 200:
				return "/shell-200.scope", true
			case 201:
				return "/agent-201.scope", true
			}
			return "", false
		},
		readCgroupMemoryPeakBytes: func(string) (uint64, bool) { return 4096, true },
	}

	var ended []agentScopePeak
	deps := monitorLoopDependencies{
		SnapshotDeps: tick1Deps,
		OnAgentGenerationEnded: func(_ context.Context, peak agentScopePeak) {
			ended = append(ended, peak)
		},
	}

	// Tick 1: from a zero-value state, nothing to diff against yet.
	state, usable := applyMonitorObservation(context.Background(), manifest, panes, nil, monitorLoopState{}, deps)
	if !usable {
		t.Fatal("tick 1: usable = false, want true")
	}
	if len(ended) != 0 {
		t.Fatalf("tick 1: OnAgentGenerationEnded fired %d times, want 0 (nothing to diff against yet): %+v", len(ended), ended)
	}
	if len(state.snapshot.ScopePeaks) != 2 {
		t.Fatalf("tick 1: len(ScopePeaks) = %d, want 2: %+v", len(state.snapshot.ScopePeaks), state.snapshot.ScopePeaks)
	}

	// Tick 2: identical evidence for both panes — present in both ticks
	// must not fire, even though the reading is freshly re-captured.
	state, usable = applyMonitorObservation(context.Background(), manifest, panes, nil, state, deps)
	if !usable {
		t.Fatal("tick 2: usable = false, want true")
	}
	if len(ended) != 0 {
		t.Fatalf("tick 2: OnAgentGenerationEnded fired %d times, want 0 (unchanged panes must not fire): %+v", len(ended), ended)
	}

	// Tick 3: pane-a's agent process is gone (no children at all); pane-b
	// is untouched. pane-a is still a manifest-owned root (its pane/PID is
	// still present) — the session itself stayed alive.
	tick3Deps := tick1Deps
	tick3Deps.childPIDs = func(_ context.Context, parentPID, _ int) ([]int, error) {
		if parentPID == 200 {
			return []int{201}, nil
		}
		return nil, nil
	}
	deps.SnapshotDeps = tick3Deps
	state, usable = applyMonitorObservation(context.Background(), manifest, panes, nil, state, deps)
	if !usable {
		t.Fatal("tick 3: usable = false, want true")
	}
	if len(ended) != 1 {
		t.Fatalf("tick 3: OnAgentGenerationEnded fired %d times, want exactly 1: %+v", len(ended), ended)
	}
	if ended[0].PaneID != "pane-a" || ended[0].PeakBytes != 4096 {
		t.Errorf("tick 3: ended[0] = %+v, want PaneID=pane-a PeakBytes=4096", ended[0])
	}
	if _, ok := state.snapshot.ScopePeaks["pane-a"]; ok {
		t.Errorf("tick 3: expected no ScopePeaks entry for pane-a after its generation ended")
	}
	if _, ok := state.snapshot.ScopePeaks["pane-b"]; !ok {
		t.Errorf("tick 3: expected pane-b's ScopePeaks entry to remain")
	}

	// Tick 4: the whole session is gone (definite-missing tmux error).
	// Even though pane-b's evidence "disappears" from state.snapshot's
	// perspective (the capture never runs at all), OnAgentGenerationEnded
	// must not fire here — that is Behavior 4's confirmed-death path.
	sessionNotFoundErr := errors.New("can't find session: s")
	beforeCount := len(ended)
	_, usable = applyMonitorObservation(context.Background(), manifest, panes, sessionNotFoundErr, state, deps)
	if usable {
		t.Fatal("tick 4: usable = true, want false (definite-missing tmux error)")
	}
	if len(ended) != beforeCount {
		t.Fatalf("tick 4: OnAgentGenerationEnded fired on a session-gone tick, want no additional calls: %+v", ended)
	}
}

// TestMonitorLoopOptions_ValidateBeforeDependencies covers Behavior 4 of the
// periodic orphan-sweep TDD plan: invalid options must fail before any
// ticker or dependency callback is touched.
func TestMonitorLoopOptions_ValidateBeforeDependencies(t *testing.T) {
	t.Parallel()

	valid := monitorLoopOptions{
		PollInterval:           time.Millisecond,
		OutputSnapshotInterval: time.Millisecond,
		MaxMisses:              1,
		ReapGrace:              time.Millisecond,
	}
	if err := valid.validate(); err != nil {
		t.Errorf("validate() on a fully valid options struct = %v, want nil", err)
	}

	cases := []struct {
		name string
		opts monitorLoopOptions
	}{
		{"zero poll interval", monitorLoopOptions{PollInterval: 0, OutputSnapshotInterval: valid.OutputSnapshotInterval, MaxMisses: valid.MaxMisses}},
		{"negative poll interval", monitorLoopOptions{PollInterval: -time.Second, OutputSnapshotInterval: valid.OutputSnapshotInterval, MaxMisses: valid.MaxMisses}},
		{"zero output interval", monitorLoopOptions{PollInterval: valid.PollInterval, OutputSnapshotInterval: 0, MaxMisses: valid.MaxMisses}},
		{"negative output interval", monitorLoopOptions{PollInterval: valid.PollInterval, OutputSnapshotInterval: -time.Second, MaxMisses: valid.MaxMisses}},
		{"zero max misses", monitorLoopOptions{PollInterval: valid.PollInterval, OutputSnapshotInterval: valid.OutputSnapshotInterval, MaxMisses: 0}},
		{"negative max misses", monitorLoopOptions{PollInterval: valid.PollInterval, OutputSnapshotInterval: valid.OutputSnapshotInterval, MaxMisses: -1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if err := tc.opts.validate(); err == nil {
				t.Fatalf("validate() error = nil for %+v, want a validation error", tc.opts)
			}

			deps := monitorLoopDependencies{
				Observe: func(context.Context, string) ([]tmux.Pane, error) {
					t.Fatal("Observe must not be called for invalid options")
					return nil, nil
				},
				CaptureOutput: func(string) {
					t.Fatal("CaptureOutput must not be called for invalid options")
				},
				SnapshotDeps: orphanSnapshotDeps{
					childPIDs: func(context.Context, int, int) ([]int, error) {
						t.Fatal("childPIDs must not be called for invalid options")
						return nil, nil
					},
					captureIdentity: func(context.Context, int) (process.ProcessIdentity, error) {
						t.Fatal("captureIdentity must not be called for invalid options")
						return process.ProcessIdentity{}, nil
					},
				},
				Ready: func(orphanProcessSnapshot) {
					t.Fatal("Ready must not be called for invalid options")
				},
				OnConfirmedDeath: func(context.Context, orphanProcessSnapshot) {
					t.Fatal("OnConfirmedDeath must not be called for invalid options")
				},
			}

			manifest := &resilience.SpawnManifest{Session: "s"}
			err := runSessionMonitorLoop(context.Background(), manifest, tc.opts, deps)
			if err == nil {
				t.Fatal("runSessionMonitorLoop error = nil, want the same validation error")
			}
		})
	}
}

// TestRunSessionMonitorLoop_LastGoodSnapshotTransitions covers Behavior 4's
// locked transition table end-to-end: a manually-driven tick sequence
// exercises every row (first/later usable capture, zero-descendant
// capture, definite-missing, ambiguous, empty/unusable pane parse, and
// pane respawn) and asserts on the final retained snapshot delivered to
// OnConfirmedDeath plus the exact identity-capture call pattern.
func TestRunSessionMonitorLoop_LastGoodSnapshotTransitions(t *testing.T) {
	t.Parallel()

	manifest := &resilience.SpawnManifest{
		Session: "test-session",
		Agents:  []resilience.AgentConfig{{PaneID: "pane-a"}},
	}

	sessionNotFoundErr := errors.New("can't find session: test-session")
	ambiguousErr := errors.New("some unexpected tmux failure")

	type tickScript struct {
		panes []tmux.Pane
		err   error
	}
	// Index 0 is consumed by the loop's pre-loop synchronous observation;
	// indices 1-8 are driven one at a time over pollCh.
	ticks := []tickScript{
		{panes: []tmux.Pane{{ID: "pane-a", PID: 100}}}, // 0: first usable live capture
		{panes: []tmux.Pane{{ID: "pane-a", PID: 100}}}, // 1: later usable live capture (new descendant)
		{panes: []tmux.Pane{{ID: "pane-a", PID: 100}}}, // 2: valid live capture, zero descendants
		{err: sessionNotFoundErr},                      // 3: definite missing
		{err: ambiguousErr},                            // 4: ambiguous tmux error (breaks the streak)
		{panes: []tmux.Pane{}},                         // 5: empty pane parse, manifest has agents
		{panes: []tmux.Pane{{ID: "pane-a", PID: 200}}}, // 6: respawn under same PaneID, new PID
		{err: sessionNotFoundErr},                      // 7: definite missing (1/2)
		{err: sessionNotFoundErr},                      // 8: definite missing (2/2) -> confirmed death
	}

	var callIdx atomic.Int32
	observeFn := func(context.Context, string) ([]tmux.Pane, error) {
		i := int(callIdx.Add(1)) - 1
		if i >= len(ticks) {
			t.Fatalf("Observe called more times (%d) than scripted (%d)", i+1, len(ticks))
		}
		return ticks[i].panes, ticks[i].err
	}

	var mu sync.Mutex
	childCallCount := map[int]int{}
	identityCallCount := map[int]int{}
	snapshotDeps := orphanSnapshotDeps{
		childPIDs: func(_ context.Context, pid, _ int) ([]int, error) {
			mu.Lock()
			defer mu.Unlock()
			childCallCount[pid]++
			switch pid {
			case 100:
				switch childCallCount[pid] {
				case 1:
					return []int{101}, nil
				case 2:
					return []int{101, 102}, nil
				default:
					return nil, nil
				}
			case 200:
				return []int{201}, nil
			}
			return nil, nil
		},
		captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
			mu.Lock()
			identityCallCount[pid]++
			mu.Unlock()
			return process.ProcessIdentity{PID: pid, CreateTimeMillis: int64(pid) * 1000}, nil
		},
	}

	readyCh := make(chan orphanProcessSnapshot, 4)
	deathCh := make(chan orphanProcessSnapshot, 1)
	pollCh := make(chan time.Time)
	outputCh := make(chan time.Time)

	deps := monitorLoopDependencies{
		Observe:       observeFn,
		CaptureOutput: func(string) {},
		SnapshotDeps:  snapshotDeps,
		Ready: func(snap orphanProcessSnapshot) {
			readyCh <- snap
		},
		OnConfirmedDeath: func(_ context.Context, snap orphanProcessSnapshot) {
			deathCh <- snap
		},
		PollTicks:   pollCh,
		OutputTicks: outputCh,
	}
	options := monitorLoopOptions{
		PollInterval:           time.Hour, // never fires on its own; driven manually via pollCh
		OutputSnapshotInterval: time.Hour,
		MaxMisses:              2,
		ReapGrace:              time.Millisecond,
	}

	const testTimeout = 2 * time.Second
	loopErrCh := make(chan error, 1)
	go func() {
		loopErrCh <- runSessionMonitorLoop(context.Background(), manifest, options, deps)
	}()

	sendTick := func() {
		t.Helper()
		select {
		case pollCh <- time.Now():
		case <-time.After(testTimeout):
			t.Fatal("loop did not consume a poll tick in time")
		}
	}

	// Tick 0 (pre-loop synchronous observation): first usable live capture.
	var readySnap orphanProcessSnapshot
	select {
	case readySnap = <-readyCh:
	case <-time.After(testTimeout):
		t.Fatal("Ready was not invoked after the first usable capture")
	}
	if readySnap.Generation != 1 {
		t.Errorf("ready snapshot Generation = %d, want 1", readySnap.Generation)
	}
	wantTick0 := map[process.ProcessIdentity]struct{}{{PID: 101, CreateTimeMillis: 101000}: {}}
	if !reflect.DeepEqual(readySnap.Candidates, wantTick0) {
		t.Errorf("ready snapshot Candidates = %v, want %v", readySnap.Candidates, wantTick0)
	}

	// Ticks 1-8, driven explicitly.
	for i := 0; i < 8; i++ {
		sendTick()
	}

	var deathSnap orphanProcessSnapshot
	select {
	case deathSnap = <-deathCh:
	case <-time.After(testTimeout):
		t.Fatal("OnConfirmedDeath was not invoked after the scripted missing streak")
	}

	select {
	case err := <-loopErrCh:
		if err != nil {
			t.Errorf("runSessionMonitorLoop returned error %v, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("runSessionMonitorLoop did not return after confirmed death")
	}

	// The retained snapshot at confirmed death must reflect tick 6 (the
	// last usable capture: respawn under the same PaneID with a new PID),
	// unaffected by the intervening missing/ambiguous/empty ticks.
	if deathSnap.Generation != 4 {
		t.Errorf("confirmed-death snapshot Generation = %d, want 4", deathSnap.Generation)
	}
	if len(deathSnap.Roots) != 1 || deathSnap.Roots[0] != 200 {
		t.Errorf("confirmed-death snapshot Roots = %v, want [200]", deathSnap.Roots)
	}
	wantDeath := map[process.ProcessIdentity]struct{}{{PID: 201, CreateTimeMillis: 201000}: {}}
	if !reflect.DeepEqual(deathSnap.Candidates, wantDeath) {
		t.Errorf("confirmed-death snapshot Candidates = %v, want %v", deathSnap.Candidates, wantDeath)
	}

	// Readiness must have fired exactly once across the whole sequence.
	select {
	case extra := <-readyCh:
		t.Errorf("Ready fired again with %+v, want exactly once", extra)
	default:
	}

	// captureIdentity must never have been reached for the missing (3),
	// ambiguous (4), or empty-pane (5) ticks — only for the three usable
	// captures (0, 1, 6), whose descendant PIDs are 101, 101+102, and 201.
	mu.Lock()
	defer mu.Unlock()
	wantIdentityCalls := map[int]int{101: 2, 102: 1, 201: 1}
	if !reflect.DeepEqual(identityCallCount, wantIdentityCalls) {
		t.Errorf("identityCallCount = %v, want %v", identityCallCount, wantIdentityCalls)
	}
}

// TestRunSessionMonitorLoop_ReadinessOnceAndDefensiveCopy covers Behavior
// 4's readiness contract: Ready fires exactly once, and the snapshot it
// receives is an independent copy — mutating it must never corrupt the
// loop's own retained state.
func TestRunSessionMonitorLoop_ReadinessOnceAndDefensiveCopy(t *testing.T) {
	t.Parallel()

	manifest := &resilience.SpawnManifest{
		Session: "test-session",
		Agents:  []resilience.AgentConfig{{PaneID: "pane-a"}},
	}
	livePanes := []tmux.Pane{{ID: "pane-a", PID: 100}}
	sessionNotFoundErr := errors.New("can't find session: test-session")

	var callIdx atomic.Int32
	observeFn := func(context.Context, string) ([]tmux.Pane, error) {
		if callIdx.Add(1) <= 2 {
			return livePanes, nil // tick 0 (pre-loop sync) and tick 1 (driven): usable live captures
		}
		return nil, sessionNotFoundErr // tick 2+: definite missing -> confirmed death at MaxMisses=1
	}

	snapshotDeps := orphanSnapshotDeps{
		childPIDs: func(context.Context, int, int) ([]int, error) { return []int{101}, nil },
		captureIdentity: func(_ context.Context, pid int) (process.ProcessIdentity, error) {
			return process.ProcessIdentity{PID: pid, CreateTimeMillis: 101000}, nil
		},
	}

	var readyMu sync.Mutex
	readyCount := 0
	var readySnap orphanProcessSnapshot
	deathCh := make(chan orphanProcessSnapshot, 1)
	pollCh := make(chan time.Time)

	deps := monitorLoopDependencies{
		Observe:       observeFn,
		CaptureOutput: func(string) {},
		SnapshotDeps:  snapshotDeps,
		Ready: func(snap orphanProcessSnapshot) {
			readyMu.Lock()
			readyCount++
			readySnap = snap
			readyMu.Unlock()
		},
		OnConfirmedDeath: func(_ context.Context, snap orphanProcessSnapshot) {
			deathCh <- snap
		},
		PollTicks:   pollCh,
		OutputTicks: make(chan time.Time),
	}
	options := monitorLoopOptions{
		PollInterval:           time.Hour,
		OutputSnapshotInterval: time.Hour,
		MaxMisses:              1,
		ReapGrace:              time.Millisecond,
	}

	const testTimeout = 2 * time.Second
	loopErrCh := make(chan error, 1)
	go func() {
		loopErrCh <- runSessionMonitorLoop(context.Background(), manifest, options, deps)
	}()

	deadline := time.Now().Add(testTimeout)
	for {
		readyMu.Lock()
		n := readyCount
		readyMu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Ready was not invoked after the first usable capture")
		}
		time.Sleep(time.Millisecond)
	}

	// Mutate the received snapshot; the loop's own retained state must be
	// unaffected because Ready receives a defensive copy.
	readyMu.Lock()
	for id := range readySnap.Candidates {
		delete(readySnap.Candidates, id)
	}
	if len(readySnap.Roots) > 0 {
		readySnap.Roots[0] = -1
	}
	readyMu.Unlock()

	// A second usable tick must not re-fire readiness.
	select {
	case pollCh <- time.Now():
	case <-time.After(testTimeout):
		t.Fatal("loop did not consume the second tick in time")
	}
	readyMu.Lock()
	n := readyCount
	readyMu.Unlock()
	if n != 1 {
		t.Errorf("readyCount = %d after a second usable tick, want still 1 (no second fire)", n)
	}

	// One more tick (definite missing) reaches MaxMisses=1 and triggers
	// confirmed death with the loop's own retained snapshot.
	select {
	case pollCh <- time.Now():
	case <-time.After(testTimeout):
		t.Fatal("loop did not consume the third tick in time")
	}

	var deathSnap orphanProcessSnapshot
	select {
	case deathSnap = <-deathCh:
	case <-time.After(testTimeout):
		t.Fatal("OnConfirmedDeath was not invoked after the definite-missing tick")
	}
	select {
	case err := <-loopErrCh:
		if err != nil {
			t.Errorf("runSessionMonitorLoop returned error %v, want nil", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("runSessionMonitorLoop did not return after confirmed death")
	}

	if len(deathSnap.Roots) != 1 || deathSnap.Roots[0] != 100 {
		t.Errorf("confirmed-death snapshot Roots = %v, want [100] (unaffected by the earlier mutation)", deathSnap.Roots)
	}
	wantCandidates := map[process.ProcessIdentity]struct{}{{PID: 101, CreateTimeMillis: 101000}: {}}
	if !reflect.DeepEqual(deathSnap.Candidates, wantCandidates) {
		t.Errorf("confirmed-death snapshot Candidates = %v, want %v (unaffected by the earlier mutation)", deathSnap.Candidates, wantCandidates)
	}
}

// TestRunSessionMonitorLoop_CancellationJoinsWithoutDeathEffects covers
// Behavior 4's cancellation contract: the loop returns promptly on context
// cancellation, never invokes OnConfirmedDeath, and never consumes a tick
// sent afterward.
func TestRunSessionMonitorLoop_CancellationJoinsWithoutDeathEffects(t *testing.T) {
	t.Parallel()

	manifest := &resilience.SpawnManifest{
		Session: "test-session",
		Agents:  []resilience.AgentConfig{{PaneID: "pane-a"}},
	}
	panes := []tmux.Pane{{ID: "pane-a", PID: 100}}

	snapshotDeps := orphanSnapshotDeps{
		childPIDs:       func(context.Context, int, int) ([]int, error) { return nil, nil },
		captureIdentity: func(context.Context, int) (process.ProcessIdentity, error) { return process.ProcessIdentity{}, nil },
	}

	var deathCalled atomic.Bool
	pollCh := make(chan time.Time)
	deps := monitorLoopDependencies{
		Observe:       func(context.Context, string) ([]tmux.Pane, error) { return panes, nil },
		CaptureOutput: func(string) {},
		SnapshotDeps:  snapshotDeps,
		Ready:         func(orphanProcessSnapshot) {},
		OnConfirmedDeath: func(context.Context, orphanProcessSnapshot) {
			deathCalled.Store(true)
		},
		PollTicks:   pollCh,
		OutputTicks: make(chan time.Time),
	}
	// MaxMisses is deliberately huge so only cancellation — never the
	// miss-streak path — can end this test's loop.
	options := monitorLoopOptions{
		PollInterval:           time.Hour,
		OutputSnapshotInterval: time.Hour,
		MaxMisses:              1_000_000,
		ReapGrace:              time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopErrCh := make(chan error, 1)
	go func() {
		loopErrCh <- runSessionMonitorLoop(ctx, manifest, options, deps)
	}()

	cancel()

	select {
	case err := <-loopErrCh:
		if err != nil {
			t.Errorf("runSessionMonitorLoop returned error %v after cancellation, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runSessionMonitorLoop did not join within the deadline after cancellation")
	}

	if deathCalled.Load() {
		t.Error("OnConfirmedDeath was invoked after cancellation, want it never called")
	}

	// Nothing is listening on pollCh anymore — a send must not be consumed.
	select {
	case pollCh <- time.Now():
		t.Error("a tick was consumed after the loop already returned from cancellation")
	case <-time.After(50 * time.Millisecond):
	}
}

// TestHandleConfirmedSessionDeath_EffectOrder covers Behavior 6 of the
// periodic orphan-sweep TDD plan: the flat, locked confirmed-death effect
// order for enabled+populated, enabled+valid-empty (which still reaps and
// logs a zero-count record — disabled is observably distinct from "the
// reaper received an empty list"), and disabled+populated.
func TestHandleConfirmedSessionDeath_EffectOrder(t *testing.T) {
	t.Parallel()

	populatedSnap := orphanProcessSnapshot{
		Valid:      true,
		Generation: 3,
		Roots:      []int{100},
		Candidates: map[process.ProcessIdentity]struct{}{{PID: 101, CreateTimeMillis: 1000}: {}},
	}
	emptySnap := orphanProcessSnapshot{
		Valid:      true,
		Generation: 5,
		Roots:      []int{100},
		Candidates: map[process.ProcessIdentity]struct{}{},
	}

	cases := []struct {
		name        string
		enabled     bool
		snap        orphanProcessSnapshot
		wantOrder   []string
		wantReapLen int // expected len(candidates) passed to Reap; reap/log skipped entirely when disabled
	}{
		{"enabled + populated", true, populatedSnap, []string{"ended", "stop", "reap", "log", "summary", "delete"}, 1},
		{"enabled + valid empty", true, emptySnap, []string{"ended", "stop", "reap", "log", "summary", "delete"}, 0},
		{"disabled + populated", false, populatedSnap, []string{"ended", "stop", "summary", "delete"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var order []string
			var reapCalled, logCalled bool
			var reapCandidates []process.ProcessIdentity
			var loggedEnabled bool
			var loggedResult orphanReapResult
			fakeResult := orphanReapResult{Captured: tc.wantReapLen}

			deps := confirmedDeathDeps{
				EmitEnded:      func(context.Context) { order = append(order, "ended") },
				StopResilience: func() { order = append(order, "stop") },
				Reap: func(_ context.Context, candidates []process.ProcessIdentity) orphanReapResult {
					order = append(order, "reap")
					reapCalled = true
					reapCandidates = candidates
					return fakeResult
				},
				LogReapResult: func(enabled bool, _ orphanProcessSnapshot, result orphanReapResult) {
					order = append(order, "log")
					logCalled = true
					loggedEnabled = enabled
					loggedResult = result
				},
				Summary: func() { order = append(order, "summary") },
				DeleteManifest: func() error {
					order = append(order, "delete")
					return nil
				},
			}

			if err := handleConfirmedSessionDeath(context.Background(), tc.enabled, tc.snap, deps); err != nil {
				t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
			}

			if !reflect.DeepEqual(order, tc.wantOrder) {
				t.Errorf("effect order = %v, want %v", order, tc.wantOrder)
			}

			if !tc.enabled {
				if reapCalled || logCalled {
					t.Errorf("reapCalled=%v logCalled=%v, want both false when disabled", reapCalled, logCalled)
				}
				return
			}
			if !reapCalled || !logCalled {
				t.Fatalf("reapCalled=%v logCalled=%v, want both true when enabled", reapCalled, logCalled)
			}
			if len(reapCandidates) != tc.wantReapLen {
				t.Errorf("len(candidates passed to Reap) = %d, want %d", len(reapCandidates), tc.wantReapLen)
			}
			if !loggedEnabled {
				t.Error("LogReapResult enabled = false, want true")
			}
			if loggedResult != fakeResult {
				t.Errorf("LogReapResult result = %+v, want %+v", loggedResult, fakeResult)
			}
		})
	}
}

// TestHandleConfirmedSessionDeath_DisabledNeverReaps covers Behavior 6:
// disabled means the reaper and its log are never invoked at all — not
// merely that they receive an empty candidate list.
func TestHandleConfirmedSessionDeath_DisabledNeverReaps(t *testing.T) {
	t.Parallel()

	snap := orphanProcessSnapshot{
		Valid:      true,
		Generation: 1,
		Roots:      []int{100},
		Candidates: map[process.ProcessIdentity]struct{}{{PID: 101, CreateTimeMillis: 1000}: {}},
	}
	var order []string
	deps := confirmedDeathDeps{
		EmitEnded:      func(context.Context) { order = append(order, "ended") },
		StopResilience: func() { order = append(order, "stop") },
		Reap: func(context.Context, []process.ProcessIdentity) orphanReapResult {
			t.Fatal("Reap must not be invoked when the policy is disabled")
			return orphanReapResult{}
		},
		LogReapResult: func(bool, orphanProcessSnapshot, orphanReapResult) {
			t.Fatal("LogReapResult must not be invoked when the policy is disabled")
		},
		Summary: func() { order = append(order, "summary") },
		DeleteManifest: func() error {
			order = append(order, "delete")
			return nil
		},
	}

	if err := handleConfirmedSessionDeath(context.Background(), false, snap, deps); err != nil {
		t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
	}
	want := []string{"ended", "stop", "summary", "delete"}
	if !reflect.DeepEqual(order, want) {
		t.Errorf("effect order = %v, want %v", order, want)
	}
}

// TestHandleConfirmedSessionDeath_SummaryFailureStillDeletesManifest covers
// Behavior 6: a panicking Summary must not prevent manifest deletion, and a
// DeleteManifest error is returned (not hidden) after Summary was attempted.
func TestHandleConfirmedSessionDeath_SummaryFailureStillDeletesManifest(t *testing.T) {
	t.Parallel()

	snap := orphanProcessSnapshot{Valid: true}
	noopReap := func(context.Context, []process.ProcessIdentity) orphanReapResult { return orphanReapResult{} }
	noopLog := func(bool, orphanProcessSnapshot, orphanReapResult) {}

	t.Run("summary panics, deletion still occurs", func(t *testing.T) {
		t.Parallel()
		deleteCalled := false
		deps := confirmedDeathDeps{
			EmitEnded:      func(context.Context) {},
			StopResilience: func() {},
			Reap:           noopReap,
			LogReapResult:  noopLog,
			Summary:        func() { panic("summary boom") },
			DeleteManifest: func() error {
				deleteCalled = true
				return nil
			},
		}

		if err := handleConfirmedSessionDeath(context.Background(), true, snap, deps); err != nil {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
		}
		if !deleteCalled {
			t.Error("DeleteManifest was not called after a panicking Summary")
		}
	})

	t.Run("delete error is returned, not hidden", func(t *testing.T) {
		t.Parallel()
		wantErr := errors.New("delete boom")
		summaryCalled := false
		deps := confirmedDeathDeps{
			EmitEnded:      func(context.Context) {},
			StopResilience: func() {},
			Reap:           noopReap,
			LogReapResult:  noopLog,
			Summary:        func() { summaryCalled = true },
			DeleteManifest: func() error { return wantErr },
		}

		err := handleConfirmedSessionDeath(context.Background(), true, snap, deps)
		if !errors.Is(err, wantErr) {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want %v", err, wantErr)
		}
		if !summaryCalled {
			t.Error("Summary was not attempted before the delete error was returned")
		}
	})
}

// TestHandleConfirmedSessionDeath_FinalizeMemorySamples covers Behavior 4 of
// the live-polled per-agent memory estimation TDD plan: FinalizeMemorySamples
// runs unconditionally (independent of the enabled reap gate), receives
// exactly the retained snapshot's ScopePeaks, sits between StopResilience
// and the reap step, a panic in it does not prevent the rest of the chain
// (including DeleteManifest) from running, and nil is a safe no-op.
func TestHandleConfirmedSessionDeath_FinalizeMemorySamples(t *testing.T) {
	t.Parallel()

	noopReap := func(context.Context, []process.ProcessIdentity) orphanReapResult { return orphanReapResult{} }
	noopLog := func(bool, orphanProcessSnapshot, orphanReapResult) {}

	t.Run("runs unconditionally, ordered after stop and before reap, receives exact ScopePeaks", func(t *testing.T) {
		t.Parallel()

		wantPeaks := map[string]agentScopePeak{
			"pane-b": {PaneID: "pane-b", PeakBytes: 4096},
		}
		snap := orphanProcessSnapshot{Valid: true, ScopePeaks: wantPeaks}

		var order []string
		var gotPeaks map[string]agentScopePeak
		deps := confirmedDeathDeps{
			EmitEnded:      func(context.Context) { order = append(order, "ended") },
			StopResilience: func() { order = append(order, "stop") },
			FinalizeMemorySamples: func(_ context.Context, peaks map[string]agentScopePeak) {
				order = append(order, "finalize")
				gotPeaks = peaks
			},
			Reap: func(context.Context, []process.ProcessIdentity) orphanReapResult {
				order = append(order, "reap")
				return orphanReapResult{}
			},
			LogReapResult:  func(bool, orphanProcessSnapshot, orphanReapResult) { order = append(order, "log") },
			Summary:        func() { order = append(order, "summary") },
			DeleteManifest: func() error { order = append(order, "delete"); return nil },
		}

		// enabled=false: FinalizeMemorySamples must still fire even though
		// Reap/LogReapResult are skipped by the disabled gate.
		if err := handleConfirmedSessionDeath(context.Background(), false, snap, deps); err != nil {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
		}

		wantOrder := []string{"ended", "stop", "finalize", "summary", "delete"}
		if !reflect.DeepEqual(order, wantOrder) {
			t.Errorf("effect order = %v, want %v", order, wantOrder)
		}
		if !reflect.DeepEqual(gotPeaks, wantPeaks) {
			t.Errorf("FinalizeMemorySamples received %+v, want %+v", gotPeaks, wantPeaks)
		}
	})

	t.Run("runs before reap when enabled", func(t *testing.T) {
		t.Parallel()

		snap := orphanProcessSnapshot{Valid: true, ScopePeaks: map[string]agentScopePeak{}}
		var order []string
		deps := confirmedDeathDeps{
			EmitEnded:             func(context.Context) {},
			StopResilience:        func() {},
			FinalizeMemorySamples: func(context.Context, map[string]agentScopePeak) { order = append(order, "finalize") },
			Reap: func(context.Context, []process.ProcessIdentity) orphanReapResult {
				order = append(order, "reap")
				return orphanReapResult{}
			},
			LogReapResult:  func(bool, orphanProcessSnapshot, orphanReapResult) { order = append(order, "log") },
			Summary:        func() {},
			DeleteManifest: func() error { return nil },
		}

		if err := handleConfirmedSessionDeath(context.Background(), true, snap, deps); err != nil {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
		}
		want := []string{"finalize", "reap", "log"}
		if !reflect.DeepEqual(order, want) {
			t.Errorf("order = %v, want %v", order, want)
		}
	})

	t.Run("empty ScopePeaks still finalizes (not skipped)", func(t *testing.T) {
		t.Parallel()

		snap := orphanProcessSnapshot{Valid: true, ScopePeaks: map[string]agentScopePeak{}}
		called := false
		var gotPeaks map[string]agentScopePeak
		deps := confirmedDeathDeps{
			EmitEnded:      func(context.Context) {},
			StopResilience: func() {},
			FinalizeMemorySamples: func(_ context.Context, peaks map[string]agentScopePeak) {
				called = true
				gotPeaks = peaks
			},
			Reap:           noopReap,
			LogReapResult:  noopLog,
			Summary:        func() {},
			DeleteManifest: func() error { return nil },
		}

		if err := handleConfirmedSessionDeath(context.Background(), true, snap, deps); err != nil {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
		}
		if !called {
			t.Error("FinalizeMemorySamples was not called for an empty (but non-nil) ScopePeaks map")
		}
		if len(gotPeaks) != 0 {
			t.Errorf("gotPeaks = %+v, want empty", gotPeaks)
		}
	})

	t.Run("panic is recovered, rest of the chain including DeleteManifest still runs", func(t *testing.T) {
		t.Parallel()

		snap := orphanProcessSnapshot{Valid: true}
		deleteCalled, summaryCalled := false, false
		deps := confirmedDeathDeps{
			EmitEnded:             func(context.Context) {},
			StopResilience:        func() {},
			FinalizeMemorySamples: func(context.Context, map[string]agentScopePeak) { panic("finalize boom") },
			Reap:                  noopReap,
			LogReapResult:         noopLog,
			Summary:               func() { summaryCalled = true },
			DeleteManifest:        func() error { deleteCalled = true; return nil },
		}

		if err := handleConfirmedSessionDeath(context.Background(), true, snap, deps); err != nil {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want nil", err)
		}
		if !summaryCalled || !deleteCalled {
			t.Errorf("summaryCalled=%v deleteCalled=%v, want both true after a panicking FinalizeMemorySamples", summaryCalled, deleteCalled)
		}
	})

	t.Run("nil FinalizeMemorySamples is a safe no-op", func(t *testing.T) {
		t.Parallel()

		snap := orphanProcessSnapshot{Valid: true, ScopePeaks: map[string]agentScopePeak{"pane-a": {}}}
		deps := confirmedDeathDeps{
			EmitEnded:      func(context.Context) {},
			StopResilience: func() {},
			// FinalizeMemorySamples intentionally left nil.
			Reap:           noopReap,
			LogReapResult:  noopLog,
			Summary:        func() {},
			DeleteManifest: func() error { return nil },
		}

		if err := handleConfirmedSessionDeath(context.Background(), true, snap, deps); err != nil {
			t.Fatalf("handleConfirmedSessionDeath error = %v, want nil (nil FinalizeMemorySamples must not panic)", err)
		}
	})
}

// TestRunMonitor_UsesManifestPolicyAndProductionLoop covers Behavior 6's
// production callback assembly: productionConfirmedDeathDeps wires every
// confirmedDeathDeps field to a real dependency, and DeleteManifest reaches
// the real, XDG-isolated resilience.DeleteManifest.
//
// The effective enabled bool at runMonitor's OnConfirmedDeath call site is
// a deliberate fail-safe false placeholder pending Behavior 1, which adds
// SpawnManifest.ReapOrphansOnExit and updates that one call site to read
// the real field — see the TODO in runMonitor.
func TestRunMonitor_UsesManifestPolicyAndProductionLoop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	manifest := &resilience.SpawnManifest{Session: "test-session", ProjectDir: t.TempDir()}
	cfg := config.Default()
	monitor := resilience.NewMonitor(manifest.Session, manifest.ProjectDir, cfg, false)

	deps := productionConfirmedDeathDeps(manifest.Session, manifest, monitor, time.Millisecond, map[string]string{})

	if deps.EmitEnded == nil || deps.StopResilience == nil || deps.Reap == nil ||
		deps.LogReapResult == nil || deps.Summary == nil || deps.DeleteManifest == nil {
		t.Fatalf("productionConfirmedDeathDeps returned a dependency struct with a nil field: %+v", deps)
	}

	// StopResilience must be the real monitor's Stop method, not a no-op —
	// calling it must be safe even though Start was never called.
	deps.StopResilience()

	// DeleteManifest must actually reach resilience.DeleteManifest: deleting
	// a manifest that was never saved is a no-op, not an error.
	if err := deps.DeleteManifest(); err != nil {
		t.Errorf("DeleteManifest() on a nonexistent manifest = %v, want nil (idempotent)", err)
	}
}

// waitForPidfile polls path until it contains a positive PID or the named
// deadline elapses, so callers never guess a fixed startup duration for a
// process that writes its own PID after launch.
func waitForPidfile(t *testing.T, path string, deadline time.Duration) int {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pidfile %s did not contain a valid PID within %s", path, deadline)
	return 0
}

// TestRunSessionMonitorLoop_OrganicDeathHUPSurvivor covers Behavior 7 of the
// periodic orphan-sweep TDD plan: the real-tmux Closure Test. A
// deliberately HUP-surviving process is launched inside a real tmux pane
// through tmux.SendKeys; runSessionMonitorLoop is driven with production
// observation/snapshot/reap dependencies and a real ticker (no PollTicks
// override); the exact target-plus-descendant identity set must appear in
// the one-shot ready snapshot; and after tmux.KillSession, the confirmed
// death path must remove every ready-snapshot identity when the policy is
// enabled while leaving them alive when it is disabled — both cases
// completing the same ended->stop->summary->delete workflow with exactly
// one summary callback and manifest absence before this test's own
// fallback cleanup.
func TestRunSessionMonitorLoop_OrganicDeathHUPSurvivor(t *testing.T) {
	testutil.RequireTmuxThrottled(t)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	const (
		behavior7PollInterval    = 100 * time.Millisecond
		behavior7OutputInterval  = time.Hour
		behavior7MaxMisses       = 3
		behavior7ReapGrace       = 200 * time.Millisecond
		behavior7ReadyDeadline   = 10 * time.Second
		behavior7JoinDeadline    = 10 * time.Second
		behavior7PidfileDeadline = 5 * time.Second
		behavior7AbsenceDeadline = 3 * time.Second
	)

	cases := []struct {
		name    string
		enabled bool
	}{
		{"enabled", true},
		{"disabled", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			pidFile := filepath.Join(t.TempDir(), "behavior7-target.pid")
			sessionName := fmt.Sprintf("ntm-test-b7-%s-%d", tc.name, time.Now().UnixNano())
			t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

			// A sentinel session, deliberately not a prefix of sessionName in
			// either direction, kept alive for the whole subtest so killing
			// sessionName below can never be the tmux server's last session.
			// Without this, that kill could tear the server down entirely,
			// producing CommandErrorNoServer — which also advances the miss
			// streak (see applyMonitorObservation) and would let this test
			// go green without ever exercising the session-specific
			// definite-absence path (CommandErrorSessionNotFound) that the
			// exactSessionTarget fix in internal/tmux/session.go exists for.
			sentinelSession := fmt.Sprintf("ntm-test-b7-sentinel-%s-%d", tc.name, time.Now().UnixNano())
			t.Cleanup(func() { _ = tmux.KillSession(sentinelSession) })
			if err := tmux.CreateSession(sentinelSession, projectDir); err != nil {
				t.Fatalf("CreateSession(sentinel): %v", err)
			}

			if err := tmux.CreateSession(sessionName, projectDir); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			panes, err := tmux.GetPanes(sessionName)
			if err != nil || len(panes) == 0 {
				t.Fatalf("GetPanes after create: panes=%v err=%v", panes, err)
			}
			pane := panes[0]
			if pane.PID <= 0 {
				t.Fatalf("pane has no resolved PID: %+v", pane)
			}

			manifest := &resilience.SpawnManifest{
				Session:           sessionName,
				ProjectDir:        projectDir,
				Agents:            []resilience.AgentConfig{{PaneID: pane.ID}},
				ReapOrphansOnExit: tc.enabled,
			}
			if err := resilience.SaveManifest(manifest); err != nil {
				t.Fatalf("SaveManifest: %v", err)
			}

			// The child is forked before the pidfile is written, so by the
			// time this test observes the pidfile, both the target and its
			// descendant already exist — runSessionMonitorLoop's Ready
			// callback fires only once, on the first usable live capture,
			// so both identities must already be present before the loop
			// starts, not merely by the time it eventually polls again.
			launchCmd := fmt.Sprintf(
				`nohup sh -c 'trap "" HUP; sleep 300 & echo $$ > %s; wait' >/dev/null 2>&1 & disown`,
				pidFile,
			)
			if err := tmux.SendKeys(pane.ID, launchCmd, true); err != nil {
				t.Fatalf("SendKeys launch: %v", err)
			}

			targetPID := waitForPidfile(t, pidFile, behavior7PidfileDeadline)
			targetIdentity, err := process.CaptureProcessIdentity(context.Background(), targetPID)
			if err != nil {
				t.Fatalf("CaptureProcessIdentity(target=%d): %v", targetPID, err)
			}
			if !process.IsAlive(targetIdentity.PID) {
				t.Fatalf("target pid %d must be alive right after capture", targetIdentity.PID)
			}

			cfg := config.Default()
			monitor := resilience.NewMonitor(manifest.Session, manifest.ProjectDir, cfg, false)

			var summaryCalls atomic.Int32
			var deathMu sync.Mutex
			var deathHandlerErr error
			deathDone := make(chan struct{})

			readyCh := make(chan orphanProcessSnapshot, 1)
			deps := monitorLoopDependencies{
				Observe:       tmux.GetPanesContext,
				CaptureOutput: func(string) {},
				SnapshotDeps:  productionOrphanSnapshotDeps(),
				Ready: func(snap orphanProcessSnapshot) {
					readyCh <- snap
				},
				OnConfirmedDeath: func(dctx context.Context, snap orphanProcessSnapshot) {
					defer close(deathDone)
					deathDeps := productionConfirmedDeathDeps(sessionName, manifest, monitor, behavior7ReapGrace, map[string]string{})
					deathDeps.Summary = func() {
						summaryCalls.Add(1)
					}
					if herr := handleConfirmedSessionDeath(dctx, tc.enabled, snap, deathDeps); herr != nil {
						deathMu.Lock()
						deathHandlerErr = herr
						deathMu.Unlock()
					}
				},
			}
			options := monitorLoopOptions{
				PollInterval:           behavior7PollInterval,
				OutputSnapshotInterval: behavior7OutputInterval,
				MaxMisses:              behavior7MaxMisses,
				ReapGrace:              behavior7ReapGrace,
			}

			ctx, cancel := context.WithCancel(context.Background())
			loopErrCh := make(chan error, 1)
			go func() {
				loopErrCh <- runSessionMonitorLoop(ctx, manifest, options, deps)
			}()

			var readySnap orphanProcessSnapshot
			select {
			case readySnap = <-readyCh:
			case <-time.After(behavior7ReadyDeadline):
				cancel()
				t.Fatal("runSessionMonitorLoop never signaled readiness")
			}

			// Hermetic cleanup contract: cancel and join first, then
			// identity-safely clean every captured process, registered
			// before this test does anything that could fail a t.Fatalf.
			t.Cleanup(func() {
				cancel()
				_ = tmux.KillSession(sessionName)
				select {
				case <-loopErrCh:
				case <-time.After(behavior7JoinDeadline):
				}
				for id := range readySnap.Candidates {
					if process.IsAlive(id.PID) {
						_ = syscall.Kill(id.PID, syscall.SIGKILL)
					}
				}
			})

			if _, ok := readySnap.Candidates[targetIdentity]; !ok {
				t.Fatalf("ready snapshot candidates %v do not contain target identity %+v", readySnap.Candidates, targetIdentity)
			}
			if len(readySnap.Candidates) < 2 {
				t.Fatalf("ready snapshot has %d candidate(s), want at least 2 (target + its descendant): %v", len(readySnap.Candidates), readySnap.Candidates)
			}

			if err := tmux.KillSession(sessionName); err != nil {
				t.Fatalf("KillSession: %v", err)
			}

			select {
			case err := <-loopErrCh:
				if err != nil {
					t.Fatalf("runSessionMonitorLoop returned error %v, want nil", err)
				}
			case <-time.After(behavior7JoinDeadline):
				t.Fatal("runSessionMonitorLoop did not join after tmux.KillSession")
			}

			select {
			case <-deathDone:
			case <-time.After(behavior7JoinDeadline):
				t.Fatal("OnConfirmedDeath handler did not complete")
			}
			deathMu.Lock()
			gotErr := deathHandlerErr
			deathMu.Unlock()
			if gotErr != nil {
				t.Fatalf("handleConfirmedSessionDeath returned error: %v", gotErr)
			}

			if got := summaryCalls.Load(); got != 1 {
				t.Errorf("summary callback invoked %d times, want exactly 1", got)
			}

			if _, err := resilience.LoadManifest(sessionName); err == nil {
				t.Error("manifest still present after confirmed death, want deleted before this test's own fallback cleanup")
			}

			if tc.enabled {
				deadline := time.Now().Add(behavior7AbsenceDeadline)
				for {
					stillAlive := false
					for id := range readySnap.Candidates {
						if process.IsAlive(id.PID) {
							stillAlive = true
							break
						}
					}
					if !stillAlive {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("candidate identities still alive after enabled reap: %v", readySnap.Candidates)
					}
					time.Sleep(20 * time.Millisecond)
				}
				return
			}

			if !process.IsAlive(targetIdentity.PID) {
				t.Error("target must still be alive after the loop joins when reaping is disabled")
			}
			fresh, err := process.CaptureProcessIdentity(context.Background(), targetIdentity.PID)
			if err != nil || fresh != targetIdentity {
				t.Errorf("disabled: fresh identity capture = %+v, err=%v; want exact match with %+v", fresh, err, targetIdentity)
			}
		})
	}
}

// TestCaptureOrphanProcessSnapshot_RealCgroupMemoryPeak covers Behavior 9 of
// the live-polled per-agent memory estimation TDD plan — the one real-
// infrastructure closure test. A real process, launched through the actual
// memLimitPrefix()-wrapped command (systemd-run --user --scope ...) inside
// a real tmux pane, allocates and holds a known amount of memory.
// runSessionMonitorLoop is driven with production dependencies — including
// the real cgroup reader (Behaviors 1-2) and the real file-backed memory-
// sample store (Behaviors 3-5) — and a short real poll interval, no mocks.
// A ScopePeaks entry must appear with a sane peak while the process is
// alive, and a persisted sample must appear in the store after the session
// (and its process) dies via confirmed death.
//
// Skipped, with a named reason, on any host without a systemd --user
// session / cgroup v2 — the same environment memLimitPrefix() itself
// requires, mirrored from TestMemLimitPrefix_MemoryHighBelowMemoryMax
// (internal/config/templates_test.go).
func TestCaptureOrphanProcessSnapshot_RealCgroupMemoryPeak(t *testing.T) {
	testutil.RequireTmuxThrottled(t)

	prefix, err := config.GenerateAgentCommand("{{memLimitPrefix}}", config.AgentTemplateVars{})
	if err != nil {
		t.Fatalf("GenerateAgentCommand: %v", err)
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		t.Skip("memLimitPrefix() is empty in this environment (no systemd --user session / cgroup v2); nothing to close")
	}

	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	store := pressure.NewFileMemorySampleStore()

	const (
		behavior9PollInterval    = 300 * time.Millisecond
		behavior9OutputInterval  = time.Hour
		behavior9MaxMisses       = 3
		behavior9ReapGrace       = 200 * time.Millisecond
		behavior9ReadyDeadline   = 15 * time.Second
		behavior9JoinDeadline    = 15 * time.Second
		behavior9PidfileDeadline = 10 * time.Second
		behavior9AllocMB         = 64
		behavior9MinSanePeak     = 32 * 1024 * 1024  // comfortably above half of a 64MB allocation
		behavior9MaxSanePeak     = 512 * 1024 * 1024 // generous upper bound
	)

	projectDir := t.TempDir()
	pidFile := filepath.Join(t.TempDir(), "behavior9-target.pid")
	sessionName := fmt.Sprintf("ntm-test-b9-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = tmux.KillSession(sessionName) })

	if err := tmux.CreateSession(sessionName, projectDir); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	panes, err := tmux.GetPanes(sessionName)
	if err != nil || len(panes) == 0 {
		t.Fatalf("GetPanes after create: panes=%v err=%v", panes, err)
	}
	pane := panes[0]
	if pane.PID <= 0 {
		t.Fatalf("pane has no resolved PID: %+v", pane)
	}

	manifest := &resilience.SpawnManifest{
		Session:    sessionName,
		ProjectDir: projectDir,
		Agents:     []resilience.AgentConfig{{PaneID: pane.ID, Type: "cc"}},
	}
	if err := resilience.SaveManifest(manifest); err != nil {
		t.Fatalf("SaveManifest: %v", err)
	}

	// Allocate behavior9AllocMB of real, resident memory (via a shell
	// variable holding dd|tr output — portable, needs no extra tooling)
	// BEFORE writing the pidfile, so by the time this test observes the
	// pidfile the allocation is already reflected in memory.peak, avoiding
	// a race against the loop's one-shot Ready capture. systemd-run execs
	// in place (no fork — see Locked Decisions), so this sh -c process's
	// PID is stable across the wrap and is the pane shell's direct (depth
	// 1) child.
	innerScript := fmt.Sprintf(
		`DATA=$(dd if=/dev/zero bs=1M count=%d 2>/dev/null | tr "\0" "a"); echo $$ > %s; sleep 60`,
		behavior9AllocMB, pidFile,
	)
	launchCmd := fmt.Sprintf(`%s sh -c '%s'`, prefix, innerScript)
	if err := tmux.SendKeys(pane.ID, launchCmd, true); err != nil {
		t.Fatalf("SendKeys launch: %v", err)
	}

	targetPID := waitForPidfile(t, pidFile, behavior9PidfileDeadline)
	targetIdentity, err := process.CaptureProcessIdentity(context.Background(), targetPID)
	if err != nil {
		t.Fatalf("CaptureProcessIdentity(target=%d): %v", targetPID, err)
	}

	monitor := resilience.NewMonitor(manifest.Session, manifest.ProjectDir, config.Default(), false)
	readyCh := make(chan orphanProcessSnapshot, 1)
	deathDone := make(chan struct{})
	var deathMu sync.Mutex
	var deathHandlerErr error

	deps := monitorLoopDependencies{
		Observe:       tmux.GetPanesContext,
		CaptureOutput: func(string) {},
		SnapshotDeps:  productionOrphanSnapshotDeps(),
		Ready: func(snap orphanProcessSnapshot) {
			readyCh <- snap
		},
		OnAgentGenerationEnded: productionOnAgentGenerationEnded(store),
		OnConfirmedDeath: func(dctx context.Context, snap orphanProcessSnapshot) {
			defer close(deathDone)
			deathDeps := productionConfirmedDeathDeps(sessionName, manifest, monitor, behavior9ReapGrace, map[string]string{})
			if herr := handleConfirmedSessionDeath(dctx, false, snap, deathDeps); herr != nil {
				deathMu.Lock()
				deathHandlerErr = herr
				deathMu.Unlock()
			}
		},
	}
	options := monitorLoopOptions{
		PollInterval:           behavior9PollInterval,
		OutputSnapshotInterval: behavior9OutputInterval,
		MaxMisses:              behavior9MaxMisses,
		ReapGrace:              behavior9ReapGrace,
	}

	ctx, cancel := context.WithCancel(context.Background())
	loopErrCh := make(chan error, 1)
	go func() {
		loopErrCh <- runSessionMonitorLoop(ctx, manifest, options, deps)
	}()

	t.Cleanup(func() {
		cancel()
		_ = tmux.KillSession(sessionName)
		select {
		case <-loopErrCh:
		case <-time.After(behavior9JoinDeadline):
		}
		if process.IsAlive(targetPID) {
			_ = syscall.Kill(targetPID, syscall.SIGKILL)
		}
	})

	var readySnap orphanProcessSnapshot
	select {
	case readySnap = <-readyCh:
	case <-time.After(behavior9ReadyDeadline):
		cancel()
		t.Fatal("runSessionMonitorLoop never signaled readiness")
	}

	gotPeak, ok := readySnap.ScopePeaks[pane.ID]
	if !ok {
		t.Fatalf("ready snapshot has no ScopePeaks entry for pane %s: %+v", pane.ID, readySnap.ScopePeaks)
	}
	if gotPeak.PeakBytes < behavior9MinSanePeak || gotPeak.PeakBytes > behavior9MaxSanePeak {
		t.Fatalf("ScopePeaks[%s].PeakBytes = %d, want a sane peak in [%d, %d] for a %dMB allocation",
			pane.ID, gotPeak.PeakBytes, behavior9MinSanePeak, behavior9MaxSanePeak, behavior9AllocMB)
	}
	if gotPeak.AgentType != agent.AgentTypeClaudeCode {
		t.Errorf("ScopePeaks[%s].AgentType = %q, want %q", pane.ID, gotPeak.AgentType, agent.AgentTypeClaudeCode)
	}
	if gotPeak.Identity != targetIdentity {
		t.Errorf("ScopePeaks[%s].Identity = %+v, want %+v", pane.ID, gotPeak.Identity, targetIdentity)
	}

	if err := tmux.KillSession(sessionName); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	select {
	case err := <-loopErrCh:
		if err != nil {
			t.Fatalf("runSessionMonitorLoop returned error %v, want nil", err)
		}
	case <-time.After(behavior9JoinDeadline):
		t.Fatal("runSessionMonitorLoop did not join after tmux.KillSession")
	}

	select {
	case <-deathDone:
	case <-time.After(behavior9JoinDeadline):
		t.Fatal("OnConfirmedDeath handler did not complete")
	}
	deathMu.Lock()
	gotErr := deathHandlerErr
	deathMu.Unlock()
	if gotErr != nil {
		t.Fatalf("handleConfirmedSessionDeath returned error: %v", gotErr)
	}

	samples, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("store.Load after confirmed death: %v", err)
	}
	persisted := false
	for _, s := range samples {
		if s.PaneID == pane.ID && s.AgentType == agent.AgentTypeClaudeCode && s.PeakBytes >= behavior9MinSanePeak {
			persisted = true
			break
		}
	}
	if !persisted {
		t.Fatalf("no persisted sample found for pane %s after confirmed death; samples=%+v", pane.ID, samples)
	}
}
