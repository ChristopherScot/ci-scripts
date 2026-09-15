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
	Port   int
}

// Runtime describes how to build and containerise one language.
type Runtime interface {
	// Name is the value used in config.yaml's `runtime:` field.
	Name() string

	// Files are the source files a new service starts with.
	Files(p Params) []File

	// Dockerfile is the container build for this language. It must produce
	// an image that runs as uid 65532, or SupportsHardened must be false -
	// otherwise the pod cannot exec its binary under the default
	// securityContext, which fails with "permission denied" and no logs.
	Dockerfile(p Params) string

	// BuildSteps are the CI steps that produce the build artifacts the
	// Dockerfile expects, in GitHub Actions YAML (list items, 6-space
	// indented to sit under `steps:`).
	BuildSteps(p Params) string

	// SupportsHardened reports whether images from this runtime can run
	// non-root with a read-only root filesystem.
	SupportsHardened() bool
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
