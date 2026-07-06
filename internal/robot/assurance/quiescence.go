package assurance

// QuiescenceState is the operator-facing classification for whether a swarm
// is safe to leave alone, needs more work claimed, or is blocked by peers.
type QuiescenceState string

const (
	QuiescenceQueueDry            QuiescenceState = "queue_dry"
	QuiescenceBlockedByPeer       QuiescenceState = "blocked_by_peer"
	QuiescenceBlockedBySelf       QuiescenceState = "blocked_by_self"
	QuiescenceSaturatedReviewLoop QuiescenceState = "saturated_review_loop"
	QuiescenceUnsafeToStandDown   QuiescenceState = "unsafe_to_stand_down"
)

// QuiescenceInput is a tmux-free summary of the evidence needed to decide
// whether a swarm can safely stand down.
type QuiescenceInput struct {
	ReadyCount               int
	ActionableCount          int
	InProgressCount          int
	LiveOwnedInProgressCount int
	ActiveReservationCount   int
	UrgentMailCount          int
	PendingAckCount          int
	TrackerNeedsFlush        bool
	DirtyWorktree            bool
	ReviewRounds             int
	RecentReviewFindings     int
}

// QuiescenceAssessment is the compact machine-readable result exposed to
// operator surfaces.
type QuiescenceAssessment struct {
	State               QuiescenceState `json:"state"`
	SafeToStandDown     bool            `json:"safe_to_stand_down"`
	Signal              Signal          `json:"signal"`
	ReasonCodes         []ReasonCode    `json:"reason_codes,omitempty"`
	Summary             string          `json:"summary"`
	SuggestedNextAction string          `json:"suggested_next_action"`
}

// EvaluateQuiescence classifies a swarm from already-collected evidence.
// It intentionally does not inspect tmux, git, Agent Mail, or Beads itself;
// callers provide the facts so unit tests stay deterministic.
func EvaluateQuiescence(input QuiescenceInput) QuiescenceAssessment {
	reasons := quiescenceUnsafeReasons(input)
	if len(reasons) > 0 {
		return newQuiescenceAssessment(
			QuiescenceUnsafeToStandDown,
			false,
			SignalStatusDegraded,
			reasons,
			"unsafe to stand down; operator-visible work or hygiene blockers remain",
			"resolve pending work, urgent mail, dirty state, or tracker drift before standing down",
		)
	}

	// Peer-attributed signals take precedence: if InProgressCount or
	// ActiveReservationCount is non-zero, the swarm is blocked on
	// state another agent (or the registry as a whole) is holding.
	// LiveOwnedInProgressCount alone — the operator's OWN in-flight
	// work — produces QuiescenceBlockedBySelf so consumers routing on
	// the State string can render the blocker correctly (bd-u068s).
	if input.InProgressCount > 0 || input.ActiveReservationCount > 0 {
		reasons := []ReasonCode{ReasonQuiescenceInProgressWork}
		if input.ActiveReservationCount > 0 {
			reasons = append(reasons, ReasonReservationPathConflict)
		}
		return newQuiescenceAssessment(
			QuiescenceBlockedByPeer,
			false,
			SignalStatusDegraded,
			reasons,
			"queue has no ready work, but peer-owned or in-flight work still needs resolution",
			"inspect in-progress beads and active reservations before creating or assigning new work",
		)
	}
	if input.LiveOwnedInProgressCount > 0 {
		return newQuiescenceAssessment(
			QuiescenceBlockedBySelf,
			false,
			SignalStatusDegraded,
			[]ReasonCode{ReasonQuiescenceInProgressWork},
			"queue has no ready work, but the operator has live in-flight work that still needs resolution",
			"finish or release your own in-progress work before standing down",
		)
	}

	if input.ReviewRounds > 0 && input.RecentReviewFindings == 0 {
		return newQuiescenceAssessment(
			QuiescenceSaturatedReviewLoop,
			true,
			SignalStatusHealthy,
			[]ReasonCode{ReasonQuiescenceQueueDry, ReasonQuiescenceReviewSaturated},
			"queue is dry and recent review rounds produced no actionable findings",
			"safe to stand down or start a new planning pass",
		)
	}

	return newQuiescenceAssessment(
		QuiescenceQueueDry,
		true,
		SignalStatusHealthy,
		[]ReasonCode{ReasonQuiescenceQueueDry},
		"queue is dry with no immediate blockers in the provided evidence",
		"run review or planning loops if additional improvement work is desired",
	)
}

func quiescenceUnsafeReasons(input QuiescenceInput) []ReasonCode {
	reasons := make([]ReasonCode, 0, 5)
	if input.ReadyCount > 0 || input.ActionableCount > 0 {
		reasons = append(reasons, ReasonQuiescenceReadyWork)
	}
	// bd-zy2c1: emit each mail-attention reason code only when its
	// corresponding input field is non-zero. Pre-fix the function
	// collapsed both UrgentMailCount and PendingAckCount under
	// ReasonQuiescenceUrgentMail, masking a pending-ack-only state
	// as 'urgent_mail' for downstream consumers routing on the
	// reason_codes field. Both can fire simultaneously when both
	// input counts are non-zero.
	if input.UrgentMailCount > 0 {
		reasons = append(reasons, ReasonQuiescenceUrgentMail)
	}
	if input.PendingAckCount > 0 {
		reasons = append(reasons, ReasonQuiescencePendingAckMail)
	}
	if input.TrackerNeedsFlush {
		reasons = append(reasons, ReasonQuiescenceTrackerDirty)
	}
	if input.DirtyWorktree {
		reasons = append(reasons, ReasonQuiescenceDirtyWorktree)
	}
	if input.RecentReviewFindings > 0 {
		reasons = append(reasons, ReasonQuiescencePendingWork)
	}
	return reasons
}

func newQuiescenceAssessment(state QuiescenceState, safe bool, status SignalStatus, reasons []ReasonCode, summary, nextAction string) QuiescenceAssessment {
	return QuiescenceAssessment{
		State:           state,
		SafeToStandDown: safe,
		Signal: Signal{
			Type:     SignalQuiescenceCandidate,
			Status:   status,
			Reasons:  reasons,
			Evidence: summary,
		},
		ReasonCodes:         reasons,
		Summary:             summary,
		SuggestedNextAction: nextAction,
	}
}
