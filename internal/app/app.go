package app

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

var openBluetooth = ble.Open

const help = `btfling — encrypted Bluetooth streams

Usage:
  btfling init NAME
  btfling keygen
  btfling pair [ID] [-as NAME] [-trust FINGERPRINT] [-timeout 0]
  btfling diagnose [-timeout 90s] [-log FILE] [-peer ID] [-trust FINGERPRINT]
  btfling discover [-timeout 10s]
  btfling peers
  btfling rename OLD NEW
  btfling forget NAME
  btfling send NAME [-in FILE] [-timeout 0]
  btfling receive NAME [-out FILE] [-timeout 0]
  btfling tui

Pair tries advertising and discovery. Run it on both devices; without an ID, the
CLI pairs with the first nearby device in pairing mode. The TUI lets you choose.
If local advertising fails, discovery continues; select the other device here.
Peer names are learned automatically; -as overrides the local alias.
Pairing trusts first-use keys by default. Compare fingerprints out of band,
or supply -trust FINGERPRINT to verify the remote key before saving it.
Transfers use mutually authenticated TLS 1.3.

Send reads stdin by default; receive writes binary payload to stdout.
-in reads a file; -out creates a new file without overwriting an existing path.
-timeout covers the whole operation; 0 waits indefinitely. Ctrl-C cancels.
Missing configuration and a local encryption key are created automatically.
Run btfling tui for the interactive client, pairing, and transfer tabs.

Configuration defaults to ~/.config/btfling/config.yaml.
Override it once before any command: btfling --config DIR COMMAND ...
`

type usageError struct{ error }

func IsUsage(e error) bool { var u usageError; return errors.As(e, &u) }
func usage(s string) error { return usageError{errors.New(s)} }
func flags(name string, errOut io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(errOut)
	return f
}

