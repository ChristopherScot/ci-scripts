package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/runtime"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// init is idempotent: every step checks for what it would create and skips
// it if present, so a run that fails partway - no network, a rate limit, a
// wrong flag - can simply be run again rather than needing manual cleanup.
// defaultTeam stamps every log line until config.yaml says otherwise.
const ownerEnv = "HOMELAB_OWNER"

const defaultTeam = "me-myself-and-i"

type initOpts struct {
	name string

	// runtimeID picks the template set. Not derivable from a config that
	// does not exist yet, and it decides which files are written, so it
	// stays a flag - but `runtime:` in config.yaml wins on a re-run.
	runtimeID  string
	owner      string
	parentRepo string // create the service inside this existing repo
	private    bool
	noSpec     bool // hand-write server.go rather than generate from a spec
	localOnly  bool
	remoteOnly bool
	dryRun     bool
	yes        bool
	// overwrite names scaffolded files to rewrite from the current
	// templates even though they exist. Keyed on the cleaned path, the
	// same form put looks up.
	overwrite map[string]bool

	// skipTidy avoids resolving dependencies, which needs a network. Set
	// by tests; there is deliberately no flag for it.
	skipTidy bool
}

func initCmd() *cobra.Command {
	var o initOpts
	var overwriteFiles []string
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "create a new service or CLI",
		Long: "Create a new service, as its own repo or as services/<name>/ inside\n" +
			"an existing one. Every step skips what already exists, so a run that\n" +
			"fails partway can simply be run again.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o.name = args[0]
			// Cleaned to the same form put looks up. Storing the raw
			// string meant `--force ./main.go` passed validation and
			// then silently matched nothing.
			o.overwrite = make(map[string]bool, len(overwriteFiles))
			for _, f := range overwriteFiles {
				o.overwrite[filepath.ToSlash(filepath.Clean(f))] = true
			}
			return runInit(o)
		},
	}
	f := cmd.Flags()
	// Kept, unlike --team/--port/--host, which only restated a config
	// field. This one decides which templates are written, so it has to
	// be answerable before a config.yaml exists - and `runtime:` in the
	// config wins on a re-run.
	f.StringVar(&o.runtimeID, "runtime", "go-service",
		"runtime: "+strings.Join(runtime.Names(), ", "))
	f.StringVar(&o.owner, "owner", "", "GitHub owner or org (default: $HOMELAB_OWNER, else asked)")
	f.StringVar(&o.parentRepo, "parent-repo", "", "add this service to an existing repo (monorepo) instead of creating one")
	f.BoolVar(&o.private, "private", false, "create the GitHub repo private (image-updater then needs a registry credential)")
	// The one value-flag that survives. It decides which files are
	// scaffolded, so it has to be answerable before a config.yaml
	// exists - and on a re-run the config's `spec:` wins, which is how
	// a service that started specless later adopts one: flip the field
	// and run again.
	f.BoolVar(&o.noSpec, "no-spec", false,
		"start without an OpenAPI spec; hand-write server.go. Change `spec:` in config.yaml afterwards")
	f.BoolVar(&o.localOnly, "local-only", false, "generate files only; create nothing on GitHub")
	f.BoolVar(&o.remoteOnly, "remote-only", false, "create the GitHub repo only; generate no files")
	f.BoolVar(&o.dryRun, "dry-run", false, "print what would happen and stop")
	f.BoolVar(&o.yes, "yes", false, "skip the confirmation prompt")
	// Not --force: that reads as "override a safety check", which is what
	// `render --force` genuinely is. This adopts the current template
	// into a file init handed over, which is ordinary maintenance.
	f.StringSliceVar(&overwriteFiles, "overwrite", nil,
		"rewrite these scaffolded files from the current templates, e.g.\n"+
			"--overwrite main.go,Dockerfile. Names one file per entry; run with\n"+
			"an unknown name to see what a service has")

	// Completing --runtime is the one that saves real typing.
	cmd.MarkFlagsMutuallyExclusive("local-only", "remote-only")
	// Only errors if the flag does not exist, which is a programming error
	// caught by the first run.
	_ = cmd.RegisterFlagCompletionFunc("runtime",
		func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return runtime.Names(), cobra.ShellCompDirectiveNoFileComp
		})
	return cmd
}

