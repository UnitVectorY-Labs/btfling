package ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

func TestFrameAndPayload(t *testing.T) {
	for _, n := range []int{0, 20, 36} {
		if _, e := payloadSize(n); e == nil {
			t.Fatalf("accepted %d", n)
		}
	}
	for _, n := range []int{37, 64, 185, 244, 512, 1000} {
		got, e := payloadSize(n)
		if e != nil || got != min(512, n)-HeaderSize {
			t.Fatal(n, got, e)
		}
	}
	f := frame{session: 42, offset: 123, ack: 7, limit: WindowSize, data: []byte{1, 0, 255}}
	p := f.encode(make([]byte, maxValueSize))
	decoded, e := decodeFrame(p)
	if e != nil || decoded.session != f.session || decoded.offset != f.offset || decoded.ack != f.ack || decoded.limit != f.limit || !bytes.Equal(decoded.data, f.data) {
		t.Fatal(decoded, e)
	}
	for _, modify := range []func([]byte) []byte{
		func(p []byte) []byte { return p[:35] }, func(p []byte) []byte { return append(p, make([]byte, 513)...) },
		func(p []byte) []byte { p[0] = 99; return p }, func(p []byte) []byte { p[1] = 128; return p },
		func(p []byte) []byte { p[2] = 1; return p }, func(p []byte) []byte { clear(p[4:12]); return p },
		func(p []byte) []byte { p[1] = flagOpen; return p },
	} {
		if _, e := decodeFrame(modify(append([]byte(nil), p...))); e == nil {
			t.Fatal("accepted malformed frame")
		}
	}
}
func TestDuplexFragmentationPartialReadsAndDuplicates(t *testing.T) {
	for _, value := range []int{37, 64, 244, 512} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			a, b := simulated(t, simOptions{value: value, duplicate: true, queue: 16})
			payload := bytes.Repeat([]byte{0, 1, 255, 128, 7}, 18000)
			results := make(chan error, 4)
			for _, s := range []*stream{a, b} {
				go func() {
					_, e := s.Write(payload)
					if e == nil {
						e = s.CloseWrite()
					}
					results <- e
				}()
				go func() {
					var got bytes.Buffer
					_, e := io.CopyBuffer(&got, s, make([]byte, 137))
					if e == nil && !bytes.Equal(got.Bytes(), payload) {
						e = errors.New("duplex mismatch")
					}
					results <- e
				}()
			}
			for range 4 {
				if e := <-results; e != nil {
					t.Fatal(e)
				}
			}
			if a.Stats().BytesSent != uint64(len(payload)) || b.Stats().BytesReceived != uint64(len(payload)) {
				t.Fatal("metrics mismatch")
			}
			if _, e := a.Write([]byte{1}); !errors.Is(e, io.ErrClosedPipe) {
				t.Fatal(e)
			}
		})
	}
}
func TestReceiverWindowAndWriteBackpressure(t *testing.T) {
	a, b := simulated(t, simOptions{queue: 4, latency: time.Millisecond})
	a.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	n, e := a.Write(make([]byte, WindowSize*4))
	if !errors.Is(e, os.ErrDeadlineExceeded) || n < WindowSize || n > 2*WindowSize+maxValueSize {
		t.Fatal(n, e)
	}
	stats := b.Stats()
	if stats.BytesReceived > WindowSize || stats.ReceiveCredit+stats.BytesReceived != WindowSize {
		t.Fatal(stats)
	}
	a.SetWriteDeadline(time.Now().Add(5 * time.Second))
	consumed := make(chan error, 1)
	go func() { _, e := io.CopyN(io.Discard, b, int64(n)); consumed <- e }()
	if e := a.Flush(); e != nil {
		t.Fatal(e)
	}
	if e := <-consumed; e != nil {
		t.Fatal(e)
	}
}
func TestSmallUnreadPayloadIsAcknowledged(t *testing.T) {
	a, b := simulated(t, simOptions{})
	if _, e := a.Write([]byte("hello")); e != nil {
		t.Fatal(e)
	}
	if e := a.Flush(); e != nil {
		t.Fatal(e)
	}
	if b.Stats().BytesReceived != 5 {
		t.Fatal(b.Stats())
	}
}
func TestProtocolViolations(t *testing.T) {
	tests := map[string]func(*stream) frame{
		"gap":              func(s *stream) frame { return frame{offset: 1, data: []byte{1}} },
		"overflow":         func(s *stream) frame { s.received = WindowSize; return frame{offset: WindowSize, data: []byte{1}} },
		"future ACK":       func(s *stream) frame { return frame{ack: 1} },
		"regressing ACK":   func(s *stream) frame { s.sent = 2; s.acked = 1; return frame{data: []byte{1}} },
		"credit below ACK": func(s *stream) frame { s.sent = 2; return frame{ack: 2, limit: 1} },
		"oversized credit": func(s *stream) frame { return frame{limit: WindowSize + 1} },
		"shrinking limit":  func(s *stream) frame { s.sent = 1; return frame{ack: 1, limit: WindowSize - 1} },
		"stale session":    func(s *stream) frame { return frame{session: 99} },
		"overlap":          func(s *stream) frame { s.received = 2; return frame{offset: 1, data: []byte{1, 2}} },
		"premature FINACK": func(s *stream) frame { return frame{flags: flagFINACK} },
		"wrong EOF":        func(s *stream) frame { return frame{flags: flagFIN, offset: 1} },
		"changed OPEN":     func(s *stream) frame { return frame{flags: flagOpen, limit: WindowSize - 1} },
		"data after FIN":   func(s *stream) frame { s.peerFIN = true; return frame{data: []byte{1}} },
		"offset wrap":      func(s *stream) frame { return frame{offset: ^uint64(0), data: []byte{1}} },
	}
	for name, makeFrame := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, e := newStream(ctx, 244, func(context.Context, []byte) error { return nil }, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			if e = s.accept((frame{flags: flagOpen, session: 42, limit: WindowSize}).encode(make([]byte, 512))); e != nil {
				t.Fatal(e)
			}
			s.mu.Lock()
			f := makeFrame(s)
			if f.session == 0 {
				f.session = 42
			}
			if f.limit == 0 {
				f.limit = WindowSize
			}
			s.mu.Unlock()
			if e = s.accept(f.encode(make([]byte, 512))); e == nil {
				t.Fatal("accepted violation")
			}
			select {
			case <-s.Done():
			case <-time.After(time.Second):
				t.Fatal("failure not propagated")
			}
		})
	}
}
func TestCumulativeACKAndCredit(t *testing.T) {
	a, b := simulated(t, simOptions{controlDelay: 20 * time.Millisecond})
	payload := make([]byte, WindowSize)
	if _, e := a.Write(payload); e != nil {
		t.Fatal(e)
	}
	if e := a.Flush(); e != nil {
		t.Fatal(e)
	}
	m := a.Stats()
	if m.Outstanding != 0 || m.SendWindow != 0 {
		t.Fatal(m)
	}
	b.mu.Lock()
	if b.rx.size != WindowSize {
		t.Fatal(b.rx.size)
	}
	b.mu.Unlock()
	if _, e := io.CopyN(io.Discard, b, WindowSize); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := a.Write(payload)
		if e == nil {
			e = a.Flush()
		}
		done <- e
	}()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.received != 2*WindowSize || b.rx.size != WindowSize {
		t.Fatal(b.received, b.rx.size)
	}
}
func TestCancellationDisconnectAndDeadlines(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprint(disconnect), func(t *testing.T) {
			a, _ := simulated(t, simOptions{})
			done := make(chan error, 2)
			go func() { _, e := a.Read(make([]byte, 1)); done <- e }()
			go func() { _, e := a.Write(make([]byte, WindowSize*4)); done <- e }()
			if disconnect {
				a.fail(io.ErrUnexpectedEOF)
			} else {
				a.Close()
			}
			for range 2 {
				select {
				case e := <-done:
					if e == nil {
						t.Fatal("missing error")
					}
				case <-time.After(time.Second):
					t.Fatal("cancellation blocked")
				}
			}
		})
	}
	a, b := simulated(t, simOptions{})
	a.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	if _, e := a.Read(make([]byte, 1)); !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	a.SetReadDeadline(time.Time{})
	go b.Write([]byte{1})
	if _, e := a.Read(make([]byte, 1)); e != nil {
		t.Fatal(e)
	}
	// A changed deadline must wake an already waiting reader.
	done := make(chan error, 1)
	go func() { _, e := a.Read(make([]byte, 1)); done <- e }()
	a.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	if e := <-done; !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestGracefulShutdown(t *testing.T) {
	a, b := simulated(t, simOptions{latency: time.Millisecond})
	done := make(chan error, 2)
	for _, s := range []*stream{a, b} {
		go func() {
			_, e := s.Write([]byte("tail"))
			if e == nil {
				e = s.Shutdown()
			}
			done <- e
		}()
	}
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []*stream{a, b} {
		p, e := io.ReadAll(s)
		if e != nil || string(p) != "tail" {
			t.Fatal(string(p), e)
		}
	}
}
func TestSimulatedMalformedAndDisconnect(t *testing.T) {
	for _, o := range []simOptions{{disconnectAfter: 3}, {mutate: func(p []byte) []byte { p[0] = 99; return p }}} {
		a, _ := simulated(t, o)
		_, e := a.Write(make([]byte, WindowSize*4))
		if e == nil {
			t.Fatal("missing simulated failure")
		}
	}
}
func TestTLSLargeTransfer(t *testing.T) {
	a, b := simulated(t, simOptions{latency: 2 * time.Millisecond, queue: 64, duplicate: true, controlDelay: time.Millisecond})
	alice, e := config.Init(t.TempDir(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	if e = alice.Keygen(); e != nil {
		t.Fatal(e)
	}
	bob, e := config.Init(t.TempDir(), "bob")
	if e != nil {
		t.Fatal(e)
	}
	if e = bob.Keygen(); e != nil {
		t.Fatal(e)
	}
	ap := config.Peer{ID: alice.ID, Name: alice.Name, PublicKey: alice.PublicKey()}
	bp := config.Peer{ID: bob.ID, Name: bob.Name, PublicKey: bob.PublicKey()}
	payload := bytes.Repeat([]byte{0, 255, 128, 7}, 1<<18)
	var got bytes.Buffer
	done := make(chan error, 1)
	go func() {
		s, e := protocol.Open(a.ctx, b, bob, &ap, false, "send")
		if e == nil {
			e = protocol.Receive(s, &got, nil, nil)
		}
		done <- e
	}()
	s, e := protocol.Open(a.ctx, a, alice, &bp, true, "send")
	if e != nil {
		t.Fatal(e)
	}
	if e = protocol.Send(s, bytes.NewReader(payload), nil); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(payload, got.Bytes()) {
		t.Fatal("TLS payload mismatch")
	}
}
func TestConcurrentClose(t *testing.T) {
	a, b := simulated(t, simOptions{queue: 1, latency: time.Millisecond})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); a.SetDeadline(time.Now()); a.Close(); b.Close() }()
	}
	wg.Wait()
}
func FuzzFrame(f *testing.F) {
	f.Add((frame{flags: flagOpen, session: 1, limit: WindowSize}).encode(make([]byte, 512)))
	f.Fuzz(func(t *testing.T, p []byte) {
		v, e := decodeFrame(p)
		if e == nil {
			encoded := v.encode(make([]byte, 512))
			if !bytes.Equal(p, encoded) {
				t.Fatal("noncanonical frame")
			}
		}
	})
}
func TestVersionMismatch(t *testing.T) {
	p := (frame{session: 1}).encode(make([]byte, 512))
	binary.BigEndian.PutUint16(p, 0xff00)
	if _, e := decodeFrame(p); e == nil {
		t.Fatal("version accepted")
	}
}

