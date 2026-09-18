package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
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
				name: "svc", runtimeID: name,
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
				// deploy/<name>/, the same layout `render --out deploy`
				// writes, so the copy into the homelab repo is `cp -r`.
				want = append(want, "Dockerfile", "config.yaml",
					filepath.Join("deploy", "svc", "kustomization.yaml"),
					filepath.Join("deploy", "svc", "deployment.yaml"))
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

	o := initOpts{name: "svc", runtimeID: "go-service",
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
		owner  string
		parent string
		cfg    config.Config
		want   string
		reason string
	}{
		{
			name:   "its own repo, named after the service",
			owner:  "acme",
			cfg:    config.Config{Name: "widget"},
			want:   "github.com/acme/widget",
			reason: "the ordinary case",
		},
		{
			name:   "a monorepo service",
			owner:  "acme",
			parent: "platform",
			cfg:    config.Config{Name: "widget"},
			want:   "github.com/acme/platform/services/widget",
			reason: "must match the directory, or the module is unfetchable",
		},
		{
			name:   "config overrides",
			owner:  "acme",
			cfg:    config.Config{Name: "widget", Module: "github.com/acme/go-widget"},
			want:   "github.com/acme/go-widget",
			reason: "the repo is not named after the service",
		},
		{
			name:   "config overrides a monorepo too",
			owner:  "acme",
			parent: "platform",
			cfg:    config.Config{Name: "widget", Module: "example.com/custom/widget"},
			want:   "example.com/custom/widget",
			reason: "an explicit path always wins",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := modulePath(&tc.cfg, tc.owner, tc.parent); got != tc.want {
				t.Errorf("modulePath() = %q, want %q (%s)", got, tc.want, tc.reason)
			}
		})
	}
}

