package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
)

// maxAuthBodyBytes caps the body of the auth endpoints. A name and a
// password, or a refresh token, fit in a few hundred bytes, and bcrypt reads
// only 72 of a password; the endpoints are open before any login, so nothing
// larger is read at all.
const maxAuthBodyBytes = 4 << 10

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
//
// The body is read by readJSON, so one not declared as application/json is a
// 415 before it is read. That closes login CSRF: another site's form can post
// the same JSON as text/plain without a CORS preflight and log the visitor in
// as the attacker, but a browser sends application/json across origins only
// after a preflight, which this service never answers.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, maxAuthBodyBytes, &req) {
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
