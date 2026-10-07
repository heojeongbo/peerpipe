package peerpipe

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/pion/webrtc/v4"
)

// BeforeOpen is what a data channel does with a value that arrives before it
// opens.
type BeforeOpen int

const (
	// DropBeforeOpen discards it. Right for a stream the next value replaces.
	DropBeforeOpen BeforeOpen = iota

	// HoldLatestBeforeOpen keeps the newest one and sends it on open. Right for
	// state published only on change, which may never be published again.
	HoldLatestBeforeOpen

	// WaitBeforeOpen takes nothing until the channel opens, leaving what arrives
	// meanwhile to the Source's own buffer. A Pushed Source cannot wait, so for
	// one this is DropBeforeOpen.
	WaitBeforeOpen
)

// Policy is how an agent-opened data channel behaves: everything each
// channel used to decide in a pump of its own. The application chooses a policy for each stream.
type Policy struct {
	// MaxBufferedAmount is the queued-byte threshold above which a new payload
	// is dropped, even on reliable channels. Zero uses DefaultMaxBufferedAmount.
	// The check is best-effort: one payload may cross the threshold.
	MaxBufferedAmount uint64

	// Reliable sends ordered and fully reliable. Otherwise the channel is
	// unordered with no retransmit, right for latest-wins telemetry.
	Reliable bool

	// Compress zlib-compresses each payload.
	Compress bool

	BeforeOpen BeforeOpen

	// StopOnError ends the pump, and with it the subscription, on the first
	// failed encode or send; the channel itself stays open. Otherwise the first
	// failure is logged and the value skipped.
	StopOnError bool
}

// Source feeds a pump and returns how to stop feeding it.
type Source[T any] func(p *pump[T]) (stop func() error, err error)

// Pushed is a Source that calls push on the goroutine that produced the value,
// which the send then runs on as well. push may still be called while stop
// runs; the pump drops those.
func Pushed[T any](listen func(push func(T)) (stop func() error, err error)) Source[T] {
	return func(p *pump[T]) (func() error, error) {
		return listen(p.push)
	}
}

// Pulled is a Source the channel's own goroutine reads. How many values the
// channel buffers, and which it loses when full, is the subscription's.
func Pulled[T any](subscribe func() (c <-chan T, stop func() error, err error)) Source[T] {
	return func(p *pump[T]) (func() error, error) {
		c, stop, err := subscribe()
		if err != nil {
			return nil, err
		}
		p.in = c
		return stop, nil
	}
}

// pump moves one Source's values into one data channel under a Policy.
type pump[T any] struct {
	ctx    context.Context
	log    *slog.Logger
	label  string
	policy Policy
	dc     *webrtc.DataChannel

	// encode turns a value into zero or more payloads, handing each to emit.
	encode func(v T, emit func([]byte)) error
	// release is called once for every value the pump is given, whatever
	// becomes of it.
	release func(T)

	in     <-chan T // non-nil for a Pulled Source
	opened chan struct{}
	closed chan struct{}
	halt   chan struct{} // closed when a Pushed delivery hits StopOnError

	// mu serializes deliveries with the channel opening, so a held value never
	// reaches the browser after a newer one.
	mu      sync.Mutex
	isOpen  bool
	done    bool
	held    T
	hasHeld bool
	dropped int
	halted  sync.Once
	sent    sync.Once
	// The first failure of each kind is logged; one kind must not hide another.
	failedEncode, failedCompress, failedSend sync.Once
}

// Options describes one outbound channel. Encode runs synchronously and must not
// retain emit. Release runs exactly once for each value accepted by the pump,
// including discarded values. Source owns unread values left in its own queue.
type Options[T any] struct {
	Label   string
	Policy  Policy
	Source  Source[T]
	Encode  func(T, func([]byte)) error
	Release func(T)
	Logger  *slog.Logger
}

// Channel owns one data channel and its subscription, never the peer connection.
// Done closes after the source stop callback returns. Close is nonblocking;
// callbacks must return for cleanup to finish.
type Channel struct {
	DataChannel *webrtc.DataChannel
	cancel      context.CancelFunc
	done        chan struct{}
}

func (c *Channel) Close() error          { c.cancel(); return c.DataChannel.Close() }
func (c *Channel) Done() <-chan struct{} { return c.done }

