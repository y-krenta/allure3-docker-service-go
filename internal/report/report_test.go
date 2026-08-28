package report

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
)

// Shell bodies for the stand-in Allure CLI used by the tests. They are called
// the way runAllure calls the real thing:
//
//	generate <resultsDir> --output <outDir> --config <configPath>
//
// so $4 is the output directory. Keep that position in mind when changing the
// command line: move --output and these bodies write the report somewhere else
// entirely, and every test that reads it fails at once.
//
// The history file is not on the command line at all - generate has no
// --history-path - so a body that needs it reads it out of the config, the way
// the real CLI does. That is what cliFindHistory is for.
const (
	// cliOK mimics a successful build by writing a recognizable report.
	cliOK = "#!/bin/sh\nprintf 'fresh' > \"$4/index.html\"\n"
	// cliFail mimics Allure rejecting the results.
	cliFail = "#!/bin/sh\necho 'boom: broken results' >&2\nexit 3\n"
	// cliFindHistory is the prologue shared by the bodies that touch the
	// history file. It leaves the output directory in $out and the history
	// file named by the config in $hist.
	//
	// $4 is captured before the loop because walking argv with shift destroys
	// the positional parameters. Argv is walked rather than indexed so the
	// assertion survives a reordering of the flags, and the path is dug out of
	// the config with sed because a shell has no JSON parser - which is fine
	// here, since encoding/json writes the file without spaces or escapes.
	cliFindHistory = "#!/bin/sh\n" +
		"out=\"$4\"\n" +
		"cfg=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--config\" ]; then cfg=\"$2\"; fi\n" +
		"  shift\n" +
		"done\n" +
		"hist=$(sed -n 's/.*\"historyPath\":\"\\([^\"]*\\)\".*/\\1/p' \"$cfg\")\n"
	// cliHistory succeeds and appends one line to the history file it was
	// pointed at, the way the real CLI records a run.
	cliHistory = cliFindHistory + "printf 'fresh' > \"$out/index.html\"\necho run >> \"$hist\"\n"
	// cliWreckHistory scribbles over the history file it was pointed at and
	// then fails, standing in for a build killed mid-write.
	cliWreckHistory = cliFindHistory + "printf 'half a lin' >> \"$hist\"\necho 'boom' >&2\nexit 3\n"
	// cliSlow succeeds, but takes long enough that a test can observe the
	// build while it is still running.
	cliSlow = "#!/bin/sh\nsleep 0.5\nprintf 'fresh' > \"$4/index.html\"\n"
	// cliUnarchivable builds a perfectly good report that cannot be copied
	// afterwards: os.CopyFS refuses anything that is not a regular file or a
	// directory, and it refuses it part-way through, having already copied
	// whatever sorted before it. The names put the fifo in the middle of the
	// walk - index.html, m-fifo, z.txt - so the failure lands with some files
	// copied and some not, which is the shape a build killed mid-archive
	// leaves behind.
	cliUnarchivable = "#!/bin/sh\n" +
		"printf 'fresh' > \"$4/index.html\"\n" +
		"mkfifo \"$4/m-fifo\"\n" +
		"printf 'last' > \"$4/z.txt\"\n"
	// cliFloodStderr fails after burying its diagnostic under roughly 9 KB of
	// noise, the way the real CLI does when it chokes on a history file: it
	// echoes the offending line first and only then says what was wrong with
	// it. The two markers sit at either end so a test can tell which end
	// survived truncation.
	cliFloodStderr = "#!/bin/sh\n" +
		"printf 'HEAD-ONLY-MARKER' >&2\n" +
		"i=0\n" +
		"while [ $i -lt 200 ]; do printf '0123456789012345678901234567890123456789' >&2; i=$((i+1)); done\n" +
		"printf 'SyntaxError: TAIL-MARKER' >&2\n" +
		"exit 3\n"
)

// cliRecordArgv returns a CLI body that dumps its argv, one element per line,
// into dumpPath before producing a report the way cliOK does. Recording the
// whole command line rather than reading a fixed position keeps the assertions
// independent of the order runAllure happens to use.
func cliRecordArgv(dumpPath string) string {
	return "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dumpPath + "\"\nprintf 'fresh' > \"$4/index.html\"\n"
}

// cliDumpConfig returns a CLI body that copies the file it was handed after
// --config into dumpPath, then produces a report the way cliOK does.
//
// The copy happens while the CLI is running, which is the whole point: the
// config has to exist and hold the right value at that moment, not merely be
// left lying around in the temp dir once the build is over. Argv is scanned
// rather than indexed so the assertion survives a reordering of the flags.
func cliDumpConfig(dumpPath string) string {
	return "#!/bin/sh\n" +
		"printf 'fresh' > \"$4/index.html\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--config\" ]; then cat \"$2\" > \"" + dumpPath + "\"; fi\n" +
		"  shift\n" +
		"done\n" +
		"exit 0\n"
}

// cliRecordCwd returns a CLI body that writes its own working directory into
// dumpPath before producing a report the way cliOK does. -P resolves symlinks,
// which matters on macOS where the temp dir lives under /var, itself a link to
// /private/var.
func cliRecordCwd(dumpPath string) string {
	return "#!/bin/sh\npwd -P > \"" + dumpPath + "\"\nprintf 'fresh' > \"$4/index.html\"\n"
}

// fakeCLI writes body to an executable file and returns its path, so a test
// can drive Generate without depending on a real Allure installation.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-allure")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake CLI: %v", err)
	}
	return path
}

// readFile reads path or fails the test, for the many assertions that only
// care about a file's content and have nothing useful to say about a read
// error beyond reporting it.
func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// testHistoryLimit is the history limit every generator built by
// newTestGenerator carries. It is deliberately not 25 or any other plausible
// production default: a test asserting on the generated Allure config has to
// prove the number travelled from the caller rather than from a constant
// someone hardcoded on the way.
const testHistoryLimit = 7

// testBaseURL is the public address the service is configured with in tests.
// It has to be absolute: the browser feeds every url the report carries to
// new URL(), which throws on a relative one and takes the page down with it.
const testBaseURL = "https://allure.example.test"

// newTestGenerator returns a Generator rooted at a fresh temp dir, running the
// given CLI, with the given projects already created and holding one result
// file each — a project with an empty results dir is refused before any build
// starts, so tests about building need something to build from.
func newTestGenerator(t *testing.T, allureBin string, projectIDs ...string) *Generator {
	t.Helper()

	dir := t.TempDir()
	for _, id := range projectIDs {
		if err := projects.CreateDir(dir, id); err != nil {
			t.Fatalf("CreateDir(%q) = %v", id, err)
		}
		writeResult(t, dir, id)
	}
	return New(dir, allureBin, testHistoryLimit, testBaseURL)
}

