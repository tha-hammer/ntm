// Package tmux provides a wrapper around tmux commands.
package tmux

import (
	"context"
	"strings"
	"sync"
)

// ============== Optimized Capture Helpers ==============
//
// These helpers provide semantic capture operations with appropriate line budgets
// to reduce latency and CPU usage. Use these instead of raw CapturePaneOutput
// with magic numbers.
//
// Line budget guidelines:
// - StatusDetection: 10-20 lines (fast, frequent polling for state changes)
// - HealthCheck: 20-50 lines (moderate analysis for health/error detection)
// - FullContext: 500-2000 lines (rare, expensive full capture for analysis)

// Capture line budget constants for consistent usage across codebase.
const (
	// LinesStatusDetection is the default for quick status/state detection.
	// Use for: agent ready checks, ack polling, interrupt detection.
	LinesStatusDetection = 20

	// LinesHealthCheck is the default for health and error analysis.
	// Use for: error detection, alert generation, process health.
	LinesHealthCheck = 50

	// LinesFullContext is the default for comprehensive context capture.
	// Use for: context estimation, grep, diff, save operations.
	LinesFullContext = 500

	// LinesCheckpoint is for session checkpoint/restore operations.
	// Use for: checkpoint capture, pipeline stages.
	LinesCheckpoint = 2000
)

// CapturePaneClient is the minimal tmux capture surface used by CaptureBroker.
// Tests can provide a fake client with capture count assertions.
type CapturePaneClient interface {
	CapturePaneOutputContext(ctx context.Context, target string, lines int) (string, error)
}

type captureBrokerEntry struct {
	lines  int
	output string
	err    error
}

// CaptureBroker caches pane captures for one command attempt. It lets multiple
// consumers share compatible output while keeping the cache lifetime explicit.
type CaptureBroker struct {
	client  CapturePaneClient
	mu      sync.Mutex
	entries map[string]captureBrokerEntry
}

// NewCaptureBroker creates a per-command capture cache around client.
func NewCaptureBroker(client CapturePaneClient) *CaptureBroker {
	if client == nil {
		client = DefaultClient
	}
	return &CaptureBroker{
		client:  client,
		entries: make(map[string]captureBrokerEntry),
	}
}

// NewDefaultCaptureBroker creates a per-command capture cache for DefaultClient.
func NewDefaultCaptureBroker() *CaptureBroker {
	return NewCaptureBroker(DefaultClient)
}

// CapturePaneOutputContext captures pane output, reusing a same-pane capture
// when the cached line budget is large enough for the request.
func (b *CaptureBroker) CapturePaneOutputContext(ctx context.Context, target string, lines int) (string, error) {
	if b == nil {
		return DefaultClient.CapturePaneOutputContext(ctx, target, lines)
	}
	lines = normalizeCaptureLines(lines)

	if output, err, ok := b.cachedCapture(target, lines); ok {
		return output, err
	}

	output, err := b.client.CapturePaneOutputContext(ctx, target, lines)
	b.storeCapture(target, lines, output, err)

	return output, err
}

// CapturePaneOutput captures pane output with the standard tmux command timeout.
func (b *CaptureBroker) CapturePaneOutput(target string, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultCommandTimeout)
	defer cancel()
	return b.CapturePaneOutputContext(ctx, target, lines)
}

// CaptureForStatusDetection captures with the status-detection budget through
// the broker.
func (b *CaptureBroker) CaptureForStatusDetection(target string) (string, error) {
	return b.CapturePaneOutput(target, LinesStatusDetection)
}

// CaptureForStatusDetectionContext captures with the status-detection budget
// through the broker.
func (b *CaptureBroker) CaptureForStatusDetectionContext(ctx context.Context, target string) (string, error) {
	return b.CapturePaneOutputContext(ctx, target, LinesStatusDetection)
}

// CaptureForHealthCheck captures with the health-check budget through the broker.
func (b *CaptureBroker) CaptureForHealthCheck(target string) (string, error) {
	return b.CapturePaneOutput(target, LinesHealthCheck)
}

// CaptureForHealthCheckContext captures with the health-check budget through
// the broker.
func (b *CaptureBroker) CaptureForHealthCheckContext(ctx context.Context, target string) (string, error) {
	return b.CapturePaneOutputContext(ctx, target, LinesHealthCheck)
}

