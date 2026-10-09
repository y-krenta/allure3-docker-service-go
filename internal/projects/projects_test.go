package projects

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateProjectID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{

		{"numeric", "1234567890", false},
		{"single digit", "1", false},
		{"single letter", "a", false},
		{"word", "demo", false},
		{"default", "default", false},

		{"hyphen inside", "my-project", false},
		{"underscore inside", "my_project", false},
		{"space inside", "my project", false},
		{"mixed separators", "my_project-1 demo", false},
		{"leading zeros", "000123", false},

		{"length 199", strings.Repeat("a", 199), false},
		{"length 200", strings.Repeat("a", 200), false},
		{"length 201", strings.Repeat("a", 201), true},

		{"empty", "", true},

		{"leading hyphen", "-abc", true},
		{"leading underscore", "_abc", true},
		{"leading space", " abc", true},

		{"trailing hyphen", "abc-", true},
		{"trailing underscore", "abc_", true},
		{"trailing space", "abc ", true},

		{"uppercase letters", "ABC123", true},
		{"tab", "abc\t123", true},
		{"newline", "abc\n123", true},
		{"plus sign", "abc+123", true},
		{"dot", "abc.123", true},
		{"slash", "abc/123", true},
		{"backslash", `abc\123`, true},
		{"path traversal", "../abc", true},
		{"unicode digits", "１２３", true},
		{"emoji", "abc😀123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectID(tt.id)

			if tt.wantErr {
				assert.Error(t, err, tt.id)
			} else {
				assert.NoError(t, err, tt.id)
			}
		})
	}
}

func TestSanitizeResultFileName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{

		{"simple json", "8f2c-result.json", "8f2c-result.json", false},
		{"letters digits dot dash underscore", "abc_123-test.json", "abc_123-test.json", false},
		{"single character", "a", "a", false},
		{"max length 255", strings.Repeat("a", 255), strings.Repeat("a", 255), false},

		{"unix traversal", "../../etc/passwd", "passwd", false},
		{"unix nested path", "dir/sub/file.json", "file.json", false},
		{"trailing slash", "dir/", "dir", false},

		{"hidden file", ".hidden.json", ".hidden.json", false},
		{"many dots", "....", "....", false},

		{"windows traversal on unix", `..\..\etc\passwd`, "", true},
		{"windows style path on unix", `dir\sub\file.json`, "", true},

		{"empty", "", "", true},
		{"dot", ".", "", true},
		{"dot dot", "..", "", true},
		{"path separator", string(filepath.Separator), "", true},

		{"length 256", strings.Repeat("a", 256), "", true},

		{"space", "rep ort.json", "", true},
		{"tab", "rep\tort.json", "", true},
		{"newline", "rep\nort.json", "", true},
		{"plus", "rep+ort.json", "", true},
		{"colon", "rep:ort.json", "", true},
		{"backslash", `rep\ort.json`, "", true},
		{"unicode", "отчет.json", "", true},
		{"emoji", "report😀.json", "", true},
		{"asterisk", "report*.json", "", true},
		{"question mark", "report?.json", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SanitizeResultFileName(tt.input)

			if tt.wantErr {
				assert.Error(t, err, tt.input)
				return
			}
			require.NoError(t, err, tt.input)
			assert.Equal(t, tt.want, got, tt.input)
		})
	}
}

func ExampleSanitizeResultFileName() {
	for _, in := range []string{
		"8f2c-result.json",
		"../../etc/passwd",
		"..",
		"rep ort.json",
	} {
		name, err := SanitizeResultFileName(in)
		fmt.Printf("%q -> %q, %v\n", in, name, err)
	}

}

func TestCreateDir(t *testing.T) {
	t.Run("creates reports and results", func(t *testing.T) {
		base := t.TempDir()

		require.NoError(t, CreateDir(base, "demo"))

		assert.DirExists(t, ReportsDir(base, "demo"))
		assert.DirExists(t, ResultsDir(base, "demo"))
	})

	t.Run("existing project reports ErrProjectExists", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))

		assert.ErrorIs(t, CreateDir(base, "demo"), ErrProjectExists)
	})

	t.Run("plain file in place of project dir reports ErrProjectExists", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(base, "demo"), nil, 0644))

		assert.ErrorIs(t, CreateDir(base, "demo"), ErrProjectExists)
	})

	t.Run("missing base dir fails without creating anything", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "does-not-exist")

		err := CreateDir(base, "demo")
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrProjectExists)

		_, err = os.Stat(base)
		assert.ErrorIs(t, err, os.ErrNotExist, "base dir was created, want it left alone")
	})
}

