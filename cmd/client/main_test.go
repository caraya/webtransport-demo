package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quic-go/webtransport-go"
)

func TestEchoMessage(t *testing.T) {
	stream, incoming, outgoing := pipeStream(t)
	serverDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(outgoing, incoming)
		serverDone <- errors.Join(err, outgoing.Close())
	}()
	message := strings.Repeat("hello \u4e16\u754c\n", 32000)
	got, err := echoMessage(stream, message)
	if err != nil {
		t.Fatal(err)
	}
	if got != message {
		t.Fatal("echo differs from the sent UTF-8 string")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

type pipeEchoStream struct {
	*io.PipeReader
	writer *io.PipeWriter
}

func (s *pipeEchoStream) Write(data []byte) (int, error) { return s.writer.Write(data) }
func (s *pipeEchoStream) Close() error                   { return s.writer.Close() }
func (s *pipeEchoStream) CancelRead(webtransport.StreamErrorCode) {
	s.PipeReader.CloseWithError(errors.New("read canceled"))
}
func (s *pipeEchoStream) CancelWrite(webtransport.StreamErrorCode) {
	s.writer.CloseWithError(errors.New("write canceled"))
}

func pipeStream(t *testing.T) (*pipeEchoStream, *io.PipeReader, *io.PipeWriter) {
	t.Helper()
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	t.Cleanup(func() {
		for _, closer := range []io.Closer{serverRead, clientWrite, clientRead, serverWrite} {
			if err := closer.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return &pipeEchoStream{PipeReader: clientRead, writer: clientWrite}, serverRead, serverWrite
}

func TestClientRejectsInvalidCertificate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if err := os.WriteFile(path, []byte("not a PEM certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	err := run([]string{"-cert", path}, &output)
	if err == nil || !strings.Contains(err.Error(), "parse trust certificate") {
		t.Fatalf("error = %v, want an explicit certificate parse failure", err)
	}
	if output.Len() != 0 {
		t.Fatal("client printed output despite failing certificate validation")
	}
}

func TestEchoMessageRejectsUnexpectedResponse(t *testing.T) {
	for _, response := range []string{"hell", "world", "hello!"} {
		t.Run(response, func(t *testing.T) {
			stream, incoming, outgoing := pipeStream(t)
			serverDone := make(chan error, 1)
			go func() {
				_, readErr := io.Copy(io.Discard, incoming)
				_, writeErr := io.WriteString(outgoing, response)
				serverDone <- errors.Join(readErr, writeErr, outgoing.Close())
			}()
			got, err := echoMessage(stream, "hello")
			if err == nil {
				t.Fatalf("unexpected response %q was accepted", got)
			}
			if got != "" {
				t.Fatal("failed echo returned a success-shaped response")
			}
			if err := <-serverDone; err != nil {
				t.Logf("peer observed expected stream cancellation: %v", err)
			}
		})
	}
}
