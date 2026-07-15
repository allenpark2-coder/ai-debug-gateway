// Command gateway-bench is the Linux-specific driver for the
// perftest harness: it runs internal/perftest.Run, adds peak RSS by
// reading /proc/self/status (which the portable perftest core
// deliberately never touches), enforces the release gate's
// responsiveness bounds, and prints the resulting metrics as JSON.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/perftest"
)

func main() {
	rate := flag.Int("rate", 10<<20, "synthetic target output rate to feed, in bytes/second")
	duration := flag.Duration("duration", 30*time.Second, "how long to feed at that rate")
	humanInterval := flag.Duration("human-interval", 10*time.Millisecond, "how often to call WriteHuman while feeding")
	maxP99 := flag.Float64("max-p99-ms", 100, "fail if measured input_latency_p99_ms is at or above this")
	flag.Parse()

	m, err := perftest.Run(perftest.Config{
		BytesPerSecond: *rate,
		Duration:       *duration,
		HumanInterval:  *humanInterval,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway-bench:", err)
		os.Exit(1)
	}

	rss, err := peakRSSBytes()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway-bench: reading peak RSS:", err)
		os.Exit(1)
	}
	m.PeakRSSBytes = rss

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		fmt.Fprintln(os.Stderr, "gateway-bench:", err)
		os.Exit(1)
	}

	if m.InputLatencyP99Ms >= *maxP99 {
		fmt.Fprintf(os.Stderr, "gateway-bench: input_latency_p99_ms %.2f >= bound %.2f\n", m.InputLatencyP99Ms, *maxP99)
		os.Exit(1)
	}
}

// peakRSSBytes reads /proc/self/status's VmHWM line -- the kernel's
// own running high-water mark for this process's resident set, so no
// polling is needed to catch a peak that happened mid-run.
func peakRSSBytes() (uint64, error) {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "VmHWM:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("unexpected VmHWM line %q", line)
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing VmHWM line %q: %w", line, err)
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("no VmHWM line found in /proc/self/status")
}
