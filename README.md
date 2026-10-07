# peerpipe

WebRTC data pipelines for Go and the browser. One repository, two independent
packages with separate toolchains and release tags.

| Directory | Package | Responsibility |
| --- | --- | --- |
| [`go/`](go) | `github.com/heojeongbo/peerpipe/go` | Pion data-channel pumps |
| [`ts/`](ts) | `@heojeongbo/peerpipe` | Browser peer and channel lifecycle |
| [`e2e/`](e2e) | Private test harness | Real Go/Chromium interoperability |
| [`docs/`](docs/wire-contract.md) | Shared contract | Payload boundaries and compression |

Peerpipe does not own authentication, signaling transport, reconnect policy,
robot commands, or application payload schemas. Exchange SDP and ICE through
an authenticated transport of your choice. A failed connection is replaced by
creating a new peer; there is no hidden reconnect loop.

## Install

```sh
go get github.com/heojeongbo/peerpipe/go@v0.2.0
npm install @heojeongbo/peerpipe
```

See the [Go API](go/README.md), [browser API](ts/README.md), and
[wire contract](docs/wire-contract.md).

## Development

Run each package's checks independently from the repository root:

```sh
(cd go && go test -race ./... && go vet ./...)
npm --prefix ts ci
npm --prefix ts run type:check
npm --prefix ts test
npm --prefix ts run build
```

Go unit tests need no Node.js installation. TypeScript unit tests need no Go
installation. CI runs these as separate jobs, followed by a shared E2E job.

## Runnable example and E2E

The loopback-only example exchanges SDP over HTTP, sends compressed ordered
JSON samples through the Go pump, and receives them using the browser package.
It is a local demo, not an authenticated production signaling server.

```sh
npm --prefix ts ci
npm --prefix ts run build
(cd go && go run ./examples/telemetry)
# Open http://127.0.0.1:18765
```

The Go server in `go/examples/telemetry` serves the browser example from
`ts/examples/telemetry` and the compiled `ts/dist` package. Start it from `go/`
so those relative paths resolve. Stop it before running the shared E2E:

```sh
npm --prefix e2e ci
(cd e2e && npx playwright install --with-deps chromium)
npm --prefix e2e test
```

The browser E2E exercises real Pion/Chromium negotiation, zlib decoding, ordered
samples, close, and creation of a replacement peer. Unit tests cover pending
ICE cancellation, rejected answers, late channel close, queue drops, and source
cleanup.

## Releases and migration

Go uses tags such as `go/v0.2.0`, because its module lives in `go/`.
TypeScript uses `ts/v0.2.0` tags and the version in `ts/package.json`.
The packages can advance independently; changes to the shared wire contract
must pass the combined E2E. See [Go's subdirectory tagging rules](https://go.dev/doc/modules/managing-source).

The original `v0.1.0` tag remains available. Migrating Go consumers must change
the import and module requirement from `github.com/heojeongbo/peerpipe` to
`github.com/heojeongbo/peerpipe/go`. The Go package identifier stays `peerpipe`;
the npm package name and browser API are unchanged.

Run the package checks and shared E2E before releasing. For npm, build a tarball
and test that exact artifact in consumers before publishing it:

```sh
npm --prefix ts run prepublishOnly
(cd ts && npm pack --pack-destination ..)
# Install the generated tarball in consumers and run their integration tests.
npm publish ./heojeongbo-peerpipe-0.2.0.tgz --access public
```

`prepack` builds JavaScript and declarations. Publishing an existing tarball
uses those packed files, so the type and unit checks above must already pass.
Keep npm authentication outside the repository. Push the corresponding package
tag from the validated commit; Go consumers resolve the module via `go/vX.Y.Z`.
