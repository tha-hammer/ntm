package pressure

import (
	"math"
	"sort"
)

// MemoryEstimateSource classifies where an EstimateAgentMemMB result came
// from: an explicit operator override, a calibrated estimate derived from
// real observed samples, or the safe fallback baseline.
type MemoryEstimateSource string

const (
	MemoryEstimateOverride   MemoryEstimateSource = "override"
	MemoryEstimateCalibrated MemoryEstimateSource = "calibrated"
	MemoryEstimateFallback   MemoryEstimateSource = "fallback"
)

const (
	memoryEstimatePercentileRank  = 90 // p90 of observed peaks
	memoryEstimateSafetyMarginNum = 5  // 5:4 safety margin over the raw p90
	memoryEstimateSafetyMarginDen = 4
	bytesPerMB                    = 1024 * 1024
)

// EstimateAgentMemMB estimates one agent type's expected memory footprint
// in MB from its recent observed peak-memory samples: the p90
// (nearest-rank) of PeakBytes, converted to MB by ceiling division, scaled
// by a 5:4 safety margin, then clamped to [floorMB, ceilingMB].
//
// Confidence reuses this package's existing sample-count curve
// (confidenceFromSamples); below defaultMinCalibrationConfidence the
// result is MemoryEstimateFallback at exactly floorMB — not a low-
// confidence computed number — which is what makes "automatic sizing
// never returns less than floorMB" (Invariant 1 of the live-polled
// per-agent memory estimation plan) hold structurally rather than by
// convention alone. Zero samples always falls below the gate, so a cold
// host is byte-identical to the flat constant this estimator replaces.
//
// An invalid ceilingMB < floorMB can never push the returned value below
// floorMB: the clamp treats such a ceiling as equal to the floor rather
// than letting it override Invariant 1.
func EstimateAgentMemMB(samples []MemorySample, floorMB, ceilingMB uint64) (mb uint64, source MemoryEstimateSource, confidence float64) {
	confidence = confidenceFromSamples(len(samples))
	if confidence < defaultMinCalibrationConfidence {
		return floorMB, MemoryEstimateFallback, confidence
	}

	peaks := make([]uint64, len(samples))
	for i, s := range samples {
		peaks[i] = s.PeakBytes
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i] < peaks[j] })

	p90MB := ceilBytesToMB(nearestRankPercentile(peaks, memoryEstimatePercentileRank))
	scaledMB := p90MB * memoryEstimateSafetyMarginNum / memoryEstimateSafetyMarginDen

	return clampUint64(scaledMB, floorMB, ceilingMB), MemoryEstimateCalibrated, confidence
}

// nearestRankPercentile returns the pth percentile (1-100) of
// ascending-sorted values using the nearest-rank method: index =
// ceil(p/100 * n), 1-based, clamped to [1, n]. Callers must not invoke
// this on an empty slice — EstimateAgentMemMB never does, since its
// confidence gate already rules out zero samples before reaching here.
func nearestRankPercentile(sortedAscending []uint64, p int) uint64 {
	n := len(sortedAscending)
	idx := int(math.Ceil(float64(p) / 100 * float64(n)))
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return sortedAscending[idx-1]
}

// ceilBytesToMB converts bytes to MB, rounding up so a footprint is never
// under-reported.
func ceilBytesToMB(b uint64) uint64 {
	return (b + bytesPerMB - 1) / bytesPerMB
}

// clampUint64 bounds v to [lo, hi]. If hi < lo (a misconfigured ceiling
// below the floor), hi is treated as lo — the result can never fall below
// the floor no matter how the ceiling is misconfigured.
func clampUint64(v, lo, hi uint64) uint64 {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
