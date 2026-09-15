// Package runtime is the extension point for languages.
//
// A Runtime knows the things that differ per language - how to build, what
// base image to run on, what source files a new service starts with - and
// nothing else. Everything downstream (Kubernetes manifests, the Argo
// Application, the image-updater annotations, CI) is identical regardless
// of language and lives in the render package.
//
// Adding Node means implementing this interface and calling Register in an
// init function. No existing code changes.
package runtime

import (
	"fmt"
	"sort"
)

// File is a file a scaffolded service starts with.
type File struct {
	Path string
	Body string
	Mode uint32 // 0 means 0644
}

// Params are the config values a runtime needs. Deliberately narrow: a
// runtime should not reach into the whole config, so that adding fields
// there does not ripple into every language.
type Params struct {
	Name   string
	Module string // import path / package name
	Owner  string // GitHub owner, for a CLI's self-update endpoint
	Port   int

	// Image is the registry path CI publishes to.
	Image string

	// PathFilter is the service's directory within a monorepo, empty for a
	// dedicated repo. It drives CI path filters and the build context, so
	// the monorepo layout is decided once rather than at four call sites.
	PathFilter string

	// BuildSteps is filled in by the runtime before rendering its workflow.
	BuildSteps string
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
type Runtime interface {
	// Name is the value used in config.yaml's `runtime:` field, e.g.
	// go-service, node-service, go-cli.
	Name() string

	// Artifacts are everything a new repo of this runtime starts with.
	// A containerised runtime must produce an image that runs as uid 65532
	// or set SupportsHardened false - otherwise the pod cannot exec its
	// binary under the default securityContext, which fails with
	// "permission denied" and no logs.
	Artifacts(p Params) Artifacts

	// SupportsHardened reports whether images from this runtime can run
	// non-root with a read-only root filesystem.
	SupportsHardened() bool

	// Deployable reports whether this runtime produces Kubernetes
	// manifests and an Argo Application. False for a CLI.
	Deployable() bool
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
