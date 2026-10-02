package watcher

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

type StartFunc func(ctx context.Context, projectID string) error

type fingerprint struct {
	count  int
	size   int64
	newest int64
}

func scan(dir string) (fingerprint, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fingerprint{}, err
	}

	var fp fingerprint

	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == projects.ExecutorFileName {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}

		fp.count++
		fp.size += info.Size()

		if mt := info.ModTime().UnixNano(); mt > fp.newest {
			fp.newest = mt
		}
	}
	return fp, nil
}

func Run(ctx context.Context, projectsDir string, interval time.Duration, start StartFunc) {
	if interval <= 0 {
		slog.Info("watcher disabled", "interval", interval)
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	seen := make(map[string]fingerprint)
	warm := true

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sweep(ctx, projectsDir, seen, start, warm)
			warm = false
		}
	}

}

func sweep(ctx context.Context, projectsDir string, seen map[string]fingerprint, start StartFunc, warm bool) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		slog.Error("watcher: failed to read project dir", "err", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		if err := projects.ValidateProjectID(id); err != nil {
			continue
		}
		fp, err := scan(projects.ResultsDir(projectsDir, id))
		if err != nil {
			continue
		}
		if fp == seen[id] {
			continue
		}

		if warm && !unbuilt(projectsDir, id, fp) {
			seen[id] = fp
			continue
		}
		err = start(ctx, id)
		switch {
		case err == nil:
			seen[id] = fp
			slog.Info("watcher: generation started", "project_id", id)

		case errors.Is(err, report.ErrAlreadyRunning):
			continue

		case errors.Is(err, report.ErrNoResults):
			seen[id] = fp

		default:
			slog.Error("watcher: generation failed to start", "project_id", id, "err", err)
		}

	}
}

// unbuilt reports whether the results fingerprinted in fp still need a build:
// there are some, and the project has no published report or one older than
// the newest of them. The warm-up pass asks it so that results uploaded while
// the service was down, or in the first interval after it came up, are built
// rather than taken for the baseline and left without a report until the
// next upload.
//
// The comparison is strict: results dated the same as the report are taken
// as already in it. A report that cannot be stat'ed for any reason other than
// not existing counts as unbuilt too - a needless build costs a few seconds,
// skipping one loses a run's report.
func unbuilt(projectsDir, id string, fp fingerprint) bool {
	if fp.count == 0 {
		return false
	}
	info, err := os.Stat(projects.LatestReportDir(projectsDir, id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true
		}
		slog.Error("watcher: failed to stat report", "project_id", id, "err", err)
		return true
	}
	return fp.newest > info.ModTime().UnixNano()
}