// writeResult drops one Allure result file into the project's results dir.
func writeResult(t *testing.T, baseDir, projectID string) {
	t.Helper()

	path := filepath.Join(projects.ResultsDir(baseDir, projectID), "9f0a1c-result.json")
	if err := os.WriteFile(path, []byte(`{"name":"a test"}`), 0o644); err != nil {
		t.Fatalf("writing result file: %v", err)
	}
}

// writeLatest replaces the project's latest report with one containing body,
// standing in for a report left by an earlier build.
func writeLatest(t *testing.T, g *Generator, projectID, body string) {
	t.Helper()

	latest := projects.LatestReportDir(g.projectsDir, projectID)
	if err := os.MkdirAll(latest, 0o755); err != nil {
		t.Fatalf("creating latest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(latest, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing latest report: %v", err)
	}
}

// readLatest returns the body of the project's current latest report.
func readLatest(t *testing.T, g *Generator, projectID string) string {
	t.Helper()

	path := filepath.Join(projects.LatestReportDir(g.projectsDir, projectID), "index.html")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading latest report: %v", err)
	}
	return string(b)
}

func TestLockForReturnsSameMutexPerProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if a, b := g.lockFor("demo"), g.lockFor("demo"); a != b {
		t.Errorf("lockFor(%q) returned different mutexes on repeated calls", "demo")
	}
	if a, b := g.lockFor("one"), g.lockFor("two"); a == b {
		t.Error("different projects share a mutex, they must not block each other")
	}
}

func TestLockForIsConcurrencySafe(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	// Hammer the registry from many goroutines: -race fails here if mu ever
	// stops guarding the map.
	const goroutines = 50
	done := make(chan *sync.Mutex, goroutines)
	for i := range goroutines {
		go func() {
			g.lockFor(string(rune('a' + i%26))) // churn the map with other keys
			done <- g.lockFor("same")
		}()
	}

	first := <-done
	for range goroutines - 1 {
		if got := <-done; got != first {
			t.Fatal("concurrent lockFor calls produced different mutexes for one project")
		}
	}
}

func TestGenerateRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if err := g.Generate(t.Context(), "../escape"); err == nil {
		t.Fatal("Generate accepted a project ID containing a path traversal")
	}
}

func TestGenerateUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.Generate(t.Context(), "missing")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("Generate(missing) = %v, want ErrProjectNotFound", err)
	}
}

// An empty results directory would build an empty report and publish it over
// the last good one, so both entry points refuse it before touching anything.
func TestEmptyResultsDirIsRefused(t *testing.T) {
	newEmptyProject := func(t *testing.T) *Generator {
		t.Helper()

		dir := t.TempDir()
		if err := projects.CreateDir(dir, "demo"); err != nil {
			t.Fatalf("CreateDir: %v", err)
		}
		return New(dir, fakeCLI(t, cliOK), testHistoryLimit, testBaseURL)
	}

	t.Run("Generate", func(t *testing.T) {
		g := newEmptyProject(t)

		if err := g.Generate(t.Context(), "demo"); !errors.Is(err, ErrNoResults) {
			t.Fatalf("Generate = %v, want ErrNoResults", err)
		}
	})

	t.Run("Start", func(t *testing.T) {
		g := newEmptyProject(t)

		if err := g.Start(t.Context(), "demo"); !errors.Is(err, ErrNoResults) {
			t.Fatalf("Start = %v, want ErrNoResults", err)
		}
		if st, ok := g.Status("demo"); ok {
			t.Fatalf("Status = %+v, want no status recorded for a rejected build", st)
		}
	})

	t.Run("the previous report survives", func(t *testing.T) {
		g := newEmptyProject(t)
		writeLatest(t, g, "demo", "previous")

		if err := g.Generate(t.Context(), "demo"); !errors.Is(err, ErrNoResults) {
			t.Fatalf("Generate = %v, want ErrNoResults", err)
		}
		if got := readLatest(t, g, "demo"); got != "previous" {
			t.Fatalf("latest report = %q, want the previous one left untouched", got)
		}
	})
}

func TestGenerateSerializesSameProject(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Simulate a build in flight by holding the project's lock directly.
	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.Generate(context.Background(), "demo") }()

	select {
	case err := <-done:
		t.Fatalf("Generate returned while the project lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Generate after unlock = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not proceed after the project lock was released")
	}
}

func TestGenerateDoesNotBlockOtherProjects(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "busy", "idle")

	held := g.lockFor("busy")
	held.Lock()
	defer held.Unlock()

	done := make(chan error, 1)
	go func() { done <- g.Generate(context.Background(), "idle") }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Generate(idle) = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate(idle) blocked while an unrelated project was building")
	}
}

func TestGenerateFirstBuildCreatesLatest(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// A brand new project has no latest report yet: the swap must treat the
	// missing directory as normal rather than as a failure.
	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

func TestGenerateReplacesPreviousReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")
	writeLatest(t, g, "demo", "stale")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the newly built %q", got, "fresh")
	}
}

func TestGenerateFailedBuildKeepsPreviousReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFail), "demo")
	writeLatest(t, g, "demo", "stale")

	err := g.Generate(t.Context(), "demo")
	if err == nil {
		t.Fatal("Generate = nil, want an error when the CLI exits non-zero")
	}
	// The CLI's own diagnostics must survive into the error, otherwise a
	// failed build is undebuggable.
	if !strings.Contains(err.Error(), "boom: broken results") {
		t.Errorf("Generate error = %v, want it to carry the CLI stderr", err)
	}
	// Build-then-swap exists for this: a failed build must not destroy the
	// report users are currently browsing.
	if got := readLatest(t, g, "demo"); got != "stale" {
		t.Errorf("latest report = %q, want the previous %q left untouched", got, "stale")
	}
}

// TestGenerateRemovesTempBuildDirs pins down that a build sweeps the staging
// area it inherits: a directory left behind by a build that was killed before
// it could clean up must not survive the next one.
//
// The assertion is about build directories rather than about the temp root
// being empty. The root legitimately keeps scratch files a build needs for its
// whole run - the staged history copy, the generated Allure config - and those
// are cleared by the next build's RemoveAll rather than at the end of this one.
func TestGenerateRemovesTempBuildDirs(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Junk left behind by a build that was killed before it could clean up.
	tmp := projects.TmpRoot(g.projectsDir, "demo")
	if err := os.MkdirAll(filepath.Join(tmp, "build-stale"), 0o755); err != nil {
		t.Fatalf("seeding stale temp dir: %v", err)
	}

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("reading temp root: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "build-") {
			t.Errorf("temp root still holds %q after a build, want every build dir gone", e.Name())
		}
	}
}

func TestRunAllureReportsMissingBinary(t *testing.T) {
	g := newTestGenerator(t, "definitely-not-an-installed-binary", "demo")

	err := g.runAllure(t.Context(), t.TempDir(), t.TempDir(),
		filepath.Join(t.TempDir(), "allurerc.json"))
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("runAllure = %v, want an error wrapping exec.ErrNotFound", err)
	}
}

