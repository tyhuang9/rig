package main

import (
	"io"
	"log"
	"log/slog"
	"sync"
	"time"
)

const httpErrorLogInterval = time.Minute

// httpErrorSanitizer deliberately treats net/http output as opaque hostile
// bytes. It never parses or forwards those bytes because they may contain a
// peer address, request material, a raw TLS error, or a panic stack.
type httpErrorSanitizer struct {
	mu      sync.Mutex
	nextLog time.Time
	now     func() time.Time
	logger  *slog.Logger
	metrics *metrics
}

func newHTTPErrorLog(metricSet *metrics, logger *slog.Logger, now func() time.Time) *log.Logger {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if now == nil {
		now = time.Now
	}
	writer := &httpErrorSanitizer{now: now, logger: logger, metrics: metricSet}
	return log.New(writer, "", 0)
}

func (w *httpErrorSanitizer) Write(raw []byte) (int, error) {
	if w.metrics != nil {
		w.metrics.httpServerErrors.Add(1)
	}
	now := w.now().UTC()
	w.mu.Lock()
	shouldLog := w.nextLog.IsZero() || !now.Before(w.nextLog)
	if shouldLog {
		w.nextLog = now.Add(httpErrorLogInterval)
	}
	w.mu.Unlock()
	if shouldLog {
		w.logger.Warn("relay HTTP server event", "code", "server_error")
	}
	return len(raw), nil
}
