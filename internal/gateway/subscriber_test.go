package gateway

import (
	"testing"
	"time"
)

func TestSubscriberDropsWhenFullInsteadOfBlocking(t *testing.T) {
	s := newSubscriber()
	for i := 0; i < subscriberQueueSize; i++ {
		s.publish(Event{Data: []byte("x")})
	}
	if s.dropped.Load() != 0 {
		t.Fatalf("did not expect drops while under capacity, got %d", s.dropped.Load())
	}

	done := make(chan struct{})
	go func() {
		s.publish(Event{Data: []byte("overflow")}) // must return promptly, not block
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publish blocked on a full channel instead of dropping")
	}

	if s.dropped.Load() != 1 {
		t.Fatalf("got dropped=%d, want 1", s.dropped.Load())
	}
}
