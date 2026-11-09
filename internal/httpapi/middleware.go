package httpapi

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/httplog/v3"
	"github.com/google/uuid"
	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
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

// requireRole returns middleware that lets a request through to next only with
// an access token of one of the allowed roles, sent as Authorization: Bearer.
// No token, or one that is not a valid access token - forged, expired, a
// refresh token, under another scheme - is a 401 carrying the
// WWW-Authenticate challenge RFC 6750 requires, and does not say which: the
// client has only to log in again. A valid token of another role is a 403,
// which logging in again as the same user would not change.
func (s *Server) requireRole(allowed ...auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, raw, _ := strings.Cut(r.Header.Get("Authorization"), " ")
			claims, err := s.tokens.Parse(raw, auth.Access)

			if err != nil || !strings.EqualFold(scheme, "Bearer") {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, r, http.StatusUnauthorized, "missing or invalid token")
				return
			}

			if !slices.Contains(allowed, claims.Role) {
				writeError(w, r, http.StatusForbidden, "insufficient role")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
