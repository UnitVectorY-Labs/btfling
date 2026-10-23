package protocol

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
)

func identities(t *testing.T) (*config.Config, *config.Config) {
	t.Helper()
	a, e := config.Init(t.TempDir(), "alice")
	if e != nil {
		t.Fatal(e)
	}
	b, e := config.Init(t.TempDir(), "bob")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Keygen(); e != nil {
		t.Fatal(e)
	}
	if e = b.Keygen(); e != nil {
		t.Fatal(e)
	}
	return a, b
}
func TestBinaryTransfer(t *testing.T) {
	for _, encrypted := range []bool{true} {
		t.Run(map[bool]string{false: "plain", true: "TLS13"}[encrypted], func(t *testing.T) {
			a, b := identities(t)
			ap := config.Peer{ID: a.ID, Name: a.Name}
			bp := config.Peer{ID: b.ID, Name: b.Name}
			if encrypted {
				ap.PublicKey = a.PublicKey()
				bp.PublicKey = b.PublicKey()
			}
			for _, payload := range [][]byte{nil, {0, 255, 1, 0}, bytes.Repeat([]byte{0, 255, 12, 34, 128}, 30000)} {
				x, y := net.Pipe()
				x.SetDeadline(time.Now().Add(10 * time.Second))
				y.SetDeadline(time.Now().Add(10 * time.Second))
				var received bytes.Buffer
				committed := false
				done := make(chan error, 1)
				go func() {
					defer y.Close()
					s, e := Open(context.Background(), y, b, &ap, false, "send")
					if e == nil {
						e = Receive(s, &received, func() error { committed = true; return nil }, nil)
					}
					done <- e
				}()
				s, e := Open(context.Background(), x, a, &bp, true, "send")
				if e == nil {
					e = Send(s, bytes.NewReader(payload), nil)
				}
				x.Close()
				if e != nil {
					t.Fatal(e)
				}
				if e = <-done; e != nil {
					t.Fatal(e)
				}
				if !committed || !bytes.Equal(payload, received.Bytes()) {
					t.Fatal("payload/commit mismatch")
				}
			}
		})
	}
}
func TestRejectEncryptionDowngradeAndWrongIdentity(t *testing.T) {
	for _, which := range []string{"missing key", "identity", "key"} {
		t.Run(which, func(t *testing.T) {
			a, b := identities(t)
			ap := config.Peer{ID: a.ID, PublicKey: a.PublicKey()}
			bp := config.Peer{ID: b.ID, PublicKey: b.PublicKey()}
			if which == "missing key" {
				ap.PublicKey = nil
			}
			if which == "identity" {
				ap.ID = "00000000000000000000000000000000"
			}
			if which == "key" {
				ap.PublicKey = bytes.Repeat([]byte{3}, 32)
			}
			x, y := net.Pipe()
			x.SetDeadline(time.Now().Add(time.Second))
			y.SetDeadline(time.Now().Add(time.Second))
			done := make(chan error, 1)
			go func() {
				defer y.Close()
				_, e := Open(context.Background(), y, b, &ap, false, "send")
				done <- e
			}()
			_, e := Open(context.Background(), x, a, &bp, true, "send")
			x.Close()
			remoteErr := <-done
			if e == nil && remoteErr == nil {
				t.Fatal("accepted incompatible or untrusted peer")
			}
		})
	}
}
func TestReceiveRejectsCorruptTruncatedAndUncommitted(t *testing.T) {
	for _, which := range []string{"digest", "length", "truncated", "commit"} {
		t.Run(which, func(t *testing.T) {
			var encoded bytes.Buffer
			WriteFrame(&encoded, []byte{1, 0, 255})
			end := make([]byte, 41)
			end[0] = 2
			binary.BigEndian.PutUint64(end[1:9], 2)
			if which == "length" {
				binary.BigEndian.PutUint64(end[1:9], 3)
			}
			if which == "truncated" {
				encoded.Write([]byte{0, 0})
			} else {
				WriteFrame(&encoded, end)
			}
			var output bytes.Buffer
			called := false
			e := Receive(&readWriter{Reader: &encoded, Writer: io.Discard}, &output, func() error { called = true; return errors.New("disk full") }, nil)
			if e == nil {
				t.Fatal("accepted invalid transfer")
			}
			if called {
				t.Fatal("committed unverified content")
			}
		})
	}
	// A valid transfer whose destination cannot be committed must fail at the sender too.
	x, y := net.Pipe()
	defer x.Close()
	done := make(chan error, 1)
	go func() {
		defer y.Close()
		done <- Receive(y, io.Discard, func() error { return errors.New("disk full") }, nil)
	}()
	if e := Send(x, bytes.NewReader([]byte("binary")), nil); e == nil {
		t.Fatal("sender accepted failed commit")
	}
	if e := <-done; e == nil {
		t.Fatal("receiver accepted failed commit")
	}
}

type readWriter struct {
	io.Reader
	io.Writer
}

func TestFrameBounds(t *testing.T) {
	for _, n := range []uint32{0, maxFrame + 1, ^uint32(0)} {
		var b bytes.Buffer
		binary.Write(&b, binary.BigEndian, n)
		if _, e := ReadFrame(&b); e == nil {
			t.Fatal("accepted length", n)
		}
	}
}
func TestTLSTamperRejected(t *testing.T) {
	a, b := identities(t)
	ap := config.Peer{ID: a.ID, PublicKey: a.PublicKey()}
	bp := config.Peer{ID: b.ID, PublicKey: b.PublicKey()}
	x, y := net.Pipe()
	x.SetDeadline(time.Now().Add(3 * time.Second))
	y.SetDeadline(time.Now().Add(3 * time.Second))
	defer x.Close()
	mut := &tamperConn{Conn: x}
	done := make(chan error, 1)
	go func() {
		defer y.Close()
		s, e := Open(context.Background(), y, b, &ap, false, "send")
		if e == nil {
			e = Receive(s, io.Discard, nil, nil)
		}
		done <- e
	}()
	s, e := Open(context.Background(), mut, a, &bp, true, "send")
	if e != nil {
		t.Fatal(e)
	}
	mut.mutate = true
	_ = Send(s, bytes.NewReader([]byte("secret")), nil)
	if e = <-done; e == nil {
		t.Fatal("accepted tampered TLS record")
	}
}

type tamperConn struct {
	net.Conn
	mutate bool
}

func (c *tamperConn) Write(p []byte) (int, error) {
	if c.mutate {
		p = append([]byte(nil), p...)
		p[len(p)-1] ^= 1
		c.mutate = false
	}
	return c.Conn.Write(p)
}

func TestPlaintextAndMissingKeysRejected(t *testing.T) {
	a, b := identities(t)
	x, y := net.Pipe()
	defer x.Close()
	defer y.Close()
	done := make(chan error, 1)
	go func() {
		_, e := Open(context.Background(), y, b, &config.Peer{ID: a.ID, PublicKey: a.PublicKey()}, false, "send")
		done <- e
	}()
	raw := hello{Version: Version, ID: a.ID, Name: a.Name, PublicKey: a.PublicKey(), Operation: "send", Encrypted: false}
	encoded, _ := json.Marshal(raw)
	if e := WriteFrame(x, encoded); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e == nil {
		t.Fatal("accepted plaintext hello")
	}
	if _, e := Open(context.Background(), x, a, &config.Peer{ID: b.ID}, true, "send"); e == nil {
		t.Fatal("accepted peer without encryption key")
	}
}
