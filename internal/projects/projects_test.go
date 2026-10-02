package projects

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
				if err == nil {
					t.Fatalf("ValidateProjectID(%q) error = nil, want error", tt.id)
				}
				return
			}

			if err != nil {
				t.Fatalf("ValidateProjectID(%q) error = %v, want nil", tt.id, err)
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
				if err == nil {
					t.Fatalf("SanitizeResultFileName(%q) error = nil, want error", tt.input)
				}
				return
			}

			if err != nil {
				t.Fatalf("SanitizeResultFileName(%q) error = %v, want nil", tt.input, err)
			}

			if got != tt.want {
				t.Fatalf("SanitizeResultFileName(%q) = %q, want %q", tt.input, got, tt.want)
			}
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

		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("CreateDir(base, %q) returned unexpected error: %v", "demo", err)
		}

		for _, dir := range []string{ReportsDir(base, "demo"), ResultsDir(base, "demo")} {
			info, err := os.Stat(dir)
			if err != nil {
				t.Errorf("stat %q: %v", dir, err)
				continue
			}
			if !info.IsDir() {
				t.Errorf("%q exists but is not a directory", dir)
			}
		}
	})

	t.Run("existing project reports ErrProjectExists", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("first CreateDir returned unexpected error: %v", err)
		}

		err := CreateDir(base, "demo")
		if !errors.Is(err, ErrProjectExists) {
			t.Fatalf("second CreateDir error = %v, want ErrProjectExists", err)
		}
	})

	t.Run("plain file in place of project dir reports ErrProjectExists", func(t *testing.T) {
		base := t.TempDir()
		if err := os.WriteFile(filepath.Join(base, "demo"), nil, 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		err := CreateDir(base, "demo")
		if !errors.Is(err, ErrProjectExists) {
			t.Fatalf("CreateDir error = %v, want ErrProjectExists", err)
		}
	})

	t.Run("missing base dir fails without creating anything", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "does-not-exist")

		err := CreateDir(base, "demo")
		if err == nil {
			t.Fatal("CreateDir with a missing base dir returned nil, want error")
		}
		if errors.Is(err, ErrProjectExists) {
			t.Fatalf("CreateDir error = %v, want a filesystem error", err)
		}
		if _, err := os.Stat(base); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("base dir %q was created, want it left alone", base)
		}
	})
}

func TestClearResults(t *testing.T) {
	t.Run("removes top-level files, leaves subdirectories", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}
		results := ResultsDir(base, "demo")
		if err := os.WriteFile(filepath.Join(results, "a-result.json"), []byte("{}"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(results, "b-result.json"), []byte("{}"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		sub := filepath.Join(results, "kept-dir")
		if err := os.MkdirAll(sub, 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(sub, "inside.json"), []byte("{}"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := ClearResults(base, "demo"); err != nil {
			t.Fatalf("ClearResults returned unexpected error: %v", err)
		}

		entries, err := os.ReadDir(results)
		if err != nil {
			t.Fatalf("ReadDir after ClearResults: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "kept-dir" {
			t.Fatalf("results dir after ClearResults = %v, want only kept-dir", entries)
		}
		if _, err := os.Stat(filepath.Join(sub, "inside.json")); err != nil {
			t.Errorf("file inside kept-dir was removed: %v", err)
		}
	})

	t.Run("already-empty results directory succeeds", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := ClearResults(base, "demo"); err != nil {
			t.Fatalf("ClearResults on empty results returned unexpected error: %v", err)
		}
	})

	t.Run("missing results directory reports the error", func(t *testing.T) {
		base := t.TempDir()

		err := ClearResults(base, "nosuch")
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ClearResults error = %v, want it to wrap fs.ErrNotExist", err)
		}
	})
}

func TestClearHistory(t *testing.T) {

	writeArchive := func(t *testing.T, base, id, name string) {
		t.Helper()
		dir := filepath.Join(ReportsDir(base, id), name)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>"+name+"</h1>"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	t.Run("removes numbered archives, leaves latest and non-numeric entries", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}
		writeArchive(t, base, "demo", "latest")
		writeArchive(t, base, "demo", "1")
		writeArchive(t, base, "demo", "2")
		if err := os.WriteFile(filepath.Join(ReportsDir(base, "demo"), "notes.txt"), nil, 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := ClearHistory(base, "demo"); err != nil {
			t.Fatalf("ClearHistory returned unexpected error: %v", err)
		}

		for _, kept := range []string{"latest", "notes.txt"} {
			if _, err := os.Stat(filepath.Join(ReportsDir(base, "demo"), kept)); err != nil {
				t.Errorf("%q was removed, want it kept: %v", kept, err)
			}
		}
		for _, gone := range []string{"1", "2"} {
			if _, err := os.Stat(filepath.Join(ReportsDir(base, "demo"), gone)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("archive %q still exists, want it removed (stat err = %v)", gone, err)
			}
		}
	})

	t.Run("removes history file and executor.json", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(HistoryFile(base, "demo"), []byte(`{"n":1}`), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		executor := filepath.Join(ResultsDir(base, "demo"), ExecutorFileName)
		if err := os.WriteFile(executor, []byte(`{"buildOrder":5}`), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := ClearHistory(base, "demo"); err != nil {
			t.Fatalf("ClearHistory returned unexpected error: %v", err)
		}

		if _, err := os.Stat(HistoryFile(base, "demo")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("history file still exists (stat err = %v)", err)
		}
		if _, err := os.Stat(executor); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("executor.json still exists (stat err = %v)", err)
		}
	})

	t.Run("fresh project with nothing to clear succeeds", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := ClearHistory(base, "demo"); err != nil {
			t.Fatalf("ClearHistory on a fresh project returned unexpected error: %v", err)
		}
	})

	t.Run("missing reports directory reports the error", func(t *testing.T) {
		base := t.TempDir()

		err := ClearHistory(base, "nosuch")
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("ClearHistory error = %v, want it to wrap fs.ErrNotExist", err)
		}
	})
}

