package ble

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

func TestMailboxBinaryDuplexWithDuplicateAndStaleReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := newStream(ctx, cancel, func() error { return nil })
	b := newStream(ctx, cancel, func() error { return nil })
	defer a.Close()
	defer b.Close()
	go func() {
		var stale []byte
		count := 0
		for ctx.Err() == nil {
			req := a.m.packet()
			if e := b.m.accept(req); e != nil {
				panic(e)
			}
			if e := b.m.accept(req); e != nil {
				panic(e)
			}
			response := b.m.packet()
			count++
			if count%3 == 0 && len(stale) > 0 {
				response = stale
			} else {
				stale = response
			}
			if e := a.m.accept(response); e != nil {
				panic(e)
			}
			time.Sleep(time.Microsecond)
		}
	}()
	pa := bytes.Repeat([]byte{0, 1, 255, 128}, 500)
	pb := bytes.Repeat([]byte{3, 0, 254}, 700)
	writes := make(chan error, 2)
	go func() { _, e := a.Write(pa); writes <- e }()
	go func() { _, e := b.Write(pb); writes <- e }()
	reads := make(chan []byte, 2)
	go func() {
		v := make([]byte, len(pb))
		_, e := io.ReadFull(a, v)
		if e != nil {
			v = nil
		}
		reads <- v
	}()
	go func() {
		v := make([]byte, len(pa))
		_, e := io.ReadFull(b, v)
		if e != nil {
			v = nil
		}
		reads <- v
	}()
	for range 2 {
		if e := <-writes; e != nil {
			t.Fatal(e)
		}
	}
	for range 2 {
		p := <-reads
		if !bytes.Equal(p, pa) && !bytes.Equal(p, pb) {
			t.Fatal("binary mismatch")
		}
	}
}
func TestMailboxCancellationUnblocksReadAndWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newStream(ctx, cancel, func() error { return nil })
	defer s.Close()
	done := make(chan error, 2)
	go func() { _, e := s.Write([]byte{1}); done <- e }()
	go func() { _, e := s.Read(make([]byte, 1)); done <- e }()
	cancel()
	for range 2 {
		select {
		case e := <-done:
			if e == nil {
				t.Fatal("missing cancellation error")
			}
		case <-time.After(time.Second):
			t.Fatal("blocked on cancellation")
		}
	}
}
func TestMailboxRejectsInvalidPacketsAndAppliesBackpressure(t *testing.T) {
	m := newMailbox(context.Background())
	for _, p := range [][]byte{{}, make([]byte, 21), {0, 0, 0, 2, 0, 0, 0, 0, 1}, {0, 0, 0, 0, 0, 0, 0, 1}} {
		if e := m.accept(p); e == nil {
			t.Fatal("accepted invalid packet")
		}
	}
	for i := uint32(1); i <= 65; i++ {
		p := make([]byte, 9)
		binary.BigEndian.PutUint32(p, i)
		if e := m.accept(p); e != nil {
			t.Fatal(e)
		}
	}
	if m.received != 64 {
		t.Fatal("acknowledged data without queue capacity")
	}
	<-m.incoming
	p := make([]byte, 9)
	binary.BigEndian.PutUint32(p, 65)
	if e := m.accept(p); e != nil {
		t.Fatal(e)
	}
	if m.received != 65 {
		t.Fatal("did not recover from backpressure")
	}
}

// Exercise the complete protocol, including TLS, through minimum-MTU GATT values.
func TestMailboxEncryptedProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a := newStream(ctx, cancel, func() error { return nil })
	b := newStream(ctx, cancel, func() error { return nil })
	defer a.Close()
	defer b.Close()
	pumpErrors := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			if e := b.m.accept(a.m.packet()); e != nil {
				pumpErrors <- e
				cancel()
				return
			}
			if e := a.m.accept(b.m.packet()); e != nil {
				pumpErrors <- e
				cancel()
				return
			}
			time.Sleep(time.Microsecond)
		}
	}()
	alice, e := config.Init(t.TempDir(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	bob, e := config.Init(t.TempDir(), "bob")
	if e != nil {
		t.Fatal(e)
	}
	alice.Keygen()
	bob.Keygen()
	ap := config.Peer{Name: alice.Name, ID: alice.ID, PublicKey: alice.PublicKey()}
	bp := config.Peer{Name: bob.Name, ID: bob.ID, PublicKey: bob.PublicKey()}
	payload := bytes.Repeat([]byte{0, 255, 128, 7}, 1000)
	var got bytes.Buffer
	done := make(chan error, 1)
	go func() {
		s, e := protocol.Open(ctx, b, bob, &ap, false, "send")
		if e == nil {
			e = protocol.Receive(s, &got, nil, nil)
		}
		done <- e
	}()
	s, e := protocol.Open(ctx, a, alice, &bp, true, "send")
	if e == nil {
		e = protocol.Send(s, bytes.NewReader(payload), nil)
	}
	if e != nil {
		t.Fatal(e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(payload, got.Bytes()) {
		t.Fatal("payload changed across GATT/TLS")
	}
	select {
	case e := <-pumpErrors:
		t.Fatal(e)
	default:
	}
}
