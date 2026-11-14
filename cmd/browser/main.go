package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

func main() {
	if err := run(); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("browser page server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "localhost:8443", "HTTP page listen address (use loopback for local development)")
	endpointURL := flag.String("url", "https://127.0.0.1:4433/echo", "WebTransport echo endpoint")
	certFile := flag.String("cert", "certs/localhost.pem", "public endpoint certificate PEM; empty uses browser system trust")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	handler, err := newPageHandler(*endpointURL, *certFile)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	slog.Info("browser page server starting", "url", "http://"+*addr, "endpoint", *endpointURL)
	select {
	case err := <-result:
		return fmt.Errorf("serve browser page: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			return errors.Join(shutdownErr, server.Close())
		}
		serveErr := <-result
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return serveErr
	}
}

func newPageHandler(endpointURL, certFile string) (http.Handler, error) {
	u, err := url.Parse(endpointURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("endpoint must be an HTTPS URL without credentials or a fragment")
	}
	config := struct {
		URL             string `json:"url"`
		CertificateHash string `json:"certificateHash"`
	}{URL: endpointURL}
	if certFile != "" {
		data, err := os.ReadFile(certFile)
		if err != nil {
			return nil, fmt.Errorf("read public certificate: %w", err)
		}
		block, _ := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("parse public certificate: expected a CERTIFICATE PEM block")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse public certificate: %w", err)
		}
		hash := sha256.Sum256(cert.Raw)
		config.CertificateHash = base64.StdEncoding.EncodeToString(hash[:])
	}
	data, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode browser defaults: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if _, err := w.Write(data); err != nil {
			slog.Warn("write browser defaults failed", "error", err)
		}
	})
	web, err := fs.Sub(assets, "web")
	if err != nil {
		return nil, fmt.Errorf("load embedded browser assets: %w", err)
	}
	files := http.FileServer(http.FS(web))
	for _, path := range []string{"/{$}", "/app.mjs", "/transport.mjs", "/style.css", "/tokens.css"} {
		mux.Handle("GET "+path, files)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' https:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	}), nil
}
