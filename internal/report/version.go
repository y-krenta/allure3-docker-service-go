package report

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Version asks the Allure CLI what version it is and returns the answer with
// the trailing newline every CLI prints stripped off. It shells out rather
// than reading an ALLURE_VERSION file the way the Python service did: that
// file records what the image was built with, which is not necessarily what is
// installed now.
//
// It takes no project lock. The question is about the binary, not about any
// project, so nothing it reads can be swapped out from under it by a build.
//
// Callers are expected to ask once, at startup, and keep the answer: the
// binary is resolved when the process starts and never re-resolved, so the
// reply cannot change while it runs, and each call costs a JVM start.
//
// A non-zero exit carries the CLI'"'"'s stderr into the returned error - without it
// a refusal to start reports nothing but "exit status 1", and the reason a
// deployment will not come up is exactly what stderr holds. It is not
// truncated as runAllure truncates its own: --version prints a line, not the
// megabytes a failing build can produce.
//
// cmd.WaitDelay is set so a CLI that hangs is still bounded by ctx in
// practice, not just in principle: cmd.Output captures stdout through a pipe,
// and a killed process does not by itself make Wait() return if a grandchild
// it spawned inherited that pipe's write end and is still holding it open.
// See cmdTimeout for the full reasoning.
func (g *Generator) Version(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, g.allureBin, "--version")
	cmd.WaitDelay = cmdTimeout
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf(
				"running allure --version: %w, stderr: %s",
				err,
				strings.TrimSpace(string(ee.Stderr)),
			)
		}
		return "", fmt.Errorf("running allure --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
