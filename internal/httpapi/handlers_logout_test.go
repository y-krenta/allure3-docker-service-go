package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
)

func postLogout(h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestLogoutRevokesRefresh checks that admin and viewer can revoke their
// refresh token while another refresh and an issued access remain usable.
func TestLogoutRevokesRefresh(t *testing.T) {
	for _, user := range []string{"admin", "viewer"} {
		t.Run(user, func(t *testing.T) {
			s, tokens := newAuthServer(t)
			h := s.Routes()
			login := postLogin(s, `{"username":"`+user+`","password":"`+user+`-pass"}`)
			require.Equal(t, http.StatusOK, login.Code)
			var pair loginResponse
			require.NoError(t, json.Unmarshal(login.Body.Bytes(), &pair))
			claims, err := tokens.Parse(pair.RefreshToken, auth.Refresh)
			require.NoError(t, err)
			role, ok := s.users.Role(user)
			require.True(t, ok)
			other, err := tokens.Issue(user, role, auth.Refresh)
			require.NoError(t, err)

			w := postLogout(h, "application/json", refreshBody(pair.RefreshToken))

			require.Equal(t, http.StatusNoContent, w.Code)
			assert.Empty(t, w.Body.String())
			revoked, err := s.denylist.Revoked(claims.ID)
			require.NoError(t, err)
			assert.True(t, revoked)
			assert.Equal(t, http.StatusUnauthorized, postRefresh(s, refreshBody(pair.RefreshToken)).Code)
			assert.Equal(t, http.StatusOK, postRefresh(s, refreshBody(other)).Code)

			r := httptest.NewRequest(http.MethodGet, "/version", nil)
			r.Header.Set("Authorization", "Bearer "+pair.AccessToken)
			access := httptest.NewRecorder()
			h.ServeHTTP(access, r)
			assert.Equal(t, http.StatusOK, access.Code)
		})
	}
}

// TestLogoutRejectsBadTokens checks that missing, malformed, expired, forged
// and access tokens receive the same JSON 401 without being revoked.
func TestLogoutRejectsBadTokens(t *testing.T) {
	s, tokens := newAuthServer(t)
	h := s.Routes()
	expired := auth.NewTokens([]byte(testSecret), 15*time.Minute, -time.Minute)
	foreign := auth.NewTokens([]byte("fedcba9876543210fedcba9876543210"), 15*time.Minute, 720*time.Hour)
	issue := func(tk *auth.Tokens, typ auth.TokenType) string {
		raw, err := tk.Issue("admin", auth.RoleAdmin, typ)
		require.NoError(t, err)
		return raw
	}
	tests := []struct {
		name string
		raw  string
	}{
		{"missing", ""},
		{"malformed", "not-a-jwt"},
		{"expired", issue(expired, auth.Refresh)},
		{"foreign signature", issue(foreign, auth.Refresh)},
		{"access token", issue(tokens, auth.Access)},
	}

	var firstBody string
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postLogout(h, "application/json", refreshBody(tt.raw))

			require.Equal(t, http.StatusUnauthorized, w.Code)
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			var resp struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.NotEmpty(t, resp.Error)
			assert.NotContains(t, w.Body.String(), "access_token")
			if firstBody == "" {
				firstBody = w.Body.String()
			}
			assert.Equal(t, firstBody, w.Body.String())
		})
	}

	accessClaims, err := tokens.Parse(tests[4].raw, auth.Access)
	require.NoError(t, err)
	revoked, err := s.denylist.Revoked(accessClaims.ID)
	require.NoError(t, err)
	assert.False(t, revoked)
}

// TestLogoutRejectsUnreadableBodies checks that invalid requests are refused
// before a valid refresh token in their body can be revoked.
func TestLogoutRejectsUnreadableBodies(t *testing.T) {
	s, tokens := newAuthServer(t)
	h := s.Routes()
	raw, err := tokens.Issue("admin", auth.RoleAdmin, auth.Refresh)
	require.NoError(t, err)
	claims, err := tokens.Parse(raw, auth.Refresh)
	require.NoError(t, err)
	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"text/plain", "text/plain", refreshBody(raw), http.StatusUnsupportedMediaType},
		{"missing content type", "", refreshBody(raw), http.StatusUnsupportedMediaType},
		{"truncated JSON", "application/json", `{"refresh_token":`, http.StatusBadRequest},
		{"wrong field type", "application/json", `{"refresh_token":123}`, http.StatusBadRequest},
		{"huge body", "application/json", `{"refresh_token":"` + strings.Repeat("t", 1<<20) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postLogout(h, tt.contentType, tt.body)

			require.Equal(t, tt.want, w.Code)
			revoked, err := s.denylist.Revoked(claims.ID)
			require.NoError(t, err)
			assert.False(t, revoked)
		})
	}
}

// TestLogoutFailsWhenDenylistCannotWrite checks that a failed revocation
// returns the generic 500 response rather than announcing a successful logout.
func TestLogoutFailsWhenDenylistCannotWrite(t *testing.T) {
	s, tokens := newAuthServer(t)
	raw, err := tokens.Issue("admin", auth.RoleAdmin, auth.Refresh)
	require.NoError(t, err)
	require.NoError(t, s.denylist.Close())

	w := postLogout(s.Routes(), "application/json", refreshBody(raw))

	require.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, msgInternalError+"\n", w.Body.String())
}

// TestLogoutRequiresEnabledAuth checks that logout exists only when auth
// is enabled and checks its own refresh token without requiring an access.
func TestLogoutRequiresEnabledAuth(t *testing.T) {
	enabled, _ := newAuthServer(t)
	disabled, _ := newTestServer(t)
	assert.Equal(t, http.StatusUnauthorized, postLogout(enabled.Routes(), "application/json", `{}`).Code)
	assert.Equal(t, http.StatusNotFound, postLogout(disabled.Routes(), "application/json", `{}`).Code)
}
