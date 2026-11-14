package endpoint_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"webtransport-demo/endpoint"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func TestHandlerRejectsNonConnect(t *testing.T) {
	h := &endpoint.Handler{HandleSession: endpoint.Echo}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/echo", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}

	if w.Header().Get("Allow") != http.MethodConnect {
		t.Fatalf("Allow = %q, want CONNECT", w.Header().Get("Allow"))
	}
}

func TestHandlerRejectsInvalidConfiguration(t *testing.T) {
	for _, missing := range []string{"server", "session handler"} {
		t.Run(missing, func(t *testing.T) {
			var logs bytes.Buffer
			h := &endpoint.Handler{
				Server:        &webtransport.Server{},
				HandleSession: endpoint.Echo,
				Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
			}
			if missing == "server" {
				h.Server = nil
			} else {
				h.HandleSession = nil
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodConnect, "/echo", nil))
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", w.Code)
			}
			if logs.Len() == 0 {
				t.Fatal("configuration failure was not logged")
			}
		})
	}
}

func TestHandlerRejectsInvalidUpgrade(t *testing.T) {
	var logs bytes.Buffer
	server := &webtransport.Server{H3: &http3.Server{}}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error(err)
		}
	})
	h := &endpoint.Handler{
		Server:        server,
		HandleSession: endpoint.Echo,
		Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodConnect, "/echo", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for non-WebTransport CONNECT", w.Code)
	}
	if logs.Len() == 0 {
		t.Fatal("upgrade failure was not logged")
	}
}