// Open subscribes before creating a channel, preserving BeforeOpen policy even
// for sources that push synchronously during registration. On source failure,
// the source must clean up partial initialization. A successful source may return
// a nil stop callback. Cancel ctx to stop the pump; Close also closes the channel.
// StopOnError stops the source but leaves the channel open for its owner's cleanup.
func Open[T any](ctx context.Context, conn *webrtc.PeerConnection, opts Options[T]) (*Channel, error) {
	if ctx == nil || conn == nil || opts.Source == nil || opts.Encode == nil {
		return nil, fmt.Errorf("peerpipe: context, connection, source and encoder are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Policy.BeforeOpen < DropBeforeOpen || opts.Policy.BeforeOpen > WaitBeforeOpen {
		return nil, fmt.Errorf("peerpipe: invalid before-open policy")
	}
	release := opts.Release
	if release == nil {
		release = func(T) {}
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	child, cancel := context.WithCancel(ctx)
	p := &pump[T]{ctx: child, log: logger, label: opts.Label, policy: opts.Policy,
		encode: opts.Encode, release: release, opened: make(chan struct{}),
		closed: make(chan struct{}), halt: make(chan struct{})}
	stop, err := opts.Source(p)
	if err != nil {
		p.finish(func() error { return nil })
		cancel()
		return nil, err
	}
	if stop == nil {
		stop = func() error { return nil }
	}
	ordered, retransmits := false, uint16(0)
	init := &webrtc.DataChannelInit{Ordered: &ordered, MaxRetransmits: &retransmits}
	if opts.Policy.Reliable {
		ordered = true
		init.MaxRetransmits = nil
	}
	dc, err := conn.CreateDataChannel(opts.Label, init)
	if err != nil {
		p.finish(stop)
		cancel()
		return nil, fmt.Errorf("create data channel: %w", err)
	}
	p.dc = dc
	ch := &Channel{DataChannel: dc, cancel: cancel, done: make(chan struct{})}
	dc.OnOpen(p.onOpen)
	dc.OnClose(func() { close(p.closed) })
	go func() { defer close(ch.done); defer cancel(); p.run(stop) }()
	return ch, nil
}

func (p *pump[T]) onOpen() {
	p.log.Info("data channel open", slog.String("label", p.label))
	if p.in != nil {
		close(p.opened)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.isOpen = true
	if !p.flushOnOpen() {
		p.stopPushed()
	}
}

// push takes one value from a Pushed Source.
func (p *pump[T]) push(v T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.done:
		p.release(v)
	case !p.isOpen:
		p.BeforeOpen(v)
	case !p.deliver(v):
		p.stopPushed()
	}
}

// stopPushed ends a Pushed pump from a delivery. The Source is stopped by run,
// not here: a Pushed Source must not be stopped from inside its own push.
// p.mu must be held.
func (p *pump[T]) stopPushed() {
	p.done = true
	p.halted.Do(func() { close(p.halt) })
}

// run waits for the channel to end, and for a Pulled Source reads it until
// then.
func (p *pump[T]) run(stop func() error) {
	defer p.finish(stop)
	ctx := p.ctx

	if p.in == nil {
		select {
		case <-ctx.Done():
		case <-p.closed:
		case <-p.halt:
		}
		return
	}

	for open := false; !open; {
		var in <-chan T // nil blocks forever: WaitBeforeOpen reads nothing
		if p.policy.BeforeOpen != WaitBeforeOpen {
			in = p.in
		}
		select {
		case <-ctx.Done():
			return
		case <-p.closed:
			return
		case <-p.opened:
			open = true
		case v, ok := <-in:
			if !ok {
				// The Source ended. Reading on would spin on the zero value.
				return
			}
			p.mu.Lock()
			p.BeforeOpen(v)
			p.mu.Unlock()
		}
	}

	p.mu.Lock()
	ok := p.flushOnOpen()
	p.mu.Unlock()
	if !ok {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.closed:
			return
		case v, open := <-p.in:
			if !open {
				// The Source ended. Reading on would spin on the zero value.
				return
			}
			p.mu.Lock()
			ok := p.deliver(v)
			p.mu.Unlock()
			if !ok {
				return
			}
		}
	}
}

// BeforeOpen applies the policy to a value that arrived before open. p.mu must
// be held.
func (p *pump[T]) BeforeOpen(v T) {
	if p.policy.BeforeOpen != HoldLatestBeforeOpen {
		p.dropped++
		p.release(v)
		return
	}
	if p.hasHeld {
		p.release(p.held)
	}
	p.held, p.hasHeld = v, true
}

// flushOnOpen reports what was dropped before open and sends what was held.
// It returns false when that send ends the pump. p.mu must be held.
func (p *pump[T]) flushOnOpen() bool {
	if p.dropped > 0 {
		p.log.Info("dropped before channel open",
			slog.String("label", p.label), slog.Int("count", p.dropped))
	}
	if !p.hasHeld {
		return true
	}
	v := p.held
	var zero T
	p.held, p.hasHeld = zero, false
	return p.deliver(v)
}

// deliver encodes v and sends every payload it yields. It returns false when a
// failure ends the pump under StopOnError. p.mu must be held.
func (p *pump[T]) deliver(v T) bool {
	defer p.release(v)

	var compressErr, sendErr error
	encodeErr := p.encode(v, func(b []byte) {
		out := b
		if p.policy.Compress {
			deflated, err := Compress(b)
			if err != nil {
				compressErr = err
				return
			}
			out = deflated
		}
		if err := TrySend(p.dc, out, p.policy.BufferLimit()); err != nil {
			sendErr = err
			return
		}
		p.sent.Do(func() {
			p.log.Info("data channel first send ok",
				slog.String("label", p.label), slog.Int("bytes", len(out)))
		})
	})
	for _, f := range []struct {
		what string
		err  error
		once *sync.Once
	}{
		{"encode", encodeErr, &p.failedEncode},
		{"compress", compressErr, &p.failedCompress},
		{"send", sendErr, &p.failedSend},
	} {
		if f.err == nil {
			continue
		}
		if p.policy.StopOnError {
			p.log.Warn("data channel pump stopped",
				slog.String("label", p.label), slog.String("failed", f.what), slog.String("err", f.err.Error()))
			return false
		}
		f.once.Do(func() {
			p.log.Warn("data channel "+f.what+" failed; skipping",
				slog.String("label", p.label), slog.String("err", f.err.Error()))
		})
	}
	return true
}

// finish stops the Source and lets go of what the pump still holds.
func (p *pump[T]) finish(stop func() error) {
	p.mu.Lock()
	p.done = true
	if p.hasHeld {
		p.release(p.held)
		var zero T
		p.held, p.hasHeld = zero, false
	}
	p.mu.Unlock()

	if err := stop(); err != nil {
		p.log.Warn("close subscription",
			slog.String("label", p.label), slog.String("err", err.Error()))
	}
}
