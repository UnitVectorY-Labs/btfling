package ble

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type ring struct {
	data       [WindowSize]byte
	head, size int
}

func (r *ring) put(p []byte) int {
	n := min(len(p), WindowSize-r.size)
	tail := (r.head + r.size) % WindowSize
	k := copy(r.data[tail:], p[:n])
	copy(r.data[:], p[k:n])
	r.size += n
	return n
}
func (r *ring) get(p []byte) int {
	n := min(len(p), r.size)
	k := copy(p[:n], r.data[r.head:min(WindowSize, r.head+n)])
	copy(p[k:n], r.data[:n-k])
	r.head = (r.head + n) % WindowSize
	r.size -= n
	return n
}

// Metrics is a consistent snapshot. Byte counts exclude transport headers.
// Sent bytes are submitted stream bytes, not an over-the-air delivery receipt.
// ControlFrames counts outgoing controls; Writes/Notifications count both native
// receive callbacks and successful local native submissions.
type Metrics struct {
	MaxPayload                                                       int
	BytesSent, BytesReceived, SendWindow, ReceiveCredit, Outstanding uint64
	FramesSent, FramesReceived, ControlFrames                        uint64
	Writes, Notifications                                            uint64
	Elapsed                                                          time.Duration
	Error                                                            string
}

// stream has one writer pump and no retransmission queue. send must accept a frame
// in order, honor OS admission, and respect ctx; it must not retain the slice.
type stream struct {
	ctx    context.Context
	cancel context.CancelFunc
	stop   func() error
	drain  func(context.Context) error

	mu      sync.Mutex
	writeMu sync.Mutex
	changed chan struct{}

	rx, tx                         ring
	session, peerSession           uint64
	queued, sent, acked            uint64
	received, consumed             uint64
	peerInitialLimit, peerLimit    uint64
	advertisedACK, advertisedLimit uint64

	opened, peerOpened            bool
	finRequested, finSent, finACK bool
	peerFIN, finACKSent           bool
	err                           error
	maxPayload                    int
	rd, wd                        time.Time
	metrics                       Metrics
	started                       time.Time

	ackGeneration uint64
	ackTimer      *time.Timer
	ackDue        bool
	closeOnce     sync.Once
}

var _ net.Conn = (*stream)(nil)

