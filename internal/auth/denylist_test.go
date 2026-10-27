package auth

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	berrors "go.etcd.io/bbolt/errors"
)

func openTestDenylist(t *testing.T, path string) *Denylist {
	t.Helper()

	d, err := OpenDenylist(path, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestDenylistRevokesOnlyWhatWasRevoked checks that each jti Revoke was
// called on is reported revoked, and that no other jti is - including on a
// denylist nothing has been revoked on yet.
func TestDenylistRevokesOnlyWhatWasRevoked(t *testing.T) {
	d := openTestDenylist(t, filepath.Join(t.TempDir(), "revoked.db"))

	revoked, err := d.Revoked("first")
	require.NoError(t, err)
	assert.False(t, revoked)

	require.NoError(t, d.Revoke("first", time.Now().Add(time.Hour)))
	require.NoError(t, d.Revoke("second", time.Now().Add(time.Hour)))

	for jti, want := range map[string]bool{"first": true, "second": true, "third": false} {
		revoked, err := d.Revoked(jti)
		require.NoError(t, err)
		assert.Equal(t, want, revoked, jti)
	}
}

// TestDenylistSurvivesReopening checks that a revocation is kept in the file:
// a denylist opened again on the same path, as after a restart, still reports
// it.
func TestDenylistSurvivesReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revoked.db")

	d, err := OpenDenylist(path, time.Second)
	require.NoError(t, err)
	require.NoError(t, d.Revoke("first", time.Now().Add(time.Hour)))
	require.NoError(t, d.Close())

	d = openTestDenylist(t, path)
	revoked, err := d.Revoked("first")
	require.NoError(t, err)
	assert.True(t, revoked)
}

// TestOpenDenylistCreatesItsDirectory checks that a missing directory for the
// file is created, as .auth is missing on a fresh projects root.
func TestOpenDenylistCreatesItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".auth", "revoked.db")

	openTestDenylist(t, path)
	assert.FileExists(t, path)
}

// TestOpenDenylistGivesUpOnALockedFile checks that opening a file another
// denylist holds fails with bbolt's timeout error once timeout has passed,
// instead of waiting for the lock forever.
func TestOpenDenylistGivesUpOnALockedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revoked.db")
	openTestDenylist(t, path)

	done := make(chan error, 1)
	go func() {
		d, err := OpenDenylist(path, 100*time.Millisecond)
		if err == nil {
			_ = d.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, berrors.ErrTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("OpenDenylist is still waiting for the lock")
	}
}

// TestDenylistFailsOnceClosed checks that Revoke and Revoked on a closed
// denylist return an error, and Revoked no verdict, rather than pretending
// they worked.
func TestDenylistFailsOnceClosed(t *testing.T) {
	d, err := OpenDenylist(filepath.Join(t.TempDir(), "revoked.db"), time.Second)
	require.NoError(t, err)
	require.NoError(t, d.Close())

	assert.Error(t, d.Revoke("first", time.Now().Add(time.Hour)))

	revoked, err := d.Revoked("first")
	assert.Error(t, err)
	assert.False(t, revoked)
}