func TestClearResults(t *testing.T) {
	t.Run("removes top-level files, leaves subdirectories", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))
		results := ResultsDir(base, "demo")
		require.NoError(t, os.WriteFile(filepath.Join(results, "a-result.json"), []byte("{}"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(results, "b-result.json"), []byte("{}"), 0644))
		sub := filepath.Join(results, "kept-dir")
		require.NoError(t, os.MkdirAll(sub, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(sub, "inside.json"), []byte("{}"), 0644))

		require.NoError(t, ClearResults(base, "demo"))

		entries, err := os.ReadDir(results)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "kept-dir", entries[0].Name())
		assert.FileExists(t, filepath.Join(sub, "inside.json"))
	})

	t.Run("already-empty results directory succeeds", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))

		assert.NoError(t, ClearResults(base, "demo"))
	})

	t.Run("missing results directory reports the error", func(t *testing.T) {
		base := t.TempDir()

		assert.ErrorIs(t, ClearResults(base, "nosuch"), os.ErrNotExist)
	})
}

func TestClearHistory(t *testing.T) {

	writeArchive := func(t *testing.T, base, id, name string) {
		t.Helper()
		dir := filepath.Join(ReportsDir(base, id), name)
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>"+name+"</h1>"), 0644))
	}

	t.Run("removes numbered archives, leaves latest and non-numeric entries", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))
		writeArchive(t, base, "demo", "latest")
		writeArchive(t, base, "demo", "1")
		writeArchive(t, base, "demo", "2")
		require.NoError(t, os.WriteFile(filepath.Join(ReportsDir(base, "demo"), "notes.txt"), nil, 0644))

		require.NoError(t, ClearHistory(base, "demo"))

		assert.DirExists(t, filepath.Join(ReportsDir(base, "demo"), "latest"))
		assert.FileExists(t, filepath.Join(ReportsDir(base, "demo"), "notes.txt"))
		for _, gone := range []string{"1", "2"} {
			_, err := os.Stat(filepath.Join(ReportsDir(base, "demo"), gone))
			assert.ErrorIs(t, err, os.ErrNotExist, "archive %q still exists", gone)
		}
	})

	t.Run("removes history file and executor.json", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))
		require.NoError(t, os.WriteFile(HistoryFile(base, "demo"), []byte(`{"n":1}`), 0644))
		executor := filepath.Join(ResultsDir(base, "demo"), ExecutorFileName)
		require.NoError(t, os.WriteFile(executor, []byte(`{"buildOrder":5}`), 0644))

		require.NoError(t, ClearHistory(base, "demo"))

		_, err := os.Stat(HistoryFile(base, "demo"))
		assert.ErrorIs(t, err, os.ErrNotExist, "history file still exists")
		_, err = os.Stat(executor)
		assert.ErrorIs(t, err, os.ErrNotExist, "executor.json still exists")
	})

	t.Run("fresh project with nothing to clear succeeds", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))

		assert.NoError(t, ClearHistory(base, "demo"))
	})

	t.Run("missing reports directory reports the error", func(t *testing.T) {
		base := t.TempDir()

		assert.ErrorIs(t, ClearHistory(base, "nosuch"), os.ErrNotExist)
	})
}

// Every build appends to the history file, and the watcher rebuilds on any
// change under results: a history file there would have each build schedule
// the next one, forever.
func TestHistoryFileStaysOutOfResults(t *testing.T) {
	base := t.TempDir()

	history := HistoryFile(base, "demo")
	results := ResultsDir(base, "demo") + string(filepath.Separator)

	assert.Falsef(t, strings.HasPrefix(history, results),
		"HistoryFile = %q, want it outside the results dir %q", history, results)
}

