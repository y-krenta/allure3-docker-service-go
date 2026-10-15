package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
)

// maxLoginBytes caps a login body. A name and a password fit in a few
// hundred bytes, and bcrypt reads only 72 of the password; the endpoint is
// open before any login, so nothing larger is read at all.
const maxLoginBytes = 4 << 10

// tokenResponse is the body that hands out tokens. Field names follow
// OAuth 2.0 (RFC 6749, section 5.1): expires_in is the access token's
// lifetime in seconds, so a client knows when to renew without decoding it.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// login handles POST /auth/login: a JSON body with username and password
// gets an access and a refresh token for that user's role. Every failed
// check - wrong password, unknown name, no password - is the same 401, so
// the answer does not tell which names exist. The response is no-store: it
// carries secrets no cache may keep.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(unwrapResponseWriter(w), r.Body, maxLoginBytes)

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if handleMaxBytesError(w, err) {
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	role, ok := s.users.Check(req.Username, req.Password)
	if !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid username or password")
		return
	}

	access, err := s.tokens.Issue(req.Username, role, auth.Access)
	if err != nil {
		slog.Error("failed to issue access token", "err", err)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}

	refresh, err := s.tokens.Issue(req.Username, role, auth.Refresh)
	if err != nil {
		slog.Error("failed to issue refresh token", "err", err)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, tokenResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(s.tokens.AccessTTL().Seconds()),
	})
}
