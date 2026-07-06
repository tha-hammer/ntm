package driftaudit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

// alignedSet is the four-surface fixture the operator expects in
// production: every command appears in every surface.
func alignedSet() Inputs {
	names := []string{"robot-status", "robot-send", "robot-tail"}
	return Inputs{
		Capabilities: SurfaceSet{Surface: SurfaceCapabilities, Names: names},
		Help:         SurfaceSet{Surface: SurfaceHelp, Names: names},
		Docs:         SurfaceSet{Surface: SurfaceDocs, Names: names},
		Contract:     SurfaceSet{Surface: SurfaceContract, Names: names},
		Now:          clock(),
	}
}

func TestCompare_AlignedSurfacesProduceNoDrift(t *testing.T) {
	t.Parallel()
	r := Compare(alignedSet())
	if r.Drift != 0 {
		t.Errorf("Drift = %d, want 0; findings = %+v", r.Drift, r.Findings)
	}
	if r.Total != 3 {
		t.Errorf("Total = %d, want 3", r.Total)
	}
}

// bd-i5da4: SurfaceSet.Surface, when set, must match the slot the set
// is passed into. A mismatch surfaces a critical _mislabeled_set:<slot>
// finding. Empty Surface (the existing default in many test fixtures)
// is silently accepted so older code paths stay valid.
func TestCompare_MislabeledSurfaceSetEmitsCriticalFinding(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	// Caller put a Help-labeled set into the Capabilities slot.
	in.Capabilities = SurfaceSet{Surface: SurfaceHelp, Names: []string{"robot-status", "robot-send", "robot-tail"}}

	r := Compare(in)
	var mislabel *Finding
	for i, f := range r.Findings {
		if f.Name == "_mislabeled_set:capabilities" {
			mislabel = &r.Findings[i]
			break
		}
	}
	if mislabel == nil {
		t.Fatalf("expected _mislabeled_set:capabilities finding, got: %+v", r.Findings)
	}
	if mislabel.Severity != SeverityCritical {
		t.Errorf("Severity = %s, want critical", mislabel.Severity)
	}
	if len(mislabel.Present) != 1 || mislabel.Present[0] != SurfaceHelp {
		t.Errorf("Present = %v, want [help] (the observed mislabel)", mislabel.Present)
	}
	if len(mislabel.Missing) != 1 || mislabel.Missing[0] != SurfaceCapabilities {
		t.Errorf("Missing = %v, want [capabilities] (the slot it should have been)", mislabel.Missing)
	}
}

// The slot's data still flows through normal classification — the
// caller's wiring bug is announced, not silently dropped, so other
// findings against the audit corpus remain visible.
func TestCompare_MislabeledSurfaceDataStillFlowsThroughClassification(t *testing.T) {
	t.Parallel()
	in := Inputs{
		// caps slot mislabeled as Help, but its data ("robot-status")
		// still ends up in the capabilities map, so an aligned Help
		// slot containing the same name produces no drift on that name.
		Capabilities: SurfaceSet{Surface: SurfaceHelp, Names: []string{"robot-status"}},
		Help:         SurfaceSet{Surface: SurfaceHelp, Names: []string{"robot-status"}},
		Docs:         SurfaceSet{Surface: SurfaceDocs, Names: []string{"robot-status"}},
		Contract:     SurfaceSet{Surface: SurfaceContract, Names: []string{"robot-status"}},
		Now:          clock(),
	}
	r := Compare(in)
	// One critical mislabel finding; no per-name findings (robot-status
	// is everywhere).
	for _, f := range r.Findings {
		if f.Name == "robot-status" {
			t.Errorf("robot-status should not appear as drift when present in every slot map: %+v", f)
		}
	}
	if !findingExists(r.Findings, "_mislabeled_set:capabilities") {
		t.Errorf("expected mislabel finding for capabilities slot: %+v", r.Findings)
	}
}

