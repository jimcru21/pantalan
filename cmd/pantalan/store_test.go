package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// apiCall sends an Admin Token request over HTTPS to a running serve's
// Dashboard listener, pinning its certificate, and returns the status and
// body.
func apiCall(t *testing.T, s *running, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, "https://"+s.addr(t, "dashboard")+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := pinnedClient(t, s.env).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, got
}

// stopServe stops a running serve and fails the test unless it exits 0.
func stopServe(t *testing.T, s *running) {
	t.Helper()
	s.stop()
	if code := s.wait(t); code != 0 {
		t.Fatalf("serve exited %d after stop; output:\n%s", code, s.stdout)
	}
}

// A repo path that doesn't exist makes the deployment fail at checkout
// without touching the network or Docker.
const appJSON = `{"name":"web","repo_url":"/nonexistent/web.git","domain":"web.example.test","port":8080,"env":{"GREETING":"hi"}}`

func TestAppsAndDeploymentsSurviveARestart(t *testing.T) {
	env := serveEnv(t)

	first := startServe(t, env)
	code, body := apiCall(t, first, http.MethodPost, "/api/v1/apps", appJSON)
	if code != http.StatusCreated {
		t.Fatalf("create app = %d %s", code, body)
	}
	var app struct {
		ID  string            `json:"id"`
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(body, &app); err != nil {
		t.Fatal(err)
	}
	code, body = apiCall(t, first, http.MethodPost, "/api/v1/apps/"+app.ID+"/deployments", "")
	if code != http.StatusAccepted {
		t.Fatalf("deploy = %d %s", code, body)
	}
	var dep struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &dep); err != nil {
		t.Fatal(err)
	}
	stopServe(t, first)

	second := startServe(t, env)
	code, body = apiCall(t, second, http.MethodGet, "/api/v1/apps/"+app.ID, "")
	if code != http.StatusOK {
		t.Fatalf("get app after restart = %d %s", code, body)
	}
	var got struct {
		Name   string            `json:"name"`
		Domain string            `json:"domain"`
		Env    map[string]string `json:"env"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "web" || got.Domain != "web.example.test" || got.Env["GREETING"] != "hi" {
		t.Errorf("app after restart = %s, want the app as created", body)
	}

	code, body = apiCall(t, second, http.MethodGet, "/api/v1/apps/"+app.ID+"/deployments", "")
	if code != http.StatusOK {
		t.Fatalf("list deployments after restart = %d %s", code, body)
	}
	var deps []struct {
		ID    string `json:"id"`
		AppID string `json:"app_id"`
	}
	if err := json.Unmarshal(body, &deps); err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].ID != dep.ID || deps[0].AppID != app.ID {
		t.Errorf("deployments after restart = %s, want just %s", body, dep.ID)
	}
}

func TestDomainsThatDifferOnlyInCaseConflict(t *testing.T) {
	s := startServe(t, serveEnv(t))
	if code, body := apiCall(t, s, http.MethodPost, "/api/v1/apps", appJSON); code != http.StatusCreated {
		t.Fatalf("create first app = %d %s", code, body)
	}

	code, body := apiCall(t, s, http.MethodPost, "/api/v1/apps",
		`{"name":"shouty","repo_url":"/nonexistent/shouty.git","domain":"WEB.Example.TEST","port":8080}`)

	if code != http.StatusConflict {
		t.Errorf("create app on WEB.Example.TEST = %d %s, want 409", code, body)
	}
	if !strings.Contains(string(body), "used by app web") {
		t.Errorf("conflict body = %s, want it to name the app holding the domain", body)
	}
	_, body = apiCall(t, s, http.MethodGet, "/api/v1/apps", "")
	if n := strings.Count(string(body), `"id"`); n != 1 {
		t.Errorf("apps after the conflict = %s, want only the first", body)
	}
}

// newestMigration is the highest number among the migration files in the
// source tree, which the binary embeds.
func newestMigration(t *testing.T) int {
	t.Helper()
	files, err := filepath.Glob("../../internal/store/sqlite/migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no migrations found (%v)", err)
	}
	newest := 0
	for _, f := range files {
		num, _, _ := strings.Cut(filepath.Base(f), "_")
		n, err := strconv.Atoi(num)
		if err != nil {
			t.Fatalf("migration %s has no number", f)
		}
		newest = max(newest, n)
	}
	return newest
}

// userVersion reads PRAGMA user_version from a database file.
func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestServeOnAFreshDataDirCreatesThePrivateMigratedDatabase(t *testing.T) {
	env := serveEnv(t)
	s := startServe(t, env)
	s.addr(t, "dashboard")
	stopServe(t, s)

	db := filepath.Join(dataDir(t, env), "pantalan.db")
	info, err := os.Stat(db)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("pantalan.db mode = %o, want 600", mode)
	}
	if got, want := userVersion(t, db), newestMigration(t); got != want {
		t.Errorf("user_version = %d, want %d (the newest migration)", got, want)
	}
	if snaps, _ := filepath.Glob(db + ".bak-*"); len(snaps) != 0 {
		t.Errorf("fresh database was snapshotted: %v", snaps)
	}
}

func TestServeRefusesADatabaseNewerThanItKnows(t *testing.T) {
	env := serveEnv(t)
	db := filepath.Join(dataDir(t, env), "pantalan.db")
	future := newestMigration(t) + 1
	setUserVersion(t, db, future)

	got := runCLI(t, t.Context(), env, "serve")

	if got.code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout:\n%s", got.code, got.stdout)
	}
	want := fmt.Sprintf("schema version %d is newer than this pantalan knows (%d)", future, future-1)
	if !strings.Contains(got.stdout+got.stderr, want) {
		t.Errorf("output does not explain the refusal with %q; stdout:\n%s\nstderr:\n%s", want, got.stdout, got.stderr)
	}
	if strings.Contains(got.stdout, "msg=listening") {
		t.Error("serve started listening on a database it refused")
	}
	if v := userVersion(t, db); v != future {
		t.Errorf("user_version = %d after the refusal, want it untouched at %d", v, future)
	}
}

// setUserVersion creates a database file stamped with schema version v.
func setUserVersion(t *testing.T, path string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatal(err)
	}
}
