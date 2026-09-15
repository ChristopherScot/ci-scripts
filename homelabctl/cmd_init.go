package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/runtime"
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
	localOnly  bool
	remoteOnly bool
	dryRun     bool
	yes        bool
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var o initOpts
	fs.StringVar(&o.runtimeID, "runtime", "go", "runtime: "+strings.Join(runtime.Names(), ", "))
	fs.StringVar(&o.team, "team", "me-myself-and-i", "owning team")
	fs.StringVar(&o.host, "host", "", "ingress hostname (omit for no ingress)")
	fs.BoolVar(&o.public, "public", false, "route via the internet-facing ingress controller")
	fs.IntVar(&o.port, "port", 3000, "port the service listens on")
	fs.StringVar(&o.owner, "owner", "christopherscot", "GitHub owner")
	fs.StringVar(&o.parentRepo, "parent-repo", "", "add this service to an existing repo (monorepo) instead of creating one")
	fs.BoolVar(&o.private, "private", false, "create the GitHub repo private (image-updater then needs a registry credential)")
	fs.BoolVar(&o.localOnly, "local-only", false, "generate files only; create nothing on GitHub")
	fs.BoolVar(&o.remoteOnly, "remote-only", false, "create the GitHub repo only; generate no files")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print what would happen and stop")
	fs.BoolVar(&o.yes, "yes", false, "skip the confirmation prompt")

	name, rest := splitPositional(args)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	o.name = name
	if o.name == "" {
		return fmt.Errorf("usage: homelabctl init <name> [--runtime %s]", strings.Join(runtime.Names(), "|"))
	}
	if o.localOnly && o.remoteOnly {
		return fmt.Errorf("--local-only and --remote-only are mutually exclusive")
	}

	r, err := runtime.Get(o.runtimeID)
	if err != nil {
		return err
	}

	c, err := buildConfig(o)
	if err != nil {
		return err
	}

	if err := confirm(o, c); err != nil {
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
		dir = o.parentRepo
	}
	if o.remoteOnly {
		printNext(o, c, dir)
		return nil
	}

	target := dir
	if o.parentRepo != "" {
		target = filepath.Join(dir, "services", o.name)
	}
	if err := setupLocal(o, c, r, target); err != nil {
		return fmt.Errorf("local setup: %w", err)
	}
	printNext(o, c, target)
	return nil
}

func buildConfig(o initOpts) (*config.Config, error) {
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
	}
	if o.host != "" {
		c.Ingress = &config.Ingress{Host: o.host, Public: o.public}
	}
	return c, c.Validate()
}

// confirm prints exactly what will be created before touching anything
// remote, and defaults to no.
func confirm(o initOpts, c *config.Config) error {
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
		fmt.Printf("  %s source, Dockerfile and CI\n", o.runtimeID)
		fmt.Printf("  deploy/ manifests for namespace %s\n", c.Namespace)
	}
	fmt.Printf("  image %s\n", c.Image.Repository)
	if c.Ingress != nil {
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
	p := runtime.Params{
		Name:   o.name,
		Module: fmt.Sprintf("github.com/%s/%s", o.owner, o.name),
		Port:   o.port,
	}

	var written, skipped []string
	put := func(path, body string, mode uint32) error {
		full := filepath.Join(dir, path)
		if _, err := os.Stat(full); err == nil {
			skipped = append(skipped, path)
			return nil
		}
		if err := writeFile(full, body, mode); err != nil {
			return err
		}
		written = append(written, path)
		return nil
	}

	for _, f := range r.Files(p) {
		if err := put(f.Path, f.Body, f.Mode); err != nil {
			return err
		}
	}
	if err := put("Dockerfile", r.Dockerfile(p), 0); err != nil {
		return err
	}
	if err := put("homelab.yaml", configYAML(c), 0); err != nil {
		return err
	}
	// Manifests are generated rather than copied, so they reflect current
	// conventions instead of whatever the template looked like the day the
	// service was created. `render` regenerates them later.
	for _, out := range render.All(c, c.Image.Repository+":latest") {
		if err := put(filepath.Join("deploy", out.Path), out.Body, 0); err != nil {
			return err
		}
	}
	wfPath := ".github/workflows/build.yaml"
	if o.parentRepo != "" {
		// One workflow per service in a monorepo, path-filtered so a push
		// only rebuilds what changed.
		wfPath = filepath.Join("..", "..", ".github", "workflows", o.name+".yaml")
	}
	if err := put(wfPath, workflow(r, p, o), 0); err != nil {
		return err
	}

	appPath := filepath.Join(dir, "deploy", "_argocd-application.yaml")
	appRepo := "https://github.com/" + o.owner + "/homelab"
	if err := writeFile(appPath, render.Application(c, appRepo, o.name), 0); err != nil {
		return err
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

func printNext(o initOpts, c *config.Config, dir string) {
	fmt.Println()
	fmt.Println("what's next:")
	if !o.remoteOnly {
		fmt.Printf("  - review and commit in %s\n", dir)
		fmt.Println("  - push to main; CI builds and pushes the image")
	}
	fmt.Printf("  - copy %s/deploy/*.yaml (except _argocd-application.yaml)\n", dir)
	fmt.Printf("    into the homelab repo as %s/\n", o.name)
	fmt.Printf("  - copy %s/deploy/_argocd-application.yaml into\n", dir)
	fmt.Printf("    homelab app-of-apps/apps/%s.yaml\n", o.name)
	fmt.Println()
	fmt.Println("argocd-image-updater then deploys every push. no homelab")
	fmt.Println("credential is needed in the service repo.")
	if o.private {
		fmt.Println()
		fmt.Println("because the package is private, give image-updater a registry")
		fmt.Println("credential or it will fail with 'unauthorized'.")
	}
}

// splitPositional pulls the first bare argument out of args. Go's flag
// package stops at the first non-flag, so without this `init foo --host x`
// would silently ignore --host.
func splitPositional(args []string) (string, []string) {
	valueFlags := map[string]bool{
		"--runtime": true, "--team": true, "--host": true, "--port": true,
		"--owner": true, "--parent-repo": true,
		"-runtime": true, "-team": true, "-host": true, "-port": true,
		"-owner": true, "-parent-repo": true,
	}
	var name string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if valueFlags[a] && i+1 < len(args) {
				i++
				rest = append(rest, args[i])
			}
			continue
		}
		if name == "" {
			name = a
			continue
		}
		rest = append(rest, a)
	}
	return name, rest
}

