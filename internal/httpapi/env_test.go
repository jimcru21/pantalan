package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An App's env vars are read and written as one dotenv-format text through
// their own endpoint, the only place their values appear.

// envBody is the JSON body that carries text as an App's env vars.
func envBody(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"env": text})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEnvVarsWrittenAsTextReadBackTheSame(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	id := s.createApp("shop")
	text := "DATABASE_URL=postgres://db/shop?sslmode=disable\nGREETING=hello world\n"

	got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", envBody(t, text), withSession(session))

	assertJSON(t, "PUT /api/v1/apps/{id}/env", got, http.StatusOK, envBody(t, text))
	assertJSON(t, "GET /api/v1/apps/{id}/env", s.get("/api/v1/apps/"+id+"/env", withSession(session)), http.StatusOK, envBody(t, text))
}

func TestCommentsAndBlankLinesAreIgnoredAndTheRestReadsBackSortedByName(t *testing.T) {
	s := newSeam(t)
	id := s.createApp("shop")
	pasted := "# shop's .env\r\n\r\nPORT=8080\r\n   \r\n  # the database\r\nDATABASE_URL=postgres://db\r\n  TOKEN=a=b # not a comment\r\n"

	got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", envBody(t, pasted), withToken(adminToken))

	want := envBody(t, "DATABASE_URL=postgres://db\nPORT=8080\nTOKEN=a=b # not a comment\n")
	assertJSON(t, "PUT a pasted .env", got, http.StatusOK, want)
	assertJSON(t, "GET it back", s.get("/api/v1/apps/"+id+"/env", withToken(adminToken)), http.StatusOK, want)
}

func TestABadLineIsReportedWithItsLineNumberAndChangesNothing(t *testing.T) {
	for name, tc := range map[string]struct{ text, line string }{
		"a line with no =":       {"A=1\n\nnot a pair\n", "line 3: "},
		"a line with no name":    {"# top\n=value\n", "line 2: "},
		"a name with a space":    {"A=1\nMY KEY=1\n", "line 2: "},
		"a name starting with -": {"-e=1\n", "line 1: "},
		"a name set twice":       {"A=1\nB=2\nA=3\n", "line 3: "},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			session := s.signedIn()
			id := s.createApp("shop")
			before := envBody(t, "KEEP=me\n")
			if got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", before, withSession(session)); got.status != http.StatusOK {
				t.Fatalf("PUT the first env = %d %q", got.status, got.body)
			}

			got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", envBody(t, tc.text), withSession(session))

			if got.status != http.StatusBadRequest || fieldOf(t, got) != "env" {
				t.Fatalf("PUT %q = %d %q, want 400 about env", tc.text, got.status, got.body)
			}
			if msg, _ := field(t, got, "error").(string); !strings.HasPrefix(msg, tc.line) {
				t.Errorf("PUT %q error = %q, want it to start %q", tc.text, msg, tc.line)
			}
			assertJSON(t, "GET after a refused PUT", s.get("/api/v1/apps/"+id+"/env", withSession(session)), http.StatusOK, before)
		})
	}
}

func TestAPutWithoutEnvIsRefusedRatherThanClearingThem(t *testing.T) {
	s := newSeam(t)
	id := s.createApp("shop")
	before := envBody(t, "KEEP=me\n")
	s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", before, withToken(adminToken))

	got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", `{}`, withToken(adminToken))

	if got.status != http.StatusBadRequest || fieldOf(t, got) != "env" {
		t.Errorf("PUT {} = %d %q, want 400 about env", got.status, got.body)
	}
	assertJSON(t, "GET after PUT {}", s.get("/api/v1/apps/"+id+"/env", withToken(adminToken)), http.StatusOK, before)
}

func TestEmptyTextClearsTheEnvVars(t *testing.T) {
	s := newSeam(t)
	id := s.createApp("shop")
	s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", envBody(t, "A=1\n"), withToken(adminToken))

	got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", envBody(t, "# nothing left\n"), withToken(adminToken))

	assertJSON(t, "PUT only a comment", got, http.StatusOK, `{"env":""}`)
	assertJSON(t, "GET after clearing", s.get("/api/v1/apps/"+id+"/env", withToken(adminToken)), http.StatusOK, `{"env":""}`)
}

func TestSavingEnvVarsStartsNoDeploymentAndTheNextDeploymentRunsWithThem(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	id := s.createApp("shop")
	s.deploy(id, "")
	s.runQueued()
	before := s.get("/api/v1/apps/"+id+"/deployments", withSession(session))

	got := s.do(http.MethodPut, "/api/v1/apps/"+id+"/env", envBody(t, "GREETING=hi\n"), withSession(session))
	if got.status != http.StatusOK {
		t.Fatalf("PUT env = %d %q, want 200", got.status, got.body)
	}
	s.runQueued()

	assertJSON(t, "GET deployments after saving env vars", s.get("/api/v1/apps/"+id+"/deployments", withSession(session)), http.StatusOK, before.body)
	if env := s.runtime.envOf("ctr-1"); len(env) != 0 {
		t.Errorf("the live container's env = %v, want it untouched by the save", env)
	}

	s.deploy(id, "")
	s.runQueued()
	if env := s.runtime.envOf("ctr-2"); env["GREETING"] != "hi" || len(env) != 1 {
		t.Errorf("the next Deployment's container env = %v, want GREETING=hi", env)
	}
}

