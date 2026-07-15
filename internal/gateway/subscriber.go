// Package gateway coordinates one board session: it wires the
// platform-independent session, command, transcript, audit, and secret
// packages to a transport.Stream, appends a completion marker to
// managed commands, and distinguishes a recognized target reboot from
// loss of the host transport itself.
package gateway

import "sync/atomic"

// Event is a byte-oriented notification delivered to a live subscriber.
type Event struct {
	Data []byte
}

// subscriberQueueSize bounds how many undelivered events a subscriber
// may hold before publish starts dropping instead of blocking.
const subscriberQueueSize = 256

// subscriber is a bounded, non-blocking fan-out channel. publish drops
// the event and counts it in dropped rather than waiting on a slow
// consumer, so the transport read loop it is fed from never blocks.
type subscriber struct {
	ch      chan Event
	dropped atomic.Uint64
}

func newSubscriber() *subscriber {
	return &subscriber{ch: make(chan Event, subscriberQueueSize)}
}

func (s *subscriber) publish(e Event) {
	select {
	case s.ch <- e:
	default:
		s.dropped.Add(1)
	}
}

// Events returns the channel to receive from.
func (s *subscriber) Events() <-chan Event { return s.ch }

// Dropped returns how many events have been dropped because the
// consumer fell behind.
func (s *subscriber) Dropped() uint64 { return s.dropped.Load() }
