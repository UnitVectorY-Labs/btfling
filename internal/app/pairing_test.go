package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
)

type memoryRadio struct {
	inbound map[string]chan io.ReadWriteCloser
	peers   []ble.Peer
	barrier chan struct{}
	mu      sync.Mutex
	dials   int
}

func (h *memoryRadio) radio(local string) pairingRadio {
	return pairingRadio{
		listen: func(ctx context.Context, id, name string) (io.ReadWriteCloser, error) {
			select {
			case conn := <-h.inbound[id]:
				return conn, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		dial: func(ctx context.Context, _ bool, id, name string) (io.ReadWriteCloser, error) {
			x, y := net.Pipe()
			x.SetDeadline(time.Now().Add(5 * time.Second))
			y.SetDeadline(time.Now().Add(5 * time.Second))
			select {
			case h.inbound[id] <- y:
			case <-ctx.Done():
				x.Close()
				y.Close()
				return nil, ctx.Err()
			}
			if h.barrier != nil {
				h.mu.Lock()
				h.dials++
				if h.dials == 2 {
					close(h.barrier)
				}
				h.mu.Unlock()
				select {
				case <-h.barrier:
				case <-ctx.Done():
					x.Close()
					return nil, ctx.Err()
				}
			}
			return x, nil
		},
		scan: func(ctx context.Context, found func(ble.Peer)) error {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-ticker.C:
					for _, p := range h.peers {
						if p.ID != local {
							found(p)
						}
					}
				}
			}
		},
	}
}

type pairResult struct {
	peer config.Peer
	err  error
}

