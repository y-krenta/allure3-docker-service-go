package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// contextKey is a private type for request-context keys, so this package's
// keys can never collide with keys set by other packages.
type contextKey string

// requestIDKey is the context key under which requestID stores the
// per-request UUID.
const requestIDKey contextKey = `request_id`

// recoverer catches panics from the wrapped handler, logs them and responds
// with 500 instead of letting the connection die uncleanly. Should be the
// outermost middleware so it can catch panics from everything inside it.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in request handler", "error", rec)
				http.Error(w, msgInternalError, http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps a ResponseWriter to capture the status code written,
// so logger can log it after the handler has already written the response.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records code before delegating to the wrapped ResponseWriter.
func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the wrapped ResponseWriter. http.ResponseController walks the
// Unwrap chain to reach the writer that actually owns the connection, so
// without this method handlers cannot adjust per-request read/write deadlines.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// logger logs one line per request: method, path, status, request ID and
// duration. Relies on requestID having already populated the context.
func logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		id, _ := r.Context().Value(requestIDKey).(string)
		next.ServeHTTP(rec, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"request_id", id,
			"duration", time.Since(start))
	})

}

// requestID generates a UUIDv7 per request, stores it in the request
// context under requestIDKey and echoes it back as the X-Request-ID header.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewV7()
		if err != nil {
			slog.Error("failed to generate new UUID", "error", err)
			http.Error(w, msgInternalError, http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), requestIDKey, id.String())
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id.String())
		next.ServeHTTP(w, r)

	})

}
