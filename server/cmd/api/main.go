package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/httpapi"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if !healthcheck() {
			os.Exit(1)
		}
		return
	}

	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	api := &http.Server{
		Addr:              cfg.HTTPListen,
		Handler:           httpapi.NewRouter(cfg),
		ReadHeaderTimeout: 5 * time.Second,
	}
	metrics := &http.Server{
		Addr:              cfg.MetricsListen,
		Handler:           metricsHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 2)
	go serve(api, "api", errs)
	go serve(metrics, "metrics", errs)

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-errs:
		if err != nil {
			slog.Error("server failed", "error", err)
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = api.Shutdown(shutdownCtx)
	_ = metrics.Shutdown(shutdownCtx)
}

func serve(server *http.Server, name string, errs chan<- error) {
	slog.Info("listening", "server", name, "address", server.Addr)
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		errs <- nil
		return
	}
	errs <- err
}

func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, "# HELP token_ne_api_up API process is running.\n# TYPE token_ne_api_up gauge\ntoken_ne_api_up 1\n")
	})
	return mux
}

func healthcheck() bool {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8080", 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.WriteString(conn, "GET /healthz HTTP/1.0\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"); err != nil {
		return false
	}
	response, err := io.ReadAll(io.LimitReader(conn, 128))
	return err == nil && len(response) > 12 && string(response[9:12]) == "200"
}
