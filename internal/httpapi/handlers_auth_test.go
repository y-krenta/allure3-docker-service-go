package httpapi

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newAuthServer(t *testing.T) (*Server, *auth.Tokens) {
	t.Helper()

	users, err := auth.NewUsers()
	require.NoError(t, err)
	require.NoError(t, users.Add("admin", "admin-pass", auth.RoleAdmin))
	require.NoError(t, users.Add("viewer", "viewer-pass", auth.RoleViewer))
	tokens := auth.NewTokens([]byte(testSecret), 15*time.Minute, 720*time.Hour)

	s, _ := newTestServer(t)
	s.EnableAuth(users, tokens, false)
	return s, tokens
}

func postAs(h http.HandlerFunc, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func postLogin(s *Server, body string) *httptest.ResponseRecorder {
	return postAs(s.login, "application/json", body)
}

func postRefresh(s *Server, body string) *httptest.ResponseRecorder {
	return postAs(s.refresh, "application/json", body)
}

func refreshBody(token string) string {
	return fmt.Sprintf(`{"refresh_token":%q}`, token)
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
			assert.Equal(t, "application/json", w.Result().Header.Get("Content-Type"))
			assert.Equal(t, "no-store", w.Result().Header.Get("Cache-Control"))

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
		assert.Equal(t, "application/json", w.Result().Header.Get("Content-Type"), body)
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
// further: the endpoint is open to anyone, before any login. Neither carries a
// token, not even when the right name and password decoded before the error.
func TestLoginRejectsUnreadableBodies(t *testing.T) {
	s, _ := newAuthServer(t)

	tests := []struct {
		name string
		body string
		want int
	}{
		{"truncated JSON", `{"username":`, http.StatusBadRequest},
		{"not JSON", `username=admin&password=admin-pass`, http.StatusBadRequest},
		{"the right credentials and a field of the wrong type", `{"username":"admin","password":"admin-pass","username":1}`, http.StatusBadRequest},
		{"huge body", `{"username":"admin","password":"` + strings.Repeat("p", 1<<20) + `"}`, http.StatusRequestEntityTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postLogin(s, tt.body)

			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "token")
		})
	}
}

// TestAuthIsRoutedOnlyWithAuth checks that the router serves POST /auth/login
// and POST /auth/refresh once EnableAuth has been called, and without it
// answers 404 instead of reaching a handler that has no users to check
// against.
func TestAuthIsRoutedOnlyWithAuth(t *testing.T) {
	enabled, tokens := newAuthServer(t)
	disabled, _ := newTestServer(t)
	refresh, err := tokens.Issue("admin", auth.RoleAdmin, auth.Refresh)
	require.NoError(t, err)

	routes := []struct {
		target string
		body   string
	}{
		{"/auth/login", `{"username":"admin","password":"admin-pass"}`},
		{"/auth/refresh", refreshBody(refresh)},
	}
	servers := []struct {
		name string
		s    *Server
		want int
	}{
		{"enabled", enabled, http.StatusOK},
		{"disabled", disabled, http.StatusNotFound},
	}

	for _, rt := range routes {
		for _, srv := range servers {
			t.Run(srv.name+" "+rt.target, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPost, rt.target, strings.NewReader(rt.body))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()

				srv.s.Routes().ServeHTTP(w, r)

				assert.Equal(t, srv.want, w.Code, w.Body.String())
			})
		}
	}
}

