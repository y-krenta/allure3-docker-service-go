package report

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

const testHistoryLimit = 7

const testBaseURL = "https://allure.example.test"

const testMaxBuilds = 4

func newTestGenerator(t *testing.T, allureBin string, projectIDs ...string) *Generator {
	t.Helper()

	dir := t.TempDir()
	for _, id := range projectIDs {
		require.NoError(t, projects.CreateDir(dir, id))
		writeResult(t, dir, id)
	}
	return New(dir, allureBin, testHistoryLimit, testBaseURL, testMaxBuilds, 0)
}

func writeResult(t *testing.T, baseDir, projectID string) {
	t.Helper()

	path := filepath.Join(projects.ResultsDir(baseDir, projectID), "9f0a1c-result.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"name":"a test"}`), 0o644))
}

func writeLatest(t *testing.T, g *Generator, projectID, body string) {
	t.Helper()

	latest := projects.LatestReportDir(g.projectsDir, projectID)
	require.NoError(t, os.MkdirAll(latest, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(latest, "index.html"), []byte(body), 0o644))
}

func readLatest(t *testing.T, g *Generator, projectID string) string {
	t.Helper()

	path := filepath.Join(projects.LatestReportDir(g.projectsDir, projectID), "index.html")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func TestLockForReturnsSameMutexPerProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	assert.Same(t, g.lockFor("demo"), g.lockFor("demo"))
	assert.NotSame(t, g.lockFor("one"), g.lockFor("two"), "different projects must not block each other")
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
		require.Same(t, first, <-done)
	}
}

func TestGenerateRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	require.Error(t, g.Generate(t.Context(), "../escape"))
}

func TestGenerateUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.Generate(t.Context(), "missing")
	require.ErrorIs(t, err, ErrProjectNotFound)
}

func TestEmptyResultsDirIsRefused(t *testing.T) {
	newEmptyProject := func(t *testing.T) *Generator {
		t.Helper()

		dir := t.TempDir()
		require.NoError(t, projects.CreateDir(dir, "demo"))
		return New(dir, fakeCLI(t, cliOK), testHistoryLimit, testBaseURL, testMaxBuilds, 0)
	}

	t.Run("Generate", func(t *testing.T) {
		g := newEmptyProject(t)

		require.ErrorIs(t, g.Generate(t.Context(), "demo"), ErrNoResults)
	})

	t.Run("Start", func(t *testing.T) {
		g := newEmptyProject(t)

		require.ErrorIs(t, g.Start(t.Context(), "demo"), ErrNoResults)
		_, ok := g.Status("demo")
		assert.False(t, ok, "want no status recorded for a rejected build")
	})

	t.Run("the previous report survives", func(t *testing.T) {
		g := newEmptyProject(t)
		writeLatest(t, g, "demo", "previous")

		require.ErrorIs(t, g.Generate(t.Context(), "demo"), ErrNoResults)
		assert.Equal(t, "previous", readLatest(t, g, "demo"))
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
		require.FailNow(t, "Generate returned while the project lock was held", "err = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Generate did not proceed after the project lock was released")
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
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Generate(idle) blocked while an unrelated project was building")
	}
}

func TestGenerateFirstBuildCreatesLatest(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))
	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestGenerateReplacesPreviousReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")
	writeLatest(t, g, "demo", "stale")

	require.NoError(t, g.Generate(t.Context(), "demo"))
	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestGenerateFailedBuildKeepsPreviousReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFail), "demo")
	writeLatest(t, g, "demo", "stale")

	err := g.Generate(t.Context(), "demo")

	require.Error(t, err)
	assert.ErrorContains(t, err, "boom: broken results", "want the CLI stderr in the error")
	assert.Equal(t, "stale", readLatest(t, g, "demo"))
}