func newStream(parent context.Context, valueSize int, send func(context.Context, []byte) error, stop func() error) (*stream, error) {
	payload, e := payloadSize(valueSize)
	if e != nil {
		return nil, e
	}
	var nonce [8]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return nil, e
	}
	session := binary.BigEndian.Uint64(nonce[:])
	if session == 0 {
		session = 1
	}
	ctx, cancel := context.WithCancel(parent)
	s := &stream{ctx: ctx, cancel: cancel, stop: stop, session: session, maxPayload: payload, changed: make(chan struct{}), started: time.Now()}
	go func() { <-ctx.Done(); s.fail(ctx.Err()) }()
	go s.pump(send)
	return s, nil
}
func (s *stream) signal() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// Allocate a broadcast generation only when a goroutine actually needs to wait.
// Caller holds mu, preventing lost wakeups between checking state and watching.
func (s *stream) watch() <-chan struct{} {
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}
func (s *stream) fail(e error) {
	if e == nil {
		e = io.ErrUnexpectedEOF
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = e
		if s.ackTimer != nil {
			s.ackGeneration++
			s.ackTimer.Stop()
			s.ackTimer = nil
		}
		s.signal()
	}
	s.mu.Unlock()
	s.cancel()
}
func (s *stream) wait(ch <-chan struct{}, deadline time.Time) error {
	if deadline.IsZero() {
		select {
		case <-ch:
			return nil
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	d := time.Until(deadline)
	if d <= 0 {
		return os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	case <-timer.C:
		return os.ErrDeadlineExceeded
	}
}
func (s *stream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if !s.rd.IsZero() && !time.Now().Before(s.rd) {
			return 0, os.ErrDeadlineExceeded
		}
		if s.rx.size > 0 {
			n := s.rx.get(p)
			s.consumed += uint64(n)
			s.signal()
			return n, nil
		}
		if s.err != nil {
			return 0, s.err
		}
		if s.peerFIN {
			return 0, io.EOF
		}
		ch, d := s.watch(), s.rd
		s.mu.Unlock()
		e := s.wait(ch, d)
		s.mu.Lock()
		if e != nil {
			return 0, e
		}
	}
}
func (s *stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for len(p) > 0 {
		if !s.wd.IsZero() && !time.Now().Before(s.wd) {
			return total, os.ErrDeadlineExceeded
		}
		if s.err != nil {
			return total, s.err
		}
		if s.finRequested {
			return total, io.ErrClosedPipe
		}
		if s.queued > ^uint64(0)-uint64(len(p))-WindowSize {
			return total, errors.New("BLE stream offset exhausted")
		}
		if s.tx.size < WindowSize {
			n := s.tx.put(p)
			s.queued += uint64(n)
			total += n
			p = p[n:]
			s.signal()
			continue
		}
		ch, d := s.watch(), s.wd
		s.mu.Unlock()
		e := s.wait(ch, d)
		s.mu.Lock()
		if e != nil {
			return total, e
		}
	}
	return total, nil
}

// Flush waits for cumulative receipt of all bytes accepted before this call.
func (s *stream) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	target := s.queued
	for s.acked < target {
		if s.err != nil {
			return s.err
		}
		ch, d := s.watch(), s.wd
		s.mu.Unlock()
		e := s.wait(ch, d)
		s.mu.Lock()
		if e != nil {
			return e
		}
	}
	return nil
}

// CloseWrite preserves reads and sends an ordered end-of-stream after queued data.
func (s *stream) CloseWrite() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finRequested = true
	s.signal()
	for !s.finACK {
		if s.err != nil {
			return s.err
		}
		ch, d := s.watch(), s.wd
		s.mu.Unlock()
		e := s.wait(ch, d)
		s.mu.Lock()
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		s.fail(net.ErrClosed)
		if s.stop != nil {
			_ = s.stop()
		}
	})
	return nil
}
func (s *stream) Done() <-chan struct{} { return s.ctx.Done() }
func (*stream) LocalAddr() net.Addr     { return bleAddress{} }
func (*stream) RemoteAddr() net.Addr    { return bleAddress{} }

type bleAddress struct{}

func (bleAddress) Network() string { return "bluetooth" }
func (bleAddress) String() string  { return "btfling" }
func (s *stream) SetDeadline(t time.Time) error {
	s.mu.Lock()
	s.rd = t
	s.wd = t
	s.signal()
	s.mu.Unlock()
	return nil
}
func (s *stream) SetReadDeadline(t time.Time) error {
	s.mu.Lock()
	s.rd = t
	s.signal()
	s.mu.Unlock()
	return nil
}
func (s *stream) SetWriteDeadline(t time.Time) error {
	s.mu.Lock()
	s.wd = t
	s.signal()
	s.mu.Unlock()
	return nil
}
func (s *stream) setValueSize(n int) error {
	p, e := payloadSize(n)
	if e != nil {
		return e
	}
	s.mu.Lock()
	s.maxPayload = p
	s.signal()
	s.mu.Unlock()
	return nil
}

