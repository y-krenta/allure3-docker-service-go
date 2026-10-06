package report

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestVersion(t *testing.T) {
	t.Run("returns what the CLI printed, without the trailing newline", func(t *testing.T) {

		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\necho '3.14.3'\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		got, err := g.Version(t.Context())
		if err != nil {
			t.Fatalf("Version() = %v", err)
		}
		if got != "3.14.3" {
			t.Errorf("Version() = %q, want %q", got, "3.14.3")
		}
	})

	t.Run("--version is the only argument passed", func(t *testing.T) {

		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		got, err := g.Version(t.Context())
		if err != nil {
			t.Fatalf("Version() = %v", err)
		}
		if got != "--version" {
			t.Errorf("args = %q, want %q", got, "--version")
		}
	})

	t.Run("a non-zero exit carries the CLI's stderr", func(t *testing.T) {

		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\necho 'Error occurred during initialization of VM' >&2\nexit 3\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		_, err := g.Version(t.Context())
		if err == nil {
			t.Fatal("Version() = nil, want an error")
		}
		if !strings.Contains(err.Error(), "initialization of VM") {
			t.Errorf("error %q does not carry the CLI's stderr", err)
		}
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Errorf("error %q does not wrap *exec.ExitError", err)
		}
	})

	t.Run("a missing CLI is reported, not panicked on", func(t *testing.T) {
		g := New("unused-dir", "no-such-allure-binary", testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		_, err := g.Version(t.Context())
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("Version() = %v, want an error wrapping exec.ErrNotFound", err)
		}
	})

	t.Run("a hung CLI is killed when the context expires", func(t *testing.T) {

		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\nsleep 30\n"), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := g.Version(ctx)
		if err == nil {
			t.Fatal("Version() = nil, want an error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("Version() took %v, want it killed with the context", elapsed)
		}
	})
}
