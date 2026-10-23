// Package ble provides a Bluetooth-only byte stream using a stop-and-wait GATT mailbox.
package ble

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

// Each ATT value fits the mandatory 23-byte MTU (20-byte attribute value).
// Sequence and acknowledgement are uint32, followed by up to 12 stream bytes.
const packetSize = 20

type mailbox struct {
	ctx            context.Context
	mu             sync.Mutex
	pending        []byte
	sent, received uint32
	ack            chan struct{}
	incoming       chan []byte
}

func newMailbox(ctx context.Context) *mailbox {
	return &mailbox{ctx: ctx, ack: make(chan struct{}, 1), incoming: make(chan []byte, 64)}
}
func (m *mailbox) packet() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := make([]byte, 8, len(m.pending)+8)
	binary.BigEndian.PutUint32(p, m.sent)
	binary.BigEndian.PutUint32(p[4:], m.received)
	return append(p, m.pending...)
}
func (m *mailbox) accept(p []byte) error {
	if len(p) < 8 || len(p) > packetSize {
		return errors.New("invalid Bluetooth mailbox packet")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seq, ack := binary.BigEndian.Uint32(p), binary.BigEndian.Uint32(p[4:])
	if ack > m.sent {
		return errors.New("invalid Bluetooth acknowledgement")
	}
	if len(m.pending) > 0 && ack == m.sent {
		m.pending = nil
		select {
		case m.ack <- struct{}{}:
		default:
		}
	}
	if len(p) > 8 && seq != m.received {
		if seq != m.received+1 || seq == 0 {
			return errors.New("Bluetooth stream sequence mismatch")
		}
		select {
		case m.incoming <- append([]byte(nil), p[8:]...):
			m.received = seq
		default:
		} // backpressure: withhold ack until queue has room
	}
	return nil
}
func (m *mailbox) write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(12, len(p))
		m.mu.Lock()
		if m.sent == ^uint32(0) {
			m.mu.Unlock()
			return total, errors.New("Bluetooth stream exceeds sequence limit")
		}
		m.sent++
		m.pending = append([]byte(nil), p[:n]...)
		m.mu.Unlock()
		select {
		case <-m.ack:
			total += n
			p = p[n:]
		case <-m.ctx.Done():
			return total, m.ctx.Err()
		}
	}
	return total, nil
}

type stream struct {
	m         *mailbox
	reader    *io.PipeReader
	writer    *io.PipeWriter
	cancel    context.CancelFunc
	close     func() error
	writeMu   sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func newStream(ctx context.Context, cancel context.CancelFunc, closeFn func() error) *stream {
	r, w := io.Pipe()
	s := &stream{m: newMailbox(ctx), reader: r, writer: w, cancel: cancel, close: closeFn}
	go func() { <-ctx.Done(); w.CloseWithError(ctx.Err()) }()
	go func() {
		defer w.Close()
		for {
			select {
			case p := <-s.m.incoming:
				if _, e := w.Write(p); e != nil {
					return
				}
			case <-ctx.Done():
				w.CloseWithError(ctx.Err())
				return
			}
		}
	}()
	return s
}
func (s *stream) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *stream) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.m.write(p)
}
func (s *stream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.close(); s.cancel(); s.reader.Close() })
	return s.closeErr
}

// Done lets a caller cancel blocked local file I/O when the radio session fails.
func (s *stream) Done() <-chan struct{} { return s.m.ctx.Done() }
