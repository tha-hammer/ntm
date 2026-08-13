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

	"github.com/Dicklesworthstone/ntm/internal/config"
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

func TestProductionOrphanSnapshotDeps_WiresRealProcessPackage(t *testing.T) {
	t.Parallel()
	deps := productionOrphanSnapshotDeps()
	if deps.childPIDs == nil || deps.captureIdentity == nil {
		t.Fatalf("productionOrphanSnapshotDeps() returned nil dependency: %+v", deps)
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
