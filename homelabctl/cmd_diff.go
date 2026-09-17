package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/textdiff"
	"github.com/spf13/cobra"
)

// diff closes the one gap in the workflow that had no review step: what
// this tool renders is copied into the GitOps repo by hand, so the change
// that actually reaches the cluster was never shown to anyone before it
// landed. This prints it.
func diffCmd() *cobra.Command {
	var against, imageRef, repoURL string
	cmd := &cobra.Command{
		Use:   "diff [config.yaml]",
		Short: "show what rendering would change in the GitOps repo",
		Long: "Renders the config and compares it against the committed manifests,\n" +
			"so a change can be reviewed before it reaches the cluster.\n\n" +
			"Exits 1 when they differ, so CI can require them to agree.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := defaultConfigPath
			if len(args) == 1 {
				path = args[0]
			}
			return runDiff(path, against, imageRef, repoURL)
		},
	}
	cmd.Flags().StringVar(&against, "against", "", "GitOps repo checkout to compare with (default: $HOMELAB_REPO or ~/homelab)")
	// The image is not what a review is about, and rendering needs one, so
	// default to the tag the deployed manifest already carries.
	cmd.Flags().StringVar(&imageRef, "image", "", "image ref to render with (image refs are not compared)")
	cmd.Flags().StringVar(&repoURL, "repo-url", "https://github.com/ChristopherScot/homelab", "repo the Application syncs from")
	return cmd
}

func runDiff(cfgPath, against, imageRef, repoURL string) error {
	c, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if against == "" {
		against = os.Getenv("HOMELAB_REPO")
	}
	if against == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		against = filepath.Join(home, "homelab")
	}

	dir := filepath.Join(against, c.AppName())
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("%s does not exist; this service is not in %s yet", dir, against)
	}

	if imageRef == "" {
		imageRef = c.Image.Repository + ":latest"
	}
	outs, err := render.All(c, imageRef)
	if err != nil {
		return err
	}

	var changed []string
	for _, o := range outs {
		live, err := os.ReadFile(filepath.Join(dir, o.Path))
		switch {
		case os.IsNotExist(err):
			fmt.Printf("\n--- %s (new)\n", o.Path)
			printDiff("", o.Body)
			changed = append(changed, o.Path)
			continue
		case err != nil:
			return err
		}
		if normalise(string(live)) == normalise(o.Body) {
			continue
		}
		fmt.Printf("\n--- %s\n", o.Path)
		printDiff(string(live), o.Body)
		changed = append(changed, o.Path)
	}

	// The Argo Application lives in the app-of-apps directory rather than
	// the service directory, and it is the file that decides whether the
	// service is synced at all - the most consequential one to get wrong,
	// and the one most easily forgotten in a hand copy.
	appPath := filepath.Join(against, "app-of-apps", "apps", c.AppName()+".yaml")
	want := render.Application(c, repoURL, c.AppName())
	switch live, err := os.ReadFile(appPath); {
	case os.IsNotExist(err):
		fmt.Printf("\n--- app-of-apps/apps/%s.yaml (missing; Argo is not syncing this service)\n", c.AppName())
		printDiff("", want)
		changed = append(changed, "app-of-apps/apps/"+c.AppName()+".yaml")
	case err != nil:
		return err
	case normalise(string(live)) != normalise(want):
		fmt.Printf("\n--- app-of-apps/apps/%s.yaml\n", c.AppName())
		printDiff(string(live), want)
		changed = append(changed, "app-of-apps/apps/"+c.AppName()+".yaml")
	}

	// A file in the repo that render no longer produces is drift too - it
	// will keep being applied by Argo and nothing generates it.
	for _, extra := range unrenderedFiles(dir, outs) {
		fmt.Printf("\n--- %s (in the repo, not generated)\n", extra)
		changed = append(changed, extra)
	}

	if len(changed) == 0 {
		fmt.Printf("%s is up to date with %s\n", dir, cfgPath)
		return nil
	}
	sort.Strings(changed)
	return fmt.Errorf("%d file(s) differ: %s", len(changed), strings.Join(changed, ", "))
}

// maskImage blanks the image reference on both sides of the comparison.
//
// argocd-image-updater rewrites the deployed digest on every build, so the
// image is almost never what the repo was rendered with - and it is not
// what a config review is about. Comparing it would make every service
// report a diff forever, which trains people to ignore the output.
//
// The KEY is masked along with the value: image-updater pins by `digest:`
// where render emits `newTag:`, so comparing the key would report a diff
// on every service too.
var imageLine = regexp.MustCompile(`(?m)^(\s*)(?:-\s+)?(?:image|digest|newTag):.*$`)

func maskImage(s string) string {
	return imageLine.ReplaceAllString(s, "${1}image: <managed by image-updater>")
}

// unrenderedFiles lists YAML in the service directory that render does
// not produce. Argo applies whatever is in the directory, so a file
// nothing generates keeps reaching the cluster with no config behind it.
func unrenderedFiles(dir string, outs []render.Output) []string {
	generated := map[string]bool{}
	for _, o := range outs {
		generated[o.Path] = true
	}
	// Written by the tool but owned by the GitOps repo, not by render.
	generated["_argocd-application.yaml"] = true

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var extra []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") || generated[e.Name()] {
			continue
		}
		extra = append(extra, e.Name())
	}
	sort.Strings(extra)
	return extra
}

// normalise ignores comment, blank-line and image churn, so a diff shows
// changes of substance rather than noise.
func normalise(s string) string {
	s = maskImage(s)
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		keep = append(keep, strings.TrimRight(line, " "))
	}
	return strings.Join(keep, "\n")
}

// printDiff prints a unified-style diff of the two bodies.
//
// A presence-based comparison is not enough here: a line can appear in
// both versions at different COUNTS - the common case being a value
// rendered twice - and a diff that reports a file as changed while
// printing nothing is worse than printing no diff at all. So this does a
// real longest-common-subsequence walk, which gets duplicates right.
func printDiff(oldBody, newBody string) {
	old := strings.Split(normalise(oldBody), "\n")
	nw := strings.Split(normalise(newBody), "\n")
	for _, h := range textdiff.Hunks(old, nw) {
		for _, line := range h {
			fmt.Printf("  %s\n", line)
		}
	}
}
