package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
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
	// argocd.json sits WITH the manifests, unlike the per-service Argo
	// Application it replaced: the ApplicationSet's files generator globs
	// */argocd.json in the homelab repo, so it has to travel with the
	// directory rather than sit above it.
	if _, err := os.Stat(filepath.Join(dir, "deploy", "svc", render.AppEntryFile)); err != nil {
		t.Errorf("deploy/svc/%s missing: %v", render.AppEntryFile, err)
	}
	// And the file it replaced is gone. Leaving it behind would mean two
	// things claiming to define the Application, with the stale one still
	// being applied by app-of-apps.
	if _, err := os.Stat(filepath.Join(dir, "deploy", "_argocd-application.yaml")); err == nil {
		t.Error("init still writes _argocd-application.yaml, which the ApplicationSet replaced")
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
	// This drives the real cobra command rather than an initOpts, so it
	// takes the owner from the environment as any non-interactive caller
	// does. Without it the test depends on whether `gh` happens to be
	// authenticated on the machine running it - which is true on a
	// developer's laptop and false on a CI runner.
	t.Setenv(ownerEnv, "o")

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

// A registry path must be lowercase; a GitHub owner need not be. While
// --owner defaulted to a lowercase literal these were the same string,
// so nothing noticed they are different rules. Reading the owner from gh
// makes "ChristopherScot" real, and ghcr.io/ChristopherScot/svc fails at
// docker push in CI - after init, render and the whole build succeeded.
func TestImagePathIsLowercasedIndependentlyOfTheOwner(t *testing.T) {
	for _, owner := range []string{"ChristopherScot", "christopherscot", "MixedCase"} {
		c, err := buildConfig(initOpts{name: "svc", owner: owner, runtimeID: "go-service"})
		if err != nil {
			t.Fatalf("owner %q: %v", owner, err)
		}
		if got := c.Image.Repository; got != strings.ToLower(got) {
			t.Errorf("owner %q produced image %q, which a registry rejects", owner, got)
		}
	}
	// And the monorepo path, which builds the string separately.
	c, err := buildConfig(initOpts{name: "svc", owner: "ChristopherScot", parentRepo: "MyRepo", runtimeID: "go-service"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Image.Repository; got != strings.ToLower(got) {
		t.Errorf("monorepo image %q is not lowercase", got)
	}
}

// The owner decides the GitHub repo, the image path and the module path,
// so it is asked for rather than defaulted. These are the paths that must
// not reach a prompt: an explicit flag, an exported env var, and a
// non-interactive run, which would otherwise hang a CI job forever
// instead of failing it.
func TestOwnerResolutionOrder(t *testing.T) {
	t.Setenv(ownerEnv, "from-env")

	if got, err := resolveOwner(initOpts{owner: "from-flag"}); err != nil || got != "from-flag" {
		t.Errorf("flag should win: got %q, %v", got, err)
	}
	if got, err := resolveOwner(initOpts{}); err != nil || got != "from-env" {
		t.Errorf("env should be used when no flag: got %q, %v", got, err)
	}

	// With neither, a non-interactive run must not block on stdin.
	t.Setenv(ownerEnv, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		// It may succeed via the gh suggestion or fail for want of one;
		// what matters is that it returns rather than waiting to be typed at.
		_, _ = resolveOwner(initOpts{yes: true})
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("resolveOwner blocked on input in a non-interactive run")
	}
}

// A trusted publisher is configured against the REPOSITORY, which is the
// parent in a monorepo and the service's own repo otherwise. Getting
// this wrong prints a command naming a repository that does not exist,
// and the failure is an opaque npm error rather than anything about
// repositories.
func TestPublishRepoNamesTheRepositoryNotTheService(t *testing.T) {
	if got := publishRepo(initOpts{name: "widget", parentRepo: "shop"}); got != "shop" {
		t.Errorf("monorepo: publishRepo = %q, want shop", got)
	}
	if got := publishRepo(initOpts{name: "gadget"}); got != "gadget" {
		t.Errorf("standalone: publishRepo = %q, want gadget", got)
	}
}

// The publish workflow is at the repository root in both layouts,
// because one file covers every client in the repo and npm fixes the
// filename at setup.
func TestPublishWorkflowPathIsTheRepoRoot(t *testing.T) {
	if got := publishWorkflowPath(initOpts{name: "widget", parentRepo: "shop"}); got != "../../.github/workflows/publish.yaml" {
		t.Errorf("monorepo: %q", got)
	}
	if got := publishWorkflowPath(initOpts{name: "gadget"}); got != ".github/workflows/publish.yaml" {
		t.Errorf("standalone: %q", got)
	}
}

// npm's trusted publishing compares every field literally, and the
// organization is the one that bites: GitHub's OIDC token carries the
// canonical casing (ChristopherScot), so a configuration created with a
// lowercased owner never matches and every publish 404s with a message
// about the package rather than the owner.
//
// The npm SCOPE is the opposite - npm rejects uppercase there - so the
// two cannot simply share a value, which is exactly how they drift.
func TestTrustCommandKeepsRepoCasingAndLowercasesOnlyTheScope(t *testing.T) {
	var out bytes.Buffer
	printTrustCommand(&out, initOpts{name: "widget", owner: "ChristopherScot", parentRepo: "shop"}, "widget")

	got := out.String()
	if !strings.Contains(got, "--repo ChristopherScot/shop") {
		t.Errorf("repo lost GitHub's casing, which npm compares literally:\n%s", got)
	}
	if !strings.Contains(got, "@christopherscot/widget-client") {
		t.Errorf("npm scope is not lowercased, which npm rejects:\n%s", got)
	}
}
