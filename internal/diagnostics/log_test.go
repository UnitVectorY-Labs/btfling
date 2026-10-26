package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentLogsAndBoundedSnapshot(t *testing.T) {
	var buffer bytes.Buffer
	l := New(&buffer)
	l.Record("info", "bluetooth", "adapter", "retained hardware capabilities")
	ctx := WithLog(context.Background(), l)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := NewLineWriter(ctx, "native")
			for range 20 {
				w.Write([]byte("one line\r\nanother"))
				w.Flush()
			}
		})
	}
	wg.Wait()
	l.Record("warning", "radio", "advertising.failed", strings.Repeat("x", 20000))
	l.Payload("remote.paired", map[string]string{"marker": "do-not-echo-this-report"})
	recent, problems := l.Snapshot()
	if facts := l.Facts(); len(facts) != 1 || facts[0].Message != "retained hardware capabilities" {
		t.Fatal("adapter metadata was lost during noisy discovery")
	}
	if len(recent) > 24 || len(problems) != 1 || len(problems[0].Message) > 520 {
		t.Fatal("snapshot is unbounded")
	}
	for _, event := range recent {
		if strings.Contains(event.Message, "do-not-echo-this-report") {
			t.Fatal("imported reports recursively echoed")
		}
	}
	decoder := json.NewDecoder(&buffer)
	var last uint64
	for {
		var event Event
		err := decoder.Decode(&event)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Sequence != 0 {
			if event.Sequence <= last {
				t.Fatal("interleaved or reordered log records")
			}
			last = event.Sequence
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func TestLogRetainsWriteFailure(t *testing.T) {
	l := New(failingWriter{})
	l.Record("info", "test", "start", "start")
	if l.Err() == nil {
		t.Fatal("lost write failure")
	}
}