// TestGenerateInvokesTheGenerateSubcommand pins the subcommand, which is not
// interchangeable with awesome even though both build the same report.
//
// The awesome subcommand throws the configured plugins away - it assigns its
// own single-plugin list over whatever the config named - so under awesome the
// report-url plugin never runs, history entries carry no URL and the trend
// chart's bars stop being clickable. Nothing fails: the build succeeds, the
// report looks right, and the regression is only visible by clicking a bar in
// a browser. Only generate honours the config.
func TestGenerateInvokesTheGenerateSubcommand(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "argv")
	g := newTestGenerator(t, fakeCLI(t, cliRecordArgv(dump)), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("reading recorded argv: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")

	if argv[0] != "generate" {
		t.Errorf("argv = %q, want it to start with the generate subcommand - awesome discards the configured plugins", argv)
	}
}

// TestGenerateStagesHistoryIntoTheConfig follows the history file the report's
// trends depend on. It reaches the CLI through the config rather than a flag,
// and two things about the path are load-bearing.
//
// It must be the staged copy, not the project's own history: the published
// file is replaced only after the report has been moved into place, so a build
// that dies halfway leaves the real history untouched. And it must sit under
// the project's temp root, which the next build wipes.
func TestGenerateStagesHistoryIntoTheConfig(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "config")
	g := newTestGenerator(t, fakeCLI(t, cliDumpConfig(dump)), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	var got struct {
		HistoryPath string `json:"historyPath"`
	}
	if err := json.Unmarshal(readFile(t, dump), &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}

	if got.HistoryPath == "" {
		t.Fatal("config carries no historyPath, so the CLI keeps no history at all")
	}
	if published := projects.HistoryFile(g.projectsDir, "demo"); got.HistoryPath == published {
		t.Errorf("historyPath = %q, want a staged copy rather than the project's own history", got.HistoryPath)
	}
	if tmp := projects.TmpRoot(g.projectsDir, "demo"); !strings.HasPrefix(got.HistoryPath, tmp+string(filepath.Separator)) {
		t.Errorf("historyPath = %q, want it staged under %q", got.HistoryPath, tmp)
	}
}

// TestRunAllureRunsTheCLIFromANeutralDirectory pins the working directory the
// CLI is started in, which is not the cosmetic detail it looks like.
//
// Allure stamps every report with a "ci" block it fills by shelling out to git
// - rev-parse --show-toplevel, rev-parse --abbrev-ref HEAD, remote get-url
// origin - with no directory of its own, so git inherits ours and searches
// upwards for a repository. Started from anywhere inside one, the service
// brands other people's reports with its own repository name, branch and
// remote. Even the project's temp dir is not safe: a projects root that lives
// inside a checkout is still inside it four levels down.
func TestRunAllureRunsTheCLIFromANeutralDirectory(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "cwd")
	g := newTestGenerator(t, fakeCLI(t, cliRecordCwd(dump)), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("reading recorded cwd: %v", err)
	}
	got := strings.TrimSpace(string(raw))

	want, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	if got != want {
		t.Errorf("CLI ran in %q, want the neutral %q - anything else lets git metadata leak into the report", got, want)
	}
}

// TestRunAllureKeepsTheTailOfAFloodedStderr pins down which end of a huge
// stderr survives. It matters because of how the CLI actually fails: fed a
// malformed history file it echoes the whole offending line - tens of
// kilobytes of JSON - and prints the SyntaxError only at the very end. Keeping
// the head would carry 4 KB of that JSON into the error and drop every word
// explaining what went wrong.
func TestRunAllureKeepsTheTailOfAFloodedStderr(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFloodStderr), "demo")

	err := g.Generate(t.Context(), "demo")
	if err == nil {
		t.Fatal("Generate = nil, want the CLI failure")
	}
	got := err.Error()

	if !strings.Contains(got, "TAIL-MARKER") {
		t.Errorf("error = %q, want it to carry the end of stderr, where the CLI puts its diagnostic", got)
	}
	if strings.Contains(got, "HEAD-ONLY-MARKER") {
		t.Error("error carries the start of stderr, so the flood was kept and the diagnostic dropped")
	}
	// The whole error, not just the captured stderr: the wrapping around it is
	// a fixed handful of bytes, so a generous margin still fails loudly if the
	// truncation stops happening at all.
	if len(got) > maxStderrBytes+512 {
		t.Errorf("error is %d bytes, want stderr truncated to about %d", len(got), maxStderrBytes)
	}
}

// TestGenerateInvokesTheCLIWithConfigPath covers the flag that carries
// everything the command line cannot: the retention limit, the history file
// and the plugins. Three things about that file's path are load-bearing.
//
// Its extension must be .json: the CLI dispatches on the extension when it
// loads a config, and anything it does not recognise is skipped in silence,
// exit code 0, no warning, retention quietly off. It must sit under the
// project's temp root, which is wiped at the start of every build - written
// anywhere else it either leaks forever or, in the projects root, shows up in
// listings as a project of its own. And the flag has to be there at all.
func TestGenerateInvokesTheCLIWithConfigPath(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "argv")
	g := newTestGenerator(t, fakeCLI(t, cliRecordArgv(dump)), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("reading recorded argv: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")

	got, ok := flagValue(argv, "--config")
	if !ok {
		t.Fatalf("argv = %q, want it to carry --config", argv)
	}
	if filepath.Ext(got) != ".json" {
		t.Errorf("--config = %q, want a .json file - the CLI ignores any other extension without saying so", got)
	}
	if tmp := projects.TmpRoot(g.projectsDir, "demo"); !strings.HasPrefix(got, tmp+string(filepath.Separator)) {
		t.Errorf("--config = %q, want it written under %q", got, tmp)
	}
}

