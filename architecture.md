# Architecture and implementation decisions

## Purpose and scope

The application is a reusable WebTransport server written in Go, with an echo
handler as the first demonstration. It accepts arbitrary bytes so future
handlers can process media, files, messages, or other content without changing
the transport adapter.

The current implementation provides:

- HTTP/3 WebTransport session establishment over QUIC and UDP.
- A reusable HTTP-to-session handler.
- An echo task for bidirectional streams, unidirectional streams, and datagrams.
- A server command with TLS configuration, origin validation, and shutdown.
- An embedded browser test UI with certificate pinning and byte-verified echo checks.
- Unit tests and real-connection integration tests.

It does not yet provide media framing, codecs, task multiplexing inside a
session or production access controls.

## How the application was built

### 1. Inspect the starting point and upstream APIs

The workspace was initially empty. The implementation started by checking the
available Go toolchain and the upstream `webtransport-go` server, session, and
client APIs rather than copying an older example.

The module pins the following direct dependencies in [go.mod](./go.mod):

| Dependency | Version | Purpose |
| --- | --- | --- |
| `github.com/quic-go/webtransport-go` | `v0.13.0` | WebTransport sessions and HTTP/3 integration |
| `github.com/quic-go/quic-go` | `v0.62.0` | HTTP/3 server configuration |
| `golang.org/x/sync` | `v0.22.0` | Coordinated concurrent work through `errgroup` |

The module requires Go 1.26 or newer, matching the selected WebTransport
dependency's minimum. Its client API uses `webtransport.Transport`, not the
`Dialer` type found in some older examples.

### 2. Separate transport adaptation from task behavior

The first public boundary was `endpoint.Handler`, an implementation of
`http.Handler`. Its task callback is:

```go
type SessionHandler func(context.Context, *webtransport.Session) error
```

This keeps HTTP upgrade and session cleanup in one reusable adapter while
allowing each task to use the full WebTransport session API. There is no
framework-specific context, mandatory message envelope, or media-only interface.

### 3. Establish the first test-first behavior

The initial unit test required ordinary GET requests to receive HTTP 405 and
an `Allow: CONNECT` header. It failed against the initial stub, then passed
after the CONNECT-only check was implemented.

Next, an integration test was added to transfer binary data over a real local
WebTransport connection. The environment blocked binding a loopback UDP socket,
including after a requested sandbox policy update. That failure occurred before
the application behavior could be tested.

Implementation continued with compilation and static checks, as agreed.
The remaining echo integration tests were added, but the complete echo
implementation has not been verified through a red-green integration cycle.

### 4. Implement byte-oriented echo and session lifecycle

The echo task was built around incremental copying, concurrent stream
processing, and a datagram receive/send loop. Cancellation was explicitly made
to close the session so active stream reads and writes could unblock.

### 5. Add the executable and operating instructions

The executable was placed in [cmd/server/main.go](./cmd/server/main.go), separate
from the reusable [endpoint package](./endpoint/). It loads a certificate,
registers `/echo`, binds a UDP socket, and handles process signals.

The original setup recommendation used a locally installed development CA.
That approach was rejected because it changes system trust. The certificate
strategy described below instead uses existing public trust or trust scoped to
an individual client connection.

### 6. Add persistent local development certificates

The integration tests' in-memory certificates could not satisfy the server
command's file-based TLS configuration. A separate
[cmd/certgen command](./cmd/certgen/main.go) was added to generate a persistent
development certificate and key without installing a CA.

Test-first cycles covered a loadable short-lived P-256 certificate, preservation
of existing files, and a printed SHA-256 pin matching the generated certificate's
full DER bytes. The command only uses Go's standard library.

It creates a seven-day self-signed leaf certificate for `localhost`, `127.0.0.1`,
and `::1`, writes PEM files, and prints the expiration and base64-encoded pin.
Its defaults match the server's expected certificate paths. New private key
files use mode `0600`; new parent directories use mode `0700`. It uses exclusive
file creation to refuse existing paths, and cleans up files created by a failed
pair-writing operation rather than leaving a partial pair. Abrupt process or
machine failure can still interrupt the writes; this is not an atomic rotation
mechanism.

Certificate generation remains separate from server startup so production
configuration never silently falls back to a self-signed certificate.

## Components and request flow

```text
Browser or Go WebTransport client
    |
    | TLS 1.3 + QUIC + HTTP/3 over UDP
    | Extended CONNECT to /echo
    v
webtransport.Server / http3.Server
    |
    v
http.ServeMux
    |
    v
endpoint.Handler
    | Validate method and handler configuration
    | Upgrade request; library checks origin and protocol requirements
    v
SessionHandler(session.Context(), session)
    |
    +-- Echo: incoming bidirectional streams -> same streams
    +-- Echo: incoming unidirectional streams -> new outgoing streams
    +-- Echo: incoming datagrams -> outgoing datagrams
    |
    v
Handler returns -> endpoint logs errors and closes session
```

