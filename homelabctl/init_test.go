package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/runtime"
)

// Every runtime must scaffold end to end. The unit tests call Artifacts()
// directly, which missed a guard in runInit that rejected every go-cli
// scaffold outright - the generation path needs its own coverage.
func TestInitGeneratesEveryRuntime(t *testing.T) {
	for _, name := range runtime.Names() {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			wd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(wd) })

			o := initOpts{
				name: "svc", runtimeID: name, team: "t", port: 3000,
				owner: "o", localOnly: true, yes: true,
				// Dependency resolution needs a network and is covered by
				// the scaffold's own CI; skip it here.
				skipTidy: true,
			}
			if err := runInit(o); err != nil {
				t.Fatalf("runInit(%s) = %v", name, err)
			}

			r, err := runtime.Get(name)
			if err != nil {
				t.Fatal(err)
			}
			// Every runtime declares its own files; assert those rather
			// than assuming a language.
			a := r.Artifacts(runtime.Params{Name: "svc", Module: "m", Owner: "o", Port: 3000})
			var want []string
			for _, f := range a.Files {
				want = append(want, f.Path)
			}
			if a.Deployable {
				want = append(want, "Dockerfile", "config.yaml",
					filepath.Join("deploy", "kustomization.yaml"),
					filepath.Join("deploy", "deployment.yaml"))
			}
			for _, f := range want {
				if _, err := os.Stat(f); err != nil {
					t.Errorf("%s: expected %s to exist: %v", name, f, err)
				}
			}
		})
	}
}

// A scaffolded service must pass the tool's own deploy check, or the very
// first CI run of every new service fails.
func TestScaffoldPassesItsOwnCheck(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	o := initOpts{name: "svc", runtimeID: "go-service", team: "t", port: 3000,
		owner: "o", localOnly: true, yes: true, skipTidy: true}
	if err := runInit(o); err != nil {
		t.Fatalf("runInit = %v", err)
	}
	if err := runCheck("deploy"); err != nil {
		t.Errorf("a fresh scaffold fails its own check: %v", err)
	}
}

// Go requires a module's path to match where it is fetched from, so a
// wrong value here is a module nobody can `go get` - not a style choice.
func TestModulePath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   initOpts
		cfg    config.Config
		want   string
		reason string
	}{
		{
			name:   "its own repo, named after the service",
			opts:   initOpts{owner: "acme", name: "widget"},
			want:   "github.com/acme/widget",
			reason: "the ordinary case",
		},
		{
			name:   "a monorepo service",
			opts:   initOpts{owner: "acme", name: "widget", parentRepo: "platform"},
			want:   "github.com/acme/platform/services/widget",
			reason: "must match the directory, or the module is unfetchable",
		},
		{
			name:   "config overrides",
			opts:   initOpts{owner: "acme", name: "widget"},
			cfg:    config.Config{Module: "github.com/acme/go-widget"},
			want:   "github.com/acme/go-widget",
			reason: "the repo is not named after the service",
		},
		{
			name:   "config overrides a monorepo too",
			opts:   initOpts{owner: "acme", name: "widget", parentRepo: "platform"},
			cfg:    config.Config{Module: "example.com/custom/widget"},
			want:   "example.com/custom/widget",
			reason: "an explicit path always wins",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modulePath(tc.opts, &tc.cfg); got != tc.want {
				t.Errorf("modulePath() = %q, want %q (%s)", got, tc.want, tc.reason)
			}
		})
	}
}
