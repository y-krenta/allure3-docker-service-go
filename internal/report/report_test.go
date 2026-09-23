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

const (
	cliOK          = "#!/bin/sh\nprintf 'fresh' > \"$4/index.html\"\n"
	cliFail        = "#!/bin/sh\necho 'boom: broken results' >&2\nexit 3\n"
	cliFindHistory = "#!/bin/sh\n" +
		"out=\"$4\"\n" +
		"cfg=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--config\" ]; then cfg=\"$2\"; fi\n" +
		"  shift\n" +
		"done\n" +
		"hist=$(sed -n 's/.*\"historyPath\":\"\\([^\"]*\\)\".*/\\1/p' \"$cfg\")\n"
	cliHistory      = cliFindHistory + "printf 'fresh' > \"$out/index.html\"\necho run >> \"$hist\"\n"
	cliWreckHistory = cliFindHistory + "printf 'half a lin' >> \"$hist\"\necho 'boom' >&2\nexit 3\n"
	cliSlow         = "#!/bin/sh\nsleep 0.5\nprintf 'fresh' > \"$4/index.html\"\n"
	cliUnarchivable = "#!/bin/sh\n" +
		"printf 'fresh' > \"$4/index.html\"\n" +
		"mkfifo \"$4/m-fifo\"\n" +
		"printf 'last' > \"$4/z.txt\"\n"
	cliFloodStderr = "#!/bin/sh\n" +
		"printf 'HEAD-ONLY-MARKER' >&2\n" +
		"i=0\n" +
		"while [ $i -lt 200 ]; do printf '0123456789012345678901234567890123456789' >&2; i=$((i+1)); done\n" +
		"printf 'SyntaxError: TAIL-MARKER' >&2\n" +
		"exit 3\n"
	cliNoIndex = cliFindHistory +
		"printf '{}' > \"$out/summary.json\"\n" +
		"echo run >> \"$hist\"\n" +
		"echo 'plugin awesome error TypeError' >&2\n"
)

func cliRecordArgv(dumpPath string) string {
	return "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + dumpPath + "\"\nprintf 'fresh' > \"$4/index.html\"\n"
}

func cliDumpConfig(dumpPath string) string {
	return "#!/bin/sh\n" +
		"printf 'fresh' > \"$4/index.html\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--config\" ]; then cat \"$2\" > \"" + dumpPath + "\"; fi\n" +
		"  shift\n" +
		"done\n" +
		"exit 0\n"
}

func cliRecordCwd(dumpPath string) string {
	return "#!/bin/sh\npwd -P > \"" + dumpPath + "\"\nprintf 'fresh' > \"$4/index.html\"\n"
}

func fakeCLI(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "fake-allure")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake CLI: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

const testHistoryLimit = 7

const testBaseURL = "https:allure.example.test"

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

