package projects

import (
	"errors"
	"path/filepath"
	"regexp"
)

// `re` is a regular expression that matches strings containing only lowercase letters, digits, underscores, and hyphens. Used for validating project IDs.
var (
	re = regexp.MustCompile(`^[a-z0-9_-]+$`)
)

func ResultsDir(baseDir, projectID string) string {
	return filepath.Join(baseDir, projectID, "results")

}

func ReportsDir(baseDir, projectID string) string {
	return filepath.Join(baseDir, projectID, "reports")

}

func LatestReportDir(baseDir, projectID string) string {
	return filepath.Join(ReportsDir(baseDir, projectID), "latest")
}

func ValidateProjectID(id string) error {
	if id == "" {
		return errors.New("project id is required")
	}
	if len(id) > 200 {
		return errors.New("project id must not exceed 200 characters")
	}
	if !re.MatchString(id) {
		return errors.New("project id must match ^[a-z0-9_-]+$")
	}

	return nil
}