// TestGenerateWritesTheLimitIntoTheConfig checks the content of that file as
// the CLI sees it: the right key, spelled the way Allure spells it, holding the
// number the generator was built with.
//
// The key is the fragile part. It is a struct tag, so nothing in Go checks it -
// drop the tag and encoding/json happily writes "HistoryLimit", which the CLI
// does not recognise and ignores without complaint, leaving history to grow
// forever.
func TestGenerateWritesTheLimitIntoTheConfig(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "config")
	g := newTestGenerator(t, fakeCLI(t, cliDumpConfig(dump)), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("reading the config the CLI was given: %v", err)
	}

	var got struct {
		HistoryLimit *int `json:"historyLimit"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config %q is not valid JSON: %v", raw, err)
	}
	if got.HistoryLimit == nil {
		t.Fatalf("config = %s, want a historyLimit key spelled exactly that way", raw)
	}
	if *got.HistoryLimit != testHistoryLimit {
		t.Errorf("historyLimit = %d, want the generator's %d", *got.HistoryLimit, testHistoryLimit)
	}
}

// TestWriteAllureConfigKeepsAZeroLimit guards the one value that must never be
// optimised away. Zero is not "unset" here: Allure reads a missing historyLimit
// as "keep everything" and a present zero as "throw the history away", and a
// zero is exactly what main passes when KEEP_HISTORY is off. An omitempty on
// the field would turn switching history off into switching it on.
func TestWriteAllureConfigKeepsAZeroLimit(t *testing.T) {
	dir := t.TempDir()

	path, err := writeAllureConfig(dir, 0, filepath.Join(dir, "history.jsonl"), 1, "demo", testBaseURL)
	if err != nil {
		t.Fatalf("writeAllureConfig = %v, want nil", err)
	}

	raw := readFile(t, path)

	// Decoded into a map rather than back into allureConfig: a round trip
	// through the producing type agrees with itself no matter what the tags
	// say, and an omitempty would show up as a missing key here and as a
	// well-behaved zero there.
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config %s is not valid JSON: %v", raw, err)
	}

	limit, ok := got["historyLimit"]
	if !ok {
		t.Fatalf("config = %s, want a historyLimit key even when it is zero", raw)
	}
	if limit != float64(0) {
		t.Errorf("historyLimit = %v, want 0", limit)
	}
}

// TestWriteAllureConfigWiresTheReportURLPlugin covers the config half of what
// makes a trend bar clickable. The plugin's only job is to hand the CLI the
// address of the report being built; the CLI stamps that address onto this
// run's history entry, and the next report turns it into a link.
//
// Every part asserted here fails silently in production. An unresolvable
// import, a misspelled option key, a URL pointing at the wrong build: the
// report still generates, exit code 0, and the only symptom is a bar that
// does nothing when clicked.
func TestWriteAllureConfigWiresTheReportURLPlugin(t *testing.T) {
	dir := t.TempDir()

	path, err := writeAllureConfig(dir, testHistoryLimit, filepath.Join(dir, "history.jsonl"), 4, "demo", testBaseURL)
	if err != nil {
		t.Fatalf("writeAllureConfig = %v, want nil", err)
	}

	raw := readFile(t, path)

	var got struct {
		Plugins struct {
			Awesome struct {
				Options struct {
					GroupBy []string `json:"groupBy"`
				} `json:"options"`
			} `json:"awesome"`
			ReportURL struct {
				Import  string `json:"import"`
				Options struct {
					URL string `json:"url"`
				} `json:"options"`
			} `json:"reporturl"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("config %s is not valid JSON: %v", raw, err)
	}

	plugin := got.Plugins.ReportURL

	// The literal, not reportURLFor: this is the value the browser follows,
	// and a test that computes it the same way the code does would follow the
	// code anywhere it went. Absolute on purpose - the report hands this
	// string to new URL(), which rejects a relative one.
	const wantURL = testBaseURL + "/projects/demo/reports/4/index.html"
	if plugin.Options.URL != wantURL {
		t.Errorf("plugin url = %q, want %q for build 4", plugin.Options.URL, wantURL)
	}
	if plugin.Import == "" {
		t.Fatalf("config = %s, want the plugin's import path", raw)
	}
	// The CLI resolves a plugin by importing the path as given, so it has to
	// exist by the time the CLI runs - and it has to end in .mjs, or Node
	// reads it as CommonJS and the ES module inside is a syntax error.
	if filepath.Ext(plugin.Import) != ".mjs" {
		t.Errorf("plugin import = %q, want a .mjs file - Node reads .js next to no package.json as CommonJS", plugin.Import)
	}
	if _, err := os.Stat(plugin.Import); err != nil {
		t.Errorf("plugin import %q does not exist: %v", plugin.Import, err)
	}

	// Without an explicit groupBy the awesome plugin defaults to no grouping
	// at all - the generate subcommand does not supply the three labels the
	// awesome subcommand used to pass on its own - and the suite tree
	// collapses into a flat list of tests.
	want := []string{"parentSuite", "suite", "subSuite"}
	if !slices.Equal(got.Plugins.Awesome.Options.GroupBy, want) {
		t.Errorf("awesome groupBy = %q, want %q", got.Plugins.Awesome.Options.GroupBy, want)
	}
}

// TestReportURLPluginSetsTheReportURL runs the plugin the service ships. Its
// source is a string constant, so nothing in the Go toolchain looks at it: a
// typo anywhere inside - reportURL for reportUrl, a missing await, a stray
// character - is caught by no compiler and no linter, and shows up only as a
// history entry with an empty URL.
//
// The harness stands in for the CLI: construct the plugin with options, call
// start with a bare context, print what it left on the context.
func TestReportURLPluginSetsTheReportURL(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, cannot execute the plugin")
	}

	dir := t.TempDir()

	pluginPath, err := writeReportURLPlugin(dir)
	if err != nil {
		t.Fatalf("writeReportURLPlugin = %v, want nil", err)
	}

	harness := filepath.Join(dir, "harness.mjs")
	body := "import Plugin from " + strconv.Quote(pluginPath) + ";\n" +
		"const plugin = new Plugin({ url: \"https://allure.example.test/projects/demo/reports/7/index.html\" });\n" +
		"const context = {};\n" +
		"await plugin.start(context);\n" +
		"console.log(context.reportUrl);\n"
	if err := os.WriteFile(harness, []byte(body), 0o644); err != nil {
		t.Fatalf("writing harness: %v", err)
	}

	out, err := exec.CommandContext(t.Context(), node, harness).CombinedOutput()
	if err != nil {
		t.Fatalf("running the plugin: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "https://allure.example.test/projects/demo/reports/7/index.html" {
		t.Errorf("context.reportUrl = %q, want the url the plugin was given", got)
	}
}

// TestReportURLForIsAbsolute guards the defect this whole url shape exists to
// prevent. The report hands every url it carries - the trend points in
// charts.json and every entry of a test's history - to the browser's URL
// constructor. new URL() takes only absolute urls: given "../4/index.html" it
// throws a TypeError, the render that asked for it unwinds, and the page stops
// responding to anything, reload included, because the open test is part of
// the address the page comes back to.
//
// A relative path is perfectly valid as an href, which is why the trend bars
// on the overview kept working while the test page died. That split is what
// made the bug hard to see, and it is why this asserts the scheme and host
// rather than eyeballing the string.
func TestReportURLForIsAbsolute(t *testing.T) {
	got := reportURLFor(testBaseURL, "demo", 4)

	want := testBaseURL + "/projects/demo/reports/4/index.html"
	if got != want {
		t.Errorf("reportURLFor = %q, want %q", got, want)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("reportURLFor produced an unparseable url %q: %v", got, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		t.Errorf("reportURLFor = %q, want a scheme and a host - new URL() rejects anything else", got)
	}
}

// TestReportURLForSurvivesNewURL runs the assertion the Go side cannot make:
// that the string this service writes is one the browser will actually accept.
// url.Parse is far more forgiving than new URL() - it takes "../4/index.html"
// without complaint - so a Go-only test cannot tell the good value from the
// one that took production down. This asks the engine that does the rejecting.
func TestReportURLForSurvivesNewURL(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, cannot exercise new URL()")
	}

	dir := t.TempDir()
	harness := filepath.Join(dir, "harness.mjs")
	body := "new URL(" + strconv.Quote(reportURLFor(testBaseURL, "demo", 4)) + ");\n" +
		"console.log(\"ok\");\n"
	if err := os.WriteFile(harness, []byte(body), 0o644); err != nil {
		t.Fatalf("writing harness: %v", err)
	}

	out, err := exec.CommandContext(t.Context(), node, harness).CombinedOutput()
	if err != nil {
		t.Fatalf("new URL() rejected the report url, which is what kills the page:\n%s", out)
	}
	if got := strings.TrimSpace(string(out)); got != "ok" {
		t.Errorf("harness said %q, want ok", got)
	}
}

