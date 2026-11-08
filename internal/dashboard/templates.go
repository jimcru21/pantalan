package dashboard

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// views are the parsed templates: the login screen and one template set
// per Dashboard page. Each page set holds base.html, shell.html and the
// page's own file, which defines "main" and any fragment blocks.
type views struct {
	login *template.Template
	pages map[string]*template.Template // by page name
}

// parseViews parses every template once, at startup, so a broken template
// fails New rather than a request.
func parseViews(fsys fs.FS, assetBase string, pageNames []string) (*views, error) {
	icons, err := loadIcons(fsys)
	if err != nil {
		return nil, err
	}
	funcs := template.FuncMap{
		// asset is the URL of a file under web/static.
		"asset": func(name string) string { return assetBase + name },
		// icon is an inline SVG from templates/icons/<name>.svg.
		"icon": func(name string) (template.HTML, error) {
			svg, ok := icons[name]
			if !ok {
				return "", fmt.Errorf("no icon %q in templates/icons", name)
			}
			return svg, nil
		},
	}
	base, err := template.New("base.html").Funcs(funcs).ParseFS(fsys, "base.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	v := &views{pages: make(map[string]*template.Template, len(pageNames))}
	if v.login, err = extend(base, fsys, "login.html"); err != nil {
		return nil, err
	}
	for _, name := range pageNames {
		t, err := extend(base, fsys, "shell.html", "pages/"+name+".html")
		if err != nil {
			return nil, err
		}
		if t.Lookup("main") == nil {
			return nil, fmt.Errorf("parse templates: pages/%s.html does not define \"main\"", name)
		}
		v.pages[name] = t
	}
	return v, nil
}

// extend is a copy of base with more files parsed into it.
func extend(base *template.Template, fsys fs.FS, files ...string) (*template.Template, error) {
	t, err := base.Clone()
	if err != nil {
		return nil, err
	}
	if t, err = t.ParseFS(fsys, files...); err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return t, nil
}

func loadIcons(fsys fs.FS) (map[string]template.HTML, error) {
	files, err := fs.Glob(fsys, "icons/*.svg")
	if err != nil {
		return nil, err
	}
	icons := make(map[string]template.HTML, len(files))
	for _, f := range files {
		svg, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		// Trusted: the icons are embedded in the binary.
		icons[strings.TrimSuffix(path.Base(f), ".svg")] = template.HTML(bytes.TrimSpace(svg))
	}
	return icons, nil
}

// render executes the named template into a buffer first, so a failing
// template answers 500 instead of half a page.
func (d *Dashboard) render(w http.ResponseWriter, status int, t *template.Template, name string, data any) {
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		d.log.Error("render template", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}
