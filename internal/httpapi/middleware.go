package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/httplog/v3"
	"github.com/google/uuid"
)

// requestID generates a UUIDv7 per request, echoes it back as the
// X-Request-ID header and adds it as request_id to httplog's line for the
// request. It must run inside httplog's middleware: outside it,
// httplog.SetAttrs finds nothing to add to and drops the ID silently.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewV7()
		if err != nil {
			slog.Error("failed to generate new UUID", "error", err)
			http.Error(w, msgInternalError, http.StatusInternalServerError)
			return
		}
		httplog.SetAttrs(r.Context(), slog.String("request_id", id.String()))
		w.Header().Set("X-Request-ID", id.String())
		next.ServeHTTP(w, r)

	})

}