// TestGenerateWritesThePluginBesideTheConfig checks where the plugin file
// lands. It is written per build into the project's temp root rather than
// shipped in the image: there is no path to keep in step between the
// Dockerfile and the Go code, no case where the file is missing, and a build
// run outside a container behaves exactly like one inside it. The price is
// that the file has to be there every time, which is what this pins.
func TestGenerateWritesThePluginBesideTheConfig(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "config")
	g := newTestGenerator(t, fakeCLI(t, cliDumpConfig(dump)), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	var got struct {
		Plugins struct {
			ReportURL struct {
				Import string `json:"import"`
			} `json:"reporturl"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(readFile(t, dump), &got); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}

	imported := got.Plugins.ReportURL.Import
	if tmp := projects.TmpRoot(g.projectsDir, "demo"); !strings.HasPrefix(imported, tmp+string(filepath.Separator)) {
		t.Errorf("plugin import = %q, want it written under %q", imported, tmp)
	}
	if _, err := os.Stat(imported); err != nil {
		t.Errorf("plugin import %q does not exist while the CLI is running: %v", imported, err)
	}
}

func TestGetNextBuildNumberWithNoReportsIsOne(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	got, err := g.getNextBuildNumber("demo")
	if err != nil {
		t.Fatalf("getNextBuildNumber = %v", err)
	}
	if got != 1 {
		t.Errorf("getNextBuildNumber = %d, want 1", got)
	}
}

func TestGetNextBuildNumberIsMaxPlusOne(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")
	// "10" sorts before "9" lexicographically, so os.ReadDir hands them back in
	// that order; a comparison that just takes whichever number comes last
	// would settle on 9, not 10 - this data catches that mistake.
	for _, name := range []string{"1", "2", "7", "9", "10", "latest"} {
		if err := os.Mkdir(filepath.Join(reports, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got, err := g.getNextBuildNumber("demo")
	if err != nil {
		t.Fatalf("getNextBuildNumber = %v", err)
	}
	if got != 11 {
		t.Errorf("getNextBuildNumber = %d, want 11 (max numeric name 10, plus one; \"latest\" ignored)", got)
	}
}

func TestGetNextBuildNumberIgnoresNonDirEntries(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")
	// A stray file named like a build number must not count as one.
	if err := os.WriteFile(filepath.Join(reports, "5"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := g.getNextBuildNumber("demo")
	if err != nil {
		t.Fatalf("getNextBuildNumber = %v", err)
	}
	if got != 1 {
		t.Errorf("getNextBuildNumber = %d, want 1 (the stray file must be ignored)", got)
	}
}

func TestWriteExecutorSkipsFirstBuild(t *testing.T) {
	dir := t.TempDir()

	if err := writeExecutor(dir, "demo", testBaseURL, 1); err != nil {
		t.Fatalf("writeExecutor = %v, want nil", err)
	}

	if _, err := os.Stat(filepath.Join(dir, projects.ExecutorFileName)); !os.IsNotExist(err) {
		t.Errorf("executor.json exists for the first build, want it absent (err = %v)", err)
	}
}

func TestWriteExecutorWritesExpectedFields(t *testing.T) {
	dir := t.TempDir()

	if err := writeExecutor(dir, "demo", testBaseURL, 3); err != nil {
		t.Fatalf("writeExecutor = %v, want nil", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, projects.ExecutorFileName))
	if err != nil {
		t.Fatalf("reading executor.json: %v", err)
	}

	var got executorFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshaling executor.json: %v", err)
	}

	want := executorFile{
		BuildOrder: 3,
		BuildName:  "demo #3",
		ReportName: "demo #3",
		ReportURL:  testBaseURL + "/projects/demo/reports/3/index.html",
	}
	if got != want {
		t.Errorf("executor.json = %+v, want %+v", got, want)
	}

	// Fields with nothing to say must be omitted entirely, not written empty -
	// Allure falls back to its own defaults only when a key is absent.
	for _, key := range []string{`"name"`, `"type"`, `"url"`, `"buildUrl"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("executor.json = %s, want it without the %s key", raw, key)
		}
	}
}

func TestGenerateSkipsExecutorOnFirstBuild(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	executorPath := filepath.Join(projects.ResultsDir(g.projectsDir, "demo"), projects.ExecutorFileName)
	if _, err := os.Stat(executorPath); !os.IsNotExist(err) {
		t.Errorf("executor.json exists after the first build, want it absent (err = %v)", err)
	}
}

