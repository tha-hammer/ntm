package pressure

import (
	"context"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// fakeMemorySampleStore is a deterministic MemorySampleStore for resolver
// tests. loadFn is nil-safe: a nil loadFn means Load must never be called.
type fakeMemorySampleStore struct {
	t          *testing.T
	loadCalled bool
	samples    []MemorySample
	loadErr    error
}

func (f *fakeMemorySampleStore) Load(context.Context) ([]MemorySample, error) {
	f.loadCalled = true
	return f.samples, f.loadErr
}

func (f *fakeMemorySampleStore) Append(context.Context, MemorySample) error {
	f.t.Fatal("Append must not be called by ResolveSpawnMemoryEstimate")
	return nil
}

func calibratedSamples(agentType agent.AgentType, n int, mb uint64) []MemorySample {
	samples := make([]MemorySample, n)
	for i := 0; i < n; i++ {
		samples[i] = MemorySample{
			PaneID:     "p",
			AgentType:  agentType,
			PeakBytes:  mb * bytesPerMB,
			ObservedAt: time.Unix(int64(1000+i), 0).UTC(),
			Generation: int64(i),
		}
	}
	return samples
}

func TestResolveSpawnMemoryEstimate_OverrideShortCircuitsWithZeroStoreIO(t *testing.T) {
	t.Parallel()

	store := &fakeMemorySampleStore{t: t} // Load would fail the test too, via loadCalled assertion below
	counts := map[agent.AgentType]int{
		agent.AgentTypeClaudeCode: 3,
		agent.AgentTypeCodex:      2,
	}
	const overrideMB = 4096

	totalMB, rows := ResolveSpawnMemoryEstimate(context.Background(), counts, overrideMB, 2048, 16384, store)

	if store.loadCalled {
		t.Error("store.Load was called despite a positive override — override must be zero store I/O")
	}
	wantTotal := 3*overrideMB + 2*overrideMB
	if totalMB != wantTotal {
		t.Errorf("totalMB = %d, want %d", totalMB, wantTotal)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Source != MemoryEstimateOverride {
			t.Errorf("row %+v: source = %q, want %q", r, r.Source, MemoryEstimateOverride)
		}
		if r.EstimateMB != overrideMB {
			t.Errorf("row %+v: EstimateMB = %d, want %d", r, r.EstimateMB, overrideMB)
		}
		if r.SampleCount != 0 {
			t.Errorf("row %+v: SampleCount = %d, want 0 (override never consults samples)", r, r.SampleCount)
		}
	}
}

func TestResolveSpawnMemoryEstimate_NilStoreWithOverrideIsSafe(t *testing.T) {
	t.Parallel()
	counts := map[agent.AgentType]int{agent.AgentTypeClaudeCode: 1}
	totalMB, rows := ResolveSpawnMemoryEstimate(context.Background(), counts, 5000, 2048, 16384, nil)
	if totalMB != 5000 || len(rows) != 1 {
		t.Fatalf("totalMB=%d rows=%+v, want 5000 and one row", totalMB, rows)
	}
}

func TestResolveSpawnMemoryEstimate_MixedTypeSum(t *testing.T) {
	t.Parallel()

	// Claude: 5 samples all reporting 3000MB -> calibrated, p90=3000,
	// scaled=3750. Codex: cold (no samples) -> fallback at floor 2048.
	store := &fakeMemorySampleStore{
		t:       t,
		samples: calibratedSamples(agent.AgentTypeClaudeCode, 5, 3000),
	}
	counts := map[agent.AgentType]int{
		agent.AgentTypeClaudeCode: 2,
		agent.AgentTypeCodex:      3,
	}

	totalMB, rows := ResolveSpawnMemoryEstimate(context.Background(), counts, 0, 2048, 16384, store)

	if !store.loadCalled {
		t.Fatal("store.Load was not called for the non-override path")
	}

	byType := make(map[agent.AgentType]AgentMemoryEstimate, len(rows))
	for _, r := range rows {
		byType[r.Type] = r
	}

	cc, ok := byType[agent.AgentTypeClaudeCode]
	if !ok {
		t.Fatal("missing ClaudeCode row")
	}
	if cc.EstimateMB != 3750 || cc.Source != MemoryEstimateCalibrated || cc.SampleCount != 5 {
		t.Errorf("ClaudeCode row = %+v, want EstimateMB=3750 Source=calibrated SampleCount=5", cc)
	}

	cod, ok := byType[agent.AgentTypeCodex]
	if !ok {
		t.Fatal("missing Codex row")
	}
	if cod.EstimateMB != 2048 || cod.Source != MemoryEstimateFallback || cod.SampleCount != 0 {
		t.Errorf("Codex row = %+v, want EstimateMB=2048 Source=fallback SampleCount=0", cod)
	}

	wantTotal := 2*3750 + 3*2048
	if totalMB != wantTotal {
		t.Errorf("totalMB = %d, want %d", totalMB, wantTotal)
	}
}

func TestResolveSpawnMemoryEstimate_LoadFailureFallsBackForEveryType(t *testing.T) {
	t.Parallel()

	store := &fakeMemorySampleStore{t: t, loadErr: context.DeadlineExceeded}
	counts := map[agent.AgentType]int{agent.AgentTypeClaudeCode: 1}

	totalMB, rows := ResolveSpawnMemoryEstimate(context.Background(), counts, 0, 2048, 16384, store)
	if len(rows) != 1 || rows[0].Source != MemoryEstimateFallback || rows[0].EstimateMB != 2048 {
		t.Fatalf("rows = %+v, want one fallback-at-floor row", rows)
	}
	if totalMB != 2048 {
		t.Errorf("totalMB = %d, want 2048", totalMB)
	}
}

func TestResolveSpawnMemoryEstimate_ZeroCountTypeIsExcluded(t *testing.T) {
	t.Parallel()
	counts := map[agent.AgentType]int{
		agent.AgentTypeClaudeCode: 2,
		agent.AgentTypeCodex:      0,
		agent.AgentTypeGemini:     -1,
	}
	_, rows := ResolveSpawnMemoryEstimate(context.Background(), counts, 1000, 2048, 16384, nil)
	if len(rows) != 1 || rows[0].Type != agent.AgentTypeClaudeCode {
		t.Fatalf("rows = %+v, want exactly one ClaudeCode row (zero/negative counts excluded)", rows)
	}
}

func TestResolveSpawnMemoryEstimate_RowsSortedByType(t *testing.T) {
	t.Parallel()
	counts := map[agent.AgentType]int{
		agent.AgentTypeGemini:     1,
		agent.AgentTypeClaudeCode: 1,
		agent.AgentTypeCodex:      1,
	}
	_, rows := ResolveSpawnMemoryEstimate(context.Background(), counts, 1000, 2048, 16384, nil)
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Type >= rows[i].Type {
			t.Errorf("rows not sorted by Type: %+v", rows)
		}
	}
}