// CleanTmp clears what killed builds left in each project's .tmp, and leaves
// alone anything this service did not create.
func TestCleanTmp(t *testing.T) {

	stageTmp := func(t *testing.T, base, id string) string {
		t.Helper()
		tmp := TmpRoot(base, id)
		dir := filepath.Join(tmp, "build-1")
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>stale</h1>"), 0644))
		return tmp
	}

	captureLog := func(t *testing.T) *bytes.Buffer {
		t.Helper()
		var buf bytes.Buffer
		log.SetOutput(&buf)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		return &buf
	}

	t.Run("removes .tmp from every project", func(t *testing.T) {
		base := t.TempDir()
		var staged []string
		for _, id := range []string{"alpha", "beta", "gamma"} {
			require.NoError(t, CreateDir(base, id))
			staged = append(staged, stageTmp(t, base, id))
		}

		require.NoError(t, os.WriteFile(filepath.Join(base, "README.txt"), []byte("not a project"), 0644))

		require.NoError(t, CleanTmp(base))

		for _, tmp := range staged {
			_, err := os.Stat(tmp)
			assert.ErrorIs(t, err, os.ErrNotExist, "%q still exists after CleanTmp", tmp)
		}
	})

	t.Run("leaves results and reports untouched", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))
		stageTmp(t, base, "demo")
		result := filepath.Join(ResultsDir(base, "demo"), "a-result.json")
		require.NoError(t, os.WriteFile(result, []byte("{}"), 0644))
		published := filepath.Join(LatestReportDir(base, "demo"), "index.html")
		require.NoError(t, os.MkdirAll(LatestReportDir(base, "demo"), 0755))
		require.NoError(t, os.WriteFile(published, []byte("<h1>report</h1>"), 0644))

		require.NoError(t, CleanTmp(base))

		assert.FileExists(t, result)
		assert.FileExists(t, published)
	})

	t.Run("a project without .tmp is not an error", func(t *testing.T) {
		base := t.TempDir()
		require.NoError(t, CreateDir(base, "demo"))

		require.NoError(t, CleanTmp(base))
		assert.DirExists(t, ProjectDir(base, "demo"))
	})

	t.Run("empty projects directory is not an error", func(t *testing.T) {
		assert.NoError(t, CleanTmp(t.TempDir()))
	})

	t.Run("skips files and directories that are not projects", func(t *testing.T) {
		base := t.TempDir()
		loose := filepath.Join(base, "README.txt")
		require.NoError(t, os.WriteFile(loose, []byte("not a project"), 0644))

		require.NoError(t, os.WriteFile(filepath.Join(base, "alpha"), []byte("not a project"), 0644))
		foreign := []string{"NotAProject", ".hidden"}
		var kept []string
		for _, name := range foreign {
			dir := filepath.Join(base, name, ".tmp")
			require.NoError(t, os.MkdirAll(dir, 0755))
			kept = append(kept, dir)
		}

		logged := captureLog(t)
		require.NoError(t, CleanTmp(base))

		assert.Empty(t, logged.String(), "CleanTmp logged about a non-project entry")
		assert.FileExists(t, loose)
		for _, dir := range kept {
			assert.DirExists(t, dir, "CleanTmp removed a directory that is not a project")
		}
	})

	t.Run("a project that cannot be cleaned does not stop the sweep", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		base := t.TempDir()
		for _, id := range []string{"alpha", "beta"} {
			require.NoError(t, CreateDir(base, id))
			stageTmp(t, base, id)
		}

		locked := ProjectDir(base, "alpha")
		require.NoError(t, os.Chmod(locked, 0500))
		t.Cleanup(func() { assert.NoError(t, os.Chmod(locked, 0755)) })

		logged := captureLog(t)
		require.NoError(t, CleanTmp(base))

		assert.Contains(t, logged.String(), TmpRoot(base, "alpha"))
		_, err := os.Stat(TmpRoot(base, "beta"))
		assert.ErrorIs(t, err, os.ErrNotExist, "beta was not cleaned after alpha failed")
	})

	t.Run("missing projects directory reports the error", func(t *testing.T) {
		assert.ErrorIs(t, CleanTmp(filepath.Join(t.TempDir(), "nosuch")), os.ErrNotExist)
	})
}