// Empty SurfaceSet.Surface (the existing default in many fixtures)
// must not trigger a mislabeled finding — the field is opt-in.
func TestCompare_EmptySurfaceFieldIsSilentlyAccepted(t *testing.T) {
	t.Parallel()
	in := Inputs{
		// Surface field unset everywhere — pre-bd-i5da4 fixtures looked
		// exactly like this and must continue to work.
		Capabilities: SurfaceSet{Names: []string{"robot-status"}},
		Help:         SurfaceSet{Names: []string{"robot-status"}},
		Docs:         SurfaceSet{Names: []string{"robot-status"}},
		Contract:     SurfaceSet{Names: []string{"robot-status"}},
		Now:          clock(),
	}
	r := Compare(in)
	for _, f := range r.Findings {
		if strings.HasPrefix(f.Name, "_mislabeled_set:") {
			t.Errorf("empty Surface must not trigger mislabel: got %+v", f)
		}
	}
}

// Multiple mislabels at once each surface their own critical finding.
func TestCompare_MultipleMislabelsAreEachReported(t *testing.T) {
	t.Parallel()
	in := Inputs{
		Capabilities: SurfaceSet{Surface: SurfaceHelp, Names: []string{"x"}},
		Help:         SurfaceSet{Surface: SurfaceCapabilities, Names: []string{"x"}},
		Docs:         SurfaceSet{Surface: SurfaceDocs, Names: []string{"x"}},
		Contract:     SurfaceSet{Surface: SurfaceContract, Names: []string{"x"}},
		Now:          clock(),
	}
	r := Compare(in)
	if !findingExists(r.Findings, "_mislabeled_set:capabilities") {
		t.Errorf("missing capabilities mislabel: %+v", r.Findings)
	}
	if !findingExists(r.Findings, "_mislabeled_set:help") {
		t.Errorf("missing help mislabel: %+v", r.Findings)
	}
	if findingExists(r.Findings, "_mislabeled_set:docs") {
		t.Errorf("docs slot is correctly labeled, should not have mislabel: %+v", r.Findings)
	}
}

func findingExists(findings []Finding, name string) bool {
	for _, f := range findings {
		if f.Name == name {
			return true
		}
	}
	return false
}

// Acceptance criterion: "a focused test or check command that fails
// on an intentionally missing sample in test fixtures."
func TestCompare_IntentionallyMissingFromCapabilitiesIsCritical(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	in.Capabilities.Names = []string{"robot-status", "robot-send"} // robot-tail missing
	r := Compare(in)
	if r.Drift == 0 {
		t.Fatalf("expected drift findings; got none")
	}
	found := false
	for _, f := range r.Findings {
		if f.Name == "robot-tail" {
			found = true
			if f.Severity != SeverityCritical {
				t.Errorf("severity = %s, want critical for capabilities-missing", f.Severity)
			}
			if !sliceHasSurface(f.Missing, SurfaceCapabilities) {
				t.Errorf("Missing = %v, want capabilities", f.Missing)
			}
		}
	}
	if !found {
		t.Errorf("robot-tail finding missing: %+v", r.Findings)
	}
}

func TestCompare_MissingFromDocsIsWarning(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	in.Docs.Names = []string{"robot-status", "robot-send"}
	r := Compare(in)
	hasWarning := false
	for _, f := range r.Findings {
		if f.Name == "robot-tail" && f.Severity == SeverityWarning {
			hasWarning = true
		}
	}
	if !hasWarning {
		t.Errorf("missing-from-docs did not produce a warning: %+v", r.Findings)
	}
}

func TestCompare_MissingOnlyFromContractIsInfo(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	in.Contract.Names = []string{"robot-status", "robot-send"}
	r := Compare(in)
	hasInfo := false
	for _, f := range r.Findings {
		if f.Name == "robot-tail" {
			if f.Severity != SeverityInfo {
				t.Errorf("severity = %s, want info", f.Severity)
			}
			hasInfo = true
		}
	}
	if !hasInfo {
		t.Errorf("missing-from-contract did not produce an info finding: %+v", r.Findings)
	}
}

