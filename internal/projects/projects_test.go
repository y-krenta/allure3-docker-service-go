package projects

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProjectID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		// valid basic
		{"numeric", "1234567890", false},
		{"single digit", "1", false},
		{"single letter", "a", false},
		{"word", "demo", false},
		{"default", "default", false},

		// valid separators inside
		{"hyphen inside", "my-project", false},
		{"underscore inside", "my_project", false},
		{"space inside", "my project", false},
		{"mixed separators", "my_project-1 demo", false},
		{"leading zeros", "000123", false},

		// length boundaries
		{"length 199", strings.Repeat("a", 199), false},
		{"length 200", strings.Repeat("a", 200), false},
		{"length 201", strings.Repeat("a", 201), true},

		// invalid empty
		{"empty", "", true},

		// invalid start
		{"leading hyphen", "-abc", true},
		{"leading underscore", "_abc", true},
		{"leading space", " abc", true},

		// invalid end
		{"trailing hyphen", "abc-", true},
		{"trailing underscore", "abc_", true},
		{"trailing space", "abc ", true},

		// invalid characters
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
		// valid names
		{"simple json", "8f2c-result.json", "8f2c-result.json", false},
		{"letters digits dot dash underscore", "abc_123-test.json", "abc_123-test.json", false},
		{"single character", "a", "a", false},
		{"max length 255", strings.Repeat("a", 255), strings.Repeat("a", 255), false},

		// filepath.Base sanitization
		{"unix traversal", "../../etc/passwd", "passwd", false},
		{"unix nested path", "dir/sub/file.json", "file.json", false},
		{"trailing slash", "dir/", "dir", false},

		// hidden and dot-only names allowed by current regexp
		{"hidden file", ".hidden.json", ".hidden.json", false},
		{"many dots", "....", "....", false},

		// platform-dependent backslash handling on Unix
		{"windows traversal on unix", `..\..\etc\passwd`, "", true},
		{"windows style path on unix", `dir\sub\file.json`, "", true},

		// unsafe names
		{"empty", "", "", true},
		{"dot", ".", "", true},
		{"dot dot", "..", "", true},
		{"path separator", string(filepath.Separator), "", true},

		// too long
		{"length 256", strings.Repeat("a", 256), "", true},

		// invalid characters
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

	// Output:
	// "8f2c-result.json" -> "8f2c-result.json", <nil>
	// "../../etc/passwd" -> "passwd", <nil>
	// ".." -> "", unsafe file name ".."
	// "rep ort.json" -> "", invalid character in file name: "rep ort.json"
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

// TestHistoryFileStaysOutOfResults guards the one property of the history
// path that is not a matter of taste. Every build appends to this file, and
// the watcher rebuilds a project whenever the listing of its results
// directory changes — put the two together and each build schedules the next
// one, forever. Moving the file under ResultsDir compiles, passes every other
// test, and only shows up in production as a project that rebuilds itself in
// a loop.
func TestHistoryFileStaysOutOfResults(t *testing.T) {
	base := t.TempDir()

	history := HistoryFile(base, "demo")
	results := ResultsDir(base, "demo") + string(filepath.Separator)

	if strings.HasPrefix(history, results) {
		t.Errorf("HistoryFile = %q, want it outside the results dir %q", history, results)
	}
}
