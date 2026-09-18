package main

import (
	"fmt"
	"os"
	"path/filepath"

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

	out string // directory to write manifests into

	dryRun bool
	force  bool
}

func renderCmd() *cobra.Command {
	var o renderOpts
	cmd := &cobra.Command{
		Use:   "render",
		Short: "render manifests from a config",
		Long: "Render every manifest from config.yaml. Run it after changing the\n" +
			"config; the manifests are derived from it and nothing else.\n\n" +
			"It takes no image reference. argocd-image-updater owns the running\n" +
			"version: it resolves :latest to a digest and writes that into\n" +
			"kustomization.yaml, so a rendered manifest always names :latest.\n" +
			"Rendering is therefore deterministic and CI can diff its output.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var err error
			if o.cfgPath, err = findConfig(); err != nil {
				return err
			}
			return runRender(o)
		},
	}
	// Empty, not ".": the default is deploy/ beside config.yaml, which
	// is where init writes and where the files already are. Defaulting
	// to the working directory wrote them wherever you happened to
	// stand; defaulting to the config's directory wrote them one level
	// above the ones it should have replaced, leaving the originals
	// stale while reporting success.
	cmd.Flags().StringVar(&o.out, "out", "", "directory to write manifests into (default: deploy/ beside config.yaml)")
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
	outs, err := render.All(c, gitSource(o.cfgPath))
	if err != nil {
		return err
	}
	out := o.out
	if out == "" {
		out = filepath.Join(filepath.Dir(o.cfgPath), "deploy")
	}
	dir := filepath.Join(out, c.Name)
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

	return nil
}
