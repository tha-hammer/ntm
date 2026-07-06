package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// calibrationFixture is one labeled scrollback fixture loaded from
// testdata/calibration/manifest.json. Each fixture pins the expected
// classifier verdict so future tuning surfaces drift as a failed
// classification rather than a quietly accepted regression.
type calibrationFixture struct {
	ID        string                 `json:"id"`
	Category  string                 `json:"category"`
	AgentType string                 `json:"agent_type"`
	File      string                 `json:"file"`
	Expect    calibrationExpectation `json:"expect"`
	Notes     string                 `json:"notes,omitempty"`
}

type calibrationExpectation struct {
	AtPrompt    bool `json:"at_prompt"`
	RateLimited bool `json:"rate_limited"`
	Crashed     bool `json:"crashed"`
}

type calibrationManifest struct {
	SchemaVersion int                  `json:"schema_version"`
	Description   string               `json:"description"`
	Fixtures      []calibrationFixture `json:"fixtures"`
}

// classifyCalibration runs the production detectors against a fixture
// and returns the observed verdict. Keep this aligned with the call
// sequence in checkAgent so the calibration mirrors real classification.
func classifyCalibration(output, agentType string) calibrationExpectation {
	_, hasPrompt := outputAfterMostRecentPrompt(output, agentType)
	rl := detectRateLimit(output, agentType)
	issues := detectErrorsForAgent(output, agentType)
	return calibrationExpectation{
		AtPrompt:    hasPrompt,
		RateLimited: rl.RateLimited,
		Crashed:     hasIssueType(issues, "crash"),
	}
}

func loadCalibrationManifest(t *testing.T) calibrationManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "calibration", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m calibrationManifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(m.Fixtures) == 0 {
		t.Fatal("manifest has zero fixtures")
	}
	return m
}

// scoreReport accumulates per-class confusion counts so the test log
// surfaces a calibration table even on the green path. Each predicate
// is scored independently against its expected label.
type scoreReport struct {
	tp, fp, tn, fn int
}

func (s *scoreReport) record(want, got bool) {
	switch {
	case want && got:
		s.tp++
	case !want && got:
		s.fp++
	case want && !got:
		s.fn++
	default:
		s.tn++
	}
}

// TestClassifierCalibration_Corpus runs the labeled corpus through the
// production health detectors and reports per-predicate false-positive
// and false-negative counts. The test fails when ANY fixture's
// observed verdict diverges from its expected label — the score table
// is logged regardless so reviewers can see the full distribution.
//
// To extend: drop a new .txt fixture under testdata/calibration/<cat>/
// and append a manifest entry; the test picks it up automatically.
func TestClassifierCalibration_Corpus(t *testing.T) {
	manifest := loadCalibrationManifest(t)

	scores := map[string]*scoreReport{
		"at_prompt":    {},
		"rate_limited": {},
		"crashed":      {},
	}
	byCategory := make(map[string]*scoreReport)
	categoryAdd := func(cat string) *scoreReport {
		s, ok := byCategory[cat]
		if !ok {
			s = &scoreReport{}
			byCategory[cat] = s
		}
		return s
	}

	mismatches := 0
	for _, f := range manifest.Fixtures {
		t.Run(f.ID, func(t *testing.T) {
			path := filepath.Join("testdata", "calibration", f.File)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read fixture %s: %v", path, err)
			}
			got := classifyCalibration(string(data), f.AgentType)

			scores["at_prompt"].record(f.Expect.AtPrompt, got.AtPrompt)
			scores["rate_limited"].record(f.Expect.RateLimited, got.RateLimited)
			scores["crashed"].record(f.Expect.Crashed, got.Crashed)
			cat := categoryAdd(f.Category)
			cat.record(f.Expect.AtPrompt, got.AtPrompt)
			cat.record(f.Expect.RateLimited, got.RateLimited)
			cat.record(f.Expect.Crashed, got.Crashed)

			if got.AtPrompt != f.Expect.AtPrompt {
				mismatches++
				t.Errorf("at_prompt: got=%v want=%v", got.AtPrompt, f.Expect.AtPrompt)
			}
			if got.RateLimited != f.Expect.RateLimited {
				mismatches++
				t.Errorf("rate_limited: got=%v want=%v", got.RateLimited, f.Expect.RateLimited)
			}
			if got.Crashed != f.Expect.Crashed {
				mismatches++
				t.Errorf("crashed: got=%v want=%v", got.Crashed, f.Expect.Crashed)
			}
		})
	}

	// Per-predicate score table.
	t.Logf("calibration score report (%d fixtures across %d categories):",
		len(manifest.Fixtures), len(byCategory))
	predicates := make([]string, 0, len(scores))
	for k := range scores {
		predicates = append(predicates, k)
	}
	sort.Strings(predicates)
	for _, p := range predicates {
		s := scores[p]
		t.Logf("  %-14s tp=%d fp=%d fn=%d tn=%d", p, s.tp, s.fp, s.fn, s.tn)
	}

	// Per-category aggregate (all 3 predicates folded together).
	cats := make([]string, 0, len(byCategory))
	for k := range byCategory {
		cats = append(cats, k)
	}
	sort.Strings(cats)
	t.Logf("category rollup (predicate-agnostic):")
	for _, c := range cats {
		s := byCategory[c]
		t.Logf("  %-26s tp=%d fp=%d fn=%d tn=%d", c, s.tp, s.fp, s.fn, s.tn)
	}
}
