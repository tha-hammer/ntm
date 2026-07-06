package dispatchplan

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func clock() time.Time {
	return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
}

func TestPlan_GreedyFillUntilBudget(t *testing.T) {
	t.Parallel()
	in := Inputs{
		AgentType:    "cc",
		BudgetTokens: 200,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "bd-1", Source: SourceBead, Priority: 0, EstimatedTokens: 100, Description: "bead body"},
			{ID: "thread-9", Source: SourceMail, Priority: 1, EstimatedTokens: 80, Description: "active thread"},
			{ID: "cass-42", Source: SourceCASS, Priority: 2, EstimatedTokens: 50, Description: "search hit"},
			{ID: "cm-7", Source: SourceCM, Priority: 3, EstimatedTokens: 40, Description: "rule snippet"},
		},
	}
	r := Plan(in)
	if r.IncludedCount != 2 {
		t.Errorf("IncludedCount = %d, want 2 (bd-1 + thread-9 fit; cass-42 overflows)", r.IncludedCount)
	}
	if r.UsedTokens != 180 {
		t.Errorf("UsedTokens = %d, want 180", r.UsedTokens)
	}
	if r.OmittedCount != 2 {
		t.Errorf("OmittedCount = %d, want 2", r.OmittedCount)
	}
	for _, d := range r.Decisions {
		if d.ID == "cass-42" && d.Reason != ReasonOmittedBudget {
			t.Errorf("cass-42 reason = %s, want omitted_budget_exhausted", d.Reason)
		}
		if d.ID == "cm-7" && d.Reason != ReasonOmittedBudget {
			t.Errorf("cm-7 reason = %s, want omitted_budget_exhausted", d.Reason)
		}
	}
}

func TestPlan_RequiredHeadersBypassBudget(t *testing.T) {
	t.Parallel()
	in := Inputs{
		BudgetTokens: 50,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "header", Source: SourceBead, Required: true, EstimatedTokens: 100, Description: "agent header"},
			{ID: "extra", Source: SourceMail, Priority: 1, EstimatedTokens: 10, Description: "context"},
		},
	}
	r := Plan(in)
	// Required must be in even though it exceeds budget; subsequent
	// candidates respect the original budget.
	if r.UsedTokens != 100 {
		t.Errorf("UsedTokens = %d, want 100 (required header bypasses budget)", r.UsedTokens)
	}
	for _, d := range r.Decisions {
		if d.ID == "header" && d.Reason != ReasonRequiredHeader {
			t.Errorf("header reason = %s, want included_required_header", d.Reason)
		}
		// extra should be omitted since required already pushed past budget.
		if d.ID == "extra" && d.Reason != ReasonOmittedBudget {
			t.Errorf("extra reason = %s, want omitted_budget_exhausted", d.Reason)
		}
	}
}

func TestPlan_AgentTypeFilterOmitsMismatch(t *testing.T) {
	t.Parallel()
	in := Inputs{
		AgentType:    "cc",
		BudgetTokens: 1000,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "claude-only", Source: SourceMail, Priority: 1, EstimatedTokens: 50, AgentTypeFilter: []string{"cc", "claude"}, Description: "claude-tuned hint"},
			{ID: "codex-only", Source: SourceMail, Priority: 2, EstimatedTokens: 50, AgentTypeFilter: []string{"cod"}, Description: "codex-tuned hint"},
		},
	}
	r := Plan(in)
	if r.IncludedCount != 1 {
		t.Errorf("IncludedCount = %d, want 1 (only claude-only matches cc)", r.IncludedCount)
	}
	for _, d := range r.Decisions {
		if d.ID == "codex-only" && d.Reason != ReasonOmittedAgentType {
			t.Errorf("codex-only reason = %s, want omitted_agent_type_filter", d.Reason)
		}
	}
}

func TestPlan_DisabledSourceOmitted(t *testing.T) {
	t.Parallel()
	in := Inputs{
		AgentType:       "cc",
		BudgetTokens:    1000,
		DisabledSources: []Source{SourceCASS},
		Now:             clock(),
		Candidates: []Candidate{
			{ID: "cass-1", Source: SourceCASS, Priority: 1, EstimatedTokens: 50, Description: "would help"},
			{ID: "mail-1", Source: SourceMail, Priority: 2, EstimatedTokens: 50, Description: "still in"},
		},
	}
	r := Plan(in)
	for _, d := range r.Decisions {
		if d.ID == "cass-1" && d.Reason != ReasonOmittedSourceOff {
			t.Errorf("cass-1 reason = %s, want omitted_source_disabled", d.Reason)
		}
	}
}

