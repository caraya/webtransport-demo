package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
	"golang.org/x/sync/errgroup"
)

type echoStream interface {
	io.ReadWriteCloser
	CancelRead(webtransport.StreamErrorCode)
	CancelWrite(webtransport.StreamErrorCode)
}

func echoMessage(stream echoStream, message string) (string, error) {
	var group errgroup.Group
	var echoed []byte
	abort := func() {
		stream.CancelRead(1)
		stream.CancelWrite(1)
	}
	group.Go(func() error {
		if _, err := io.Copy(stream, strings.NewReader(message)); err != nil {
			abort()
			return fmt.Errorf("send message: %w", err)
		}
		if err := stream.Close(); err != nil {
			abort()
			return fmt.Errorf("finish message: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		data, err := io.ReadAll(io.LimitReader(stream, int64(len(message))+1))
		if err != nil {
			abort()
			return fmt.Errorf("read echo: %w", err)
		}
		if !bytes.Equal(data, []byte(message)) {
			abort()
			return fmt.Errorf("echo does not match the sent message")
		}
		echoed = data
		return nil
	})
	if err := group.Wait(); err != nil {
		return "", err
	}
	return string(echoed), nil
}

func run(args []string, output io.Writer) (resultErr error) {
	flags := flag.NewFlagSet("client", flag.ContinueOnError)
	flags.SetOutput(output)
	endpointURL := flags.String("url", "https://localhost:4433/echo", "WebTransport echo URL")
	certPath := flags.String("cert", "certs/localhost.pem", "client-scoped trust certificate PEM (empty uses system roots)")
	message := flags.String("message", "hello", "string to send and echo")
	origin := flags.String("origin", "", "Origin header, if the server requires an explicit origin")
	timeout := flags.Duration("timeout", 10*time.Second, "total connection and echo timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		return fmt.Errorf("provide a positive -timeout and no positional arguments; use -message for text")
	}
	u, err := url.Parse(*endpointURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid WebTransport URL %q: use https://host[:port]/path without credentials or a fragment", *endpointURL)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{http3.NextProtoH3}}
	if *certPath != "" {
		data, err := os.ReadFile(*certPath)
		if err != nil {
			return fmt.Errorf("read trust certificate: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(data) {
			return fmt.Errorf("parse trust certificate %q: no valid PEM certificates", *certPath)
		}
		tlsConfig.RootCAs = roots
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	transport := &webtransport.Transport{TLSClientConfig: tlsConfig}
	defer func() {
		if err := transport.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close client transport: %w", err))
		}
	}()
	headers := make(http.Header)
	if *origin != "" {
		headers.Set("Origin", *origin)
	}
	_, session, err := transport.Dial(ctx, *endpointURL, headers)
	if err != nil {
		return fmt.Errorf("connect to WebTransport: %w", err)
	}
	defer func() {
		if err := session.CloseWithError(0, "client complete"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close client session: %w", err))
		}
	}()
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		return fmt.Errorf("open echo stream: %w", err)
	}
	deadline, _ := ctx.Deadline() // WithTimeout always supplies a deadline.
	if err := stream.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set echo deadline: %w", err)
	}
	echoed, err := echoMessage(stream, *message)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, echoed); err != nil {
		return fmt.Errorf("print echo: %w", err)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("echo client failed", "error", err)
		os.Exit(1)
	}
}
