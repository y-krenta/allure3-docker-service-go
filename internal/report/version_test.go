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
		// The script prints with a newline because every real CLI does,
		// and stripping it is the whole reason this lives in a tested
		// package instead of in main: a version still carrying "\n" is
		// valid JSON and wrong on the wire.
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\necho '3.14.3'\n"), testHistoryLimit, testBaseURL)

		got, err := g.Version(t.Context())
		if err != nil {
			t.Fatalf("Version() = %v", err)
		}
		if got != "3.14.3" {
			t.Errorf("Version() = %q, want %q", got, "3.14.3")
		}
	})

	t.Run("--version is the only argument passed", func(t *testing.T) {
		// A CLI asked the wrong question can still answer something that
		// looks like a version. The script echoes its arguments back, so
		// the assertion is on what the process actually received.
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), testHistoryLimit, testBaseURL)

		got, err := g.Version(t.Context())
		if err != nil {
			t.Fatalf("Version() = %v", err)
		}
		if got != "--version" {
			t.Errorf("args = %q, want %q", got, "--version")
		}
	})

	t.Run("a non-zero exit carries the CLI's stderr", func(t *testing.T) {
		// "exit status 1" alone is what an operator would otherwise get
		// when the service refuses to start, so the message is the point
		// of the branch, not a nicety.
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\necho 'Error occurred during initialization of VM' >&2\nexit 3\n"), testHistoryLimit, testBaseURL)

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
		g := New("unused-dir", "no-such-allure-binary", testHistoryLimit, testBaseURL)

		_, err := g.Version(t.Context())
		if !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("Version() = %v, want an error wrapping exec.ErrNotFound", err)
		}
	})

	t.Run("a hung CLI is killed when the context expires", func(t *testing.T) {
		// Without the context this call would block startup forever, and
		// silently: no port open, no log line, nothing to look at.
		g := New("unused-dir", fakeCLI(t, "#!/bin/sh\nsleep 30\n"), testHistoryLimit, testBaseURL)

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