func TestGenerateRejectsReportWithoutIndex(t *testing.T) {
	t.Run("previous report, archive and history stay as they were", func(t *testing.T) {
		g := newTestGenerator(t, fakeCLI(t, cliNoIndex), "demo")
		writeLatest(t, g, "demo", "stale")
		history := projects.HistoryFile(g.projectsDir, "demo")
		const want = "run one\n"
		require.NoError(t, os.WriteFile(history, []byte(want), 0o644))

		require.Error(t, g.Generate(t.Context(), "demo"))

		assert.Equal(t, "stale", readLatest(t, g, "demo"))
		_, err := os.Stat(projects.NumberedReportDir(g.projectsDir, "demo", 1))
		assert.ErrorIs(t, err, os.ErrNotExist, "want the broken report left unarchived")
		assert.Equal(t, want, string(readFile(t, history)))
	})

	t.Run("first build leaves the project unbuilt and its number free", func(t *testing.T) {
		g := newTestGenerator(t, fakeCLI(t, cliNoIndex), "demo")

		require.Error(t, g.Generate(t.Context(), "demo"))
		_, err := os.Stat(projects.LatestReportDir(g.projectsDir, "demo"))
		assert.ErrorIs(t, err, os.ErrNotExist, "reports/latest: want nothing published")
		_, err = os.Stat(projects.HistoryFile(g.projectsDir, "demo"))
		assert.ErrorIs(t, err, os.ErrNotExist, "history: want nothing published")

		retry := New(g.projectsDir, fakeCLI(t, cliOK), testHistoryLimit, testBaseURL, testMaxBuilds, 0)
		require.NoError(t, retry.Generate(t.Context(), "demo"))
		assert.DirExists(t, projects.NumberedReportDir(g.projectsDir, "demo", 1))
	})
}

func TestGenerateRemovesTempBuildDirs(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	tmp := projects.TmpRoot(g.projectsDir, "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "build-stale"), 0o755))

	require.NoError(t, g.Generate(t.Context(), "demo"))

	entries, err := os.ReadDir(tmp)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), "build-"), "temp root still holds %q after a build", e.Name())
	}
}

func TestRunAllureReportsMissingBinary(t *testing.T) {
	g := newTestGenerator(t, "definitely-not-an-installed-binary", "demo")

	err := g.runAllure(t.Context(), t.TempDir(), t.TempDir(),
		filepath.Join(t.TempDir(), "allurerc.json"))
	require.ErrorIs(t, err, exec.ErrNotFound)
}

func TestGenerateInvokesTheGenerateSubcommand(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "argv")
	g := newTestGenerator(t, fakeCLI(t, cliRecordArgv(dump)), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	raw, err := os.ReadFile(dump)
	require.NoError(t, err)
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")

	assert.Equal(t, "generate", argv[0], "awesome discards the configured plugins")
}

func TestGenerateStagesHistoryIntoTheConfig(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "config")
	g := newTestGenerator(t, fakeCLI(t, cliDumpConfig(dump)), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	var got struct {
		HistoryPath string `json:"historyPath"`
	}
	require.NoError(t, json.Unmarshal(readFile(t, dump), &got))

	require.NotEmpty(t, got.HistoryPath, "config carries no historyPath, so the CLI keeps no history at all")
	assert.NotEqual(t, projects.HistoryFile(g.projectsDir, "demo"), got.HistoryPath, "want a staged copy rather than the project's own history")
	tmp := projects.TmpRoot(g.projectsDir, "demo")
	assert.True(t, strings.HasPrefix(got.HistoryPath, tmp+string(filepath.Separator)), "historyPath = %q, want it staged under %q", got.HistoryPath, tmp)
}

func TestRunAllureRunsTheCLIFromANeutralDirectory(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "cwd")
	g := newTestGenerator(t, fakeCLI(t, cliRecordCwd(dump)), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	raw, err := os.ReadFile(dump)
	require.NoError(t, err)
	got := strings.TrimSpace(string(raw))

	want, err := filepath.EvalSymlinks(os.TempDir())
	require.NoError(t, err)
	assert.Equal(t, want, got, "anything but a neutral directory lets git metadata leak into the report")
}

func TestRunAllureCapsTheBuildHeap(t *testing.T) {
	t.Setenv("NODE_OPTIONS", "--max-old-space-size=9999")
	dump := filepath.Join(t.TempDir(), "node-options")
	cli := "#!/bin/sh\nprintf '%s' \"$NODE_OPTIONS\" > \"" + dump + "\"\nprintf 'fresh' > \"$4/index.html\"\n"
	g := newTestGenerator(t, fakeCLI(t, cli), "demo")
	g.heapMB = 512

	require.NoError(t, g.Generate(t.Context(), "demo"))

	assert.Equal(t, "--max-old-space-size=9999 --max-old-space-size=512", string(readFile(t, dump)), "the cap has to come last to win")
}

