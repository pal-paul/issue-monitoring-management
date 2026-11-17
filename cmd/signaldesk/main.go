package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"signaldesk/internal/monitor"
	"signaldesk/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	defaultPort := os.Getenv("PORT")
	if defaultPort == "" {
		defaultPort = "8080"
	}
	dataDir := flag.String(
		"data-dir",
		"/data",
		"Private persistent bootstrap configuration directory",
	)
	port := flag.String("port", defaultPort, "HTTP listening port")
	local := flag.Bool(
		"local",
		false,
		"Loopback-only HTTP development with authenticated sample injection",
	)
	localContainer := flag.Bool(
		"local-container",
		false,
		"Local container HTTP development; publish the port on loopback only",
	)
	demo := flag.Bool(
		"demo",
		false,
		"Seed sample incidents in an empty local development workspace",
	)
	flag.Parse()
	localMode := *local || *localContainer
	if (localMode || *demo) && os.Getenv("K_SERVICE") != "" {
		return errors.New("local development flags are forbidden on Cloud Run")
	}
	if *demo && !localMode {
		return errors.New("-demo requires -local or -local-container")
	}
	address := ":" + *port
	if *local && !*localContainer {
		address = "127.0.0.1:" + *port
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := &monitor.Server{
		DataDir:   *dataDir,
		LocalHTTP: localMode,
		Config:    monitor.Config{Dev: localMode, Demo: *demo},
	}
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	err := app.Load(startup)
	cancel()
	if err != nil {
		return err
	}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); app.RunPull(ctx) }()
	defer func() { stop(); <-workerDone; app.Close() }()
	server := &http.Server{
		Addr:              address,
		Handler:           app.Handler(web.Assets()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	errorsCh := make(chan error, 1)
	go func() {
		slog.Info("Signal Desk ready", "address", address)
		errorsCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
