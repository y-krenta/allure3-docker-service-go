package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/y-krenta/allure3-docker-service-go/internal/auth"
	"github.com/y-krenta/allure3-docker-service-go/internal/config"
)

// The startup line is what an operator reads to learn how builds are capped.
// With BUILD_HEAP_MB=0 there is no cap at all - Node takes a quarter of the
// memory per build - and "0 MB" reads as the opposite: a heap of nothing.
func TestDescribeHeap(t *testing.T) {
	tests := []struct {
		mb   int
		want string
	}{
		{mb: 0, want: "left to Node"},
		{mb: 2048, want: "2048 MB"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, describeHeap(tt.mb), "describeHeap(%d)", tt.mb)
	}
}

func authConfig() config.Config {
	return config.Config{
		SecurityEnable:     true,
		SecurityUser:       "admin",
		SecurityPass:       "admin-pass",
		SecurityViewerUser: "viewer",
		SecurityViewerPass: "viewer-pass",
		JWTSecretKey:       "0123456789abcdef0123456789abcdef",
		AccessTokenTTL:     10 * time.Minute,
		RefreshTokenTTL:    48 * time.Hour,
	}
}

// TestNewAuth checks that main enables auth with what the configuration
// names: the admin and the viewer log in with their passwords and get their
// roles, and tokens are signed with JWT_SECRET_KEY and live for the
// configured TTLs.
func TestNewAuth(t *testing.T) {
	cfg := authConfig()

	users, tokens, err := newAuth(cfg)
	require.NoError(t, err)

	role, ok := users.Check("admin", "admin-pass")
	assert.True(t, ok)
	assert.Equal(t, auth.RoleAdmin, role)
	role, ok = users.Check("viewer", "viewer-pass")
	assert.True(t, ok)
	assert.Equal(t, auth.RoleViewer, role)

	assert.Equal(t, 10*time.Minute, tokens.AccessTTL())
	raw, err := tokens.Issue("admin", auth.RoleAdmin, auth.Refresh)
	require.NoError(t, err)
	claims, err := auth.NewTokens([]byte(cfg.JWTSecretKey), time.Minute, time.Minute).Parse(raw, auth.Refresh)
	require.NoError(t, err)
	assert.Equal(t, 48*time.Hour, claims.ExpiresAt.Sub(claims.IssuedAt.Time))
}

// TestNewAuthWithoutViewer checks that with no viewer configured only the
// admin can log in, and an empty name and password get nobody's role.
func TestNewAuthWithoutViewer(t *testing.T) {
	cfg := authConfig()
	cfg.SecurityViewerUser = ""
	cfg.SecurityViewerPass = ""

	users, _, err := newAuth(cfg)
	require.NoError(t, err)

	_, ok := users.Check("admin", "admin-pass")
	assert.True(t, ok)
	_, ok = users.Check("", "")
	assert.False(t, ok)
}

// TestNewAuthRejectsUnusablePasswords checks that a password bcrypt cannot
// take, the admin's or the viewer's, stops auth from being built, and that
// the error, which main writes to the log, does not quote the password.
func TestNewAuthRejectsUnusablePasswords(t *testing.T) {
	long := strings.Repeat("p", 73)

	tests := []struct {
		name   string
		modify func(*config.Config)
	}{
		{"admin", func(c *config.Config) { c.SecurityPass = long }},
		{"viewer", func(c *config.Config) { c.SecurityViewerPass = long }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := authConfig()
			tt.modify(&cfg)

			_, _, err := newAuth(cfg)

			require.Error(t, err)
			assert.NotContains(t, err.Error(), long)
		})
	}
}