func (b *CaptureBroker) cachedCapture(target string, lines int) (string, error, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry, ok := b.entries[target]
	if !ok {
		return "", nil, false
	}
	if entry.err != nil {
		return "", entry.err, true
	}
	if entry.lines >= lines {
		return trimCaptureOutput(entry.output, lines), nil, true
	}
	return "", nil, false
}

func (b *CaptureBroker) storeCapture(target string, lines int, output string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	entry := captureBrokerEntry{lines: lines, output: output}
	if err != nil {
		entry = captureBrokerEntry{lines: lines, err: err}
	}
	b.entries[target] = entry
}

func normalizeCaptureLines(lines int) int {
	if lines < 0 {
		return -lines
	}
	return lines
}

func trimCaptureOutput(output string, lines int) string {
	if lines <= 0 {
		return output
	}

	hasTrailingNewline := strings.HasSuffix(output, "\n")
	parts := strings.Split(output, "\n")
	if hasTrailingNewline && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) <= lines {
		return output
	}

	tail := strings.Join(parts[len(parts)-lines:], "\n")
	if hasTrailingNewline {
		tail += "\n"
	}
	return tail
}

// CaptureForStatusDetection captures a minimal amount of output for quick state detection.
// This is optimized for frequent polling operations (ack, ready checks, interrupt).
// Uses LinesStatusDetection (20 lines) by default.
func (c *Client) CaptureForStatusDetection(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesStatusDetection)
}

// CaptureForStatusDetectionContext captures with context support for status detection.
func (c *Client) CaptureForStatusDetectionContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesStatusDetection)
}

// CaptureForStatusDetection captures for status detection (default client).
func CaptureForStatusDetection(target string) (string, error) {
	return DefaultClient.CaptureForStatusDetection(target)
}

// CaptureForStatusDetectionContext captures with context support (default client).
func CaptureForStatusDetectionContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForStatusDetectionContext(ctx, target)
}

// CaptureForHealthCheck captures output for health analysis and error detection.
// Uses LinesHealthCheck (50 lines) to balance between detail and performance.
func (c *Client) CaptureForHealthCheck(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesHealthCheck)
}

// CaptureForHealthCheckContext captures with context support for health checks.
func (c *Client) CaptureForHealthCheckContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesHealthCheck)
}

// CaptureForHealthCheck captures for health checks (default client).
func CaptureForHealthCheck(target string) (string, error) {
	return DefaultClient.CaptureForHealthCheck(target)
}

// CaptureForHealthCheckContext captures with context support (default client).
func CaptureForHealthCheckContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForHealthCheckContext(ctx, target)
}

// CaptureForFullContext captures comprehensive output for analysis.
// Uses LinesFullContext (500 lines) for grep, diff, save, and context estimation.
func (c *Client) CaptureForFullContext(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesFullContext)
}

// CaptureForFullContextContext captures with context support for full analysis.
func (c *Client) CaptureForFullContextContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesFullContext)
}

// CaptureForFullContext captures full context (default client).
func CaptureForFullContext(target string) (string, error) {
	return DefaultClient.CaptureForFullContext(target)
}

// CaptureForFullContextContext captures with context support (default client).
func CaptureForFullContextContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForFullContextContext(ctx, target)
}

// CaptureForCheckpoint captures maximum output for checkpoint/pipeline operations.
// Uses LinesCheckpoint (2000 lines) for complete session state.
func (c *Client) CaptureForCheckpoint(target string) (string, error) {
	return c.CapturePaneOutput(target, LinesCheckpoint)
}

// CaptureForCheckpointContext captures with context support for checkpoints.
func (c *Client) CaptureForCheckpointContext(ctx context.Context, target string) (string, error) {
	return c.CapturePaneOutputContext(ctx, target, LinesCheckpoint)
}

// CaptureForCheckpoint captures for checkpoints (default client).
func CaptureForCheckpoint(target string) (string, error) {
	return DefaultClient.CaptureForCheckpoint(target)
}

// CaptureForCheckpointContext captures with context support (default client).
func CaptureForCheckpointContext(ctx context.Context, target string) (string, error) {
	return DefaultClient.CaptureForCheckpointContext(ctx, target)
}
