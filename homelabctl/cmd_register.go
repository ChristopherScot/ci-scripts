package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
)

// register tells Argo about a service, by opening a PR on the GitOps
// repo that adds its argocd.json.
//
// Its own command rather than `render --register`, which it used to be.
// That flag made 30% of runRender a block that wrote no files and made
// one network call to a different repository - three unrelated effects
// (write manifests locally, produce a registration record, open a
// cross-repo PR) sharing nothing but an in-memory slice that two lines
// recompute.
//
// The flag combinations are what settled it. `--dry-run --register` was
// accepted and silently did nothing, because --dry-run returns before
// render.All is called: a rehearsal of registration that rehearsed
// nothing and exited 0. `--force` gated registration on preflight,
// which inspects the live Deployment - unrelated to registering.
// `--out` wrote manifests somewhere else while the PR still carried the
// git-derived path. Separate commands delete all three rather than
// documenting them.
//
// argocd.json is still rendered by render.All, deliberately: it is
// enumerated by kustomization.yaml's resourceNames, so moving it to
// another package would turn a same-package call into a cross-package
// dependency. Keeping it there is what makes this command two lines.
func registerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register",
		Short: "tell Argo about this service, by PR on the GitOps repo",
		Long: "Opens a PR adding this service's argocd.json to the GitOps repo,\n" +
			"which is what makes an ApplicationSet generate an Application for\n" +
			"it.\n\n" +
			"Idempotent: re-running when the entry is already current opens\n" +
			"nothing and says so.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := findConfig()
			if err != nil {
				return err
			}
			c, err := config.Load(path)
			if err != nil {
				return err
			}
			return runRegister(cmd.OutOrStdout(), c, path)
		},
	}
}

func runRegister(w io.Writer, c *config.Config, cfgPath string) error {
	src, err := withManifests(gitSource(cfgPath), c, cfgPath)
	if err != nil {
		return err
	}
	// Checked BEFORE rendering, on the typed field rather than on the
	// JSON afterwards.
	//
	// This was `strings.Contains(entry, "repoURL")` against the rendered
	// bytes - reverse-engineering from serialized text a fact that
	// Source carries two calls earlier. A service whose manifests:
	// contained a file with the literal text "repoURL" would have
	// matched the wrong document and registered with an empty repoURL,
	// which the ApplicationSet interpolates straight into a live
	// Application that then fetches nothing.
	if src.RepoURL == "" {
		return fmt.Errorf("this service has no git remote yet, so Argo would have nowhere to fetch it from.\n" +
			"  commit and push it first, then run `homelabctl register`")
	}

	entry, err := render.AppEntry(c, src)
	if err != nil {
		return err
	}
	url, err := openGitOpsPR(c.Name, entry)
	if err != nil {
		return err
	}
	if url == "" {
		fmt.Fprintf(w, "%s is already registered with Argo\n", c.Name)
		return nil
	}
	fmt.Fprintf(w, "opened %s\n  merge it and Argo starts deploying %s\n", url, c.Name)
	return nil
}