func pairFixture(t *testing.T) (string, string, *config.Config, *config.Config, *memoryRadio) {
	t.Helper()
	ad, bd := t.TempDir(), t.TempDir()
	a, e := config.Init(ad, "alice")
	if e != nil {
		t.Fatal(e)
	}
	b, e := config.Init(bd, "bob")
	if e != nil {
		t.Fatal(e)
	}
	a.ID = "00000000000000000000000000000001"
	b.ID = "00000000000000000000000000000002"
	a.Save(ad)
	b.Save(bd)
	h := &memoryRadio{inbound: map[string]chan io.ReadWriteCloser{a.ID: make(chan io.ReadWriteCloser, 2), b.ID: make(chan io.ReadWriteCloser, 2)}, peers: []ble.Peer{{ID: a.ID, Name: a.Name, Pairing: true}, {ID: b.ID, Name: b.Name, Pairing: true}}}
	return ad, bd, a, b, h
}
func TestMutualPairingAndSelection(t *testing.T) {
	for _, mode := range []string{"automatic", "select smaller", "select larger", "simultaneous", "verified"} {
		t.Run(mode, func(t *testing.T) {
			ad, bd, a, b, h := pairFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ao, bo := pairOptions{}, pairOptions{}
			switch mode {
			case "automatic":
				ao.AutoSelect = true
				bo.AutoSelect = true
			case "select smaller":
				ao.Target = b.ID
			case "select larger":
				bo.Target = a.ID
			case "simultaneous":
				ao.Target = b.ID
				bo.Target = a.ID
				h.barrier = make(chan struct{})
			case "verified":
				ao.Target = b.ID
				ao.Trust = config.Fingerprint(b.PublicKey())
				bo.Trust = config.Fingerprint(a.PublicKey())
				ao.Alias = "my-bob"
			}
			results := make(chan pairResult, 2)
			status := func(string) {}
			go func() {
				p, e := pairWithRadio(ctx, ad, a, ao, nil, nil, status, h.radio(a.ID))
				results <- pairResult{p, e}
			}()
			go func() {
				p, e := pairWithRadio(ctx, bd, b, bo, nil, nil, status, h.radio(b.ID))
				results <- pairResult{p, e}
			}()
			for range 2 {
				r := <-results
				if r.err != nil {
					t.Fatal(r.err)
				}
			}
			savedA, e := config.Load(ad)
			if e != nil {
				t.Fatal(e)
			}
			savedB, e := config.Load(bd)
			if e != nil {
				t.Fatal(e)
			}
			if len(savedA.Peers) != 1 || len(savedB.Peers) != 1 || !bytes.Equal(savedA.Peers[0].PublicKey, b.PublicKey()) || !bytes.Equal(savedB.Peers[0].PublicKey, a.PublicKey()) {
				t.Fatal("mutual trust was not saved exactly once")
			}
			expected := "bob"
			if mode == "verified" {
				expected = "my-bob"
			}
			if savedA.Peers[0].Name != expected {
				t.Fatal("learned/overridden nickname mismatch")
			}
		})
	}
}
func TestWrongPairFingerprintDoesNotSaveTrust(t *testing.T) {
	ad, bd, a, b, h := pairFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results := make(chan pairResult, 2)
	go func() {
		p, e := pairWithRadio(ctx, ad, a, pairOptions{Target: b.ID, Trust: strings.Repeat("0", 64)}, nil, nil, func(string) {}, h.radio(a.ID))
		results <- pairResult{p, e}
	}()
	go func() {
		p, e := pairWithRadio(ctx, bd, b, pairOptions{}, nil, nil, func(string) {}, h.radio(b.ID))
		results <- pairResult{p, e}
	}()
	found := false
	for range 2 {
		r := <-results
		if r.err != nil && strings.Contains(r.err.Error(), "fingerprint") {
			found = true
		}
		if r.err == nil {
			t.Fatal("accepted wrong fingerprint")
		}
	}
	if !found {
		t.Fatal("missing fingerprint error")
	}
	saved, _ := config.Load(ad)
	if len(saved.Peers) > 0 {
		t.Fatal("saved rejected key")
	}
}
func TestRememberKnownPeerDoesNotReplaceKey(t *testing.T) {
	_, _, a, b, _ := pairFixture(t)
	p := config.Peer{ID: b.ID, Name: b.Name, PublicKey: b.PublicKey()}
	if e := rememberPeer(a, p, ""); e != nil {
		t.Fatal(e)
	}
	if e := rememberPeer(a, p, ""); e != nil || len(a.Peers) != 1 {
		t.Fatal("known pairing is not idempotent", e)
	}
	p.PublicKey = bytes.Repeat([]byte{9}, 32)
	if e := rememberPeer(a, p, ""); e == nil {
		t.Fatal("replaced known key")
	}
}
func TestPairingCancellation(t *testing.T) {
	ad, _, a, _, h := pairFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := pairWithRadio(ctx, ad, a, pairOptions{}, nil, nil, func(string) {}, h.radio(a.ID))
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestPairingWithOneRadioRoleUnavailable(t *testing.T) {
	for _, failedRole := range []string{"alice advertising", "bob advertising", "alice discovery", "bob discovery"} {
		t.Run(failedRole, func(t *testing.T) {
			ad, bd, a, b, h := pairFixture(t)
			ar, br := h.radio(a.ID), h.radio(b.ID)
			roleFailure := errors.New("radio role unavailable")
			listenFailure := func(context.Context, string, string) (io.ReadWriteCloser, error) { return nil, roleFailure }
			scanFailure := func(context.Context, func(ble.Peer)) error { return roleFailure }
			switch failedRole {
			case "alice advertising":
				ar.listen = listenFailure
				// A device that cannot advertise is genuinely absent from scans.
				h.peers = []ble.Peer{{ID: b.ID, Name: b.Name, Pairing: true}}
			case "bob advertising":
				br.listen = listenFailure
				h.peers = []ble.Peer{{ID: a.ID, Name: a.Name, Pairing: true}}
			case "alice discovery":
				ar.scan = scanFailure
			case "bob discovery":
				br.scan = scanFailure
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			results := make(chan pairResult, 2)
			go func() {
				p, err := pairWithRadio(ctx, ad, a, pairOptions{AutoSelect: true}, nil, nil, func(string) {}, ar)
				results <- pairResult{p, err}
			}()
			go func() {
				p, err := pairWithRadio(ctx, bd, b, pairOptions{AutoSelect: true}, nil, nil, func(string) {}, br)
				results <- pairResult{p, err}
			}()
			for range 2 {
				if result := <-results; result.err != nil {
					t.Fatal(result.err)
				}
			}
			ac, _ := config.Load(ad)
			bc, _ := config.Load(bd)
			if len(ac.Peers) != 1 || len(bc.Peers) != 1 || !bytes.Equal(ac.Peers[0].PublicKey, b.PublicKey()) || !bytes.Equal(bc.Peers[0].PublicKey, a.PublicKey()) {
				t.Fatal("fallback did not save mutually authenticated peers")
			}
		})
	}
}

func TestPairingReportsBothFailedRadioRoles(t *testing.T) {
	ad, _, a, _, _ := pairFixture(t)
	advertiseError, discoveryError := errors.New("advertising aborted without a reason"), errors.New("scan unavailable")
	radio := pairingRadio{
		listen: func(context.Context, string, string) (io.ReadWriteCloser, error) { return nil, advertiseError },
		scan:   func(context.Context, func(ble.Peer)) error { return discoveryError },
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := pairWithRadio(ctx, ad, a, pairOptions{}, nil, nil, func(string) {}, radio)
	if !errors.Is(err, advertiseError) || !errors.Is(err, discoveryError) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("did not report both failed roles promptly: %v", err)
	}
}

func TestPairingManualSelectionWithoutAdvertising(t *testing.T) {
	ad, bd, a, b, h := pairFixture(t)
	// Alice represents Windows: not discoverable, but able to select the Mac.
	h.peers = []ble.Peer{{ID: b.ID, Name: b.Name, Pairing: true}}
	ar := h.radio(a.ID)
	ar.listen = func(context.Context, string, string) (io.ReadWriteCloser, error) {
		return nil, errors.New("advertising aborted without a reason")
	}
	selections := make(chan string, 1)
	fallback := make(chan struct{}, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan pairResult, 2)
	go func() {
		p, err := pairWithRadio(ctx, ad, a, pairOptions{}, selections, func(peer ble.Peer) {
			// Simulate selecting a device after the fallback is shown in the TUI.
			select {
			case <-fallback:
				selections <- peer.ID
			default:
			}
		}, func(text string) {
			if strings.HasPrefix(text, "Advertising unavailable") {
				fallback <- struct{}{}
			}
		}, ar)
		results <- pairResult{p, err}
	}()
	go func() {
		p, err := pairWithRadio(ctx, bd, b, pairOptions{}, nil, nil, func(string) {}, h.radio(b.ID))
		results <- pairResult{p, err}
	}()
	for range 2 {
		if result := <-results; result.err != nil {
			t.Fatal(result.err)
		}
	}
}
