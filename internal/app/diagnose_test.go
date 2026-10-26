package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/diagnostics"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

func TestDiagnoseBothDevicesAndRemoteFailureLogs(t *testing.T) {
	for _, advertisingFails := range []bool{false, true} {
		t.Run(fmt.Sprint("windows-advertising-fails=", advertisingFails), func(t *testing.T) {
			ad, bd, a, b, h := pairFixture(t)
			ar, br := h.radio(a.ID), h.radio(b.ID)
			if advertisingFails {
				h.peers = []ble.Peer{{ID: a.ID, Name: a.Name, Pairing: true, Diagnostic: true}}
				br.listen = func(context.Context, string, string) (io.ReadWriteCloser, error) {
					return nil, errors.New("Windows adapter test: advertising aborted without a reason")
				}
			}
			var dials atomic.Int32
			for _, r := range []*pairingRadio{&ar, &br} {
				dial := r.dial
				r.dial = func(ctx context.Context, listen bool, id, name string) (io.ReadWriteCloser, error) {
					dials.Add(1)
					return dial(ctx, listen, id, name)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			results := make(chan error, 2)
			var ao, bo bytes.Buffer
			al, bl := filepath.Join(ad, "mac.log"), filepath.Join(bd, "windows.log")
			go func() {
				results <- diagnoseWithRadio(ctx, ad, a, []string{"-timeout", "8s", "-log", al}, &ao, io.Discard, ar)
			}()
			go func() {
				results <- diagnoseWithRadio(ctx, bd, b, []string{"-timeout", "8s", "-log", bl}, &bo, io.Discard, br)
			}()
			for range 2 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			if dials.Load() != 1 {
				t.Fatalf("staggered election made %d outgoing connections", dials.Load())
			}
			if !strings.Contains(ao.String(), "PASS:") || !strings.Contains(bo.String(), "PASS:") {
				t.Fatal("missing bidirectional success")
			}
			for _, path := range []string{al, bl} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(data, []byte(`"phase":"remote.paired"`)) || !bytes.Contains(data, []byte(`"phase":"remote.complete"`)) {
					t.Fatalf("missing imported peer reports: %s", data)
				}
				for _, key := range [][]byte{a.PrivateKey, b.PrivateKey} {
					if bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(key))) || bytes.Contains(data, []byte(hex.EncodeToString(key))) {
						t.Fatal("private key leaked into diagnostics")
					}
				}
				decoder := json.NewDecoder(bytes.NewReader(data))
				for {
					var row map[string]any
					err := decoder.Decode(&row)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("invalid JSON log: %v", err)
					}
				}
			}
			if advertisingFails {
				data, _ := os.ReadFile(al)
				if !bytes.Contains(data, []byte("Windows adapter test")) {
					t.Fatal("Mac log did not receive the Windows failure report")
				}
			}
			for _, dir := range []string{ad, bd} {
				saved, err := config.Load(dir)
				if err != nil || len(saved.Peers) != 1 {
					t.Fatalf("pairing was not persisted: %v", err)
				}
			}
		})
	}
}

func TestDiagnosticExchangeWithSimultaneousConnections(t *testing.T) {
	ad, bd, a, b, h := pairFixture(t)
	h.barrier = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for _, side := range []struct {
		dir    string
		c      *config.Config
		target string
		os     string
	}{{ad, a, b.ID, "darwin"}, {bd, b, a.ID, "windows"}} {
		go func() {
			log := diagnostics.New(io.Discard)
			ctx := diagnostics.WithLog(ctx, log)
			info := diagnosticInfo{RunID: strings.Repeat("a", 32), DeviceID: side.c.ID, Name: side.c.Name, Fingerprint: config.Fingerprint(side.c.PublicKey()), OS: side.os}
			var reports int
			_, err := pairWithRadio(ctx, side.dir, side.c, pairOptions{Target: side.target, Operation: "diagnose-v1", AfterPair: func(ctx context.Context, s *protocol.Session, conn io.ReadWriteCloser, initiator bool) error {
				return exchangeDiagnostics(ctx, s, conn, initiator, info, log, func(diagnosticReport) { reports++ })
			}}, nil, nil, func(string) {}, h.radio(side.c.ID))
			if err == nil && reports != 2 {
				err = fmt.Errorf("received %d reports", reports)
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestDiagnosticUnavailablePeerAndLogPreservation(t *testing.T) {
	ad, _, a, _, _ := pairFixture(t)
	path := filepath.Join(ad, "diagnostic.log")
	radio := pairingRadio{
		listen: func(ctx context.Context, _, _ string) (io.ReadWriteCloser, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		scan: func(ctx context.Context, _ func(ble.Peer)) error { <-ctx.Done(); return ctx.Err() },
	}
	var out bytes.Buffer
	err := diagnoseWithRadio(context.Background(), ad, a, []string{"-timeout", "30ms", "-log", path}, &out, io.Discard, radio)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing deadline: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte(`"phase":"remote.unavailable"`)) || strings.Contains(out.String(), "PASS:") {
		t.Fatal("failed run implied success or remote knowledge")
	}
	if err := diagnoseWithRadio(context.Background(), ad, a, []string{"-log", path}, io.Discard, io.Discard, radio); err == nil {
		t.Fatal("overwrote existing log")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("prior log was modified")
	}
}

func TestDiagnosticReportRejectsWrongAuthenticatedIdentity(t *testing.T) {
	_, _, a, b, _ := pairFixture(t)
	x, y := net.Pipe()
	defer x.Close()
	defer y.Close()
	x.SetDeadline(time.Now().Add(time.Second))
	y.SetDeadline(time.Now().Add(time.Second))
	done := make(chan error, 1)
	go func() {
		_, err := protocol.ReadFrame(y)
		if err == nil {
			data, _ := json.Marshal(diagnosticReport{Schema: 1, Stage: "paired", Info: diagnosticInfo{DeviceID: a.ID}})
			err = protocol.WriteFrame(y, data)
		}
		done <- err
	}()
	_, err := exchangeDiagnosticReport(&protocol.Session{ReadWriter: x, Remote: config.Peer{ID: b.ID, Name: b.Name, PublicKey: b.PublicKey()}}, true, diagnosticReport{Schema: 1, Stage: "paired"})
	if err == nil || !strings.Contains(err.Error(), "authenticated session") {
		t.Fatalf("accepted substituted report identity: %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticExchangeCancellationUnblocksIO(t *testing.T) {
	x, y := net.Pipe()
	defer x.Close()
	defer y.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := exchangeDiagnostics(ctx, &protocol.Session{ReadWriter: x}, x, true, diagnosticInfo{}, diagnostics.New(io.Discard), func(diagnosticReport) {})
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("blocked exchange ignored cancellation: %v", err)
	}
}

func TestDiagnoseCLIValidation(t *testing.T) {
	ad, _, _, _, _ := pairFixture(t)
	for _, args := range [][]string{{"-timeout", "0"}, {"-timeout", "-1s"}, {"-peer", "bogus"}, {"-trust", "bogus"}, {"unexpected"}} {
		err := Run(context.Background(), append([]string{"--config", ad, "diagnose"}, args...), strings.NewReader(""), io.Discard, io.Discard)
		if !IsUsage(err) {
			t.Fatalf("expected usage error for %v: %v", args, err)
		}
	}
}
