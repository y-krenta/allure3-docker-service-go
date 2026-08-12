package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/y-krenta/allure3-docker-service-go/internal/config"
	"github.com/y-krenta/allure3-docker-service-go/internal/httpapi"
	"github.com/y-krenta/allure3-docker-service-go/internal/projects"
	"github.com/y-krenta/allure3-docker-service-go/internal/report"
)

func main() {
	cfg := config.Load()
	resolved, err := exec.LookPath(cfg.AllureBin)
	if err != nil {
		log.Fatalf("allure CLI not found (ALLURE_BIN=%q): %v", cfg.AllureBin, err)
	}

	log.Printf("allure %s", resolved)
	pathDefaultLatestReportDir := projects.LatestReportDir(cfg.ProjectsDir, projects.DefaultProjectID)
	pathDefaultResultDir := projects.ResultsDir(cfg.ProjectsDir, projects.DefaultProjectID)

	err = os.MkdirAll(cfg.ProjectsDir, 0755)
	if err != nil {
		log.Fatalf("cannot create projects dir %q: %v", cfg.ProjectsDir, err)
	}

	err = os.MkdirAll(pathDefaultLatestReportDir, 0755)
	if err != nil {
		log.Fatalf("cannot create default latest report dir %q: %v", pathDefaultLatestReportDir, err)
	}
	err = os.MkdirAll(pathDefaultResultDir, 0755)
	if err != nil {
		log.Fatalf("cannot create default result dir %q: %v", pathDefaultResultDir, err)
	}

	reports := report.New(cfg.ProjectsDir, cfg.AllureBin)

	s := httpapi.NewServer(cfg.ProjectsDir, reports)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           s.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("Starting server on port %v", cfg.Port)
	go func() {
		errStartServer := srv.ListenAndServe()
		if errStartServer != nil && !errors.Is(errStartServer, http.ErrServerClosed) {
			log.Fatal(errStartServer)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errShutdown := srv.Shutdown(ctx)
	if errShutdown != nil {
		log.Println(errShutdown)
	}
	log.Println("Server gracefully stopped")
}
