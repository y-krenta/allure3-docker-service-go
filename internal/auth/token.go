// Package auth issues and verifies the service's JWTs. Tokens are HS256,
// signed with JWT_SECRET_KEY; the server keeps no session, so anything that
// holds a token with a valid signature and an unexpired exp is believed.
package auth

import (
	"fmt"
	"time"
	"uuid"

	"github.com/golang-jwt/jwt/v5"
)

// Role is what a user may do: an admin reads and writes, a viewer only reads.
type Role string

const (
	RoleAdmin  Role = "admin"
	RoleViewer Role = "viewer"
)

// TokenType tells an access token, sent with every request, from a refresh
// token, good only for getting a new access token. It travels in the typ
// claim.
type TokenType string

const (
	Access  TokenType = "access"
	Refresh TokenType = "refresh"
)

// Claims is a token's payload. Anyone holding the token can decode it, so it
// carries nothing secret. RegisteredClaims is embedded by value: as a pointer
// it would be nil when parsing and jwt would panic on it.
type Claims struct {
	Role Role      `json:"role"`
	Type TokenType `json:"typ"`
	jwt.RegisteredClaims
}

// Tokens issues and parses tokens with one signing key.
type Tokens struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewTokens returns a Tokens signing with secret and giving access and
// refresh tokens the lifetimes accessTTL and refreshTTL.
func NewTokens(secret []byte, accessTTL, refreshTTL time.Duration) *Tokens {
	return &Tokens{
		secret:     secret,
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// Issue signs a token of type typ for user with role, living for that type's
// TTL. Every token gets its own jti, so that logout can revoke exactly one.
// An unknown typ is an error rather than a token with a guessed lifetime.
func (t *Tokens) Issue(user string, role Role, typ TokenType) (string, error) {
	var ttl time.Duration
	switch typ {
	case Access:
		ttl = t.accessTTL
	case Refresh:
		ttl = t.refreshTTL
	default:
		return "", fmt.Errorf("unknown token type: %q", typ)
	}

	id := uuid.NewV7()
	now := time.Now()
	claims := Claims{
		Role:      role,
		Type:      typ,
		Subject:   user,
		ID:        id.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}

	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// Parse verifies raw and returns its claims if it is a token of type want. It
// accepts only HS256, whatever alg the token's header names - trusting that
// header is the classic JWT hole - and checks the signature and exp. The typ
// check keeps a refresh token, valid for weeks, from passing as an access
// token. On any error it returns zero Claims, never those of a token it did
// not accept.
func (t *Tokens) Parse(raw string, want TokenType) (Claims, error) {
	var c Claims
	_, err := jwt.ParseWithClaims(raw, &c, func(*jwt.Token) (any, error) {
		return t.secret, nil
	},
		jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return Claims{}, err
	}
	if c.Type != want {
		return Claims{}, fmt.Errorf("token type %q, want %q", c.Type, want)
	}
	return c, nil
}
