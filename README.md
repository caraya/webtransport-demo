# Generic WebTransport endpoint

A Go HTTP/3 server using `webtransport-go`. The transport layer handles raw
bytes, not text, JSON, MIME types, codecs, or media containers.

## Run the echo demo

### Go command-line client: send a string

No browser, Python, page server, or certificate hash is needed for this client.
Run these commands from the project root. If the development PEM files already
exist, skip certificate generation.

```sh
go run ./cmd/certgen
```

In terminal A, start the server without an explicit browser-origin restriction:

```sh
go run ./cmd/server -addr localhost:4433
```

In terminal B, send a string:

```sh
go run ./cmd/client -message "hello"
```

Expected standard output:

```text
hello
```

The client trusts `certs/localhost.pem` only inside its own TLS configuration;
hostname and expiration verification remain enabled. It never installs a CA or
changes OS/application trust settings. It sends UTF-8 bytes over a bidirectional
stream, reads concurrently, checks the full echo, and closes the session.

If your server is already running with an explicit `-origin`, send the exact
same value from the client rather than starting another server on the same port:

```sh
# Match the -origin value used by your running server.
go run ./cmd/client -message "hello" -origin https://localhost:8443
```

For replacement development certificates, use `-cert certs/next.pem` on the
client. For a publicly trusted production endpoint, use system roots without
changing them:

```sh
go run ./cmd/client -url https://transport.example.com:4433/echo \
  -cert="" -message "hello"
```

`-timeout` defaults to `10s` and bounds connection establishment and stream I/O.
Use `go run ./cmd/client -h` for all flags.

### Browser walkthrough

The browser client uses Go 1.26 or newer and a recent Chrome or Edge with
`serverCertificateHashes` support. No Python, Node.js, frontend build, browser
console commands, or OS trust-store changes are needed to run it.
WebTransport uses **UDP**, not TCP; allow loopback UDP on port 4433.

Two servers have different roles:

| Address | Role |
| --- | --- |
| `http://localhost:8443` | Browser test UI, served by `cmd/browser` over TCP |
| `https://127.0.0.1:4433/echo` | Go WebTransport endpoint called by JavaScript |

Do not navigate directly to the echo URL. The address bar makes an ordinary
HTTPS request, not the extended CONNECT request needed for WebTransport.

#### 1. Generate or reuse development certificates

Generate local development certificates without installing a CA or changing
OS/application trust stores. From the project root:

```sh
go run ./cmd/certgen
```

The generator creates `certs/localhost.pem` and `certs/localhost-key.pem`, which
are the server's default paths. It creates a self-signed ECDSA P-256 leaf
certificate valid for seven days and covering `localhost`, `127.0.0.1`, and
`::1`. The private key is created with owner-only permissions. Files in `certs/`
are excluded from version control.

The page server reads the **public certificate only** and fills in its SHA-256
pin automatically. It never reads or serves the private key.

If the files already exist and have not expired, skip generation. To inspect
their lifetime:

```sh
openssl x509 -in certs/localhost.pem -noout -dates
```

Existing output files are never overwritten. Before expiration, generate a
replacement pair at new paths:

```sh
go run ./cmd/certgen -cert certs/next.pem -key certs/next-key.pem
```

Stop the old server before starting its replacement on the same port, and
start it with `-cert certs/next.pem -key certs/next-key.pem`. Restart the page
server with `-cert certs/next.pem` too, then reload the page. Neither server
generates, renews, or hot-reloads certificates.

#### 2. Start the echo server in terminal A

From the project root, run:

```sh
go run ./cmd/server -addr 127.0.0.1:4433 -origin http://localhost:8443
```

Leave this running. Look for the `WebTransport echo listening` log message.
Notice that the allowed page origin uses **http**, while the WebTransport
endpoint itself still uses **https** and TLS.

#### 3. Start the browser page server in terminal B

From the project root:

```sh
go run ./cmd/browser
```

