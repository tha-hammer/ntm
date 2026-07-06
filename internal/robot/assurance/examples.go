package assurance

import "time"

// HealthyExample returns the canonical "evidence supports operator
// confidence" Signal: Status=healthy, Confidence=1.0, no reasons,
// optional Evidence pointer. Use this in docs/tests as the baseline
// shape downstream consumers should expect for healthy paths.
func HealthyExample() Signal {
	return Signal{
		Type:       SignalQuiescenceCandidate,
		Status:     SignalStatusHealthy,
		Confidence: 1.0,
		Evidence:   "no in-progress beads, no urgent mail, tracker in_sync",
	}
}

// DegradedExample returns the canonical "evidence supports lowered
// operator confidence" Signal. The chosen Type + ReasonCodes
// demonstrate how a real degraded report ties a Status to specific
// reason codes the consumer can route on.
func DegradedExample() Signal {
	t := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	return Signal{
		Type:       SignalProviderDegraded,
		Status:     SignalStatusDegraded,
		Confidence: 0.7,
		Reasons: []ReasonCode{
			ReasonProviderRateLimited,
		},
		Evidence:   "anthropic api 429 rate-limit observed in last 5 min",
		ObservedAt: &t,
	}
}

// UnknownExample returns the canonical "insufficient evidence" Signal.
// The Status zero value (empty string) is the contract for unknown;
// EffectiveStatus() converts it to SignalStatusUnknown for callers
// that need a non-empty token.
func UnknownExample() Signal {
	return Signal{
		Type: SignalEvidenceFreshness,
		// Status intentionally zero-valued — exercise the zero-value
		// contract that maps to SignalStatusUnknown via EffectiveStatus
		// and JSON marshalling.
	}
}
