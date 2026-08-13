package cli

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
