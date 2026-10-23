package ble

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// simLink models ordered GATT submission separately from delivery latency. A
// bounded queue models OS backpressure. Delivery times are stamped on admission,
// so connection latency does not incorrectly serialize every radio fragment.
// Hooks can duplicate, corrupt, delay control updates, or disconnect either path.
type simOptions struct {
	controlGate           <-chan struct{}
	packetsPerEvent       int
	eventInterval         time.Duration
	value, queue          int
	latency, controlDelay time.Duration
	duplicate             bool
	mutate                func([]byte) []byte
	disconnectAfter       int
}
type simPacket struct {
	data []byte
	at   time.Time
}

func simulated(tb testing.TB, o simOptions) (*stream, *stream) {
	tb.Helper()
	if o.value == 0 {
		o.value = 244
	}
	if o.queue == 0 {
		o.queue = 128
	}
	ctx, cancel := context.WithCancel(context.Background())
	ab, ba := make(chan simPacket, o.queue), make(chan simPacket, o.queue)
	submit := func(q chan simPacket) func(context.Context, []byte) error {
		var nextEvent time.Time
		eventPackets := 0
		return func(ctx context.Context, p []byte) error {
			now := time.Now()
			at := now.Add(o.latency)
			if o.packetsPerEvent > 0 && o.eventInterval > 0 {
				if nextEvent.Before(now) {
					nextEvent = now.Add(o.eventInterval)
					eventPackets = 0
				}
				at = nextEvent.Add(o.latency)
				eventPackets++
				if eventPackets == o.packetsPerEvent {
					nextEvent = nextEvent.Add(o.eventInterval)
					eventPackets = 0
				}
			}
			packet := simPacket{data: append([]byte(nil), p...), at: at}
			if len(p) == HeaderSize && p[1]&flagOpen == 0 {
				packet.at = packet.at.Add(o.controlDelay)
			}
			select {
			case q <- packet:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	a, e := newStream(ctx, o.value, submit(ab), nil)
	if e != nil {
		tb.Fatal(e)
	}
	b, e := newStream(ctx, o.value, submit(ba), nil)
	if e != nil {
		tb.Fatal(e)
	}
	var wg sync.WaitGroup
	deliver := func(q chan simPacket, to *stream) {
		defer wg.Done()
		count := 0
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-q:
				if delay := time.Until(p.at); delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
				}
				if o.controlGate != nil && len(p.data) == HeaderSize && p.data[1]&flagOpen == 0 {
					select {
					case <-ctx.Done():
						return
					case <-o.controlGate:
					}
				}
				count++
				if o.disconnectAfter > 0 && count >= o.disconnectAfter {
					a.fail(errors.New("simulated peer disconnect"))
					b.fail(errors.New("simulated peer disconnect"))
					return
				}
				if o.mutate != nil {
					p.data = o.mutate(p.data)
				}
				if e := to.accept(p.data); e != nil {
					a.fail(e)
					b.fail(e)
					return
				}
				if o.duplicate && len(p.data) > HeaderSize {
					if e := to.accept(p.data); e != nil {
						a.fail(e)
						b.fail(e)
						return
					}
				}
			}
		}
	}
	wg.Add(2)
	go deliver(ab, b)
	go deliver(ba, a)
	tb.Cleanup(func() { cancel(); a.Close(); b.Close(); wg.Wait() })
	deadline := time.Now().Add(15 * time.Second)
	a.SetDeadline(deadline)
	b.SetDeadline(deadline)
	return a, b
}
