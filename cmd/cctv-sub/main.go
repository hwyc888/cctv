package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"cctv-sub/internal/cctv"
	"cctv-sub/internal/model"
	"cctv-sub/internal/server"
	"cctv-sub/internal/service"
	"cctv-sub/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "API listen address")
	dataDir := flag.String("data", "./data", "application data directory")
	downloadDir := flag.String("downloads", "", "default download directory")
	databasePath := flag.String("database", "", "SQLite database file")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	workingDir, err := os.Getwd()
	if err != nil {
		logger.Error("read working directory", "error", err)
		os.Exit(1)
	}
	defaultDownloadPath := *downloadDir
	if defaultDownloadPath == "" {
		defaultDownloadPath = filepath.Join(workingDir, "downloads")
	}
	defaults := model.Settings{DownloadPath: defaultDownloadPath, PollIntervalMinutes: 30, ConcurrentDownloads: 2}
	stateStore, err := store.Open(context.Background(), *databasePath, *dataDir, defaults)
	if err != nil {
		logger.Error("open application state", "error", err)
		os.Exit(1)
	}
	defer stateStore.Close()
	client := cctv.New()
	manager := service.NewManager(stateStore, client)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	manager.Start(ctx)
	apiHandler := server.New(stateStore, client, manager, logger)
	handler := apiHandler
	if uiHandler := webHandler(); uiHandler != nil {
		router := http.NewServeMux()
		router.Handle("/api/", apiHandler)
		router.Handle("/", uiHandler)
		handler = router
	}
	httpServer := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		logger.Info("CCTV subscription API started", "url", "http://"+*addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server stopped", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
}
