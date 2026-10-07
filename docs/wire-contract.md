# Wire contract

There is no proprietary envelope or control protocol. Both sides agree on label,
codec, and compression out of band. Each Go `emit` corresponds to one binary data
channel message, with the same boundary at the browser. `Compress: true` uses a
zlib wrapper, decoded by `new DecompressionStream("deflate")`. Compression is
not auto-detected. Serialize asynchronous decoding if message order matters.
The application limits payload sizes and chooses codecs for untrusted input.
