// Package transcript holds the bounded in-memory ring buffer that
// terminal and AI clients tail, and the durable append-only transcript
// store used for later export. It must not refer to /dev, Unix
// sockets, POSIX terminal APIs, COM ports, or Windows Named Pipes.
package transcript

import "sync"

// Chunk is a slice of ring contents returned by ReadAfter.
type Chunk struct {
	// Start is the sequence number of Data[0].
	Start uint64
	// Next is the sequence number to pass as `after` on the next call
	// to continue reading where this chunk left off.
	Next uint64
	Data []byte
	// Gap is true when the requested `after` sequence was already
	// overwritten: the caller missed bytes that can never be recovered
	// and must resume from Start instead.
	Gap bool
}

// Ring is a fixed-capacity, bounded, in-memory byte ring buffer.
// Append never blocks or grows the buffer; once full, the oldest bytes
// are silently overwritten. It is safe for concurrent use.
type Ring struct {
	mu    sync.Mutex
	buf   []byte
	total uint64 // total bytes ever written
}

// NewRing constructs a Ring with the given fixed byte capacity.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("transcript: ring capacity must be positive")
	}
	return &Ring{buf: make([]byte, capacity)}
}

// Append copies data into the ring, overwriting the oldest bytes once
// the ring is full. It never waits on a reader.
func (r *Ring) Append(data []byte) {
	if len(data) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	capacity := uint64(len(r.buf))
	if uint64(len(data)) >= capacity {
		// Only the tail can survive this single append; everything
		// before it is overwritten before any reader could observe it.
		// The tail is exactly `capacity` bytes, so writing it starting
		// at the physical position its first byte's sequence number
		// maps to fills the ring exactly once around.
		tail := data[uint64(len(data))-capacity:]
		startPos := (r.total + uint64(len(data)) - capacity) % capacity
		first := capacity - startPos
		copy(r.buf[startPos:], tail[:first])
		copy(r.buf[:capacity-first], tail[first:])
		r.total += uint64(len(data))
		return
	}

	start := r.total % capacity
	n := uint64(len(data))
	first := capacity - start
	if first > n {
		first = n
	}
	copy(r.buf[start:start+first], data[:first])
	if n > first {
		copy(r.buf[:n-first], data[first:])
	}
	r.total += n
}

// ReadAfter returns up to max bytes of ring contents starting at
// sequence `after`, or from the earliest retained sequence if `after`
// has already been overwritten (Chunk.Gap is set in that case).
func (r *Ring) ReadAfter(after uint64, max int) Chunk {
	r.mu.Lock()
	defer r.mu.Unlock()

	capacity := uint64(len(r.buf))
	retainedStart := uint64(0)
	if r.total > capacity {
		retainedStart = r.total - capacity
	}

	gap := after < retainedStart
	from := after
	if gap {
		from = retainedStart
	}
	if from > r.total {
		from = r.total
	}

	available := r.total - from
	n := available
	if max >= 0 && uint64(max) < n {
		n = uint64(max)
	}

	data := r.readRange(from, int(n))
	return Chunk{Start: from, Next: from + n, Data: data, Gap: gap}
}

// readRange copies n bytes of ring contents starting at logical
// sequence `from`. Callers must hold r.mu and ensure the requested
// range is within [retainedStart, total].
func (r *Ring) readRange(from uint64, n int) []byte {
	out := make([]byte, n)
	if n == 0 {
		return out
	}
	capacity := uint64(len(r.buf))
	start := from % capacity
	first := int(capacity - start)
	if first > n {
		first = n
	}
	copy(out[:first], r.buf[start:int(start)+first])
	if n > first {
		copy(out[first:], r.buf[:n-first])
	}
	return out
}
