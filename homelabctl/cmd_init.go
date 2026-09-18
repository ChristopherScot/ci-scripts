package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
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
		if root := repoRoot(); filepath.Base(root) == o.parentRepo {
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
	if err := setupLocal(o, c, r, target); err != nil {
		return fmt.Errorf("local setup: %w", err)
	}
	printNext(o, c, target, isCLI)
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
	p := runtime.Params{
		Name:        c.Name,
		Team:        c.Team,
		Spec:        c.Spec,
		SpecVersion: runtime.InitialSpecVersion,
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

	if o.parentRepo == "" {
		if err := exec.Command("gh", "repo", "view", slug).Run(); err != nil {
			vis := "--public"
			if o.private {
				vis = "--private"
			}
			fmt.Printf("creating github.com/%s\n", slug)
			cmd := exec.Command("gh", "repo", "create", slug, vis,
				"--description", fmt.Sprintf("%s service", o.name))
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("gh repo create: %w", err)
			}
		} else {
			fmt.Printf("github.com/%s already exists, reusing it\n", slug)
		}
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
	var manifests []render.Output
	if a.Deployable {
		var err error
		if manifests, err = render.All(c); err != nil {
			return err
		}
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
	for _, f := range a.Files {
		if overwritable(f.Path) {
			scaffolding[f.Path] = true
		}
	}
	// Manifests too: init renders them with render.All, so --overwrite
	// deploy/x re-runs the same function render would. It is not a
	// second implementation, just a second entry point.
	for _, out := range manifests {
		scaffolding[filepath.Join("deploy", c.Name, out.Path)] = true
	}
	if err := checkOverwrite(o.overwrite, scaffolding); err != nil {
		return err
	}

	var written, skipped, overwritten []string

	put := func(path, body string) error {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil {
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
			if err := put(filepath.Join("deploy", c.Name, out.Path), out.Body); err != nil {
				return err
			}
		}
	}
	wfPath := workflowPath(o)
	if err := put(wfPath, a.Workflow); err != nil {
		return err
	}

	// Resolve dependencies so the scaffold builds immediately. Without a
	// go.sum, Go refuses to build at all - it will not fetch on demand -
	// so a template that declares any dependency is dead on arrival.
	if o.skipTidy {
		// nothing to resolve
		// Generate first - a lockfile cannot resolve an import that does
		// not exist yet - then upgrade, then lock what that settled on.
	} else if err := run(dir, r.Generate(artifactParams(c, o.owner, o.parentRepo)), r.Upgrade(), r.Lock()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
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
	return nil
}

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
	fmt.Printf("  - cp -r %s/deploy/%s into the homelab repo\n", dir, o.name)
	fmt.Printf("  - copy %s/deploy/_argocd-application.yaml into\n", dir)
	fmt.Printf("    homelab app-of-apps/apps/%s.yaml\n", o.name)
	// The copy above is the step that reaches the cluster, and it is done
	// by hand. Point at the command that makes it reviewable rather than
	// leaving people to eyeball it.
	fmt.Println("  - run `homelabctl diff` to check the copy landed as rendered")
	if !isCLI && c.Spec {
		// The spec is the source of truth, and everything derived from it
		// is regenerated by one command rather than four remembered ones.
		fmt.Println("  - after editing openapi.yml, run `homelabctl regen`")
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
	fmt.Println("      homelabctl vault config.yaml --apply")
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