func TestGenerateWritesExecutorWhenAPreviousBuildIsArchived(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Stand in for an already-archived build: Generate doesn't archive reports
	// into reports/<N> itself yet, so the next build number has to come from a
	// report directory placed there directly.
	reports := projects.ReportsDir(g.projectsDir, "demo")
	if err := os.Mkdir(filepath.Join(reports, "3"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	executorPath := filepath.Join(projects.ResultsDir(g.projectsDir, "demo"), projects.ExecutorFileName)
	raw, err := os.ReadFile(executorPath)
	if err != nil {
		t.Fatalf("reading executor.json: %v", err)
	}

	var got executorFile
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshaling executor.json: %v", err)
	}
	if got.BuildOrder != 4 {
		t.Errorf("buildOrder = %d, want 4 (one past the archived build 3)", got.BuildOrder)
	}
}

func TestGenerateArchivesFirstBuildAtNumberOne(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	archived := filepath.Join(projects.NumberedReportDir(g.projectsDir, "demo", 1), "index.html")
	body, err := os.ReadFile(archived)
	if err != nil {
		t.Fatalf("reading archived report: %v", err)
	}
	if string(body) != "fresh" {
		t.Errorf("archived report = %q, want %q", body, "fresh")
	}
}

func TestGenerateArchivesUnderTheNextBuildNumber(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")
	if err := os.Mkdir(filepath.Join(reports, "3"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	archived := filepath.Join(projects.NumberedReportDir(g.projectsDir, "demo", 4), "index.html")
	if _, err := os.Stat(archived); err != nil {
		t.Errorf("archived report at build 4 missing: %v", err)
	}
}

func TestGenerateArchiveFailureDoesNotFailTheBuild(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// A plain file sitting where the archive directory needs to go.
	// getNextBuildNumber ignores non-dir entries when picking the next
	// number, so this does not shift it away from 1 - but os.CopyFS still
	// cannot create a directory where a file already exists.
	reports := projects.ReportsDir(g.projectsDir, "demo")
	if err := os.WriteFile(filepath.Join(reports, "1"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil (archiving is best-effort)", err)
	}

	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q; a failed archive must not affect publishing", got, "fresh")
	}
}

func TestGenerateLeavesNoPartialArchiveBehind(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliUnarchivable), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil (archiving is best-effort)", err)
	}

	// The copy is staged in tmp and renamed into place, so a copy that dies
	// half-way leaves nothing under the build number at all. Copying straight
	// into it would leave a numbered report holding some of its files and not
	// others - permanently, because the next build claims a higher number and
	// never revisits this one, while getProject lists it as a real build the
	// moment index.html is among the files that made it.
	archived := projects.NumberedReportDir(g.projectsDir, "demo", 1)
	if _, err := os.Stat(archived); !errors.Is(err, os.ErrNotExist) {
		entries, _ := os.ReadDir(archived)
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("a partial archive was published at build 1: %v (stat err = %v)", names, err)
	}

	// The build itself is untouched by any of this: the report is already
	// live by the time archiving is attempted.
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

func TestGenerateSkipsTheArchiveWhenHistoryIsOff(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliUnarchivable), "demo")
	g.historyLimit = 0 // KEEP_HISTORY=false

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

	// With the numbered archives switched off, pruneReports deletes every one
	// it finds - so copying the report into place first only to erase it costs
	// a full duplicate of the report per build for nothing. The CLI here
	// produces a report os.CopyFS cannot copy, and a failed copy leaves its
	// partial work in tmp: an archive directory there is the proof that the
	// copy was atempted at all.
	staged := filepath.Join(projects.TmpRoot(g.projectsDir, "demo"), "archive")
	if _, err := os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the report was staged for archiving at %s (stat err = %v), want no archiving with the limit at 0", staged, err)
	}

	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

func TestPruneReportsKeepsAllWhenUnderTheLimit(t *testing.T) {
	dir := t.TempDir()
	if err := projects.CreateDir(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	g := New(dir, "unused-cli", 3, testBaseURL)

	reports := projects.ReportsDir(dir, "demo")
	for _, name := range []string{"1", "2"} {
		if err := os.Mkdir(filepath.Join(reports, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := g.pruneReports("demo"); err != nil {
		t.Fatalf("pruneReports = %v, want nil", err)
	}

	for _, name := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(reports, name)); err != nil {
			t.Errorf("reports/%s missing after prune, want it kept (fewer builds than the limit): %v", name, err)
		}
	}
}

func TestPruneReportsDeletesOldestByNumber(t *testing.T) {
	dir := t.TempDir()
	if err := projects.CreateDir(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	g := New(dir, "unused-cli", 3, testBaseURL)

	reports := projects.ReportsDir(dir, "demo")
	// "10" sorts before "9" lexicographically - this data catches a prune that
	// forgot to sort numerically before cutting the slice.
	for _, name := range []string{"1", "2", "3", "7", "9", "10", "latest"} {
		if err := os.Mkdir(filepath.Join(reports, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := g.pruneReports("demo"); err != nil {
		t.Fatalf("pruneReports = %v, want nil", err)
	}

	for _, name := range []string{"1", "2", "3"} {
		if _, err := os.Stat(filepath.Join(reports, name)); !os.IsNotExist(err) {
			t.Errorf("reports/%s still exists, want the three oldest builds pruned (err = %v)", name, err)
		}
	}
	for _, name := range []string{"7", "9", "10", "latest"} {
		if _, err := os.Stat(filepath.Join(reports, name)); err != nil {
			t.Errorf("reports/%s missing after prune, want the newest builds and latest kept: %v", name, err)
		}
	}
}

func TestPruneReportsReadDirErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	g := New(dir, "unused-cli", 3, testBaseURL) // no project created: ReportsDir does not exist

	if err := g.pruneReports("missing"); err == nil {
		t.Fatal("pruneReports = nil, want an error when the reports directory can't be read")
	}
}

// TestGenerateContinuesWhenPruneReportsFails forces pruneReports' only error
// path - the reports directory becoming unreadable - from inside the fake
// CLI itself, since that is the one point in Generate between the build
// number being read and pruneReports running.
func TestGenerateContinuesWhenPruneReportsFails(t *testing.T) {
	dir := t.TempDir()
	if err := projects.CreateDir(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	writeResult(t, dir, "demo")
	g := New(dir, "unused-cli", testHistoryLimit, testBaseURL)

	reports := projects.ReportsDir(dir, "demo")
	// 0300: write+execute survive, so the rename of "latest" and the mkdir for
	// the archive (both done by name, not by listing) still succeed; only
	// os.ReadDir, which needs read permission to enumerate entries, fails -
	// isolating the one operation pruneReports performs.
	g.allureBin = fakeCLI(t, "#!/bin/sh\n"+
		"chmod 300 \""+reports+"\"\n"+
		"printf 'fresh' > \"$4/index.html\"\n")

	// Restore permissions before the temp dir's own cleanup tries to remove
	// the tree; a directory without read permission cannot be listed to
	// delete its contents.
	t.Cleanup(func() { _ = os.Chmod(reports, 0o755) })

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil (a failed prune must not fail the build)", err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

// TestGenerateAccumulatesHistoryAcrossBuilds is the test for the staging copy
// being seeded from the real history. Skip that copy and every build hands the
// CLI an empty file, which then replaces the accumulated history with a single
// line - trends silently reset on every build, and nothing else in the suite
// notices.
func TestGenerateAccumulatesHistoryAcrossBuilds(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliHistory), "demo")

	const builds = 3
	for i := range builds {
		if err := g.Generate(t.Context(), "demo"); err != nil {
			t.Fatalf("Generate (build %d) = %v, want nil", i+1, err)
		}
	}

	b, err := os.ReadFile(projects.HistoryFile(g.projectsDir, "demo"))
	if err != nil {
		t.Fatalf("reading published history: %v", err)
	}
	if got := strings.Count(string(b), "\n"); got != builds {
		t.Errorf("history holds %d runs after %d builds, want %d: %q",
			got, builds, builds, b)
	}
}

// TestFailedBuildLeavesHistoryIntact is the reason the CLI is pointed at a
// copy at all. The CLI writes history in place and cannot be trusted to leave
// a whole file behind when it dies, and a history ending in half a line fails
// every later build for good.
func TestFailedBuildLeavesHistoryIntact(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliWreckHistory), "demo")

	history := projects.HistoryFile(g.projectsDir, "demo")
	const want = "run one\nrun two\n"
	if err := os.WriteFile(history, []byte(want), 0o644); err != nil {
		t.Fatalf("seeding history: %v", err)
	}

	if err := g.Generate(t.Context(), "demo"); err == nil {
		t.Fatal("Generate = nil, want the failing CLI to be reported")
	}

	got, err := os.ReadFile(history)
	if err != nil {
		t.Fatalf("reading history after a failed build: %v", err)
	}
	if string(got) != want {
		t.Errorf("history after a failed build = %q, want it untouched at %q", got, want)
	}
}

// flagValue returns the argument following name in argv. A flag sitting last,
// with nothing after it, counts as absent: the CLI would reject it anyway.
func flagValue(argv []string, name string) (string, bool) {
	for i, arg := range argv {
		if arg == name && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

func TestGenerateWithRealAllure(t *testing.T) {
	if _, err := exec.LookPath("allure"); err != nil {
		t.Skip("allure CLI not installed")
	}

	g := newTestGenerator(t, "allure", "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	index := filepath.Join(projects.LatestReportDir(g.projectsDir, "demo"), "index.html")
	if _, err := os.Stat(index); err != nil {
		t.Errorf("real Allure run left no index.html at %s: %v", index, err)
	}

	// The fake CLIs cannot prove the path is one the real Allure accepts and
	// writes to; only a real run can. This is also what would catch the CLI
	// changing the flag out from under us.
	history := projects.HistoryFile(g.projectsDir, "demo")
	if _, err := os.Stat(history); err != nil {
		t.Errorf("real Allure run left no history at %s: %v", history, err)
	}
}

func TestGenerateContextDoneWhileWaitingForLock(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	held := g.lockFor("demo")
	held.Lock()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- g.Generate(ctx, "demo") }()

	// Let the goroutine reach the lock, then kill the context and release it:
	// Generate must notice the dead context instead of starting the build.
	time.Sleep(50 * time.Millisecond)
	cancel()
	held.Unlock()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Generate = %v, want an error wrapping context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Generate did not return after its context was canceled")
	}
}

// waitForState polls until the project's status reaches want, and returns that
// status. Polling rather than a channel keeps the test honest: it observes the
// generator only through its public surface, exactly as an HTTP handler would.
func waitForState(t *testing.T, g *Generator, projectID string, want State) Status {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := g.Status(projectID)
		if ok && st.State == want {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}

	st, ok := g.Status(projectID)
	t.Fatalf("status of %q never reached %q (last: %+v, exists=%v)", projectID, want, st, ok)
	return Status{}
}

func TestStartReturnsBeforeTheBuildFinishes(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	began := time.Now()
	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	// The whole point of Start: the caller is not made to wait for the CLI.
	if waited := time.Since(began); waited > 200*time.Millisecond {
		t.Errorf("Start blocked for %v, want it to return while the build runs", waited)
	}

	// And the build must be visible as running, not silently in flight.
	st, ok := g.Status("demo")
	if !ok || st.State != StateRunning {
		t.Fatalf("status right after Start = %+v (exists=%v), want %q", st, ok, StateRunning)
	}
	if st.StartedAt.IsZero() {
		t.Error("running status has no StartedAt")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestStartRecordsSuccessAndPublishesReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}

	st := waitForState(t, g, "demo", StateSucceeded)
	if st.Err != nil {
		t.Errorf("succeeded status carries an error: %v", st.Err)
	}
	if st.FinishedAt.Before(st.StartedAt) || st.FinishedAt.IsZero() {
		t.Errorf("timestamps make no sense: started %v, finished %v", st.StartedAt, st.FinishedAt)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the newly built %q", got, "fresh")
	}
}

func TestStartRecordsFailure(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFail), "demo")
	writeLatest(t, g, "demo", "stale")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil: a build that will fail still starts fine", err)
	}

	st := waitForState(t, g, "demo", StateFailed)
	if st.Err == nil {
		t.Fatal("failed status carries no error, the caller has no way to learn why")
	}
	if !strings.Contains(st.Err.Error(), "boom: broken results") {
		t.Errorf("status error = %v, want it to carry the CLI stderr", st.Err)
	}
	if got := readLatest(t, g, "demo"); got != "stale" {
		t.Errorf("latest report = %q, want the previous %q left untouched", got, "stale")
	}
}

func TestStartRejectsASecondBuildOfTheSameProject(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("first Start = %v, want nil", err)
	}

	err := g.Start(t.Context(), "demo")
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start = %v, want ErrAlreadyRunning", err)
	}

	// Once the first build is done the project is free again: the claim is a
	// lock on the build, not a permanent mark on the project.
	waitForState(t, g, "demo", StateSucceeded)
	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start after the previous build finished = %v, want nil", err)
	}
	waitForState(t, g, "demo", StateSucceeded)
}