### Executable: server configuration

[cmd/server/main.go](./cmd/server/main.go) owns process-level responsibilities:

- `-addr`: UDP bind address, defaulting to `localhost:4433`.
- `-cert` and `-key`: PEM certificate and private key paths.
- `-origin`: optional exact browser origin to allow.
- TLS certificate loading before opening the listening socket.
- HTTP/3 TLS configuration via `http3.ConfigureTLSConfig`, advertising `h3` ALPN.
- Route registration and server startup.
- SIGINT/SIGTERM handling and server closure.

The command loads certificates once at startup. It does not request, generate,
renew, or hot-reload them. Missing certificate files are startup errors, not a
reason to fall back to insecure transport.

There is no `main.go` in the endpoint package because it is a library, not an
executable. Run the command from the module root with `go run ./cmd/server`.

### Executable: browser test page

[cmd/browser/main.go](./cmd/browser/main.go) runs a separate, loopback HTTP page
server. It embeds the HTML, CSS, and JavaScript from
[cmd/browser/web](./cmd/browser/web/), exposes only the named client assets and
`/config.json`, and never serves the working directory or private keys.

- `-addr`: page TCP address, defaulting to `localhost:8443`.
- `-url`: echo endpoint, defaulting to `https://127.0.0.1:4433/echo`.
- `-cert`: local public certificate used to calculate the SHA-256 DER pin;
  defaults to `certs/localhost.pem`. An empty value selects normal browser trust.

The browser, not the page server, connects to the WebTransport endpoint. The
page origin must match the endpoint's `-origin`. Loopback HTTP is a secure
context; non-loopback hosting needs HTTPS. Pins are loaded from local,
authenticated certificate material, never discovered from the remote endpoint.
No CA is installed and no browser security flags are disabled.
The default endpoint uses IPv4 explicitly to avoid browsers resolving
`localhost` to IPv6 while Go listens on IPv4. The page origin remains
`http://localhost:8443`.

[transport.mjs](./cmd/browser/web/transport.mjs) opens a fresh session per test,
reads and writes concurrently, verifies every byte, and requests session closure
on completion, cancellation, timeout, or error. Unidirectional echo is read
from an incoming server stream; datagrams are checked as single messages
without waiting for a channel EOF. Streams are bounded to 64 KiB in this UI,
and response buffering never exceeds the expected payload length. Datagrams
are additionally limited by the negotiated maximum. A ten-second deadline
covers setup and I/O; datagram loss is reported as a timeout, not a success or
an automatic protocol fallback.

The UI supports UTF-8 and binary hex input, individual or sequential all-mode
tests, a bounded hex preview, and explicit pass/fail results. The provisional
neutral/blue CSS tokens have no pre-existing project design system.
Unit tests use real Web Streams and a simulated WebTransport peer. A socket-free
TLS handshake regression test verifies that the server actually negotiates
`h3`; without configuring ALPN, browsers reject QUIC setup with
`NO_APPLICATION_PROTOCOL`. Live Chromium 150 testing against externally
started servers verified all three modes and exact binary echo. Accessibility
audits remain unverified, and shell UDP integration tests still need permission
to create local listeners.

### Adapter: HTTP to session

[endpoint/handler.go](./endpoint/handler.go) handles common endpoint behavior:

1. Reject methods other than CONNECT with HTTP 405.
2. Reject missing server or task configuration with HTTP 500 and a log entry.
3. Call `webtransport.Server.Upgrade`.
4. Log upgrade failures and attempt an HTTP 400 response.
5. Invoke the task with the session's context.
6. Log task failures and close the session.

Successful task completion requests session close code 0. Task failure requests
code 1 without exposing the internal error text to the remote client. A session
already closed by the peer or task cannot be closed again with a different code.

Logging uses the configured `slog.Logger`, or `slog.Default()` when none is
provided. Configure the adapter and server before serving concurrent requests.

### Task: echo

[endpoint/echo.go](./endpoint/echo.go) treats payloads as opaque bytes:

| Input | Processing | Completion |
| --- | --- | --- |
| Bidirectional stream | `io.Copy` back onto the same stream | Close response write side after input EOF |
| Unidirectional stream | Open a server unidirectional stream and copy into it | Close outgoing stream after input EOF |
| Datagram | Receive payload and send it back | Preserve payload, not reliable delivery |

Streams preserve byte order within an individual stream. Read chunks do not
necessarily correspond to the sender's write chunks. Different streams operate
independently; no ordering is promised between unidirectional echo responses.

For a future media task, codecs, timestamps, stream identifiers, metadata, and
message boundaries belong to the task's application protocol. Generic byte
transport alone does not provide media synchronization or playback semantics.

