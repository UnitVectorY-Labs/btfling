package ble

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
)

type Peer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Pairing bool   `json:"pairing"`
}
type message struct {
	Event   string `json:"event,omitempty"`
	Op      string `json:"op,omitempty"`
	Data    []byte `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Pairing bool   `json:"pairing,omitempty"`
}
type helper struct {
	cmd      *exec.Cmd
	in       io.WriteCloser
	events   chan message
	done     chan struct{}
	mu       sync.Mutex
	err      error
	cancel   context.CancelFunc
	stopOnce sync.Once
}

func start(ctx context.Context, mode, id, name string) (*helper, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return nil, errors.New("Bluetooth is supported on macOS and Windows")
	}
	executable, e := os.Executable()
	if e != nil {
		return nil, e
	}
	workdir, e := os.Getwd()
	if e != nil {
		return nil, e
	}
	path, e := resolveHelper(executable, workdir, os.Getenv("BTFLING_BLE_HELPER"), runtime.GOOS)
	if e != nil {
		return nil, e
	}
	ctx, helperCancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, path, mode, id, name)
	in, e := cmd.StdinPipe()
	if e != nil {
		helperCancel()
		return nil, e
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		helperCancel()
		in.Close()
		return nil, e
	}
	cmd.Stderr = os.Stderr
	h := &helper{cmd: cmd, in: in, events: make(chan message, 64), done: make(chan struct{}), cancel: helperCancel}
	if e = cmd.Start(); e != nil {
		helperCancel()
		return nil, fmt.Errorf("start native Bluetooth adapter %s: %w", path, e)
	}
	go func() {
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 4096), 65536)
		for scan.Scan() {
			var m message
			if e := json.Unmarshal(scan.Bytes(), &m); e != nil {
				h.err = fmt.Errorf("native adapter JSON: %w", e)
				break
			}
			select {
			case h.events <- m:
			case <-ctx.Done():
			}
		}
		if e := scan.Err(); e != nil {
			h.err = e
		}
		if e := cmd.Wait(); e != nil && h.err == nil {
			h.err = e
		}
		close(h.done)
	}()
	return h, nil
}
func (h *helper) send(m message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return json.NewEncoder(h.in).Encode(m)
}
func (h *helper) next(ctx context.Context) (message, error) {
	decode := func(m message) (message, error) {
		if m.Event == "error" {
			return m, errors.New(m.Error)
		}
		return m, nil
	}
	select {
	case m := <-h.events:
		return decode(m)
	default:
	}
	select {
	case m := <-h.events:
		return decode(m)
	case <-h.done:
		// Drain final diagnostics before reporting a generic process exit.
		select {
		case m := <-h.events:
			return decode(m)
		default:
		}
		if h.err != nil {
			return message{}, h.err
		}
		return message{}, io.EOF
	case <-ctx.Done():
		return message{}, ctx.Err()
	}
}
func (h *helper) stop() error {
	h.stopOnce.Do(func() {
		h.cancel()
		h.in.Close()
		if h.cmd.Process != nil {
			_ = h.cmd.Process.Kill()
		}
		<-h.done
	})
	return nil
}
func Discover(ctx context.Context, found func(Peer)) error {
	h, e := start(ctx, "scan", "", "")
	if e != nil {
		return e
	}
	defer h.stop()
	for {
		m, e := h.next(ctx)
		if e != nil {
			return e
		}
		if m.Event == "peer" && validID(m.ID) && config.ValidName(m.Name) {
			found(Peer{ID: m.ID, Name: m.Name, Pairing: m.Pairing})
		}
	}
}
func Open(ctx context.Context, listen bool, id, name string) (io.ReadWriteCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	mode := "connect"
	if listen {
		mode = "listen"
	}
	h, e := start(ctx, mode, id, name)
	if e != nil {
		cancel()
		return nil, e
	}
	for {
		m, e := h.next(ctx)
		if e != nil {
			cancel()
			h.stop()
			return nil, e
		}
		if m.Event == "ready" {
			break
		}
	}
	return attachStream(ctx, cancel, h, listen), nil
}

// ListenPair advertises a pairing-enabled identity while a separate scanner runs.
func ListenPair(ctx context.Context, id, name string) (io.ReadWriteCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	h, e := start(ctx, "pair-listen", id, name)
	if e != nil {
		cancel()
		return nil, e
	}
	for {
		m, e := h.next(ctx)
		if e != nil {
			cancel()
			h.stop()
			return nil, e
		}
		if m.Event == "ready" {
			break
		}
	}
	return attachStream(ctx, cancel, h, true), nil
}
func attachStream(ctx context.Context, cancel context.CancelFunc, h *helper, listen bool) *stream {
	var lastPacket atomic.Int64
	finish := make(chan struct{})
	pumpDone := make(chan struct{})
	s := newStream(ctx, cancel, func() error {
		if !listen {
			close(finish)
			select {
			case <-pumpDone:
			case <-time.After(2 * time.Second):
			}
		}
		cancel()
		return h.stop()
	})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if at := lastPacket.Load(); at != 0 && time.Since(time.Unix(0, at)) > 45*time.Second {
					s.writer.CloseWithError(errors.New("Bluetooth peer stopped responding for 45 seconds"))
					cancel()
					return
				}
			}
		}
	}()
	go func() {
		defer cancel()
		defer close(pumpDone)
		if listen {
			if e := h.send(message{Op: "value", Data: s.m.packet()}); e != nil {
				return
			}
			for {
				m, e := h.next(ctx)
				if e != nil {
					s.writer.CloseWithError(e)
					return
				}
				if m.Event == "packet" {
					lastPacket.Store(time.Now().UnixNano())
					if e = s.m.accept(m.Data); e != nil {
						return
					}
					if e = h.send(message{Op: "value", Data: s.m.packet()}); e != nil {
						return
					}
				}
			}
		}
		lastPacket.Store(time.Now().UnixNano())
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			closing := false
			select {
			case <-finish:
				closing = true
			default:
			}
			if e := h.send(message{Op: "exchange", Data: s.m.packet()}); e != nil {
				return
			}
			m, e := h.next(ctx)
			if e != nil {
				s.writer.CloseWithError(e)
				return
			}
			lastPacket.Store(time.Now().UnixNano())
			if m.Event != "packet" || s.m.accept(m.Data) != nil {
				return
			}
			if closing {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return s
}

func validID(id string) bool { b, e := hex.DecodeString(id); return e == nil && len(b) == 16 }
