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
	GetApp(ctx context.Context, id string) (domain.App, error)
	ListApps(ctx context.Context) ([]domain.App, error)
	FindAppsByRepo(ctx context.Context, repoURL, branch string) ([]domain.App, error)
}

type DeploymentRepository interface {
	// SaveDeployment inserts or replaces a deployment by ID.
	SaveDeployment(ctx context.Context, dep domain.Deployment) error
	GetDeployment(ctx context.Context, id string) (domain.Deployment, error)
	ListDeployments(ctx context.Context, appID string) ([]domain.Deployment, error)
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

// AdminRepository stores the one Admin.
type AdminRepository interface {
	// CreateAdmin returns domain.ErrConflict when an Admin already exists.
	CreateAdmin(ctx context.Context, admin domain.Admin) error
	// GetAdmin returns domain.ErrNotFound when there is no Admin.
	GetAdmin(ctx context.Context) (domain.Admin, error)
	// ResetAdminPassword, in one transaction, replaces the Admin's
	// password hash, deletes every session and clears the failure count.
	// It returns domain.ErrNotFound when there is no Admin.
	ResetAdminPassword(ctx context.Context, hash string, at time.Time) error
}

// SessionRepository stores Dashboard sessions by the hash of their token.
type SessionRepository interface {
	CreateSession(ctx context.Context, s domain.Session) error
	// GetSession returns domain.ErrNotFound when there is no such session.
	GetSession(ctx context.Context, tokenHash string) (domain.Session, error)
	TouchSession(ctx context.Context, tokenHash string, at time.Time) error
	// DeleteSession succeeds when the session is already gone.
	DeleteSession(ctx context.Context, tokenHash string) error
}

// PasswordHasher turns a password into a self-describing hash string and
// checks a password against one.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, hash string) (bool, error)
}
