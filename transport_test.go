package peerpipe

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"io"

	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func peerPair(t *testing.T) (*webrtc.PeerConnection, <-chan []byte, func()) {
	t.Helper()
	var settings webrtc.SettingEngine
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
	a, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("connection states", a.ConnectionState(), b.ConnectionState(), a.ICEConnectionState(), b.ICEConnectionState())
		}
		a.Close()
		b.Close()
	})
	received := make(chan []byte, 16)
	b.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(m webrtc.DataChannelMessage) {
			select {
			case received <- m.Data:
			default:
			}
		})
	})
	connect := func() {
		t.Helper()
		offer, err := a.CreateOffer(nil)
		if err != nil {
			t.Fatal(err)
		}
		ready := webrtc.GatheringCompletePromise(a)
		if err = a.SetLocalDescription(offer); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ready:
		case <-time.After(20 * time.Second):
			t.Fatal("offer gathering timeout")
		}
		if err = b.SetRemoteDescription(*a.LocalDescription()); err != nil {
			t.Fatal(err)
		}
		answer, err := b.CreateAnswer(nil)
		if err != nil {
			t.Fatal(err)
		}
		ready = webrtc.GatheringCompletePromise(b)
		if err = b.SetLocalDescription(answer); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ready:
		case <-time.After(20 * time.Second):
			t.Fatal("answer gathering timeout")
		}
		if err = a.SetRemoteDescription(*b.LocalDescription()); err != nil {
			t.Fatal(err)
		}
	}
	return a, received, connect
}

func next(t *testing.T, received <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-received:
		return b
	case <-time.After(20 * time.Second):
		t.Fatal("no payload")
		return nil
	}
}

func TestHeldLatestCrossesRealChannel(t *testing.T) {
	pc, received, connect := peerPair(t)
	var release atomic.Int32
	var push func(int)
	ch, err := Open(context.Background(), pc, Options[int]{Label: "latest", Policy: Policy{Reliable: true, BeforeOpen: HoldLatestBeforeOpen, Compress: true},
		Source: Pushed(func(f func(int)) (func() error, error) { push = f; return nil, nil }),
		Encode: func(n int, emit func([]byte)) error { emit([]byte{byte(n)}); return nil }, Release: func(int) { release.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	push(1)
	push(2)
	connect()
	reader, err := zlib.NewReader(bytes.NewReader(next(t, received)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, []byte{2}) {
		t.Fatalf("got %v", payload)
	}
	ch.Close()
	<-ch.Done()
	if release.Load() != 2 {
		t.Fatalf("released %d values", release.Load())
	}
}

func TestPulledWaitAndEncodeFailure(t *testing.T) {
	pc, received, connect := peerPair(t)
	values := make(chan int, 3)
	values <- 1
	values <- 2
	values <- 99
	var stopped, released atomic.Int32
	ch, err := Open(context.Background(), pc, Options[int]{Label: "ordered", Policy: Policy{Reliable: true, BeforeOpen: WaitBeforeOpen, StopOnError: true},
		Source: Pulled(func() (<-chan int, func() error, error) {
			return values, func() error { stopped.Add(1); return nil }, nil
		}),
		Encode: func(n int, emit func([]byte)) error {
			if n == 99 {
				return errors.New("bad frame")
			}
			emit([]byte{byte(n)})
			return nil
		}, Release: func(int) { released.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	connect()
	if got := next(t, received); !bytes.Equal(got, []byte{1}) {
		t.Fatalf("first: %v", got)
	}
	if got := next(t, received); !bytes.Equal(got, []byte{2}) {
		t.Fatalf("second: %v", got)
	}
	select {
	case <-ch.Done():
	case <-time.After(time.Second):
		t.Fatal("source did not stop")
	}
	if stopped.Load() != 1 || released.Load() != 3 {
		t.Fatalf("stopped=%d released=%d", stopped.Load(), released.Load())
	}
	if ch.DataChannel.ReadyState() != webrtc.DataChannelStateOpen {
		t.Fatal("encode failure closed the channel")
	}
}
