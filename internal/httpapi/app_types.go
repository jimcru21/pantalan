package httpapi

import "github.com/pantalan/pantalan/internal/domain"

// App is how every App response shows an App: its settings, and the names
// of its env vars, sorted. Values may be secrets, so they appear only in
// the env endpoint's answer (AppEnv), never here.
type App struct {
	domain.App `tstype:",extends"`
	EnvNames   []string `json:"env_names"`
}

func appOf(a domain.App) App { return App{App: a, EnvNames: domain.EnvNames(a.Env)} }

// AppListItem is one row of GET /api/v1/apps: the App, and its newest
// Deployment so the list can show each App's status without a request per
// App. LatestDeployment is missing for an App never deployed.
type AppListItem struct {
	App              `tstype:",extends"`
	LatestDeployment *domain.Deployment `json:"latest_deployment,omitempty" tstype:"domain.Deployment"`
}
