package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	shortTagInManifest = regexp.MustCompile(`image:\s*\S+:[0-9a-f]{7,12}\s*$`)
	changeme           = regexp.MustCompile(`CHANGEME`)
)

// runCheck turns the deploy failures that are otherwise silent into a red
// build. Each of these presented as Synced/Healthy with nothing shipping.
func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "deploy"
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

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

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "ERROR: "+p)
		}
		return fmt.Errorf("%d problem(s) found", len(problems))
	}
	fmt.Println("deploy manifests OK")
	return nil
}