func runInit(o initOpts) error {

	r, err := runtime.Get(o.runtimeID)
	if err != nil {
		return err
	}

	if o.owner, err = resolveOwner(o); err != nil {
		return err
	}

	c, err := buildConfig(o)
	if err != nil {
		return err
	}
	// Only meaningful for something that becomes a pod: the securityContext
	// defaults to hardened, so a runtime whose image cannot run as uid
	// 65532 would produce a pod that cannot exec its binary - "permission
	// denied", no logs. A CLI has no pod, so hardening does not apply.
	// One source of truth: the artifacts the runtime actually produces.
	arts := r.Artifacts(artifactParams(c, o.owner, o.parentRepo))
	isCLI := !arts.Deployable

	if arts.Deployable && c.Hardened && !r.SupportsHardened() {
		return fmt.Errorf("runtime %q cannot run hardened; set `hardened: false` in config.yaml", r.Name())
	}
	if err := confirm(o, c, isCLI); err != nil {
		return err
	}
	if o.dryRun {
		return nil
	}

	// Remote first, so the local tree ends up inside a real clone with a
	// remote already set, rather than files you then have to wire up.
	dir := "."
	if !o.localOnly {
		d, err := setupRemote(o)
		if err != nil {
			return fmt.Errorf("remote setup: %w", err)
		}
		dir = d
	} else if o.parentRepo != "" {
		// Only reached with --local-only. Normally init clones the repo
		// into the working directory and uses that, so it runs OUTSIDE
		// any repository and none of this applies.
		//
		// --parent-repo names the monorepo to add to, and the service
		// belongs at its root regardless of where this was run.
		//
		// Resolved from the repository rather than from the working
		// directory. Asking "is the cwd's basename the repo name?" is
		// only right when standing in the root: from services/alpha the
		// answer was no, so init descended and produced
		// services/alpha/<repo>/services/<name>. Asking where the
		// repository IS has one answer from anywhere inside it.
		// Asks the REMOTE what this repo is called, not the directory.
		//
		// Comparing filepath.Base(root) assumes the checkout directory
		// is named after the repo. Clone it anywhere else - a worktree,
		// a CI checkout, or just `git clone <url> work` - and the names
		// differ, so init decided it was NOT in the parent repo and
		// created one as a subdirectory: <repo>/hlx-mono/services/<name>
		// inside the repo it was already standing in.
		//
		// The basename is still the fallback, for a repo with no origin.
		if root := repoRoot(); root != "" && repoNameMatches(root, o.parentRepo) {
			dir = root
		} else {
			dir = o.parentRepo
		}
	}
	if o.remoteOnly {
		printNext(o, c, dir, isCLI)
		return nil
	}

	target := dir
	if o.parentRepo != "" {
		target = filepath.Join(dir, "services", o.name)
	}
	// setupLocal returns a tidy failure rather than aborting on it: the
	// files are written and running again resolves them, so exiting
	// non-zero mid-scaffold would leave a tree the user cannot tell the
	// state of.
	//
	// It is printed AFTER the next-steps text, because that text was
	// what buried it - a one-line warning on stderr followed by the
	// "created:" list and a cheerful "what's next" reads as a success.
	// Without a go.sum Go refuses to build at all, so the first thing
	// the user does is the thing that fails.
	tidyErr := setupLocal(o, c, r, target)
	if tidyErr != nil && !errors.Is(tidyErr, errDepsUnresolved) {
		return fmt.Errorf("local setup: %w", tidyErr)
	}
	printNext(o, c, target, isCLI)
	if tidyErr != nil {
		fmt.Println()
		fmt.Fprintf(os.Stderr, "WARNING: dependencies did not resolve, so this tree will not build yet.\n")
		fmt.Fprintf(os.Stderr, "  cd %s && go mod tidy\n", target)
		// tidyErr itself: errors.Unwrap on a two-verb %w wrap returns
		// nil, and the sentinel prefix reads fine inline.
		fmt.Fprintf(os.Stderr, "  (%v)\n", tidyErr)
	}
	return nil
}

// tidy resolves the generated module's dependencies. Best-effort: a
// missing toolchain or no network should not lose the scaffold, but it is
// reported, because the result will not build until it is run.
// tidy runs the runtime's dependency-resolution command in the new
// service directory. What to run is the runtime's business, declared in
// registered.go; this only knows how to run it.
// run executes each group of commands in order, in dir.
func run(dir string, groups ...[][]string) error {
	for _, cmds := range groups {
		for _, argv := range cmds {
			if len(argv) == 0 {
				continue
			}
			cmd := exec.Command(argv[0], argv[1:]...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s in %s: %w\n%s", strings.Join(cmd.Args, " "), dir, err, out)
			}
		}
	}
	return nil
}

// artifactParams derives the render inputs from the options and config, so
// the monorepo layout is decided in one place.
// artifactParams derives the render inputs, so the monorepo layout is
// decided in one place.
//
// It takes only what the CONFIG cannot answer. Everything describing the
// service - its name, port, image, team, whether it has a spec - comes
// from the Config, which Complete() has already defaulted and validated.
// The two arguments are the facts about where the repo lives, which
// config.yaml deliberately does not store.
//
// The narrow signature is the point. This used to take the whole
// initOpts alongside the Config and choose per field, and it chose
// wrong: Port came from the flag while its neighbours came from the
// config, so any caller working from an existing config.yaml - where
// the flag is zero - rendered EXPOSE 0 and a readiness probe against
// port 0. With the flags out of reach, that particular mistake cannot
// be made again.
func artifactParams(c *config.Config, owner, parentRepo string) runtime.Params {
	// The spec's own version when there is a spec, the initial one when
	// there is not. Hardcoding the initial version here meant a
	// regenerated TypeScript client advertised 0.1.0 forever, however
	// far openapi.yml had moved.
	specVersion := specVersionIn(".")
	if specVersion == "" {
		specVersion = runtime.InitialSpecVersion
	}
	p := runtime.Params{
		Name:        c.Name,
		Team:        c.Team,
		Spec:        c.Spec,
		SpecVersion: specVersion,
		Owner:       owner,
		Port:        c.Port,
		Image:       c.Image.Repository,
	}
	if parentRepo != "" {
		p.PathFilter = filepath.Join("services", c.Name)
	}
	p.Module = modulePath(c, owner, parentRepo)
	return p
}

