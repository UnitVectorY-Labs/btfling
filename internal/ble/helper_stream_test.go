package ble

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

// Exercise real Go/helper IPC admission and event handling without native radios.
// Each fake helper decodes commands and emits independent packets/completions;
// completion never waits for a remote transport ACK.
func helperPair(t *testing.T) (*stream, *stream) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var radios [2]*helper
	var readers [2]*io.PipeReader
	for i := range 2 {
		r, w := io.Pipe()
		readers[i] = r
		hctx, stop := context.WithCancel(ctx)
		radios[i] = &helper{cmd: &exec.Cmd{Args: []string{"fake", "connect"}}, in: w, events: make(chan message, 64), done: make(chan struct{}), cancel: stop}
		wg.Add(1)
		go func() { defer wg.Done(); <-hctx.Done(); r.CloseWithError(hctx.Err()) }()
	}
	for i := range 2 {
		h, peer, r := radios[i], radios[1-i], readers[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(h.done)
			decoder := json.NewDecoder(r)
			for {
				var m message
				if e := decoder.Decode(&m); e != nil {
					return
				}
				if m.Op != "send" {
					t.Error("unexpected native command")
					return
				}
				select {
				case peer.events <- message{Event: "packet", Data: m.Data}:
				case <-ctx.Done():
					return
				}
				select {
				case h.events <- message{Event: "sent"}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	a, e := attachStream(ctx, func() {}, radios[0], 244, nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := attachStream(ctx, func() {}, radios[1], 244, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); a.Close(); b.Close(); wg.Wait() })
	a.SetDeadline(time.Now().Add(5 * time.Second))
	b.SetDeadline(time.Now().Add(5 * time.Second))
	return a, b
}
func TestHelperAdmissionTLSAndShutdown(t *testing.T) {
	a, b := helperPair(t)
	left, right := tlsSessions(t, a, b)
	payload := bytes.Repeat([]byte{0, 255, 7, 128}, 64*1024)
	done := make(chan error, 1)
	go func() { done <- protocol.Receive(right, io.Discard, nil, nil) }()
	if e := protocol.Send(left, bytes.NewReader(payload), nil); e != nil {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	finish := make(chan error, 2)
	for _, s := range []*stream{a, b} {
		go func() { finish <- s.Shutdown() }()
	}
	for range 2 {
		if e := <-finish; e != nil {
			t.Fatal(e)
		}
	}
	if a.Stats().Writes == 0 || a.Stats().Notifications == 0 {
		t.Fatal("missing native metrics", a.Stats())
	}
}
