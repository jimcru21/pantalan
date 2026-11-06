// Command pantalan is the control plane: dashboard, REST API, deploy workers
// and the reverse proxy, all in one process.
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
	"github.com/pantalan/pantalan/internal/store/memory"
	"github.com/pantalan/pantalan/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("pantalan exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Wiring: this is the only place that knows which adapter backs which port.
	store := memory.New()
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

	servers := []*http.Server{
		{Addr: cfg.APIAddr, Handler: api, ReadHeaderTimeout: 10 * time.Second},
		{Addr: cfg.ProxyAddr, Handler: router, ReadHeaderTimeout: 10 * time.Second},
	}
	serveErr := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			log.Info("listening", "addr", srv.Addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
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
	for _, srv := range servers {
		if shutErr := srv.Shutdown(shutdownCtx); shutErr != nil {
			err = errors.Join(err, shutErr)
		}
	}
	<-workersDone
	return err
}
