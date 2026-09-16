package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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

// renderOpts is what `render` was asked to do. A struct rather than eight
// positional parameters, matching initOpts: four consecutive strings at a
// call site are indistinguishable from each other, and the compiler
// cannot catch a transposition.
type renderOpts struct {
	cfgPath  string
	imageRef string

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
		Use:   "render [config.yaml] <image-ref>",
		Short: "render manifests from a config",
		Long: "Render manifests. Used by CI and to regenerate after a convention\n" +
			"change. image-ref must be a full SHA or digest - an abbreviated SHA\n" +
			"is not a registry tag and yields ImagePullBackOff.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(_ *cobra.Command, args []string) error {
			o.cfgPath, o.imageRef = defaultConfigPath, args[0]
			if len(args) == 2 {
				o.cfgPath, o.imageRef = args[0], args[1]
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
	if shortSHA.MatchString(o.imageRef) {
		return fmt.Errorf("%q: %w", o.imageRef, errAbbreviatedSHA)
	}

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
				"fix config.yaml, or pass --o.force if this is intended")
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
	outs, err := render.All(c, o.imageRef)
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