func TestTryStartClaimsExactlyOnceUnderConcurrency(t *testing.T) {
	// The claim exists for exactly this: many callers arriving together, all
	// seeing a free project, and only one being allowed to build it.
	//
	// A check-then-set split into two separately locked operations survives a
	// single round most of the time, because the window between them is a few
	// nanoseconds wide. Repeating the round is what turns "rarely wrong" into
	// "reliably caught". Calling tryStart directly matters too: going through
	// Start puts an os.Stat in front of the claim, which spreads the callers
	// out and hides the race.
	const rounds, callers = 200, 40

	for round := range rounds {
		g := New("unused-dir", "unused-cli", testHistoryLimit, testBaseURL) // tryStart never touches disk

		var ready, done sync.WaitGroup
		ready.Add(callers)
		done.Add(callers)

		release := make(chan struct{})
		won := make(chan struct{}, callers)

		for range callers {
			go func() {
				defer done.Done()
				ready.Done()
				<-release // everyone leaves the gate in the same instant
				if g.tryStart("demo", time.Now()) {
					won <- struct{}{}
				}
			}()
		}

		ready.Wait()
		close(release)
		done.Wait()
		close(won)

		if n := len(won); n != 1 {
			t.Fatalf("round %d: %d of %d callers claimed the project, want exactly 1",
				round, n, callers)
		}
	}
}

func TestStatusStaysReadableWhileABuildRuns(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}

	// Holding g.mu for the duration of the build would deadlock the service:
	// every status request would queue behind the CLI.
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		g.Status("demo")
	}()

	select {
	case <-answered:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Status blocked while a build was running: the build is holding g.mu")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestStartIgnoresTheCallersCancellation(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	ctx, cancel := context.WithCancel(context.Background())
	if err := g.Start(ctx, "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	// Stands in for the client disconnecting mid-build. The work is already
	// underway and its result lands on disk, so it must not be thrown away.
	cancel()

	st := waitForState(t, g, "demo", StateSucceeded)
	if st.Err != nil {
		t.Errorf("build reported %v after the caller went away, want it to finish", st.Err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the build to have published %q", got, "fresh")
	}
}

func TestStartRejectsUnknownAndMalformedProjects(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Start(t.Context(), "missing"); !errors.Is(err, ErrProjectNotFound) {
		t.Errorf("Start(missing) = %v, want ErrProjectNotFound", err)
	}
	if err := g.Start(t.Context(), "../escape"); err == nil {
		t.Error("Start accepted a project ID containing a path traversal")
	}

	// A rejected call must leave no trace, otherwise a typo would mark a
	// project as running forever.
	if st, ok := g.Status("missing"); ok {
		t.Errorf("rejected Start left a status behind: %+v", st)
	}
}

func TestClearResultsRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if err := g.ClearResults("../escape"); err == nil {
		t.Fatal("ClearResults accepted a project ID containing a path traversal")
	}
}

func TestClearResultsUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.ClearResults("missing")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("ClearResults(missing) = %v, want ErrProjectNotFound", err)
	}
}

