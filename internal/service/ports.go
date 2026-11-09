// Package service holds Pantalan's use cases. It defines the interfaces
// (ports) it needs; packages under internal/ provide them (adapters). The
// service package never imports an adapter.
package service

import (
	"context"
	"io"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

type AppRepository interface {
	CreateApp(ctx context.Context, app domain.App) error
	// UpdateApp replaces the settings (name, repository, branch, build
	// type, domain, port) of the App with app.ID, keeping its env vars and
	// creation time. It returns domain.ErrNotFound when there is no such
	// App, and a domain.ErrConflict when another App has the domain.
	UpdateApp(ctx context.Context, app domain.App) error
	GetApp(ctx context.Context, id string) (domain.App, error)
	ListApps(ctx context.Context) ([]domain.App, error)
	FindAppsByRepo(ctx context.Context, repoURL, branch string) ([]domain.App, error)
}

type DeploymentRepository interface {
	// SaveDeployment inserts or replaces a deployment by ID.
	SaveDeployment(ctx context.Context, dep domain.Deployment) error
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	ListDeployments(ctx context.Context, appID string) ([]domain.Deployment, error)
	// LatestDeployments returns the newest deployment of each app that
	// has any, all at once.
	LatestDeployments(ctx context.Context) ([]domain.Deployment, error)
	// LiveDeployment returns domain.ErrNotFound when the app has nothing live.
	LiveDeployment(ctx context.Context, appID string) (domain.Deployment, error)
}

// SourceFetcher checks out ref of a repository into dir and returns the commit.
type SourceFetcher interface {
	Fetch(ctx context.Context, repoURL, ref, dir string) (commit string, err error)
}

type BuildRequest struct {
	Dir   string
	Image string
	Logs  io.Writer
}

type Builder interface {
	Build(ctx context.Context, req BuildRequest) error
}

type ContainerSpec struct {
	Name   string
	Image  string
	Port   int
	Env    map[string]string
	Labels map[string]string
}

type Container struct {
	ID       string
	Upstream string // host:port reachable from the proxy
}

type Runtime interface {
	Start(ctx context.Context, spec ContainerSpec) (Container, error)
	// Remove force-removes a container, given its ID or its name.
	Remove(ctx context.Context, containerID string) error
	// Upstream returns the host:port the container's port is published on
	// now. It changes when Docker restarts the container.
	Upstream(ctx context.Context, containerID string, port int) (string, error)
}

type HealthChecker interface {
	// Wait blocks until upstream is serving or gives up with an error.
	Wait(ctx context.Context, upstream string) error
}

type Router interface {
	SetRoute(host, upstream string)
	RemoveRoute(host string)
}

// Scheduler hands a queued deployment to whatever runs it.
type Scheduler interface {
	Enqueue(deploymentID string) error
}

// Credentials stores the one Admin and their Dashboard sessions. Each
// method is one atomic operation, so the CLI and a running service that
// share the file (ADR 0001) never see half of one.
type Credentials interface {
	// CreateAdmin returns domain.ErrConflict when an Admin already exists.
	CreateAdmin(ctx context.Context, admin domain.Admin) error
	// GetAdmin returns domain.ErrNotFound when there is no Admin.
	GetAdmin(ctx context.Context) (domain.Admin, error)
	// ResetPassword replaces the Admin's password hash, ends every session
	// and clears the failure count. It returns domain.ErrNotFound when
	// there is no Admin.
	ResetPassword(ctx context.Context, hash string, at time.Time) error
	// ReserveAttempt reads the Admin and, unless admit refuses them, counts
	// one more failed sign-in at at. It returns the Admin as read, admit's
	// error (and counts nothing), or domain.ErrNotFound when there is no
	// Admin. Two attempts reserved at once are counted one after the
	// other, the second seeing the first's failure.
	ReserveAttempt(ctx context.Context, at time.Time, admit func(domain.Admin) error) (domain.Admin, error)
	// StartSession records a successful sign-in, only while the Admin's
	// password hash is still checked (the one the password was verified
	// against): it clears the failure count, replaces the hash with
	// rehashed unless that is empty, stores s, and ends the oldest
	// sessions beyond the newest keep. It returns domain.ErrConflict, and
	// changes nothing, when the hash has changed (a password reset won
	// the race).
	StartSession(ctx context.Context, checked, rehashed string, s domain.Session, keep int) error
	// GetSession returns domain.ErrNotFound when there is no such session.
	GetSession(ctx context.Context, tokenHash string) (domain.Session, error)
	TouchSession(ctx context.Context, tokenHash string, at time.Time) error
	// EndSession succeeds when the session is already gone.
	EndSession(ctx context.Context, tokenHash string) error
	// EndExpiredSessions ends the sessions last seen at or before seenBy
	// or created at or before createdBy, returning how many.
	EndExpiredSessions(ctx context.Context, seenBy, createdBy time.Time) (int, error)
}

// PasswordHasher turns a password into a self-describing hash string and
// checks a password against one.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, hash string) (bool, error)
	// NeedsRehash reports whether hash was made with other parameters
	// than Hash uses now.
	NeedsRehash(hash string) bool
}
