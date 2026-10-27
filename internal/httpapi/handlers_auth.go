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
	RefreshToken string `json:"refresh_token,omitempty"`
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

// refresh handles POST /auth/refresh: a JSON body with a refresh token gets a
// new access token, and no new refresh token - the one sent stays valid until
// it expires. The role comes from the users configured now, not from the
// token, so a role changed since login takes effect on the next refresh and a
// removed user's refresh token stops working rather than living out its
// thirty days.
//
// Every refusal - a bad signature, an expired token, an access token sent as
// a refresh one, a user no longer configured - is the same 401, as in login.
// The body is read by readJSON like login's, and the response is no-store.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !readJSON(w, r, maxAuthBodyBytes, &req) {
		return
	}

	claims, err := s.tokens.Parse(req.RefreshToken, auth.Refresh)
	role, ok := s.users.Role(claims.Subject)

	if err != nil || !ok {
		writeError(w, r, http.StatusUnauthorized, "invalid refresh token")
		return
	}

	access, err := s.tokens.Issue(claims.Subject, role, auth.Access)
	if err != nil {
		slog.Error("failed to issue access token", "err", err)
		http.Error(w, msgInternalError, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r, tokenResponse{
		AccessToken: access,
		ExpiresIn:   int(s.tokens.AccessTTL().Seconds()),
	})
}
