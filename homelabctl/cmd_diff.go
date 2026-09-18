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

// diff shows what rendering would change, against the manifests as
// committed in THIS repo.
//
// It used to compare against the GitOps repo, back when a service's
// manifests were copied there by hand. They are not: Argo reads each
// service's deploy/ directory from the service's own repo, and the
// GitOps repo holds only argocd.json. Comparing against it meant every
// manifest came back "(new)" for a service that was deployed and
// running - four false differences out of five outputs on pokedex - and
// the empty case printed "up to date" about a directory holding one
// JSON file, which reads as a statement about the deployment.
//
// Comparing against deploy/ here makes it a real check again: the same
// question `render` + `git diff --exit-code` asks in CI, answerable
// before committing.
func diffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "show what rendering would change in deploy/",
		Long: "Renders the config and compares it against the manifests committed\n" +
			"in deploy/, so a change can be reviewed before it is pushed.\n\n" +
			"Argo reads those manifests from this repo, so they are what reaches\n" +
			"the cluster.\n\n" +
			"Exits 1 when they differ, so CI can require them to agree.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path, err := findConfig()
			if err != nil {
				return err
			}
			return runDiff(path)
		},
	}
	return cmd
}

func runDiff(cfgPath string) error {
	c, err := config.Load(cfgPath)
	if err != nil {
		return err
	}

	// Where render writes, so the two commands cannot disagree about
	// which directory holds this service's manifests.
	dir := filepath.Join(filepath.Dir(cfgPath), "deploy", c.Name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("%s does not exist; run `homelabctl render` first", dir)
	}

	src, err := withManifests(gitSource(cfgPath), c, cfgPath)
	if err != nil {
		return err
	}
	outs, err := render.All(c, src)
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

	// No special case for the Argo Application any more: the service
	// publishes argocd.json, an ApplicationSet in the homelab repo
	// templates the Application from it, and that file is compared by the
	// loop above like every other output. What used to be the most
	// consequential file to get wrong - and the one most easily forgotten
	// in a hand copy - is now ordinary.

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
