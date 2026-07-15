// Package perftest is the portable backpressure and latency
// measurement core for the release gate: feed synthetic target output
// through a real gateway.Coordinator at a configured rate while a
// concurrent human writes, and report throughput and human input
// latency. It must not refer to /dev, Unix sockets, POSIX terminal
// APIs, or /proc: RSS sampling belongs in cmd/gateway-bench, the
// Linux-specific command built on top of this package, not here.
package perftest

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/gateway"
	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// producerBlockThreshold is how long a single push into the feeder
// stream may wait before Run reports the producer as blocked: proof
// the coordinator's read loop has stopped keeping up, not just normal
// scheduling jitter.
const producerBlockThreshold = 2 * time.Second

// These mirror internal/gateway's own fixed bounds -- the transcript
// ring's capacity (NewCoordinator's transcript.NewRing(1 << 20)) and
// one subscriber's worst-case backlog (subscriberQueueSize events in
// subscriber.go, each up to the read loop's 4096-byte buffer in
// coordinator.go) -- duplicated here rather than imported, since
// neither is exported from that package.
const (
	ringCapacityBytes     = 1 << 20
	subscriberQueueEvents = 256
	readLoopBufferBytes   = 4096
)

// ExpectedBaselineBytes is the fixed-size memory a Run's one
// Coordinator and one subscriber can account for by construction: the
// ring plus that subscriber's worst-case backlog. A caller checking
// Metrics.PeakRSSBytes against a bound should add its own allowance
// for runtime overhead (goroutine stacks, GC headroom, the Go
// runtime itself) on top of this baseline.
const ExpectedBaselineBytes = ringCapacityBytes + subscriberQueueEvents*readLoopBufferBytes

// Config parameterizes one harness run.
type Config struct {
	// BytesPerSecond is the synthetic target output rate to feed.
	BytesPerSecond int
	// Duration is how long to feed at that rate.
	Duration time.Duration
	// HumanInterval is how often the harness calls WriteHuman while
	// feeding, to measure its latency under load.
	HumanInterval time.Duration
}

// Metrics is one run's results. PeakRSSBytes is always zero here: this
// package never reads /proc/self/status; cmd/gateway-bench samples it
// concurrently with Run and fills it in.
type Metrics struct {
	BytesPerSecond      float64 `json:"bytes_per_second"`
	InputLatencyP50Ms   float64 `json:"input_latency_p50_ms"`
	InputLatencyP99Ms   float64 `json:"input_latency_p99_ms"`
	PeakRSSBytes        uint64  `json:"peak_rss_bytes"`
	DroppedClientFrames uint64  `json:"dropped_client_frames"`
}

// Run feeds synthetic target output through a real gateway.Coordinator
// at cfg.BytesPerSecond for cfg.Duration, while a concurrent goroutine
// calls WriteHuman every cfg.HumanInterval and times each call. It
// also registers one output subscriber via SubscribeHuman and never
// drains it -- standing in for any slow or absent consumer of the
// coordinator's fan-out, since the underlying non-blocking-drop
// behavior in subscriber.go is identical regardless of which side
// registers it -- and reports how many frames it dropped.
func Run(cfg Config) (Metrics, error) {
	if cfg.BytesPerSecond <= 0 {
		return Metrics{}, fmt.Errorf("perftest: BytesPerSecond must be positive")
	}
	if cfg.HumanInterval <= 0 {
		return Metrics{}, fmt.Errorf("perftest: HumanInterval must be positive")
	}

	c := gateway.NewCoordinator("perftest-board")
	defer c.Stop()

	stream := newFeederStream()
	if err := c.StartUART(stream, gateway.LoginConfig{}, nil); err != nil {
		return Metrics{}, err
	}

	sub := c.SubscribeHuman()

	stop := make(chan struct{})
	var wg sync.WaitGroup

	var producerBytes int64
	var producerErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		producerBytes, producerErr = feed(stream, cfg.BytesPerSecond, stop)
	}()

	var latMu sync.Mutex
	latencies := make([]time.Duration, 0, 1024)
	wg.Add(1)
	go func() {
		defer wg.Done()
		measureHumanLatency(c, cfg.HumanInterval, stop, &latMu, &latencies)
	}()

	time.Sleep(cfg.Duration)
	close(stop)
	wg.Wait()
	stream.Close()

	if producerErr != nil {
		return Metrics{}, producerErr
	}

	achievedRate := float64(producerBytes) / cfg.Duration.Seconds()
	p50 := percentile(latencies, 50)
	p99 := percentile(latencies, 99)

	return Metrics{
		BytesPerSecond:      achievedRate,
		InputLatencyP50Ms:   float64(p50) / float64(time.Millisecond),
		InputLatencyP99Ms:   float64(p99) / float64(time.Millisecond),
		DroppedClientFrames: sub.Dropped(),
	}, nil
}