## Concurrency, backpressure, and cancellation

Echo runs three receive loops in an `errgroup`: one for each stream type and one
for datagrams. Accepted streams get their own copy workers.

The generic internal `echoStreams` helper shares acceptance and slot management
between the two stream types. Each type has a limit of 32 active workers per
session. It acquires a slot before accepting another stream, providing
application-level backpressure instead of starting unlimited copy goroutines.

`io.Copy` transfers data incrementally with bounded buffers. The application
does not collect an entire media stream in memory. QUIC and the library also
maintain their own transport buffers; the worker limit is not a global memory,
connection, or bandwidth quota.

The task lifetime follows `session.Context()`, not the HTTP request's lifetime.
This avoids terminating a session merely because its establishing request
context ends.

Canceling an accept context does not interrupt an existing stream `Read` or
`Write`. Echo therefore registers a cancellation callback that closes the
session with code 1 and the reason `echo stopped`. It waits for its workers and,
when running, that callback before returning. Caller cancellation and normal
remote session closure are treated as normal task termination.

Other transport errors are propagated. The demo's policy is to stop the entire
session rather than isolate a failed stream or silently discard a failed
datagram send. Future tasks can choose a different error policy.

Process shutdown closes active sessions; it does not drain unfinished transfers.

## Extending the application

Additional tasks should implement `SessionHandler` and be registered on the
existing mux using the same WebTransport server. Separate routes such as
`/media` and `/files` can select different tasks without changing the adapter.
Only `/echo` is currently implemented.

Each task owns its stream acceptance, outgoing streams, datagrams, application
protocol, and worker cleanup. Avoid competing accept loops for the same stream
type unless they intentionally distribute work.

If multiple tasks must share one session, define an application dispatch
protocol first. Route selection alone does not multiplex tasks within a session.

## Certificates for production and local development

### Encryption and trust are separate requirements

Go can generate an X.509 certificate and use it for TLS. That provides
cryptographic material, but does not make the certificate trusted by browsers.
The client must authenticate the server through either:

- A publicly trusted certificate chain matching the connection hostname.
- Explicit trust in the certificate, scoped to that client or connection.

Neither recommended approach below requires installing a new OS root CA.
Do not disable certificate verification or use global insecure browser flags.

### Production: publicly trusted certificates

Use a certificate issued by a public CA, such as Let's Encrypt, for a hostname
you control. The client must connect using a name covered by the certificate's
subject alternative names, not `localhost`.

An ACME client performs issuance and renewal outside the current application.
Let's Encrypt requires proof of control:

- HTTP-01 uses a publicly reachable HTTP challenge on TCP port 80.
- DNS-01 uses public DNS TXT records and can validate a domain whose application
  server is not exposed to the public internet.

The application's UDP-only listener does not implement these challenges.
Prefer narrowly scoped DNS credentials or a separate issuance environment
rather than exposing broad DNS account credentials to the application.

Supply a PEM certificate chain, with the leaf first followed by intermediates,
and its matching private key:

```sh
go run ./cmd/server \
  -addr 0.0.0.0:4433 \
  -cert /path/to/fullchain.pem \
  -key /path/to/privkey.pem \
  -origin https://app.example.com
```

The paths and origin above are placeholders. Clients connect to a covered
hostname such as `https://transport.example.com:4433/echo`, and UDP access to
that port must be allowed. An ordinary TCP HTTPS reverse proxy is not sufficient
unless it specifically supports the required WebTransport/HTTP/3 deployment.

Protect the private key, automate renewal, and restart the current server after
replacing certificate files. Issuing a certificate does not install a new root
CA or change trust for unrelated applications. Public certificate issuance can
also publish the certificate's hostnames through certificate transparency logs.

A publicly trusted certificate is sufficient for TLS authentication, not for
production readiness of the application as a whole.

### Local development: Go clients and automated tests

The integration tests already generate an ECDSA P-256 self-signed certificate
in memory, valid for approximately one hour. They put the certificate into a
fresh `x509.CertPool` used only by their WebTransport client, with HTTP/3 ALPN
configured. Hostname/IP verification remains enabled.

This changes neither the macOS trust store nor trust for other applications.
The test certificate and key are not written to disk and are not available to
the server command.

For a standalone Go development client, use the same client-scoped trust
approach and a certificate with SANs matching the actual endpoint. The
`go run ./cmd/certgen` command now supplies persistent localhost certificate/key
PEM files for the server.

[cmd/client](./cmd/client/main.go) is a standalone string-echo client using
`webtransport.Transport`. It loads the development certificate into a
client-scoped pool, or uses system roots when `-cert=""` is supplied for a
publicly trusted endpoint. No trust store is modified and TLS verification is
not disabled.