// modulePath is where Go will fetch this service from.
//
// Not a preference: Go requires a module's path to match its location, so
// a wrong value here is not a stylistic problem, it is a module nobody can
// `go get`. Three cases, in order of precedence:
//
//   - config.yaml says so. The escape hatch for a repo that is not named
//     after the service it holds.
//   - a monorepo: the parent repo plus the directory the service sits in.
//     Deriving this from the service name alone - which is what this did
//     until 2026-09-17 - produced a path that pointed nowhere.
//   - a repo of its own, named after the service.
func modulePath(c *config.Config, owner, parentRepo string) string {
	if c.Module != "" {
		return c.Module
	}
	if parentRepo != "" {
		return fmt.Sprintf("github.com/%s/%s/%s", owner, parentRepo,
			filepath.ToSlash(filepath.Join("services", c.Name)))
	}
	return fmt.Sprintf("github.com/%s/%s", owner, c.Name)
}

// buildConfig is what the new service will be.
//
// An existing config.yaml wins. init skips files that are already there,
// so without this it would keep a config it then ignored - writing a
// go.mod derived from flags while config.yaml said something else, and
// leaving the two to disagree silently. Re-running init in a directory
// that already has one is how a half-finished scaffold gets completed.
func buildConfig(o initOpts) (*config.Config, error) {
	// configName, not findConfig: init creates a service HERE, so a
	// config.yaml in a parent belongs to a different service and
	// adopting it would scaffold the wrong thing. Every other command
	// walks up, because they act on a service that already exists.
	if existing, err := config.Load(configName); err == nil {
		return existing, nil
	}

	// Lowercased: a registry path must be lowercase, while the GitHub
	// owner keeps whatever casing the account has. They were the same
	// string while the owner was a hardcoded lowercase default; now that
	// it comes from gh, "ChristopherScot" would render
	// ghcr.io/ChristopherScot/svc and fail at docker push in CI, after
	// everything else had already succeeded.
	image := fmt.Sprintf("ghcr.io/%s/%s", o.owner, o.name)
	if o.parentRepo != "" {
		// One registry path per repo would collide in a monorepo.
		image = fmt.Sprintf("ghcr.io/%s/%s-%s", o.owner, o.parentRepo, o.name)
	}
	// Lowercased as a whole, because every component can carry casing:
	// the owner comes from gh ("ChristopherScot") and the parent repo is
	// whatever the repo is called. A registry path must be lowercase, so
	// ghcr.io/ChristopherScot/MyRepo-svc fails at docker push in CI -
	// after init, render and the build had all succeeded.
	image = strings.ToLower(image)
	// From Defaults(), not a bare literal: hardening and metrics are on
	// by default and their zero value is off, so a literal would scaffold
	// an unhardened, unscraped service - and now that config.yaml is
	// marshalled from the struct, it would write `hardened: false` into
	// the file and make that permanent.
	cfg := config.Defaults()
	c := &cfg
	c.Name = o.name
	c.Team = defaultTeam
	c.Runtime = o.runtimeID
	c.Image = config.Image{Repository: image}
	c.Spec = !o.noSpec
	// No ingress by default. A service that wants one adds `ingress:`
	// to config.yaml and re-runs render - which is also how it gets more
	// than one hostname, something a --host flag could never express.
	return c, c.Complete()
}