// TestLoginRequiresJSON checks that the right name and password are refused
// with 415 - one JSON error and no token - unless the body is declared as
// JSON, with a charset or without, in a header that parses: another site's
// form can post the same bytes as text/plain with no CORS preflight, and would
// log the visitor in as whoever the attacker chose.
func TestLoginRequiresJSON(t *testing.T) {
	s, _ := newAuthServer(t)
	body := `{"username":"admin","password":"admin-pass"}`

	tests := []struct {
		contentType string
		want        int
	}{
		{"application/json", http.StatusOK},
		{"application/json; charset=utf-8", http.StatusOK},
		{"text/plain", http.StatusUnsupportedMediaType},
		{"application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"multipart/form-data; boundary=x", http.StatusUnsupportedMediaType},
		{"application/json; charset", http.StatusUnsupportedMediaType},
		{"", http.StatusUnsupportedMediaType},
	}

	for _, tt := range tests {
		t.Run(tt.contentType, func(t *testing.T) {
			w := postAs(s.login, tt.contentType, body)

			assert.Equal(t, tt.want, w.Code, w.Body.String())
			if tt.want != http.StatusOK {
				assert.NotContains(t, w.Body.String(), "token")
				var resp struct {
					Error string `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
			}
		})
	}
}

// TestRequireRole checks that the role middleware lets a request through only
// with an access token of an allowed role in a Bearer header: any other token,
// or none, is a 401 announcing the Bearer scheme, a valid token of another
// role is a 403, and neither reaches the handler.
func TestRequireRole(t *testing.T) {
	s, tokens := newAuthServer(t)
	expired := auth.NewTokens([]byte(testSecret), -time.Minute, 720*time.Hour)
	foreign := auth.NewTokens([]byte("fedcba9876543210fedcba9876543210"), 15*time.Minute, 720*time.Hour)

	issue := func(tk *auth.Tokens, role auth.Role, typ auth.TokenType) string {
		raw, err := tk.Issue("someone", role, typ)
		require.NoError(t, err)
		return raw
	}
	admin := issue(tokens, auth.RoleAdmin, auth.Access)
	viewer := issue(tokens, auth.RoleViewer, auth.Access)

	write := []auth.Role{auth.RoleAdmin}
	read := []auth.Role{auth.RoleAdmin, auth.RoleViewer}

	tests := []struct {
		name          string
		authorization string
		allowed       []auth.Role
		want          int
	}{
		{"admin on a write route", "Bearer " + admin, write, http.StatusOK},
		{"admin on a read route", "Bearer " + admin, read, http.StatusOK},
		{"viewer on a read route", "Bearer " + viewer, read, http.StatusOK},
		{"scheme in lower case", "bearer " + admin, write, http.StatusOK},
		{"viewer on a write route", "Bearer " + viewer, write, http.StatusForbidden},
		{"no header", "", read, http.StatusUnauthorized},
		{"token without a scheme", admin, read, http.StatusUnauthorized},
		{"basic credentials", "Basic YWRtaW46YWRtaW4tcGFzcw==", read, http.StatusUnauthorized},
		{"token under another scheme", "Token " + admin, read, http.StatusUnauthorized},
		{"scheme without a token", "Bearer ", read, http.StatusUnauthorized},
		{"not a token", "Bearer not-a-jwt", read, http.StatusUnauthorized},
		{"refresh token", "Bearer " + issue(tokens, auth.RoleAdmin, auth.Refresh), read, http.StatusUnauthorized},
		{"expired token", "Bearer " + issue(expired, auth.RoleAdmin, auth.Access), read, http.StatusUnauthorized},
		{"token signed with another key", "Bearer " + issue(foreign, auth.RoleAdmin, auth.Access), read, http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			r := httptest.NewRequest(http.MethodGet, "/projects", nil)
			if tt.authorization != "" {
				r.Header.Set("Authorization", tt.authorization)
			}
			w := httptest.NewRecorder()

			s.requireRole(tt.allowed...)(next).ServeHTTP(w, r)

			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.Equal(t, tt.want == http.StatusOK, called, "handler reached")
			if tt.want == http.StatusUnauthorized {
				assert.True(t, strings.HasPrefix(w.Result().Header.Get("WWW-Authenticate"), "Bearer"), w.Result().Header)
			}
			if tt.want != http.StatusOK {
				assert.Equal(t, "application/json", w.Result().Header.Get("Content-Type"))
			}
		})
	}
}

// TestRoutesRequireRoles checks that with auth enabled each route lets in only
// the callers its group allows: health and login anyone, every other GET a
// viewer or an admin, every POST and DELETE an admin alone - and that with
// public reads every GET is open to anyone while writing still takes an
// admin. The table has to name every route the router serves, so that a new
// one cannot land outside the groups unnoticed.
func TestRoutesRequireRoles(t *testing.T) {
	const (
		public = "public"
		read   = "read"
		write  = "write"
	)
	routes := map[string]string{
		"GET /health":                       public,
		"POST /auth/login":                  public,
		"POST /auth/refresh":                public,
		"GET /projects":                     read,
		"GET /config":                       read,
		"GET /version":                      read,
		"GET /projects/{id}":                read,
		"GET /projects/{id}/reports/*":      read,
		"GET /projects/{id}/generation":     read,
		"GET /projects/{id}/report/export":  read,
		"GET /projects/{id}/latest-report":  read,
		"POST /projects":                    write,
		"POST /projects/{id}/results":       write,
		"POST /projects/{id}/generation":    write,
		"POST /projects/{id}/history/clean": write,
		"POST /projects/{id}/history/seed":  write,
		"DELETE /projects/{id}":             write,
		"DELETE /projects/{id}/results":     write,
	}
	modes := []struct {
		name        string
		publicReads bool
		anonymous   []string
	}{
		{"reads need a token", false, []string{public}},
		{"reads are public", true, []string{public, read}},
	}
	paths := strings.NewReplacer("{id}", "demo", "*", "latest/index.html")

	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			s, tokens := newAuthServer(t)
			s.EnableAuth(s.users, tokens, m.publicReads)
			s.reports = &stubGenerator{}
			h := s.Routes()

			var served []string
			require.NoError(t, chi.Walk(h.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
				served = append(served, method+" "+route)
				return nil
			}))
			require.ElementsMatch(t, slices.Collect(maps.Keys(routes)), served)

			issue := func(role auth.Role) string {
				raw, err := tokens.Issue("someone", role, auth.Access)
				require.NoError(t, err)
				return raw
			}
			callers := []struct {
				name    string
				token   string
				allowed []string
			}{
				{"anonymous", "", m.anonymous},
				{"viewer", issue(auth.RoleViewer), []string{public, read}},
				{"admin", issue(auth.RoleAdmin), []string{public, read, write}},
			}

			for route, group := range routes {
				method, path, _ := strings.Cut(route, " ")
				for _, c := range callers {
					t.Run(c.name+" "+route, func(t *testing.T) {
						r := httptest.NewRequest(method, paths.Replace(path), nil)
						if c.token != "" {
							r.Header.Set("Authorization", "Bearer "+c.token)
						}
						w := httptest.NewRecorder()

						h.ServeHTTP(w, r)

						denied := w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden
						assert.Equal(t, !slices.Contains(c.allowed, group), denied, "%d %s", w.Code, w.Body.String())
					})
				}
			}
		})
	}
}

// TestRefreshIssuesAccessToken checks that a valid refresh token gets a new
// access token for its user, with the role that user is configured with now
// rather than the one the token was issued with, the access token's lifetime
// in seconds, no new refresh token, and a response no cache may keep.
func TestRefreshIssuesAccessToken(t *testing.T) {
	tests := []struct {
		name     string
		user     string
		issuedAs auth.Role
		want     auth.Role
	}{
		{"admin", "admin", auth.RoleAdmin, auth.RoleAdmin},
		{"viewer", "viewer", auth.RoleViewer, auth.RoleViewer},
		{"role changed since", "viewer", auth.RoleAdmin, auth.RoleViewer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, tokens := newAuthServer(t)
			refresh, err := tokens.Issue(tt.user, tt.issuedAs, auth.Refresh)
			require.NoError(t, err)

			w := postRefresh(s, refreshBody(refresh))

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			assert.Equal(t, "application/json", w.Result().Header.Get("Content-Type"))
			assert.Equal(t, "no-store", w.Result().Header.Get("Cache-Control"))

			var keys map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &keys), w.Body.String())
			assert.ElementsMatch(t, []string{"access_token", "expires_in"}, slices.Collect(maps.Keys(keys)))

			var resp loginResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
			assert.Equal(t, 900, resp.ExpiresIn)

			access, err := tokens.Parse(resp.AccessToken, auth.Access)
			require.NoError(t, err)
			assert.Equal(t, tt.user, access.Subject)
			assert.Equal(t, tt.want, access.Role)
		})
	}
}

// TestRefreshRejectsBadTokens checks that anything but a valid refresh token
// of a user still configured - an access token, an expired or forged one, a
// removed user's, none at all - gets the same 401 with a JSON error and no
// access token, so the answer does not tell which check failed.
func TestRefreshRejectsBadTokens(t *testing.T) {
	s, tokens := newAuthServer(t)
	expired := auth.NewTokens([]byte(testSecret), 15*time.Minute, -time.Minute)
	foreign := auth.NewTokens([]byte("fedcba9876543210fedcba9876543210"), 15*time.Minute, 720*time.Hour)

	issue := func(tk *auth.Tokens, user string, typ auth.TokenType) string {
		raw, err := tk.Issue(user, auth.RoleAdmin, typ)
		require.NoError(t, err)
		return raw
	}

	tests := []struct {
		name string
		body string
	}{
		{"access token", refreshBody(issue(tokens, "admin", auth.Access))},
		{"expired", refreshBody(issue(expired, "admin", auth.Refresh))},
		{"signed with another key", refreshBody(issue(foreign, "admin", auth.Refresh))},
		{"removed user", refreshBody(issue(tokens, "ghost", auth.Refresh))},
		{"not a token", refreshBody("not-a-jwt")},
		{"no token", `{}`},
	}

	var answers []string
	for _, tt := range tests {
		w := postRefresh(s, tt.body)

		assert.Equal(t, http.StatusUnauthorized, w.Code, tt.name)
		assert.Equal(t, "application/json", w.Result().Header.Get("Content-Type"), tt.name)
		var resp struct {
			Error string `json:"error"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
		assert.NotEmpty(t, resp.Error, tt.name)
		assert.NotContains(t, w.Body.String(), "access_token", tt.name)
		answers = append(answers, w.Body.String())
	}

	for i, answer := range answers {
		assert.Equal(t, answers[0], answer, "%s answered unlike %s", tests[i].name, tests[0].name)
	}
}

// TestRefreshRejectsUnreadableBodies checks that refresh reads its body like
// login: 415 unless it is declared as JSON, 413 when far too large, 400 when
// it does not decode - and none of them carries an access token, not even when
// a valid refresh token decoded before the error.
func TestRefreshRejectsUnreadableBodies(t *testing.T) {
	s, tokens := newAuthServer(t)
	refresh, err := tokens.Issue("admin", auth.RoleAdmin, auth.Refresh)
	require.NoError(t, err)

	tests := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"text/plain", "text/plain", refreshBody(refresh), http.StatusUnsupportedMediaType},
		{"huge body", "application/json", `{"refresh_token":"` + strings.Repeat("t", 1<<20) + `"}`, http.StatusRequestEntityTooLarge},
		{"a valid token and a field of the wrong type", "application/json", fmt.Sprintf(`{"refresh_token":%q,"refresh_token":1}`, refresh), http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postAs(s.refresh, tt.contentType, tt.body)

			assert.Equal(t, tt.want, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "access_token")
		})
	}
}