func TestCompare_MultipleMissingTakesHighestSeverity(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	// robot-tail missing from capabilities AND docs; the worse
	// (capabilities) severity wins.
	in.Capabilities.Names = []string{"robot-status", "robot-send"}
	in.Docs.Names = []string{"robot-status", "robot-send"}
	r := Compare(in)
	for _, f := range r.Findings {
		if f.Name == "robot-tail" {
			if f.Severity != SeverityCritical {
				t.Errorf("severity = %s, want critical (worst missing)", f.Severity)
			}
		}
	}
}

func TestCompare_IgnoredNamesAreSkippedEntirely(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	in.Capabilities.Names = []string{"robot-status", "robot-send"}
	in.IgnoredNames = []string{"robot-tail"}
	r := Compare(in)
	for _, f := range r.Findings {
		if f.Name == "robot-tail" {
			t.Errorf("ignored name surfaced as finding: %+v", f)
		}
	}
}

func TestCompare_NormalizesWhitespaceAndDoubleDashPrefix(t *testing.T) {
	t.Parallel()
	r := Compare(Inputs{
		Capabilities: SurfaceSet{Names: []string{"robot-status"}},
		Help:         SurfaceSet{Names: []string{"--robot-status"}},
		Docs:         SurfaceSet{Names: []string{"  Robot-Status  "}},
		Contract:     SurfaceSet{Names: []string{"robot-status"}},
		Now:          clock(),
	})
	if r.Drift != 0 {
		t.Errorf("normalization did not match: drift=%d findings=%+v", r.Drift, r.Findings)
	}
}

func TestCompare_FindingsSortedBySeverityThenName(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	// robot-tail missing from capabilities (critical),
	// robot-zebra missing only from contract (info — but sort puts critical first).
	in.Capabilities.Names = []string{"robot-status", "robot-send"}
	in.Help.Names = append(in.Help.Names, "robot-zebra")
	in.Docs.Names = append(in.Docs.Names, "robot-zebra")
	in.Capabilities.Names = append(in.Capabilities.Names, "robot-zebra")
	r := Compare(in)
	if len(r.Findings) < 2 {
		t.Fatalf("findings = %d, want >=2", len(r.Findings))
	}
	for i := 1; i < len(r.Findings); i++ {
		ri := severityRank(r.Findings[i-1].Severity)
		rj := severityRank(r.Findings[i].Severity)
		if rj > ri {
			t.Errorf("findings out of order at %d: %s after %s",
				i, r.Findings[i].Severity, r.Findings[i-1].Severity)
		}
		if rj == ri && r.Findings[i].Name < r.Findings[i-1].Name {
			t.Errorf("findings out of order by name: %s after %s",
				r.Findings[i].Name, r.Findings[i-1].Name)
		}
	}
}

func TestCompare_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := alignedSet()
	in.Capabilities.Names = []string{"robot-status"} // produces drift for tail+send
	a, err := json.Marshal(Compare(in))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	b, err := json.Marshal(Compare(in))
	if err != nil {
		t.Fatalf("Marshal twice: %v", err)
	}
	if string(a) != string(b) {
		t.Errorf("JSON drifted between Compare calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{
		`"surfaces"`, `"findings"`, `"drift"`, `"total"`,
		`"surface":"capabilities"`, `"surface":"help"`,
		`"surface":"docs"`, `"surface":"contract"`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
}

func TestPin_RoundTrips(t *testing.T) {
	t.Parallel()
	r := Compare(alignedSet())
	pinned, err := r.Pin()
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(pinned), &v); err != nil {
		t.Errorf("pinned snapshot did not parse: %v", err)
	}
}

func sliceHasSurface(slice []Surface, want Surface) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
