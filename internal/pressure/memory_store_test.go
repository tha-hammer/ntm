package pressure

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

func TestMemorySampleStorePath_RespectsXDGDataHome(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)
	want := filepath.Join(tmpDir, "ntm", "agent-memory-samples.json")
	if got := MemorySampleStorePath(); got != want {
		t.Fatalf("MemorySampleStorePath() = %q, want %q", got, want)
	}
}

func TestFileMemorySampleStore_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	store := NewFileMemorySampleStore()
	ctx := context.Background()

	empty, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load (cold start) error = %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Load (cold start) = %+v, want empty", empty)
	}

	want := MemorySample{
		PaneID:     "pane-a",
		AgentType:  agent.AgentTypeClaudeCode,
		PeakBytes:  123456,
		ObservedAt: time.Unix(1700000000, 0).UTC(),
		Generation: 42,
	}
	if err := store.Append(ctx, want); err != nil {
		t.Fatalf("Append error = %v", err)
	}

	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("Load = %+v, want [%+v]", got, want)
	}
}

func TestFileMemorySampleStore_DuplicateAppendIsNoOp(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)
	store := NewFileMemorySampleStore()
	ctx := context.Background()

	sample := MemorySample{PaneID: "pane-a", AgentType: agent.AgentTypeClaudeCode, Generation: 1, PeakBytes: 100}
	if err := store.Append(ctx, sample); err != nil {
		t.Fatalf("first Append error = %v", err)
	}
	dup := sample
	dup.PeakBytes = 999 // same dedup key, different payload
	if err := store.Append(ctx, dup); err != nil {
		t.Fatalf("second Append error = %v", err)
	}

	got, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if len(got) != 1 || got[0].PeakBytes != 100 {
		t.Fatalf("Load = %+v, want exactly one entry retaining the first Append's payload", got)
	}
}

func TestFileMemorySampleStore_CorruptFileLeftUntouchedAndReportedAsError(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	path := MemorySampleStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	corrupt := []byte("{not valid json")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	store := NewFileMemorySampleStore()
	ctx := context.Background()
	if _, err := store.Load(ctx); err == nil {
		t.Fatal("Load error = nil, want an error for a corrupt file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading file: %v", err)
	}
	if !bytes.Equal(after, corrupt) {
		t.Errorf("corrupt file was modified by a failed Load: got %q, want unchanged %q", after, corrupt)
	}

	// Append must also refuse to silently clobber a corrupt file with a
	// fresh one-sample file.
	if err := store.Append(ctx, MemorySample{PaneID: "p", Generation: 1}); err == nil {
		t.Error("Append error = nil, want an error instead of silently overwriting a corrupt file")
	}
	after2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading file after failed Append: %v", err)
	}
	if !bytes.Equal(after2, corrupt) {
		t.Errorf("corrupt file was modified by a failed Append: got %q, want unchanged %q", after2, corrupt)
	}
}

func TestAppendMemorySample_DuplicateIsNoOp(t *testing.T) {
	t.Parallel()
	existing := []MemorySample{{PaneID: "p", AgentType: agent.AgentTypeClaudeCode, Generation: 1, PeakBytes: 100}}
	dup := MemorySample{PaneID: "p", AgentType: agent.AgentTypeClaudeCode, Generation: 1, PeakBytes: 999}

	got := appendMemorySample(existing, dup)
	if len(got) != 1 || got[0].PeakBytes != 100 {
		t.Fatalf("got %+v, want the original entry unchanged (duplicate is a no-op, not an update)", got)
	}
}

func TestAppendMemorySample_EvictionOrder(t *testing.T) {
	t.Parallel()

	var existing []MemorySample
	base := time.Unix(1000, 0)
	for i := 0; i < maxSamplesPerType; i++ {
		existing = append(existing, MemorySample{
			PaneID:     fmt.Sprintf("pane-%d", i),
			AgentType:  agent.AgentTypeClaudeCode,
			Generation: int64(i),
			ObservedAt: base.Add(time.Duration(i) * time.Second),
			PeakBytes:  uint64(i),
		})
	}
	newest := MemorySample{
		PaneID:     "pane-new",
		AgentType:  agent.AgentTypeClaudeCode,
		Generation: int64(maxSamplesPerType),
		ObservedAt: base.Add(time.Duration(maxSamplesPerType) * time.Second),
		PeakBytes:  999,
	}

	got := appendMemorySample(existing, newest)
	if len(got) != maxSamplesPerType {
		t.Fatalf("len(got) = %d, want %d", len(got), maxSamplesPerType)
	}
	for _, s := range got {
		if s.Generation == 0 {
			t.Fatalf("oldest sample (generation 0) was not evicted: %+v", got)
		}
	}
	foundNewest := false
	for _, s := range got {
		if s.Generation == int64(maxSamplesPerType) {
			foundNewest = true
		}
	}
	if !foundNewest {
		t.Fatalf("newest sample missing after eviction: %+v", got)
	}
}

