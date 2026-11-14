// Package endpoint adapts HTTP requests to content-agnostic WebTransport sessions.
package endpoint

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/quic-go/webtransport-go"
)

// SessionHandler owns a session until it returns. The endpoint then closes it.
type SessionHandler func(context.Context, *webtransport.Session) error

// Handler upgrades a request and dispatches it to a session handler.
// Configure its fields before serving requests. Origin validation belongs to Server.
type Handler struct {
	Server        *webtransport.Server
	HandleSession SessionHandler
	Logger        *slog.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		w.Header().Set("Allow", http.MethodConnect)
		http.Error(w, "WebTransport requires CONNECT", http.StatusMethodNotAllowed)
		return
	}
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if h.Server == nil || h.HandleSession == nil {
		logger.Error("WebTransport endpoint is not configured", "path", r.URL.Path)
		http.Error(w, "endpoint is not configured", http.StatusInternalServerError)
		return
	}
	session, err := h.Server.Upgrade(w, r)
	if err != nil {
		logger.Warn("WebTransport upgrade failed", "path", r.URL.Path, "error", err)
		http.Error(w, "WebTransport upgrade failed", http.StatusBadRequest)
		return
	}
	// A session can outlive the HTTP request. Its own context owns the handler lifetime.
	err = h.HandleSession(session.Context(), session)
	code := webtransport.SessionErrorCode(0)
	message := "handler complete"
	if err != nil {
		logger.Error("WebTransport handler failed", "path", r.URL.Path, "remote", session.RemoteAddr(), "error", err)
		code = 1
		message = "handler failed"
	}
	if err := session.CloseWithError(code, message); err != nil {
		logger.Warn("WebTransport session close failed", "path", r.URL.Path, "error", err)
	}
}
