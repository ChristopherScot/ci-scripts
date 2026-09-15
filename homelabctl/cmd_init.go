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
	localOnly  bool
	remoteOnly bool
	dryRun     bool
	yes        bool
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
	f.IntVar(&o.port, "port", 3000, "port the service listens on")
	f.StringVar(&o.owner, "owner", "christopherscot", "GitHub owner")
	f.StringVar(&o.parentRepo, "parent-repo", "", "add this service to an existing repo (monorepo) instead of creating one")
	f.BoolVar(&o.private, "private", false, "create the GitHub repo private (image-updater then needs a registry credential)")
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

	isCLI := r.Kind() == runtime.KindCLI
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
	p := runtime.Params{
		Name:   o.name,
		Module: fmt.Sprintf("github.com/%s/%s", o.owner, o.name),
		Owner:  o.owner,
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
	// A CLI is not containerised or deployed: no Dockerfile, no manifests,
	// no Argo Application. It builds cross-platform binaries and publishes
	// them as release assets instead.
	if r.Kind() == runtime.KindService {
		if err := put("Dockerfile", r.Dockerfile(p), 0); err != nil {
			return err
		}
		if err := put("homelab.yaml", configYAML(c), 0); err != nil {
			return err
		}
		// Manifests are generated rather than copied, so they reflect
		// current conventions instead of whatever the template looked like
		// the day the service was created. `render` regenerates them later.
		for _, out := range render.All(c, c.Image.Repository+":latest") {
			if err := put(filepath.Join("deploy", out.Path), out.Body, 0); err != nil {
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
	if err := put(wfPath, workflow(r, p, o), 0); err != nil {
		return err
	}

	if r.Kind() == runtime.KindService {
		appPath := filepath.Join(dir, "deploy", "_argocd-application.yaml")
		appRepo := "https://github.com/" + o.owner + "/homelab"
		if err := writeFile(appPath, render.Application(c, appRepo, o.name), 0); err != nil {
			return err
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

// cliWorkflow cross-compiles and publishes release assets, named to match
// what the generated update command looks for. Triggered by a change to
// VERSION rather than every push, so a release is deliberate.
func cliWorkflow(r runtime.Runtime, p runtime.Params) string {
	return `name: release ` + p.Name + `

on:
  push:
    branches: [main]
    paths: [VERSION]
  workflow_dispatch:

permissions:
  contents: write

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
` + r.BuildSteps(p) + `
  release:
    needs: test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: Read version
        id: v
        run: echo "version=$(head -n1 VERSION)" >> $GITHUB_OUTPUT

      # Asset names must match what ` + p.Name + ` update looks for:
      # ` + p.Name + `_<goos>_<goarch>.tar.gz containing the bare binary.
      - name: Cross-compile
        run: |
          VERSION="${{ steps.v.outputs.version }}"
          LDFLAGS="-s -w -X main.Version=$VERSION"
          for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
            GOOS="${target%/*}"; GOARCH="${target#*/}"
            mkdir -p "dist/$GOOS/$GOARCH"
            GOOS=$GOOS GOARCH=$GOARCH go build -ldflags "$LDFLAGS"               -o "dist/$GOOS/$GOARCH/` + p.Name + `" .
            tar -czf "` + p.Name + `_${GOOS}_${GOARCH}.tar.gz"               -C "dist/$GOOS/$GOARCH" ` + p.Name + `
          done

      - uses: softprops/action-gh-release@v2
        with:
          tag_name: ${{ steps.v.outputs.version }}
          body_path: VERSION
          files: ` + p.Name + `_*.tar.gz
`
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
	if r.Kind() == runtime.KindCLI {
		return cliWorkflow(r, p)
	}
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
