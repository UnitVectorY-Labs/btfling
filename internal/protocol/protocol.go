// Package protocol implements bounded streaming records and mutually authenticated TLS over Bluetooth.
package protocol

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
)

const maxFrame = 32769
const Version = 1

func WriteFrame(w io.Writer, p []byte) error {
	if len(p) > maxFrame {
		return errors.New("record too large")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(p)))
	if e := writeAll(w, h[:]); e != nil {
		return e
	}
	return writeAll(w, p)
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, e := w.Write(p)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func ReadFrame(r io.Reader) ([]byte, error) { return readFrame(r, maxFrame) }
func readFrame(r io.Reader, max int) ([]byte, error) {
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return nil, e
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > uint32(max) {
		return nil, errors.New("invalid record length")
	}
	p := make([]byte, n)
	_, e := io.ReadFull(r, p)
	return p, e
}

type hello struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey []byte `json:"public_key"`
	Operation string `json:"operation"`
}
type Session struct {
	io.ReadWriter
	Remote config.Peer
}

// Open verifies device identity and establishes mutually authenticated TLS.
// Pairing is an explicit trust-on-first-use exchange; callers must verify the returned key.
func Open(ctx context.Context, rw io.ReadWriteCloser, c *config.Config, expected *config.Peer, initiator bool, operation string) (*Session, error) {
	if operation == "send" && (expected == nil || len(expected.PublicKey) != ed25519.PublicKeySize) {
		return nil, errors.New("invalid trusted peer public key")
	}
	if len(c.PrivateKey) != ed25519.SeedSize {
		return nil, errors.New("invalid local private key seed")
	}
	local := hello{Version: Version, ID: c.ID, Name: c.Name, PublicKey: c.PublicKey(), Operation: operation}

	send := func() error {
		b, e := json.Marshal(local)
		if e != nil {
			return e
		}
		return WriteFrame(rw, b)
	}
	var remote hello
	receive := func() error {
		b, e := readFrame(rw, 4096)
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &remote); e != nil {
			return e
		}
		idBytes, idError := hex.DecodeString(remote.ID)
		if idError != nil || len(idBytes) != 16 {
			return errors.New("invalid peer device ID")
		}
		if remote.Version != Version {
			return errors.New("unsupported btfling protocol version")
		}
		if !config.ValidName(remote.Name) || len(remote.ID) != 32 || remote.ID == c.ID {
			return errors.New("invalid btfling identity")
		}
		if len(remote.PublicKey) != ed25519.PublicKeySize {
			return errors.New("invalid peer public key")
		}
		if expected != nil && remote.ID != expected.ID {
			return errors.New("unexpected Bluetooth device")
		}
		if remote.Operation != operation {
			return errors.New("peer is waiting for a different operation")
		}
		return nil
	}
	if initiator {
		if e := send(); e != nil {
			return nil, e
		}
		if e := receive(); e != nil {
			return nil, e
		}
	} else {
		if e := receive(); e != nil {
			return nil, e
		}
		if e := send(); e != nil {
			return nil, e
		}
	}
	s := &Session{ReadWriter: rw, Remote: config.Peer{ID: remote.ID, Name: remote.Name, PublicKey: remote.PublicKey}}
	pinned := remote.PublicKey
	if expected != nil {
		pinned = expected.PublicKey
	}
	if !bytes.Equal(remote.PublicKey, pinned) {
		return nil, errors.New("peer public key changed")
	}
	cert, e := certificate(c.PrivateKey)
	if e != nil {
		return nil, e
	}
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, SessionTicketsDisabled: true,
		// A pinned Ed25519 public key replaces public PKI and DNS verification.
		InsecureSkipVerify: true, ClientAuth: tls.RequireAnyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 {
				return errors.New("expected one pinned peer certificate")
			}
			pub, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok || !bytes.Equal(pub, pinned) {
				return errors.New("TLS peer key does not match trusted key")
			}
			return nil
		}}
	var conn net.Conn
	if native, ok := rw.(net.Conn); ok {
		conn = native
	} else {
		conn = &bluetoothConn{ReadWriteCloser: rw}
	}
	var secure *tls.Conn
	if initiator {
		secure = tls.Client(conn, tc)
	} else {
		secure = tls.Server(conn, tc)
	}
	if e = secure.HandshakeContext(ctx); e != nil {
		return nil, fmt.Errorf("Bluetooth TLS handshake: %w", e)
	}
	s.ReadWriter = secure
	return s, nil
}
func certificate(seed []byte) (tls.Certificate, error) {
	key := ed25519.NewKeyFromSeed(seed)
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return tls.Certificate{}, e
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "btfling"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, e
}

// net.Conn is only an interface adapter for crypto/tls. No socket is created.
// Native BLE streams implement net.Conn themselves. This adapter is for simpler
// injected streams; it never silently accepts unsupported deadlines.
type bluetoothConn struct{ io.ReadWriteCloser }
type address struct{}

func (address) Network() string             { return "bluetooth" }
func (address) String() string              { return "btfling" }
func (*bluetoothConn) LocalAddr() net.Addr  { return address{} }
func (*bluetoothConn) RemoteAddr() net.Addr { return address{} }
func (*bluetoothConn) SetDeadline(time.Time) error {
	return errors.New("stream does not support deadlines")
}
func (*bluetoothConn) SetReadDeadline(time.Time) error {
	return errors.New("stream does not support deadlines")
}
func (*bluetoothConn) SetWriteDeadline(time.Time) error {
	return errors.New("stream does not support deadlines")
}

type Progress func(int64)

func Send(rw io.ReadWriter, input io.Reader, progress Progress) error {
	h := sha256.New()
	var total int64
	buf := make([]byte, 32768)
	for {
		n, e := input.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
			if e := WriteFrame(rw, append([]byte{1}, buf[:n]...)); e != nil {
				return e
			}
			if progress != nil {
				progress(total)
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	end := make([]byte, 41)
	end[0] = 2
	binary.BigEndian.PutUint64(end[1:9], uint64(total))
	copy(end[9:], h.Sum(nil))
	if e := WriteFrame(rw, end); e != nil {
		return e
	}
	ack, e := ReadFrame(rw)
	if e != nil {
		return fmt.Errorf("waiting for receiver completion: %w", e)
	}
	if !bytes.Equal(ack, append([]byte{3}, end[1:]...)) {
		return errors.New("receiver did not confirm content and length")
	}
	return nil
}

// commit is called only after the entire stream and digest have been verified,
// and before acknowledging success to the sender.
func Receive(rw io.ReadWriter, out io.Writer, commit func() error, progress Progress) error {
	h := sha256.New()
	var total int64
	for {
		p, e := ReadFrame(rw)
		if e != nil {
			return e
		}
		switch p[0] {
		case 1:
			if len(p) == 1 {
				return errors.New("empty data record")
			}
			if e = writeAll(out, p[1:]); e != nil {
				return e
			}
			h.Write(p[1:])
			total += int64(len(p) - 1)
			if progress != nil {
				progress(total)
			}
		case 2:
			if len(p) != 41 || binary.BigEndian.Uint64(p[1:9]) != uint64(total) || !bytes.Equal(p[9:], h.Sum(nil)) {
				return errors.New("received length or SHA-256 checksum mismatch")
			}
			if commit != nil {
				if e = commit(); e != nil {
					return e
				}
			}
			return WriteFrame(rw, append([]byte{3}, p[1:]...))
		default:
			return errors.New("unexpected transfer record")
		}
	}
}
