package runtime

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"sort"
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
	name string
	dir  string
	// deployable: produces a container image and Kubernetes manifests.
	// False for a CLI, which ships as release assets instead.
	deployable bool
	hardened   bool

	// generate rebuilds what the service's own sources derive.
	generate [][]string

	// lock pins declared dependencies; deterministic, so regen runs it.
	lock [][]string

	// upgrade moves dependencies forward; init only.
	upgrade [][]string

	// files maps a template file to the path it is written to in the
	// generated service. A .tmpl suffix means it is rendered with Params;
	// anything else is copied verbatim.
	files map[string]string
}

func (e embedded) Name() string           { return e.name }
func (e embedded) SupportsHardened() bool { return e.hardened }
func (e embedded) Generate() [][]string   { return e.generate }
func (e embedded) Lock() [][]string       { return e.lock }
func (e embedded) Upgrade() [][]string    { return e.upgrade }

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

// Artifacts assembles everything this runtime contributes. A non-
// deployable runtime simply has no Dockerfile, so callers never ask "what
// kind is this?" - they ask what they were given.
func (e embedded) Artifacts(p Params) Artifacts {
	a := Artifacts{Files: e.renderFiles(p), Deployable: e.deployable}
	if e.deployable {
		a.Dockerfile = e.render("Dockerfile", p)
	}
	// The workflow embeds the runtime's build steps, so render those first.
	p.BuildSteps = e.buildSteps(p)
	a.Workflow = e.render("workflow.yaml", p)
	return a
}

// buildSteps are the CI steps that produce what the Dockerfile or release
// expects, as GitHub Actions YAML list items.
func (e embedded) buildSteps(p Params) string {
	return strings.TrimRight(e.render("steps.yaml", p), "\n") + "\n"
}

func (e embedded) renderFiles(p Params) []File {
	// Sorted, because Go randomizes map iteration order: ranging directly
	// made two identical `init` runs print their "created:" lists in
	// different orders, so a re-run looked like a change. It would also
	// hide any ordering dependence that ever crept into the write loop
	// behind an intermittent failure.
	srcs := make([]string, 0, len(e.files))
	for src := range e.files {
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)

	out := make([]File, 0, len(e.files))
	for _, src := range srcs {
		dst := e.files[src]
		body := e.read(src)
		if strings.HasSuffix(src, ".tmpl") {
			body = e.render(src, p)
		}
		out = append(out, File{Path: dst, Body: body})
	}
	return out
}
