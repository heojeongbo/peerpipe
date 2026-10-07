# @heojeongbo/peerpipe

Browser WebRTC peer and data-channel lifecycle, with no runtime dependencies.

```sh
npm install @heojeongbo/peerpipe
```

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
instead rejects with a `TimeoutError` DOMException on timeout. Timeout values
must be finite and between 0 and 2147483647 milliseconds; invalid values reject
with `RangeError` before negotiation starts. Closing the peer immediately rejects pending ICE
waiters. Remote-answer failure clears negotiation state in `finally`.

A newer channel with the same label replaces the lookup entry; a late close
from the older channel does not delete the replacement. Callbacks still identify
the actual channel. Close is idempotent. Payloads retain native browser types;
set `channel.binaryType = "arraybuffer"` in `onDataChannel` when appropriate.
Listeners use `addEventListener`, so application `onmessage` / `onclose` handlers
coexist with library callbacks. If both route messages to the same consumer,
choose one to avoid processing them twice. Owned listeners detach on channel close.

`onError(error, channel)` receives asynchronous native data-channel errors;
`channel` is provided by peerpipe. Failures from API methods throw/reject and
are handled by the caller, without a second `onError` notification. Connection
failures remain connection-state events. Failed transceiver setup closes the
partially constructed native peer before rethrowing the original error.

The caller serializes SDP negotiation and chooses glare/reconnect policy.
`isReadyForOffer` is a state hint, not a negotiation mutex.

## Logging

Supply `logger: { info, warn }` bound to the host's session or request context.
The default is silent. Creation, connection-state changes, channel open/close,
and peer shutdown are logged with structured state or label/ID details. Channel
errors, failed setup, and gathering timeouts are warnings. No payload or raw
SDP/ICE credentials are logged. There is no logger/exporter configuration owned
by this package; logger callbacks must return without throwing.


## Development

```sh
npm ci
npm run type:check
npm test
npm run build
```

No Go installation is required for this package or its unit tests.
See the [repository](https://github.com/heojeongbo/peerpipe) for the companion
Go pump, shared E2E, and [wire contract](https://github.com/heojeongbo/peerpipe/blob/main/docs/wire-contract.md).
