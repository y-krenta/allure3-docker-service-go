package report

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersion(t *testing.T) {
	t.Run("returns what the CLI printed, without the trailing newline", func(t *testing.T) {
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\necho '3.14.3'\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		got, err := g.Version(t.Context())

		require.NoError(t, err)
		assert.Equal(t, "3.14.3", got)
	})

	t.Run("--version is the only argument passed", func(t *testing.T) {
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		got, err := g.Version(t.Context())

		require.NoError(t, err)
		assert.Equal(t, "--version", got)
	})

	t.Run("a non-zero exit carries the CLI's stderr", func(t *testing.T) {
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\necho 'Error occurred during initialization of VM' >&2\nexit 3\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		_, err := g.Version(t.Context())

		require.Error(t, err)
		assert.ErrorContains(t, err, "initialization of VM")
		var ee *exec.ExitError
		assert.ErrorAs(t, err, &ee)
	})

	t.Run("a missing CLI is reported, not panicked on", func(t *testing.T) {
		g := New("unused-dir", "no-such-allure-binary", testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		_, err := g.Version(t.Context())

		assert.ErrorIs(t, err, exec.ErrNotFound)
	})

	t.Run("a hung CLI is killed when the context expires", func(t *testing.T) {
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\nsleep 30\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := g.Version(ctx)

		require.Error(t, err)
		assert.Less(t, time.Since(start), 5*time.Second, "want the CLI killed with the context")
	})
}
