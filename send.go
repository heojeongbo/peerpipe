package peerpipe

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

// TrySend discards a payload when the queue is already over limit. A discard is
// not an error. Reliable SCTP delivery does not override this application policy.
// Callers serialize sends when sharing the same sender. Zero uses the default.
func TrySend(dc BufferedSender, payload []byte, limit uint64) error {
	if limit == 0 {
		limit = DefaultMaxBufferedAmount
	}
	if dc.BufferedAmount() > limit {
		return nil
	}
	return dc.Send(payload)
}
