// Package audit is the durable, append-only security-event store:
// proposals, human decisions, executed command text, state
// transitions, interruptions, and terminal results. It is logically
// separate from the transcript package, with its own store interface,
// file, sequence space, retention settings, and export policy; neither
// package is implemented as a mode of the other. It must not refer to
// /dev, Unix sockets, POSIX terminal APIs, COM ports, or Windows Named
// Pipes.
package audit

import (
	"sync"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/framing"
)

// Record is one durable audit entry.
type Record struct {
	Board     string    `json:"board"`
	Session   string    `json:"session"`
	Transport string    `json:"transport"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	// Kind identifies the event, e.g. "proposal", "approval",
	// "rejection", "edit", "transaction", "result",
	// "state-transition", "interruption", "secret-begin", or
	// "secret-done".
	Kind string `json:"kind"`
	// Detail is a redacted, human-readable description; it must never
	// carry secret bytes.
	Detail string `json:"detail"`
}

// Writer is the durable, append-only audit store. It has its own file
// and sequence space, independent of transcript.Writer.
type Writer struct {
	mu   sync.Mutex
	path string
	seq  uint64
}

// NewWriter constructs a Writer backed by the file at path.
func NewWriter(path string) *Writer {
	return &Writer{path: path}
}

// Append assigns the next sequence number and timestamp (if unset) to
// rec, persists it, and returns the stored copy.
func (w *Writer) Append(rec Record) (Record, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.seq++
	rec.Seq = w.seq
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now()
	}
	if err := framing.AppendJSONL(w.path, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// ReadAll returns every durable record written so far.
func (w *Writer) ReadAll() ([]Record, error) {
	return framing.ReadAllJSONL[Record](w.path)
}