Leave this running too. It serves embedded client assets and `/config.json`,
not the project directory or certificate files. The defaults are
`-addr localhost:8443`, `-url https://127.0.0.1:4433/echo`, and
`-cert certs/localhost.pem`. The endpoint remains a separate process; this page
server does not proxy WebTransport traffic.

Browsers treat loopback pages such as `http://localhost:8443` as potentially
trustworthy secure contexts. This avoids needing another HTTPS certificate for
the demo page and does not change OS trust settings.

#### 4. Open the page and run the test

1. Open **`http://localhost:8443/`** in Chrome or Edge.
2. Confirm the endpoint URL and automatically loaded certificate pin.
3. Keep **All three modes** selected and the default hex payload `00 ff 80 2a`,
   then select **Run echo test**.
4. Expect **PASS** for bidirectional stream, unidirectional stream, and datagram,
   with four matching bytes and the received hex preview.
5. To test a string, select **UTF-8 text**, replace the payload with `hello`,
   and run again. Select an individual transport to isolate failures.

Keep `localhost` in the page address: `http://127.0.0.1:8443` is a different
origin and would be rejected by the server configuration above.
The endpoint URL deliberately uses `127.0.0.1`: browsers can resolve `localhost`
to IPv6 `::1` while the Go endpoint listens only on IPv4. The development
certificate covers both names and addresses.

Each mode opens a fresh WebTransport session directly from the browser to the
Go endpoint. Reads run concurrently with writes. Streams are checked through
EOF, regardless of chunk boundaries; unidirectional responses arrive on a
separate server stream. Datagrams are checked as individual messages.
Responses must match the complete sent bytes, not just decoded text.

The client limits payloads to 64 KiB, checks the negotiated datagram size, and
shows a hex preview of up to 256 received bytes. Tests have a ten-second
deadline covering connection setup and I/O; **Cancel test** closes the current
session and stops the remaining tests. Sessions also close on success or
failure. Elapsed times include setup and are not throughput benchmarks.
Datagrams are unreliable: a timeout can mean packet loss, not lack of browser
support. There is no HTTP or WebSocket fallback.

The pin must come from the local generator output or another authenticated
source, never blindly from the untrusted endpoint. It trusts this certificate
only for this WebTransport connection, not ordinary HTTPS or other applications.

When finished, press **Control+C** in both terminals.

#### Troubleshoot the browser connection

| Symptom | Check |
| --- | --- |
| Missing certificate file at startup | Complete step 1, or supply existing files with `-cert` and `-key` |
| `WebTransport` is unavailable | Use a supported browser and confirm `window.isSecureContext` is true |
| Invalid pin / hash-length error | Use the automatically loaded pin, or paste only a trusted base64 SHA-256 hash |
| Upgrade rejected / origin error in server logs | Match `location.origin` exactly to `-origin`, including scheme and port |
| `transport.ready` rejects | Check certificate expiration, the current certificate's pin, browser hash support, and that the UDP server is running |
| Connection times out | Check loopback UDP permissions, firewall/VPN restrictions, and both terminal logs |
| `ERR_CONNECTION_REFUSED` for `localhost` | Use `https://127.0.0.1:4433/echo` with a server listening on `127.0.0.1:4433` |
| `NO_APPLICATION_PROTOCOL` / QUIC handshake failure | Restart the updated endpoint; its TLS configuration must advertise HTTP/3 `h3` via ALPN |
| Test page cannot load | Confirm `cmd/browser` is running on TCP port 8443; use HTTP for the local page |
| Datagram too large | Choose a smaller payload or test a stream instead |

Live interoperability was verified in the VS Code integrated browser
(Chromium 150) with both servers running outside the command sandbox. All three
modes returned the exact `00 ff 80 2a` payload without console errors.
Browser transport unit tests additionally use a simulated peer with real Web
Streams. Shell-based UDP integration tests require permission to create local
listeners; visual/accessibility audits remain unverified.

### Use an existing HTTPS page or production certificate

For production, supply a public CA certificate chain and its matching private
key using `-cert` and `-key`; keep private keys outside `certs/` out of version
control too.

