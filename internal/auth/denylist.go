package auth

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

// revokedBucket holds the denylist: the jti of each revoked token, mapped to
// the token's exp as big-endian unix seconds.
var revokedBucket = []byte("revoked")

// Denylist records the refresh tokens logout has revoked, so that a token
// whose signature and exp are still good is refused anyway. It lives in a
// bbolt file and so survives a restart. An entry matters only until the
// token's exp: after that the token is refused as expired. bbolt runs one
// writer at a time and readers alongside it, so a Denylist is safe for
// concurrent use.
type Denylist struct {
	db *bbolt.DB
}

// OpenDenylist opens the denylist at path, creating the file and its
// directory if they are missing. bbolt locks the file for as long as it is
// open. If another process holds it - a second service on the same projects
// root - OpenDenylist waits up to timeout and fails with bbolt's ErrTimeout
// instead of hanging the start.
func OpenDenylist(path string, timeout time.Duration) (*Denylist, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create denylist directory: %w", err)
	}

	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: timeout})
	if err != nil {
		return nil, fmt.Errorf("open denylist %s: %w", path, err)
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(revokedBucket)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create denylist bucket: %w", err)
	}

	return &Denylist{db: db}, nil
}

// Revoke records jti as revoked until exp; revoking it again replaces exp.
// The write is on disk by the time Revoke returns.
func (d *Denylist) Revoke(jti string, exp time.Time) error {
	return d.db.Update(func(tx *bbolt.Tx) error {
		value := binary.BigEndian.AppendUint64(nil, uint64(exp.Unix()))
		return tx.Bucket(revokedBucket).Put([]byte(jti), value)
	})
}

// Revoked reports whether jti has been revoked. With an error the file could
// not be read, and the false that comes with it is no answer: a caller must
// not take it as "not revoked".
func (d *Denylist) Revoked(jti string) (bool, error) {
	var found bool
	err := d.db.View(func(tx *bbolt.Tx) error {
		found = tx.Bucket(revokedBucket).Get([]byte(jti)) != nil
		return nil
	})

	return found, err
}

// Close releases the file and its lock. Revoke and Revoked fail after it.
func (d *Denylist) Close() error {
	return d.db.Close()
}