// The CLI prints its diagnostic last, so stderr capped in size has to keep its
// end, not its start.
func TestRunAllureKeepsTheTailOfAFloodedStderr(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFloodStderr), "demo")

	err := g.Generate(t.Context(), "demo")
	require.Error(t, err)
	got := err.Error()

	assert.Contains(t, got, "TAIL-MARKER", "want the end of stderr, where the CLI puts its diagnostic")
	assert.NotContains(t, got, "HEAD-ONLY-MARKER", "the flood was kept and the diagnostic dropped")
	assert.LessOrEqual(t, len(got), maxStderrBytes+512, "want stderr truncated to about %d bytes", maxStderrBytes)
}

func TestGenerateInvokesTheCLIWithConfigPath(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "argv")
	g := newTestGenerator(t, fakeCLI(t, cliRecordArgv(dump)), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	raw, err := os.ReadFile(dump)
	require.NoError(t, err)
	argv := strings.Split(strings.TrimSpace(string(raw)), "\n")

	got, ok := flagValue(argv, "--config")
	require.True(t, ok, "argv = %q, want it to carry --config", argv)
	assert.Equal(t, ".json", filepath.Ext(got), "the CLI ignores any other extension without saying so")
	tmp := projects.TmpRoot(g.projectsDir, "demo")
	assert.True(t, strings.HasPrefix(got, tmp+string(filepath.Separator)), "--config = %q, want it written under %q", got, tmp)
}

