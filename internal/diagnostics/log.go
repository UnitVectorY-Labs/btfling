// Package diagnostics provides opt-in, bounded diagnostic logging. It never
// records stream payloads, private keys, or the contents of transferred files.
package diagnostics

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
)

type Event struct {
	Sequence  uint64    `json:"sequence"`
	Time      time.Time `json:"time"`
	ElapsedMS int64     `json:"elapsed_ms"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Phase     string    `json:"phase"`
	Message   string    `json:"message"`
}

type Log struct {
	mu               sync.Mutex
	encoder          *json.Encoder
	started          time.Time
	sequence         uint64
	recent, problems []Event
	facts            []Event
	err              error
}

func New(w io.Writer) *Log { return &Log{encoder: json.NewEncoder(w), started: time.Now()} }

func (l *Log) Record(level, component, phase, message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sequence++
	e := Event{l.sequence, time.Now().UTC(), time.Since(l.started).Milliseconds(), level, component, phase, bounded(message, 4096)}
	l.write(e)
	e.Message = bounded(e.Message, 512)
	l.recent = append(l.recent, e)
	if len(l.recent) > 24 {
		l.recent = l.recent[1:]
	}
	if level == "error" || level == "warning" {
		l.problems = append(l.problems, e)
		if len(l.problems) > 12 {
			l.problems = l.problems[1:]
		}
	}
	if phase == "adapter" {
		l.facts = append(l.facts, e)
		if len(l.facts) > 8 {
			l.facts = l.facts[1:]
		}
	}
}

// Payload is written only locally, and excluded from snapshots to avoid
// recursively retransmitting previously imported remote reports.
func (l *Log) Payload(phase string, value any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.write(struct {
		Time  time.Time `json:"time"`
		Phase string    `json:"phase"`
		Data  any       `json:"data"`
	}{time.Now().UTC(), phase, value})
}

func (l *Log) write(value any) {
	if l.err == nil {
		l.err = l.encoder.Encode(value)
	}
}
func (l *Log) Err() error { l.mu.Lock(); defer l.mu.Unlock(); return l.err }
func (l *Log) Snapshot() (recent, problems []Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.recent...), append([]Event(nil), l.problems...)
}
func (l *Log) Facts() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.facts...)
}
func bounded(s string, n int) string {
	s = strings.ToValidUTF8(s, "?")
	if len(s) <= n {
		return s
	}
	// Avoid cutting a UTF-8 encoding in half.
	return strings.ToValidUTF8(s[:n], "") + "…"
}

type contextKey struct{}

func WithLog(ctx context.Context, l *Log) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}
func FromContext(ctx context.Context) *Log { l, _ := ctx.Value(contextKey{}).(*Log); return l }
func Emit(ctx context.Context, level, component, phase, message string) {
	if l := FromContext(ctx); l != nil {
		l.Record(level, component, phase, message)
	}
}

// LineWriter collects bounded native stderr lines. Independent helpers must
// each get a separate writer; Log itself serializes their concurrent records.
type LineWriter struct {
	mu        sync.Mutex
	ctx       context.Context
	component string
	pending   string
}

func NewLineWriter(ctx context.Context, component string) *LineWriter {
	return &LineWriter{ctx: ctx, component: component}
}
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 && len(w.pending) < 4096 {
			break
		}
		if i < 0 || i > 4096 {
			i = 4096
		}
		line := w.pending[:i]
		w.pending = w.pending[i:]
		w.pending = strings.TrimPrefix(w.pending, "\n")
		Emit(w.ctx, "info", w.component, "stderr", strings.TrimSuffix(line, "\r"))
	}
	return len(p), nil
}
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		Emit(w.ctx, "info", w.component, "stderr", w.pending)
		w.pending = ""
	}
}