func measureHumanLatency(c *gateway.Coordinator, interval time.Duration, stop <-chan struct{}, mu *sync.Mutex, out *[]time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	payload := []byte("x")
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			start := time.Now()
			_, _ = c.WriteHuman(payload)
			d := time.Since(start)
			mu.Lock()
			*out = append(*out, d)
			mu.Unlock()
		}
	}
}

// feedPollInterval is deliberately much finer than any Duration this
// harness is meaningfully run with, and unrelated to bytesPerSecond:
// feed recomputes how many bytes should have gone out by elapsed real
// time on every poll and sends the shortfall, so it converges on the
// configured rate over the run even when the scheduler coalesces or
// delays some ticks, rather than assuming any single tick interval is
// hit precisely.
const feedPollInterval = 2 * time.Millisecond

// feed paces bytes into stream at bytesPerSecond until stop closes,
// returning the total bytes actually pushed.
func feed(stream *feederStream, bytesPerSecond int, stop <-chan struct{}) (int64, error) {
	ticker := time.NewTicker(feedPollInterval)
	defer ticker.Stop()

	start := time.Now()
	var sent int64
	for {
		select {
		case <-stop:
			return sent, nil
		case <-ticker.C:
			target := int64(float64(bytesPerSecond) * time.Since(start).Seconds())
			need := target - sent
			if need <= 0 {
				continue
			}
			chunk := make([]byte, need)
			for i := range chunk {
				chunk[i] = byte('a' + i%26)
			}
			if err := stream.push(chunk, producerBlockThreshold); err != nil {
				return sent, err
			}
			sent += int64(len(chunk))
		}
	}
}

// percentile returns the p-th percentile of d by the nearest-rank
// method, without mutating d. It returns 0 for an empty input.
func percentile(d []time.Duration, p int) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), d...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := len(sorted) * p / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// feederStream is a synthetic transport.Stream: Read hands out
// whatever push fed it (standing in for a paced target device), Write
// is a no-op sink -- matching how WriteHuman only ever touches the
// stream directly and never the ring or any subscriber, so this
// stream never needs to model an echo.
type feederStream struct {
	ch        chan []byte
	buf       []byte
	closeOnce sync.Once
	closed    chan struct{}
}

func newFeederStream() *feederStream {
	return &feederStream{
		ch:     make(chan []byte, 64),
		closed: make(chan struct{}),
	}
}

// push delivers chunk to a future Read call. It reports an error if it
// had to wait longer than blockedAfter, meaning the reader side (the
// coordinator's read loop) has stopped keeping up, or if the stream is
// already closed.
func (f *feederStream) push(chunk []byte, blockedAfter time.Duration) error {
	select {
	case f.ch <- chunk:
		return nil
	case <-f.closed:
		return io.ErrClosedPipe
	case <-time.After(blockedAfter):
		return fmt.Errorf("perftest: producer blocked for over %s -- the coordinator's read loop is not keeping up", blockedAfter)
	}
}

func (f *feederStream) Read(p []byte) (int, error) {
	if len(f.buf) > 0 {
		n := copy(p, f.buf)
		f.buf = f.buf[n:]
		return n, nil
	}
	select {
	case b, ok := <-f.ch:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, b)
		if n < len(b) {
			f.buf = append([]byte(nil), b[n:]...)
		}
		return n, nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *feederStream) Write(p []byte) (int, error) { return len(p), nil }

func (f *feederStream) Close() error {
	f.closeOnce.Do(func() {
		close(f.closed)
	})
	return nil
}

func (f *feederStream) Identity() transport.Identity { return transport.Identity{} }
func (f *feederStream) Kind() string                 { return "perftest" }

var _ transport.Stream = (*feederStream)(nil)
