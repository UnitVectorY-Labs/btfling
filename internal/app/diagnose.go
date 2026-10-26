package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/UnitVectorY-Labs/btfling/internal/ble"
	"github.com/UnitVectorY-Labs/btfling/internal/config"
	"github.com/UnitVectorY-Labs/btfling/internal/diagnostics"
	"github.com/UnitVectorY-Labs/btfling/internal/protocol"
)

const diagnosticProbeSize = 4096
const diagnosticReportLimit = 24 * 1024

var diagnosticRadio = pairingRadio{listen: ble.ListenDiagnostic, dial: func(ctx context.Context, _ bool, id, name string) (io.ReadWriteCloser, error) {
	return ble.OpenDiagnostic(ctx, id, name)
}, scan: ble.DiscoverDiagnostic}

type diagnosticInfo struct {
	RunID        string `json:"run_id"`
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Fingerprint  string `json:"public_key_fingerprint"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	GoVersion    string `json:"go_version"`
	Revision     string `json:"revision,omitempty"`
	Modified     *bool  `json:"modified_sources,omitempty"`
	Backend      string `json:"bluetooth_backend"`
}
type diagnosticReport struct {
	Schema        int                 `json:"schema"`
	Stage         string              `json:"stage"`
	Info          diagnosticInfo      `json:"info"`
	Recent        []diagnostics.Event `json:"recent_events"`
	Problems      []diagnostics.Event `json:"problems"`
	Facts         []diagnostics.Event `json:"adapter_events"`
	Metrics       *ble.Metrics        `json:"transport_metrics,omitempty"`
	VerifiedBytes int                 `json:"verified_bytes_each_direction"`
}

func diagnose(ctx context.Context, dir string, c *config.Config, args []string, out, errOut io.Writer) error {
	return diagnoseWithRadio(ctx, dir, c, args, out, errOut, diagnosticRadio)
}
func diagnoseWithRadio(ctx context.Context, dir string, c *config.Config, args []string, out, errOut io.Writer, radio pairingRadio) error {
	f := flags("diagnose", errOut)
	timeout := f.Duration("timeout", 90*time.Second, "overall diagnostic timeout")
	logPath := f.String("log", "", "log path (default a new btfling-diagnose-*.log in the current directory)")
	peer := f.String("peer", "", "optional discovered peer ID")
	trust := f.String("trust", "", "optional expected peer public-key fingerprint")
	if err := f.Parse(args); err != nil {
		return usage(err.Error())
	}
	if f.NArg() != 0 || *timeout <= 0 {
		return usage("diagnose requires a positive -timeout and no positional arguments")
	}
	if *peer != "" {
		b, e := hex.DecodeString(*peer)
		if e != nil || len(b) != 16 {
			return usage("-peer requires a 32-hex-character device ID")
		}
	}
	if err := validateFingerprint(*trust); err != nil {
		return usage(err.Error())
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	info := diagnosticInfo{RunID: hex.EncodeToString(random[:]), DeviceID: c.ID, Name: c.Name, Fingerprint: config.Fingerprint(c.PublicKey()), OS: runtime.GOOS, Architecture: runtime.GOARCH, GoVersion: runtime.Version(), Backend: "embedded or cached native helper"}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = setting.Value
			case "vcs.modified":
				modified := setting.Value == "true"
				info.Modified = &modified
			}
		}
	}
	if *logPath == "" {
		*logPath = fmt.Sprintf("btfling-diagnose-%s-%s-%s.log", runtime.GOOS, time.Now().UTC().Format("20060102T150405Z"), info.RunID[:8])
	}
	abs, err := filepath.Abs(*logPath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create diagnostic log (existing files are not overwritten): %w", err)
	}
	log := diagnostics.New(file)
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	ctx = diagnostics.WithLog(ctx, log)
	fmt.Fprintf(out, "Diagnostic log: %s\nDevice: %s (%s)\nRun this command on the other computer too.\n", abs, c.Name, c.ID)
	log.Payload("local.info", info)
	log.Record("info", "diagnostic", "start", fmt.Sprintf("timeout=%s; diagnostic clients only; lower ID initiates first; higher ID falls back after 4s", *timeout))
	if err = log.Err(); err != nil {
		file.Close()
		return err
	}
	remoteReceived := false
	options := pairOptions{Target: *peer, Trust: *trust, AutoSelect: true, Operation: "diagnose-v1", DialDelay: 4 * time.Second}
	options.AfterPair = func(ctx context.Context, s *protocol.Session, conn io.ReadWriteCloser, initiator bool) error {
		fmt.Fprintf(out, "Paired with %s; exchanging reports and testing both directions…\n", s.Remote.Name)
		return exchangeDiagnostics(ctx, s, conn, initiator, info, log, func(report diagnosticReport) {
			remoteReceived = true
			log.Payload("remote."+report.Stage, report)
			fmt.Fprintf(out, "Received %s report from %s (%s/%s); %d recorded problems.\n", report.Stage, report.Info.Name, report.Info.OS, report.Info.Architecture, len(report.Problems))
		})
	}
	_, err = pairWithRadio(ctx, dir, c, options, nil, func(p ble.Peer) {
		log.Record("info", "diagnostic", "peer.discovered", fmt.Sprintf("name=%s id=%s", p.Name, p.ID))
		fmt.Fprintf(out, "Found diagnostic client: %s (%s)\n", p.Name, p.ID)
	}, func(status string) {
		log.Record("info", "pairing", "status", status)
		if strings.HasPrefix(status, "Advertising unavailable") {
			status = "Advertising unavailable; discovery continues. Details recorded in the log."
		}
		if strings.HasPrefix(status, "Discovery unavailable") {
			status = "Discovery unavailable; waiting for the other device to initiate. Details recorded in the log."
		}
		fmt.Fprintln(out, status)
	}, radio)
	if err != nil {
		log.Record("error", "diagnostic", "failed", err.Error())
		if !remoteReceived {
			message := "No remote report was received. This log cannot establish the other computer's failure reason without a working Bluetooth connection."
			log.Record("warning", "diagnostic", "remote.unavailable", message)
			fmt.Fprintln(out, message)
		}
	} else {
		log.Record("info", "diagnostic", "passed", "Pairing, both reports, bidirectional 4096-byte integrity checks, and session shutdown completed")
	}
	logErr := log.Err()
	syncErr := file.Sync()
	closeErr := file.Close()
	if err != nil {
		err = &diagnosticFailure{path: abs, cause: err}
	}
	result := errors.Join(err, logErr, syncErr, closeErr)
	if result == nil {
		fmt.Fprintln(out, "PASS: paired, received both reports, and verified 4096 bytes in each direction.")
	}
	return result
}

type diagnosticFailure struct {
	path  string
	cause error
}

func (e *diagnosticFailure) Error() string {
	message := []rune(strings.Join(strings.Fields(e.cause.Error()), " "))
	if len(message) > 240 {
		message = append(message[:240], []rune("…")...)
	}
	return fmt.Sprintf("diagnostic failed; log: %s: %s", e.path, string(message))
}
func (e *diagnosticFailure) Unwrap() error { return e.cause }

func exchangeDiagnostics(ctx context.Context, s *protocol.Session, conn io.ReadWriteCloser, initiator bool, info diagnosticInfo, log *diagnostics.Log, received func(diagnosticReport)) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	report := func(stage string, verified int) diagnosticReport {
		recent, problems := log.Snapshot()
		r := diagnosticReport{Schema: 1, Stage: stage, Info: info, Recent: recent, Problems: problems, Facts: log.Facts(), VerifiedBytes: verified}
		if stream, ok := conn.(interface{ Stats() ble.Metrics }); ok {
			m := stream.Stats()
			r.Metrics = &m
		}
		return r
	}
	initial, err := exchangeDiagnosticReport(s, initiator, report("paired", 0))
	if err != nil {
		return fmt.Errorf("initial report: %w", err)
	}
	received(initial)
	log.Record("info", "diagnostic", "reports.received", "Received the peer's authenticated initial report")
	probe := make([]byte, diagnosticProbeSize)
	if _, err = rand.Read(probe); err != nil {
		return err
	}
	remoteProbe, err := exchangeDiagnosticFrame(s, initiator, probe)
	if err != nil {
		return fmt.Errorf("bidirectional probe: %w", err)
	}
	if len(remoteProbe) != diagnosticProbeSize {
		return errors.New("peer sent an incorrectly sized diagnostic probe")
	}
	remoteHash := sha256.Sum256(remoteProbe)
	ack, err := exchangeDiagnosticFrame(s, initiator, remoteHash[:])
	if err != nil {
		return fmt.Errorf("probe receipt: %w", err)
	}
	localHash := sha256.Sum256(probe)
	if !bytes.Equal(ack, localHash[:]) {
		return errors.New("peer did not confirm the SHA-256 digest of the sent probe")
	}
	log.Record("info", "diagnostic", "probe.verified", "Both 4096-byte random probes were received and their SHA-256 receipts verified")
	final, err := exchangeDiagnosticReport(s, initiator, report("complete", diagnosticProbeSize))
	if err != nil {
		return fmt.Errorf("final report: %w", err)
	}
	if final.Info.RunID != initial.Info.RunID || final.VerifiedBytes != diagnosticProbeSize {
		return errors.New("peer did not confirm completion of this diagnostic run")
	}
	received(final)
	end, err := exchangeDiagnosticFrame(s, initiator, []byte("diagnostic-complete-v1"))
	if err == nil && string(end) != "diagnostic-complete-v1" {
		err = errors.New("invalid diagnostic completion acknowledgement")
	}
	return err
}

func exchangeDiagnosticFrame(s io.ReadWriter, initiator bool, data []byte) ([]byte, error) {
	if initiator {
		if err := protocol.WriteFrame(s, data); err != nil {
			return nil, err
		}
		return protocol.ReadFrame(s)
	}
	remote, err := protocol.ReadFrame(s)
	if err != nil {
		return nil, err
	}
	if err = protocol.WriteFrame(s, data); err != nil {
		return nil, err
	}
	return remote, nil
}

func exchangeDiagnosticReport(s *protocol.Session, initiator bool, report diagnosticReport) (diagnosticReport, error) {
	for {
		data, err := json.Marshal(report)
		if err != nil {
			return diagnosticReport{}, err
		}
		if len(data) <= diagnosticReportLimit {
			data, err = exchangeDiagnosticFrame(s, initiator, data)
			if err != nil {
				return diagnosticReport{}, err
			}
			if len(data) > diagnosticReportLimit {
				return diagnosticReport{}, errors.New("peer diagnostic report is too large")
			}
			var remote diagnosticReport
			if err = json.Unmarshal(data, &remote); err != nil {
				return remote, err
			}
			if remote.Schema != 1 || remote.Stage != report.Stage || remote.Info.DeviceID != s.Remote.ID || remote.Info.Fingerprint != config.Fingerprint(s.Remote.PublicKey) || remote.Info.Name != s.Remote.Name || len(remote.Info.RunID) != 32 || len(remote.Recent) > 24 || len(remote.Problems) > 12 || len(remote.Facts) > 8 {
				return remote, errors.New("peer diagnostic report does not match the authenticated session or report schema")
			}
			return remote, nil
		}
		if len(report.Recent) > 0 {
			report.Recent = report.Recent[1:]
		} else if len(report.Problems) > 0 {
			report.Problems = report.Problems[1:]
		} else if len(report.Facts) > 0 {
			report.Facts = report.Facts[1:]
		} else {
			return diagnosticReport{}, errors.New("local diagnostic metadata is too large")
		}
	}
}
