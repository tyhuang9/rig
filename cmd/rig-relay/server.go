package main

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const controllerConnectPath = "/v1/controllers/connect"

type connectionContextKey struct{}

type readyStore interface {
	Ready(context.Context) error
}

type relayHTTPHandler struct {
	service         http.Handler
	websocket       http.Handler
	store           readyStore
	accepting       *atomic.Bool
	metrics         http.Handler
	readTimeout     time.Duration
	writeTimeout    time.Duration
	requests        admissionTracker
	serviceSlots    chan struct{}
	serviceActive   atomic.Int64
	serviceRejected atomic.Uint64
}

func (h *relayHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if exemptWebSocketRequest(r) {
		h.websocket.ServeHTTP(w, r)
		return
	}
	if !h.requests.Admit() {
		writeServiceUnavailable(w)
		return
	}
	defer h.requests.Done()
	cancelRequest, ok := h.applyDeadlines(w, r)
	if !ok {
		return
	}
	defer cancelRequest()
	if reservedRelayRoute(r.URL.Path, r.URL.EscapedPath()) && (r.URL.RawQuery != "" || r.URL.Path != r.URL.EscapedPath() || !exactRelayRoute(r.URL.Path)) {
		writeOperational(w, http.StatusNotFound, "not found\n")
		return
	}
	switch r.URL.Path {
	case controllerConnectPath:
		// Non-upgrade and otherwise malformed connect requests are deliberately
		// bounded before the WSS handler returns its stable validation response.
		h.serveBounded(w, r, h.websocket)
	case "/healthz":
		h.serveHealth(w, r)
	case "/readyz":
		h.serveReady(w, r)
	case "/metrics":
		if !requireOperationalGET(w, r) {
			return
		}
		h.metrics.ServeHTTP(w, r)
	default:
		h.serveBounded(w, r, h.service)
	}
}

func (h *relayHTTPHandler) serveBounded(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if h.serviceSlots == nil {
		writeServiceUnavailable(w)
		return
	}
	select {
	case h.serviceSlots <- struct{}{}:
		h.serviceActive.Add(1)
		defer func() { <-h.serviceSlots; h.serviceActive.Add(-1) }()
		next.ServeHTTP(w, r)
	default:
		h.serviceRejected.Add(1)
		writeServiceUnavailable(w)
	}
}

func (h *relayHTTPHandler) StopAdmissions()                { h.requests.StopAdmissions() }
func (h *relayHTTPHandler) Wait(ctx context.Context) error { return h.requests.Wait(ctx) }

func (h *relayHTTPHandler) applyDeadlines(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if h.readTimeout <= 0 && h.writeTimeout <= 0 {
		return func() {}, true
	}
	now := time.Now()
	controller := http.NewResponseController(w)
	if h.readTimeout > 0 {
		if err := controller.SetReadDeadline(now.Add(h.readTimeout)); err != nil {
			closeRequestConnection(r)
			writeServiceUnavailable(w)
			return nil, false
		}
	}
	if h.writeTimeout > 0 {
		if err := controller.SetWriteDeadline(now.Add(h.writeTimeout)); err != nil {
			closeRequestConnection(r)
			writeServiceUnavailable(w)
			return nil, false
		}
	}
	cleanup := func() {}
	bound := h.writeTimeout
	if h.readTimeout > bound {
		bound = h.readTimeout
	}
	if bound > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), bound)
		*r = *r.WithContext(ctx)
		cleanup = cancel
	}
	return cleanup, true
}

func closeRequestConnection(r *http.Request) {
	if conn, ok := r.Context().Value(connectionContextKey{}).(net.Conn); ok {
		_ = conn.Close()
	}
}

func exemptWebSocketRequest(r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != controllerConnectPath || r.URL.EscapedPath() != controllerConnectPath || r.URL.RawQuery != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return false
	}
	if !headerHasToken(r.Header.Values("Connection"), "upgrade") || !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	if !offeredProtocol(r.Header.Values("Sec-WebSocket-Protocol"), "rig.relay.v1") {
		return false
	}
	versions := r.Header.Values("Sec-WebSocket-Version")
	if len(versions) != 1 || versions[0] != "13" {
		return false
	}
	keys := r.Header.Values("Sec-WebSocket-Key")
	if len(keys) != 1 {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(keys[0])
	return err == nil && len(decoded) == 16 && base64.StdEncoding.EncodeToString(decoded) == keys[0]
}

func headerHasToken(values []string, required string) bool {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), required) {
				return true
			}
		}
	}
	return false
}

func offeredProtocol(values []string, required string) bool { return headerHasToken(values, required) }

func writeServiceUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte("service unavailable\n"))
}

func exactRelayRoute(path string) bool {
	return path == controllerConnectPath || path == "/healthz" || path == "/readyz" || path == "/metrics"
}

func reservedRelayRoute(path, escapedPath string) bool {
	for _, reserved := range []string{controllerConnectPath, "/healthz", "/readyz", "/metrics"} {
		if path == reserved || strings.HasPrefix(path, reserved+"/") || escapedPath == reserved || strings.HasPrefix(escapedPath, reserved+"/") || strings.HasPrefix(escapedPath, reserved+"%") {
			return true
		}
	}
	return false
}

func (h *relayHTTPHandler) serveHealth(w http.ResponseWriter, r *http.Request) {
	if !requireOperationalGET(w, r) {
		return
	}
	writeOperational(w, http.StatusOK, "ok\n")
}

func (h *relayHTTPHandler) serveReady(w http.ResponseWriter, r *http.Request) {
	if !requireOperationalGET(w, r) {
		return
	}
	if h.accepting == nil || !h.accepting.Load() {
		writeOperational(w, http.StatusServiceUnavailable, "not ready\n")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if h.store == nil || h.store.Ready(ctx) != nil {
		writeOperational(w, http.StatusServiceUnavailable, "not ready\n")
		return
	}
	writeOperational(w, http.StatusOK, "ready\n")
}

func requireOperationalGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", http.MethodGet)
	writeOperational(w, http.StatusMethodNotAllowed, "method not allowed\n")
	return false
}

func writeOperational(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
