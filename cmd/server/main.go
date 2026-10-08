package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dot/gmail-archiver/internal/api"
	"github.com/dot/gmail-archiver/internal/config"
	"github.com/dot/gmail-archiver/internal/db"
	"github.com/dot/gmail-archiver/internal/imap"
	"github.com/dot/gmail-archiver/internal/parser"
	"github.com/dot/gmail-archiver/internal/storage"
)

// [Main]
func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Println("[server] starting gmail-archiver ...")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[server] configuration error: %v", err)
	}

	storageEngine, err := storage.New(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] storage init error: %v", err)
	}

	database, err := db.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("[server] database init error: %v", err)
	}
	defer database.Close()

	emailParser := parser.New(storageEngine)
	watcher := imap.NewWatcher(cfg, database, emailParser)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start IMAP watcher in background
	go watcher.Start(ctx)

	// Setup HTTP server
	apiServer := api.NewServer(cfg, database, storageEngine, watcher)
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           apiServer.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start HTTP server in background
	go func() {
		log.Printf("[server] HTTP listening on :%d", cfg.HTTPPort)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[server] HTTP server error: %v", err)
		}
	}()

	// Wait for termination signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	sig := <-quit
	log.Printf("[server] received shutdown signal (%v), shutting down gracefully ...", sig)

	// Stop background IMAP watcher
	cancel()

	// Shutdown HTTP server with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("[server] HTTP server shutdown error: %v", err)
	}

	log.Println("[server] gmail-archiver stopped cleanly")
}
