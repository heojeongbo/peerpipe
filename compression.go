package peerpipe

import (
	"bytes"
	"compress/zlib"
)

// Compress produces one zlib-wrapped payload, decoded in browsers with deflate.
func Compress(v []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	if _, err := w.Write(v); err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
