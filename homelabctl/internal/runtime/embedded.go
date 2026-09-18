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

// tmpl is one template and what becomes of it.
type tmpl struct {
	// dst is the path it is written to in the generated service. A
	// .tmpl suffix on the template name means it is rendered with
	// Params; anything else is copied verbatim.
	dst string

	// specOnly marks a file that exists only for a spec-first service:
	// openapi.yml, the generator config, and everything derived from
	// them. A service with Spec false drops these.
	specOnly bool

	// regen marks a file `homelabctl regen` rewrites from its template.
	//
	// Deliberately separate from specOnly, which means "exists only for
	// a spec-first service" and covers the spec INPUTS too: openapi.yml
	// is the author's source of truth and ogen.yml is theirs to tune.
	// Rewriting those from the template destroys the API - regen did
	// exactly that once, replacing a five-endpoint spec with the
	// two-endpoint scaffold.
	//
	// Only generated OUTPUT belongs here: the clients, which exist to be
	// overwritten and carry a "do not edit" header saying so.
	regen bool

	// plain replaces this template when the service has no spec. The
	// handler, its tests and the workflow differ rather than disappear.
	plain string
}

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

	// files maps a template file to what it becomes. One entry per
	// template, carrying everything known about it.
	//
	// This was three maps - files, specFiles and specSwaps - keyed on
	// the same template names and aligned by hand. A typo in one of the
	// parallel keys was silent: a name that was not in files resolved
	// to "", so a spec-only file was written to an empty path.
	// One map cannot disagree with itself.
	files map[string]tmpl
}

func (e embedded) Name() string           { return e.name }
func (e embedded) SupportsHardened() bool { return e.hardened }

// Generate is nil without a spec: every command here derives code from
// openapi.yml, so running them would fail on a missing file rather than
// produce nothing.
func (e embedded) Generate(p Params) [][]string {
	if !p.Spec {
		return nil
	}
	return e.generate
}

// SpecFiles are the paths `homelabctl regen` rewrites from their
// templates: generated client code, and nothing the author owns.
//
// Without this, regen ran the ogen commands and stopped - so a fix to a
// client TEMPLATE reached new services and never existing ones, and the
// only way to pick it up was knowing that `init --overwrite` also
// regenerates. A command named regen should regenerate what it
// generated.
func (e embedded) SpecFiles() []string {
	var out []string
	for _, t := range e.files {
		if t.regen && t.dst != "" {
			out = append(out, t.dst)
		}
	}
	sort.Strings(out)
	return out
}

func (e embedded) Lock() [][]string    { return e.lock }
func (e embedded) Upgrade() [][]string { return e.upgrade }

// swap returns the template to use for src: the specless variant when
// this service has no spec, the original otherwise.
func (e embedded) swap(src string, p Params) string {
	if !p.Spec {
		if alt := e.files[src].plain; alt != "" {
			return alt
		}
	}
	return src
}

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
	// Through the same swap table as every other file. It does not go
	// through renderFiles - Artifacts carries it separately - so the
	// lookup has to be repeated here, or a specless service gets CI that
	// regenerates from a spec it does not have.
	a.Workflow = e.render(e.swap("workflow.yaml", p), p)
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
	for src, t := range e.files {
		// An empty dst is a template something else writes - the
		// workflow, whose path setupLocal decides. It is in this map so
		// its specless variant lives with every other file's.
		if t.dst == "" {
			continue
		}
		// Everything derived from a spec, when there is no spec.
		if !p.Spec && e.files[src].specOnly {
			continue
		}
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)

	out := make([]File, 0, len(e.files))
	for _, src := range srcs {
		dst := e.files[src].dst
		src = e.swap(src, p)
		body := e.read(src)
		if strings.HasSuffix(src, ".tmpl") {
			body = e.render(src, p)
		}
		out = append(out, File{Path: dst, Body: body})
	}
	return out
}
