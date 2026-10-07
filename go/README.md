# peerpipe for Go

Pion data-channel pumps. The package name is `peerpipe`.

```sh
go get github.com/heojeongbo/peerpipe/go@v0.2.0
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


## Development

From this directory, run `go test -race ./...` and `go vet ./...`.
No Node.js installation is required for the Go package or its tests.
See the [repository README](../README.md) for the browser example and shared E2E.
