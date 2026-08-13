package pressure

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/ntm/internal/agent"
	"github.com/Dicklesworthstone/ntm/internal/util"
)

const (
	memorySampleStoreFileName = "agent-memory-samples.json"
	maxSamplesPerType         = 50
)

// MemorySample is one best-effort observed peak-memory reading for a single
// agent process generation.
type MemorySample struct {
	PaneID     string          `json:"pane_id"`
	AgentType  agent.AgentType `json:"agent_type"`
	PeakBytes  uint64          `json:"peak_bytes"`
	ObservedAt time.Time       `json:"observed_at"`
	Generation int64           `json:"generation"` // ProcessIdentity.CreateTimeMillis — dedup key
}

// MemorySampleStore persists best-effort per-agent-type memory samples
// across ntm invocations, so EstimateAgentMemMB can learn from real
// observed usage instead of a flat guess.
type MemorySampleStore interface {
	Load(ctx context.Context) ([]MemorySample, error)
	Append(ctx context.Context, sample MemorySample) error
}

// MemorySampleStorePath returns the path to the memory-sample store file.
// Uses XDG_DATA_HOME if set, otherwise ~/.local/share/ntm/agent-memory-samples.json —
// the same resolution order as this codebase's other XDG-scoped stores.
func MemorySampleStorePath() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return memorySampleStoreFileName // fallback to current dir
		}
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "ntm", memorySampleStoreFileName)
}

// memorySampleStoreMu is the in-process complement to the OS-level flock,
// mirroring internal/history's acquireLock pattern exactly: every Load and
// Append — reads included — takes this mutex, so within one process all
// store access is fully serialized; the flock is what additionally
// arbitrates across separate OS processes (e.g. two internal-monitor
// processes for two active sessions).
var memorySampleStoreMu sync.Mutex

// acquireMemorySampleLock is implemented in platform-specific files:
//   - memory_store_lock_unix.go for Unix systems (flock, shared for Load,
//     exclusive for Append)
//   - memory_store_lock_windows.go for Windows (in-process mutex only)

// fileMemorySampleStore is the flock-based MemorySampleStore.
type fileMemorySampleStore struct{}

// NewFileMemorySampleStore returns the production, flock-backed
// MemorySampleStore.
func NewFileMemorySampleStore() MemorySampleStore {
	return fileMemorySampleStore{}
}

func (fileMemorySampleStore) Load(_ context.Context) ([]MemorySample, error) {
	unlock, err := acquireMemorySampleLock(true)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return loadMemorySamplesLocked()
}

func (fileMemorySampleStore) Append(_ context.Context, sample MemorySample) error {
	unlock, err := acquireMemorySampleLock(false)
	if err != nil {
		return err
	}
	defer unlock()

	existing, err := loadMemorySamplesLocked()
	if err != nil {
		return err
	}
	return saveMemorySamplesLocked(appendMemorySample(existing, sample))
}

// loadMemorySamplesLocked reads every sample from the store file (caller
// must hold the lock). A missing file is an empty, non-error result — cold
// start. A present-but-corrupt file returns an error and is left untouched;
// callers (the estimator) fall back to cold-start behavior for every type
// rather than silently discarding an operator's data.
func loadMemorySamplesLocked() ([]MemorySample, error) {
	path := MemorySampleStorePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var samples []MemorySample
	if err := json.Unmarshal(data, &samples); err != nil {
		return nil, fmt.Errorf("%s: %w", memorySampleStoreFileName, err)
	}
	return samples, nil
}

// saveMemorySamplesLocked rewrites the store file wholesale (caller must
// hold the lock), atomically via a same-directory temp file + rename.
func saveMemorySamplesLocked(samples []MemorySample) error {
	path := MemorySampleStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.Marshal(samples)
	if err != nil {
		return err
	}
	return util.AtomicWriteFile(path, data, 0600)
}

// appendMemorySample returns existing with sample merged in. An exact
// (PaneID, AgentType, Generation) duplicate is a no-op — existing is
// returned unchanged, not re-sorted or re-pruned. Otherwise sample is
// added and every AgentType's samples are independently pruned to the most
// recent maxSamplesPerType, oldest evicted first by ObservedAt.
func appendMemorySample(existing []MemorySample, sample MemorySample) []MemorySample {
	for _, s := range existing {
		if s.PaneID == sample.PaneID && s.AgentType == sample.AgentType && s.Generation == sample.Generation {
			return existing
		}
	}

	merged := append(append([]MemorySample(nil), existing...), sample)

	var order []agent.AgentType
	byType := make(map[agent.AgentType][]MemorySample)
	for _, s := range merged {
		if _, ok := byType[s.AgentType]; !ok {
			order = append(order, s.AgentType)
		}
		byType[s.AgentType] = append(byType[s.AgentType], s)
	}

	result := make([]MemorySample, 0, len(merged))
	for _, t := range order {
		group := byType[t]
		sort.Slice(group, func(i, j int) bool { return group[i].ObservedAt.Before(group[j].ObservedAt) })
		if len(group) > maxSamplesPerType {
			group = group[len(group)-maxSamplesPerType:]
		}
		result = append(result, group...)
	}
	return result
}
