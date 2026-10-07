package peerpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

type logBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *logBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.Buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

type correlationKey struct{}

// Model an OTel handler reading correlation from the context passed to Handle.
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	r.AddAttrs(slog.Any("correlation", ctx.Value(correlationKey{})))
	return h.Handler.Handle(ctx, r)
}
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}
func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}

func TestLifecycleLogsKeepCallerContext(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	var output logBuffer
	ctx := context.WithValue(t.Context(), correlationKey{}, "request-123")
	values := make(chan int)
	close(values)
	ch, err := Open(ctx, pc, Options[int]{
		Label: "samples", Logger: slog.New(contextHandler{slog.NewJSONHandler(&output, nil)}),
		Source: Pulled(func() (<-chan int, func() error, error) {
			return values, func() error { return errors.New("cleanup failed") }, nil
		}),
		Encode: func(int, func([]byte)) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	select {
	case <-ch.Done():
	case <-time.After(time.Second):
		t.Fatal("source did not finish")
	}
	seen := map[string]bool{}
	for _, r := range output.records(t) {
		if r["correlation"] != "request-123" || r["component"] != "peerpipe" || r["label"] != "samples" {
			t.Fatalf("lost caller context or channel identity: %v", r)
		}
		seen[r["msg"].(string)] = true
		if r["msg"] == "close subscription failed" && (r["level"] != "WARN" || r["error"] != "cleanup failed") {
			t.Fatalf("bad failure record: %v", r)
		}
		if r["msg"] == "data channel pump stopped" && r["reason"] != "source ended" {
			t.Fatalf("bad stop reason: %v", r)
		}
	}
	for _, msg := range []string{"data channel created", "close subscription failed", "data channel pump stopped"} {
		if !seen[msg] {
			t.Fatalf("missing %s", msg)
		}
	}
}

func TestCongestionLogsOnlyRealSendsAsSuccess(t *testing.T) {
	var output logBuffer
	s := &sender{queued: 11}
	var droppedBytes, released int
	p := &pump[int]{ctx: t.Context(), log: slog.New(slog.NewJSONHandler(&output, nil)), dc: s,
		policy:  Policy{MaxBufferedAmount: 10, Reliable: true, StopOnError: true},
		encode:  func(_ int, emit func([]byte)) error { emit([]byte("private-payload")); return nil },
		release: func(int) { released++ }, onDrop: func(n int) { droppedBytes += n },
	}
	for i := 0; i < 3; i++ {
		if !p.deliver(i) {
			t.Fatal("a congestion drop stopped the source")
		}
	}
	for _, r := range output.records(t) {
		if r["msg"] == "data channel first send ok" {
			t.Fatal("drop logged as send success")
		}
	}
	s.queued = 0
	if !p.deliver(3) {
		t.Fatal("accepted send failed")
	}
	p.finish(func() error { return nil })
	if s.sent != 1 || released != 4 || droppedBytes != 3*len("private-payload") {
		t.Fatalf("sent=%d released=%d dropped bytes=%d", s.sent, released, droppedBytes)
	}
	var congestion, success int
	for _, r := range output.records(t) {
		switch r["msg"] {
		case "data channel congested; dropping payloads":
			congestion++
		case "data channel first send ok":
			success++
		case "data channel pump stopped":
			if r["sent_payloads"] != float64(1) || r["dropped_payloads"] != float64(3) {
				t.Fatalf("bad totals: %v", r)
			}
		}
		if strings.Contains(r["msg"].(string), "private-payload") {
			t.Fatal("payload leaked into logs")
		}
	}
	if congestion != 1 || success != 1 {
		t.Fatalf("congestion=%d success=%d", congestion, success)
	}
}

func TestStopOnErrorStopsLaterEmits(t *testing.T) {
	var output logBuffer
	s := &sender{err: errors.New("send failed")}
	var released int
	p := &pump[int]{ctx: t.Context(), log: slog.New(slog.NewJSONHandler(&output, nil)), dc: s,
		policy:  Policy{StopOnError: true},
		encode:  func(_ int, emit func([]byte)) error { emit([]byte{1}); emit([]byte{2}); return nil },
		release: func(int) { released++ },
	}
	if p.deliver(0) || s.sent != 1 || released != 1 {
		t.Fatalf("sent=%d released=%d", s.sent, released)
	}
}
