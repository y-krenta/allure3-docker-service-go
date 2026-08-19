package projects

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var (
	// projectIDPattern is the allowed shape for a project ID: starts and
	// ends with a lowercase letter or digit, letters/digits/space/_/-
	// allowed in between.
	projectIDPattern = regexp.MustCompile(`^[a-z\d]([a-z\d _-]*[a-z\d])?$`)
	// resultFileNamePattern is the allowed shape for an uploaded result file
	// name: ASCII letters, digits, dot, underscore and hyphen. Covers every
	// name Allure generates (<uuid>-result.json, environment.properties, ...).
	resultFileNamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	// ErrProjectExists is returned by CreateDir when the project directory
	// already exists.
	ErrProjectExists = errors.New("project already exists")
)

const (
	// DefaultProjectID is the reserved, always-present project. It is
	// bootstrapped on startup and cannot be deleted (see the httpapi
	// deleteProject handler).
	DefaultProjectID = "default"
	// ExecutorFileName is the fixed name of the executor metadata file the
	// service writes into ResultsDir before a build. The watcher must skip
	// it when fingerprinting results, or writing it would look like new
	// results and trigger another build.
	ExecutorFileName = "executor.json"
)

// ResultsDir returns the path where a project's raw Allure results live:
// <baseDir>/<projectID>/results.
func ResultsDir(baseDir, projectID string) string {
	return filepath.Join(baseDir, projectID, "results")

}

// ReportsDir returns the path where a project's generated report builds
// live: <baseDir>/<projectID>/reports.
func ReportsDir(baseDir, projectID string) string {
	return filepath.Join(baseDir, projectID, "reports")

}

// LatestReportDir returns the path to a project's "latest" report build,
// a subdirectory of ReportsDir.
func LatestReportDir(baseDir, projectID string) string {
	return filepath.Join(ReportsDir(baseDir, projectID), "latest")
}

// TmpRoot returns the path where a project's in-progress report builds are
// staged: <baseDir>/<projectID>/.tmp. It deliberately sits beside ReportsDir
// rather than inside it, so half-built reports are invisible to anything
// listing the project's builds. Staging here also keeps the finished build on
// the same filesystem as LatestReportDir, which is what lets it be published
// with a rename.
func TmpRoot(baseDir, projectID string) string {
	return filepath.Join(baseDir, projectID, ".tmp")
}

// ValidateProjectID checks id against projectIDPattern and a 200-character
// length limit, returning a descriptive error if either check fails.
func ValidateProjectID(id string) error {
	if id == "" {
		return errors.New("project ID is required")
	}
	if len(id) > 200 {
		return errors.New("project ID must not exceed 200 characters")
	}
	if !projectIDPattern.MatchString(id) {
		return errors.New(`project ID may contain only lowercase letters, digits, spaces, underscores (_), and hyphens (-), and must start and end with a letter or digit`)
	}

	return nil
}

// CreateDir creates a new project's directory tree (the project dir plus
// its ReportsDir and ResultsDir subdirectories) under baseDir. Returns
// ErrProjectExists if the project directory is already there. If any step
// after the initial directory creation fails, it rolls back by removing
// everything it created.
func CreateDir(baseDir, id string) error {
	err := os.Mkdir(filepath.Join(baseDir, id), 0755)

	if errors.Is(err, fs.ErrExist) {
		return ErrProjectExists
	}

	if err != nil {
		return fmt.Errorf("unable to create project directory: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			if rmErr := os.RemoveAll(filepath.Join(baseDir, id)); rmErr != nil {
				log.Printf("rollback failed: %v\n", rmErr)
			}
		}
	}()

	err = os.MkdirAll(ReportsDir(baseDir, id), 0755)
	if err != nil {
		return fmt.Errorf("unable to create reports directory: %w", err)
	}

	err = os.MkdirAll(ResultsDir(baseDir, id), 0755)
	if err != nil {
		return fmt.Errorf("unable to create results directory: %w", err)
	}

	ok = true

	return nil
}

