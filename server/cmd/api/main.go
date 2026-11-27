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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IvanLi-CN/token-ne/server/internal/auth"
	"github.com/IvanLi-CN/token-ne/server/internal/authsettings"
	"github.com/IvanLi-CN/token-ne/server/internal/caddy"
	"github.com/IvanLi-CN/token-ne/server/internal/config"
	"github.com/IvanLi-CN/token-ne/server/internal/cores"
	"github.com/IvanLi-CN/token-ne/server/internal/httpapi"
	"github.com/IvanLi-CN/token-ne/server/internal/metrics"
	"github.com/IvanLi-CN/token-ne/server/internal/store"
	"github.com/IvanLi-CN/token-ne/server/internal/version"
	"github.com/IvanLi-CN/token-ne/server/internal/xray"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		if err := authLocalLogin(os.Args[2:]); err != nil {
			slog.Error("authentication command failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "config" {
		if err := exportConfig(os.Args[2:]); err != nil {
			slog.Error("configuration export failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if !healthcheck() {
			os.Exit(1)
		}
		return
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	startup, cancelStartup := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelStartup()
	repo, err := store.Open(startup, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database initialization failed")
		os.Exit(1)
	}
	defer repo.Close()
	if err := repo.Bootstrap(startup, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		slog.Error("admin bootstrap failed")
		os.Exit(1)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if _, warnings, err := authsettings.Startup(startup, repo, cfg); err != nil {
		slog.Error("authentication configuration invalid", "error", err)
		os.Exit(1)
	} else {
		for _, field := range warnings {
			slog.Warn("authentication setting is stored in the database; remove the environment variable", "field", field)
		}
	}
	if err := auth.NewOIDC(auth.OIDCOptions{CAFile: cfg.OIDCCaFile}).InitError(); err != nil {
		slog.Error("OIDC TLS configuration failed", "error", err)
		os.Exit(1)
	}
	registry := metrics.New()

	controlToken := cfg.CoreControlToken
	peer := cores.NewClient(controlToken)
	docker, err := cores.NewEngine(os.Getenv("DOCKER_HOST"))
	if err != nil {
		slog.Error("Docker API configuration failed", "error", err)
		os.Exit(1)
	}
	if image := os.Getenv("CORE_STATE_INIT_IMAGE"); image != "" {
		docker.StateInitImage = image
	}
	coreImage, err := cores.ResolveCoreImageRepository(os.Getenv(cores.CoreImageRepositoryEnv))
	if err != nil {
		slog.Error("core image repository configuration failed", "error", err)
		os.Exit(1)
	}
	caddySettings := caddy.Settings{Socket: os.Getenv("CADDY_ADMIN_URL"), Domain: os.Getenv("PUBLIC_DOMAIN"),
		TLSMode: os.Getenv("CADDY_TLS_MODE"), APIAddress: "api:8080"}
	if len(caddySettings.Socket) > 6 && caddySettings.Socket[:7] == "unix://" {
		caddySettings.Socket = caddySettings.Socket[7:]
	}
	if caddySettings.Socket == "" {
		caddySettings.Socket = "/run/caddy/admin.sock"
	}
	policyPath := os.Getenv("STATUS_REWRITE_CONFIG")
	if policyPath == "" {
		policyPath = "/etc/token-ne/status-rewrite.yaml"
	}
	proxy := caddy.New(caddySettings)
	// The control-plane URL a core uses for config pull and usage ingestion is
	// the compose service name by default; a bare-metal core needs the address
	// it can actually reach.
	controlURL := strings.TrimSpace(os.Getenv("CONTROL_PLANE_URL"))
	if controlURL == "" {
		controlURL = "http://api:8080"
	}
	spoolDir := strings.TrimSpace(os.Getenv("CORE_SPOOL_DIR"))
	snapshots := &cores.Snapshots{DB: repo.Pool, Master: cfg.MasterKey, Token: controlToken,
		SigningKey: os.Getenv("CORE_CONFIG_SIGNING_KEY"), SignatureMode: os.Getenv("CORE_CONFIG_SIGNATURE_MODE"),
		BaseURL: controlURL, PolicyPath: policyPath, SpoolDir: spoolDir, StreamUsageInjection: &cfg.StreamUsageInjection}
	runtime := &cores.Runtime{DB: repo, Config: snapshots, Peer: peer, Docker: docker, Caddy: proxy, Metrics: registry,
		Project: os.Getenv("COMPOSE_PROJECT_NAME"), Networks: splitNetworks(os.Getenv("CORE_NETWORKS")),
		StateHostDir: os.Getenv("CORE_STATE_HOST_DIR"), CoreURL: controlURL, CoreImage: coreImage, Token: controlToken,
		SigningKey: snapshots.SigningKey, SignatureMode: snapshots.SignatureMode, CacheKey: os.Getenv("CORE_CACHE_KEY"),
		AllowPrivateUpstreams: os.Getenv("CORE_ALLOW_PRIVATE_UPSTREAMS"), CurrentVersion: version.Version, CurrentCommit: version.Commit}
	// Xray is a Compose service the control plane only talks to. The dial is lazy
	// so a deployment without the egress overlay still starts, and a missing Xray
	// stays a retried reconcile failure instead of a startup blocker.
	runtime.Egress = lazyEgress(context.Background(), cfg.XrayAPIURL)
	runtime.EgressInterval = time.Duration(cfg.XrayReconcileIntervalSeconds) * time.Second
	runtime.Master = cfg.MasterKey
	coreHTTP := &cores.HTTP{Runtime: runtime, Token: controlToken, PublicMeta: cores.BrokerMeta()}
	serviceCtx, stopService := context.WithCancel(context.Background())
	defer stopService()
	go runtime.Run(serviceCtx)

	// NewRouterWithUsage registers the management API, the usage ingestion routes
	// and the egress proxy routes (newRouter always mounts them), so the egress
	// endpoints stay reachable; the usage variant is the one this binary needs.
	api := &http.Server{
		Addr:              cfg.HTTPListen,
		Handler:           httpapi.NewRouterWithUsage(cfg, repo, registry, cores.Registrar(coreHTTP)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	metrics := &http.Server{
		Addr:              cfg.MetricsListen,
		Handler:           metricsHandler(cfg, repo, registry),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
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

// lazyEgress dials the Xray gRPC API on the first reconcile pass. A deployment
// without the egress overlay has no Xray: startup must not block on it, and the
// loop's retry is what turns a late Xray into a working one.
func lazyEgress(ctx context.Context, address string) cores.EgressReconciler {
	return &lazyEngine{address: address, ctx: ctx}
}

// lazyEngine dials once and keeps the client. Every failure path returns an
// empty summary with Unreachable set, so the caller alerts once and retries on
// the next tick without touching the Xray container.
type lazyEngine struct {
	address string
	ctx     context.Context
	mu      sync.Mutex
	engine  *xray.Engine
	client  *xray.Client
}

func (l *lazyEngine) StartupReconcile(ctx context.Context, targets []xray.Target) xray.Summary {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.engine == nil {
		client, err := xray.Dial(l.ctx, l.address, xray.DialOptions{})
		if err != nil {
			return xray.Summary{Unreachable: true, Error: err.Error()}
		}
		l.client, l.engine = client, xray.NewEngine(client)
	}
	summary := l.engine.StartupReconcile(ctx, targets)
	// A client that lost its connection is rebuilt on the next pass: the engine
	// is stateless, only the gRPC channel has to be replaced.
	if summary.Unreachable {
		_ = l.client.Close()
		l.client, l.engine = nil, nil
	}
	return summary
}

func splitNetworks(value string) []string {
	result := []string{}
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
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

func metricsHandler(cfg config.Config, repo interface {
	store.Repository
	metrics.Source
}, registries ...*metrics.Registry) http.Handler {
	mux := http.NewServeMux()
	registry := metrics.New()
	if len(registries) > 0 && registries[0] != nil {
		registry = registries[0]
	}
	mux.HandleFunc("GET /metrics", registry.Handler(cfg.MetricsToken, repo))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if repo.Ping(ctx) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
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
