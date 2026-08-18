package watcher

import (
	"context"
	"errors"
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
	newest int64 // самая свежая mtime, UnixNano
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
		fp, err := scan(projects.ResultsDir(projectsDir, id))
		if err != nil {
			continue
		}
		if fp == seen[id] {
			continue
		}

		if warm {
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

		default:
			slog.Error("watcher: generation failed to start", "project_id", id, "err", err)
		}

	}
}
