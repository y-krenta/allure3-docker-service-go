package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/y-krenta/allure3-docker-service-go/internal/config"
)

func main() {
	cfg := config.Load()

	r := chi.NewRouter()
	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
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