func writeFile(path, body string, mode uint32) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	return os.WriteFile(path, []byte(body), os.FileMode(mode))
}

func configYAML(c *config.Config) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# The single source of truth for this service. `+"`homelabctl render`"+`
# regenerates every manifest from it, so change things here rather than
# editing deploy/ by hand.
name: %s
team: %s
runtime: %s
port: %d
image:
  repository: %s
`, c.Name, c.Team, c.Runtime, c.Port, c.Image.Repository)
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

func workflow(r runtime.Runtime, p runtime.Params, o initOpts) string {
	trigger := `on:
  push:
    branches: [main]
  pull_request:
  workflow_dispatch:
`
	checkout := "      - uses: actions/checkout@v4\n"
	workdir := ""
	images := "ghcr.io/${{ github.repository }}"
	if o.parentRepo != "" {
		// Path filter so a push rebuilds only the service that changed.
		trigger = fmt.Sprintf(`on:
  push:
    branches: [main]
    paths: ['services/%s/**', '.github/workflows/%s.yaml']
  pull_request:
    paths: ['services/%s/**']
  workflow_dispatch:
`, p.Name, p.Name, p.Name)
		workdir = fmt.Sprintf(`
    defaults:
      run:
        working-directory: services/%s
`, p.Name)
		images = fmt.Sprintf("ghcr.io/%s/%s-%s", o.owner, o.parentRepo, p.Name)
	}

	ctx := "."
	if o.parentRepo != "" {
		ctx = "services/" + p.Name
	}

	return `# Builds and pushes the image. Deployment happens in-cluster:
# argocd-image-updater watches the registry and commits the new digest to
# the homelab repo itself, so this workflow needs no homelab credential -
# GITHUB_TOKEN is issued per run and can only push packages.
name: build ` + p.Name + `

` + trigger + `
jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write` + workdir + `
    steps:
` + checkout + `
` + r.BuildSteps(p) + `
      - name: Check deploy manifests
        run: |
          curl -fsSL https://github.com/ChristopherScot/ci-scripts/releases/latest/download/homelabctl_linux_amd64.tar.gz | tar -xz
          ./homelabctl check deploy

      - uses: docker/login-action@v3
        if: github.event_name != 'pull_request'
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - id: meta
        uses: docker/metadata-action@v5
        with:
          images: ` + images + `
          # Full SHA: an abbreviated one is not a registry tag and gives
          # ImagePullBackOff.
          tags: |
            type=raw,value=latest,enable={{is_default_branch}}
            type=sha,format=long

      - uses: docker/build-push-action@v6
        with:
          context: ` + ctx + `
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
`
}
