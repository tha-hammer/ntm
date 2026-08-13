package pressure

import (
	"math"
	"testing"
)

func mbSamples(mbValues ...uint64) []MemorySample {
	samples := make([]MemorySample, len(mbValues))
	for i, mb := range mbValues {
		samples[i] = MemorySample{PeakBytes: mb * bytesPerMB}
	}
	return samples
}

func wantConfidence(n int) float64 {
	if n <= 0 {
		return 0.1
	}
	return math.Min(1, math.Log1p(float64(n))/math.Log1p(100))
}

func TestEstimateAgentMemMB(t *testing.T) {
	t.Parallel()

	const defaultFloorMB = 2048 // the tactical fix's shipped defaultPerAgentExpectedMemMB
	const defaultCeilingMB = 16384

	t.Run("zero samples is fallback at exactly the floor (cold-start non-regression)", func(t *testing.T) {
		t.Parallel()
		mb, source, confidence := EstimateAgentMemMB(nil, defaultFloorMB, defaultCeilingMB)
		if mb != defaultFloorMB {
			t.Errorf("mb = %d, want %d (byte-for-byte match with the shipped tactical-fix constant)", mb, defaultFloorMB)
		}
		if source != MemoryEstimateFallback {
			t.Errorf("source = %q, want %q", source, MemoryEstimateFallback)
		}
		if confidence != wantConfidence(0) {
			t.Errorf("confidence = %v, want %v", confidence, wantConfidence(0))
		}
	})

	t.Run("n=1 below confidence gate is fallback regardless of sample content", func(t *testing.T) {
		t.Parallel()
		samples := mbSamples(999999) // a huge peak must not leak through below the gate
		mb, source, confidence := EstimateAgentMemMB(samples, defaultFloorMB, defaultCeilingMB)
		if mb != defaultFloorMB || source != MemoryEstimateFallback {
			t.Errorf("mb=%d source=%q, want mb=%d source=%q", mb, source, defaultFloorMB, MemoryEstimateFallback)
		}
		if confidence != wantConfidence(1) {
			t.Errorf("confidence = %v, want %v", confidence, wantConfidence(1))
		}
	})

	t.Run("n=4 is still below the confidence gate (0.3487 < 0.35), fallback", func(t *testing.T) {
		t.Parallel()
		samples := mbSamples(999999, 999999, 999999, 999999)
		mb, source, confidence := EstimateAgentMemMB(samples, defaultFloorMB, defaultCeilingMB)
		if mb != defaultFloorMB || source != MemoryEstimateFallback {
			t.Errorf("mb=%d source=%q, want mb=%d source=%q", mb, source, defaultFloorMB, MemoryEstimateFallback)
		}
		if confidence != wantConfidence(4) {
			t.Errorf("confidence = %v, want %v", confidence, wantConfidence(4))
		}
		if confidence >= defaultMinCalibrationConfidence {
			t.Fatalf("test premise violated: confidence(4) = %v is not below the gate %v", confidence, defaultMinCalibrationConfidence)
		}
	})

	t.Run("n=5 crosses the confidence gate (0.3882 >= 0.35), calibrated with exact p90/margin math", func(t *testing.T) {
		t.Parallel()
		// idx = ceil(90/100*5) = 5 -> largest value (3000MB) is the p90.
		samples := mbSamples(2000, 2200, 2400, 2600, 3000)
		mb, source, confidence := EstimateAgentMemMB(samples, defaultFloorMB, defaultCeilingMB)
		wantMB := uint64(3000 * memoryEstimateSafetyMarginNum / memoryEstimateSafetyMarginDen) // 3750
		if mb != wantMB || source != MemoryEstimateCalibrated {
			t.Errorf("mb=%d source=%q, want mb=%d source=%q", mb, source, wantMB, MemoryEstimateCalibrated)
		}
		if confidence != wantConfidence(5) {
			t.Errorf("confidence = %v, want %v", confidence, wantConfidence(5))
		}
		if confidence < defaultMinCalibrationConfidence {
			t.Fatalf("test premise violated: confidence(5) = %v is not >= the gate %v", confidence, defaultMinCalibrationConfidence)
		}
	})

	t.Run("n=11 exercises a non-trivial nearest-rank index", func(t *testing.T) {
		t.Parallel()
		// 1000..2000 MB step 100 -> 11 values. idx = ceil(90/100*11) = 10 ->
		// the 10th smallest (1-indexed) = 1900MB.
		samples := mbSamples(1000, 1100, 1200, 1300, 1400, 1500, 1600, 1700, 1800, 1900, 2000)
		mb, source, _ := EstimateAgentMemMB(samples, defaultFloorMB, defaultCeilingMB)
		wantMB := uint64(1900 * memoryEstimateSafetyMarginNum / memoryEstimateSafetyMarginDen) // 2375
		if mb != wantMB || source != MemoryEstimateCalibrated {
			t.Errorf("mb=%d source=%q, want mb=%d source=%q", mb, source, wantMB, MemoryEstimateCalibrated)
		}
	})

	t.Run("n=100 calibrated math can still clamp down to the floor (source stays calibrated, not fallback)", func(t *testing.T) {
		t.Parallel()
		// Values 1..100 MB. idx = ceil(90/100*100) = 90 -> 90th smallest = 90MB.
		// 90 * 5/4 = 112 (integer division), which clamps up to the 2048 floor —
		// distinct from the confidence-gate fallback path, which never runs
		// the p90/margin math at all.
		mbValues := make([]uint64, 100)
		for i := range mbValues {
			mbValues[i] = uint64(i + 1)
		}
		samples := mbSamples(mbValues...)
		mb, source, confidence := EstimateAgentMemMB(samples, defaultFloorMB, defaultCeilingMB)
		if mb != defaultFloorMB {
			t.Errorf("mb = %d, want %d (clamped up to the floor)", mb, defaultFloorMB)
		}
		if source != MemoryEstimateCalibrated {
			t.Errorf("source = %q, want %q (clamped-to-floor is still a calibrated result, not a fallback one)", source, MemoryEstimateCalibrated)
		}
		if confidence != wantConfidence(100) {
			t.Errorf("confidence = %v, want %v", confidence, wantConfidence(100))
		}
	})

	t.Run("a low ceiling clamps the calibrated value down without changing its source", func(t *testing.T) {
		t.Parallel()
		samples := mbSamples(5000, 5000, 5000, 5000, 5000) // p90=5000, scaled=6250
		const lowCeilingMB = 3000
		mb, source, _ := EstimateAgentMemMB(samples, defaultFloorMB, lowCeilingMB)
		if mb != lowCeilingMB {
			t.Errorf("mb = %d, want %d (ceiling-bound)", mb, lowCeilingMB)
		}
		if source != MemoryEstimateCalibrated {
			t.Errorf("source = %q, want %q", source, MemoryEstimateCalibrated)
		}
	})

	t.Run("a ceiling below the floor is rejected: the result never falls below the floor", func(t *testing.T) {
		t.Parallel()
		samples := mbSamples(5000, 5000, 5000, 5000, 5000)
		const invalidCeilingMB = 1000 // below defaultFloorMB
		mb, source, _ := EstimateAgentMemMB(samples, defaultFloorMB, invalidCeilingMB)
		if mb != defaultFloorMB {
			t.Errorf("mb = %d, want %d (an invalid ceiling must never push the result below the floor)", mb, defaultFloorMB)
		}
		if source != MemoryEstimateCalibrated {
			t.Errorf("source = %q, want %q (the confidence gate alone determines source)", source, MemoryEstimateCalibrated)
		}
	})
}

func TestNearestRankPercentile(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		values []uint64
		p      int
		want   uint64
	}{
		{"single value", []uint64{42}, 90, 42},
		{"n=5 p90 picks the max (idx=5)", []uint64{10, 20, 30, 40, 50}, 90, 50},
		{"n=11 p90 picks idx=10", []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, 90, 10},
		{"n=100 p90 picks idx=90", func() []uint64 {
			v := make([]uint64, 100)
			for i := range v {
				v[i] = uint64(i + 1)
			}
			return v
		}(), 90, 90},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := nearestRankPercentile(tc.values, tc.p); got != tc.want {
				t.Errorf("nearestRankPercentile(%v, %d) = %d, want %d", tc.values, tc.p, got, tc.want)
			}
		})
	}
}

func TestClampUint64(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		v, lo, hi uint64
		want      uint64
	}{
		{"within range", 50, 10, 100, 50},
		{"below floor", 5, 10, 100, 10},
		{"above ceiling", 500, 10, 100, 100},
		{"invalid ceiling below floor still respects floor", 500, 100, 10, 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := clampUint64(tc.v, tc.lo, tc.hi); got != tc.want {
				t.Errorf("clampUint64(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
			}
		})
	}
}
