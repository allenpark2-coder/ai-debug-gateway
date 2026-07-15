// Command gateway-bench is the Linux-specific driver for the
// perftest harness: it runs internal/perftest.Run, adds peak RSS by
// reading /proc/self/status (which the portable perftest core
// deliberately never touches), enforces the release gate's
// responsiveness and memory bounds, and prints the resulting metrics
// as JSON.
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
	rate := flag.String("rate", "10MiB", "synthetic target output rate to feed, e.g. 10MiB")
	duration := flag.Duration("duration", 30*time.Second, "how long to feed at that rate")
	humanInterval := flag.Duration("human-interval", 10*time.Millisecond, "how often to call WriteHuman while feeding")
	maxP99 := flag.Duration("max-p99", 100*time.Millisecond, "fail if measured p99 human input latency reaches this")
	runtimeOverhead := flag.String("runtime-overhead", "64MiB", "allowance on top of the fixed ring/queue baseline before peak RSS fails the gate")
	flag.Parse()

	rateBytes, err := parseByteSize(*rate)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway-bench: -rate:", err)
		os.Exit(2)
	}
	overheadBytes, err := parseByteSize(*runtimeOverhead)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway-bench: -runtime-overhead:", err)
		os.Exit(2)
	}

	m, err := perftest.Run(perftest.Config{
		BytesPerSecond: int(rateBytes),
		Duration:       *duration,
		HumanInterval:  humanIntervalOrDefault(*humanInterval),
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

	maxP99Ms := float64(*maxP99) / float64(time.Millisecond)
	failed := false
	if m.InputLatencyP99Ms >= maxP99Ms {
		fmt.Fprintf(os.Stderr, "gateway-bench: input_latency_p99_ms %.3f >= bound %.3f\n", m.InputLatencyP99Ms, maxP99Ms)
		failed = true
	}
	memBound := perftest.ExpectedBaselineBytes + overheadBytes
	if m.PeakRSSBytes > memBound {
		fmt.Fprintf(os.Stderr, "gateway-bench: peak_rss_bytes %d > bound %d (baseline %d + overhead %d)\n",
			m.PeakRSSBytes, memBound, uint64(perftest.ExpectedBaselineBytes), overheadBytes)
		failed = true
	}
	if failed {
		os.Exit(1)
	}
}

func humanIntervalOrDefault(d time.Duration) time.Duration {
	if d <= 0 {
		return 10 * time.Millisecond
	}
	return d
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

// byteSizeSuffixes must stay ordered longest-suffix-first: "kib" is a
// suffix of nothing else here, but checking "b" before "kib" would
// misparse "10kib" as "10ki" + trailing "b" never matched.
var byteSizeSuffixes = []struct {
	suffix     string
	multiplier uint64
}{
	{"gib", 1 << 30},
	{"mib", 1 << 20},
	{"kib", 1 << 10},
	{"gb", 1 << 30},
	{"mb", 1 << 20},
	{"kb", 1 << 10},
	{"b", 1},
}

// parseByteSize parses a human-readable byte size like "10MiB",
// "64MiB", "512B", or a bare number of bytes such as "4096".
func parseByteSize(s string) (uint64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty byte size")
	}
	lower := strings.ToLower(trimmed)

	for _, suf := range byteSizeSuffixes {
		if strings.HasSuffix(lower, suf.suffix) {
			numPart := strings.TrimSpace(trimmed[:len(trimmed)-len(suf.suffix)])
			if numPart == "" {
				return 0, fmt.Errorf("byte size %q has no number", s)
			}
			n, err := strconv.ParseFloat(numPart, 64)
			if err != nil {
				return 0, fmt.Errorf("byte size %q: %w", s, err)
			}
			if n < 0 {
				return 0, fmt.Errorf("byte size %q must not be negative", s)
			}
			return uint64(n * float64(suf.multiplier)), nil
		}
	}

	n, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("byte size %q: unrecognized unit and not a plain integer", s)
	}
	return n, nil
}
