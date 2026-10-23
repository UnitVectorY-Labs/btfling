package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

func setupCLI(t *testing.T) (string, *config.Config, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	a, e := config.Init(dir, "alice")
	if e != nil {
		t.Fatal(e)
	}
	b, e := config.Init(t.TempDir(), "bob")
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Add(config.Peer{Name: b.Name, ID: b.ID, PublicKey: b.PublicKey()}); e != nil {
		t.Fatal(e)
	}
	if e = a.Save(dir); e != nil {
		t.Fatal(e)
	}
	return dir, a, b
}
func TestHeadlessPipelinesAndFiles(t *testing.T) {
	for _, mode := range []string{"send-stdin", "send-file", "receive-stdout", "receive-file"} {
		t.Run(mode, func(t *testing.T) {
			dir, a, b := setupCLI(t)
			payload := bytes.Repeat([]byte{0, 255, 128, '\n'}, 10000)
			x, y := net.Pipe()
			x.SetDeadline(time.Now().Add(3 * time.Second))
			y.SetDeadline(time.Now().Add(3 * time.Second))
			old := openBluetooth
			defer func() { openBluetooth = old }()
			openBluetooth = func(context.Context, bool, string, string) (io.ReadWriteCloser, error) { return x, nil }
			receiving := mode == "receive-stdout" || mode == "receive-file"
			done := make(chan error, 1)
			var received bytes.Buffer
			destination := filepath.Join(t.TempDir(), "binary.dat")
			go func() {
				defer y.Close()
				p := config.Peer{Name: a.Name, ID: a.ID, PublicKey: a.PublicKey()}
				s, e := protocol.Open(context.Background(), y, b, &p, receiving, "send")
				if e == nil {
					if receiving {
						e = protocol.Send(s, bytes.NewReader(payload), nil)
					} else {
						e = protocol.Receive(s, &received, nil, nil)
					}
				}
				done <- e
			}()
			args := []string{"--config", dir, "send", "bob"}
			if receiving {
				args[2] = "receive"
			}
			if mode == "send-file" {
				if e := os.WriteFile(destination, payload, 0600); e != nil {
					t.Fatal(e)
				}
				args = append(args, "-in", destination)
			}
			if mode == "receive-file" {
				args = append(args, "-out", destination)
			}
			var stdout, stderr bytes.Buffer
			e := Run(context.Background(), args, bytes.NewReader(payload), &stdout, &stderr)
			x.Close()
			remoteErr := <-done
			if e != nil || remoteErr != nil {
				t.Fatalf("local %v remote %v", e, remoteErr)
			}
			got := received.Bytes()
			if receiving {
				got = stdout.Bytes()
			}
			if mode == "receive-file" {
				got, e = os.ReadFile(destination)
				if e != nil {
					t.Fatal(e)
				}
				if stdout.Len() != 0 {
					t.Fatal("status leaked to stdout")
				}
			}
			if !bytes.Equal(got, payload) {
				t.Fatal("binary data changed")
			}
			if !receiving && stdout.Len() != 0 {
				t.Fatal("send wrote stdout")
			}
		})
	}
}
func TestReceiveFailureDoesNotPublishOrOverwrite(t *testing.T) {
	dir, _, b := setupCLI(t)
	dest := filepath.Join(t.TempDir(), "output")
	old := openBluetooth
	defer func() { openBluetooth = old }()
	openBluetooth = func(context.Context, bool, string, string) (io.ReadWriteCloser, error) {
		return nil, errors.New("radio unavailable")
	}
	if e := Run(context.Background(), []string{"--config", dir, "receive", "bob", "-out", dest}, bytes.NewReader(nil), io.Discard, io.Discard); e == nil {
		t.Fatal("missing error")
	}
	entries, e := os.ReadDir(filepath.Dir(dest))
	if e != nil || len(entries) != 0 {
		t.Fatal("partial file leaked", e)
	}
	if e = os.WriteFile(dest, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	called := false
	openBluetooth = func(context.Context, bool, string, string) (io.ReadWriteCloser, error) {
		called = true
		return nil, errors.New("unexpected")
	}
	_ = Run(context.Background(), []string{"--config", dir, "receive", b.Name, "-out", dest}, bytes.NewReader(nil), io.Discard, io.Discard)
	data, _ := os.ReadFile(dest)
	if called || string(data) != "original" {
		t.Fatal("overwrote existing destination")
	}
}
func TestCLIUsageAndTrustManagement(t *testing.T) {
	dir, _, _ := setupCLI(t)
	for _, args := range [][]string{{"unknown"}, {"send"}, {"send", "bob", "-out", "x"}, {"pair", "-trust", "invalid"}, {"pair", "-listen"}, {"pair", "-trust-new"}, {"discover", "-timeout", "-1s"}} {
		e := Run(context.Background(), append([]string{"--config", dir}, args...), bytes.NewReader(nil), io.Discard, io.Discard)
		if !IsUsage(e) {
			t.Fatalf("%v: expected usage error, got %v", args, e)
		}
	}
	var out bytes.Buffer
	if e := Run(context.Background(), []string{"--config", dir, "rename", "bob", "robert"}, bytes.NewReader(nil), &out, io.Discard); e != nil {
		t.Fatal(e)
	}
	if e := Run(context.Background(), []string{"--config", dir, "peers"}, bytes.NewReader(nil), &out, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(out.Bytes(), []byte("robert")) {
		t.Fatal("rename not persisted")
	}
	if e := Run(context.Background(), []string{"--config", dir, "forget", "robert"}, bytes.NewReader(nil), &out, io.Discard); e != nil {
		t.Fatal(e)
	}
}

func TestPartialReceiveCleansTemporaryFile(t *testing.T) {
	dir, a, b := setupCLI(t)
	dest := filepath.Join(t.TempDir(), "received")
	x, y := net.Pipe()
	x.SetDeadline(time.Now().Add(3 * time.Second))
	y.SetDeadline(time.Now().Add(3 * time.Second))
	old := openBluetooth
	defer func() { openBluetooth = old }()
	openBluetooth = func(context.Context, bool, string, string) (io.ReadWriteCloser, error) { return x, nil }
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer y.Close()
		s, e := protocol.Open(context.Background(), y, b, &config.Peer{ID: a.ID, PublicKey: a.PublicKey()}, true, "send")
		if e == nil {
			protocol.WriteFrame(s, []byte{1, 0, 255, 128})
		}
	}()
	if e := Run(context.Background(), []string{"--config", dir, "receive", "bob", "-out", dest}, bytes.NewReader(nil), io.Discard, io.Discard); e == nil {
		t.Fatal("accepted truncated transfer")
	}
	<-done
	entries, e := os.ReadDir(filepath.Dir(dest))
	if e != nil || len(entries) != 0 {
		t.Fatal("unverified partial file was published or leaked", e)
	}
}

func TestAutomaticConfigurationAndReadOnlyHelp(t *testing.T) {
	for _, command := range [][]string{nil, {"peers"}} {
		dir := filepath.Join(t.TempDir(), "automatic")
		args := append([]string{"--config", dir}, command...)
		var stdout, stderr bytes.Buffer
		if e := Run(context.Background(), args, bytes.NewReader(nil), &stdout, &stderr); e != nil {
			t.Fatal(e)
		}
		c, e := config.Load(dir)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Contains(stderr.Bytes(), []byte("Initialized")) {
			t.Fatal("missing stderr initialization notice")
		}
		if len(command) > 0 && stdout.Len() != 0 {
			t.Fatal("initialization leaked into stdout")
		}
		stderr.Reset()
		if e = Run(context.Background(), args, bytes.NewReader(nil), io.Discard, &stderr); e != nil {
			t.Fatal(e)
		}
		again, _ := config.Load(dir)
		if again.ID != c.ID || stderr.Len() != 0 {
			t.Fatal("reinitialized an existing identity")
		}
	}
	for _, command := range []string{"help", "--help", "invalid-command"} {
		dir := filepath.Join(t.TempDir(), "absent")
		_ = Run(context.Background(), []string{"--config", dir, command}, bytes.NewReader(nil), io.Discard, io.Discard)
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatal("help/unknown command created configuration")
		}
	}
}
