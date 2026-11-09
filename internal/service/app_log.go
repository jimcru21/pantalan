package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pantalan/pantalan/internal/domain"
)

const (
	// AppLogTail is how many of its last lines a first look at an App
	// Log starts with.
	AppLogTail = 200
	// appLogRestartWait is how long Follow waits before following a
	// live container again once it has stopped: Docker restarts it.
	appLogRestartWait = time.Second
)

var (
	// ErrNoLiveDeployment is why an App Log can't be followed: the App
	// has no live Deployment, so no container to follow.
	ErrNoLiveDeployment = errors.New("the App has no live Deployment")
	// ErrReplaced ends an App Log: a new Deployment replaced the
	// container it was following.
	ErrReplaced = errors.New("a new Deployment replaced the container")
)

// AppLogLine is one piece of an App Log and where it is in it.
type AppLogLine struct {
	LogLine
	Cursor AppLogCursor
}

// AppLogCursor is a place in an App Log: just after the line written at
// Time by the container of the Deployment with ID Deployment. A reconnect
// passes the last one it got to Follow, to go on from there.
type AppLogCursor struct {
	Deployment string
	Time       time.Time
}

type AppLogDeps struct {
	Apps        AppRepository
	Deployments DeploymentRepository
	Containers  ContainerLogs
	// After waits before following a restarted container again; nil
	// means time.After.
	After func(time.Duration) <-chan time.Time
}

// AppLogs follows Apps' App Logs: what the container of an App's live
// Deployment writes to stdout and stderr.
type AppLogs struct{ deps AppLogDeps }

func NewAppLogs(deps AppLogDeps) *AppLogs {
	if deps.After == nil {
		deps.After = time.After
	}
	return &AppLogs{deps: deps}
}

// Follow sends the App Log of the App with ID appID to send: its last
// AppLogTail lines (or, given a cursor, what came after it), then each
// line as the container writes it. When the container stops while its
// Deployment is still live, Docker restarts it, and Follow goes on with
// the restarted container's output.
//
// It returns domain.ErrNotFound when there is no such App,
// ErrNoLiveDeployment when the App has no live Deployment (or no longer
// exists), ErrReplaced when a new Deployment replaced the container
// (before the cursor's, too), ctx's error when ctx ends, or send's error.
func (s *AppLogs) Follow(ctx context.Context, appID string, after AppLogCursor, send func(AppLogLine) error) error {
	if _, err := s.deps.Apps.GetApp(ctx, appID); err != nil {
		return err
	}
	dep, err := s.deps.Deployments.LiveDeployment(ctx, appID)
	if errors.Is(err, domain.ErrNotFound) {
		if after.Deployment != "" {
			return ErrReplaced
		}
		return ErrNoLiveDeployment
	}
	if err != nil {
		return fmt.Errorf("find live deployment: %w", err)
	}
	if after.Deployment != "" && after.Deployment != dep.ID {
		return ErrReplaced
	}

	start := LogStart{Tail: AppLogTail}
	if after.Deployment != "" {
		start = LogStart{Since: after.Time}
	}
	for {
		// Following from a time starts with the line sent last, at that
		// time: skip it, and anything else already sent.
		sent := start.Since
		err := s.deps.Containers.FollowLogs(ctx, dep.ContainerID, start, func(l LogLine) error {
			if !sent.IsZero() && !l.Time.After(sent) {
				return nil
			}
			sent = time.Time{}
			if err := send(AppLogLine{LogLine: l, Cursor: AppLogCursor{Deployment: dep.ID, Time: l.Time}}); err != nil {
				return err
			}
			start = LogStart{Since: l.Time}
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		// The container has stopped, or is gone: replaced, or restarting?
		followed, err := s.deps.Deployments.GetDeployment(ctx, dep.ID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			return ErrNoLiveDeployment // the App was deleted
		case err != nil:
			return fmt.Errorf("get deployment: %w", err)
		case followed.Status != domain.DeploymentLive:
			return ErrReplaced
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.deps.After(appLogRestartWait):
		}
	}
}
