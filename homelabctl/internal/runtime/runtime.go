// Package runtime is the extension point for languages.
//
// A Runtime knows the things that differ per language - how to build, what
// base image to run on, what source files a new service starts with - and
// nothing else. Everything downstream (Kubernetes manifests, the Argo
// Application, the image-updater annotations, CI) is identical regardless
// of language and lives in the render package.
//
// Adding a language is normally NO Go code: a directory under templates/
// holding its Dockerfile, steps.yaml, workflow.yaml and starter files,
// plus one embedded{...} entry in registered.go. All three shipped
// runtimes are exactly that.
//
// The Runtime interface exists for the runtime that eventually needs more
// than data - one whose build steps depend on inspecting a lockfile, say.
// That one can be its own type and Register itself, instead of becoming a
// special case inside embedded. Note embedded's fields are unexported and
// this package is internal, so implementing Runtime means adding a type
// HERE, not from another module.
package runtime

import (
	"fmt"
	"sort"
	"strings"
)

// InitialSpecVersion is where a new service's API starts. It reaches both
// the spec's info.version and the client's ClientVersion, which must not
// drift apart.
const InitialSpecVersion = "0.1.0"

// File is a file a scaffolded service starts with.
type File struct {
	Path string
	Body string
}

// Params are the config values a runtime needs. Deliberately narrow: a
// runtime should not reach into the whole config, so that adding fields
// there does not ripple into every language.
type Params struct {
	Name string
	Team string // owning team, stamped onto every log line

	// Spec means this service generates its API from openapi.yml. False
	// hand-writes server.go instead: no spec, no generated api/ package,
	// no clients, and nothing for CI to check as stale.
	//
	// Always set explicitly by the caller from config.Spec, so unlike
	// that field there is no "absent" case to represent here.
	//
	// The seam is one file. main.go, the Dockerfile, CI and every
	// manifest are the same either way - which is what makes this a
	// field rather than a second runtime.
	Spec bool

	// SpecVersion is the API version from the spec's info.version. The
	// generated client reports it in X-Client-Version, so a server can
	// see which client versions still call it. One version for the API
	// and its clients, rather than a second scheme to keep in sync.
	SpecVersion string
	Module      string // import path / package name
	Owner       string // GitHub owner, for a CLI's self-update endpoint
	Port        int

	// Image is the registry path CI publishes to.
	Image string

	// PathFilter is the service's directory within a monorepo, empty for a
	// dedicated repo. It drives CI path filters and the build context, so
	// the monorepo layout is decided once rather than at four call sites.
	PathFilter string

	// BuildSteps is filled in by the runtime before rendering its workflow.
	BuildSteps string
}

// BinaryName is what `go build` with no -o produces: the last element of
// the module path, which is not always the service name. The repo
// go-shlink-redirector holds the service shlink-redirector, so a
// .gitignore keyed on the name would miss the binary entirely.
func (p Params) BinaryName() string {
	if i := strings.LastIndex(p.Module, "/"); i >= 0 {
		return p.Module[i+1:]
	}
	if p.Module != "" {
		return p.Module
	}
	return p.Name
}

// Context is the Docker build context: the service directory in a
// monorepo, the repo root otherwise.
func (p Params) Context() string {
	if p.PathFilter != "" {
		return p.PathFilter
	}
	return "."
}

// Artifacts is everything a runtime contributes to a new repo.
//
// Callers branch on the DATA here, not on a kind tag: a CLI simply has no
// Dockerfile and is not Deployable, so "what does this produce" is answered
// once, by the runtime, instead of at every call site. Adding a shape that
// produces some other mix needs no changes outside the runtime.
type Artifacts struct {
	Files []File

	// Dockerfile is empty for anything not containerised.
	Dockerfile string

	// Workflow is the CI that builds this runtime's artifacts.
	Workflow string

	// Deployable says whether Kubernetes manifests and an Argo Application
	// apply. False for a CLI, which ships as release assets.
	Deployable bool
}

// Runtime describes how to build one kind of thing in one language.
//
// Every runtime that ships today is an embedded{} - a data declaration
// over a template directory. The interface is the seam for the first one
// that is not.
type Runtime interface {
	// Name is the value used in config.yaml's `runtime:` field, e.g.
	// go-service, node-service, go-cli.
	Name() string

	// SpecFiles are the paths Artifacts writes only when Params.Spec is
	// set - the spec, its generator config and everything generated from
	// it. Named here so `check` and `regen` can ask what a specless
	// service legitimately lacks rather than each keeping its own list.
	SpecFiles() []string

	// Artifacts are everything a new repo of this runtime starts with.
	// A containerised runtime must produce an image that runs as uid 65532
	// or set SupportsHardened false - otherwise the pod cannot exec its
	// binary under the default securityContext, which fails with
	// "permission denied" and no logs.
	Artifacts(p Params) Artifacts

	// SupportsHardened reports whether images from this runtime can run
	// non-root with a read-only root filesystem.
	SupportsHardened() bool

	// Generate rebuilds what the service's own sources derive - code
	// generated from its OpenAPI spec, say. Run in order, in the service
	// directory. Nil if a runtime generates nothing, and nil for a
	// service built without a spec: there is no source to derive from.
	//
	// It takes Params rather than a bare bool because a bool at a call
	// site reads plausibly in both directions: regen passed `false`
	// meaning "not disabled" and so generated nothing at all, while
	// still reporting success.
	Generate(p Params) [][]string

	// Lock resolves declared dependencies into a lockfile: `go mod tidy`,
	// `npm install --package-lock-only`. It reads what the manifest
	// already says and pins it, so running it twice changes nothing.
	//
	// It belongs here rather than in a switch at the call site because it
	// is the one build concern that cannot be expressed as a template.
	// init used to infer it from a "go-"/"node-" prefix on the runtime
	// name, so a language whose name did not start with one of those got
	// no lockfile and no warning - and a Go service without go.sum does
	// not build at all.
	Lock() [][]string

	// Upgrade moves dependencies to newer versions: `go get -u`. Unlike
	// Generate and Lock, its output depends on what the world has
	// published, so the same inputs give different results on different
	// days.
	//
	// Run once, by init, so a new service starts on current transitive
	// versions rather than the minimums its direct dependency declares -
	// typically months old. Never by regen, which CI runs and diffs: an
	// upgrade there would be a red build nobody caused.
	Upgrade() [][]string
}

var registry = map[string]Runtime{}

// Register makes a runtime available to `runtime:` in config.yaml. Panics
// on a duplicate name, which can only happen at init time and is a
// programming error.
func Register(r Runtime) {
	if _, dup := registry[r.Name()]; dup {
		panic("runtime registered twice: " + r.Name())
	}
	registry[r.Name()] = r
}

// Get returns a runtime by name, listing what is available when it is not
// found - the error a typo in config.yaml produces.
func Get(name string) (Runtime, error) {
	r, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown runtime %q; available: %v", name, Names())
	}
	return r, nil
}

// Names lists registered runtimes, sorted for stable output.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
