package httpapi

import "github.com/pantalan/pantalan/internal/domain"

// AppListItem is one row of GET /api/v1/apps: the App, and its newest
// Deployment so the list can show each App's status without a request per
// App. LatestDeployment is missing for an App never deployed.
type AppListItem struct {
	domain.App       `tstype:",extends"`
	LatestDeployment *domain.Deployment `json:"latest_deployment,omitempty" tstype:"domain.Deployment"`
}
