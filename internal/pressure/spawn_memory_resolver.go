package pressure

import (
	"context"
	"sort"

	"github.com/Dicklesworthstone/ntm/internal/agent"
)

// AgentMemoryEstimate is one canonical agent type's contribution to a
// spawn's aggregate memory-admission estimate, kept inspectable in
// SpawnAdmission's JSON for --robot-spawn --dry-run.
type AgentMemoryEstimate struct {
	Type        agent.AgentType      `json:"type"`
	Count       int                  `json:"count"`
	EstimateMB  uint64               `json:"estimate_mb"`
	Source      MemoryEstimateSource `json:"source"`
	Confidence  float64              `json:"confidence"`
	SampleCount int                  `json:"sample_count"`
}

// ResolveSpawnMemoryEstimate computes both the aggregate requested-memory
// MB (for admission math) and the per-type breakdown (for the JSON
// response) for a spawn requesting counts[type] agents of each canonical
// type. It is the single resolver shared by the CLI and robot spawn
// admission call sites — one implementation, not two near-duplicates.
//
// An explicit positive overrideMB (an operator's per_agent_expected_mem_mb)
// short-circuits entirely: every type's row uses overrideMB with
// MemoryEstimateOverride and zero sample count, and store is never
// touched — zero store I/O for the override path, proven by callers able
// to pass a nil or panic-on-call store fake in that case.
//
// Otherwise every type independently loads its own recent samples from
// store (a single Load call, split by type) and estimates via
// EstimateAgentMemMB with the given floor/ceiling. A nil store, or a
// failed Load (e.g. a corrupt file), degrades every type to
// EstimateAgentMemMB's own cold-start fallback — never an error returned
// to the caller, matching this plan's best-effort evidence posture.
func ResolveSpawnMemoryEstimate(ctx context.Context, counts map[agent.AgentType]int, overrideMB, floorMB, ceilingMB uint64, store MemorySampleStore) (totalMB int, rows []AgentMemoryEstimate) {
	types := make([]agent.AgentType, 0, len(counts))
	for t, count := range counts {
		if count > 0 {
			types = append(types, t)
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })

	if overrideMB > 0 {
		for _, t := range types {
			count := counts[t]
			rows = append(rows, AgentMemoryEstimate{
				Type:       t,
				Count:      count,
				EstimateMB: overrideMB,
				Source:     MemoryEstimateOverride,
			})
			totalMB += count * int(overrideMB)
		}
		return totalMB, rows
	}

	samplesByType := make(map[agent.AgentType][]MemorySample)
	if store != nil {
		if all, err := store.Load(ctx); err == nil {
			for _, s := range all {
				samplesByType[s.AgentType] = append(samplesByType[s.AgentType], s)
			}
		}
	}

	for _, t := range types {
		count := counts[t]
		mb, source, confidence := EstimateAgentMemMB(samplesByType[t], floorMB, ceilingMB)
		rows = append(rows, AgentMemoryEstimate{
			Type:        t,
			Count:       count,
			EstimateMB:  mb,
			Source:      source,
			Confidence:  confidence,
			SampleCount: len(samplesByType[t]),
		})
		totalMB += count * int(mb)
	}
	return totalMB, rows
}