func BenchmarkStream(b *testing.B) {
	for _, latency := range []time.Duration{0, 5 * time.Millisecond, 30 * time.Millisecond} {
		b.Run(latency.String(), func(b *testing.B) {
			a, peer := simulated(b, simOptions{value: 244, queue: 512, latency: latency})
			a.SetDeadline(time.Time{})
			peer.SetDeadline(time.Time{})
			payload := bytes.Repeat([]byte{0, 1, 2, 3}, 256*1024)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			done := make(chan error, 1)
			go func() { _, e := io.CopyN(io.Discard, peer, int64(b.N)*int64(len(payload))); done <- e }()
			b.ResetTimer()
			for range b.N {
				if _, e := a.Write(payload); e != nil {
					b.Fatal(e)
				}
			}
			if e := a.Flush(); e != nil {
				b.Fatal(e)
			}
			if e := <-done; e != nil {
				b.Fatal(e)
			}
			b.StopTimer()
		})
	}
}

func TestDelayedDuplicatesAndCreditRelease(t *testing.T) {
	a, b := simulated(t, simOptions{})
	if _, e := a.Write([]byte("first")); e != nil {
		t.Fatal(e)
	}
	if e := a.Flush(); e != nil {
		t.Fatal(e)
	}
	b.mu.Lock()
	session := b.peerSession
	initial := b.peerInitialLimit
	b.mu.Unlock()
	// Duplicate OPEN, an old data notification, and a stale control update must
	// neither duplicate bytes nor retract newer cumulative receive state.
	frames := []frame{{flags: flagOpen, session: session, limit: initial}, {session: session, limit: initial, data: []byte("first")}, {session: session, limit: initial}}
	for _, f := range frames {
		if e := b.accept(f.encode(make([]byte, 512))); e != nil {
			t.Fatal(e)
		}
	}
	p := make([]byte, 5)
	if _, e := io.ReadFull(b, p); e != nil || string(p) != "first" {
		t.Fatal(string(p), e)
	}
	// A release below the batching threshold must eventually advertise credit.
	timeLimit := time.Now().Add(time.Second)
	for a.Stats().SendWindow != WindowSize {
		if time.Now().After(timeLimit) {
			t.Fatal("small credit release lost")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNativeSizeChangeAndWriteFailure(t *testing.T) {
	a, b := simulated(t, simOptions{})
	if e := a.setValueSize(185); e != nil {
		t.Fatal(e)
	}
	if a.Stats().MaxPayload != 149 {
		t.Fatal(a.Stats())
	}
	if e := a.setValueSize(20); e == nil {
		t.Fatal("unsafe value size")
	}
	a.fail(errors.New("native GATT write failed"))
	if _, e := a.Write([]byte{1}); e == nil {
		t.Fatal("write failure lost")
	}
	b.Close()
}

func TestHalfCloseKeepsOtherDirectionOpen(t *testing.T) {
	a, b := simulated(t, simOptions{})
	if _, e := a.Write([]byte("request")); e != nil {
		t.Fatal(e)
	}
	if e := a.CloseWrite(); e != nil {
		t.Fatal(e)
	}
	p, e := io.ReadAll(b)
	if e != nil || string(p) != "request" {
		t.Fatal(string(p), e)
	}
	if _, e = b.Write([]byte("response")); e != nil {
		t.Fatal(e)
	}
	if e = b.CloseWrite(); e != nil {
		t.Fatal(e)
	}
	p, e = io.ReadAll(a)
	if e != nil || string(p) != "response" {
		t.Fatal(string(p), e)
	}
}

func tlsSessions(tb testing.TB, a, b *stream) (*protocol.Session, *protocol.Session) {
	tb.Helper()
	alice, e := config.Init(tb.TempDir(), "alice")
	if e != nil {
		tb.Fatal(e)
	}
	if e = alice.Keygen(); e != nil {
		tb.Fatal(e)
	}
	bob, e := config.Init(tb.TempDir(), "bob")
	if e != nil {
		tb.Fatal(e)
	}
	if e = bob.Keygen(); e != nil {
		tb.Fatal(e)
	}
	ap := config.Peer{ID: alice.ID, Name: alice.Name, PublicKey: alice.PublicKey()}
	bp := config.Peer{ID: bob.ID, Name: bob.Name, PublicKey: bob.PublicKey()}
	type result struct {
		s *protocol.Session
		e error
	}
	done := make(chan result, 1)
	go func() { s, e := protocol.Open(a.ctx, b, bob, &ap, false, "send"); done <- result{s, e} }()
	left, e := protocol.Open(a.ctx, a, alice, &bp, true, "send")
	if e != nil {
		tb.Fatal(e)
	}
	right := <-done
	if right.e != nil {
		tb.Fatal(right.e)
	}
	return left, right.s
}
func TestTLSAndTransportShutdown(t *testing.T) {
	a, b := simulated(t, simOptions{latency: time.Millisecond, queue: 16})
	left, right := tlsSessions(t, a, b)
	done := make(chan error, 2)
	for i, s := range []*protocol.Session{left, right} {
		transport := []*stream{a, b}[i]
		go func() {
			e := s.ReadWriter.(interface{ CloseWrite() error }).CloseWrite()
			transport.SetWriteDeadline(time.Now().Add(time.Second))
			if e == nil {
				p := make([]byte, 1)
				_, e = s.Read(p)
				if e == io.EOF {
					e = nil
				} else if e == nil {
					e = errors.New("expected TLS close_notify")
				}
			}
			if e == nil {
				e = transport.Shutdown()
			}
			done <- e
		}()
	}
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
}
func BenchmarkTLSStream(b *testing.B) {
	a, peer := simulated(b, simOptions{value: 244, queue: 512, latency: 5 * time.Millisecond})
	left, right := tlsSessions(b, a, peer)
	a.SetDeadline(time.Time{})
	peer.SetDeadline(time.Time{})
	payload := bytes.Repeat([]byte{0, 1, 2, 3}, 256*1024)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	done := make(chan error, 1)
	go func() {
		for range b.N {
			if e := protocol.Receive(right, io.Discard, nil, nil); e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	b.ResetTimer()
	for range b.N {
		if e := protocol.Send(left, bytes.NewReader(payload), nil); e != nil {
			b.Fatal(e)
		}
	}
	if e := <-done; e != nil {
		b.Fatal(e)
	}
	b.StopTimer()
}

func BenchmarkEventLimitedStream(b *testing.B) {
	for _, latency := range []time.Duration{5 * time.Millisecond, 30 * time.Millisecond} {
		b.Run(latency.String(), func(b *testing.B) {
			a, peer := simulated(b, simOptions{value: 244, queue: 128, latency: latency, packetsPerEvent: 8, eventInterval: 15 * time.Millisecond})
			a.SetDeadline(time.Time{})
			peer.SetDeadline(time.Time{})
			payload := bytes.Repeat([]byte{0, 1, 2, 3}, 64*1024)
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			done := make(chan error, 1)
			go func() { _, e := io.CopyN(io.Discard, peer, int64(b.N)*int64(len(payload))); done <- e }()
			b.ResetTimer()
			for range b.N {
				if _, e := a.Write(payload); e != nil {
					b.Fatal(e)
				}
			}
			if e := a.Flush(); e != nil {
				b.Fatal(e)
			}
			if e := <-done; e != nil {
				b.Fatal(e)
			}
			b.StopTimer()
		})
	}
}

func TestParentCancellationAndNativeSendError(t *testing.T) {
	for _, nativeError := range []bool{false, true} {
		t.Run(fmt.Sprint(nativeError), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			send := func(context.Context, []byte) error {
				if nativeError {
					return io.ErrUnexpectedEOF
				}
				return nil
			}
			s, e := newStream(ctx, 244, send, nil)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			results := make(chan error, 2)
			go func() { _, e := s.Read(make([]byte, 1)); results <- e }()
			go func() { _, e := s.Write(make([]byte, WindowSize*3)); results <- e }()
			if !nativeError {
				cancel()
			}
			for range 2 {
				select {
				case e := <-results:
					if e == nil {
						t.Fatal("missing cancellation/native error")
					}
				case <-time.After(time.Second):
					t.Fatal("blocked after failure")
				}
			}
		})
	}
}
func TestOpenValidation(t *testing.T) {
	for _, f := range []frame{{session: 1}, {session: 1, flags: flagOpen}, {session: 1, flags: flagOpen, limit: WindowSize + 1}} {
		ctx, cancel := context.WithCancel(context.Background())
		s, e := newStream(ctx, 244, func(context.Context, []byte) error { return nil }, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = s.accept(f.encode(make([]byte, 512))); e == nil {
			t.Fatal("accepted invalid OPEN", f)
		}
		s.Close()
		cancel()
	}
}
func TestCoalescingAndPartialRead(t *testing.T) {
	a, b := simulated(t, simOptions{})
	for _, p := range []string{"ab", "c", "def"} {
		if _, e := a.Write([]byte(p)); e != nil {
			t.Fatal(e)
		}
	}
	if e := a.Flush(); e != nil {
		t.Fatal(e)
	}
	p := make([]byte, 2)
	if n, e := b.Read(p); n != 2 || e != nil || string(p) != "ab" {
		t.Fatal(n, e, string(p))
	}
	p = make([]byte, 32)
	if n, e := b.Read(p); n != 4 || e != nil || string(p[:n]) != "cdef" {
		t.Fatal(n, e, string(p))
	}
}

func TestManyFragmentsInFlightBeforeACK(t *testing.T) {
	gate := make(chan struct{})
	a, b := simulated(t, simOptions{controlGate: gate, queue: 512})
	if _, e := a.Write(make([]byte, WindowSize)); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	b.mu.Lock()
	for b.received < WindowSize {
		ch := b.watch()
		b.mu.Unlock()
		e := b.wait(ch, deadline)
		if e != nil {
			t.Fatal(e)
		}
		b.mu.Lock()
	}
	b.mu.Unlock()
	if stats := a.Stats(); stats.Outstanding != WindowSize {
		t.Fatal("sender waited for fragment ACKs", stats)
	}
	if stats := b.Stats(); stats.FramesReceived < 300 {
		t.Fatal("fragmentation not exercised", stats)
	}
	close(gate)
}