// confirm prints exactly what will be created before touching anything
// remote, and defaults to no.
func confirm(o initOpts, c *config.Config, isCLI bool) error {
	vis := "public"
	if o.private {
		vis = "private"
	}
	fmt.Println()
	fmt.Println("about to create:")
	if !o.localOnly {
		if o.parentRepo != "" {
			fmt.Printf("  services/%s/ in the existing repo %s/%s\n", o.name, o.owner, o.parentRepo)
		} else {
			fmt.Printf("  github.com/%s/%s   (%s)\n", o.owner, o.name, vis)
		}
	}
	if !o.remoteOnly {
		if isCLI {
			fmt.Printf("  %s source and release CI\n", o.runtimeID)
		} else {
			fmt.Printf("  %s source, Dockerfile and CI\n", o.runtimeID)
			fmt.Printf("  deploy/ manifests for namespace %s\n", c.Namespace)
		}
	}
	if !isCLI {
		fmt.Printf("  image %s\n", c.Image.Repository)
	}
	if c.Ingress != nil && !isCLI {
		class := "external (LAN)"
		if c.Ingress.Public {
			class = "public (internet)"
		}
		fmt.Printf("  ingress %s via %s\n", c.Ingress.Hosts[0].Name, class)
	}
	if o.private {
		// Worth saying out loud: a private package makes image automation
		// fail with "unauthorized", which surfaces only in the updater log.
		fmt.Println()
		fmt.Println("  note: a private package needs a registry credential for")
		fmt.Println("        argocd-image-updater, which it does not have by default")
	}
	fmt.Println()
	if o.dryRun || o.yes {
		return nil
	}
	fmt.Print("continue? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if s := strings.TrimSpace(strings.ToLower(line)); s != "y" && s != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// setupRemote creates the repo if it does not exist and clones it,
// returning the local directory. Both halves are skipped when already
// present, so re-running is safe.
func setupRemote(o initOpts) (string, error) {
	repo := o.name
	if o.parentRepo != "" {
		repo = o.parentRepo
	}
	slug := o.owner + "/" + repo

	// Already standing in the monorepo: use it rather than cloning a
	// second copy inside itself. Adding the second and later services is
	// the ordinary case, and doing it from inside the repo is the obvious
	// way to try - it used to attempt `gh repo clone pokemon pokemon`
	// from the repo root and fail on the existing directory.
	//
	// repoRoot rather than the working directory's name, so this is right
	// from services/pokedex as well as from the root.
	if o.parentRepo != "" {
		if root := repoRoot(); root != "" && filepath.Base(root) == o.parentRepo {
			fmt.Printf("already inside %s, using it\n", root)
			return root, nil
		}
	}

	// Created whether or not this is a monorepo. --parent-repo used to
	// skip this on the assumption that the parent already existed, which
	// is true for every service but the FIRST one: bootstrapping a new
	// monorepo failed at the clone below with gh's "repository not
	// found", and the only way through was to create the repo by hand -
	// which is the thing this command exists to avoid.
	if err := exec.Command("gh", "repo", "view", slug).Run(); err != nil {
		vis := "--public"
		if o.private {
			vis = "--private"
		}
		fmt.Printf("creating github.com/%s\n", slug)
		desc := fmt.Sprintf("%s service", o.name)
		if o.parentRepo != "" {
			desc = fmt.Sprintf("%s services", o.parentRepo)
		}
		cmd := exec.Command("gh", "repo", "create", slug, vis, "--description", desc)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("gh repo create: %w", err)
		}
	} else {
		fmt.Printf("github.com/%s already exists, reusing it\n", slug)
	}

	if _, err := os.Stat(repo); err == nil {
		fmt.Printf("%s/ already cloned\n", repo)
		return repo, nil
	}
	fmt.Printf("cloning %s\n", slug)
	cmd := exec.Command("gh", "repo", "clone", slug, repo)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh repo clone: %w", err)
	}

	// A repo created a moment ago has no commits, so the clone lands on
	// an unborn HEAD named by whatever init.defaultBranch happens to be
	// on this machine. Every generated workflow triggers on `main`, so a
	// machine still defaulting to `master` produces a repo whose CI never
	// runs - and nothing says so, because a workflow that does not match
	// its branch is not an error.
	if err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Run(); err != nil {
		b := exec.Command("git", "-C", repo, "checkout", "-q", "-B", "main")
		b.Stderr = os.Stderr
		if err := b.Run(); err != nil {
			return "", fmt.Errorf("setting the initial branch to main: %w", err)
		}
	}
	return repo, nil
}

// workflowPath is where CI lands. One function rather than a literal in
// two places: a monorepo puts it outside the service directory, so a
// caller guessing ".github/workflows/build.yaml" would name a file that
// is never written there - which is exactly what --overwrite used to
// accept and silently ignore.
func workflowPath(o initOpts) string {
	if o.parentRepo != "" {
		// One workflow per service in a monorepo, path-filtered so a push
		// only rebuilds what changed.
		return filepath.ToSlash(filepath.Join("..", "..", ".github", "workflows", o.name+".yaml"))
	}
	return ".github/workflows/build.yaml"
}

// printTrustCommand writes the one-liner that registers this package's
// trusted publisher.
//
// Extracted so the casing is testable, because npm compares every field
// LITERALLY and the two halves need opposite treatment:
//
//   - the npm scope must be lowercase, or npm rejects the package name;
//   - the GitHub owner must keep its canonical casing, because that is
//     what the OIDC token carries. A configuration created with a
//     lowercased owner never matches, and the failure is a 404 naming
//     the package, which points nowhere near the owner.
//
// Sharing one value between them is how they drift, so they are derived
// separately here rather than from a single variable.
func printTrustCommand(w io.Writer, o initOpts, name string) {
	fmt.Fprintf(w, "      npm trust github @%s/%s-client \\\n", strings.ToLower(o.owner), name)
	fmt.Fprintf(w, "        --file publish.yaml --repo %s/%s --allow-publish\n",
		o.owner, publishRepo(o))
	fmt.Fprintln(w, "    (needs npm >= 11.10; it opens a browser to authenticate)")
}

// publishRepo is the GitHub repository holding this service, which is
// what a trusted publisher is configured against: the parent in a
// monorepo, the service's own repo otherwise.
func publishRepo(o initOpts) string {
	if o.parentRepo != "" {
		return o.parentRepo
	}
	return o.name
}