func (s *stream) accept(p []byte) error {
	f, e := decodeFrame(p)
	if e != nil {
		s.fail(e)
		return e
	}
	s.mu.Lock()
	e = s.acceptLocked(f)
	if e == nil {
		s.metrics.FramesReceived++
		s.signal()
	}
	s.mu.Unlock()
	if e != nil {
		s.fail(e)
	}
	return e
}
func (s *stream) acceptLocked(f frame) error {
	violation := func() error { return errors.New("BLE stream protocol violation: session, offset, or receive window") }
	if s.err != nil {
		return s.err
	}
	if !s.peerOpened {
		if f.flags != flagOpen || f.limit == 0 || f.limit > WindowSize {
			return violation()
		}
		s.peerSession = f.session
		s.peerOpened = true
		s.peerLimit = f.limit
		s.peerInitialLimit = f.limit
		return nil
	}
	if f.session != s.peerSession {
		return violation()
	}
	if f.flags&flagOpen != 0 {
		if f.limit == s.peerInitialLimit {
			return nil
		}
		return violation()
	}
	// Duplicate control notifications may carry an older stream offset and window.
	// Ignore stale cumulative state; never retract a previously accepted limit.
	if len(f.data) == 0 && f.flags == 0 && f.offset <= s.received && f.ack <= s.acked && f.limit <= s.peerLimit {
		if f.limit < f.ack || f.limit-f.ack > WindowSize {
			return violation()
		}
		return nil
	}
	if len(f.data) > 0 && f.offset < s.received && f.offset+uint64(len(f.data)) >= f.offset && f.offset+uint64(len(f.data)) <= s.received && f.flags == 0 {
		if f.ack > s.sent || f.limit < f.ack || f.limit-f.ack > WindowSize {
			return violation()
		}
		return nil
	}
	// Repeated terminal frames are harmless once that exact end offset is known.
	if f.flags&flagFIN != 0 && s.peerFIN && f.offset+uint64(len(f.data)) >= f.offset && f.offset+uint64(len(f.data)) == s.received && f.ack <= s.acked && f.limit <= s.peerLimit {
		if f.limit < f.ack || f.limit-f.ack > WindowSize {
			return violation()
		}
		if f.flags&flagFINACK != 0 && (!s.finSent || f.ack != s.sent) {
			return violation()
		}
		if f.flags&flagFINACK != 0 {
			s.finACK = true
		}
		return nil
	}
	// Absolute limits must be monotonic, and cannot advertise more than one
	// bounded window beyond bytes cumulatively acknowledged.
	if f.ack < s.acked || f.ack > s.sent || f.limit < s.peerLimit || f.limit < f.ack || f.limit-f.ack > WindowSize {
		return violation()
	}
	if f.flags&flagFINACK != 0 && (!s.finSent || f.ack != s.sent) {
		return violation()
	}
	end := f.offset + uint64(len(f.data))
	if end < f.offset || end > ^uint64(0)-WindowSize || f.offset > s.received || end > s.advertisedLimit {
		return violation()
	}
	if len(f.data) > 0 {
		if end <= s.received { // A fully duplicated range is never delivered twice.
			if f.flags&flagFIN != 0 && (!s.peerFIN || end != s.received) {
				return violation()
			}
		} else {
			if f.offset != s.received || s.peerFIN || s.rx.put(f.data) != len(f.data) {
				return violation()
			}
			s.received = end
			s.metrics.BytesReceived += uint64(len(f.data))
		}
	} else if f.offset != s.received {
		return violation()
	}
	if f.flags&flagFIN != 0 {
		if end != s.received {
			return violation()
		}
		s.peerFIN = true
	}
	s.acked = f.ack
	s.peerLimit = f.limit
	if f.flags&flagFINACK != 0 {
		s.finACK = true
	}
	return nil
}
func (s *stream) pump(send func(context.Context, []byte) error) {
	buf := make([]byte, maxValueSize)
	for {
		s.mu.Lock()
		if s.err != nil {
			s.mu.Unlock()
			return
		}
		limit := s.consumed + WindowSize
		f := frame{session: s.session, offset: s.sent, ack: s.received, limit: limit}
		emit := false
		if !s.opened {
			f.flags = flagOpen
			s.opened = true
			emit = true
		} else if s.peerOpened {
			n := min(s.tx.size, s.maxPayload, int(s.peerLimit-s.sent))
			if n > 0 {
				f.data = buf[HeaderSize : HeaderSize+n]
				s.tx.get(f.data)
				s.sent += uint64(n)
				s.metrics.BytesSent += uint64(n)
				emit = true
			}
			if s.finRequested && s.tx.size == 0 && !s.finSent {
				f.flags |= flagFIN
				s.finSent = true
				emit = true
			}
			if s.peerFIN {
				f.flags |= flagFINACK
				if !s.finACKSent {
					emit = true
				}
			}
			// ACKs need not gate a sender. Batch receipt and credit advertisements;
			// a delayed update and FIN ensure small requests never stall.
			if s.ackDue || s.received-s.advertisedACK >= WindowSize/4 || limit-s.advertisedLimit >= WindowSize/4 {
				emit = true
			}
		}
		if !emit {
			if s.peerOpened && (s.received != s.advertisedACK || limit != s.advertisedLimit) {
				s.scheduleACK()
			}
			ch := s.watch()
			s.mu.Unlock()
			if s.wait(ch, time.Time{}) != nil {
				return
			}
			continue
		}
		s.ackDue = false
		if s.ackTimer != nil {
			s.ackGeneration++
			s.ackTimer.Stop()
			s.ackTimer = nil
		}
		s.advertisedACK = f.ack
		s.advertisedLimit = f.limit
		s.signal()
		s.mu.Unlock()
		// Header and data share one reusable pump buffer. No per-frame stream allocation.
		p := f.encode(buf)
		if e := send(s.ctx, p); e != nil {
			s.fail(e)
			return
		}
		s.mu.Lock()
		s.metrics.FramesSent++
		if len(f.data) == 0 {
			s.metrics.ControlFrames++
		}
		if f.flags&flagFINACK != 0 {
			s.finACKSent = true
		}
		s.signal()
		s.mu.Unlock()
	}
}
func (s *stream) Stats() Metrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.metrics
	m.MaxPayload = s.maxPayload
	m.Outstanding = s.sent - s.acked
	m.SendWindow = s.peerLimit - s.sent
	m.ReceiveCredit = WindowSize - uint64(s.rx.size)
	m.Elapsed = time.Since(s.started)
	if s.err != nil {
		m.Error = s.err.Error()
	}
	return m
}

