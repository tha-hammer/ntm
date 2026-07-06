package tmux

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

type fakeCaptureClient struct {
	outputs map[string]string
	errors  map[string]error
	calls   []captureCall
}

type captureCall struct {
	target string
	lines  int
}

func newFakeCaptureClient() *fakeCaptureClient {
	return &fakeCaptureClient{
		outputs: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (f *fakeCaptureClient) CapturePaneOutputContext(_ context.Context, target string, lines int) (string, error) {
	f.calls = append(f.calls, captureCall{target: target, lines: lines})
	key := captureKey(target, lines)
	if err := f.errors[key]; err != nil {
		return "", err
	}
	return f.outputs[key], nil
}

func (f *fakeCaptureClient) setOutput(target string, lines int, output string) {
	f.outputs[captureKey(target, lines)] = output
}

func (f *fakeCaptureClient) setError(target string, lines int, err error) {
	f.errors[captureKey(target, lines)] = err
}

func captureKey(target string, lines int) string {
	return fmt.Sprintf("%s:%d", target, lines)
}

func TestCaptureBrokerReusesCompatibleLargerCapture(t *testing.T) {
	client := newFakeCaptureClient()
	client.setOutput("%1", LinesHealthCheck, "one\ntwo\nthree\nfour\n")
	broker := NewCaptureBroker(client)

	health, err := broker.CaptureForHealthCheckContext(context.Background(), "%1")
	if err != nil {
		t.Fatalf("health capture failed: %v", err)
	}
	status, err := broker.CapturePaneOutputContext(context.Background(), "%1", 2)
	if err != nil {
		t.Fatalf("status capture failed: %v", err)
	}

	if health != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("health output = %q", health)
	}
	if status != "three\nfour\n" {
		t.Fatalf("status output = %q, want trimmed cached tail", status)
	}
	assertCaptureCalls(t, client.calls, []captureCall{{target: "%1", lines: LinesHealthCheck}})
}

func TestCaptureBrokerRecapturesForLargerBudget(t *testing.T) {
	client := newFakeCaptureClient()
	client.setOutput("%1", 2, "three\nfour\n")
	client.setOutput("%1", LinesHealthCheck, "one\ntwo\nthree\nfour\n")
	broker := NewCaptureBroker(client)

	first, err := broker.CapturePaneOutputContext(context.Background(), "%1", 2)
	if err != nil {
		t.Fatalf("first capture failed: %v", err)
	}
	second, err := broker.CaptureForHealthCheckContext(context.Background(), "%1")
	if err != nil {
		t.Fatalf("larger capture failed: %v", err)
	}
	third, err := broker.CapturePaneOutputContext(context.Background(), "%1", 2)
	if err != nil {
		t.Fatalf("third capture failed: %v", err)
	}

	if first != "three\nfour\n" {
		t.Fatalf("first output = %q", first)
	}
	if second != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("second output = %q", second)
	}
	if third != "three\nfour\n" {
		t.Fatalf("third output = %q, want tail of larger capture", third)
	}
	assertCaptureCalls(t, client.calls, []captureCall{
		{target: "%1", lines: 2},
		{target: "%1", lines: LinesHealthCheck},
	})
}

func TestCaptureBrokerCachesFailureWithinAttempt(t *testing.T) {
	client := newFakeCaptureClient()
	captureErr := errors.New("pane missing")
	client.setError("%1", LinesStatusDetection, captureErr)
	broker := NewCaptureBroker(client)

	_, firstErr := broker.CaptureForStatusDetectionContext(context.Background(), "%1")
	_, secondErr := broker.CaptureForStatusDetectionContext(context.Background(), "%1")

	if !errors.Is(firstErr, captureErr) {
		t.Fatalf("first error = %v, want %v", firstErr, captureErr)
	}
	if !errors.Is(secondErr, captureErr) {
		t.Fatalf("second error = %v, want cached %v", secondErr, captureErr)
	}
	assertCaptureCalls(t, client.calls, []captureCall{{target: "%1", lines: LinesStatusDetection}})
}

func TestCaptureBrokerFailureCacheIsPerBroker(t *testing.T) {
	client := newFakeCaptureClient()
	captureErr := errors.New("pane missing")
	client.setError("%1", LinesStatusDetection, captureErr)

	firstBroker := NewCaptureBroker(client)
	if _, err := firstBroker.CaptureForStatusDetectionContext(context.Background(), "%1"); !errors.Is(err, captureErr) {
		t.Fatalf("first broker error = %v, want %v", err, captureErr)
	}

	delete(client.errors, captureKey("%1", LinesStatusDetection))
	client.setOutput("%1", LinesStatusDetection, "ready\n")

	secondBroker := NewCaptureBroker(client)
	output, err := secondBroker.CaptureForStatusDetectionContext(context.Background(), "%1")
	if err != nil {
		t.Fatalf("second broker capture failed: %v", err)
	}
	if output != "ready\n" {
		t.Fatalf("second broker output = %q", output)
	}
	assertCaptureCalls(t, client.calls, []captureCall{
		{target: "%1", lines: LinesStatusDetection},
		{target: "%1", lines: LinesStatusDetection},
	})
}

func TestCaptureBrokerCachesPerTarget(t *testing.T) {
	client := newFakeCaptureClient()
	client.setOutput("%1", LinesStatusDetection, "one\n")
	client.setOutput("%2", LinesStatusDetection, "two\n")
	broker := NewCaptureBroker(client)

	if _, err := broker.CaptureForStatusDetectionContext(context.Background(), "%1"); err != nil {
		t.Fatalf("first target capture failed: %v", err)
	}
	if _, err := broker.CaptureForStatusDetectionContext(context.Background(), "%2"); err != nil {
		t.Fatalf("second target capture failed: %v", err)
	}
	if _, err := broker.CaptureForStatusDetectionContext(context.Background(), "%1"); err != nil {
		t.Fatalf("cached first target capture failed: %v", err)
	}

	assertCaptureCalls(t, client.calls, []captureCall{
		{target: "%1", lines: LinesStatusDetection},
		{target: "%2", lines: LinesStatusDetection},
	})
}

func assertCaptureCalls(t *testing.T, got, want []captureCall) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("capture calls = %#v, want %#v", got, want)
	}
}