// splitTarget supports the natural Unix spelling "send alice -in file".
func splitTarget(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}
func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	dir, e := config.DefaultDir()
	if e != nil {
		return e
	}
	if len(args) >= 2 && (args[0] == "--config" || args[0] == "-config") {
		dir = args[1]
		args = args[2:]
	}
	if len(args) == 0 {
		if _, e = ensureConfig(dir, errOut); e != nil {
			return e
		}
		_, e = io.WriteString(out, help)
		return e
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, e = io.WriteString(out, help)
		return e
	}
	cmd, args := args[0], args[1:]
	if cmd == "init" {
		if len(args) != 1 {
			return usage("init requires a local nickname")
		}
		c, e := config.Init(dir, args[0])
		if e == nil {
			fmt.Fprintln(errOut, "Initialized", c.Name, "in", dir)
		}
		return e
	}
	switch cmd {
	case "keygen", "peers", "rename", "forget", "discover", "pair", "diagnose", "send", "receive", "tui":
	default:
		return usage("unknown command " + cmd + "; run btfling help")
	}
	c, e := ensureConfig(dir, errOut)
	if e != nil {
		return e
	}
	switch cmd {
	case "keygen":
		if len(args) != 0 {
			return usage("keygen takes no arguments")
		}
		fmt.Fprintln(out, config.Fingerprint(c.PublicKey()))
		return nil
	case "peers":
		if len(args) != 0 {
			return usage("peers takes no arguments")
		}
		for _, p := range c.Peers {
			fp := config.Fingerprint(p.PublicKey)
			fmt.Fprintf(out, "%s\t%s\t%s\n", p.Name, p.ID, fp)
		}
		return nil
	case "rename":
		if len(args) != 2 || !config.ValidName(args[1]) {
			return usage("rename requires OLD NEW nicknames")
		}
		for _, p := range c.Peers {
			if p.Name == args[1] {
				return errors.New("nickname already exists")
			}
		}
		for i, p := range c.Peers {
			if p.Name == args[0] {
				c.Peers[i].Name = args[1]
				return c.Save(dir)
			}
		}
		return errors.New("unknown peer")
	case "forget":
		if len(args) != 1 {
			return usage("forget requires a nickname")
		}
		for i, p := range c.Peers {
			if p.Name == args[0] {
				c.Peers = append(c.Peers[:i], c.Peers[i+1:]...)
				return c.Save(dir)
			}
		}
		return errors.New("unknown peer")
	case "discover":
		f := flags(cmd, errOut)
		timeout := f.Duration("timeout", 10*time.Second, "scan duration")
		if e = f.Parse(args); e != nil {
			return usage(e.Error())
		}
		if f.NArg() != 0 || *timeout <= 0 {
			return usage("discover requires a positive -timeout")
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		seen := map[string]bool{}
		e = ble.Discover(ctx, func(p ble.Peer) {
			if !seen[p.ID] {
				seen[p.ID] = true
				fmt.Fprintf(out, "%s\t%s\n", p.ID, p.Name)
			}
		})
		if errors.Is(e, context.DeadlineExceeded) {
			return nil
		}
		return e
	case "pair":
		return pair(ctx, dir, c, args, errOut)
	case "diagnose":
		return diagnose(ctx, dir, c, args, out, errOut)
	case "send", "receive":
		return transfer(ctx, c, cmd, args, in, out, errOut, nil)
	case "tui":
		if len(args) != 0 {
			return usage("tui takes no arguments")
		}
		return runTUI(ctx, dir, in, errOut)
	default:
		return usage("unknown command " + cmd + "; run btfling help")
	}
}
func ensureConfig(dir string, errOut io.Writer) (*config.Config, error) {
	c, created, err := config.LoadOrInit(dir)
	if err != nil {
		return nil, fmt.Errorf("load or create config: %w", err)
	}
	if created {
		fmt.Fprintln(errOut, "Initialized", c.Name, "in", dir)
	}
	return c, nil
}
func operationContext(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}
func pair(ctx context.Context, dir string, c *config.Config, args []string, errOut io.Writer) error {
	target, args := splitTarget(args)
	f := flags("pair", errOut)
	alias := f.String("as", "", "override the learned peer nickname")
	trust := f.String("trust", "", "optional expected SHA-256 public-key fingerprint")
	timeout := f.Duration("timeout", 0, "overall timeout")
	if e := f.Parse(args); e != nil {
		return usage(e.Error())
	}
	if target == "" && f.NArg() == 1 {
		target = f.Arg(0)
	} else if f.NArg() != 0 {
		return usage("unexpected pair arguments")
	}
	if *timeout < 0 {
		return usage("pair requires a nonnegative timeout")
	}
	if *alias != "" && !config.ValidName(*alias) {
		return usage("invalid peer nickname")
	}
	if *trust != "" {
		b, e := hex.DecodeString(*trust)
		if e != nil || len(b) != 32 {
			return usage("-trust requires a 64-hex-digit SHA-256 fingerprint")
		}
	}
	if target != "" {
		b, e := hex.DecodeString(target)
		if e != nil || len(b) != 16 {
			return usage("pair requires a discovered 32-hex-character device ID")
		}
	}
	ctx, cancel := operationContext(ctx, *timeout)
	defer cancel()
	fmt.Fprintln(errOut, "Local key fingerprint:", config.Fingerprint(c.PublicKey()))
	p, e := runPairing(ctx, dir, c, pairOptions{Target: target, Alias: *alias, Trust: *trust, AutoSelect: true}, nil, func(p ble.Peer) {
		if p.Pairing {
			fmt.Fprintf(errOut, "Nearby: %s  %s\n", p.Name, p.ID)
		}
	}, func(s string) { fmt.Fprintln(errOut, s) })
	if e != nil {
		return e
	}
	fmt.Fprintln(errOut, "Saved peer", p.Name)
	fmt.Fprintln(errOut, "Remote key fingerprint:", config.Fingerprint(p.PublicKey))
	return nil
}
func transfer(ctx context.Context, c *config.Config, cmd string, args []string, in io.Reader, out, errOut io.Writer, progress protocol.Progress) error {
	target, args := splitTarget(args)
	f := flags(cmd, errOut)
	input := f.String("in", "", "input file (default stdin)")
	output := f.String("out", "", "new output file (default stdout)")
	timeout := f.Duration("timeout", 0, "overall timeout")
	if e := f.Parse(args); e != nil {
		return usage(e.Error())
	}
	if target == "" && f.NArg() == 1 {
		target = f.Arg(0)
	} else if f.NArg() != 0 {
		return usage("unexpected transfer arguments")
	}
	if target == "" || *timeout < 0 {
		return usage(cmd + " requires a peer nickname and a nonnegative timeout")
	}
	if cmd == "send" && *output != "" || cmd == "receive" && *input != "" {
		return usage("send uses -in; receive uses -out")
	}
	p, e := c.Peer(target)
	if e != nil {
		return e
	}
	if len(p.PublicKey) != 32 {
		return errors.New("invalid trusted peer public key")
	}
	if cmd == "send" && *input != "" {
		file, e := os.Open(*input)
		if e != nil {
			return e
		}
		defer file.Close()
		in = file
	}
	var temp *os.File
	committed := false
	commit := func() error { return nil }
	if cmd == "receive" && *output != "" {
		if _, e = os.Lstat(*output); e == nil {
			return errors.New("output path already exists")
		} else if !os.IsNotExist(e) {
			return e
		}
		temp, e = os.CreateTemp(filepath.Dir(*output), ".btfling-*")
		if e != nil {
			return e
		}
		defer func() {
			temp.Close()
			if !committed {
				os.Remove(temp.Name())
			}
		}()
		out = temp
		commit = func() error {
			if e := temp.Sync(); e != nil {
				return e
			}
			if e := temp.Close(); e != nil {
				return e
			}
			// A hard link atomically publishes without overwriting a concurrently created destination.
			if e := os.Link(temp.Name(), *output); e != nil {
				return fmt.Errorf("publish received file (requires hard-link capable filesystem): %w", e)
			}
			committed = true
			return os.Remove(temp.Name())
		}
	}
	ctx, cancel := operationContext(ctx, *timeout)
	defer cancel()
	if cmd == "send" {
		if file, ok := in.(*os.File); ok {
			stop := context.AfterFunc(ctx, func() { file.Close() })
			defer stop()
		}
	} else {
		if file, ok := out.(*os.File); ok {
			stop := context.AfterFunc(ctx, func() { file.Close() })
			defer stop()
		}
	}
	listen := cmd == "receive"
	id := p.ID
	if listen {
		id = c.ID
	}
	fmt.Fprintln(errOut, "Waiting for", p.Name, "over Bluetooth…")
	conn, e := openBluetooth(ctx, listen, id, c.Name)
	if e != nil {
		return e
	}
	defer conn.Close()
	if session, ok := conn.(interface{ Done() <-chan struct{} }); ok {
		stopped := make(chan struct{})
		defer close(stopped)
		go func() {
			select {
			case <-session.Done():
				cancel()
			case <-stopped:
			}
		}()
	}
	s, e := protocol.Open(ctx, conn, c, &p, !listen, "send")
	if e != nil {
		return e
	}
	fmt.Fprintln(errOut, "Connected (encrypted)")
	started := time.Now()
	var transferred int64
	report := func(n int64) {
		transferred = n
		if progress != nil {
			progress(n)
		}
	}
	if listen {
		e = protocol.Receive(s, out, commit, report)
	} else {
		e = protocol.Send(s, in, report)
	}
	if e == nil {
		e = shutdownSession(conn, s)
	}
	if e == nil {
		elapsed := time.Since(started)
		fmt.Fprintf(errOut, "Transfer complete: %d bytes in %s (%.1f KiB/s)\n", transferred, elapsed.Round(time.Millisecond), float64(transferred)/1024/elapsed.Seconds())
		if os.Getenv("BTFLING_BLE_DEBUG") != "" {
			if stats, ok := conn.(interface{ Stats() ble.Metrics }); ok {
				fmt.Fprintf(errOut, "BLE: %+v\n", stats.Stats())
			}
		}
	}
	return e
}

// shutdownSession completes TLS and transport shutdown after the encrypted
// application confirmation. In-memory app tests may supply a simpler stream.
func shutdownSession(conn io.ReadWriteCloser, s *protocol.Session) error {
	transport, ok := conn.(interface {
		Shutdown() error
		SetDeadline(time.Time) error
	})
	if !ok {
		return nil
	}
	shutdownDeadline := time.Now().Add(5 * time.Second)
	if e := transport.SetDeadline(shutdownDeadline); e != nil {
		return e
	}
	if closer, ok := s.ReadWriter.(interface{ CloseWrite() error }); ok {
		if e := closer.CloseWrite(); e != nil {
			return e
		}
		// crypto/tls expires the underlying write deadline after close_notify.
		if e := transport.SetDeadline(shutdownDeadline); e != nil {
			return e
		}
	}
	var tail [1]byte
	n, e := s.Read(tail[:])
	if n != 0 || e == nil {
		return errors.New("unexpected bytes after application completion")
	}
	if e != io.EOF {
		return e
	}
	return transport.Shutdown()
}
