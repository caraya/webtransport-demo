package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerateDevelopmentCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "certs", "localhost.pem")
	keyPath := filepath.Join(dir, "certs", "localhost-key.pem")
	var output bytes.Buffer
	if err := run([]string{"-cert", certPath, "-key", keyPath}, &output); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.IsCA {
		t.Fatal("development certificate must not be a CA")
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		t.Fatal("certificate must use ECDSA P-256 for browser pinning")
	}
	if lifetime := leaf.NotAfter.Sub(leaf.NotBefore); lifetime <= 0 || lifetime >= 14*24*time.Hour {
		t.Fatalf("certificate validity interval = %v, must be positive and less than two weeks", lifetime)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	for _, name := range []string{"localhost", "127.0.0.1", "::1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("private key permissions = %o, must deny group/other access", info.Mode().Perm())
	}
}

func TestRefusesExistingFiles(t *testing.T) {
	for _, existing := range []string{"certificate", "key"} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			certPath := filepath.Join(dir, "cert.pem")
			keyPath := filepath.Join(dir, "key.pem")
			path, other := certPath, keyPath
			if existing == "key" {
				path, other = keyPath, certPath
			}
			original := []byte("existing file must remain unchanged")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := run([]string{"-cert", certPath, "-key", keyPath}, &output); err == nil {
				t.Fatal("generation must fail instead of overwriting an existing file")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, original) {
				t.Fatal("existing file was modified")
			}
			if _, err := os.Stat(other); !os.IsNotExist(err) {
				t.Fatalf("failed generation left another output file: %v", err)
			}
		})
	}
}

func TestPrintsCertificatePin(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	var output bytes.Buffer
	if err := run([]string{"-cert", certPath, "-key", keyPath}, &output); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	var pin string
	for _, line := range strings.Split(output.String(), "\n") {
		if value, ok := strings.CutPrefix(line, "SHA-256 (base64): "); ok {
			pin = value
		}
	}
	if pin == "" {
		t.Fatal("output is missing the browser certificate pin")
	}
	got, err := base64.StdEncoding.DecodeString(pin)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(pair.Certificate[0])
	if !bytes.Equal(got, want[:]) {
		t.Fatal("printed pin does not match the full certificate DER SHA-256 hash")
	}
}
