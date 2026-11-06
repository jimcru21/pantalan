package domain

import (
	"strings"
	"time"
)

// DeploymentStatus is a deployment's position in its lifecycle:
//
//	queued → building → starting → live → superseded
//	                 ↘          ↘
//	                  failed     failed
type DeploymentStatus string

const (
	DeploymentQueued     DeploymentStatus = "queued"
	DeploymentBuilding   DeploymentStatus = "building"
	DeploymentStarting   DeploymentStatus = "starting"
	DeploymentLive       DeploymentStatus = "live"
	DeploymentFailed     DeploymentStatus = "failed"
	DeploymentSuperseded DeploymentStatus = "superseded"
)

// Deployment is one attempt to put a specific revision of an App live.
type Deployment struct {
	ID          string           `json:"id"`
	AppID       string           `json:"app_id"`
	Ref         string           `json:"ref"`    // branch, tag or commit requested
	Commit      string           `json:"commit"` // commit actually built
	Image       string           `json:"image"`
	ContainerID string           `json:"container_id,omitempty"`
	Upstream    string           `json:"upstream,omitempty"` // host:port the proxy forwards to
	Status      DeploymentStatus `json:"status"`
	Error       string           `json:"error,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	FinishedAt  *time.Time       `json:"finished_at,omitempty"`
}

// ValidateRef rejects refs that git would parse as a flag.
func ValidateRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return invalid("ref is invalid")
	}
	return nil
}
