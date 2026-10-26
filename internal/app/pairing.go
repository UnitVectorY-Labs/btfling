package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/diagnostics"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

type pairOptions struct {
	Target, Alias, Trust string
	AutoSelect           bool
	Operation            string
	DialDelay            time.Duration
	AfterPair            func(context.Context, *protocol.Session, io.ReadWriteCloser, bool) error
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
	session      *protocol.Session
	conn         io.ReadWriteCloser
	initiator    bool
	decision     chan bool
	err          error
	agreed       bool
	listenFailed bool
}

var errPairYield = errors.New("simultaneous pairing: using the other connection")

func runPairing(ctx context.Context, dir string, c *config.Config, options pairOptions, selections <-chan string, found func(ble.Peer), status func(string)) (config.Peer, error) {
	return pairWithRadio(ctx, dir, c, options, selections, found, status, bluetoothPairing)
}
func pairWithRadio(ctx context.Context, dir string, c *config.Config, options pairOptions, selections <-chan string, found func(ble.Peer), status func(string), radio pairingRadio) (config.Peer, error) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	operation := options.Operation
	if operation == "" {
		operation = "pair"
	}
	listenCtx, stopListening := context.WithCancel(ctx)
	defer stopListening()
	scanCtx, stopScanning := context.WithCancel(ctx)
	defer stopScanning()
	scanDone := make(chan struct{})
	events := make(chan pairAttempt, 8)
	peers := make(chan ble.Peer, 64)
	scanError := make(chan error, 1)
	defer func() {
		cancel()
		workers.Wait()
		// A losing candidate can have been queued just as cancellation won.
		// No worker retains ownership after handing it to this channel.
		close(events)
		for event := range events {
			if event.conn != nil {
				event.conn.Close()
			}
		}
	}()
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
		diagnostics.Emit(ctx, "info", "pairing", "handshake.start", fmt.Sprintf("initiator=%t operation=%s", initiator, operation))
		stop := context.AfterFunc(attemptCtx, func() { conn.Close() })
		handedOff := false
		defer func() {
			stop()
			if !handedOff {
				conn.Close()
			}
		}()
		s, e := protocol.Open(attemptCtx, conn, c, nil, initiator, operation)
		if e != nil {
			emit(pairAttempt{initiator: initiator, err: e})
			return
		}
		diagnostics.Emit(ctx, "info", "pairing", "handshake.authenticated", fmt.Sprintf("peer=%s initiator=%t", s.Remote.ID, initiator))
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
			// A buffered transport must deliver the vote before cancelling this session.
			_ = shutdownSession(conn, s)
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
		workers.Go(func() {
			conn, e := radio.listen(listenCtx, c.ID, c.Name)
			if e != nil {
				emit(pairAttempt{listenFailed: true, err: fmt.Errorf("advertise pairing: %w", e)})
				return
			}
			attempt(listenCtx, conn, false)
		})
	}
	status("Looking for nearby devices • waiting for pairing…")
	startListener()
	workers.Go(func() {
		defer close(scanDone)
		scanError <- radio.scan(scanCtx, func(p ble.Peer) {
			if p.ID == c.ID {
				return
			}
			select {
			case peers <- p:
			case <-scanCtx.Done():
			}
		})
	})
	target := options.Target
	var outgoingCancel context.CancelFunc
	inboundApproved := false
	var listenErr, discoveryErr error
	var dialTimer *time.Timer
	var dialReady <-chan time.Time
	var scheduledTarget string
	defer func() {
		if dialTimer != nil {
			dialTimer.Stop()
		}
	}()
	startOutgoing := func(id string) {
		if outgoingCancel != nil || inboundApproved {
			return
		}
		target = id
		child, stop := context.WithCancel(ctx)
		outgoingCancel = stop
		status("Connecting to " + id + "…")
		workers.Go(func() {
			conn, e := radio.dial(child, false, id, c.Name)
			if e != nil {
				emit(pairAttempt{initiator: true, err: e})
				return
			}
			attempt(child, conn, true)
		})
	}
	if target != "" {
		startOutgoing(target)
	}
	for {
		select {
		case <-dialReady:
			dialReady = nil
			startOutgoing(scheduledTarget)
		case <-ctx.Done():
			return config.Peer{}, errors.Join(ctx.Err(), listenErr, discoveryErr)
		case e := <-scanError:
			if ctx.Err() == nil {
				if e == nil {
					e = errors.New("Bluetooth discovery stopped")
				}
				discoveryErr = fmt.Errorf("discover pairing peers: %w", e)
				if listenErr != nil && outgoingCancel == nil {
					return config.Peer{}, errors.Join(listenErr, discoveryErr)
				}
				if outgoingCancel == nil {
					status("Discovery unavailable; select this computer on the other device. " + discoveryErr.Error())
				}
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
				if options.DialDelay > 0 && c.ID > p.ID {
					target = p.ID
					scheduledTarget = p.ID
					dialTimer = time.NewTimer(options.DialDelay)
					dialReady = dialTimer.C
					diagnostics.Emit(ctx, "info", "pairing", "election.wait", fmt.Sprintf("peer=%s has lower ID; waiting %s for its connection before fallback", p.ID, options.DialDelay))
				} else {
					diagnostics.Emit(ctx, "info", "pairing", "election.dial", "Local ID has priority; starting outgoing connection")
					startOutgoing(p.ID)
				}
			}
		case event := <-events:
			if event.err != nil {
				diagnostics.Emit(ctx, "warning", "pairing", "attempt.failed", fmt.Sprintf("initiator=%t listener_setup=%t: %v", event.initiator, event.listenFailed, event.err))
				if ctx.Err() != nil {
					return config.Peer{}, ctx.Err()
				}
				if event.listenFailed {
					listenErr = event.err
					if discoveryErr != nil && outgoingCancel == nil {
						return config.Peer{}, errors.Join(listenErr, discoveryErr)
					}
					if outgoingCancel == nil {
						status("Advertising unavailable; discovery continues. Select the other device on this computer. " + listenErr.Error())
					}
					continue
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
				stopOnCancel := context.AfterFunc(ctx, func() { event.conn.Close() })
				defer stopOnCancel()
				stopScanning()
				if event.initiator {
					stopListening()
				}
				diagnostics.Emit(ctx, "info", "pairing", "connection.agreed", fmt.Sprintf("peer=%s initiator=%t; stopping competing discovery/listener work", p.ID, event.initiator))
				if e := rememberPeer(c, p, options.Alias); e != nil {
					return config.Peer{}, e
				}
				if e := c.Save(dir); e != nil {
					return config.Peer{}, e
				}
				if e := confirmSaved(event.session, event.initiator); e != nil {
					return config.Peer{}, fmt.Errorf("peer saved locally; remote confirmation failed: %w", e)
				}
				diagnostics.Emit(ctx, "info", "pairing", "paired", "Both devices confirmed saving the peer key")
				if options.AfterPair != nil {
					select {
					case <-scanDone:
					case <-ctx.Done():
						return config.Peer{}, ctx.Err()
					}
					if err := options.AfterPair(ctx, event.session, event.conn, event.initiator); err != nil {
						return config.Peer{}, fmt.Errorf("paired, but diagnostic exchange failed: %w", err)
					}
				}
				if e := shutdownSession(event.conn, event.session); e != nil {
					return config.Peer{}, fmt.Errorf("peer saved locally; session shutdown failed: %w", e)
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
					if old.ID == p.ID && !bytes.Equal(old.PublicKey, p.PublicKey) {
						event.decision <- false
						return config.Peer{}, errors.New("known peer public key changed; refusing to replace trust")
					}
				}
				if !event.initiator {
					inboundApproved = true
					dialReady = nil
					if dialTimer != nil {
						dialTimer.Stop()
					}
					target = p.ID
					if outgoingCancel != nil {
						outgoingCancel()
					}
				}
				status("Exchanging keys with " + p.Name + "…")
			}
			event.decision <- accept
			diagnostics.Emit(ctx, "info", "pairing", "election.vote", fmt.Sprintf("peer=%s initiator=%t accept=%t", p.ID, event.initiator, accept))
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
		if !bytes.Equal(old.PublicKey, p.PublicKey) {
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