func TestClearResultsClearsFiles(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	if err := g.ClearResults("demo"); err != nil {
		t.Fatalf("ClearResults = %v, want nil", err)
	}

	entries, err := os.ReadDir(projects.ResultsDir(g.projectsDir, "demo"))
	if err != nil {
		t.Fatalf("ReadDir after ClearResults: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("results dir after ClearResults = %v, want empty", entries)
	}
}

func TestClearResultsSerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Simulate a build in flight by holding the project's lock directly, the
	// same technique TestGenerateSerializesSameProject uses.
	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.ClearResults("demo") }()

	select {
	case err := <-done:
		t.Fatalf("ClearResults returned while the project lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ClearResults after unlock = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ClearResults did not proceed after the project lock was released")
	}
}

func TestClearHistoryRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if err := g.ClearHistory(t.Context(), "../escape"); err == nil {
		t.Fatal("ClearHistory accepted a project ID containing a path traversal")
	}
}

func TestClearHistoryUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.ClearHistory(t.Context(), "missing")
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("ClearHistory(missing) = %v, want ErrProjectNotFound", err)
	}
}

func TestClearHistoryClearsAndTriggersRebuild(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	archive := projects.NumberedReportDir(g.projectsDir, "demo", 1)
	if err := os.MkdirAll(archive, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archive, "index.html"), []byte("old"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(projects.HistoryFile(g.projectsDir, "demo"), []byte(`{"n":1}`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	executor := filepath.Join(projects.ResultsDir(g.projectsDir, "demo"), projects.ExecutorFileName)
	if err := os.WriteFile(executor, []byte(`{"buildOrder":5}`), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := g.ClearHistory(t.Context(), "demo"); err != nil {
		t.Fatalf("ClearHistory = %v, want nil", err)
	}

	if _, err := os.Stat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("archive still exists (stat err = %v)", err)
	}
	if _, err := os.Stat(executor); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("executor.json still exists (stat err = %v)", err)
	}

	// ClearHistory does not wait for the rebuild it triggers - the report is
	// only guaranteed once the status catches up.
	st := waitForState(t, g, "demo", StateSucceeded)
	if st.Err != nil {
		t.Errorf("triggered rebuild failed: %v", st.Err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want the rebuild's own %q", got, "fresh")
	}
}

func TestClearHistoryRefusesWhenResultsAreEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := projects.CreateDir(dir, "demo"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	g := New(dir, fakeCLI(t, cliOK), testHistoryLimit, testBaseURL)

	err := g.ClearHistory(t.Context(), "demo")
	if !errors.Is(err, ErrNoResults) {
		t.Fatalf("ClearHistory with empty results = %v, want ErrNoResults", err)
	}
}

func TestClearHistorySerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.ClearHistory(context.Background(), "demo") }()

	select {
	case err := <-done:
		t.Fatalf("ClearHistory returned while the project lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ClearHistory after unlock = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ClearHistory did not proceed after the project lock was released")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestDeleteRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	if err := g.Delete("../escape"); err == nil {
		t.Fatal("Delete(\"../escape\") = nil, want a validation error")
	}
}

func TestDeleteUnknownProjectSucceeds(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	// The caller asked for the project to be gone and it is gone. os.RemoveAll
	// reports no error for a missing path, so neither does Delete - and the
	// HTTP layer answers 204 rather than 404 on the strength of that.
	if err := g.Delete("nosuch"); err != nil {
		t.Fatalf("Delete of an absent project = %v, want nil", err)
	}
}

func TestDeleteRemovesTheWholeProjectTree(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}
	// Delete has to take out results, reports and history alike, so build a
	// report first: a project with only a results dir would not prove it.
	if _, err := os.Stat(projects.LatestReportDir(g.projectsDir, "demo")); err != nil {
		t.Fatalf("setup: no report to delete: %v", err)
	}

	if err := g.Delete("demo"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}

	if _, err := os.Stat(projects.ProjectDir(g.projectsDir, "demo")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("project dir still there after Delete (stat err = %v)", err)
	}
}

func TestDeleteForgetsTheProjectStatus(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	waitForState(t, g, "demo", StateSucceeded)

	if err := g.Delete("demo"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}

	// A project recreated under the same ID must not inherit this one's
	// StateSucceeded and report a build it never ran.
	if st, ok := g.Status("demo"); ok {
		t.Errorf("Status after Delete = %+v, exists=%v, want no status at all", st, ok)
	}
}

func TestDeleteOutlastsALateStatusWrite(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	waitForState(t, g, "demo", StateSucceeded)

	if err := g.Delete("demo"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}

	// The build goroutine records its outcome only after Generate has released
	// the project lock, so a Delete arriving in that window takes the lock,
	// removes the tree and forgets the status - and the write still lands
	// afterwards. That is reproduced here by writing the status directly,
	// which is all the goroutine does at that point. A project nobody is
	// building must not grow a status out of nothing: the next project created
	// under this ID would inherit a build it never ran.
	g.setStatus("demo", Status{State: StateSucceeded, StartedAt: time.Now()})

	if st, ok := g.Status("demo"); ok {
		t.Errorf("Status after a late write = %+v, exists=%v, want no status at all", st, ok)
	}
}

func TestDeleteKeepsTheProjectLock(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	before := g.lockFor("demo")
	if err := g.Delete("demo"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	after := g.lockFor("demo")

	// Dropping the mutex from the registry along with the project would let a
	// caller that arrives next miss the map and create a second mutex for the
	// same ID - while an earlier caller still holds the first one. Both would
	// then believe they had the project to themselves. Keeping the entry costs
	// one mutex per ID the process has ever seen; losing the invariant costs
	// the serialization every other operation depends on.
	if before != after {
		t.Error("Delete replaced the project's mutex; two callers can now hold different locks for one project")
	}
}

func TestDeleteSerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	// Simulate a build in flight by holding the project's lock directly. A
	// Delete that ignored the lock would pull the directory out from under
	// that build mid-rename.
	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.Delete("demo") }()

	select {
	case err := <-done:
		t.Fatalf("Delete returned while the project lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Delete after unlock = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Delete did not proceed after the project lock was released")
	}
}
