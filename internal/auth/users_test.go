package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func newTestUsers(t *testing.T) *Users {
	t.Helper()

	users, err := NewUsers()
	require.NoError(t, err)
	return users
}

// TestCheckAcceptsTheRightPassword checks that a user added with a password,
// given as plain text or as a bcrypt hash of it, logs in with that password
// and gets the role they were added with.
func TestCheckAcceptsTheRightPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("viewer-pass"), bcrypt.MinCost)
	require.NoError(t, err)

	users := newTestUsers(t)
	require.NoError(t, users.Add("admin", "admin-pass", RoleAdmin))
	require.NoError(t, users.Add("viewer", string(hash), RoleViewer))

	role, ok := users.Check("admin", "admin-pass")
	assert.True(t, ok)
	assert.Equal(t, RoleAdmin, role)

	role, ok = users.Check("viewer", "viewer-pass")
	assert.True(t, ok)
	assert.Equal(t, RoleViewer, role)
}

// TestCheckRejectsBadCredentials checks that a wrong password, another user's
// password, an unknown name and a stored hash typed in as the password all
// fail alike, with no role.
func TestCheckRejectsBadCredentials(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("viewer-pass"), bcrypt.MinCost)
	require.NoError(t, err)

	users := newTestUsers(t)
	require.NoError(t, users.Add("admin", "admin-pass", RoleAdmin))
	require.NoError(t, users.Add("viewer", string(hash), RoleViewer))

	tests := []struct {
		name, user, password string
	}{
		{"wrong password", "admin", "admin-pas"},
		{"another user's password", "admin", "viewer-pass"},
		{"empty password", "admin", ""},
		{"unknown user", "root", "admin-pass"},
		{"the hash as the password", "viewer", string(hash)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role, ok := users.Check(tt.user, tt.password)

			assert.False(t, ok)
			assert.Empty(t, role)
		})
	}
}

// TestCheckTakesAsLongForAnUnknownUser checks that a login with an unknown
// name costs a bcrypt comparison too. Without one it answers in microseconds
// instead of tens of milliseconds, and the response time tells an attacker
// which names exist.
func TestCheckTakesAsLongForAnUnknownUser(t *testing.T) {
	users := newTestUsers(t)
	require.NoError(t, users.Add("admin", "admin-pass", RoleAdmin))

	timed := func(name string) time.Duration {
		start := time.Now()
		users.Check(name, "wrong-pass")
		return time.Since(start)
	}
	known := timed("admin")
	unknown := timed("root")

	assert.Greater(t, unknown, known/4, "unknown user took %v, a known one with a wrong password %v", unknown, known)
}

// TestAddRejectsUnusablePasswords checks that Add refuses what bcrypt cannot
// check: a password longer than bcrypt's 72 bytes, which it would otherwise
// cut short, and a value that looks like a bcrypt hash but is not one, which
// would otherwise become a literal password nobody meant to set.
func TestAddRejectsUnusablePasswords(t *testing.T) {
	tests := []struct {
		name, password string
	}{
		{"73 bytes", strings.Repeat("p", 73)},
		{"broken hash", "$2a$10$not-a-real-hash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, newTestUsers(t).Add("admin", tt.password, RoleAdmin))
		})
	}
}
