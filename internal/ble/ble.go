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
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/config"
)

type Peer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Pairing bool   `json:"pairing"`
}
type message struct {
	Event    string `json:"event,omitempty"`
	Op       string `json:"op,omitempty"`
	Data     []byte `json:"data,omitempty"`
	Error    string `json:"error,omitempty"`
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Pairing  bool   `json:"pairing,omitempty"`
	MaxValue int    `json:"max_value,omitempty"`
	Version  int    `json:"version,omitempty"`
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
	if e := ctx.Err(); e != nil {
		return message{}, e
	}
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
		if m.Event == "peer" && m.Version == WireVersion && validID(m.ID) && config.ValidName(m.Name) {
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
	mValue := 0
	var early [][]byte
	for {
		m, e := h.next(ctx)
		if e != nil {
			cancel()
			h.stop()
			return nil, e
		}
		if m.Event == "packet" {
			if len(early) >= 16 {
				cancel()
				h.stop()
				return nil, errors.New("too many pre-subscription frames")
			}
			early = append(early, m.Data)
		}
		if m.Event == "ready" {
			mValue = m.MaxValue
			break
		}
	}
	return attachStream(ctx, cancel, h, mValue, early)
}

// ListenPair advertises a pairing-enabled identity while a separate scanner runs.
func ListenPair(ctx context.Context, id, name string) (io.ReadWriteCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	h, e := start(ctx, "pair-listen", id, name)
	if e != nil {
		cancel()
		return nil, e
	}
	mValue := 0
	var early [][]byte
	for {
		m, e := h.next(ctx)
		if e != nil {
			cancel()
			h.stop()
			return nil, e
		}
		if m.Event == "packet" {
			if len(early) >= 16 {
				cancel()
				h.stop()
				return nil, errors.New("too many pre-subscription frames")
			}
			early = append(early, m.Data)
		}
		if m.Event == "ready" {
			mValue = m.MaxValue
			break
		}
	}
	return attachStream(ctx, cancel, h, mValue, early)
}

// Native admissions are local OS queue completions, never remote acknowledgements.
// Sixteen commands bound pipe/native buffering while allowing radio pipelining.
func attachStream(ctx context.Context, cancel context.CancelFunc, h *helper, maxValue int, early [][]byte) (*stream, error) {
	slots := make(chan struct{}, 16)
	completed := make(chan struct{}, 1)
	s, e := newStream(ctx, maxValue, func(ctx context.Context, p []byte) error {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		return h.send(message{Op: "send", Data: p})
	}, func() error { cancel(); return h.stop() })
	if e != nil {
		cancel()
		h.stop()
		return nil, e
	}
	s.drain = func(ctx context.Context) error {
		for len(slots) > 0 {
			select {
			case <-completed:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	receivePacket := func(p []byte) error {
		s.mu.Lock()
		if h.cmd.Args[1] == "connect" {
			s.metrics.Notifications++
		} else {
			s.metrics.Writes++
		}
		s.mu.Unlock()
		return s.accept(p)
	}
	for _, p := range early {
		if e = receivePacket(p); e != nil {
			s.Close()
			return nil, e
		}
	}
	go func() {
		defer func() {
			if os.Getenv("BTFLING_BLE_DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "BLE stream closed: %+v\n", s.Stats())
			}
		}()
		defer h.stop()
		for {
			m, e := h.next(s.ctx)
			if e != nil {
				s.fail(e)
				return
			}
			switch m.Event {
			case "packet":
				if e = receivePacket(m.Data); e != nil {
					return
				}
			case "sent":
				s.mu.Lock()
				if h.cmd.Args[1] == "connect" {
					s.metrics.Writes++
				} else {
					s.metrics.Notifications++
				}
				s.mu.Unlock()
				select {
				case <-slots:
					select {
					case completed <- struct{}{}:
					default:
					}
				default:
					s.fail(errors.New("unexpected native send completion"))
					return
				}
			case "size":
				if e = s.setValueSize(m.MaxValue); e != nil {
					s.fail(e)
					return
				}
			}
		}
	}()
	// A one-second health check is independent of the event-driven data path.
	// Idle sessions are legal; only unacknowledged data or setup can stall.
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		lastProgress := time.Now()
		var ack uint64
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-tick.C:
				s.mu.Lock()
				pending := !s.peerOpened || s.sent != s.acked || (s.finRequested && !s.finACK)
				if !pending || s.acked != ack {
					lastProgress = time.Now()
					ack = s.acked
				}
				s.mu.Unlock()
				if time.Since(lastProgress) > 45*time.Second {
					s.fail(errors.New("Bluetooth stream made no progress for 45 seconds"))
					return
				}
			}
		}
	}()
	return s, nil
}

func validID(id string) bool { b, e := hex.DecodeString(id); return e == nil && len(b) == 16 }
