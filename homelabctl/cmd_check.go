package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var (
	shortTagInManifest = regexp.MustCompile(`image:\s*\S+:[0-9a-f]{7,12}\s*$`)
	changeme           = regexp.MustCompile(`CHANGEME`)
)

// runCheck turns the deploy failures that are otherwise silent into a red
// build. Each of these presented as Synced/Healthy with nothing shipping.
func checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check [dir]",
		Short: "fail on deploy misconfigurations that are otherwise silent",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "deploy"
			if len(args) > 0 {
				dir = args[0]
			}
			return runCheck(dir)
		},
	}
}

func runCheck(dir string) error {

	var problems []string
	add := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no %s/ directory", dir)
	}

	kPath := filepath.Join(dir, "kustomization.yaml")
	kb, err := os.ReadFile(kPath)
	switch {
	case err != nil:
		// The failure that cost the most: without it, image-updater skips
		// the app entirely and says so only in its own log.
		add("%s is missing - argocd-image-updater will silently skip this app and it will stay on a stale image", kPath)
	case !strings.Contains(string(kb), "images:"):
		add("%s has no `images:` block for image-updater to write into", kPath)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if changeme.MatchString(line) {
				add("%s:%d still contains a CHANGEME placeholder", p, i+1)
			}
			if shortTagInManifest.MatchString(line) && !strings.Contains(line, "@sha256:") {
				add("%s:%d image tag looks like an abbreviated SHA; registry tags are full 40-char SHAs", p, i+1)
			}
		}
	}

	// Return the problems rather than printing them and returning a count:
	// the caller prints once, and the error carries the actual content.
	if len(problems) > 0 {
		return fmt.Errorf("%d problem(s) in %s:\n  - %s",
			len(problems), dir, strings.Join(problems, "\n  - "))
	}
	fmt.Println("deploy manifests OK")
	return nil
}
