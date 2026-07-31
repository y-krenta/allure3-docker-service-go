package projects

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

var (
	// projectIDPattern is the allowed shape for a project ID: starts and
	// ends with a lowercase letter or digit, letters/digits/space/_/-
	// allowed in between.
	projectIDPattern = regexp.MustCompile(`^[a-z\d]([a-z\d _-]*[a-z\d])?$`)
	// ErrProjectExists is returned by CreateDir when the project directory
	// already exists.
	ErrProjectExists = errors.New("project already exists")
)

const (
	// DefaultProjectID is the reserved, always-present project. It is
	// bootstrapped on startup and cannot be deleted (see the httpapi
	// deleteProject handler).
	DefaultProjectID = "default"
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
