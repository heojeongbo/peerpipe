# peerpipe

WebRTC data pipelines for Go and the browser. One project, two packages:

- Go: `github.com/heojeongbo/peerpipe` — Pion data-channel pumps.
- npm: `@heojeongbo/peerpipe` — browser peer and channel lifecycle.

Peerpipe does not own authentication, signaling transport, reconnect policy,
robot commands, or application payload schemas. Exchange SDP and ICE through
an authenticated transport of your choice. A failed connection is replaced by
creating a new peer; there is no hidden reconnect loop.

## Install

```sh
go get github.com/heojeongbo/peerpipe@v0.1.0
npm install @heojeongbo/peerpipe
```

## Go

```go
channel, err := peerpipe.Open(ctx, pc, peerpipe.Options[[]byte]{
    Label: "telemetry",
    Policy: peerpipe.Policy{
        Reliable: true,
        BeforeOpen: peerpipe.HoldLatestBeforeOpen,
    },
    Source: peerpipe.Pushed(subscribe),
    Encode: func(value []byte, emit func([]byte)) error {
        emit(value)
        return nil
    },
})
// subscribe has type func(func([]byte)) (stop func() error, err error).
// Handle err. Close the owned channel when finished, then await channel.Done().
```

The caller owns `pc`. Each returned Channel owns its subscription and data
channel. `Close` cancels the pump and closes that channel, never the peer.
Cancellation of `ctx` stops the source; the owner still closes its channel or
peer. `Done()` closes after source cleanup. Do not replace the pump-owned
`DataChannel.OnOpen` or `DataChannel.OnClose` handlers. Cleanup is asynchronous, so a source
stop callback may wait for its publishing goroutine without deadlocking inside
that goroutine's push callback.

`Pushed` executes encoding and sending synchronously on the producer's goroutine.
`Pulled` reads from the source channel on the pump goroutine. Encode, release,
and stop callbacks must return and must not panic. Encode must not retain emit
or mutate emitted bytes before it returns. Release is called once for each
accepted value, including replaced, dropped, and failed values. The source
owns values remaining in its unread queue and partial initialization on failure.

| Policy | Behavior |
| --- | --- |
| `Reliable: false` | Unordered, zero retransmissions |
| `Reliable: true` | Ordered, reliable SCTP delivery |
| `DropBeforeOpen` | Discard samples until open (default) |
| `HoldLatestBeforeOpen` | Retain only the newest sample, release replaced values |
| `WaitBeforeOpen` | Do not read a pulled source before open; pushed sources drop |
| `Compress` | Independently zlib-compress each emitted payload |
| `StopOnError` | Stop the source on encode/compress/send failure; channel remains open |
| `MaxBufferedAmount` | Drop new payloads above this queued-byte threshold; zero means 1 MiB |

**Reliability does not disable congestion dropping.** The queue limit is a
best-effort threshold, not a strict allocation bound: one payload may cross it.
A congestion drop is not an error. This is a telemetry policy, not guaranteed
command delivery. Applications needing acknowledgments implement them above it.
A nil logger is silent; supply `*slog.Logger` to observe lifecycle and errors.

## Browser

```ts
import { WebRTC } from "@heojeongbo/peerpipe";

const peer = WebRTC.create(
  { iceServers: [{ urls: "stun:your-stun.example:3478" }] },
  {
    onIceCandidate: candidate => signaling.sendCandidate(candidate.toJSON()),
    onDataChannelMessage: (payload, channel) => consume(channel.label, payload),
    onConnectionStateChange: state => renderStatus(state.connectionState),
  },
);
peer.createDataChannel("control");
const offer = await peer.createOffer();
await signaling.sendOffer(offer);
// On answer: await peer.setRemoteDescription(answer).
// On remote candidate: await peer.addIceCandidate(candidate).
// On completion: peer.close().
```

`signaling`, `consume`, and `renderStatus` above belong to your application.
No public STUN server, media transceiver, framework, or logger is installed by
default. RTCConfiguration is forwarded to the native peer. Optional
`transceivers` configures media; optional `logger` supplies info/warn callbacks.

For non-trickle signaling, `createOfferWithGatheredIce` and
`createAnswerWithGatheredIce` wait up to two seconds (overridable) and return the
current local description even on gathering timeout (closing still rejects). `waitForIceGatheringComplete`
instead rejects on timeout. Closing the peer immediately rejects pending ICE
waiters. Remote-answer failure clears negotiation state in `finally`.

A newer channel with the same label replaces the lookup entry; a late close
from the older channel does not delete the replacement. Callbacks still identify
the actual channel. Close is idempotent. Payloads retain native browser types;
set `channel.binaryType = "arraybuffer"` in `onDataChannel` when appropriate.

## Wire contract

There is no proprietary envelope or control protocol. Both sides agree on label,
codec, and compression out of band. Each Go `emit` corresponds to one binary data
channel message, with the same boundary at the browser. `Compress: true` uses a
zlib wrapper, decoded by `new DecompressionStream("deflate")`. Compression is
not auto-detected. Serialize asynchronous decoding if message order matters.
The application limits payload sizes and chooses codecs for untrusted input.

## Runnable example and checks

The loopback-only example exchanges SDP over HTTP, sends compressed ordered
JSON samples through the Go pump, and receives them using the npm browser code.
It is a local demo, not an authenticated production signaling server.

```sh
npm ci
npm run build
go run ./examples/telemetry
# Open http://127.0.0.1:18765

go test -race ./...
npm test
npx playwright install --with-deps chromium
npm run e2e
```

The browser E2E exercises real Pion/Chromium negotiation, zlib decoding, ordered
samples, close, and creation of a replacement peer. Unit tests cover pending
ICE cancellation, rejected answers, late channel close, queue drops, and source
cleanup. Go and npm use the same version; a `vX.Y.Z` tag identifies both sources.

## Release

Run all checks above and inspect `npm pack --dry-run` before tagging. Publish the
Go module by pushing the version tag. Run `npm publish --access public` from a
clean checkout of that tag with npm authentication configured outside the repo.
The `prepublishOnly` hook checks types, browser unit tests, and builds declarations.
