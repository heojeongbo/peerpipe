package peerpipe

import "context"

// SendFunc chooses when to send or discard an encoded payload. It returns the
// same outcomes as TrySend. Calls are serialized per pump, after compression.
// A blocking implementation must stop when ctx is canceled. Do not retain the
// payload after returning, reenter push, or wait for Channel.Done from here.
// limit is already resolved (nonzero); a custom policy may choose not to use it.
type SendFunc func(ctx context.Context, dc BufferedSender, payload []byte, limit uint64) (sent bool, err error)

// DefaultMaxBufferedAmount bounds queued telemetry to approximately one MiB.
const DefaultMaxBufferedAmount uint64 = 1 << 20

// BufferedSender is implemented by a Pion data channel.
type BufferedSender interface {
	BufferedAmount() uint64
	Send([]byte) error
}

// BufferLimit resolves the default queue threshold.
func (p Policy) BufferLimit() uint64 {
	if p.MaxBufferedAmount == 0 {
		return DefaultMaxBufferedAmount
	}
	return p.MaxBufferedAmount
}

// TrySend returns (true, nil) when Send accepts a payload, (false, nil) when
// congestion drops it, or (false, err) when Send fails. Acceptance does not mean
// remote delivery. Reliable SCTP does not override the congestion policy.
// Callers serialize sends when sharing the same sender. Zero uses the default.
func TrySend(dc BufferedSender, payload []byte, limit uint64) (sent bool, err error) {
	if limit == 0 {
		limit = DefaultMaxBufferedAmount
	}
	if dc.BufferedAmount() > limit {
		return false, nil
	}
	if err := dc.Send(payload); err != nil {
		return false, err
	}
	return true, nil
}
