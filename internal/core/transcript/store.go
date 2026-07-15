package transcript

import (
	"sync"
	"time"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/core/framing"
)

// Record is one durable transcript entry: permitted target input and
// output for debugging. It is logically separate from audit.Record,
// which captures security-relevant decisions instead.
type Record struct {
	Board     string    `json:"board"`
	Session   string    `json:"session"`
	Transport string    `json:"transport"`
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Direction string    `json:"direction"` // "to-target" or "from-target"
	Source    string    `json:"source"`    // "human", "ai", or "daemon"
	Redacted  bool      `json:"redacted"`
	Data      []byte    `json:"data"`
}

// Writer is the durable, append-only transcript store. It has its own
// file and sequence space, independent of audit.Writer.
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