// After seeding, the target's history is exactly the source's - an empty one
// included - with nothing left behind and nothing written outside baseDir.
func TestSeedHistory(t *testing.T) {

	seedProject := func(t *testing.T, base, id, content string) {
		t.Helper()
		require.NoError(t, CreateDir(base, id))
		if content == "" {
			return
		}
		require.NoError(t, os.WriteFile(HistoryFile(base, id), []byte(content), 0644))
	}

	projectFiles := func(t *testing.T, base, id string) []string {
		t.Helper()
		entries, err := os.ReadDir(ProjectDir(base, id))
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		return names
	}

	t.Run("copies the source history over the target's own", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n{\"run\":2}\n")
		seedProject(t, base, "dst", "{\"stale\":true}\n")

		require.NoError(t, SeedHistory(base, "dst", "src"))

		got, err := os.ReadFile(HistoryFile(base, "dst"))
		require.NoError(t, err)
		assert.Equal(t, "{\"run\":1}\n{\"run\":2}\n", string(got))
	})

	t.Run("leaves the source history untouched", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		require.NoError(t, SeedHistory(base, "dst", "src"))

		got, err := os.ReadFile(HistoryFile(base, "src"))
		require.NoError(t, err)
		assert.Equal(t, "{\"run\":1}\n", string(got))
	})

	t.Run("target with no history of its own is seeded", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		require.NoError(t, SeedHistory(base, "dst", "src"))

		assert.FileExists(t, HistoryFile(base, "dst"))
	})

	t.Run("source without history clears the target and reports ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "")
		seedProject(t, base, "dst", "{\"stale\":true}\n")

		require.ErrorIs(t, SeedHistory(base, "dst", "src"), ErrNoHistory)

		_, err := os.Stat(HistoryFile(base, "dst"))
		assert.ErrorIs(t, err, os.ErrNotExist, "target history survived an empty source")
	})

	t.Run("neither project has history and it still reports ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "")
		seedProject(t, base, "dst", "")

		assert.ErrorIs(t, SeedHistory(base, "dst", "src"), ErrNoHistory)
	})

	t.Run("missing target project reports fs.ErrNotExist, not ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()

		for _, sourceHistory := range []string{"{\"run\":1}\n", ""} {
			srcID := fmt.Sprintf("src-%d", len(sourceHistory))
			seedProject(t, base, srcID, sourceHistory)

			err := SeedHistory(base, "nosuch", srcID)
			assert.ErrorIs(t, err, os.ErrNotExist, "source history %q", sourceHistory)
			assert.NotErrorIs(t, err, ErrNoHistory, "source history %q", sourceHistory)
		}
	})

	t.Run("missing source project reports ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "dst", "")

		assert.ErrorIs(t, SeedHistory(base, "dst", "nosuch"), ErrNoHistory)
	})

	t.Run("leaves no staging file behind", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		before := projectFiles(t, base, "dst")

		require.NoError(t, SeedHistory(base, "dst", "src"))

		assert.ElementsMatch(t, append(slices.Clone(before), "history.jsonl"), projectFiles(t, base, "dst"))
	})

	t.Run("refuses to seed a project from itself", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "dst", "{\"own\":true}\n")

		require.ErrorIs(t, SeedHistory(base, "dst", "dst"), ErrCopyToSelf)

		got, err := os.ReadFile(HistoryFile(base, "dst"))
		require.NoError(t, err, "the refusal touched the history file")
		assert.Equal(t, "{\"own\":true}\n", string(got))
	})

	t.Run("leaves no staging file behind when the rename fails", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		blocker := HistoryFile(base, "dst")
		require.NoError(t, os.MkdirAll(filepath.Join(blocker, "occupied"), 0o755))

		before := projectFiles(t, base, "dst")

		require.Error(t, SeedHistory(base, "dst", "src"), "want the rename to fail")

		assert.ElementsMatch(t, before, projectFiles(t, base, "dst"))
	})

	t.Run("rejects invalid IDs without touching the filesystem", func(t *testing.T) {
		tests := []struct {
			name         string
			target, from string
		}{
			{"target escapes", "../evil", "src"},
			{"source escapes", "dst", "../evil"},
			{"target empty", "", "src"},
			{"source empty", "dst", ""},
			{"target uppercase", "DST", "src"},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				base := t.TempDir()
				outside := filepath.Join(base, "..", "evil")
				seedProject(t, base, "src", "{\"run\":1}\n")
				seedProject(t, base, "dst", "{\"stale\":true}\n")

				err := SeedHistory(base, tt.target, tt.from)
				require.Error(t, err)
				assert.NotErrorIs(t, err, ErrNoHistory)

				_, err = os.Stat(outside)
				assert.ErrorIs(t, err, os.ErrNotExist, "something was written outside baseDir")
				got, err := os.ReadFile(HistoryFile(base, "dst"))
				require.NoError(t, err)
				assert.Equal(t, "{\"stale\":true}\n", string(got))
			})
		}
	})
}
