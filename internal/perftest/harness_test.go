package perftest

import (
	"testing"
	"time"
)

// TestBackpressureUnderLoad is a fast smoke-test variant of the
// official make perf gate (10 MiB/s for 30s): a shorter run at a
// lower rate, fast enough for routine `go test`, checking that human
// input latency stays low even while target output is arriving
// continuously and nobody drains the registered output subscriber.
func TestBackpressureUnderLoad(t *testing.T) {
	cfg := Config{
		BytesPerSecond: 2 << 20, // 2 MiB/s
		Duration:       500 * time.Millisecond,
		HumanInterval:  5 * time.Millisecond,
	}
	m, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if m.InputLatencyP99Ms >= 100 {
		t.Fatalf("got p99 latency %.2fms, want < 100ms", m.InputLatencyP99Ms)
	}
	if m.InputLatencyP50Ms <= 0 {
		t.Fatalf("got p50 latency %.2fms, want a positive measured value", m.InputLatencyP50Ms)
	}
	if m.BytesPerSecond <= 0 {
		t.Fatalf("got achieved throughput %.2f, want a positive measured value", m.BytesPerSecond)
	}
}

// TestStalledOutputSubscriberDropsInsteadOfBlockingProducer feeds
// enough data to guarantee the never-drained output subscriber's
// bounded queue overflows, and asserts that this shows up only as a
// drop count -- never as elevated human input latency -- proving a
// slow or absent consumer can never make the producer (or a human
// typing) wait.
func TestStalledOutputSubscriberDropsInsteadOfBlockingProducer(t *testing.T) {
	cfg := Config{
		// 16 MiB/s for 1s asks for 16 MiB, well over the ~1 MiB (256 *
		// 4096B) subscriber queue even with the race detector's slowdown
		// eating most of the nominal rate.
		BytesPerSecond: 16 << 20,
		Duration:       time.Second,
		HumanInterval:  5 * time.Millisecond,
	}
	m, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if m.DroppedClientFrames == 0 {
		t.Fatal("want the never-drained subscriber to have dropped at least one frame")
	}
	if m.InputLatencyP99Ms >= 100 {
		t.Fatalf("got p99 latency %.2fms, want < 100ms even with a stalled subscriber", m.InputLatencyP99Ms)
	}
}

func TestPercentileHelper(t *testing.T) {
	d := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(d, 50); got != 6 {
		t.Fatalf("got %v, want 6", got)
	}
	if got := percentile(d, 99); got != 10 {
		t.Fatalf("got %v, want 10", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Fatalf("got %v, want 0 for empty input", got)
	}
}
