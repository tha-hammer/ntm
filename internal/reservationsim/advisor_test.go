package reservationsim

import (
	"strings"
	"testing"
	"time"
)

func TestAdviseReservations_StaleBroadAndInactiveHolder(t *testing.T) {
	t.Parallel()
	now := anchor().Add(4 * time.Hour)
	report := AdviseReservations([]ReservationRiskInput{
		{
			ID:          7,
			PathPattern: "**",
			AgentName:   "BlueLake",
			Exclusive:   true,
			Reason:      "bd-stale",
			CreatedAt:   anchor(),
			ExpiresAt:   now.Add(10 * time.Minute),
		},
	}, ReservationAdvisorOptions{
		Now: now,
		HolderLastActive: map[string]time.Time{
			"BlueLake": anchor().Add(30 * time.Minute),
		},
		StaleInProgressByReason: map[string]bool{"bd-stale": true},
	})

	if !report.AgentMailAvailable {
		t.Fatal("agent mail should be available by default")
	}
	if len(report.Recommendations) != 1 {
		t.Fatalf("recommendations = %d, want 1", len(report.Recommendations))
	}
	rec := report.Recommendations[0]
	if !reservationActionOK(rec.Action, ReservationActionMessageHolder, ReservationActionNarrow) {
		t.Fatalf("Action = %q, want holder message or narrow recommendation", rec.Action)
	}
	requireReservationText(t, rec.Risk, "critical")
	for _, want := range []string{"broad_path_pattern", "inactive_holder", "stale_in_progress_context", "stale_reservation", "short_ttl"} {
		if !containsReservationString(rec.ReasonCodes, want) {
			t.Fatalf("ReasonCodes missing %q: %#v", want, rec.ReasonCodes)
		}
	}
	if len(report.LogRows) != 1 {
		t.Fatalf("LogRows = %d, want 1", len(report.LogRows))
	}
	log := report.LogRows[0]
	requireReservationText(t, log.PathPattern, "**")
	requireReservationText(t, log.Holder, "BlueLake")
	requireReservationText(t, log.WorktreePath, "")
	if log.ReservationID != 7 {
		t.Fatalf("ReservationID = %d, want 7", log.ReservationID)
	}
}

func TestAdviseReservations_OverlapsSortByRisk(t *testing.T) {
	t.Parallel()
	now := anchor().Add(30 * time.Minute)
	report := AdviseReservations([]ReservationRiskInput{
		{
			ID:          1,
			PathPattern: "internal/auth/**",
			AgentName:   "BlueLake",
			Exclusive:   true,
			CreatedAt:   anchor(),
			ExpiresAt:   now.Add(time.Hour),
		},
		{
			ID:          2,
			PathPattern: "internal/auth/session.go",
			AgentName:   "GreenHill",
			Exclusive:   true,
			CreatedAt:   now.Add(-5 * time.Minute),
			ExpiresAt:   now.Add(time.Hour),
		},
	}, ReservationAdvisorOptions{Now: now})

	if len(report.Recommendations) != 2 {
		t.Fatalf("recommendations = %d, want 2", len(report.Recommendations))
	}
	for _, rec := range report.Recommendations {
		if !containsReservationString(rec.ReasonCodes, "overlapping_reservation") {
			t.Fatalf("expected overlap reason in %+v", rec)
		}
	}
	if report.Recommendations[0].RiskScore < report.Recommendations[1].RiskScore {
		t.Fatalf("recommendations not sorted by risk: %+v", report.Recommendations)
	}
}

func TestAdviseReservations_AgentMailUnavailableIsProofModeWarning(t *testing.T) {
	t.Parallel()
	report := AdviseReservations([]ReservationRiskInput{
		{ID: 1, PathPattern: "**", AgentName: "BlueLake", Exclusive: true},
	}, ReservationAdvisorOptions{
		Now:                  anchor(),
		AgentMailUnavailable: true,
		AgentMailError:       "connection refused",
	})

	if report.AgentMailAvailable {
		t.Fatal("AgentMailAvailable = true, want false")
	}
	if len(report.Recommendations) != 0 {
		t.Fatalf("recommendations = %d, want 0 when source unavailable", len(report.Recommendations))
	}
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "connection refused") {
		t.Fatalf("unexpected warnings: %#v", report.Warnings)
	}
}

func reservationActionOK(got string, wants ...string) bool {
	for _, want := range wants {
		if strings.Compare(got, want) == 0 {
			return true
		}
	}
	return false
}

func containsReservationString(values []string, want string) bool {
	for _, value := range values {
		if strings.Compare(value, want) == 0 {
			return true
		}
	}
	return false
}

func requireReservationText(t *testing.T, got, want string) {
	t.Helper()
	if strings.Compare(got, want) != 0 {
		t.Fatalf("got %q, want %q", got, want)
	}
}
