package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func newTestTokens() *Tokens {
	return NewTokens([]byte(testSecret), 15*time.Minute, 720*time.Hour)
}

// TestIssuedTokenParsesBack checks that a token Issue signs is read back by
// Parse with the user, role and type it was issued for, and lives as long as
// the TTL of its type.
func TestIssuedTokenParsesBack(t *testing.T) {
	tests := []struct {
		typ TokenType
		ttl time.Duration
	}{
		{Access, 15 * time.Minute},
		{Refresh, 720 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			tokens := newTestTokens()
			issued := time.Now()

			raw, err := tokens.Issue("admin", RoleAdmin, tt.typ)
			require.NoError(t, err)
			got, err := tokens.Parse(raw, tt.typ)
			require.NoError(t, err)

			assert.Equal(t, "admin", got.Subject)
			assert.Equal(t, RoleAdmin, got.Role)
			assert.Equal(t, tt.typ, got.Type)
			require.NotNil(t, got.IssuedAt)
			assert.WithinDuration(t, issued, got.IssuedAt.Time, 2*time.Second)
			require.NotNil(t, got.ExpiresAt)
			assert.WithinDuration(t, issued.Add(tt.ttl), got.ExpiresAt.Time, 2*time.Second)
		})
	}
}

// TestIssueGivesEveryTokenItsOwnID checks that two tokens for the same user
// carry different jti: logout revokes a token by its jti, and a shared one
// would log out every session of that user at once.
func TestIssueGivesEveryTokenItsOwnID(t *testing.T) {
	tokens := newTestTokens()

	first, err := tokens.Issue("admin", RoleAdmin, Refresh)
	require.NoError(t, err)
	second, err := tokens.Issue("admin", RoleAdmin, Refresh)
	require.NoError(t, err)

	a, err := tokens.Parse(first, Refresh)
	require.NoError(t, err)
	b, err := tokens.Parse(second, Refresh)
	require.NoError(t, err)

	assert.NotEmpty(t, a.ID)
	assert.NotEqual(t, a.ID, b.ID)
}

// TestIssueRejectsUnknownType checks that Issue refuses a token type it has no
// lifetime for instead of signing it with one of the others.
func TestIssueRejectsUnknownType(t *testing.T) {
	_, err := newTestTokens().Issue("admin", RoleAdmin, TokenType("session"))

	assert.Error(t, err)
}

func sign(t *testing.T, method jwt.SigningMethod, key []byte, typ TokenType) string {
	t.Helper()

	now := time.Now()
	raw, err := jwt.NewWithClaims(method, Claims{
		Role: RoleAdmin,
		Type: typ,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "admin",
			ID:        "forged",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	}).SignedString(key)
	require.NoError(t, err)
	return raw
}

// TestParseRejects checks that Parse refuses every token it must not believe:
// one of the other type, so a refresh token never authorizes a request; one
// signed with another algorithm, even with our key; one signed with another
// key; an expired one; and a string that is not a token at all.
func TestParseRejects(t *testing.T) {
	tokens := newTestTokens()
	issue := func(t *testing.T, tk *Tokens, typ TokenType) string {
		raw, err := tk.Issue("admin", RoleAdmin, typ)
		require.NoError(t, err)
		return raw
	}

	tests := []struct {
		name string
		raw  func(t *testing.T) string
		want TokenType
	}{
		{"refresh as access", func(t *testing.T) string { return issue(t, tokens, Refresh) }, Access},
		{"access as refresh", func(t *testing.T) string { return issue(t, tokens, Access) }, Refresh},
		{"HS512 with our key", func(t *testing.T) string { return sign(t, jwt.SigningMethodHS512, []byte(testSecret), Access) }, Access},
		{"another key", func(t *testing.T) string {
			return sign(t, jwt.SigningMethodHS256, []byte("another-key-another-key-another-k"), Access)
		}, Access},
		{"expired", func(t *testing.T) string {
			return issue(t, NewTokens([]byte(testSecret), -time.Minute, time.Hour), Access)
		}, Access},
		{"not a token", func(*testing.T) string { return "not-a-token" }, Access},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tokens.Parse(tt.raw(t), tt.want)

			assert.Error(t, err)
			assert.Equal(t, Claims{}, got)
		})
	}
}

// TestTokenClaimNames checks the claim names on the wire, the part of a token
// anyone can decode: the registered ones and our role and typ, with no other
// field leaking into the payload.
func TestTokenClaimNames(t *testing.T) {
	raw, err := newTestTokens().Issue("viewer", RoleViewer, Access)
	require.NoError(t, err)

	parts := strings.Split(raw, ".")
	require.Len(t, parts, 3, "a signed JWT is header.payload.signature")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))

	assert.ElementsMatch(t, []string{"sub", "role", "typ", "jti", "iat", "exp"}, keys(claims))
	assert.Equal(t, "viewer", claims["sub"])
	assert.Equal(t, "viewer", claims["role"])
	assert.Equal(t, "access", claims["typ"])
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
