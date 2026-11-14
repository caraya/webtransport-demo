package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"

	"webtransport-demo/endpoint"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "localhost:4433", "UDP listen address")
	certFile := flag.String("cert", "certs/localhost.pem", "TLS certificate file")
	keyFile := flag.String("key", "certs/localhost-key.pem", "TLS private key file")
	origin := flag.String("origin", "", "allowed browser origin (default: same host and port)")
	flag.Parse()
	if *origin != "" {
		u, err := url.Parse(*origin)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
			u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return fmt.Errorf("invalid origin %q: use scheme://host[:port] without a path", *origin)
		}
	}

	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		return fmt.Errorf("load TLS certificate: %w", err)
	}
	mux := http.NewServeMux()
	server := &webtransport.Server{H3: &http3.Server{
		Addr:      *addr,
		Handler:   mux,
		TLSConfig: serverTLSConfig(cert),
	}}
	if *origin != "" {
		server.CheckOrigin = func(r *http.Request) bool {
			return r.Header.Get("Origin") == *origin
		}
	}

	mux.Handle("/echo", &endpoint.Handler{Server: server, HandleSession: endpoint.Echo})

	conn, err := net.ListenPacket("udp", *addr)
	if err != nil {
		return fmt.Errorf("listen on UDP %s: %w", *addr, err)
	}
	defer func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.Warn("UDP socket close failed", "error", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.Serve(conn) }()
	slog.Info("WebTransport echo listening", "address", conn.LocalAddr(), "path", "/echo")
	select {
	case err := <-result:
		if err != nil {
			err = fmt.Errorf("serve WebTransport: %w", err)
		}
		return errors.Join(err, server.Close())
	case <-ctx.Done():
		closeErr := server.Close()
		serveErr := <-result
		if errors.Is(serveErr, context.Canceled) || errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
			serveErr = nil
		}
		return errors.Join(closeErr, serveErr)
	}
}

func serverTLSConfig(cert tls.Certificate) *tls.Config {
	return http3.ConfigureTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	})
}