The client writes and reads concurrently to avoid flow-control deadlocks for
larger strings. It requires an exact echo and reads at most the sent message's
byte length plus one byte, rejecting truncated, altered, or oversized responses.
It enforces a connection/stream deadline and resets both stream halves on I/O
failure. An optional `-origin` header supports servers configured with an exact
origin restriction; that header is not proof of client identity.

Use `go run ./cmd/client -message "hello"` with a server using the default origin
policy. A browser page or certificate hash is unnecessary for this Go client.

### Local development: browser certificate pinning

Supporting browsers can authenticate a dedicated WebTransport connection using
the `serverCertificateHashes` constructor option rather than public CA trust.

For an interoperable development setup:

1. Run `go run ./cmd/certgen` to generate a self-signed X.509v3 certificate
   with an ECDSA P-256 key.
2. Use a validity interval of less than two weeks, with the current time inside
   that interval.
3. Write the certificate and key as PEM files for the server's `-cert` and
   `-key` options.
4. Use the printed `SHA-256 (base64)` value. The generator hashes the full
   certificate's DER bytes, not the PEM text or just its public key.
5. Supply the 32 hash bytes to the browser as a `serverCertificateHashes` entry
   with algorithm `sha-256`. Keep `allowPooling` false, its default.
6. Before expiration, generate replacement files at new `-cert` and `-key`
   paths, restart the server with those paths, and update the pin. The generator
   refuses to overwrite existing files.

The hash must come from a trusted source, such as a manually verified local
value or an authenticated application configuration channel. Fetching a hash
from the same unauthenticated server and accepting it blindly does not
authenticate that server.

The browser page itself must run in a secure context. Pinning the WebTransport
certificate does not make an untrusted HTTPS page trusted and does not provide
a trust exception for ordinary HTTPS requests or other applications. Check
support for this constructor option in the target browsers.

The generator and a pinned browser-console echo example are now included; see
the [README](./README.md). Certificate hashes are printed locally, not published
over an unauthenticated endpoint. There is still no hosted browser UI or
automatic certificate/pin rotation. Live browser verification remains pending.

### Certificate decision summary

| Environment | Certificate and authentication | OS trust change |
| --- | --- | --- |
| Production browser client | Public CA certificate, matching hostname | None |
| Local domain-based browser client | Public CA certificate obtained through domain validation | None |
| Local Go tests/client | Generated certificate trusted only by that client's `CertPool` | None |
| Local browser with hash support | Short-lived P-256 certificate pinned to the connection | None |

The previously suggested `mkcert -install` workflow is not the chosen approach.
It installs a development CA in system and potentially other application trust
stores. Possession of its CA private key allows signing certificates for more
than just this endpoint, which was considered an unacceptable additional risk.
No such installation was performed during implementation.

## Validation and current limitations

The implementation-time checks that passed were:

```sh
go test -race ./... -run '^TestHandler' -count=1
go test -race ./cmd/certgen -count=1
go test -race ./cmd/client -count=1
go vet ./...
go build ./...
```

[Handler tests](./endpoint/handler_test.go) cover non-CONNECT requests, missing
configuration, and invalid upgrades, including logged error paths.

[Generator tests](./cmd/certgen/main_test.go) verify loadable certificate/key
files, localhost/IP identity verification, P-256 keys, validity below the
two-week browser limit, private key permissions, refusal to overwrite existing
files, cleanup of partial output pairs, and a printed pin matching the
certificate's full DER hash. The generator was also run to create the local
development PEM files; those ignored files are not source artifacts.

[Client tests](./cmd/client/main_test.go) use in-memory duplex pipes at the
WebTransport stream boundary to check large UTF-8 string echo, malformed trust
certificate errors, and rejection of incorrect response lengths or contents.
They do not establish a real QUIC connection.

[Integration tests](./endpoint/echo_test.go) are designed to cover:

- A 96,000-byte binary bidirectional echo with concurrent reading and writing.
- Binary unidirectional echo.
- Binary datagram echo, with retransmission to accommodate permitted loss.
- Cancellation while a stream remains open.

These tests compile, but live integration verification remained blocked by the
sandbox's loopback UDP bind restriction. No successful browser connection,
full integration test run, or media workload validation is claimed.

When loopback UDP is permitted, run:

```sh
go test -race ./...
```

Before a public deployment, add authentication and authorization before upgrade,
appropriate origin rules, session/client quotas, application deadlines,
observability, and task-specific payload limits. Origin checks are not
authentication. The current 32-worker limit does not cap the number of sessions.

## References

- [webtransport-go](https://github.com/quic-go/webtransport-go)
- [WebTransport constructor and certificate hashes](https://developer.mozilla.org/en-US/docs/Web/API/WebTransport/WebTransport)
- [Let's Encrypt challenge types](https://letsencrypt.org/docs/challenge-types/)
- [Quick start and echo example](./README.md)
