package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/runtime"
	"github.com/spf13/cobra"
)

// init is idempotent: every step checks for what it would create and skips
// it if present, so a run that fails partway - no network, a rate limit, a
// wrong flag - can simply be run again rather than needing manual cleanup.
type initOpts struct {
	name       string
	runtimeID  string
	team       string
	host       string
	public     bool
	port       int
	owner      string
	parentRepo string // create the service inside this existing repo
	private    bool
	noSpec     bool // hand-write server.go rather than generate from a spec
	localOnly  bool
	remoteOnly bool
	dryRun     bool
	yes        bool
	// skipTidy avoids resolving dependencies, which needs a network. Set
	// by tests; there is deliberately no flag for it.
	skipTidy bool
}

func initCmd() *cobra.Command {
	var o initOpts
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "create a new service or CLI",
		Long: "Create a new service, as its own repo or as services/<name>/ inside\n" +
			"an existing one. Every step skips what already exists, so a run that\n" +
			"fails partway can simply be run again.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o.name = args[0]
			return runInit(o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.runtimeID, "runtime", "go-service", "runtime: "+strings.Join(runtime.Names(), ", "))
	f.StringVar(&o.team, "team", "me-myself-and-i", "owning team")
	f.StringVar(&o.host, "host", "", "ingress hostname (omit for no ingress)")
	f.BoolVar(&o.public, "public", false, "route via the internet-facing ingress controller")
	f.IntVar(&o.port, "port", config.DefaultPort, "port the service listens on")
	f.StringVar(&o.owner, "owner", "christopherscot", "GitHub owner")
	f.StringVar(&o.parentRepo, "parent-repo", "", "add this service to an existing repo (monorepo) instead of creating one")
	f.BoolVar(&o.private, "private", false, "create the GitHub repo private (image-updater then needs a registry credential)")
	f.BoolVar(&o.noSpec, "no-spec", false, "hand-write server.go instead of generating the API from openapi.yml")
	f.BoolVar(&o.localOnly, "local-only", false, "generate files only; create nothing on GitHub")
	f.BoolVar(&o.remoteOnly, "remote-only", false, "create the GitHub repo only; generate no files")
	f.BoolVar(&o.dryRun, "dry-run", false, "print what would happen and stop")
	f.BoolVar(&o.yes, "yes", false, "skip the confirmation prompt")

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

	c, err := buildConfig(o)
	if err != nil {
		return err
	}
	// Only meaningful for something that becomes a pod: the securityContext
	// defaults to hardened, so a runtime whose image cannot run as uid
	// 65532 would produce a pod that cannot exec its binary - "permission
	// denied", no logs. A CLI has no pod, so hardening does not apply.
	// One source of truth: the artifacts the runtime actually produces.
	arts := r.Artifacts(artifactParams(o, c))
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
	} else if o.parentRepo != "" && filepath.Base(mustCwd()) != o.parentRepo {
		// --parent-repo names the monorepo to add to. Descend into it
		// only when we are not already there: running from inside the
		// repo is the normal case, and prepending its name produced
		// platform/platform/services/<name>.
		dir = o.parentRepo
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
func artifactParams(o initOpts, c *config.Config) runtime.Params {
	p := runtime.Params{
		Name:        o.name,
		Team:        c.Team,
		Spec:        c.Spec,
		SpecVersion: runtime.InitialSpecVersion,
		Owner:       o.owner,
		Port:        o.port,
		Image:       c.Image.Repository,
	}
	if o.parentRepo != "" {
		p.PathFilter = filepath.Join("services", o.name)
	}
	p.Module = modulePath(o, c)
	return p
}

// mustCwd is the working directory, or "." when it cannot be determined -
// in which case the caller's comparison simply fails and the old
// behaviour applies.
func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
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
func modulePath(o initOpts, c *config.Config) string {
	if c.Module != "" {
		return c.Module
	}
	if o.parentRepo != "" {
		return fmt.Sprintf("github.com/%s/%s/%s", o.owner, o.parentRepo,
			filepath.ToSlash(filepath.Join("services", o.name)))
	}
	return fmt.Sprintf("github.com/%s/%s", o.owner, o.name)
}

// buildConfig is what the new service will be.
//
// An existing config.yaml wins. init skips files that are already there,
// so without this it would keep a config it then ignored - writing a
// go.mod derived from flags while config.yaml said something else, and
// leaving the two to disagree silently. Re-running init in a directory
// that already has one is how a half-finished scaffold gets completed.
func buildConfig(o initOpts) (*config.Config, error) {
	if existing, err := config.Load(defaultConfigPath); err == nil {
		return existing, nil
	}

	image := fmt.Sprintf("ghcr.io/%s/%s", o.owner, o.name)
	if o.parentRepo != "" {
		// One registry path per repo would collide in a monorepo.
		image = fmt.Sprintf("ghcr.io/%s/%s-%s", o.owner, o.parentRepo, o.name)
	}
	c := &config.Config{
		Name:    o.name,
		Team:    o.team,
		Runtime: o.runtimeID,
		Port:    o.port,
		Image:   config.Image{Repository: image},
		Spec:    !o.noSpec,
	}
	if o.host != "" {
		c.Ingress = &config.Ingress{Host: o.host, Public: o.public}
	}
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
		fmt.Printf("  ingress %s via %s\n", c.Ingress.Host, class)
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

// setupLocal writes the service. Existing files are left alone so a re-run
// does not clobber work in progress.
func setupLocal(o initOpts, c *config.Config, r runtime.Runtime, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	a := r.Artifacts(artifactParams(o, c))

	var written, skipped []string
	put := func(path, body string) error {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil {
			skipped = append(skipped, path)
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
		// Editors validate against this as you type, which is what turns a
		// silently-ignored typo like `hardend:` into a visible squiggle.
		if err := put("config.schema.json", config.Schema); err != nil {
			return err
		}
		// Manifests are generated rather than copied, so they reflect
		// current conventions instead of whatever the template looked like
		// the day the service was created. `render` regenerates them later.
		manifests, err := render.All(c, c.Image.Repository+":latest")
		if err != nil {
			return err
		}
		for _, out := range manifests {
			if err := put(filepath.Join("deploy", out.Path), out.Body); err != nil {
				return err
			}
		}
	}
	wfPath := ".github/workflows/build.yaml"
	if o.parentRepo != "" {
		// One workflow per service in a monorepo, path-filtered so a push
		// only rebuilds what changed.
		wfPath = filepath.Join("..", "..", ".github", "workflows", o.name+".yaml")
	}
	if err := put(wfPath, a.Workflow); err != nil {
		return err
	}

	if a.Deployable {
		appPath := filepath.Join(dir, "deploy", "_argocd-application.yaml")
		appRepo := "https://github.com/" + o.owner + "/homelab"
		if err := writeFile(appPath, render.Application(c, appRepo, o.name)); err != nil {
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
	} else if err := run(dir, r.Generate(c.Spec), r.Upgrade(), r.Lock()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
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
	fmt.Printf("  - copy %s/deploy/*.yaml (except _argocd-application.yaml)\n", dir)
	fmt.Printf("    into the homelab repo as %s/\n", o.name)
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

func configYAML(c *config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# yaml-language-server: $schema=config.schema.json
# The single source of truth for this service. `+"`homelabctl render`"+`
# regenerates every manifest from it, so change things here rather than
# editing deploy/ by hand.
name: %s
team: %s
runtime: %s
port: %d
image:
  repository: %s
`, c.Name, c.Team, c.Runtime, c.Port, c.Image.Repository)
	// Only when off, since spec-first is the default. It has to be
	// written: every later command reads this file, and a service whose
	// config claims a spec it does not have regenerates into a build
	// failure.
	if !c.Spec {
		b.WriteString("\n# server.go is hand-written here; there is no openapi.yml to\n" +
			"# generate from and no client for consumers to import.\nspec: false\n")
	}
	if c.Ingress != nil {
		fmt.Fprintf(&b, "ingress:\n  host: %s\n  public: %t\n", c.Ingress.Host, c.Ingress.Public)
	}
	b.WriteString(`
# secrets:
#   vaultPath: <name>/config      # one Vault path per service
#   keys: [SOME_TOKEN]
`)
	return b.String()
}
