package endpoint_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"webtransport-demo/endpoint"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func TestEchoBidirectionalBinary(t *testing.T) {
	session, ctx := dialEndpoint(t, endpoint.Echo, nil)
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0, 255, 128, 1, 13, 10}, 16000)
	written := make(chan error, 1)
	go func() {
		_, err := stream.Write(payload)
		if err == nil {
			err = stream.Close()
		}
		written <- err
	}()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo differs: got %d bytes, want %d", len(got), len(payload))
	}
}

func TestEchoUnidirectionalBinary(t *testing.T) {
	session, ctx := dialEndpoint(t, endpoint.Echo, nil)
	out, err := session.OpenUniStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := out.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, 255, 128, 42}
	if _, err := out.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	in, err := session.AcceptUniStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := in.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(in)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo = %v, want %v", got, payload)
	}
}

func TestEchoDatagramBinary(t *testing.T) {
	session, ctx := dialEndpoint(t, endpoint.Echo, nil)
	payload := []byte{0, 255, 128, 42}
	// Datagram loss is allowed even on loopback. Retransmit the same payload
	// until a response arrives, without assuming reliable transport.
	timer := time.NewTicker(20 * time.Millisecond)
	defer timer.Stop()
	receiveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	received := make(chan result, 1)
	go func() {
		data, err := session.ReceiveDatagram(receiveCtx)
		received <- result{data, err}
	}()
	for {
		if err := session.SendDatagram(payload); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-received:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if !bytes.Equal(r.data, payload) {
				t.Fatalf("echo = %v, want %v", r.data, payload)
			}
			return
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestEchoCancellationUnblocksActiveStream(t *testing.T) {
	done := make(chan error, 1)
	cancelHandler := make(chan context.CancelFunc, 1)
	handler := func(ctx context.Context, session *webtransport.Session) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		cancelHandler <- cancel
		err := endpoint.Echo(ctx, session)
		done <- err
		return err
	}
	session, ctx := dialEndpoint(t, handler, nil)
	stream, err := session.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("still open")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("still open"))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	cancel := <-cancelHandler
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("echo did not stop after cancellation")
	}
}

func dialEndpoint(t *testing.T, handler endpoint.SessionHandler, headers http.Header) (*webtransport.Session, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cert, roots := testCertificate(t)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	mux := http.NewServeMux()
	server := &webtransport.Server{H3: &http3.Server{
		Handler:   mux,
		TLSConfig: http3.ConfigureTLSConfig(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}),
	}}
	mux.Handle("/echo", &endpoint.Handler{Server: server, HandleSession: handler})
	done := make(chan error, 1)
	go func() { done <- server.Serve(conn) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	client := &webtransport.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, NextProtos: []string{http3.NextProtoH3}, MinVersion: tls.VersionTLS13,
	}}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	response, session, err := client.Dial(ctx, "https://"+conn.LocalAddr().String()+"/echo", headers)
	if err != nil {
		t.Fatalf("dial: %v (response: %v)", err, response)
	}
	t.Cleanup(func() {
		if err := session.CloseWithError(0, "test complete"); err != nil {
			t.Error(err)
		}
	})
	return session, ctx
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, roots
}
