package httpapi_test

import (
	"io/fs"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Deleting an App removes what it left on the Node (its container, its
// images, its route and its Deployment Logs) and its records.

func TestDeletingAnAppRemovesItsContainerImagesRouteLogsAndRecords(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	kept := s.createApp("kept")
	keptDep := s.deploy(kept, "")
	s.runQueued()

	gone := s.createApp("gone")
	superseded := s.deploy(gone, "v1")
	s.runQueued()
	live := s.deploy(gone, "v2")
	s.runQueued()
	s.builder.fail("compile error")
	failed := s.deploy(gone, "v3")
	s.runQueued()
	goneDeps := []string{superseded, live, failed}

	keptImage := decodeDeployment(t, "GET kept's Deployment", s.get("/api/v1/apps/"+kept+"/deployments/"+keptDep, withSession(session)), http.StatusOK).Image
	var goneImages []string
	for _, id := range goneDeps {
		d := decodeDeployment(t, "GET gone's Deployment", s.get("/api/v1/apps/"+gone+"/deployments/"+id, withSession(session)), http.StatusOK)
		goneImages = append(goneImages, d.Image)
	}
	// The scene is what it should be before the delete.
	// The failed build of v3 left no image behind.
	if got := s.runtime.images(); len(got) != 3 {
		t.Fatalf("before the delete Docker has images %v, want kept's and gone's 2", got)
	}
	if got := s.router.table(); len(got) != 2 {
		t.Fatalf("before the delete the routes are %v, want kept's and gone's", got)
	}
	if got := s.logFiles(); len(got) != 4 {
		t.Fatalf("before the delete the Deployment Logs are %v, want 4", got)
	}

	got := s.do(http.MethodDelete, "/api/v1/apps/"+gone, "", withSession(session))
	if got.status != http.StatusNoContent || got.body != "" {
		t.Fatalf("DELETE the App = %d %q, want 204 and no body", got.status, got.body)
	}

	t.Run("its container is removed", func(t *testing.T) {
		if got := s.runtime.containersOf(gone); len(got) != 0 {
			t.Errorf("gone's containers after the delete = %v, want none", got)
		}
		if got := s.runtime.containersOf(kept); len(got) != 1 {
			t.Errorf("kept's containers after the delete = %v, want its live one", got)
		}
	})
	t.Run("its images are removed", func(t *testing.T) {
		if got := s.runtime.images(); !slices.Equal(got, []string{keptImage}) {
			t.Errorf("images after the delete = %v, want only kept's %s (gone's were %v)", got, keptImage, goneImages)
		}
	})
	t.Run("its route is removed", func(t *testing.T) {
		routes := s.router.table()
		if _, ok := routes["gone.example.test"]; ok || len(routes) != 1 || routes["kept.example.test"] == "" {
			t.Errorf("routes after the delete = %v, want only kept.example.test", routes)
		}
	})
	t.Run("its Deployment Logs are gone", func(t *testing.T) {
		files := s.logFiles()
		for _, id := range goneDeps {
			for _, f := range files {
				if strings.Contains(f, id) {
					t.Errorf("Deployment Log %s of gone's Deployment %s is still there", f, id)
				}
			}
		}
		if len(files) != 1 || !strings.Contains(files[0], keptDep) {
			t.Errorf("Deployment Logs after the delete = %v, want only kept's %s", files, keptDep)
		}
		for _, id := range goneDeps {
			for _, path := range []string{
				"/api/v1/deployments/" + id + "/logs",
				"/api/v1/apps/" + gone + "/deployments/" + id + "/log",
			} {
				if got := s.get(path, withSession(session)); got.status != http.StatusNotFound {
					t.Errorf("GET %s = %d %q, want 404", path, got.status, got.body)
				}
			}
		}
	})
	t.Run("the App and its Deployments are no longer returned", func(t *testing.T) {
		if got := s.get("/api/v1/apps/"+gone, withSession(session)); got.status != http.StatusNotFound {
			t.Errorf("GET the deleted App = %d %q, want 404", got.status, got.body)
		}
		if got := s.get("/api/v1/apps/"+gone+"/deployments", withSession(session)); got.status != http.StatusNotFound {
			t.Errorf("GET the deleted App's Deployments = %d %q, want 404", got.status, got.body)
		}
		for _, id := range goneDeps {
			if got := s.get("/api/v1/apps/"+gone+"/deployments/"+id, withSession(session)); got.status != http.StatusNotFound {
				t.Errorf("GET Deployment %s of the deleted App = %d %q, want 404", id, got.status, got.body)
			}
		}
		apps := s.listApps(withSession(session))
		if len(apps) != 1 || apps[0].ID != kept {
			t.Errorf("the Apps list after the delete = %+v, want only kept", apps)
		}
	})
}

func TestDeletingAnAppRemovesItsLiveRouteEvenAfterItsDomainChanged(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	app := s.createApp("moved")
	s.deploy(app, "")
	s.runQueued()
	// The live container stays routed under the old domain until the next
	// Deployment.
	edit := s.do(http.MethodPut, "/api/v1/apps/"+app,
		`{"name":"moved","repo_url":"https://example.test/acme/moved.git","domain":"elsewhere.example.test","port":8080}`,
		withSession(session))
	if edit.status != http.StatusOK {
		t.Fatalf("PUT the new domain = %d %q, want 200", edit.status, edit.body)
	}
	if routes := s.router.table(); routes["moved.example.test"] == "" {
		t.Fatalf("routes before the delete = %v, want the old domain still routed", routes)
	}

	if got := s.do(http.MethodDelete, "/api/v1/apps/"+app, "", withSession(session)); got.status != http.StatusNoContent {
		t.Fatalf("DELETE the App = %d %q, want 204", got.status, got.body)
	}
	if routes := s.router.table(); len(routes) != 0 {
		t.Errorf("routes after the delete = %v, want none", routes)
	}
}

func TestDeletingAnAppWhileADeploymentOfItIsInFlightIsAConflictThatChangesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(s *seam, app string) (finish func())
	}{
		{"queued", func(s *seam, app string) func() {
			s.deploy(app, "v2")
			return s.runQueued
		}},
		{"building", func(s *seam, app string) func() {
			s.deploy(app, "v2")
			held := s.builder.hold()
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.runQueued()
			}()
			held.started(s.t)
			return func() {
				held.finish()
				<-done
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSeam(t)
			session := s.signedIn()
			app := s.createApp("busy")
			s.deploy(app, "v1")
			s.runQueued()
			finish := tc.start(s, app)

			beforeDeps := s.get("/api/v1/apps/"+app+"/deployments", withSession(session))
			routes, images, ctrs, logs := s.router.table(), s.runtime.images(), s.runtime.containersOf(app), s.logFiles()

			got := s.do(http.MethodDelete, "/api/v1/apps/"+app, "", withSession(session))
			if got.status != http.StatusConflict {
				t.Fatalf("DELETE while a Deployment is %s = %d %q, want 409", tc.name, got.status, got.body)
			}
			if msg, _ := field(t, got, "error").(string); !strings.Contains(msg, tc.name) {
				t.Errorf("the conflict says %q, want it to say the Deployment is %s", msg, tc.name)
			}

			if read := s.get("/api/v1/apps/"+app, withSession(session)); read.status != http.StatusOK {
				t.Errorf("GET the App after the refusal = %d %q, want 200", read.status, read.body)
			}
			if afterDeps := s.get("/api/v1/apps/"+app+"/deployments", withSession(session)); afterDeps.body != beforeDeps.body {
				t.Errorf("the Deployments after the refusal = %s, want them as before: %s", afterDeps.body, beforeDeps.body)
			}
			if got := s.router.table(); !maps.Equal(got, routes) {
				t.Errorf("routes after the refusal = %v, want %v", got, routes)
			}
			if got := s.runtime.images(); !slices.Equal(got, images) {
				t.Errorf("images after the refusal = %v, want %v", got, images)
			}
			if got := s.runtime.containersOf(app); !slices.Equal(got, ctrs) {
				t.Errorf("containers after the refusal = %v, want %v", got, ctrs)
			}
			if got := s.logFiles(); !slices.Equal(got, logs) {
				t.Errorf("Deployment Logs after the refusal = %v, want %v", got, logs)
			}

			// Once it has finished, the App can be deleted.
			finish()
			if got := s.do(http.MethodDelete, "/api/v1/apps/"+app, "", withSession(session)); got.status != http.StatusNoContent {
				t.Errorf("DELETE once the Deployment finished = %d %q, want 204", got.status, got.body)
			}
		})
	}
}

