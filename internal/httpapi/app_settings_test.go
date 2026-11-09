package httpapi_test

import (
	"net/http"
	"testing"
)

// The create form and the edit form send an App's settings; these are the
// rules the API applies to them.

func TestTheAdminCreatesAnAppFromTheDashboard(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()

	got := s.do(http.MethodPost, "/api/v1/apps",
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","branch":"release","build_type":"nixpacks","domain":"shop.example.test","port":3000}`,
		withSession(session))

	if got.status != http.StatusCreated {
		t.Fatalf("POST /api/v1/apps = %d %q, want 201", got.status, got.body)
	}
	id, _ := field(t, got, "id").(string)
	if id == "" {
		t.Fatalf("the created App has no id: %s", got.body)
	}
	want := map[string]any{
		"name": "shop", "repo_url": "https://example.test/acme/shop.git", "branch": "release",
		"build_type": "nixpacks", "domain": "shop.example.test", "port": 3000.0,
	}
	for path, v := range want {
		if field(t, got, path) != v {
			t.Errorf("created App %s = %v, want %v", path, field(t, got, path), v)
		}
	}
	if read := s.get("/api/v1/apps/"+id, withSession(session)); read.status != http.StatusOK || field(t, read, "name") != "shop" {
		t.Errorf("GET the created App = %d %q, want 200 with it", read.status, read.body)
	}
}

func TestACreatedAppDefaultsToBranchMainAndADockerfile(t *testing.T) {
	s := newSeam(t)

	got := s.do(http.MethodPost, "/api/v1/apps",
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":3000}`,
		withToken(adminToken))

	if got.status != http.StatusCreated || field(t, got, "branch") != "main" || field(t, got, "build_type") != "dockerfile" {
		t.Errorf("POST /api/v1/apps without branch or build_type = %d %q, want 201 with main and dockerfile", got.status, got.body)
	}
}

func TestAnUpdateChangesTheSettingsAndKeepsTheIDCreationTimeAndEnv(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	created := s.do(http.MethodPost, "/api/v1/apps",
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":3000,"env":{"DATABASE_URL":"postgres://db"}}`,
		withToken(adminToken))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d %q, want 201", created.status, created.body)
	}
	id := field(t, created, "id").(string)
	createdAt := field(t, created, "created_at")

	got := s.do(http.MethodPut, "/api/v1/apps/"+id,
		`{"name":"store","repo_url":"https://example.test/acme/store.git","branch":"release","build_type":"nixpacks","domain":"store.example.test","port":8080}`,
		withSession(session))

	want := `{"id":"` + id + `","name":"store","repo_url":"https://example.test/acme/store.git","branch":"release",` +
		`"build_type":"nixpacks","domain":"store.example.test","port":8080,` +
		`"env":{"DATABASE_URL":"postgres://db"},"created_at":` + asJSON(t, createdAt) + `}`
	assertJSON(t, "PUT /api/v1/apps/{id}", got, http.StatusOK, want)
	assertJSON(t, "GET the updated App", s.get("/api/v1/apps/"+id, withSession(session)), http.StatusOK, want)
}

func TestAnUpdateCanKeepTheAppsOwnDomain(t *testing.T) {
	s := newSeam(t)
	id := s.createApp("shop")

	got := s.do(http.MethodPut, "/api/v1/apps/"+id,
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","branch":"next","domain":"SHOP.example.test","port":8080}`,
		withToken(adminToken))

	if got.status != http.StatusOK || field(t, got, "branch") != "next" {
		t.Errorf("PUT keeping the App's own domain = %d %q, want 200", got.status, got.body)
	}
}

func TestEditingAnAppNeverStartsADeployment(t *testing.T) {
	s := newSeam(t)
	id := s.createApp("shop")

	got := s.do(http.MethodPut, "/api/v1/apps/"+id,
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","branch":"next","domain":"shop.example.test","port":8080}`,
		withToken(adminToken))
	if got.status != http.StatusOK {
		t.Fatalf("PUT = %d %q, want 200", got.status, got.body)
	}

	assertJSON(t, "GET deployments after an edit", s.get("/api/v1/apps/"+id+"/deployments", withToken(adminToken)), http.StatusOK, `[]`)
}

func TestUpdatingAnUnknownAppIsNotFound(t *testing.T) {
	s := newSeam(t)

	got := s.do(http.MethodPut, "/api/v1/apps/nosuchapp",
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":8080}`,
		withSession(s.signedIn()))

	if got.status != http.StatusNotFound {
		t.Errorf("PUT /api/v1/apps/nosuchapp = %d %q, want 404", got.status, got.body)
	}
	if msg, _ := field(t, got, "error").(string); msg == "" {
		t.Errorf("PUT /api/v1/apps/nosuchapp body = %q, want an {\"error\"} message", got.body)
	}
	if got := s.listApps(withToken(adminToken)); len(got) != 0 {
		t.Errorf("updating an unknown App created %+v, want no Apps", got)
	}
}