// The delayed ACK is a one-shot coalescing timer, not a polling/data-path clock.
// It guarantees Flush and small credit releases progress even if the reader stops.
// Caller holds mu.
func (s *stream) scheduleACK() {
	if s.ackTimer != nil || s.ackDue || s.err != nil {
		return
	}
	s.ackGeneration++
	generation := s.ackGeneration
	s.ackTimer = time.AfterFunc(10*time.Millisecond, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if generation != s.ackGeneration {
			return
		}
		s.ackTimer = nil
		if s.err == nil {
			s.ackDue = true
			s.signal()
		}
	})
}

// Shutdown finishes both byte directions. Application completion is a separate
// TLS record; transport EOF by itself never proves a successful file transfer.
func (s *stream) Shutdown() error {
	if e := s.CloseWrite(); e != nil {
		return e
	}
	s.mu.Lock()
	for !s.peerFIN || !s.finACKSent {
		if s.err != nil {
			e := s.err
			s.mu.Unlock()
			return e
		}
		ch, d := s.watch(), s.wd
		s.mu.Unlock()
		e := s.wait(ch, d)
		s.mu.Lock()
		if e != nil {
			s.mu.Unlock()
			return e
		}
	}
	deadline := s.wd
	s.mu.Unlock()
	if s.drain != nil {
		ctx := s.ctx
		var cancel context.CancelFunc
		if !deadline.IsZero() {
			ctx, cancel = context.WithDeadline(ctx, deadline)
			defer cancel()
		}
		return s.drain(ctx)
	}
	return nil
}
