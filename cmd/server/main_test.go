package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
)

func TestServerNegotiatesHTTP3(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
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
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		for _, conn := range []net.Conn{serverConn, clientConn} {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	for _, conn := range []net.Conn{serverConn, clientConn} {
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	server := tls.Server(serverConn, serverTLSConfig(tls.Certificate{
		Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf,
	}))
	client := tls.Client(clientConn, &tls.Config{
		RootCAs: roots, ServerName: "localhost",
		NextProtos: []string{http3.NextProtoH3}, MinVersion: tls.VersionTLS13,
	})
	done := make(chan error, 1)
	go func() { done <- server.Handshake() }()
	if err := client.Handshake(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := client.ConnectionState().NegotiatedProtocol; got != http3.NextProtoH3 {
		t.Fatalf("negotiated protocol = %q, want HTTP/3 h3", got)
	}
}
