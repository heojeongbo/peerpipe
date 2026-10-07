package peerpipe

import (
	"context"
	"errors"
	"github.com/pion/webrtc/v4"
	"sync/atomic"
	"testing"
	"time"
)

func TestReleaseAndStopBeforeOpen(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	var push func(int)
	var releases, stops atomic.Int32
	ch, err := Open(context.Background(), pc, Options[int]{Label: "samples", Policy: Policy{BeforeOpen: HoldLatestBeforeOpen},
		Source: Pushed(func(f func(int)) (func() error, error) {
			push = f
			f(1)
			return func() error { stops.Add(1); return nil }, nil
		}),
		Encode: func(v int, emit func([]byte)) error { emit([]byte{byte(v)}); return nil }, Release: func(int) { releases.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	push(2)
	if releases.Load() != 1 {
		t.Fatal("replaced value was not released")
	}
	if err := ch.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch.Done():
	case <-time.After(time.Second):
		t.Fatal("cleanup blocked")
	}
	push(3)
	if releases.Load() != 3 || stops.Load() != 1 {
		t.Fatalf("release=%d stop=%d", releases.Load(), stops.Load())
	}
}

func TestSourceFailureReleasesHeldValue(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	var released int
	_, err = Open(context.Background(), pc, Options[int]{Policy: Policy{BeforeOpen: HoldLatestBeforeOpen},
		Source: Pushed(func(push func(int)) (func() error, error) { push(1); return nil, errors.New("subscribe failed") }),
		Encode: func(int, func([]byte)) error { return nil }, Release: func(int) { released++ }})
	if err == nil || released != 1 {
		t.Fatalf("err=%v released=%d", err, released)
	}
}

type sender struct {
	queued uint64
	sent   int
	err    error
}

func (s *sender) BufferedAmount() uint64 { return s.queued }
func (s *sender) Send([]byte) error      { s.sent++; return s.err }
func TestCongestionIsIndependentOfReliability(t *testing.T) {
	s := &sender{queued: 11}
	if sent, err := TrySend(s, []byte{1}, 10); sent || err != nil || s.sent != 0 {
		t.Fatal("congested send was not dropped")
	}
	s.queued = 10
	s.err = errors.New("closed")
	if sent, err := TrySend(s, []byte{1}, 10); sent || !errors.Is(err, s.err) || s.sent != 1 {
		t.Fatal("send error lost")
	}
}

func TestTrySendAcceptsAtLimitAndDefaultsZeroLimit(t *testing.T) {
	for _, tc := range []struct {
		queued, limit uint64
		want          bool
	}{
		{10, 10, true}, {DefaultMaxBufferedAmount, 0, true}, {DefaultMaxBufferedAmount + 1, 0, false},
	} {
		s := &sender{queued: tc.queued}
		sent, err := TrySend(s, []byte{1}, tc.limit)
		if sent != tc.want || err != nil || (s.sent == 1) != tc.want {
			t.Fatalf("queued=%d limit=%d: sent=%v err=%v calls=%d", tc.queued, tc.limit, sent, err, s.sent)
		}
	}
}

func TestCancellationDuringSubscribeCleansUpWithoutOpening(t *testing.T) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var released, stopped int
	ch, err := Open(ctx, pc, Options[int]{
		Policy: Policy{BeforeOpen: HoldLatestBeforeOpen},
		Source: Pushed(func(push func(int)) (func() error, error) {
			push(1)
			cancel()
			return func() error { stopped++; return nil }, nil
		}),
		Encode:  func(int, func([]byte)) error { t.Fatal("encoded before open"); return nil },
		Release: func(int) { released++ },
	})
	if ch != nil || !errors.Is(err, context.Canceled) || released != 1 || stopped != 1 {
		t.Fatalf("channel=%v error=%v released=%d stopped=%d", ch, err, released, stopped)
	}
}