func TestDeletingAnUnknownAppIsNotFound(t *testing.T) {
	s := newSeam(t)
	got := s.do(http.MethodDelete, "/api/v1/apps/nosuchapp", "", withSession(s.signedIn()))
	if got.status != http.StatusNotFound {
		t.Errorf("DELETE an unknown App = %d %q, want 404", got.status, got.body)
	}
	if msg, _ := field(t, got, "error").(string); msg == "" {
		t.Errorf("the 404 has no error message: %s", got.body)
	}
}

func TestAnAppCanBeDeletedWithTheAdminToken(t *testing.T) {
	s := newSeam(t)
	app := s.createApp("scripted")
	if got := s.do(http.MethodDelete, "/api/v1/apps/"+app, "", withToken(adminToken)); got.status != http.StatusNoContent {
		t.Errorf("DELETE with the Admin Token = %d %q, want 204", got.status, got.body)
	}
	if got := s.do(http.MethodDelete, "/api/v1/apps/"+app, "", withToken("wrong")); got.status != http.StatusUnauthorized {
		t.Errorf("DELETE with a wrong token = %d %q, want 401", got.status, got.body)
	}
}

// logFiles lists every file under the Deployer's data dir (Deployment
// Logs, and any build checkout left behind), relative to it.
func (s *seam) logFiles() []string {
	s.t.Helper()
	var files []string
	err := filepath.WalkDir(s.dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(s.dataDir, path)
		files = append(files, rel)
		return err
	})
	if err != nil {
		s.t.Fatalf("walk the data dir: %v", err)
	}
	return files
}
