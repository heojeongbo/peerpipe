// A loopback-only demo: HTTP exchanges SDP; WebRTC transports the samples.
package main

import (
	"context"
	"encoding/json"
	"github.com/heojeongbo/peerpipe/go"
	"github.com/pion/webrtc/v4"
	"log"
	"net/http"
	"time"
)

func main() {
	http.Handle("/dist/", http.StripPrefix("/dist/", http.FileServer(http.Dir("../ts/dist"))))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "../ts/examples/telemetry/index.html")
	})
	http.HandleFunc("/offer", offer)
	log.Fatal(http.ListenAndServe("127.0.0.1:18765", nil))
}
func offer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var remote webrtc.SessionDescription
	if err := json.NewDecoder(r.Body).Decode(&remote); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Demo sessions have a hard lifetime as well as transport-driven cleanup.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	go func() { <-ctx.Done(); pc.Close() }()
	success := false
	defer func() {
		if !success {
			cancel()
		}
	}()
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		if s == webrtc.PeerConnectionStateFailed || s == webrtc.PeerConnectionStateClosed || s == webrtc.PeerConnectionStateDisconnected {
			cancel()
		}
	})
	// The remote application can create its own channel with its own protocol.
	// This demo opts into echo only for that protocol; other channels are ignored.
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		if dc.Protocol() != "peerpipe.echo" {
			return
		}
		var push func([]byte)
		_, err := peerpipe.Attach(ctx, dc, peerpipe.Options[[]byte]{
			Policy: peerpipe.Policy{BeforeOpen: peerpipe.HoldLatestBeforeOpen, StopOnError: true},
			Source: peerpipe.Pushed(func(f func([]byte)) (func() error, error) {
				push = f
				return nil, nil
			}),
			Encode: func(value []byte, emit func([]byte)) error { emit(value); return nil },
		})
		if err != nil {
			log.Printf("attach echo: %v", err)
			dc.Close()
			return
		}
		dc.OnMessage(func(message webrtc.DataChannelMessage) { push(message.Data) })
	})
	_, err = peerpipe.Open(ctx, pc, peerpipe.Options[int]{Label: "telemetry", Policy: peerpipe.Policy{Reliable: true, Compress: true, BeforeOpen: peerpipe.WaitBeforeOpen},
		Source: peerpipe.Pulled(func() (<-chan int, func() error, error) {
			values := make(chan int, 1)
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer close(values)
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for n := 0; ; {
					select {
					case <-stop:
						return
					case <-ticker.C:
						select {
						case values <- n:
							n++
						case <-stop:
							return
						}
					}
				}
			}()
			return values, func() error { close(stop); <-done; return nil }, nil
		}), Encode: func(n int, emit func([]byte)) error {
			b, err := json.Marshal(map[string]int{"sequence": n})
			if err == nil {
				emit(b)
			}
			return err
		}})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err = pc.SetRemoteDescription(remote); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(answer); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	select {
	case <-gathered:
	case <-r.Context().Done():
		return
	case <-ctx.Done():
		http.Error(w, "ICE timeout", 504)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err = json.NewEncoder(w).Encode(pc.LocalDescription()); err != nil {
		return
	}
	success = true
}
