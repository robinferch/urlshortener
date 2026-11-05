package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/robinferch/urlshortener/internal/app"
	"github.com/robinferch/urlshortener/web"
)

var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			os.Stdout.WriteString(version + "\n")
			return nil
		case "healthcheck": // for Docker HEALTHCHECK; the distroless image has no curl
			addr := os.Getenv("LISTEN_ADDR")
			if addr == "" || addr[0] == ':' {
				addr = "127.0.0.1" + addr
				if addr == "127.0.0.1" {
					addr += ":8080"
				}
			}
			res, err := http.Get("http://" + addr + "/healthz")
			if err != nil {
				return err
			}
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				return errors.New("unhealthy: " + res.Status)
			}
			return nil
		}
	}
	cfg, err := app.LoadConfig()
	if err != nil {
		return err
	}
	cfg.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := app.OpenDB(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	workers, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	srv, err := app.New(workers, cfg, db, web.Dist)
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	slog.Info("listening", "addr", cfg.ListenAddr, "app_url", cfg.AppURL.String(), "version", version)

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = httpSrv.Shutdown(shutdownCtx)
	srv.Close() // flush buffered visits before the DB pool closes
	cancelWorkers()
	return err
}