// publishWorkflowPath is where the npm publish workflow lands: always
// .github/workflows/publish.yaml at the REPOSITORY root, whichever
// layout this is.
//
// Fixed because npm trusted publishing takes the workflow filename at
// setup and will not let it change afterwards. One name means
// configuring a package is the same three values every time - owner,
// repo, publish.yaml - rather than a lookup per service.
//
// At the root because one workflow covers every client in the repo: it
// finds clients/ts/package.json rather than naming paths, so a monorepo
// publishes each service's client and a single-service repo publishes
// its one.
func publishWorkflowPath(o initOpts) string {
	if o.parentRepo != "" {
		return filepath.ToSlash(filepath.Join("..", "..", ".github", "workflows", "publish.yaml"))
	}
	return ".github/workflows/publish.yaml"
}

// overwritable reports whether --overwrite may rewrite a scaffolded
// file from its template.
//
// Almost everything is. init writes api/ through the same put as
// main.go, calls render.All for manifests and runs the runtime's
// Generate - so rewriting any of those repeats what init already did
// with the code that owns them, rather than reaching across a boundary.
// A command is not an owner; the module is.
//
// The exceptions are files whose CONTENT is not the template's to
// restate:
//
//   - config.yaml and openapi.yml are the sources everything else
//     derives from. A template copy would discard the service.
//   - go.mod and go.sum belong to the toolchain. `go mod tidy`
//     maintains them, and a template copy is stale on arrival.
//   - server.go and its tests are the seam a service replaces on
//     purpose; its own header says so.
func overwritable(path string) bool {
	switch path {
	case "config.yaml", "openapi.yml", "go.mod", "go.sum",
		"server.go", "main_test.go":
		return false
	}
	return true
}

// derived reports whether a path is computed entirely from config.yaml,
// with nothing of the author's in it.
//
// Those files are rewritten on every run, ignoring the "skip what
// already exists" rule that protects everything else. That rule exists
// so a service's source survives a re-run - but these have no content
// to protect, and keeping a stale one is actively wrong:
// kustomization.yaml lists the other files, so a re-run that wrote a
// new manifest kept a resources: block that did not mention it. The
// file was committed, visible in the diff, and never applied.
//
// argocd.json is the same shape: it carries repoURL and manifestPath,
// so a stale copy points Argo at where the service used to be.
//
// Matched on the base name because the deploy directory is nested under
// deploy/<name>/.
func derived(path string) bool {
	switch filepath.Base(path) {
	case "kustomization.yaml", render.AppEntryFile:
		return true
	}
	return false
}