// Every build appends to the history file, and the watcher rebuilds on any
// change under results: a history file there would have each build schedule
// the next one, forever.
func TestHistoryFileStaysOutOfResults(t *testing.T) {
	base := t.TempDir()

	history := HistoryFile(base, "demo")
	results := ResultsDir(base, "demo") + string(filepath.Separator)

	if strings.HasPrefix(history, results) {
		t.Errorf("HistoryFile = %q, want it outside the results dir %q", history, results)
	}
}

// CleanTmp clears what killed builds left in each project's .tmp, and leaves
// alone anything this service did not create.
func TestCleanTmp(t *testing.T) {

	stageTmp := func(t *testing.T, base, id string) string {
		t.Helper()
		tmp := TmpRoot(base, id)
		dir := filepath.Join(tmp, "build-1")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>stale</h1>"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
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
			if err := CreateDir(base, id); err != nil {
				t.Fatalf("setup: %v", err)
			}
			staged = append(staged, stageTmp(t, base, id))
		}

		if err := os.WriteFile(filepath.Join(base, "README.txt"), []byte("not a project"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := CleanTmp(base); err != nil {
			t.Fatalf("CleanTmp returned unexpected error: %v", err)
		}

		for _, tmp := range staged {
			if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%q still exists after CleanTmp (stat error = %v)", tmp, err)
			}
		}
	})

	t.Run("leaves results and reports untouched", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}
		stageTmp(t, base, "demo")
		result := filepath.Join(ResultsDir(base, "demo"), "a-result.json")
		if err := os.WriteFile(result, []byte("{}"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		published := filepath.Join(LatestReportDir(base, "demo"), "index.html")
		if err := os.MkdirAll(LatestReportDir(base, "demo"), 0755); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if err := os.WriteFile(published, []byte("<h1>report</h1>"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := CleanTmp(base); err != nil {
			t.Fatalf("CleanTmp returned unexpected error: %v", err)
		}

		for _, path := range []string{result, published} {
			if _, err := os.Stat(path); err != nil {
				t.Errorf("CleanTmp removed %q: %v", path, err)
			}
		}
	})

	t.Run("a project without .tmp is not an error", func(t *testing.T) {
		base := t.TempDir()
		if err := CreateDir(base, "demo"); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := CleanTmp(base); err != nil {
			t.Fatalf("CleanTmp returned unexpected error: %v", err)
		}
		if _, err := os.Stat(ProjectDir(base, "demo")); err != nil {
			t.Errorf("project directory disappeared: %v", err)
		}
	})

	t.Run("empty projects directory is not an error", func(t *testing.T) {
		if err := CleanTmp(t.TempDir()); err != nil {
			t.Fatalf("CleanTmp on empty root returned unexpected error: %v", err)
		}
	})

	t.Run("skips files and directories that are not projects", func(t *testing.T) {
		base := t.TempDir()
		loose := filepath.Join(base, "README.txt")
		if err := os.WriteFile(loose, []byte("not a project"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}

		if err := os.WriteFile(filepath.Join(base, "alpha"), []byte("not a project"), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		foreign := []string{"NotAProject", ".hidden"}
		var kept []string
		for _, name := range foreign {
			dir := filepath.Join(base, name, ".tmp")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatalf("setup: %v", err)
			}
			kept = append(kept, dir)
		}

		logged := captureLog(t)
		if err := CleanTmp(base); err != nil {
			t.Fatalf("CleanTmp returned unexpected error: %v", err)
		}

		if logged.Len() != 0 {
			t.Errorf("CleanTmp logged about a non-project entry: %s", logged.String())
		}
		if _, err := os.Stat(loose); err != nil {
			t.Errorf("CleanTmp removed a loose file: %v", err)
		}
		for _, dir := range kept {
			if _, err := os.Stat(dir); err != nil {
				t.Errorf("CleanTmp removed %q, which is not a project: %v", dir, err)
			}
		}
	})

	t.Run("a project that cannot be cleaned does not stop the sweep", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		base := t.TempDir()
		for _, id := range []string{"alpha", "beta"} {
			if err := CreateDir(base, id); err != nil {
				t.Fatalf("setup: %v", err)
			}
			stageTmp(t, base, id)
		}

		locked := ProjectDir(base, "alpha")
		if err := os.Chmod(locked, 0500); err != nil {
			t.Fatalf("setup: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(locked, 0755); err != nil {
				t.Errorf("cleanup: restoring permissions on %q: %v", locked, err)
			}
		})

		logged := captureLog(t)
		if err := CleanTmp(base); err != nil {
			t.Fatalf("CleanTmp returned an error for one unremovable project: %v", err)
		}

		if !strings.Contains(logged.String(), TmpRoot(base, "alpha")) {
			t.Errorf("CleanTmp did not log the project it could not clean, log = %q", logged.String())
		}
		if _, err := os.Stat(TmpRoot(base, "beta")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("beta was not cleaned after alpha failed (stat error = %v)", err)
		}
	})

	t.Run("missing projects directory reports the error", func(t *testing.T) {
		err := CleanTmp(filepath.Join(t.TempDir(), "nosuch"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CleanTmp error = %v, want it to wrap fs.ErrNotExist", err)
		}
	})
}

// After seeding, the target's history is exactly the source's - an empty one
// included - with nothing left behind and nothing written outside baseDir.
func TestSeedHistory(t *testing.T) {

	seedProject := func(t *testing.T, base, id, content string) {
		t.Helper()
		if err := CreateDir(base, id); err != nil {
			t.Fatalf("setup: %v", err)
		}
		if content == "" {
			return
		}
		if err := os.WriteFile(HistoryFile(base, id), []byte(content), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	projectFiles := func(t *testing.T, base, id string) []string {
		t.Helper()
		entries, err := os.ReadDir(ProjectDir(base, id))
		if err != nil {
			t.Fatalf("failed to read project dir: %v", err)
		}
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

		if err := SeedHistory(base, "dst", "src"); err != nil {
			t.Fatalf("SeedHistory returned unexpected error: %v", err)
		}

		got, err := os.ReadFile(HistoryFile(base, "dst"))
		if err != nil {
			t.Fatalf("reading the seeded history: %v", err)
		}
		want := "{\"run\":1}\n{\"run\":2}\n"
		if string(got) != want {
			t.Errorf("seeded history = %q, want %q", got, want)
		}
	})

	t.Run("leaves the source history untouched", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		if err := SeedHistory(base, "dst", "src"); err != nil {
			t.Fatalf("SeedHistory returned unexpected error: %v", err)
		}

		got, err := os.ReadFile(HistoryFile(base, "src"))
		if err != nil {
			t.Fatalf("reading the source history: %v", err)
		}
		if string(got) != "{\"run\":1}\n" {
			t.Errorf("source history = %q, want it unchanged", got)
		}
	})

	t.Run("target with no history of its own is seeded", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		if err := SeedHistory(base, "dst", "src"); err != nil {
			t.Fatalf("SeedHistory returned unexpected error: %v", err)
		}

		if _, err := os.Stat(HistoryFile(base, "dst")); err != nil {
			t.Errorf("target has no history after seeding: %v", err)
		}
	})

	t.Run("source without history clears the target and reports ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "")
		seedProject(t, base, "dst", "{\"stale\":true}\n")

		err := SeedHistory(base, "dst", "src")
		if !errors.Is(err, ErrNoHistory) {
			t.Fatalf("SeedHistory error = %v, want ErrNoHistory", err)
		}

		if _, err := os.Stat(HistoryFile(base, "dst")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("target history survived an empty source (stat err = %v)", err)
		}
	})

	t.Run("neither project has history and it still reports ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "")
		seedProject(t, base, "dst", "")

		err := SeedHistory(base, "dst", "src")
		if !errors.Is(err, ErrNoHistory) {
			t.Fatalf("SeedHistory error = %v, want ErrNoHistory", err)
		}
	})

	t.Run("missing target project reports fs.ErrNotExist, not ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()

		for _, sourceHistory := range []string{"{\"run\":1}\n", ""} {
			srcID := fmt.Sprintf("src-%d", len(sourceHistory))
			seedProject(t, base, srcID, sourceHistory)

			err := SeedHistory(base, "nosuch", srcID)
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("source history %q: error = %v, want it to wrap fs.ErrNotExist",
					sourceHistory, err)
			}
			if errors.Is(err, ErrNoHistory) {
				t.Errorf("source history %q: error = %v, want the missing target reported instead",
					sourceHistory, err)
			}
		}
	})

	t.Run("missing source project reports ErrNoHistory", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "dst", "")

		err := SeedHistory(base, "dst", "nosuch")
		if !errors.Is(err, ErrNoHistory) {
			t.Fatalf("SeedHistory error = %v, want ErrNoHistory", err)
		}
	})

	t.Run("leaves no staging file behind", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		before := projectFiles(t, base, "dst")

		if err := SeedHistory(base, "dst", "src"); err != nil {
			t.Fatalf("SeedHistory returned unexpected error: %v", err)
		}

		want := append(slices.Clone(before), "history.jsonl")
		slices.Sort(want)
		got := projectFiles(t, base, "dst")
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("project directory holds %v, want %v", got, want)
		}
	})

	t.Run("refuses to seed a project from itself", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "dst", "{\"own\":true}\n")

		err := SeedHistory(base, "dst", "dst")

		if !errors.Is(err, ErrCopyToSelf) {
			t.Fatalf("SeedHistory returned %v, want ErrCopyToSelf", err)
		}
		got, readErr := os.ReadFile(HistoryFile(base, "dst"))
		if readErr != nil {
			t.Fatalf("the refusal touched the history file: %v", readErr)
		}
		if string(got) != "{\"own\":true}\n" {
			t.Errorf("history = %q, want it untouched", got)
		}
	})

	t.Run("leaves no staging file behind when the rename fails", func(t *testing.T) {
		base := t.TempDir()
		seedProject(t, base, "src", "{\"run\":1}\n")
		seedProject(t, base, "dst", "")

		blocker := HistoryFile(base, "dst")
		if err := os.MkdirAll(filepath.Join(blocker, "occupied"), 0o755); err != nil {
			t.Fatalf("failed to block the destination: %v", err)
		}

		before := projectFiles(t, base, "dst")

		if err := SeedHistory(base, "dst", "src"); err == nil {
			t.Fatal("SeedHistory returned nil, want the rename to fail")
		}

		want := slices.Clone(before)
		slices.Sort(want)
		got := projectFiles(t, base, "dst")
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("project directory holds %v, want %v", got, want)
		}
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
				if err == nil {
					t.Fatalf("SeedHistory(%q, %q) = nil, want a validation error",
						tt.target, tt.from)
				}
				if errors.Is(err, ErrNoHistory) {
					t.Errorf("error = %v, want a validation error rather than ErrNoHistory", err)
				}

				if _, err := os.Stat(outside); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("something was written outside baseDir at %q (stat err = %v)",
						outside, err)
				}
				got, err := os.ReadFile(HistoryFile(base, "dst"))
				if err != nil {
					t.Fatalf("reading the target history: %v", err)
				}
				if string(got) != "{\"stale\":true}\n" {
					t.Errorf("target history = %q, want it untouched by a rejected call", got)
				}
			})
		}
	})
}
