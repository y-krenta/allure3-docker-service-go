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
	projectIDPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)
	ErrProjectExists = errors.New("project already exists")
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
	if !projectIDPattern.MatchString(id) {
		return errors.New("project id must match ^[a-z0-9_-]+$")
	}

	return nil
}

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
