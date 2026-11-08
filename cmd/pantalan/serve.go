package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pantalan/pantalan/internal/build"
	"github.com/pantalan/pantalan/internal/config"
	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/health"
	"github.com/pantalan/pantalan/internal/httpapi"
	"github.com/pantalan/pantalan/internal/proxy"
	"github.com/pantalan/pantalan/internal/queue"
	"github.com/pantalan/pantalan/internal/runtime/docker"
	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/internal/source"
	"github.com/pantalan/pantalan/internal/store/sqlite"
	"github.com/pantalan/pantalan/web"
)

// serveCmd runs the service until ctx is cancelled. Logs go to stdout, which
// systemd captures into the journal.
func serveCmd(ctx context.Context, inv invocation) int {
	log := slog.New(slog.NewTextHandler(inv.stdio.Out, nil))
	bin := buildInfo()
	log.Info("starting", "version", bin.Version, "revision", bin.Revision, "go", bin.GoVersion)
	if err := serve(ctx, log, inv.getenv); err != nil {
		log.Error("pantalan exited", "err", err)
		return 1
	}
	return 0
}

func serve(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}

	store, err := sqlite.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	// Wiring: this is the only place that knows which adapter backs which port.
	router := proxy.New(log)
	jobs := queue.New(100)
	deployer := service.NewDeployer(service.DeployerDeps{
		Apps:        store,
		Deployments: store,
		Source:      source.Git{},
		Builders: map[domain.BuildType]service.Builder{
			domain.BuildDockerfile: build.Dockerfile{},
			domain.BuildNixpacks:   build.Nixpacks{},
		},
		Runtime:   docker.New(),
		Health:    health.NewHTTP(),
		Router:    router,
		Scheduler: jobs,
		DataDir:   cfg.DataDir,
		Log:       log,
	})
	if err := deployer.RestoreRoutes(ctx); err != nil {
		return err
	}

	ui, err := web.Handler()
	if err != nil {
		return err
	}
	api := httpapi.NewHandler(httpapi.Deps{
		Apps:          service.NewAppService(store),
		Deployer:      deployer,
		AdminToken:    cfg.AdminToken,
		WebhookSecret: cfg.WebhookSecret,
		Web:           ui,
		Log:           log,
	})

	servers := []struct {
		name string
		addr string
		srv  *http.Server
	}{
		{"api", cfg.APIAddr, &http.Server{Handler: api, ReadHeaderTimeout: 10 * time.Second}},
		{"proxy", cfg.ProxyAddr, &http.Server{Handler: router, ReadHeaderTimeout: 10 * time.Second}},
	}
	listeners := make([]net.Listener, len(servers))
	for i, s := range servers {
		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			for _, open := range listeners[:i] {
				open.Close()
			}
			return err
		}
		listeners[i] = ln
	}
	serveErr := make(chan error, len(servers))
	for i, s := range servers {
		ln := listeners[i]
		log.Info("listening", "server", s.name, "addr", ln.Addr().String())
		go func() {
			if err := s.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
				serveErr <- err
			}
		}()
	}

	workersDone := make(chan struct{})
	go func() {
		jobs.Run(ctx, cfg.Workers, deployer.Run, log)
		close(workersDone)
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err = <-serveErr:
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		if shutErr := s.srv.Shutdown(shutdownCtx); shutErr != nil {
			err = errors.Join(err, shutErr)
		}
	}
	<-workersDone
	return err
}
