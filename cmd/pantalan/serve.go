package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pantalan/pantalan/internal/build"
	"github.com/pantalan/pantalan/internal/config"
	"github.com/pantalan/pantalan/internal/dashboard"
	"github.com/pantalan/pantalan/internal/dashtls"
	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/health"
	"github.com/pantalan/pantalan/internal/httpapi"
	"github.com/pantalan/pantalan/internal/nodeinfo"
	"github.com/pantalan/pantalan/internal/password"
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
	if err := serve(ctx, log, inv.getenv, inv.world, bin); err != nil {
		log.Error("pantalan exited", "err", err)
		return 1
	}
	return 0
}

// pruneSessions deletes expired Dashboard sessions. A failure is logged
// and left for the next run: an expired session is refused either way.
func pruneSessions(ctx context.Context, log *slog.Logger, admins *service.AdminService) {
	n, err := admins.PruneSessions(ctx)
	if err != nil {
		log.Error("pruning expired sessions failed", "err", err)
		return
	}
	log.Info("pruned expired sessions", "deleted", n)
}

func serve(ctx context.Context, log *slog.Logger, getenv func(string) string, world World, bin binary) error {
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
	// Startup order (spec #35): re-queue, refresh upstreams and restore
	// routes first, before the queue runs, so re-queued deployments keep
	// their order and nothing new starts while interrupted ones are
	// cleaned up. Then the certificate, session pruning and the daily
	// maintenance tick; last the sampler, the queue and the listeners.
	if err := deployer.Recover(ctx); err != nil {
		return fmt.Errorf("startup recovery: %w", err)
	}

	certs, generated, err := dashtls.Open(tlsDir(cfg.DataDir), time.Now())
	if err != nil {
		return err
	}
	renewCertificate(log, certs)
	cert := certs.Current()
	log.Info("tls certificate", "fingerprint", cert.Fingerprint(), "expires", cert.Leaf().NotAfter, "generated", generated)

	admins := service.NewAdminService(service.AdminDeps{
		Credentials: store,
		Hasher:      password.Hasher{},
		Now:         world.now,
	})
	pruneSessions(ctx, log, admins)

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	daily := world.MaintenanceTicks
	if daily == nil {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		daily = ticker.C
	}
	go func() {
		for {
			select {
			case <-daily:
				renewCertificate(log, certs)
				pruneSessions(ctx, log, admins)
			case <-ctx.Done():
				return
			}
		}
	}()

	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("hostname: %w", err)
	}
	sampler := nodeinfo.NewSampler(nodeinfo.Host{ProcRoot: world.ProcRoot, Statfs: world.Statfs}, log)
	samplerDone := make(chan struct{})
	go func() {
		sampler.Run(ctx, world.SampleTicks)
		close(samplerDone)
	}()
	dockerAPI := nodeinfo.NewDocker(world.DockerSocket, world.Statfs, world.now)
	events := nodeinfo.NewEventStream(dockerAPI, log)
	eventsDone := make(chan struct{})
	go func() {
		events.Run(ctx, world.EventsBackoff)
		close(eventsDone)
	}()
	defer func() {
		stop()
		<-samplerDone
		<-eventsDone
	}()

	apps := service.NewAppService(store)
	dash := dashboard.New(dashboard.Deps{
		Sessions: admins,
		Files:    web.Dist(),
		Log:      log,
	})
	api := httpapi.NewHandler(httpapi.Deps{
		Apps:          apps,
		Deployer:      deployer,
		Sessions:      admins,
		Hostname:      hostname,
		Host:          sampler,
		Docker:        dockerAPI,
		Events:        events,
		Now:           world.now,
		AdminToken:    cfg.AdminToken,
		WebhookSecret: cfg.WebhookSecret,
		Dashboard:     dash,
		Log:           log,
	})

	servers := []struct {
		name  string
		addr  string
		srv   *http.Server
		serve func(*http.Server, net.Listener) error
	}{
		{"dashboard", cfg.DashboardAddr, &http.Server{
			Handler:           api,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: certs.GetCertificate},
		}, func(srv *http.Server, ln net.Listener) error {
			return srv.ServeTLS(dashtls.RedirectPlainHTTP(ln), "", "")
		}},
		{"proxy", cfg.ProxyAddr, &http.Server{Handler: router, ReadHeaderTimeout: 10 * time.Second}, (*http.Server).Serve},
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
			if err := s.serve(s.srv, ln); !errors.Is(err, http.ErrServerClosed) {
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
