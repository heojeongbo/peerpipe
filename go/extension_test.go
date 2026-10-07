package peerpipe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestNativeChannelOptions(t *testing.T) {
	pc, received, connect := peerPair(t)
	ordered, lifetime, protocol := false, uint16(250), "example/samples"
	readyToSend := make(chan struct{})
	ch, err := Open(t.Context(), pc, Options[int]{
		Label: "custom", Policy: Policy{Reliable: true, BeforeOpen: HoldLatestBeforeOpen},
		DataChannelInit: &webrtc.DataChannelInit{Ordered: &ordered, MaxPacketLifeTime: &lifetime, Protocol: &protocol},
		Source:          Pushed(func(push func(int)) (func() error, error) { push(42); return nil, nil }),
		Encode:          func(v int, emit func([]byte)) error { emit([]byte{byte(v)}); return nil },
		Send: func(ctx context.Context, dc BufferedSender, payload []byte, limit uint64) (bool, error) {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-readyToSend:
				return TrySend(dc, payload, limit)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	dc := ch.DataChannel
	if dc.Ordered() || dc.MaxRetransmits() != nil || dc.MaxPacketLifeTime() == nil || *dc.MaxPacketLifeTime() != lifetime || dc.Protocol() != protocol {
		t.Fatal("native options were replaced by a preset")
	}
	connect()
	select {
	case <-ch.Opened():
	case <-time.After(20 * time.Second):
		t.Fatal("not open")
	}
	select {
	case <-received:
		t.Fatal("send policy bypassed")
	default:
	}
	close(readyToSend)
	if got := next(t, received); !bytes.Equal(got, []byte{42}) {
		t.Fatalf("got %v", got)
	}
}

func TestAttachAlreadyOpenChannel(t *testing.T) {
	pc, received, connect := peerPair(t)
	dc, err := pc.CreateDataChannel("existing", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	connect()
	select {
	case <-opened:
	case <-time.After(20 * time.Second):
		t.Fatal("not open")
	}
	var released, stopped atomic.Int32
	ch, err := Attach(t.Context(), dc, Options[int]{
		Policy: Policy{BeforeOpen: HoldLatestBeforeOpen},
		Source: Pushed(func(push func(int)) (func() error, error) {
			push(7)
			return func() error { stopped.Add(1); return nil }, nil
		}),
		Encode:  func(v int, emit func([]byte)) error { emit([]byte{byte(v)}); return nil },
		Release: func(int) { released.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if ch.DataChannel != dc {
		t.Fatal("replaced channel")
	}
	select {
	case <-ch.Opened():
	case <-time.After(time.Second):
		t.Fatal("missing open notification")
	}
	if got := next(t, received); !bytes.Equal(got, []byte{7}) {
		t.Fatalf("got %v", got)
	}
	ch.Close()
	select {
	case <-ch.Done():
	case <-time.After(time.Second):
		t.Fatal("cleanup blocked")
	}
	if released.Load() != 1 || stopped.Load() != 1 {
		t.Fatalf("released=%d stopped=%d", released.Load(), stopped.Load())
	}
}

func TestAttachFailureLeavesCallerChannelUsable(t *testing.T) {
	pc, received, connect := peerPair(t)
	dc, err := pc.CreateDataChannel("caller", nil)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan struct{})
	dc.OnOpen(func() { close(opened) })
	want := errors.New("subscription failed")
	_, err = Attach(t.Context(), dc, Options[int]{
		Source: Pushed(func(func(int)) (func() error, error) { return nil, want }),
		Encode: func(int, func([]byte)) error { return nil },
	})
	if !errors.Is(err, want) {
		t.Fatal(err)
	}
	connect()
	select {
	case <-opened:
	case <-time.After(20 * time.Second):
		t.Fatal("caller handler replaced")
	}
	if err := dc.Send([]byte("still owned")); err != nil {
		t.Fatal(err)
	}
	if got := next(t, received); string(got) != "still owned" {
		t.Fatalf("got %q", got)
	}
}

func TestCustomSendCanceledByChannelClose(t *testing.T) {
	pc, _, connect := peerPair(t)
	entered := make(chan struct{})
	errorsSeen := make(chan error, 1)
	var released, stopped atomic.Int32
	ch, err := Open(t.Context(), pc, Options[int]{
		Policy: Policy{BeforeOpen: HoldLatestBeforeOpen},
		Source: Pushed(func(push func(int)) (func() error, error) {
			push(1)
			return func() error { stopped.Add(1); return nil }, nil
		}),
		Encode:  func(_ int, emit func([]byte)) error { emit([]byte("waiting")); return nil },
		Release: func(int) { released.Add(1) },
		Send: func(ctx context.Context, _ BufferedSender, _ []byte, limit uint64) (bool, error) {
			if limit != DefaultMaxBufferedAmount {
				return false, errors.New("unresolved limit")
			}
			close(entered)
			<-ctx.Done()
			return false, ctx.Err()
		},
		OnError: func(_ string, err error) { errorsSeen <- err },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	connect()
	select {
	case <-entered:
	case <-time.After(20 * time.Second):
		t.Fatal("send not entered")
	}
	// Native closure must cancel the custom sender too, without Channel.Close.
	ch.DataChannel.Close()
	select {
	case <-ch.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("send prevented cleanup")
	}
	if released.Load() != 1 || stopped.Load() != 1 {
		t.Fatalf("released=%d stopped=%d", released.Load(), stopped.Load())
	}
	select {
	case err := <-errorsSeen:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	default:
		t.Fatal("missing send error")
	}
}

func TestCustomSendOutcomesAndErrorCallbacks(t *testing.T) {
	var output logBuffer
	s := &sender{}
	want := errors.New("policy failed")
	var calls, releases, drops int
	var failures []string
	p := &pump[int]{ctx: t.Context(), log: slog.New(slog.NewJSONHandler(&output, nil)), dc: s,
		policy:  Policy{Compress: true, MaxBufferedAmount: 123},
		encode:  func(_ int, emit func([]byte)) error { emit([]byte("encode me")); return nil },
		release: func(int) { releases++ },
		onDrop:  func(int) { drops++ },
		onError: func(op string, err error) {
			if !errors.Is(err, want) {
				t.Error(err)
			}
			failures = append(failures, op)
		},
		send: func(ctx context.Context, dc BufferedSender, payload []byte, limit uint64) (bool, error) {
			if ctx != t.Context() || dc != s || limit != 123 || bytes.Equal(payload, []byte("encode me")) {
				t.Fatal("bad send inputs")
			}
			calls++
			switch calls {
			case 1:
				return false, nil
			case 2:
				return true, nil
			default:
				return false, want
			}
		},
	}
	for i := 0; i < 4; i++ {
		if !p.deliver(i) {
			t.Fatal("unexpected stop")
		}
	}
	p.finish(func() error { return want })
	if releases != 4 || drops != 1 || p.sentPayloads != 1 || p.droppedPayloads != 1 || s.sent != 0 {
		t.Fatalf("releases=%d drops=%d sent=%d discarded=%d native=%d", releases, drops, p.sentPayloads, p.droppedPayloads, s.sent)
	}
	if len(failures) != 3 || failures[0] != "send" || failures[1] != "send" || failures[2] != "cleanup" {
		t.Fatal(failures)
	}
}

func TestAttachRejectsInvalidConfigurationBeforeSubscribing(t *testing.T) {
	pc, _, _ := peerPair(t)
	dc, err := pc.CreateDataChannel("existing", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := Options[int]{
		Source: Pushed(func(func(int)) (func() error, error) { t.Fatal("subscribed"); return nil, nil }),
		Encode: func(int, func([]byte)) error { return nil },
	}
	for _, change := range []func(*Options[int]){
		func(o *Options[int]) { o.Label = "other" },
		func(o *Options[int]) { o.DataChannelInit = &webrtc.DataChannelInit{} },
	} {
		opts := base
		change(&opts)
		if ch, err := Attach(t.Context(), dc, opts); err == nil || ch != nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	dc.Close()
	if ch, err := Attach(t.Context(), dc, base); err == nil || ch != nil {
		t.Fatal("accepted closed channel")
	}
}
