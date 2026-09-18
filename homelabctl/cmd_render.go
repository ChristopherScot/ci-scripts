package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
)

// Abbreviated SHAs are the mistake this catches: they look like valid tags
// and fail only at pull time, as ImagePullBackOff with the app still
// showing Synced.

// renderOpts is what `render` was asked to do. A struct rather than eight
// positional parameters, matching initOpts: four consecutive strings at a
// call site are indistinguishable from each other, and the compiler
// cannot catch a transposition.
type renderOpts struct {
	cfgPath string

	out     string // directory to write manifests into
	appOut  string // where to also write the Argo Application, if anywhere
	repoURL string // repo the Application syncs from
	appPath string // path within that repo

	dryRun bool
	force  bool
}

func renderCmd() *cobra.Command {
	var o renderOpts
	cmd := &cobra.Command{
		Use:   "render [config.yaml]",
		Short: "render manifests from a config",
		Long: "Render every manifest from config.yaml. Run it after changing the\n" +
			"config; the manifests are derived from it and nothing else.\n\n" +
			"It takes no image reference. argocd-image-updater owns the running\n" +
			"version: it resolves :latest to a digest and writes that into\n" +
			"kustomization.yaml, so a rendered manifest always names :latest.\n" +
			"Rendering is therefore deterministic and CI can diff its output.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o.cfgPath = defaultConfigPath
			if len(args) == 1 {
				o.cfgPath = args[0]
			}
			return runRender(o)
		},
	}
	cmd.Flags().StringVar(&o.out, "out", ".", "directory to write manifests into")
	cmd.Flags().StringVar(&o.appOut, "app-out", "", "also write the Argo Application here")
	cmd.Flags().StringVar(&o.repoURL, "repo-url", "https://github.com/ChristopherScot/homelab", "repo the Application syncs from")
	cmd.Flags().StringVar(&o.appPath, "app-path", "", "path within that repo (default: service name)")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "check against the running service and write nothing")
	cmd.Flags().BoolVar(&o.force, "force", false, "write even if the change would break the running service")
	return cmd
}

func runRender(o renderOpts) error {
	c, err := config.Load(o.cfgPath)
	if err != nil {
		return err
	}

	if c.Image.Repository == "" {
		return fmt.Errorf("image.repository is required to render")
	}

	// Compare against what is actually running before touching anything.
	// Regenerating a live service can silently drop env vars it declares
	// nowhere, or rename an identity Vault still authorizes by the old
	// name - both invisible until the pod crashloops.
	findings, err := preflight(c)
	switch {
	case err != nil:
		fmt.Println("preflight skipped:", err)
	case len(findings) > 0:
		if reportPreflight(findings) && !o.force {
			return fmt.Errorf("\nrefusing to render: the above would break the running service.\n" +
				"fix config.yaml, or pass --force if this is intended")
		}
	case !o.dryRun:
		// nothing to report
	default:
		fmt.Println("preflight: no drift from the running service")
	}
	if o.dryRun {
		fmt.Println("dry run: nothing written")
		return nil
	}

	// An override naming a file that is never generated is a typo, and
	// silently dropping it leaves the author believing it applied.
	outs, err := render.All(c)
	if err != nil {
		return err
	}
	if unknown := render.UnknownOverrides(c, outs); len(unknown) > 0 {
		return fmt.Errorf("overrides name file(s) this service does not generate: %s",
			strings.Join(unknown, ", "))
	}

	dir := filepath.Join(o.out, c.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, o := range outs {
		p := filepath.Join(dir, o.Path)
		if err := os.WriteFile(p, []byte(o.Body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		fmt.Println("wrote", p)
	}

	if o.appOut != "" {
		p := o.appPath
		if p == "" {
			p = c.Name
		}
		if err := os.MkdirAll(filepath.Dir(o.appOut), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(o.appOut, []byte(render.Application(c, o.repoURL, p)), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", o.appOut)
	}
	return nil
}
