package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPageConfigPinsLocalCertificate(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := newPageHandler("https://localhost:4433/echo", path)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/config.json", nil))
	if response.Code != 200 {
		t.Fatalf("config status = %d, want 200", response.Code)
	}
	var config struct {
		URL             string `json:"url"`
		CertificateHash string `json:"certificateHash"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(der)
	if config.URL != "https://localhost:4433/echo" || config.CertificateHash != base64.StdEncoding.EncodeToString(hash[:]) {
		t.Fatalf("incorrect browser defaults: %+v", config)
	}
}

func TestPageServesOnlyEmbeddedClient(t *testing.T) {
	handler, err := newPageHandler("https://localhost:4433/echo", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/style.css", "/tokens.css", "/app.mjs", "/transport.mjs"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Errorf("%s status = %d, want 200", path, response.Code)
		}
	}
	for _, path := range []string{"/certs/localhost-key.pem", "/main.go", "/web/", "/unknown"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 404 {
			t.Errorf("%s status = %d, want 404", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(response.Body.String(), "<title>WebTransport echo test</title>") {
		t.Fatal("root did not serve the browser client")
	}
}

func TestPageRejectsInvalidConfiguration(t *testing.T) {
	for _, endpointURL := range []string{"http://localhost:4433/echo", "https://", "https://user:pass@localhost/echo", "https://localhost/echo#fragment"} {
		if _, err := newPageHandler(endpointURL, ""); err == nil {
			t.Errorf("accepted invalid endpoint URL %q", endpointURL)
		}
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newPageHandler("https://localhost:4433/echo", path); err == nil {
		t.Fatal("invalid certificate was accepted")
	}
	if _, err := newPageHandler("https://localhost:4433/echo", path+".missing"); err == nil {
		t.Fatal("missing certificate was accepted")
	}
}
