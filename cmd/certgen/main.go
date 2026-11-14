package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil && !errors.Is(err, flag.ErrHelp) {
		slog.Error("development certificate generation failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("certgen", flag.ContinueOnError)
	flags.SetOutput(output)
	certPath := flags.String("cert", "certs/localhost.pem", "development certificate PEM path")
	keyPath := flags.String("key", "certs/localhost-key.pem", "development private key PEM path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *certPath == "" || *keyPath == "" {
		return fmt.Errorf("provide nonempty -cert and -key paths without positional arguments")
	}
	certAbs, err := filepath.Abs(*certPath)
	if err != nil {
		return fmt.Errorf("resolve certificate path: %w", err)
	}
	keyAbs, err := filepath.Abs(*keyPath)
	if err != nil {
		return fmt.Errorf("resolve key path: %w", err)
	}
	if certAbs == keyAbs {
		return fmt.Errorf("certificate and private key paths must differ")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate P-256 key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate certificate serial number: %w", err)
	}
	serial.Add(serial, big.NewInt(1))
	notBefore := time.Now().UTC().Add(-time.Minute)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "localhost development"},
		NotBefore:             notBefore,
		NotAfter:              notBefore.Add(7 * 24 * time.Hour),
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("encode private key: %w", err)
	}
	for _, path := range []string{certAbs, keyAbs} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("create output directory: %w", err)
		}
	}
	if err := writeNewFile(keyAbs, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := writeNewFile(certAbs, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0644); err != nil {
		return errors.Join(fmt.Errorf("write certificate: %w", err), os.Remove(keyAbs))
	}
	hash := sha256.Sum256(der)
	if _, err := fmt.Fprintf(output, "Certificate: %s\nPrivate key: %s\nExpires (UTC): %s\nSHA-256 (base64): %s\n",
		certAbs, keyAbs, template.NotAfter.Format(time.RFC3339), base64.StdEncoding.EncodeToString(hash[:])); err != nil {
		return fmt.Errorf("print certificate details (PEM files were created): %w", err)
	}
	return nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}