func TestAppendMemorySample_PerTypeIndependentEviction(t *testing.T) {
	t.Parallel()

	var existing []MemorySample
	for i := 0; i < maxSamplesPerType; i++ {
		existing = append(existing, MemorySample{PaneID: fmt.Sprintf("a-%d", i), AgentType: agent.AgentTypeClaudeCode, Generation: int64(i)})
	}
	existing = append(existing, MemorySample{PaneID: "b-0", AgentType: agent.AgentTypeCodex, Generation: 0})

	got := appendMemorySample(existing, MemorySample{PaneID: "b-1", AgentType: agent.AgentTypeCodex, Generation: 1})

	ccCount, codCount := 0, 0
	for _, s := range got {
		switch s.AgentType {
		case agent.AgentTypeClaudeCode:
			ccCount++
		case agent.AgentTypeCodex:
			codCount++
		}
	}
	if ccCount != maxSamplesPerType {
		t.Errorf("AgentTypeClaudeCode count = %d, want %d (must be untouched by a Codex append)", ccCount, maxSamplesPerType)
	}
	if codCount != 2 {
		t.Errorf("AgentTypeCodex count = %d, want 2", codCount)
	}
}

// TestMemoryStoreHelperProcess is a self-exec helper invoked by
// TestFileMemorySampleStore_ConcurrentAppendAcrossProcesses: when run
// directly by `go test` it is a no-op, mirroring
// internal/process/liveness_test.go's TestLivenessHelperProcess pattern.
func TestMemoryStoreHelperProcess(t *testing.T) {
	if os.Getenv("NTM_MEMORY_STORE_HELPER") != "append" {
		return
	}
	workerIdx, err := strconv.Atoi(os.Getenv("NTM_MEMORY_STORE_HELPER_IDX"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad worker index: %v\n", err)
		os.Exit(2)
	}

	store := NewFileMemorySampleStore()
	ctx := context.Background()
	for i := 0; i < memoryStoreHelperSamplesPerWorker; i++ {
		sample := MemorySample{
			PaneID:     fmt.Sprintf("pane-%d-%d", workerIdx, i),
			AgentType:  agent.AgentTypeClaudeCode,
			PeakBytes:  1024,
			ObservedAt: time.Unix(int64(1000+workerIdx*100+i), 0).UTC(),
			Generation: int64(workerIdx*1000 + i),
		}
		if err := store.Append(ctx, sample); err != nil {
			fmt.Fprintf(os.Stderr, "append failed: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(0)
}

const (
	memoryStoreHelperWorkers          = 5
	memoryStoreHelperSamplesPerWorker = 3
)

// TestFileMemorySampleStore_ConcurrentAppendAcrossProcesses covers Behavior
// 5's real cross-process concurrency requirement: the in-process mutex
// alone cannot arbitrate between separate OS processes (e.g. two
// internal-monitor processes for two active sessions), so this spawns real
// subprocesses — not goroutines — racing Append against the same store
// file, and asserts no sample is lost.
func TestFileMemorySampleStore_ConcurrentAppendAcrossProcesses(t *testing.T) {
	tmpDir := t.TempDir()

	var wg sync.WaitGroup
	errCh := make(chan error, memoryStoreHelperWorkers)
	for i := 0; i < memoryStoreHelperWorkers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=TestMemoryStoreHelperProcess")
			cmd.Env = append(os.Environ(),
				"NTM_MEMORY_STORE_HELPER=append",
				fmt.Sprintf("NTM_MEMORY_STORE_HELPER_IDX=%d", idx),
				"XDG_DATA_HOME="+tmpDir,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				errCh <- fmt.Errorf("worker %d: %w: %s", idx, err, out)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	t.Setenv("XDG_DATA_HOME", tmpDir)
	store := NewFileMemorySampleStore()
	samples, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	wantTotal := memoryStoreHelperWorkers * memoryStoreHelperSamplesPerWorker
	if len(samples) != wantTotal {
		t.Fatalf("len(samples) = %d, want %d (no sample should be lost to cross-process lock contention): %+v", len(samples), wantTotal, samples)
	}

	seen := make(map[int64]struct{}, wantTotal)
	for _, s := range samples {
		seen[s.Generation] = struct{}{}
	}
	if len(seen) != wantTotal {
		t.Errorf("len(unique generations) = %d, want %d — a concurrent write clobbered another's data", len(seen), wantTotal)
	}
}
