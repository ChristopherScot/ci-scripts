package runtime

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"strings"
	"text/template"
)

// templates holds every runtime's files. They are real files rather than
// Go string literals so they can be read, diffed and edited as the
// Dockerfiles and source they are - and so adding a runtime is mostly
// adding a directory.
//
//go:embed templates
var templates embed.FS

// embedded implements Runtime from a directory under templates/. A runtime
// is then a data declaration plus its files; only genuinely different
// behaviour needs Go code.
type embedded struct {
	name     string
	dir      string
	hardened bool

	// files maps a template file to the path it is written to in the
	// generated service. A .tmpl suffix means it is rendered with Params;
	// anything else is copied verbatim.
	files map[string]string
}

func (e embedded) Name() string           { return e.name }
func (e embedded) SupportsHardened() bool { return e.hardened }

func (e embedded) read(name string) string {
	b, err := templates.ReadFile(path.Join("templates", e.dir, name))
	if err != nil {
		// Only reachable if a template is missing from the binary, which
		// is a build-time mistake rather than a runtime condition.
		panic(fmt.Sprintf("runtime %q: missing template %s: %v", e.name, name, err))
	}
	return string(b)
}

func (e embedded) render(name string, p Params) string {
	t, err := template.New(name).Parse(e.read(name))
	if err != nil {
		panic(fmt.Sprintf("runtime %q: template %s: %v", e.name, name, err))
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		panic(fmt.Sprintf("runtime %q: template %s: %v", e.name, name, err))
	}
	return buf.String()
}

func (e embedded) Dockerfile(p Params) string { return e.render("Dockerfile", p) }

func (e embedded) BuildSteps(p Params) string {
	// Steps are plain YAML, but rendered anyway so a runtime can vary them
	// by port or name if it needs to.
	return strings.TrimRight(e.render("steps.yaml", p), "\n") + "\n"
}

func (e embedded) Files(p Params) []File {
	out := make([]File, 0, len(e.files))
	for src, dst := range e.files {
		body := e.read(src)
		if strings.HasSuffix(src, ".tmpl") {
			body = e.render(src, p)
		}
		out = append(out, File{Path: dst, Body: body})
	}
	return out
}