func TestAnEditCannotChangeEnvVarsOrTheIDOrCreationTime(t *testing.T) {
	for name, extra := range map[string]string{
		"env":        `,"env":{"A":"1"}`,
		"id":         `,"id":"other"`,
		"created_at": `,"created_at":"2020-01-01T00:00:00Z"`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			id := s.createApp("shop")
			before := s.get("/api/v1/apps/"+id, withToken(adminToken))

			got := s.do(http.MethodPut, "/api/v1/apps/"+id,
				`{"name":"store","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":8080`+extra+`}`,
				withToken(adminToken))

			if got.status != http.StatusBadRequest {
				t.Errorf("PUT with %s = %d %q, want 400", name, got.status, got.body)
			}
			assertJSON(t, "GET after a refused edit", s.get("/api/v1/apps/"+id, withToken(adminToken)), http.StatusOK, before.body)
		})
	}
}

// Each broken rule answers with the field it is about, so the form can
// show the message beside that field.
func TestEachValidationErrorNamesItsField(t *testing.T) {
	valid := map[string]string{
		"name":       `"shop"`,
		"repo_url":   `"https://example.test/acme/shop.git"`,
		"branch":     `"main"`,
		"build_type": `"dockerfile"`,
		"domain":     `"shop.example.test"`,
		"port":       `8080`,
	}
	cases := []struct {
		name   string
		field  string
		value  string
		status int
	}{
		{"a blank name", "name", `"  "`, http.StatusBadRequest},
		{"a repository starting with a dash", "repo_url", `"-oProxyCommand=x"`, http.StatusBadRequest},
		{"a missing repository", "repo_url", `""`, http.StatusBadRequest},
		{"a branch starting with a dash", "branch", `"-x"`, http.StatusBadRequest},
		{"an unknown build type", "build_type", `"buildpacks"`, http.StatusBadRequest},
		{"a domain with a scheme", "domain", `"https://shop.example.test"`, http.StatusBadRequest},
		{"port zero", "port", `0`, http.StatusBadRequest},
		{"a port too high", "port", `65536`, http.StatusBadRequest},
		{"a port that is not a number", "port", `"8080"`, http.StatusBadRequest},
		{"another App's domain", "domain", `"taken.example.test"`, http.StatusConflict},
	}
	body := func(field, value string) string {
		out := "{"
		for _, k := range []string{"name", "repo_url", "branch", "build_type", "domain", "port"} {
			v := valid[k]
			if k == field {
				v = value
			}
			if out != "{" {
				out += ","
			}
			out += `"` + k + `":` + v
		}
		return out + "}"
	}
	check := func(t *testing.T, what string, got response, status int, wantField string) {
		t.Helper()
		if got.status != status {
			t.Fatalf("%s = %d %q, want %d", what, got.status, got.body, status)
		}
		if f := fieldOf(t, got); f != wantField {
			t.Errorf("%s field = %q, want %q (body %s)", what, f, wantField, got.body)
		}
		if msg, _ := field(t, got, "error").(string); msg == "" {
			t.Errorf("%s body = %q, want an {\"error\"} message", what, got.body)
		}
	}

	for _, tc := range cases {
		t.Run("create with "+tc.name, func(t *testing.T) {
			s := newSeam(t)
			s.createApp("taken")
			got := s.do(http.MethodPost, "/api/v1/apps", body(tc.field, tc.value), withSession(s.signedIn()))
			check(t, "POST /api/v1/apps", got, tc.status, tc.field)
		})
		t.Run("update with "+tc.name, func(t *testing.T) {
			s := newSeam(t)
			s.createApp("taken")
			id := s.createApp("shop")
			got := s.do(http.MethodPut, "/api/v1/apps/"+id, body(tc.field, tc.value), withSession(s.signedIn()))
			check(t, "PUT /api/v1/apps/{id}", got, tc.status, tc.field)
		})
	}

	t.Run("create with a bad env name", func(t *testing.T) {
		s := newSeam(t)
		got := s.do(http.MethodPost, "/api/v1/apps",
			`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":8080,"env":{"A=B":"1"}}`,
			withToken(adminToken))
		check(t, "POST /api/v1/apps", got, http.StatusBadRequest, "env")
	})
}

// fieldOf is the "field" of an error body, "" when it has none.
func fieldOf(t *testing.T, got response) string {
	t.Helper()
	f, _ := field(t, got, "field").(string)
	return f
}
