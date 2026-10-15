package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
)

func newAuthServer(t *testing.T) (*Server, *auth.Tokens) {
	t.Helper()

	users, err := auth.NewUsers()
	require.NoError(t, err)
	require.NoError(t, users.Add("admin", "admin-pass", auth.RoleAdmin))
	require.NoError(t, users.Add("viewer", "viewer-pass", auth.RoleViewer))
	tokens := auth.NewTokens([]byte("0123456789abcdef0123456789abcdef"), 15*time.Minute, 720*time.Hour)

	s, _ := newTestServer(t)
	s.EnableAuth(users, tokens)
	return s, tokens
}

func postLogin(s *Server, body string) *httptest.ResponseRecorder {
	return callWithPath(s.login, http.MethodPost, "/auth/login", strings.NewReader(body), nil)
}

type loginResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// TestLoginIssuesTokens checks that the right name and password get an access
// and a refresh token for that user and role, the access token's lifetime in
// seconds, and a response no cache may keep.
func TestLoginIssuesTokens(t *testing.T) {
	tests := []struct {
		user, password string
		role           auth.Role
	}{
		{"admin", "admin-pass", auth.RoleAdmin},
		{"viewer", "viewer-pass", auth.RoleViewer},
	}

	for _, tt := range tests {
		t.Run(tt.user, func(t *testing.T) {
			s, tokens := newAuthServer(t)

			w := postLogin(s, fmt.Sprintf(`{"username":%q,"password":%q}`, tt.user, tt.password))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
			assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))

			var resp loginResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
			assert.Equal(t, 900, resp.ExpiresIn)

			access, err := tokens.Parse(resp.AccessToken, auth.Access)
			require.NoError(t, err)
			assert.Equal(t, tt.user, access.Subject)
			assert.Equal(t, tt.role, access.Role)

			refresh, err := tokens.Parse(resp.RefreshToken, auth.Refresh)
			require.NoError(t, err)
			assert.Equal(t, tt.user, refresh.Subject)
			assert.Equal(t, tt.role, refresh.Role)
		})
	}
}

// TestLoginRejectsBadCredentials checks that a wrong password, an unknown name
// and a missing password all get the same 401 with a JSON error and no token,
// so the answer does not tell which names exist.
func TestLoginRejectsBadCredentials(t *testing.T) {
	s, _ := newAuthServer(t)
	bodies := []string{
		`{"username":"admin","password":"wrong-pass"}`,
		`{"username":"root","password":"admin-pass"}`,
		`{"username":"admin"}`,
	}

	var answers []string
	for _, body := range bodies {
		w := postLogin(s, body)

		assert.Equal(t, http.StatusUnauthorized, w.Code, body)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"), body)
		var resp struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
		assert.NotEmpty(t, resp.Error, body)
		assert.NotContains(t, w.Body.String(), "token", body)
		answers = append(answers, w.Body.String())
	}

	assert.Equal(t, answers[0], answers[1], "unknown name answered unlike a wrong password")
	assert.Equal(t, answers[0], answers[2], "missing password answered unlike a wrong password")
}

// TestLoginRejectsUnreadableBodies checks that a body that is not the expected
// JSON is a 400, and one far larger than any name and password a 413 read no
// further: the endpoint is open to anyone, before any login.
func TestLoginRejectsUnreadableBodies(t *testing.T) {
	s, _ := newAuthServer(t)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"truncated JSON", `{"username":`, http.StatusBadRequest},
		{"not JSON", `username=admin&password=admin-pass`, http.StatusBadRequest},
		{"huge body", `{"username":"admin","password":"` + strings.Repeat("p", 1<<20) + `"}`, http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postLogin(s, tt.body)

			assert.Equal(t, tt.want, w.Code, w.Body.String())
		})
	}
}