func writeResult(t *testing.T, baseDir, projectID string) {
	t.Helper()

	path := filepath.Join(projects.ResultsDir(baseDir, projectID), "9f0a1c-result.json")
	if err := os.WriteFile(path, []byte(`{"name":"a test"}`), 0o644); err != nil {
		t.Fatalf("writing result file: %v", err)
	}
}

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

	const goroutines = 50
	done := make(chan *sync.Mutex, goroutines)
	for i := range goroutines {
		go func() {
			g.lockFor(string(rune('a' + i%26)))
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

	if !strings.Contains(err.Error(), "boom: broken results") {
		t.Errorf("Generate error = %v, want it to carry the CLI stderr", err)
	}

	if got := readLatest(t, g, "demo"); got != "stale" {
		t.Errorf("latest report = %q, want the previous %q left untouched", got, "stale")
	}
}

func TestGenerateRejectsReportWithoutIndex(t *testing.T) {
	t.Run("previous report, archive and history stay as they were", func(t *testing.T) {
		g := newTestGenerator(t, fakeCLI(t, cliNoIndex), "demo")
		writeLatest(t, g, "demo", "stale")
		history := projects.HistoryFile(g.projectsDir, "demo")
		const want = "run one\n"
		if err := os.WriteFile(history, []byte(want), 0o644); err != nil {
			t.Fatalf("seeding history: %v", err)
		}

		if err := g.Generate(t.Context(), "demo"); err == nil {
			t.Fatal("Generate = nil, want an error for a report with no index.html")
		}

		if got := readLatest(t, g, "demo"); got != "stale" {
			t.Errorf("latest report = %q, want the previous %q left untouched", got, "stale")
		}
		if _, err := os.Stat(projects.NumberedReportDir(g.projectsDir, "demo", 1)); !os.IsNotExist(err) {
			t.Errorf("reports/1 exists (err = %v), want the broken report left unarchived", err)
		}
		if got := string(readFile(t, history)); got != want {
			t.Errorf("history = %q, want it untouched at %q", got, want)
		}
	})

	t.Run("first build leaves the project unbuilt and its number free", func(t *testing.T) {
		g := newTestGenerator(t, fakeCLI(t, cliNoIndex), "demo")

		if err := g.Generate(t.Context(), "demo"); err == nil {
			t.Fatal("Generate = nil, want an error for a report with no index.html")
		}
		if _, err := os.Stat(projects.LatestReportDir(g.projectsDir, "demo")); !os.IsNotExist(err) {
			t.Errorf("reports/latest exists (err = %v), want nothing published", err)
		}
		if _, err := os.Stat(projects.HistoryFile(g.projectsDir, "demo")); !os.IsNotExist(err) {
			t.Errorf("history exists (err = %v), want nothing published", err)
		}

		retry := New(g.projectsDir, fakeCLI(t, cliOK), testHistoryLimit, testBaseURL)
		if err := retry.Generate(t.Context(), "demo"); err != nil {
			t.Fatalf("Generate after the failed build = %v, want nil", err)
		}
		if _, err := os.Stat(projects.NumberedReportDir(g.projectsDir, "demo", 1)); err != nil {
			t.Errorf("reports/1 missing after the first good build: %v", err)
		}
	})
}

func TestGenerateRemovesTempBuildDirs(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

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

	if len(got) > maxStderrBytes+512 {
		t.Errorf("error is %d bytes, want stderr truncated to about %d", len(got), maxStderrBytes)
	}
}

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

func TestWriteAllureConfigKeepsAZeroLimit(t *testing.T) {
	dir := t.TempDir()

	path, err := writeAllureConfig(dir, 0, filepath.Join(dir, "history.jsonl"), 1, "demo", testBaseURL)
	if err != nil {
		t.Fatalf("writeAllureConfig = %v, want nil", err)
	}

	raw := readFile(t, path)

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

	const wantURL = testBaseURL + "/projects/demo/reports/4/index.html"
	if plugin.Options.URL != wantURL {
		t.Errorf("plugin url = %q, want %q for build 4", plugin.Options.URL, wantURL)
	}
	if plugin.Import == "" {
		t.Fatalf("config = %s, want the plugin's import path", raw)
	}

	if filepath.Ext(plugin.Import) != ".mjs" {
		t.Errorf("plugin import = %q, want a .mjs file - Node reads .js next to no package.json as CommonJS", plugin.Import)
	}
	if _, err := os.Stat(plugin.Import); err != nil {
		t.Errorf("plugin import %q does not exist: %v", plugin.Import, err)
	}

	want := []string{"parentSuite", "suite", "subSuite"}
	if !slices.Equal(got.Plugins.Awesome.Options.GroupBy, want) {
		t.Errorf("awesome groupBy = %q, want %q", got.Plugins.Awesome.Options.GroupBy, want)
	}
}

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
		"const plugin = new Plugin({ url: \"https:allure.example.test/projects/demo/reports/7/index.html\" });\n" +
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
	if got := strings.TrimSpace(string(out)); got != "https:allure.example.test/projects/demo/reports/7/index.html" {
		t.Errorf("context.reportUrl = %q, want the url the plugin was given", got)
	}
}

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

	archived := projects.NumberedReportDir(g.projectsDir, "demo", 1)
	if _, err := os.Stat(archived); !errors.Is(err, os.ErrNotExist) {
		entries, _ := os.ReadDir(archived)
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("a partial archive was published at build 1: %v (stat err = %v)", names, err)
	}

	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

func TestGenerateSkipsTheArchiveWhenHistoryIsOff(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliUnarchivable), "demo")
	g.historyLimit = 0

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

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
	g := New(dir, "unused-cli", 3, testBaseURL)

	if err := g.pruneReports("missing"); err == nil {
		t.Fatal("pruneReports = nil, want an error when the reports directory can't be read")
	}
}

func TestGenerateContinuesWhenPruneReportsFails(t *testing.T) {
	dir := t.TempDir()
	if err := projects.CreateDir(dir, "demo"); err != nil {
		t.Fatal(err)
	}
	writeResult(t, dir, "demo")
	g := New(dir, "unused-cli", testHistoryLimit, testBaseURL)

	reports := projects.ReportsDir(dir, "demo")

	g.allureBin = fakeCLI(t, "#!/bin/sh\n"+
		"chmod 300 \""+reports+"\"\n"+
		"printf 'fresh' > \"$4/index.html\"\n")

	t.Cleanup(func() { _ = os.Chmod(reports, 0o755) })

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil (a failed prune must not fail the build)", err)
	}
	if got := readLatest(t, g, "demo"); got != "fresh" {
		t.Errorf("latest report = %q, want %q", got, "fresh")
	}
}

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

	if waited := time.Since(began); waited > 200*time.Millisecond {
		t.Errorf("Start blocked for %v, want it to return while the build runs", waited)
	}

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

	waitForState(t, g, "demo", StateSucceeded)
	if err := g.Start(t.Context(), "demo"); err != nil {
		t.Fatalf("Start after the previous build finished = %v, want nil", err)
	}
	waitForState(t, g, "demo", StateSucceeded)
}

func TestTryStartClaimsExactlyOnceUnderConcurrency(t *testing.T) {

	const rounds, callers = 200, 40

	for round := range rounds {
		g := New("unused-dir", "unused-cli", testHistoryLimit, testBaseURL)

		var ready, done sync.WaitGroup
		ready.Add(callers)
		done.Add(callers)

		release := make(chan struct{})
		won := make(chan struct{}, callers)

		for range callers {
			go func() {
				defer done.Done()
				ready.Done()
				<-release
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

	if err := g.Delete("nosuch"); err != nil {
		t.Fatalf("Delete of an absent project = %v, want nil", err)
	}
}

func TestDeleteRemovesTheWholeProjectTree(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	if err := g.Generate(t.Context(), "demo"); err != nil {
		t.Fatalf("Generate = %v, want nil", err)
	}

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

	if before != after {
		t.Error("Delete replaced the project's mutex; two callers can now hold different locks for one project")
	}
}

func TestDeleteSerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

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