// checkOverwrite rejects a name that is not scaffolding, before anything
// is written.
//
// An unknown name used to be a silent no-op: `--overwrite mian.go`
// exited 0 having done nothing, and the file appeared under "kept" with
// no hint it had been asked for.
func checkOverwrite(want, scaffolding map[string]bool) error {
	var unknown []string
	for p := range want {
		if !scaffolding[p] {
			unknown = append(unknown, p)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)

	names := make([]string, 0, len(scaffolding))
	for p := range scaffolding {
		names = append(names, p)
	}
	sort.Strings(names)

	return fmt.Errorf("--overwrite %s: not scaffolding for this service.\n"+
		"manifests come from config.yaml (`homelabctl render`) and generated code\n"+
		"from openapi.yml (`homelabctl regen`). this service scaffolds:\n  %s",
		strings.Join(unknown, ", "), strings.Join(names, "\n  "))
}

// setupLocal writes the service. Existing files are left alone so a re-run
// does not clobber work in progress.
// resolveOwner decides which GitHub owner or org this service is created
// under: --owner, else $HOMELAB_OWNER, else ask.
//
// Deliberately NOT defaulted, and not silently taken from `gh auth`
// either. The owner decides the GitHub repo, the ghcr.io image path and
// the Go module path, so a wrong one is not a typo to fix later - it
// scaffolds a service pointing at someone else's namespace, and the
// failure surfaces at docker push in CI long after init reported
// success. Anyone with more than one account, or scaffolding under an
// org rather than their own login, would hit exactly that.
//
// The gh login is offered as the suggestion when asking, because it is
// usually right - but it is a suggestion the author confirms rather than
// a default that acts on its own.
func resolveOwner(o initOpts) (string, error) {
	if o.owner != "" {
		return o.owner, nil
	}
	if env := strings.TrimSpace(os.Getenv(ownerEnv)); env != "" {
		return env, nil
	}
	// Non-interactive: a prompt here would hang a CI run forever rather
	// than fail it.
	if o.yes || o.dryRun {
		if gh := ghLogin(); gh != "" {
			return gh, nil
		}
		return "", fmt.Errorf("no GitHub owner: pass --owner or set %s", ownerEnv)
	}

	suggestion := ghLogin()
	if suggestion != "" {
		fmt.Printf("GitHub owner or org [%s]: ", suggestion)
	} else {
		fmt.Print("GitHub owner or org: ")
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	owner := strings.TrimSpace(line)
	if owner == "" {
		owner = suggestion
	}
	if owner == "" {
		return "", fmt.Errorf("no GitHub owner given; pass --owner or set %s", ownerEnv)
	}
	offerToPersist(owner)
	return owner, nil
}

// offerToPersist prints the export line rather than editing a shell
// profile. Which file to write is a guess - .zshrc, .bash_profile,
// .config/fish, a direnv .envrc - and a tool that guesses wrong has
// silently edited a file the author did not expect it to touch.
func offerToPersist(owner string) {
	fmt.Printf("\n  to skip this next time: export %s=%s\n\n", ownerEnv, owner)
}

// repoNameMatches reports whether the repository at root is the one
// named by --parent-repo.
//
// From origin when there is one, because that is what the repository IS
// rather than where it happens to sit on disk.
func repoNameMatches(root, want string) bool {
	if want == "" {
		return false
	}
	out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output()
	if err != nil {
		// No remote to ask: fall back to the directory name, which is
		// the old behaviour and right for a repo that was never pushed.
		return filepath.Base(root) == want
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	return path.Base(url) == want
}

// ghLogin is the account gh is authenticated as, or "" if it cannot say.
// A suggestion only: see resolveOwner.
func ghLogin() string {
	out, err := exec.Command("gh", "api", "user", "--jq", ".login").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func setupLocal(o initOpts, c *config.Config, r runtime.Runtime, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	a := r.Artifacts(artifactParams(c, o.owner, o.parentRepo))

	// What --overwrite may name: the files init hands over and never
	// rewrites. Not everything it writes - api/ and clients/ are regen's,
	// openapi.yml and config.yaml are sources, go.mod and go.sum are the
	// toolchain's, and deploy/ is render's. Computed before anything is
	// written, so a bad name fails before the first file lands.
	// Rendered here rather than beside the write, so the allowlist can
	// name them: --overwrite has to know every path init produces before
	// it writes the first one.
	// What the deploy plane owns, NAMED rather than rendered.
	//
	// --overwrite needs the inventory, not the bodies. Rendering to get
	// it meant calling render.All with a zero Source, which has no git
	// remote and no manifest contents: it wrote an argocd.json missing
	// repoURL and manifestPath, and failed outright on any config
	// carrying manifests:. render.Files answers the question that was
	// actually being asked.
	var manifestPaths []string
	if a.Deployable {
		manifestPaths = render.Files(c)
	}

	scaffolding := map[string]bool{}
	if a.Dockerfile != "" {
		scaffolding["Dockerfile"] = true
	}
	if a.Workflow != "" {
		// Whichever path it lands at - a monorepo puts it at
		// ../../.github/workflows/<name>.yaml, which is why this comes
		// from the same helper setupLocal writes with rather than a
		// literal a user would have to guess.
		scaffolding[workflowPath(o)] = true
	}
	if a.PublishWorkflow != "" {
		scaffolding[publishWorkflowPath(o)] = true
	}
	for _, f := range a.Files {
		if overwritable(f.Path) {
			scaffolding[f.Path] = true
		}
	}
	for _, mp := range manifestPaths {
		scaffolding[filepath.Join(DeployDirName, c.Name, mp)] = true
	}
	if err := checkOverwrite(o.overwrite, scaffolding); err != nil {
		return err
	}

	var written, skipped, overwritten []string
	// Deferred to the end of the run; see where it is set.
	var tidyErr error

	put := func(path, body string) error {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil && !derived(path) {
			// Scaffolded files are handed over and never rewritten,
			// which is what makes them editable - and also means a later
			// template fix cannot reach a service that already exists.
			// --overwrite is how one is pulled in, having read the diff.
			//
			// One source for what may be rewritten: the scaffolding set
			// above, which checkOverwrite has already validated against.
			// A second bool per call site meant the two could disagree,
			// and they did - manifests were in the set and refused here.
			if !o.overwrite[path] {
				skipped = append(skipped, path)
				return nil
			}
			overwritten = append(overwritten, path)
			if err := writeFile(full, body); err != nil {
				return err
			}
			return nil
		}
		if err := writeFile(full, body); err != nil {
			return err
		}
		written = append(written, path)
		return nil
	}

	for _, f := range a.Files {
		if err := put(f.Path, f.Body); err != nil {
			return err
		}
	}
	// Nothing here asks what kind of runtime this is - a CLI simply has no
	// Dockerfile and is not Deployable.
	if a.Dockerfile != "" {
		if err := put("Dockerfile", a.Dockerfile); err != nil {
			return err
		}
	}
	if a.Deployable {
		if err := put("config.yaml", configYAML(c)); err != nil {
			return err
		}
		// Overwritten rather than skipped, unlike everything else here.
		// It is not this service's content: it is a copy of a constant
		// in the binary, identical in every repo, that exists only so an
		// editor can resolve the `$schema=` line and offer completion.
		// Nobody edits it, and a stale copy silently validates against a
		// schema the tool stopped using - so the tool keeps it current.
		if err := config.WriteSchema(dir); err != nil {
			return err
		}
		// Rendered HERE rather than from the inventory above, and with a
		// real Source.
		//
		// setupRemote has already run (see run()), so the tree is a
		// clone with a remote and gitSource resolves - which is what
		// makes argocd.json carry repoURL and manifestPath. Rendering
		// with a zero Source wrote a file that `render --register` then
		// refuses as "no git remote yet".
		//
		// Deferred to here so a failure lands after the scaffolding
		// checks rather than before them.
		cfgPath := filepath.Join(dir, "config.yaml")
		src, err := withManifests(gitSource(cfgPath), c, cfgPath)
		if err != nil {
			return err
		}
		manifests, err := render.All(c, src)
		if err != nil {
			return err
		}
		// Manifests are generated rather than copied, so they reflect
		// current conventions instead of whatever the template looked like
		// the day the service was created. `render` regenerates them later
		// with this same function.
		for _, out := range manifests {
			// deploy/<name>/, matching `render --out deploy`. Writing
			// them flat meant the first render moved every file, and it
			// forced the next-steps text to describe a rename: "copy
			// deploy/*.yaml into the homelab repo AS <name>/". Nested,
			// the copy is `cp -r` and the directory already has the name
			// the app occupies in that repo.
			if err := put(filepath.Join(DeployDirName, c.Name, out.Path), out.Body); err != nil {
				return err
			}
		}
	}
	wfPath := workflowPath(o)
	if err := put(wfPath, a.Workflow); err != nil {
		return err
	}

	// The publish workflow, if this runtime generates anything to
	// publish. Byte-identical in every repo - it carries no service name
	// and no template variables, and discovers clients by looking for
	// clients/ts/package.json - so writing it for the second service in
	// a monorepo is a no-op rather than a conflict, and adding a third
	// service never requires editing it.
	if a.PublishWorkflow != "" {
		if err := put(publishWorkflowPath(o), a.PublishWorkflow); err != nil {
			return err
		}
	}

	// Resolve dependencies so the scaffold builds immediately. Without a
	// go.sum, Go refuses to build at all - it will not fetch on demand -
	// so a template that declares any dependency is dead on arrival.
	if o.skipTidy {
		// nothing to resolve
		// Generate first - a lockfile cannot resolve an import that does
		// not exist yet - then upgrade, then lock what that settled on.
	} else if err := run(dir, r.Generate(artifactParams(c, o.owner, o.parentRepo)), r.Upgrade(), r.Lock()); err != nil {
		err = fmt.Errorf("%w: %w", errDepsUnresolved, err)
		// Recorded, not just warned about.
		//
		// This is one line on stderr, and the "created:" list plus the
		// next-steps text print after it - so the failure scrolls away
		// and the run reads as a success. Without a go.sum Go refuses to
		// build at all, so the scaffold is dead on arrival: the first
		// thing the user does is the thing that fails, with no hint that
		// init already knew.
		//
		// Still not fatal. The files are on disk and running again
		// resolves them; exiting non-zero here would leave a scaffold
		// the user cannot tell the state of. It is reported at the END
		// instead, after the file lists, where it is the last thing on
		// screen.
		tidyErr = err
	}

	if len(overwritten) > 0 {
		fmt.Println()
		fmt.Println("overwritten:")
		for _, p := range overwritten {
			fmt.Println(" ", p)
		}
	}

	fmt.Println()
	fmt.Println("created:")
	for _, p := range written {
		fmt.Println("  " + filepath.Join(dir, p))
	}
	if len(skipped) > 0 {
		fmt.Println("kept (already existed):")
		for _, p := range skipped {
			fmt.Println("  " + filepath.Join(dir, p))
		}
	}

	return tidyErr
}

// errDepsUnresolved marks a scaffold that was written but whose
// dependencies did not resolve. The files are fine; the tree does not
// build yet. Distinguished from a real setup failure so run() can
// report it after the next-steps text rather than aborting on it.
var errDepsUnresolved = errors.New("dependencies did not resolve")

func printNext(o initOpts, c *config.Config, dir string, isCLI bool) {
	fmt.Println()
	fmt.Println("what's next:")
	if !o.remoteOnly {
		fmt.Printf("  - review and commit in %s\n", dir)
		if isCLI {
			fmt.Println("  - bump VERSION and push; CI cross-compiles and publishes a release")
			fmt.Println()
			fmt.Printf("    users then install with `%s update`\n", o.name)
			return
		}
		fmt.Println("  - push to main; CI builds and pushes the image")
	}
	// One command, not a procedure.
	//
	// This used to narrate the deploy workflow: cp -r the deploy
	// directory into the GitOps repo, then copy
	// deploy/_argocd-application.yaml into app-of-apps/apps/. Every
	// line of that was wrong by the time anyone read it. That file is
	// not rendered - the generator input is argocd.json - and an
	// ApplicationSet has discovered services by globbing */argocd.json
	// since, so there is no per-service file to place at all.
	//
	// It rotted because init does not own any of it. init scaffolds a
	// service; registration belongs to `render --register`, which opens
	// the PR itself and prints its own result. Prose here describing
	// another command's mechanism has no compiler and no test to keep
	// it honest, and drifted through at least two renames unnoticed.
	// Pointing at the command means it cannot drift again.
	fmt.Println("  - register it with Argo, which opens a PR on the GitOps repo:")
	fmt.Println("      homelabctl register")
	// Named here because nothing else reports it: CI cannot reach the
	// cluster, so a manifest the API server rejects is invisible until
	// someone asks.
	fmt.Println("  - after it deploys, check what Argo made of it:")
	fmt.Println("      homelabctl status")
	if !isCLI && c.Spec {
		// The spec is the source of truth, and everything derived from it
		// is regenerated by one command rather than four remembered ones.
		fmt.Println("  - after editing openapi.yml, run `homelabctl regen`")
		// Two one-time steps, in this order, both printed as commands
		// rather than described.
		//
		// The FIRST publish is manual because npm trusted publishing is
		// configured per PACKAGE, and a package that has never been
		// published has no settings to configure. After that, CI
		// publishes every version on the spec's version bump.
		//
		// `npm trust` needs npm >= 11.10. It replaces what used to be a
		// walk through the npmjs.com UI, which is worth printing as a
		// command because the fixed publish.yaml filename makes it the
		// same line in every repo - the only thing that changes is the
		// package and the repo.
		fmt.Println()
		fmt.Println("  - publish the TypeScript client once, by hand, after the")
		fmt.Println("    first push:")
		fmt.Printf("      cd %s/clients/ts && npm publish\n", dir)
		fmt.Println()
		fmt.Println("  - then let CI publish every version after that:")
		printTrustCommand(os.Stdout, o, c.Name)
		fmt.Println()
	} else if !isCLI {
		// No spec: server.go is the surface, and it is hand-written.
		fmt.Println("  - edit server.go to add routes; there is no spec to generate from")
	}
	// Mentioned unconditionally: init takes its config from flags, so it
	// cannot know whether secrets will be added, and adding them later is
	// the common case. Without this the Vault role is a step nobody knows
	// to take, which is how a SecretStore ends up naming a role that does
	// not exist.
	fmt.Println("  - if you add `secrets:` to config.yaml, create its Vault role:")
	// No path argument: vaultCmd is cobra.NoArgs and finds config.yaml
	// itself. The old line printed `vault config.yaml --apply`, which
	// fails with `unknown command "config.yaml"` for anyone who copies
	// it.
	fmt.Println("      homelabctl vault --apply")
	fmt.Println()
	fmt.Println("argocd-image-updater then deploys every push. no homelab")
	fmt.Println("credential is needed in the service repo.")
	if o.private {
		fmt.Println()
		fmt.Println("because the package is private, give image-updater a registry")
		fmt.Println("credential or it will fail with 'unauthorized'.")
	}
}

// writeFile creates a scaffolded file. Everything scaffolded is source,
// config or CI YAML, so they are all 0644 - a CLI's binary is produced by
// `go build`, not written here, and its self-update opens the replacement
// 0755 itself.
func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// configYAML renders a Config back to the file it was loaded from.
//
// Marshalled from the struct, so the yaml tags are the single place the
// file's shape lives. The previous version printed seven fields by hand
// and silently dropped the other fourteen - env, probes, resources,
// patches, namespace, kind, schedule and the rest - so a Config that had
// been through Load could not be written back without losing most of
// itself. Nothing caught it, because the only caller skips a config.yaml
// that already exists: one unrelated line stood between that and data
// loss. A field added to the struct now appears here by existing, rather
// than by someone remembering to add a Printf.
//
// What is written is the difference from Defaults(), not the whole
// struct. A scaffolded config should say what is true of THIS service,
// not restate every default - and a default that is spelled out stops
// tracking the default when it later changes.
func configYAML(c *config.Config) string {
	body, err := yaml.Marshal(minus(c, config.Defaults()))
	if err != nil {
		// Config is plain data; Marshal fails only on a field that cannot
		// be represented, which is a build-time mistake.
		panic(fmt.Sprintf("marshalling config: %v", err))
	}

	var b strings.Builder
	b.WriteString("# yaml-language-server: $schema=config.schema.json\n" +
		"# The single source of truth for this service. `homelabctl render`\n" +
		"# regenerates every manifest from it, so change things here rather than\n" +
		"# editing deploy/ by hand.\n")
	b.Write(body)
	if c.Secrets == nil {
		// A hint rather than an empty block, since a service with no
		// secrets should not carry one.
		b.WriteString(`
# secrets:
#   vaultPath: <name>/config      # one Vault path per service
#   keys: [SOME_TOKEN]
`)
	}
	return b.String()
}

// minus blanks the fields that already match the default, so marshalling
// writes only what this service actually chose.
//
// It works on a copy: clearing fields on the caller's Config would leave
// it half-populated for everything that runs after this.
func minus(c *config.Config, def config.Config) *config.Config {
	out := *c
	if out.Kind == def.Kind {
		out.Kind = ""
	}
	if out.Replicas == def.Replicas {
		out.Replicas = 0
	}
	// Namespace defaults to the service name, not to a constant.
	if out.Namespace == out.Name {
		out.Namespace = ""
	}
	if out.Probes != nil && def.Probes != nil && *out.Probes == *def.Probes {
		out.Probes = nil
	}
	if out.Resources != nil && def.Resources != nil && *out.Resources == *def.Resources {
		out.Resources = nil
	}
	return &out
}
