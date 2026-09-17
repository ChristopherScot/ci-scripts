package runtime

import (
	"slices"
	"strings"
	"testing"
)

func testParams() Params {
	return Params{Name: "svc", Module: "github.com/o/svc", Owner: "o", Port: 3000,
		Image: "ghcr.io/o/svc"}
}

// Templates live in files and are only exercised at generate time, so a
// typo would otherwise surface as a panic in front of a user creating a
// service. Render every registered runtime here instead.
func TestEveryRuntimeRendersCleanly(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			r, err := Get(name)
			if err != nil {
				t.Fatalf("Get(%q) = %v", name, err)
			}
			a := r.Artifacts(testParams())

			if len(a.Files) == 0 {
				t.Fatal("Artifacts returned no files")
			}
			for _, f := range a.Files {
				if f.Path == "" {
					t.Error("file with empty path")
				}
				if strings.Contains(f.Body, "{{") {
					t.Errorf("%s still contains an unrendered action:\n%s", f.Path, f.Body)
				}
			}
			if strings.TrimSpace(a.Workflow) == "" {
				t.Error("Workflow is empty")
			}
			// A GitHub Actions ${{ }} expression must survive templating.
			if !strings.Contains(a.Workflow, "${{") {
				t.Error("workflow has no ${{ }} expressions; templating likely ate them")
			}
			if strings.Contains(a.Workflow, "{{ .") {
				t.Errorf("workflow contains an unrendered action:\n%s", a.Workflow)
			}

			if a.Deployable {
				if !strings.Contains(a.Dockerfile, "EXPOSE 3000") {
					t.Errorf("Dockerfile did not substitute Port:\n%s", a.Dockerfile)
				}
			} else if a.Dockerfile != "" {
				t.Errorf("non-deployable runtime produced a Dockerfile:\n%s", a.Dockerfile)
			}
		})
	}
}

// A hardened runtime's image must run as the uid the manifests expect, or
// the pod cannot exec its binary and fails with no logs.
func TestHardenedRuntimesRunAsNonroot(t *testing.T) {
	for _, name := range Names() {
		r, _ := Get(name)
		a := r.Artifacts(testParams())
		if !r.SupportsHardened() || !a.Deployable {
			continue
		}
		if !strings.Contains(a.Dockerfile, "65532") {
			t.Errorf("runtime %q claims hardened support but its Dockerfile does not use uid 65532", name)
		}
	}
}

// A CLI ships a self-update command, which is the reason the shape exists;
// without it users have no way to get a new version.
func TestCLIRuntimesShipSelfUpdate(t *testing.T) {
	p := Params{Name: "mytool", Module: "github.com/o/mytool", Owner: "o", Port: 3000}
	for _, name := range Names() {
		r, _ := Get(name)
		a := r.Artifacts(p)
		if a.Deployable {
			continue
		}
		var hasUpdate, hasVersion bool
		for _, f := range a.Files {
			switch f.Path {
			case "update.go":
				hasUpdate = true
				if !strings.Contains(f.Body, `repoOwner = "o"`) {
					t.Errorf("%s: update.go did not substitute Owner", name)
				}
				if !strings.Contains(f.Body, `repoName  = "mytool"`) {
					t.Errorf("%s: update.go did not substitute Name", name)
				}
			case "VERSION":
				hasVersion = true
			}
		}
		if !hasUpdate {
			t.Errorf("CLI runtime %q ships no update.go", name)
		}
		if !hasVersion {
			t.Errorf("CLI runtime %q ships no VERSION file", name)
		}
		// The release workflow must name assets the way update looks for
		// them, or self-update fails for everyone.
		if !strings.Contains(a.Workflow, "mytool_${GOOS}_${GOARCH}.tar.gz") {
			t.Errorf("CLI runtime %q: release asset name does not match what update expects", name)
		}
		if !strings.Contains(a.Workflow, "main.Version=") {
			t.Errorf("CLI runtime %q: release does not inject Version, so the binary reports dev and refuses to update", name)
		}
	}
}

// A monorepo service must only rebuild when its own directory changes.
func TestMonorepoWorkflowIsPathFiltered(t *testing.T) {
	r, _ := Get("go-service")
	p := testParams()
	p.PathFilter = "services/svc"
	a := r.Artifacts(p)

	if !strings.Contains(a.Workflow, "services/svc/**") {
		t.Error("monorepo workflow has no path filter; every push would rebuild every service")
	}
	if !strings.Contains(a.Workflow, "working-directory: services/svc") {
		t.Error("monorepo workflow does not set working-directory")
	}
}

func TestGetUnknownRuntimeListsAvailable(t *testing.T) {
	_, err := Get("cobol")
	if err == nil {
		t.Fatal("Get(cobol) succeeded")
	}
	if !strings.Contains(err.Error(), "go-service") {
		t.Errorf("error should list available runtimes; got %v", err)
	}
}

// Go randomizes map iteration order, so ranging over the file map made
// every scaffold list its files in a different order.
func TestArtifactsAreDeterministic(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		p := Params{Name: "svc", Module: "example.com/svc", Port: 3000}

		var first []string
		for i := 0; i < 25; i++ {
			var paths []string
			for _, f := range r.Artifacts(p).Files {
				paths = append(paths, f.Path)
			}
			if i == 0 {
				first = paths
				continue
			}
			if !slices.Equal(paths, first) {
				t.Fatalf("%s: file order changed between runs:\n  run 0: %v\n  run %d: %v",
					name, first, i, paths)
			}
		}
	}
}

// Dependency resolution used to be inferred from a "go-"/"node-" prefix on
// the runtime name, in a switch at the call site. A runtime whose name did
// not start with one of those silently got no lockfile - and a Go service
// without go.sum does not build at all. Every runtime that generates a
// manifest of dependencies must declare how to lock it.
func TestRuntimesDeclareDependencyResolution(t *testing.T) {
	manifests := map[string]string{
		"go.mod":       "go",
		"package.json": "npm",
	}

	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		files := r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000}).Files

		for _, f := range files {
			tool, needsLock := manifests[f.Path]
			if !needsLock {
				continue
			}
			argv := r.ResolveDeps()
			if len(argv) == 0 {
				t.Errorf("%s generates %s but declares no ResolveDeps; its scaffold will not build",
					name, f.Path)
				continue
			}
			if argv[0] != tool {
				t.Errorf("%s generates %s but resolves with %q", name, f.Path, argv[0])
			}
		}
	}
}

// A log line that does not say which service emitted it is worth little
// outside its Loki label context - in a ticket, an alert, or a terminal.
// A deployable runtime must stamp its identity onto the default logger.
func TestDeployableRuntimesStampLogContext(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		a := r.Artifacts(Params{
			Name: "svc", Team: "platform",
			Module: "example.com/svc", Port: 3000,
		})
		if !a.Deployable {
			continue // a CLI writes to a terminal, not an aggregator
		}

		var entry string
		for _, f := range a.Files {
			if f.Path == "main.go" || f.Path == "server.js" {
				entry = f.Body
			}
		}
		if entry == "" {
			t.Errorf("%s: no entrypoint file among its artifacts", name)
			continue
		}
		for _, want := range []string{"svc", "platform"} {
			if !strings.Contains(entry, want) {
				t.Errorf("%s: entrypoint does not carry %q in its log context", name, want)
			}
		}
	}
}