// Scaffolded files are handed to the service and never rewritten, which
// is what makes them editable - and also means a template fix cannot
// reach a service that already exists. --force is how one is pulled in,
// and it must touch only what it names.
func TestOverwriteRewritesOnlyTheNamedFiles(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "main.go")
	keep := filepath.Join(dir, "server.go")
	for _, p := range []string{mine, keep} {
		if err := os.WriteFile(p, []byte("// mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	o := initOpts{
		name: "svc", runtimeID: "go-service",
		owner: "o", localOnly: true, yes: true, skipTidy: true,
		overwrite: map[string]bool{"main.go": true},
	}
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = o.name, defaultTeam, o.runtimeID
	c.Image = config.Image{Repository: "ghcr.io/o/svc"}
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	r, err := runtime.Get(o.runtimeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := setupLocal(o, &c, r, dir); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(mine)
	if string(got) == "// mine\n" {
		t.Error("--overwrite main.go did not rewrite it")
	}
	untouched, _ := os.ReadFile(keep)
	if string(untouched) != "// mine\n" {
		t.Error("--overwrite main.go rewrote server.go, which it did not name")
	}
}

// Without --force nothing existing is touched, which is the default a
// re-run depends on.
func TestWithoutOverwriteExistingFilesSurvive(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mine, []byte("// mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	o := initOpts{
		name: "svc", runtimeID: "go-service",
		owner: "o", localOnly: true, yes: true, skipTidy: true,
	}
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = o.name, defaultTeam, o.runtimeID
	c.Image = config.Image{Repository: "ghcr.io/o/svc"}
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	r, _ := runtime.Get(o.runtimeID)
	if err := setupLocal(o, &c, r, dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(mine); string(got) != "// mine\n" {
		t.Error("a re-run without --overwrite clobbered an existing file")
	}
}

// init and `render --out deploy` must agree on where manifests go.
// init used to write them flat, so the first render moved every file -
// and the next-steps text had to describe a rename ("copy deploy/*.yaml
// into the homelab repo AS <name>/") instead of a copy.
func TestInitWritesManifestsWhereRenderDoes(t *testing.T) {
	dir := t.TempDir()
	o := initOpts{
		name: "svc", runtimeID: "go-service",
		owner: "o", localOnly: true, yes: true, skipTidy: true,
	}
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = o.name, defaultTeam, o.runtimeID
	c.Image = config.Image{Repository: "ghcr.io/o/svc"}
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	r, _ := runtime.Get(o.runtimeID)
	if err := setupLocal(o, &c, r, dir); err != nil {
		t.Fatal(err)
	}

	// Nested under the service name, which is the directory the app
	// occupies in the homelab repo - so the copy is `cp -r`.
	if _, err := os.Stat(filepath.Join(dir, "deploy", "svc", "deployment.yaml")); err != nil {
		t.Errorf("deploy/svc/deployment.yaml missing: %v", err)
	}
	// Not flat beside it.
	if _, err := os.Stat(filepath.Join(dir, "deploy", "deployment.yaml")); err == nil {
		t.Error("a manifest was written flat into deploy/, where render would not put it")
	}
	// The Argo Application stays at the top: it belongs to
	// app-of-apps/, not to the service's own directory.
	if _, err := os.Stat(filepath.Join(dir, "deploy", "_argocd-application.yaml")); err != nil {
		t.Errorf("deploy/_argocd-application.yaml missing: %v", err)
	}
}

// --overwrite may rewrite anything init writes, including files another
// command also maintains: init renders manifests with render.All and
// generates api/ with the runtime's Generate, so rewriting either
// re-runs the code that owns it rather than reimplementing it.
// Ownership is the module's, not the command's.
//
// What it refuses is content a template cannot restate.
func TestOverwriteRefusesOnlyWhatATemplateCannotRestate(t *testing.T) {
	run := func(arg string) error {
		dir := t.TempDir()
		cmd := initCmd()
		cmd.SetArgs([]string{"svc", "--local-only", "--yes", "--overwrite", arg})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		wd, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(wd)
		return cmd.Execute()
	}

	for _, arg := range []string{
		"config.yaml",  // the source everything derives from
		"openapi.yml",  // the other source
		"go.mod",       // the toolchain's, maintained by `go mod tidy`
		"server.go",    // the seam a service replaces on purpose
		"main_test.go", // its tests, which go with it
		"mian.go",      // a typo
	} {
		if err := run(arg); err == nil {
			t.Errorf("--overwrite %s was accepted", arg)
		} else if !strings.Contains(err.Error(), "not scaffolding") {
			t.Errorf("--overwrite %s: %v", arg, err)
		}
	}

	// Accepted, because init writes these with the same functions the
	// commands that own them use.
	for _, arg := range []string{"deploy/svc/deployment.yaml", "api/client.go", "main.go"} {
		if err := run(arg); err != nil && strings.Contains(err.Error(), "not scaffolding") {
			t.Errorf("--overwrite %s was refused; init writes it with the owning module's code", arg)
		}
	}
}

// The flag's argument is cleaned to the form put looks up. Storing the
// raw string meant `--overwrite ./main.go` passed validation and then
// matched nothing: the file was reported as "kept", having been asked
// for explicitly.
func TestOverwriteNormalisesItsArgument(t *testing.T) {
	for _, spelling := range []string{"main.go", "./main.go"} {
		dir := t.TempDir()
		mine := filepath.Join(dir, "main.go")
		if err := os.WriteFile(mine, []byte("// mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		o := initOpts{
			name: "svc", runtimeID: "go-service",
			owner: "o", localOnly: true, yes: true, skipTidy: true,
			overwrite: map[string]bool{filepath.ToSlash(filepath.Clean(spelling)): true},
		}
		c := config.Defaults()
		c.Name, c.Team, c.Runtime = o.name, defaultTeam, o.runtimeID
		c.Image = config.Image{Repository: "ghcr.io/o/svc"}
		if err := c.Complete(); err != nil {
			t.Fatal(err)
		}
		r, _ := runtime.Get(o.runtimeID)
		if err := setupLocal(o, &c, r, dir); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(mine); string(got) == "// mine\n" {
			t.Errorf("--overwrite %s did not rewrite main.go", spelling)
		}
	}
}
