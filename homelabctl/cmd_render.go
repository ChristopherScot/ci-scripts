package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
)

// Abbreviated SHAs are the mistake this catches: they look like valid tags
// and fail only at pull time, as ImagePullBackOff with the app still
// showing Synced.
var shortSHA = regexp.MustCompile(`:[0-9a-f]{7,12}$`)

// errAbbreviatedSHA is a sentinel so callers and tests can recognise this
// without matching on the message text.
var errAbbreviatedSHA = errors.New("image ref ends in an abbreviated SHA; registry tags are full 40-char SHAs or digests")

func renderCmd() *cobra.Command {
	var out, appOut, repoURL, appPath string
	var dryRun, force bool
	cmd := &cobra.Command{
		Use:   "render <config.yaml> <image-ref>",
		Short: "render manifests from a config",
		Long: "Render manifests. Used by CI and to regenerate after a convention\n" +
			"change. image-ref must be a full SHA or digest - an abbreviated SHA\n" +
			"is not a registry tag and yields ImagePullBackOff.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return runRender(args[0], args[1], out, appOut, repoURL, appPath, dryRun, force)
		},
	}
	cmd.Flags().StringVar(&out, "out", ".", "directory to write manifests into")
	cmd.Flags().StringVar(&appOut, "app-out", "", "also write the Argo Application here")
	cmd.Flags().StringVar(&repoURL, "repo-url", "https://github.com/ChristopherScot/homelab", "repo the Application syncs from")
	cmd.Flags().StringVar(&appPath, "app-path", "", "path within that repo (default: service name)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check against the running service and write nothing")
	cmd.Flags().BoolVar(&force, "force", false, "write even if the change would break the running service")
	return cmd
}

func runRender(cfgPath, imageRef, out, appOut, repoURL, appPath string, dryRun, force bool) error {

	if shortSHA.MatchString(imageRef) {
		return fmt.Errorf("%q: %w", imageRef, errAbbreviatedSHA)
	}

	c, err := config.Load(cfgPath)
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
		if reportPreflight(findings) && !force {
			return fmt.Errorf("\nrefusing to render: the above would break the running service.\n" +
				"fix config.yaml, or pass --force if this is intended")
		}
	case !dryRun:
		// nothing to report
	default:
		fmt.Println("preflight: no drift from the running service")
	}
	if dryRun {
		fmt.Println("dry run: nothing written")
		return nil
	}

	dir := filepath.Join(out, c.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, o := range render.All(c, imageRef) {
		p := filepath.Join(dir, o.Path)
		if err := os.WriteFile(p, []byte(o.Body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		fmt.Println("wrote", p)
	}

	if appOut != "" {
		p := appPath
		if p == "" {
			p = c.Name
		}
		if err := os.MkdirAll(filepath.Dir(appOut), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(appOut, []byte(render.Application(c, repoURL, p)), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", appOut)
	}
	return nil
}
