package assurance

import (
	"encoding/json"
	"strings"
	"testing"
)

// HealthyExample contract: SignalStatusHealthy, Confidence == 1.0,
// no reasons, evidence pointer set. JSON must include the stable
// field names.
func TestHealthyExample_ShapeAndJSON(t *testing.T) {
	t.Parallel()
	s := HealthyExample()
	if s.EffectiveStatus() != SignalStatusHealthy {
		t.Errorf("EffectiveStatus = %s, want healthy", s.EffectiveStatus())
	}
	if s.EffectiveConfidence() != 1.0 {
		t.Errorf("EffectiveConfidence = %v, want 1.0", s.EffectiveConfidence())
	}
	if len(s.Reasons) != 0 {
		t.Errorf("Reasons = %v, want none", s.Reasons)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{`"type":"quiescence_candidate"`, `"status":"healthy"`, `"confidence":1`, `"evidence":`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON missing %s: %s", want, data)
		}
	}
	if strings.Contains(string(data), `"reasons"`) {
		t.Errorf("healthy example should not emit reasons: %s", data)
	}
}

// DegradedExample contract: SignalStatusDegraded, Confidence in
// (0,1), at least one stable reason code from the registry, an
// ObservedAt pointer.
func TestDegradedExample_ShapeAndJSON(t *testing.T) {
	t.Parallel()
	s := DegradedExample()
	if s.EffectiveStatus() != SignalStatusDegraded {
		t.Errorf("EffectiveStatus = %s, want degraded", s.EffectiveStatus())
	}
	if s.EffectiveConfidence() <= 0 || s.EffectiveConfidence() >= 1 {
		t.Errorf("EffectiveConfidence = %v, want strictly between 0 and 1", s.EffectiveConfidence())
	}
	if len(s.Reasons) == 0 {
		t.Errorf("DegradedExample must list at least one ReasonCode")
	}
	for _, code := range s.Reasons {
		if !KnownReasonCode(code) {
			t.Errorf("reason code %q not in canonical registry", code)
		}
	}
	if s.ObservedAt == nil {
		t.Errorf("DegradedExample must set ObservedAt")
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{`"status":"degraded"`, `"reasons":["provider.rate_limited"]`, `"observed_at":`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON missing %s: %s", want, data)
		}
	}
}

// UnknownExample contract: zero-valued Status, no reasons, no
// confidence — but EffectiveStatus and the marshalled JSON both
// promote to "unknown".
func TestUnknownExample_ShapeAndJSON(t *testing.T) {
	t.Parallel()
	s := UnknownExample()
	if s.EffectiveStatus() != SignalStatusUnknown {
		t.Errorf("EffectiveStatus = %s, want unknown", s.EffectiveStatus())
	}
	if s.EffectiveConfidence() != 0 {
		t.Errorf("EffectiveConfidence = %v, want 0", s.EffectiveConfidence())
	}
	if len(s.Reasons) != 0 {
		t.Errorf("UnknownExample should ship no reasons; got %v", s.Reasons)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"status":"unknown"`) {
		t.Errorf("zero-valued Status did not promote to unknown in JSON: %s", data)
	}
	for _, forbid := range []string{`"reasons"`, `"observed_at"`, `"confidence"`} {
		if strings.Contains(string(data), forbid) {
			t.Errorf("unknown example leaked optional field %s: %s", forbid, data)
		}
	}
}

// All three examples must use SignalTypes that appear in the
// canonical AllSignalTypes registry, so docs and runtime stay in
// sync with the contract surface.
func TestExamples_UseRegisteredSignalTypes(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]Signal{
		"healthy":  HealthyExample(),
		"degraded": DegradedExample(),
		"unknown":  UnknownExample(),
	} {
		if !KnownSignalType(s.Type) {
			t.Errorf("%s example uses unregistered SignalType %q", name, s.Type)
		}
	}
}
