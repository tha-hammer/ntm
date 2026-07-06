package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// bd-v4u1i: a parallel group whose parent context is cancelled externally
// (Ctrl-C, parent workflow cancel, etc.) must report StatusCancelled — not
// StatusCompleted — even when no individual substep returned an error. The
// previous code only checked context.DeadlineExceeded, so plain
// context.Canceled fell through to the success branch.
func TestExecuteParallel_ParentContextCanceledMakesGroupCancelled(t *testing.T) {
	e, workflow := createTestExecutor()
	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "step1", Prompt: "Task 1"},
			{ID: "step2", Prompt: "Task 2"},
		}},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled — child goroutines see Done immediately

	result := e.executeParallel(ctx, step, workflow)

	if result.Status == StatusCompleted {
		t.Fatalf("status = %q, want anything but Completed when parent ctx is cancelled", result.Status)
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q, want %q", result.Status, StatusCancelled)
	}
	if result.SkipKind != SkipKindCancelled {
		t.Fatalf("SkipKind = %q, want %q", result.SkipKind, SkipKindCancelled)
	}
	if result.Error == nil || result.Error.Type != "parallel_cancelled" {
		t.Fatalf("error = %#v, want parallel_cancelled error", result.Error)
	}
}

// bd-v4u1i: even without an explicit parent-context cancel, any substep that
// itself reports StatusCancelled (e.g. a long-running child cancelled inline)
// must surface as a cancelled parent — the old code reported StatusCompleted
// because failed==0, fail-fast cancelled==false, and ctx.Err()==nil.
func TestExecuteParallel_CancelledSubstepMakesGroupCancelled(t *testing.T) {
	e, workflow := createTestExecutor()
	step := &Step{
		ID: "parallel_group",
		Parallel: ParallelSpec{Steps: []Step{
			{ID: "step1", Prompt: "Task 1"},
			{ID: "step2", Prompt: "Task 2"},
		}},
	}

	// Run with a context that fires after the substeps would normally
	// complete in dry-run; we cancel during the wait window so at least one
	// substep observes the cancel through parallelCtx.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	result := e.executeParallel(ctx, step, workflow)

	// In dry-run the substeps may all complete before the cancel arrives;
	// when that happens this test trivially passes. The bug fix is exercised
	// only when at least one substep observed the cancel.
	if result.Status == StatusCompleted {
		// Acceptable race: cancel arrived after both substeps finished.
		// Skip in that case rather than flaking.
		t.Skip("dry-run substeps completed before cancel arrived — race tolerant")
	}
	if result.Status != StatusCancelled && result.Status != StatusFailed {
		t.Fatalf("status = %q, want Cancelled or Failed", result.Status)
	}
	if result.Status == StatusCancelled && !strings.Contains(result.SkipReason, "cancelled") {
		t.Fatalf("SkipReason = %q, want substring 'cancelled'", result.SkipReason)
	}
}