// ClearResults removes the top-level files in a project's results
// directory, leaving any subdirectories in place. It mirrors the
// "-maxdepth 1 -type f" behaviour of the original cleanAllureResults.sh:
// a directory sitting in results/ is left for whatever put it there.
//
// A missing results directory is returned as-is (wrapping fs.ErrNotExist)
// rather than translated to a sentinel here - that is the caller's job,
// the same as with a plain os.ReadDir elsewhere in this package.
func ClearResults(baseDir, projectID string) error {
	resultsDir := ResultsDir(baseDir, projectID)

	entries, err := os.ReadDir(resultsDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		err := os.Remove(filepath.Join(resultsDir, entry.Name()))
		if err != nil {
			return err
		}
	}
	return nil
}

// SanitizeResultFileName reduces a client-supplied file name to a single
// path element safe to create inside a project's results directory.
//
// Any directory part is stripped silently, so "a/b/x.json" becomes "x.json".
// The remaining name is rejected if it is a directory reference ("." or ".."),
// longer than 255 bytes, or contains anything outside ASCII letters, digits,
// ".", "_" and "-".
//
// The returned error describes the reason and is safe to show to the client.
func SanitizeResultFileName(name string) (string, error) {
	name = filepath.Base(name)
	switch name {
	case "", ".", "..", string(filepath.Separator):
		return "", fmt.Errorf("unsafe file name %q", name)

	}

	if len(name) > 255 {
		return "", fmt.Errorf("file name too long: %d bytes", len(name))
	}
	if !resultFileNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid character in file name: %q", name)
	}

	return name, nil
}

// HistoryFile returns the path to a project's Allure history:
// <baseDir>/<projectID>/history.jsonl, one JSON object per past run. The
// Allure CLI keeps it up to date itself, given the path.
//
// It sits in the project root rather than in ResultsDir on purpose. The
// watcher rebuilds a project when the listing of its results directory
// changes, and every build appends to this file - inside ResultsDir it would
// make each build trigger the next one, forever.
func HistoryFile(baseDir, projectID string) string {
	return filepath.Join(baseDir, projectID, "history.jsonl")
}

// NumberedReportDir returns the path to a project's archived report build
// numbered n, a subdirectory of ReportsDir alongside LatestReportDir. n comes
// from the build's own count of past reports, not from any input a caller
// controls.
func NumberedReportDir(baseDir, projectID string, n int) string {
	path := filepath.Join(ReportsDir(baseDir, projectID), fmt.Sprintf("%d", n))
	return path
}

// ClearHistory resets everything that gives a project's report its trend
// history: every numbered archive under ReportsDir (identified by a name
// that parses as a number - "latest" and anything else is left alone),
// the HistoryFile, and the results directory's ExecutorFileName. Deleting
// the numbered archives also resets the next build's number back to 1,
// which is why ExecutorFileName has to go too: writeExecutor in the report
// package is a no-op for build 1, so a stale executor.json from a higher
// build number would otherwise survive untouched.
//
// A missing ReportsDir is returned as-is (wrapping fs.ErrNotExist), the
// same convention as ClearResults. A missing HistoryFile is not an error -
// a project with no build yet has none - so that removal alone tolerates
// fs.ErrNotExist rather than propagating it.
func ClearHistory(baseDir, projectID string) error {
	entries, err := os.ReadDir(ReportsDir(baseDir, projectID))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		_, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		err = os.RemoveAll(filepath.Join(ReportsDir(baseDir, projectID), entry.Name()))
		if err != nil {
			return err
		}
	}

	err = os.Remove(HistoryFile(baseDir, projectID))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	err = os.RemoveAll(filepath.Join(ResultsDir(baseDir, projectID), ExecutorFileName))
	if err != nil {
		return err
	}

	return nil
}