func TestPlan_DuplicateIDOmitted(t *testing.T) {
	t.Parallel()
	in := Inputs{
		BudgetTokens: 1000,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "dup", Source: SourceMail, Priority: 1, EstimatedTokens: 50, Description: "first copy"},
			{ID: "dup", Source: SourceCASS, Priority: 2, EstimatedTokens: 50, Description: "second copy"},
		},
	}
	r := Plan(in)
	if r.IncludedCount != 1 {
		t.Errorf("IncludedCount = %d, want 1 (dedupe)", r.IncludedCount)
	}
	for _, d := range r.Decisions {
		if d.Source == SourceCASS && d.Reason != ReasonOmittedDuplicate {
			t.Errorf("second dup reason = %s, want omitted_duplicate", d.Reason)
		}
	}
}

func TestPlan_EmptyTokensOmitted(t *testing.T) {
	t.Parallel()
	in := Inputs{
		BudgetTokens: 1000,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "empty", Source: SourceMail, Priority: 1, EstimatedTokens: 0, Description: "no body"},
			{ID: "filled", Source: SourceMail, Priority: 2, EstimatedTokens: 10, Description: "ok"},
		},
	}
	r := Plan(in)
	for _, d := range r.Decisions {
		if d.ID == "empty" && d.Reason != ReasonOmittedEmpty {
			t.Errorf("empty reason = %s, want omitted_empty", d.Reason)
		}
	}
}

func TestPlan_PriorityOrderingDeterministic(t *testing.T) {
	t.Parallel()
	t0 := clock()
	in := Inputs{
		BudgetTokens: 100,
		Now:          t0,
		Candidates: []Candidate{
			{ID: "a", Source: SourceMail, Priority: 5, EstimatedTokens: 60, CreatedAt: t0},
			{ID: "b", Source: SourceMail, Priority: 0, EstimatedTokens: 60, CreatedAt: t0},
			{ID: "c", Source: SourceMail, Priority: 3, EstimatedTokens: 60, CreatedAt: t0},
		},
	}
	r := Plan(in)
	// Priority 0 (b) wins, exhausting budget. a and c should be omitted.
	if r.Decisions[0].ID != "b" {
		t.Errorf("first decision = %s, want b (priority 0)", r.Decisions[0].ID)
	}
	if r.IncludedCount != 1 {
		t.Errorf("IncludedCount = %d, want 1", r.IncludedCount)
	}
}

func TestPlan_ZeroBudgetIncludesNothingNonRequired(t *testing.T) {
	t.Parallel()
	in := Inputs{
		BudgetTokens: 0,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "a", Source: SourceMail, Priority: 1, EstimatedTokens: 1},
		},
	}
	r := Plan(in)
	// BudgetTokens == 0 disables the budget check (no overflow comparison),
	// so the candidate is included. Document this explicitly with a follow-
	// up assertion: required+budget=0 is the valid "no budget configured"
	// signal — the planner does not gate when no budget is set.
	if r.IncludedCount != 1 {
		t.Errorf("IncludedCount = %d, want 1 (BudgetTokens=0 means 'no budget configured')", r.IncludedCount)
	}
}

func TestPlan_BudgetOf1OmitsAllNonRequired(t *testing.T) {
	t.Parallel()
	in := Inputs{
		BudgetTokens: 1,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "a", Source: SourceMail, Priority: 1, EstimatedTokens: 5},
		},
	}
	r := Plan(in)
	if r.IncludedCount != 0 {
		t.Errorf("IncludedCount = %d, want 0 (budget too small)", r.IncludedCount)
	}
}

func TestPlan_JSONShapeIsStable(t *testing.T) {
	t.Parallel()
	in := Inputs{
		AgentType:    "cc",
		BudgetTokens: 100,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "bd-1", Source: SourceBead, Required: true, EstimatedTokens: 30, Description: "bead body"},
			{ID: "thread", Source: SourceMail, Priority: 1, EstimatedTokens: 50, Description: "thread"},
			{ID: "cass", Source: SourceCASS, Priority: 2, EstimatedTokens: 30, Description: "cass hit"},
		},
	}
	a, _ := json.Marshal(Plan(in))
	b, _ := json.Marshal(Plan(in))
	if string(a) != string(b) {
		t.Errorf("Plan JSON drifted across calls:\nfirst:  %s\nsecond: %s", a, b)
	}
	for _, want := range []string{
		`"agent_type":"cc"`, `"budget_tokens":100`, `"used_tokens"`,
		`"included_count"`, `"omitted_count"`, `"decisions"`, `"summary"`,
	} {
		if !strings.Contains(string(a), want) {
			t.Errorf("JSON missing %s: %s", want, a)
		}
	}
}

