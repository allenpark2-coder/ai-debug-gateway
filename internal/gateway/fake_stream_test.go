package gateway

import (
	"bytes"
	"io"
	"sync"

	"github.com/allenpark2-coder/ai-debug-gateway/internal/transport"
)

// fakeStream is a minimal in-memory transport.Stream for tests: feed
// pushes bytes for Read to return, failRead injects the next Read
// error (simulating EOF/hangup/ENODEV/EIO), and writtenSoFar exposes
// everything written to it (simulating what the target would have
// received).
type fakeStream struct {
	identity transport.Identity

	data  chan []byte
	err   chan error
	close chan struct{}
	once  sync.Once

	writeMu  sync.Mutex
	written  bytes.Buffer
	writeErr error
}

func newFakeStream(identity transport.Identity) *fakeStream {
	return &fakeStream{
		identity: identity,
		data:     make(chan []byte, 64),
		err:      make(chan error, 1),
		close:    make(chan struct{}),
	}
}

func (f *fakeStream) Read(p []byte) (int, error) {
	select {
	case chunk, ok := <-f.data:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, chunk)
		return n, nil
	case err := <-f.err:
		return 0, err
	case <-f.close:
		return 0, io.EOF
	}
}

func (f *fakeStream) Write(p []byte) (int, error) {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.written.Write(p)
}

func (f *fakeStream) Close() error {
	f.once.Do(func() { close(f.close) })
	return nil
}

func (f *fakeStream) Identity() transport.Identity { return f.identity }
func (f *fakeStream) Kind() string                 { return "fake" }

// feed makes the next Read return data. Callers must keep each chunk
// no larger than the coordinator's read buffer.
func (f *fakeStream) feed(data []byte) { f.data <- append([]byte(nil), data...) }

// failRead makes the next Read return err instead of data.
func (f *fakeStream) failRead(err error) { f.err <- err }

func (f *fakeStream) writtenSoFar() []byte {
	f.writeMu.Lock()
	defer f.writeMu.Unlock()
	return append([]byte(nil), f.written.Bytes()...)
}
