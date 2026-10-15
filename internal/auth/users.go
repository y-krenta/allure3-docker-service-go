package auth

import (
	"crypto/rand"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// user is one account as Users keeps it: never the password, only its hash.
type user struct {
	role Role
	hash []byte
}

// Users checks login credentials against the accounts given at startup. It
// is filled by Add before the server starts and only read afterwards, so
// concurrent Checks need no lock.
type Users struct {
	byName map[string]user
	dummy  []byte
}

// NewUsers returns an empty Users. It hashes a random string at the cost Add
// uses, so that Check on an unknown name spends as long in bcrypt as on a
// known one; that costs one bcrypt hash at startup.
func NewUsers() (*Users, error) {
	dummy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("dummy password hash: %w", err)
	}
	return &Users{byName: make(map[string]user), dummy: dummy}, nil
}

// Add adds the account name with role. password is either plain text, hashed
// here, or a bcrypt hash ("$2..."), kept as it is, so an operator need not
// put the password itself in the environment. A password over bcrypt's 72
// bytes or a malformed hash is an error; the error never quotes the value.
func (u *Users) Add(name, password string, role Role) error {
	var hash []byte

	if strings.HasPrefix(password, "$2") {
		hash = []byte(password)

		if _, err := bcrypt.Cost(hash); err != nil {
			return fmt.Errorf("password of %s: %w", name, err)
		}
	} else {
		var err error
		hash, err = bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("password of %s: %w", name, err)
		}
	}

	u.byName[name] = user{role: role, hash: hash}
	return nil
}

// Check reports the role of name if password is its password. A wrong
// password and an unknown name fail alike and take as long: both cost one
// bcrypt comparison, so neither the answer nor its timing tells whether the
// name exists.
func (u *Users) Check(name, password string) (Role, bool) {
	entry, found := u.byName[name]
	if !found {
		_ = bcrypt.CompareHashAndPassword(u.dummy, []byte(password))
		return "", false
	}

	if bcrypt.CompareHashAndPassword(entry.hash, []byte(password)) != nil {
		return "", false
	}

	return entry.role, true
}