func TestGenerateWritesTheLimitIntoTheConfig(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "config")
	g := newTestGenerator(t, fakeCLI(t, cliDumpConfig(dump)), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	raw, err := os.ReadFile(dump)
	require.NoError(t, err)

	var got struct {
		HistoryLimit *int `json:"historyLimit"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.NotNil(t, got.HistoryLimit, "config = %s, want a historyLimit key spelled exactly that way", raw)
	assert.Equal(t, testHistoryLimit, *got.HistoryLimit)
}

func TestWriteAllureConfigKeepsAZeroLimit(t *testing.T) {
	dir := t.TempDir()

	path, err := writeAllureConfig(dir, 0, filepath.Join(dir, "history.jsonl"), 1, "demo", testBaseURL)
	require.NoError(t, err)

	raw := readFile(t, path)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Contains(t, got, "historyLimit", "want the key even when it is zero")
	assert.Equal(t, float64(0), got["historyLimit"])
}

func TestWriteAllureConfigWiresTheReportURLPlugin(t *testing.T) {
	dir := t.TempDir()

	path, err := writeAllureConfig(dir, testHistoryLimit, filepath.Join(dir, "history.jsonl"), 4, "demo", testBaseURL)
	require.NoError(t, err)

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
	require.NoError(t, json.Unmarshal(raw, &got))

	plugin := got.Plugins.ReportURL

	assert.Equal(t, testBaseURL+"/projects/demo/reports/4/index.html", plugin.Options.URL)
	require.NotEmpty(t, plugin.Import, "config = %s, want the plugin's import path", raw)
	assert.Equal(t, ".mjs", filepath.Ext(plugin.Import), "Node reads .js next to no package.json as CommonJS")
	assert.FileExists(t, plugin.Import)

	assert.Equal(t, []string{"parentSuite", "suite", "subSuite"}, got.Plugins.Awesome.Options.GroupBy)
}

func TestReportURLPluginSetsTheReportURL(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, cannot execute the plugin")
	}

	dir := t.TempDir()

	pluginPath, err := writeReportURLPlugin(dir)
	require.NoError(t, err)

	harness := filepath.Join(dir, "harness.mjs")
	body := "import Plugin from " + strconv.Quote(pluginPath) + ";\n" +
		"const plugin = new Plugin({ url: \"https://allure.example.test/projects/demo/reports/7/index.html\" });\n" +
		"const context = {};\n" +
		"await plugin.start(context);\n" +
		"console.log(context.reportUrl);\n"
	require.NoError(t, os.WriteFile(harness, []byte(body), 0o644))

	out, err := exec.CommandContext(t.Context(), node, harness).CombinedOutput()
	require.NoError(t, err, "%s", out)
	assert.Equal(t, "https://allure.example.test/projects/demo/reports/7/index.html", strings.TrimSpace(string(out)))
}

func TestReportURLForIsAbsolute(t *testing.T) {
	got := reportURLFor(testBaseURL, "demo", 4)

	assert.Equal(t, testBaseURL+"/projects/demo/reports/4/index.html", got)

	parsed, err := url.Parse(got)
	require.NoError(t, err)
	assert.NotEmpty(t, parsed.Scheme, "new URL() rejects a url without a scheme")
	assert.NotEmpty(t, parsed.Host, "new URL() rejects a url without a host")
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
	require.NoError(t, os.WriteFile(harness, []byte(body), 0o644))

	out, err := exec.CommandContext(t.Context(), node, harness).CombinedOutput()
	require.NoError(t, err, "new URL() rejected the report url, which is what kills the page:\n%s", out)
	assert.Equal(t, "ok", strings.TrimSpace(string(out)))
}

func TestGenerateWritesThePluginBesideTheConfig(t *testing.T) {
	dump := filepath.Join(t.TempDir(), "config")
	g := newTestGenerator(t, fakeCLI(t, cliDumpConfig(dump)), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	var got struct {
		Plugins struct {
			ReportURL struct {
				Import string `json:"import"`
			} `json:"reporturl"`
		} `json:"plugins"`
	}
	require.NoError(t, json.Unmarshal(readFile(t, dump), &got))

	imported := got.Plugins.ReportURL.Import
	tmp := projects.TmpRoot(g.projectsDir, "demo")
	assert.True(t, strings.HasPrefix(imported, tmp+string(filepath.Separator)), "plugin import = %q, want it written under %q", imported, tmp)
	assert.FileExists(t, imported, "want the plugin in place while the CLI is running")
}

func TestGetNextBuildNumberWithNoReportsIsOne(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	got, err := g.getNextBuildNumber("demo")
	require.NoError(t, err)
	assert.Equal(t, 1, got)
}

func TestGetNextBuildNumberIsMaxPlusOne(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")

	for _, name := range []string{"1", "2", "7", "9", "10", "latest"} {
		require.NoError(t, os.Mkdir(filepath.Join(reports, name), 0o755))
	}

	got, err := g.getNextBuildNumber("demo")
	require.NoError(t, err)
	assert.Equal(t, 11, got, "max numeric name 10, plus one; \"latest\" ignored")
}

func TestGetNextBuildNumberIgnoresNonDirEntries(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")

	require.NoError(t, os.WriteFile(filepath.Join(reports, "5"), []byte("x"), 0o644))

	got, err := g.getNextBuildNumber("demo")
	require.NoError(t, err)
	assert.Equal(t, 1, got, "the stray file must be ignored")
}

func TestWriteExecutorSkipsFirstBuild(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeExecutor(dir, "demo", testBaseURL, 1))

	_, err := os.Stat(filepath.Join(dir, projects.ExecutorFileName))
	assert.ErrorIs(t, err, os.ErrNotExist, "want no executor.json for the first build")
}

func TestWriteExecutorWritesExpectedFields(t *testing.T) {
	dir := t.TempDir()

	require.NoError(t, writeExecutor(dir, "demo", testBaseURL, 3))

	raw, err := os.ReadFile(filepath.Join(dir, projects.ExecutorFileName))
	require.NoError(t, err)

	var got executorFile
	require.NoError(t, json.Unmarshal(raw, &got))

	want := executorFile{
		BuildOrder: 3,
		BuildName:  "demo #3",
		ReportName: "demo #3",
		ReportURL:  testBaseURL + "/projects/demo/reports/3/index.html",
	}
	assert.Equal(t, want, got)

	for _, key := range []string{`"name"`, `"type"`, `"url"`, `"buildUrl"`} {
		assert.NotContains(t, string(raw), key)
	}
}

func TestGenerateSkipsExecutorOnFirstBuild(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	executorPath := filepath.Join(projects.ResultsDir(g.projectsDir, "demo"), projects.ExecutorFileName)
	_, err := os.Stat(executorPath)
	assert.ErrorIs(t, err, os.ErrNotExist, "want no executor.json after the first build")
}

func TestGenerateWritesExecutorWhenAPreviousBuildIsArchived(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")
	require.NoError(t, os.Mkdir(filepath.Join(reports, "3"), 0o755))

	require.NoError(t, g.Generate(t.Context(), "demo"))

	executorPath := filepath.Join(projects.ResultsDir(g.projectsDir, "demo"), projects.ExecutorFileName)
	raw, err := os.ReadFile(executorPath)
	require.NoError(t, err)

	var got executorFile
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, 4, got.BuildOrder, "one past the archived build 3")
}

func TestGenerateArchivesFirstBuildAtNumberOne(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	archived := filepath.Join(projects.NumberedReportDir(g.projectsDir, "demo", 1), "index.html")
	body, err := os.ReadFile(archived)
	require.NoError(t, err)
	assert.Equal(t, "fresh", string(body))
}

func TestGenerateArchivesUnderTheNextBuildNumber(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")
	require.NoError(t, os.Mkdir(filepath.Join(reports, "3"), 0o755))

	require.NoError(t, g.Generate(t.Context(), "demo"))

	archived := filepath.Join(projects.NumberedReportDir(g.projectsDir, "demo", 4), "index.html")
	assert.FileExists(t, archived)
}

func TestGenerateArchiveFailureDoesNotFailTheBuild(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	reports := projects.ReportsDir(g.projectsDir, "demo")
	require.NoError(t, os.WriteFile(filepath.Join(reports, "1"), []byte("not a directory"), 0o644))

	require.NoError(t, g.Generate(t.Context(), "demo"), "archiving is best-effort")

	assert.Equal(t, "fresh", readLatest(t, g, "demo"), "a failed archive must not affect publishing")
}

func TestGenerateLeavesNoPartialArchiveBehind(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliUnarchivable), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"), "archiving is best-effort")

	archived := projects.NumberedReportDir(g.projectsDir, "demo", 1)
	_, err := os.Stat(archived)
	assert.ErrorIs(t, err, os.ErrNotExist, "a partial archive was published at build 1")

	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestGenerateSkipsTheArchiveWhenHistoryIsOff(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliUnarchivable), "demo")
	g.historyLimit = 0

	require.NoError(t, g.Generate(t.Context(), "demo"))

	staged := filepath.Join(projects.TmpRoot(g.projectsDir, "demo"), "archive")
	_, err := os.Stat(staged)
	assert.ErrorIs(t, err, os.ErrNotExist, "want no archiving with the limit at 0")

	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestPruneReportsKeepsAllWhenUnderTheLimit(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, projects.CreateDir(dir, "demo"))
	g := New(dir, "unused-cli", 3, testBaseURL, testMaxBuilds, 0)

	reports := projects.ReportsDir(dir, "demo")
	for _, name := range []string{"1", "2"} {
		require.NoError(t, os.Mkdir(filepath.Join(reports, name), 0o755))
	}

	require.NoError(t, g.pruneReports("demo"))

	for _, name := range []string{"1", "2"} {
		assert.DirExists(t, filepath.Join(reports, name), "fewer builds than the limit, want all kept")
	}
}

func TestPruneReportsDeletesOldestByNumber(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, projects.CreateDir(dir, "demo"))
	g := New(dir, "unused-cli", 3, testBaseURL, testMaxBuilds, 0)

	reports := projects.ReportsDir(dir, "demo")

	for _, name := range []string{"1", "2", "3", "7", "9", "10", "latest"} {
		require.NoError(t, os.Mkdir(filepath.Join(reports, name), 0o755))
	}

	require.NoError(t, g.pruneReports("demo"))

	for _, name := range []string{"1", "2", "3"} {
		_, err := os.Stat(filepath.Join(reports, name))
		assert.ErrorIs(t, err, os.ErrNotExist, "want the three oldest builds pruned")
	}
	for _, name := range []string{"7", "9", "10", "latest"} {
		assert.DirExists(t, filepath.Join(reports, name), "want the newest builds and latest kept")
	}
}

func TestPruneReportsReadDirErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	g := New(dir, "unused-cli", 3, testBaseURL, testMaxBuilds, 0)

	require.Error(t, g.pruneReports("missing"))
}

func TestGenerateContinuesWhenPruneReportsFails(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, projects.CreateDir(dir, "demo"))
	writeResult(t, dir, "demo")
	g := New(dir, "unused-cli", testHistoryLimit, testBaseURL, testMaxBuilds, 0)

	reports := projects.ReportsDir(dir, "demo")

	g.allureBin = fakeCLI(t, "#!/bin/sh\n"+
		"chmod 300 \""+reports+"\"\n"+
		"printf 'fresh' > \"$4/index.html\"\n")

	t.Cleanup(func() { _ = os.Chmod(reports, 0o755) })

	require.NoError(t, g.Generate(t.Context(), "demo"), "a failed prune must not fail the build")
	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestGenerateAccumulatesHistoryAcrossBuilds(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliHistory), "demo")

	const builds = 3
	for i := range builds {
		require.NoError(t, g.Generate(t.Context(), "demo"), "build %d", i+1)
	}

	b, err := os.ReadFile(projects.HistoryFile(g.projectsDir, "demo"))
	require.NoError(t, err)
	assert.Equal(t, builds, strings.Count(string(b), "\n"), "runs in history: %q", b)
}

func TestFailedBuildLeavesHistoryIntact(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliWreckHistory), "demo")

	history := projects.HistoryFile(g.projectsDir, "demo")
	const want = "run one\nrun two\n"
	require.NoError(t, os.WriteFile(history, []byte(want), 0o644))

	require.Error(t, g.Generate(t.Context(), "demo"))

	got, err := os.ReadFile(history)
	require.NoError(t, err)
	assert.Equal(t, want, string(got), "want history untouched by a failed build")
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

	require.NoError(t, g.Generate(t.Context(), "demo"))
	index := filepath.Join(projects.LatestReportDir(g.projectsDir, "demo"), "index.html")
	assert.FileExists(t, index)
	assert.FileExists(t, projects.HistoryFile(g.projectsDir, "demo"))
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
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Generate did not return after its context was canceled")
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
	require.FailNow(t, "status never reached the wanted state", "project %q, want %q, last: %+v, exists=%v", projectID, want, st, ok)
	return Status{}
}

func TestStartReturnsBeforeTheBuildFinishes(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	began := time.Now()
	require.NoError(t, g.Start(t.Context(), "demo"))

	assert.LessOrEqual(t, time.Since(began), 200*time.Millisecond, "want Start to return while the build runs")

	st, ok := g.Status("demo")
	require.True(t, ok)
	require.Equal(t, StateRunning, st.State)
	assert.NotZero(t, st.StartedAt)

	waitForState(t, g, "demo", StateSucceeded)
}

func cliExclusive(lockDir string) string {
	return "#!/bin/sh\nmkdir \"" + lockDir + "\" || exit 1\nsleep 0.5\n" +
		"printf 'fresh' > \"$4/index.html\"\nrmdir \"" + lockDir + "\"\n"
}

func waitUntilFinished(t *testing.T, g *Generator, projectID string) Status {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		st, ok := g.Status(projectID)
		if ok && st.State != StateRunning {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}

	st, ok := g.Status(projectID)
	require.FailNow(t, "build never finished", "project %q, last: %+v, exists=%v", projectID, st, ok)
	return Status{}
}

func TestStartQueuesWhenAllSlotsAreTaken(t *testing.T) {
	cli := fakeCLI(t, cliExclusive(filepath.Join(t.TempDir(), "running")))
	g := newTestGenerator(t, cli, "a", "b")
	g.slots = make(chan struct{}, 1)

	for _, id := range []string{"a", "b"} {
		require.NoError(t, g.Start(t.Context(), id), "a full generator queues, it does not refuse")
	}

	st, _ := g.Status("b")
	assert.Equal(t, StateRunning, st.State, "status of the queued build")

	for _, id := range []string{"a", "b"} {
		st := waitUntilFinished(t, g, id)
		assert.Equal(t, StateSucceeded, st.State, "build of %s (%v): with one slot the builds must not overlap", id, st.Err)
	}
}

// A build queued behind its project's lock - an export a slow client is still
// reading - must not hold a build slot meanwhile, or other projects wait on a
// build that is doing nothing.
func TestBuildWaitingForItsProjectHoldsNoSlot(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "a", "b")
	g.slots = make(chan struct{}, 1)

	lock := g.lockFor("a")
	lock.Lock()

	require.NoError(t, g.Start(t.Context(), "a"))

	time.Sleep(100 * time.Millisecond)

	require.NoError(t, g.Start(t.Context(), "b"))
	st := waitUntilFinished(t, g, "b")
	require.Equal(t, StateSucceeded, st.State, "build of b: %v", st.Err)

	lock.Unlock()
	waitForState(t, g, "a", StateSucceeded)
}

// The slot cap lives in Generate, so a direct call obeys it as well as one
// made through Start: with every slot taken it waits, and gives up only when
// its context does.
func TestGenerateWaitsForASlotUntilItsContextEnds(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")
	g.slots = make(chan struct{}, 1)
	g.slots <- struct{}{}

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Generate(ctx, "demo") }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Generate kept waiting for a slot after its context ended")
	}
	_, err := os.Stat(projects.LatestReportDir(g.projectsDir, "demo"))
	assert.ErrorIs(t, err, os.ErrNotExist, "a build that never got a slot published a report")
}

func TestNewTreatsNoSlotsAsOne(t *testing.T) {
	g := New(t.TempDir(), "unused-cli", testHistoryLimit, testBaseURL, 0, 0)

	assert.Equal(t, 1, cap(g.slots), "with no slots, every build would wait forever")
}

func TestStartRecordsSuccessAndPublishesReport(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Start(t.Context(), "demo"))

	st := waitForState(t, g, "demo", StateSucceeded)
	assert.NoError(t, st.Err)
	assert.NotZero(t, st.FinishedAt)
	assert.False(t, st.FinishedAt.Before(st.StartedAt), "finished %v before it started %v", st.FinishedAt, st.StartedAt)
	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestStartRecordsFailure(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliFail), "demo")
	writeLatest(t, g, "demo", "stale")

	require.NoError(t, g.Start(t.Context(), "demo"))

	st := waitForState(t, g, "demo", StateFailed)
	require.Error(t, st.Err, "without an error the caller has no way to learn why the build failed")
	assert.ErrorContains(t, st.Err, "boom: broken results", "want the CLI stderr in the error")
	assert.Equal(t, "stale", readLatest(t, g, "demo"))
}

func TestStartRejectsASecondBuildOfTheSameProject(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	require.NoError(t, g.Start(t.Context(), "demo"))

	err := g.Start(t.Context(), "demo")
	require.ErrorIs(t, err, ErrAlreadyRunning)

	waitForState(t, g, "demo", StateSucceeded)
	require.NoError(t, g.Start(t.Context(), "demo"))
	waitForState(t, g, "demo", StateSucceeded)
}

func TestTryStartClaimsExactlyOnceUnderConcurrency(t *testing.T) {

	const rounds, callers = 200, 40

	for round := range rounds {
		g := New("unused-dir", "unused-cli", testHistoryLimit, testBaseURL, testMaxBuilds, 0)

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

		require.Len(t, won, 1, "round %d: want exactly one of %d callers to claim the project", round, callers)
	}
}

func TestStatusStaysReadableWhileABuildRuns(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	require.NoError(t, g.Start(t.Context(), "demo"))

	answered := make(chan struct{})
	go func() {
		defer close(answered)
		g.Status("demo")
	}()

	select {
	case <-answered:
	case <-time.After(200 * time.Millisecond):
		require.FailNow(t, "Status blocked while a build was running: the build is holding g.mu")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestStartIgnoresTheCallersCancellation(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliSlow), "demo")

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, g.Start(ctx, "demo"))

	cancel()

	st := waitForState(t, g, "demo", StateSucceeded)
	assert.NoError(t, st.Err, "want the build to finish after the caller went away")
	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestStartRejectsUnknownAndMalformedProjects(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	assert.ErrorIs(t, g.Start(t.Context(), "missing"), ErrProjectNotFound)
	assert.Error(t, g.Start(t.Context(), "../escape"))

	_, ok := g.Status("missing")
	assert.False(t, ok, "rejected Start left a status behind")
}

func TestClearResultsRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	require.Error(t, g.ClearResults("../escape"))
}

func TestClearResultsUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.ClearResults("missing")
	require.ErrorIs(t, err, ErrProjectNotFound)
}

func TestClearResultsClearsFiles(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	require.NoError(t, g.ClearResults("demo"))

	entries, err := os.ReadDir(projects.ResultsDir(g.projectsDir, "demo"))
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestClearResultsSerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.ClearResults("demo") }()

	select {
	case err := <-done:
		require.FailNow(t, "ClearResults returned while the project lock was held", "err = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ClearResults did not proceed after the project lock was released")
	}
}

func TestClearHistoryRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	require.Error(t, g.ClearHistory(t.Context(), "../escape"))
}

func TestClearHistoryUnknownProject(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	err := g.ClearHistory(t.Context(), "missing")
	require.ErrorIs(t, err, ErrProjectNotFound)
}

func TestClearHistoryClearsAndTriggersRebuild(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	archive := projects.NumberedReportDir(g.projectsDir, "demo", 1)
	require.NoError(t, os.MkdirAll(archive, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(archive, "index.html"), []byte("old"), 0644))
	require.NoError(t, os.WriteFile(projects.HistoryFile(g.projectsDir, "demo"), []byte(`{"n":1}`), 0644))
	executor := filepath.Join(projects.ResultsDir(g.projectsDir, "demo"), projects.ExecutorFileName)
	require.NoError(t, os.WriteFile(executor, []byte(`{"buildOrder":5}`), 0644))

	require.NoError(t, g.ClearHistory(t.Context(), "demo"))

	_, err := os.Stat(archive)
	assert.ErrorIs(t, err, os.ErrNotExist, "archive still exists")
	_, err = os.Stat(executor)
	assert.ErrorIs(t, err, os.ErrNotExist, "executor.json still exists")

	st := waitForState(t, g, "demo", StateSucceeded)
	assert.NoError(t, st.Err, "triggered rebuild failed")
	assert.Equal(t, "fresh", readLatest(t, g, "demo"))
}

func TestClearHistoryRefusesWhenResultsAreEmpty(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, projects.CreateDir(dir, "demo"))
	g := New(dir, fakeCLI(t, cliOK), testHistoryLimit, testBaseURL, testMaxBuilds, 0)

	err := g.ClearHistory(t.Context(), "demo")
	require.ErrorIs(t, err, ErrNoResults)
}

func TestClearHistorySerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.ClearHistory(context.Background(), "demo") }()

	select {
	case err := <-done:
		require.FailNow(t, "ClearHistory returned while the project lock was held", "err = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ClearHistory did not proceed after the project lock was released")
	}

	waitForState(t, g, "demo", StateSucceeded)
}

func TestDeleteRejectsBadProjectID(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	require.Error(t, g.Delete("../escape"))
}

func TestDeleteUnknownProjectSucceeds(t *testing.T) {
	g := newTestGenerator(t, "unused-cli")

	require.NoError(t, g.Delete("nosuch"))
}

func TestDeleteRemovesTheWholeProjectTree(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Generate(t.Context(), "demo"))

	require.DirExists(t, projects.LatestReportDir(g.projectsDir, "demo"), "setup: no report to delete")

	require.NoError(t, g.Delete("demo"))

	_, err := os.Stat(projects.ProjectDir(g.projectsDir, "demo"))
	assert.ErrorIs(t, err, os.ErrNotExist, "project dir still there after Delete")
}

func TestDeleteForgetsTheProjectStatus(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Start(t.Context(), "demo"))
	waitForState(t, g, "demo", StateSucceeded)

	require.NoError(t, g.Delete("demo"))

	_, ok := g.Status("demo")
	assert.False(t, ok, "want no status at all after Delete")
}

// A build goroutine writing its status after the project was deleted must not
// bring that status back.
func TestDeleteOutlastsALateStatusWrite(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	require.NoError(t, g.Start(t.Context(), "demo"))
	waitForState(t, g, "demo", StateSucceeded)

	require.NoError(t, g.Delete("demo"))

	g.setStatus("demo", Status{State: StateSucceeded, StartedAt: time.Now()})

	_, ok := g.Status("demo")
	assert.False(t, ok, "want no status at all after a late write")
}

func TestDeleteKeepsTheProjectLock(t *testing.T) {
	g := newTestGenerator(t, "unused-cli", "demo")

	before := g.lockFor("demo")
	require.NoError(t, g.Delete("demo"))
	after := g.lockFor("demo")

	assert.Same(t, before, after, "two callers could hold different locks for one project")
}

func TestDeleteSerializesWithABuildInFlight(t *testing.T) {
	g := newTestGenerator(t, fakeCLI(t, cliOK), "demo")

	held := g.lockFor("demo")
	held.Lock()

	done := make(chan error, 1)
	go func() { done <- g.Delete("demo") }()

	select {
	case err := <-done:
		require.FailNow(t, "Delete returned while the project lock was held", "err = %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	held.Unlock()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "Delete did not proceed after the project lock was released")
	}
}
