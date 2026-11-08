package dashboard_test

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/pantalan/pantalan/internal/dashboard"
	"github.com/pantalan/pantalan/web"
)

// templatesWith is the real template tree with some files replaced.
func templatesWith(t *testing.T, replace map[string]string) fs.FS {
	t.Helper()
	tree := fstest.MapFS{}
	err := fs.WalkDir(web.Templates(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(web.Templates(), path)
		tree[path] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range replace {
		tree[path] = &fstest.MapFile{Data: []byte(content)}
	}
	return tree
}

func deps(templates fs.FS) dashboard.Deps {
	return dashboard.Deps{Templates: templates, Static: web.Static(), AssetVersion: "test", Hostname: "node"}
}

func TestTheEmbeddedTemplatesParse(t *testing.T) {
	if _, err := dashboard.New(deps(web.Templates())); err != nil {
		t.Fatalf("New with the embedded templates: %v", err)
	}
}

func TestABrokenTemplateFailsNew(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"syntax error in the layout":  {"base.html": `{{define "base"}}{{if}}{{end}}`},
		"syntax error in the login":   {"login.html": `{{define "body"}}{{.Hostname}{{end}}`},
		"syntax error in a page":      {"pages/overview.html": `{{define "main"}}{{range}}{{end}}`},
		"page that defines no main":   {"pages/overview.html": `{{define "other"}}{{end}}`},
		"page missing a fragment":     {"pages/overview.html": `{{define "main"}}{{end}}{{define "topbar"}}{{end}}`},
		"call of an unknown function": {"shell.html": `{{define "body"}}{{nosuchfunc}}{{end}}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := dashboard.New(deps(templatesWith(t, files))); err == nil {
				t.Error("New succeeded, want a parse error")
			}
		})
	}
}
