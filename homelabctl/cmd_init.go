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
	"gopkg.in/yaml.v3"
)

// init is idempotent: every step checks for what it would create and skips
// it if present, so a run that fails partway - no network, a rate limit, a
// wrong flag - can simply be run again rather than needing manual cleanup.
// defaultTeam stamps every log line until config.yaml says otherwise.
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
	// force names files to rewrite even though they exist, from
	// --force. Scaffolded files are otherwise never rewritten.
	force map[string]bool

	// skipTidy avoids resolving dependencies, which needs a network. Set
	// by tests; there is deliberately no flag for it.
	skipTidy bool
}

func initCmd() *cobra.Command {
	var o initOpts
	var forceFiles []string
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "create a new service or CLI",
		Long: "Create a new service, as its own repo or as services/<name>/ inside\n" +
			"an existing one. Every step skips what already exists, so a run that\n" +
			"fails partway can simply be run again.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o.name = args[0]
			o.force = map[string]bool{}
			for _, f := range forceFiles {
				o.force[f] = true
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
	f.StringVar(&o.owner, "owner", "christopherscot", "GitHub owner")
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
	f.StringSliceVar(&forceFiles, "force", nil,
		"rewrite these scaffolded files even though they exist, e.g. --force main.go,Dockerfile")

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
	if existing, err := config.Load(defaultConfigPath); err == nil {
		return existing, nil
	}

	image := fmt.Sprintf("ghcr.io/%s/%s", o.owner, o.name)
	if o.parentRepo != "" {
		// One registry path per repo would collide in a monorepo.
		image = fmt.Sprintf("ghcr.io/%s/%s-%s", o.owner, o.parentRepo, o.name)
	}
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

// setupLocal writes the service. Existing files are left alone so a re-run
// does not clobber work in progress.
func setupLocal(o initOpts, c *config.Config, r runtime.Runtime, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	a := r.Artifacts(artifactParams(c, o.owner, o.parentRepo))

	var written, skipped, forced []string
	put := func(path, body string) error {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil {
			// --force names the files to rewrite. Scaffolded files are
			// handed over to the service and never rewritten otherwise,
			// which is what makes them editable - and also means a later
			// template fix cannot reach a service that already exists.
			// This is how you pull one in, having read the diff first.
			if !o.force[path] {
				skipped = append(skipped, path)
				return nil
			}
			forced = append(forced, path)
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
		// the day the service was created. `render` regenerates them later.
		manifests, err := render.All(c)
		if err != nil {
			return err
		}
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
	} else if err := run(dir, r.Generate(artifactParams(c, o.owner, o.parentRepo)), r.Upgrade(), r.Lock()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	if len(forced) > 0 {
		fmt.Println()
		fmt.Println("overwritten (--force):")
		for _, p := range forced {
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
