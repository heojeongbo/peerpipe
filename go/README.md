# peerpipe for Go

Pion data-channel pumps. The package name is `peerpipe`.

```sh
go get github.com/heojeongbo/peerpipe/go@v0.4.0
```

```go
import "github.com/heojeongbo/peerpipe/go"
```

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
`OnDrop func(bytes int)` observes each congestion-dropped encoded payload, after
compression. It runs synchronously with Encode; do not reenter the source push
callback or wait for `Done` from it. Before-open values are separate and are
reported in lifecycle logs.

## Native channel configuration and existing channels

`Options.DataChannelInit` passes Pion's full configuration to `CreateDataChannel`:
ordered/unordered delivery, partial reliability, protocol, negotiated IDs, and
externally negotiated channels. When present it replaces the `Policy.Reliable`
preset entirely; `&webrtc.DataChannelInit{}` selects Pion's native defaults.
Both peers must agree on protocol, negotiated IDs, and payload encoding.

```go
ordered, lifetime, protocol := false, uint16(250), "example/updates"
opts.DataChannelInit = &webrtc.DataChannelInit{
    Ordered: &ordered, MaxPacketLifeTime: &lifetime, Protocol: &protocol,
}
channel, err := peerpipe.Open(ctx, pc, opts)
```

For an already-created channel, including one received through `pc.OnDataChannel`,
use `peerpipe.Attach(ctx, dc, opts)`. Omit `DataChannelInit`; `Label` may be empty
or match the native label, and `Reliable` has no effect. On success the returned
Channel owns closing the data channel; on failure it remains caller-owned and
its event handlers are untouched. Attaching a closed/closing channel fails.
Only one pump may own a channel at a time.

Pion has single handler slots. Attach requires an unused `OnClose` slot and,
when the channel is still connecting, an unused `OnOpen` slot. Do not replace
these handlers while attached. Existing `OnOpen` handlers on already-open channels
are preserved. `OnMessage`, `OnError`, and `OnBufferedAmountLow` are available
to the application; detached Pion channels are outside this API's contract.
Use `channel.Opened()` to observe readiness without replacing a handler; select
with `channel.Done()` too, since cancellation may happen before opening.

## Custom sending and error handling

`Options.Send` replaces the default congestion policy. Its `SendFunc` receives
the pump context, native sender, encoded bytes (after optional compression), and
resolved queue limit. Return `true, nil` for acceptance, `false, nil` for a drop,
or `false, err` for failure. Drops reach `OnDrop`, failures reach `OnError`, and
`StopOnError` still applies. Logs distinguish custom policy drops from the default
congestion rule. A custom sender controls queue admission and retry behavior;
peerpipe adds no hidden queue or goroutine per payload.

For example, a caller can wait for queue capacity with a bounded deadline:

```go
opts.Send = func(ctx context.Context, dc peerpipe.BufferedSender, payload []byte, limit uint64) (bool, error) {
    wait, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
    defer cancel()
    tick := time.NewTicker(5*time.Millisecond)
    defer tick.Stop()
    for {
        if err := wait.Err(); err != nil { return false, err }
        sent, err := peerpipe.TrySend(dc, payload, limit)
        if sent || err != nil { return sent, err }
        select {
        case <-wait.Done(): return false, wait.Err()
        case <-tick.C:
        }
    }
}
```

Waiting blocks the producer for `Pushed`, or the reader for `Pulled`; choose
source buffering accordingly. Native channel closure, parent cancellation, or
`Channel.Close` cancels the sender context. Blocking callbacks must honor it.
Do not retain bytes beyond the call, reenter push, or wait for `Done` from Send,
OnDrop, or OnError. Separate pumps have separate synchronization; a shared custom
sender must synchronize its own state. SendText or another native encoding can
be selected by a custom sender that captures the channel passed to Attach; keep `Compress`
off for plain text and agree with the receiver on the wire format.

`OnError(operation, err)` observes encode, compress, send, and cleanup errors.
Encode/compress/send errors are reported at most once per operation per source
value, including repeated failing values; logs remain bounded. Cleanup errors
can also be reported during failed initialization. Initialization errors themselves
are returned to Open/Attach's caller. Native asynchronous errors remain on
`DataChannel.OnError`. The library does not turn a local send into a delivery ACK.

## Sending directly

```go
sent, err := peerpipe.TrySend(channel, payload, queueLimit)
switch {
case err != nil:
    // The native Send failed: report or stop according to application policy.
case !sent:
    // Congestion discarded the payload: count it or retry from the owner.
default:
    // The native sender accepted it; this is not a remote acknowledgment.
}
```

## Logging

Pass the logger belonging to the operation and the same context to `Open`.
The library uses `InfoContext` / `WarnContext`, preserving values such as trace
correlation even during cancellation and cleanup. It adds `component=peerpipe`
and `label`; the application supplies session or request identity via its logger.
There is no global logger or automatic stdout output.

For an application already using go-app's OTx logging, the integration is:

```go
// log is github.com/lesomnus/otx/log, configured by the host application.
channel, err := peerpipe.Open(ctx, pc, peerpipe.Options[[]byte]{
    Label: "updates",
    Source: peerpipe.Pushed(subscribe),
    Encode: func(v []byte, emit func([]byte)) error { emit(v); return nil },
    Logger: log.From(ctx),
})
// Handle err. The owner closes channel and waits for channel.Done().
```

Created/open/closed and first accepted send are informational. Initialization,
encoding, sending, and cleanup failures are warnings with an `error` field;
encode/send records also identify the `operation`. Only the first congestion
warning is emitted per pump, and shutdown records the reason, accepted payload
count, congestion drop count, and before-open drop count. Replaced held values
are released but are not counted as congestion drops. Payload contents are never
logged by the library; application-provided labels and error messages remain the
application's responsibility. Returning errors still belong to the caller to handle.

## Development

From this directory, run `go test -race ./...` and `go vet ./...`.
No Node.js installation is required for the Go package or its tests.
See the [repository README](../README.md) for the browser example and shared E2E.