// App responses name the env vars but never carry a value, so the App page
// and the Apps list can't show one, and neither can a script's log.
func TestAppResponsesListEnvVarNamesButNeverTheirValues(t *testing.T) {
	s := newSeam(t)
	session := s.signedIn()
	const secret = "s3cret-value"
	created := s.do(http.MethodPost, "/api/v1/apps",
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":3000,"env":{"STRIPE_KEY":"`+secret+`","DATABASE_URL":"`+secret+`"}}`,
		withToken(adminToken))
	if created.status != http.StatusCreated {
		t.Fatalf("create = %d %q, want 201", created.status, created.body)
	}
	id := field(t, created, "id").(string)
	s.deploy(id, "")
	s.runQueued()

	for what, got := range map[string]response{
		"POST /api/v1/apps":     created,
		"GET /api/v1/apps/{id}": s.get("/api/v1/apps/"+id, withSession(session)),
		"GET /api/v1/apps":      s.get("/api/v1/apps", withSession(session)),
		"PUT /api/v1/apps/{id}": s.do(http.MethodPut, "/api/v1/apps/"+id,
			`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":3000}`,
			withSession(session)),
	} {
		if got.status/100 != 2 {
			t.Fatalf("%s = %d %q, want a success", what, got.status, got.body)
		}
		if strings.Contains(got.body, secret) {
			t.Errorf("%s carries an env value: %s", what, got.body)
		}
		app := got.body
		if what == "GET /api/v1/apps" {
			var list []json.RawMessage
			if err := json.Unmarshal([]byte(got.body), &list); err != nil || len(list) != 1 {
				t.Fatalf("%s = %s, want one App", what, got.body)
			}
			app = string(list[0])
		}
		var names struct {
			EnvNames []string `json:"env_names"`
			Env      any      `json:"env"`
		}
		if err := json.Unmarshal([]byte(app), &names); err != nil {
			t.Fatalf("%s body %q: %v", what, app, err)
		}
		if strings.Join(names.EnvNames, ",") != "DATABASE_URL,STRIPE_KEY" || names.Env != nil {
			t.Errorf("%s = %s, want env_names [DATABASE_URL STRIPE_KEY] and no env", what, app)
		}
	}
}

func TestAnAppWithNoEnvVarsListsNoNames(t *testing.T) {
	s := newSeam(t)
	id := s.createApp("shop")

	if got := s.get("/api/v1/apps/"+id, withToken(adminToken)); asJSON(t, field(t, got, "env_names")) != `[]` {
		t.Errorf("GET an App with no env vars = %s, want env_names []", got.body)
	}
}

// The create request still takes an env map. Its env vars must read back
// as the same text, so a value can't hold a line break, nor a name start
// with "#".
func TestCreateRefusesEnvVarsThatTextCannotHold(t *testing.T) {
	for name, env := range map[string]string{
		"a value with a line break": `{"KEY":"a\nb"}`,
		"a name that is a comment":  `{"#KEY":"1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := newSeam(t)
			got := s.do(http.MethodPost, "/api/v1/apps",
				`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":3000,"env":`+env+`}`,
				withToken(adminToken))
			if got.status != http.StatusBadRequest || fieldOf(t, got) != "env" {
				t.Errorf("POST with %s = %d %q, want 400 about env", name, got.status, got.body)
			}
		})
	}
}

func TestACreatedAppsEnvVarsReadBackAsText(t *testing.T) {
	s := newSeam(t)
	created := s.do(http.MethodPost, "/api/v1/apps",
		`{"name":"shop","repo_url":"https://example.test/acme/shop.git","domain":"shop.example.test","port":3000,"env":{"PORT":"3000","DATABASE_URL":"postgres://db"}}`,
		withToken(adminToken))
	id := field(t, created, "id").(string)

	assertJSON(t, "GET a created App's env", s.get("/api/v1/apps/"+id+"/env", withToken(adminToken)), http.StatusOK,
		envBody(t, "DATABASE_URL=postgres://db\nPORT=3000\n"))
}

func TestTheEnvOfAnUnknownAppIsNotFound(t *testing.T) {
	s := newSeam(t)

	for _, got := range []response{
		s.get("/api/v1/apps/nosuchapp/env", withToken(adminToken)),
		s.do(http.MethodPut, "/api/v1/apps/nosuchapp/env", envBody(t, "A=1\n"), withToken(adminToken)),
	} {
		if got.status != http.StatusNotFound {
			t.Errorf("env of an unknown App = %d %q, want 404", got.status, got.body)
		}
	}
}