The local endpoint is `https://localhost:4433/echo`. With a public CA certificate,
connect using the hostname covered by that certificate instead of `localhost`.
See [architecture and certificate choices](./architecture.md#certificates-for-production-and-local-development)
for production certificates, client-scoped Go trust, and the browser
certificate-pinning workflow. No system root CA installation is required.

The server uses the library's same-host-and-port origin policy by default.
For a browser demo hosted on another origin, explicitly allow that origin:

```sh
go run ./cmd/server -addr localhost:4433 \
  -cert /path/to/cert.pem -key /path/to/key.pem \
  -origin https://localhost:8443
```

Use the exact origin of your existing HTTPS demo page, without a trailing slash.
The server does not create or host that page or provide a TCP HTTPS listener.
For a public CA certificate and covered hostname, start the page server without
a pin:

```sh
go run ./cmd/browser -url https://transport.example.com:4433/echo -cert=""
```

An empty pin uses the browser's normal certificate verification. For a self-signed
development certificate, use the matching local public PEM with `-cert`.
If serving the UI beyond loopback, use HTTPS and an explicit endpoint origin
policy; plain HTTP on a non-loopback host is not a secure context.

## Reuse the endpoint

`endpoint.Handler` implements `http.Handler`. It upgrades the request, calls a
`SessionHandler`, logs errors, and closes the session when the handler returns.
The supplied context follows the **session**, not the HTTP request lifetime.
Configure handlers and the server before serving requests.

Register additional tasks on the same HTTP mux and WebTransport server:

```go
// With your existing mux and *webtransport.Server:
mux.Handle("/echo", &endpoint.Handler{
    Server: server,
    HandleSession: endpoint.Echo,
})
mux.Handle("/media", &endpoint.Handler{
    Server: server,
    HandleSession: handleMedia,
})
```

Each task implements:

```go
func handleMedia(ctx context.Context, session *webtransport.Session) error
```

That handler owns stream acceptance, stream processing, outgoing streams,
datagrams, and its goroutines until it returns. Choose framing, metadata,
content type, codec, and any task dispatch protocol inside the task handler;
the endpoint does not impose an envelope. Do not run two independent stream
acceptors for the same stream type unless they intentionally share work.
For custom cancellation, cancel/reset active streams or close the session:
canceling an accept context alone does not unblock active stream reads/writes.

## Echo behavior

| Input | Echo response |
| --- | --- |
| Bidirectional stream | Same bytes on that stream; response FIN after input FIN |
| Unidirectional stream | Same bytes on a newly opened server unidirectional stream |
| Datagram | Same datagram payload |

Streams run concurrently, with at most 32 active echo streams **per type per
session** and bounded copy buffers. Large media streams are copied incrementally,
not accumulated in memory. Beyond this limit, stream acceptance waits for a slot.
Multiple unidirectional responses can arrive in a different order from their
inputs; add application-level IDs if correlation is needed.

Datagrams are unreliable and unordered and have a negotiated maximum size.
Use streams for reliable transfers or larger payloads. Echo fails the session
on transport errors, including an unsendable datagram; future task handlers can
choose a different policy. Normal remote closure and caller cancellation stop
echo workers. SIGINT/SIGTERM closes the server and active sessions; it does not
drain unfinished transfers.

Before public deployment, add authentication/authorization **before upgrading**,
appropriate origin rules, per-client/session quotas, and application-specific
deadlines and limits. This is a reusable starting point, not a media protocol or
a production abuse-prevention layer.

## Validate

```sh
go test -race ./...
go vet ./...
go build ./...
# Optional Node.js 22+ for dependency-free browser transport unit tests:
node --test cmd/browser/transport.test.mjs
```

Integration tests use real local UDP WebTransport connections and generated
in-memory certificates trusted only by their test clients. They cover binary
bidirectional and unidirectional streams, datagrams, and cancellation during
an active stream. They require permission to bind loopback UDP sockets.
