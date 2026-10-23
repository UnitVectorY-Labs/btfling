package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

type pairOptions struct {
	Target, Alias, Trust string
	AutoSelect           bool
}
type pairingRadio struct {
	listen func(context.Context, string, string) (io.ReadWriteCloser, error)
	dial   func(context.Context, bool, string, string) (io.ReadWriteCloser, error)
	scan   func(context.Context, func(ble.Peer)) error
}

var bluetoothPairing = pairingRadio{ble.ListenPair, func(ctx context.Context, listen bool, id, name string) (io.ReadWriteCloser, error) {
	return openBluetooth(ctx, listen, id, name)
}, ble.Discover}

type pairAttempt struct {
	session   *protocol.Session
	conn      io.ReadWriteCloser
	initiator bool
	decision  chan bool
	err       error
	agreed    bool
}

var errPairYield = errors.New("simultaneous pairing: using the other connection")

func runPairing(ctx context.Context, dir string, c *config.Config, options pairOptions, selections <-chan string, found func(ble.Peer), status func(string)) (config.Peer, error) {
	return pairWithRadio(ctx, dir, c, options, selections, found, status, bluetoothPairing)
}
func pairWithRadio(ctx context.Context, dir string, c *config.Config, options pairOptions, selections <-chan string, found func(ble.Peer), status func(string), radio pairingRadio) (config.Peer, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := make(chan pairAttempt, 8)
	peers := make(chan ble.Peer, 64)
	scanError := make(chan error, 1)
	emit := func(e pairAttempt) {
		select {
		case events <- e:
		case <-ctx.Done():
			if e.conn != nil {
				e.conn.Close()
			}
		}
	}
	// A connection remains owned by its worker until an agreed session is handed back.
	attempt := func(attemptCtx context.Context, conn io.ReadWriteCloser, initiator bool) {
		stop := context.AfterFunc(attemptCtx, func() { conn.Close() })
		handedOff := false
		defer func() {
			stop()
			if !handedOff {
				conn.Close()
			}
		}()
		s, e := protocol.Open(attemptCtx, conn, c, nil, initiator, "pair")
		if e != nil {
			emit(pairAttempt{initiator: initiator, err: e})
			return
		}
		decision := make(chan bool, 1)
		candidate := pairAttempt{session: s, conn: conn, initiator: initiator, decision: decision}
		emit(candidate)
		var accept bool
		select {
		case accept = <-decision:
		case <-attemptCtx.Done():
			return
		}
		vote := "yield"
		if accept {
			vote = "ready"
		}
		var remote []byte
		if initiator {
			e = protocol.WriteFrame(s, []byte(vote))
			if e == nil {
				remote, e = protocol.ReadFrame(s)
			}
		} else {
			remote, e = protocol.ReadFrame(s)
			if e == nil {
				e = protocol.WriteFrame(s, []byte(vote))
			}
		}
		if e == nil && (!accept || string(remote) != "ready") {
			e = errPairYield
		}
		if e != nil {
			conn.Close()
			emit(pairAttempt{initiator: initiator, err: e})
			return
		}
		candidate.agreed = true
		handedOff = true
		emit(candidate)
	}
	startListener := func() {
		go func() {
			conn, e := radio.listen(ctx, c.ID, c.Name)
			if e != nil {
				emit(pairAttempt{err: fmt.Errorf("advertise pairing: %w", e)})
				return
			}
			attempt(ctx, conn, false)
		}()
	}
	startListener()
	go func() {
		scanError <- radio.scan(ctx, func(p ble.Peer) {
			if p.ID == c.ID {
				return
			}
			select {
			case peers <- p:
			case <-ctx.Done():
			}
		})
	}()
	target := options.Target
	var outgoingCancel context.CancelFunc
	inboundApproved := false
	startOutgoing := func(id string) {
		if outgoingCancel != nil || inboundApproved {
			return
		}
		target = id
		child, stop := context.WithCancel(ctx)
		outgoingCancel = stop
		status("Connecting to " + id + "…")
		go func() {
			conn, e := radio.dial(child, false, id, c.Name)
			if e != nil {
				emit(pairAttempt{initiator: true, err: e})
				return
			}
			attempt(child, conn, true)
		}()
	}
	if target != "" {
		startOutgoing(target)
	}
	status("Bluetooth active • advertising and discovering")
	for {
		select {
		case <-ctx.Done():
			return config.Peer{}, ctx.Err()
		case e := <-scanError:
			if e != nil && ctx.Err() == nil {
				return config.Peer{}, fmt.Errorf("discover pairing peers: %w", e)
			}
		case id := <-selections:
			if id != "" {
				startOutgoing(id)
			}
		case p := <-peers:
			if found != nil {
				found(p)
			}
			if options.AutoSelect && target == "" && p.Pairing {
				startOutgoing(p.ID)
			}
		case event := <-events:
			if event.err != nil {
				if ctx.Err() != nil {
					return config.Peer{}, ctx.Err()
				}
				if errors.Is(event.err, errPairYield) {
					if !event.initiator && !inboundApproved {
						startListener()
					}
					continue
				}
				if event.initiator && inboundApproved {
					continue
				}
				if !event.initiator && outgoingCancel != nil && !inboundApproved {
					// The other device may cancel its losing outgoing handshake
					// as soon as it accepts our preferred incoming connection.
					startListener()
					continue
				}
				return config.Peer{}, event.err
			}
			p := event.session.Remote
			if event.agreed {
				if ctx.Err() != nil {
					event.conn.Close()
					return config.Peer{}, ctx.Err()
				}
				defer event.conn.Close()
				if e := rememberPeer(c, p, options.Alias); e != nil {
					return config.Peer{}, e
				}
				if e := c.Save(dir); e != nil {
					return config.Peer{}, e
				}
				if e := confirmSaved(event.session, event.initiator); e != nil {
					return config.Peer{}, fmt.Errorf("peer saved locally; remote confirmation failed: %w", e)
				}
				saved, _ := c.Peer(peerName(c, p.ID))
				return saved, nil
			}
			accept := target == "" || target == p.ID
			// If both users select each other, the lower device ID's outgoing connection wins.
			if !event.initiator && c.ID < p.ID && outgoingCancel != nil && target == p.ID {
				accept = false
			}
			if event.initiator && inboundApproved {
				accept = false
			}
			if accept {
				if options.Trust != "" && !strings.EqualFold(options.Trust, config.Fingerprint(p.PublicKey)) {
					event.decision <- false
					return config.Peer{}, errors.New("peer fingerprint does not match -trust; peer was not saved")
				}
				for _, old := range c.Peers {
					if old.ID == p.ID && len(old.PublicKey) > 0 && !bytes.Equal(old.PublicKey, p.PublicKey) {
						event.decision <- false
						return config.Peer{}, errors.New("known peer public key changed; refusing to replace trust")
					}
				}
				if !event.initiator {
					inboundApproved = true
					target = p.ID
					if outgoingCancel != nil {
						outgoingCancel()
					}
				}
				status("Exchanging keys with " + p.Name + "…")
			}
			event.decision <- accept
		}
	}
}
func peerName(c *config.Config, id string) string {
	for _, p := range c.Peers {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}
func rememberPeer(c *config.Config, p config.Peer, alias string) error {
	for i, old := range c.Peers {
		if old.ID != p.ID {
			continue
		}
		if len(old.PublicKey) > 0 && !bytes.Equal(old.PublicKey, p.PublicKey) {
			return errors.New("known peer key changed")
		}
		p.Name = old.Name
		if alias != "" {
			p.Name = alias
		}
		for _, other := range c.Peers {
			if other.ID != p.ID && other.Name == p.Name {
				return errors.New("peer nickname already in use; choose -as NAME")
			}
		}
		c.Peers[i] = p
		return c.Validate()
	}
	if alias != "" {
		p.Name = alias
	}
	if e := c.Add(p); e != nil {
		return fmt.Errorf("save peer (use -as NAME for a unique nickname): %w", e)
	}
	return nil
}
func confirmSaved(s io.ReadWriter, initiator bool) error {
	send := func() error { return protocol.WriteFrame(s, []byte("saved")) }
	receive := func() error {
		b, e := protocol.ReadFrame(s)
		if e == nil && string(b) != "saved" {
			return errors.New("peer did not confirm pairing")
		}
		return e
	}
	if initiator {
		if e := send(); e != nil {
			return e
		}
		return receive()
	}
	if e := receive(); e != nil {
		return e
	}
	return send()
}

func validateFingerprint(s string) error {
	if s == "" {
		return nil
	}
	b, e := hex.DecodeString(s)
	if e != nil || len(b) != 32 {
		return errors.New("fingerprint must contain 64 hexadecimal digits")
	}
	return nil
}