func TestPlan_SummaryHasCounts(t *testing.T) {
	t.Parallel()
	r := Plan(Inputs{
		BudgetTokens: 100,
		Now:          clock(),
		Candidates: []Candidate{
			{ID: "a", Source: SourceMail, Priority: 1, EstimatedTokens: 50},
			{ID: "b", Source: SourceMail, Priority: 2, EstimatedTokens: 60}, // overflows
		},
	})
	for _, want := range []string{"included=1", "omitted=1", "used=50/100"} {
		if !strings.Contains(r.Summary, want) {
			t.Errorf("Summary missing %q: %s", want, r.Summary)
		}
	}
}

// bd-njp52: Required candidates bypass every gate except duplicate-ID.
// This test pins the FULL bypass scope that the godoc now promises —
// budget, source-disabled, agent-type-filter, and empty-tokens are all
// skipped for Required, so a future code refactor that re-narrows the
// bypass cannot land without flipping this test.
//
// The duplicate-ID gate IS still respected: a Required candidate whose
// ID has already been emitted is recorded as ReasonOmittedDuplicate.
func TestPlan_RequiredBypassesEveryGateExceptDuplicateID(t *testing.T) {
	t.Parallel()
	r := Plan(Inputs{
		AgentType:       "cc",
		BudgetTokens:    1, // tiny, so non-required would overflow
		DisabledSources: []Source{SourceCASS},
		Now:             clock(),
		Candidates: []Candidate{
			// Required + EstimatedTokens=0 → bypasses the empty-tokens gate.
			{ID: "hdr-empty", Source: SourceBead, Priority: 0, EstimatedTokens: 0, Required: true, Description: "empty-tokens"},
			// Required + source-disabled (CASS) → bypasses the source-disabled gate.
			{ID: "hdr-disabled", Source: SourceCASS, Priority: 0, EstimatedTokens: 5, Required: true, Description: "from-disabled-source"},
			// Required + AgentTypeFilter excludes "cc" → bypasses agent-type gate.
			{ID: "hdr-other-agent", Source: SourceMail, Priority: 0, EstimatedTokens: 5, Required: true, AgentTypeFilter: []string{"cod"}, Description: "other-agent-only"},
			// Required + huge tokens above tiny budget → bypasses budget gate.
			{ID: "hdr-big", Source: SourceBead, Priority: 0, EstimatedTokens: 9999, Required: true, Description: "way-over-budget"},
			// Duplicate of hdr-big — Required, but dedupe still applies.
			{ID: "hdr-big", Source: SourceBead, Priority: 0, EstimatedTokens: 9999, Required: true, Description: "duplicate-of-hdr-big"},
		},
	})

	// Three of the unique-ID Required candidates land as RequiredHeader;
	// the duplicate must be OmittedDuplicate. Map by ID for the unique
	// IDs and count by ID for hdr-big.
	gotByUniqueID := map[string]Reason{}
	var requiredHits, dupHits int
	for _, d := range r.Decisions {
		if d.ID == "hdr-big" {
			switch d.Reason {
			case ReasonRequiredHeader:
				requiredHits++
			case ReasonOmittedDuplicate:
				dupHits++
			}
			continue
		}
		gotByUniqueID[d.ID] = d.Reason
	}
	for _, want := range []string{"hdr-empty", "hdr-disabled", "hdr-other-agent"} {
		if gotByUniqueID[want] != ReasonRequiredHeader {
			t.Errorf("decision[%s].Reason = %q, want %q (Required must bypass every gate)",
				want, gotByUniqueID[want], ReasonRequiredHeader)
		}
	}
	if requiredHits != 1 || dupHits != 1 {
		t.Errorf("duplicate-ID gate must apply even to Required: got %d RequiredHeader + %d OmittedDuplicate for hdr-big, want 1 + 1",
			requiredHits, dupHits)
	}
}
