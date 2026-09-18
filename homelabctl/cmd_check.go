package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var changeme = regexp.MustCompile(`CHANGEME`)

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

// manifestDir finds where the manifests actually are.
//
// `render --out deploy` writes to deploy/<service>/, so that the
// directory name matches the path the app occupies in the homelab repo
// and the copy is a plain `cp -r`. check used to look in deploy/ itself
// and reported a missing kustomization.yaml for every service - a
// failure whose message named a real hazard that was not happening.
//
// One subdirectory holding a kustomization.yaml is that layout; anything
// else is the flat one, and the caller's directory stands.
func manifestDir(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dir
	}
	var found string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "kustomization.yaml")); err != nil {
			continue
		}
		if found != "" {
			// Several: ambiguous, so check what the caller named rather
			// than guessing which service is the subject.
			return dir
		}
		found = filepath.Join(dir, e.Name())
	}
	if found != "" {
		return found
	}
	return dir
}

// checkESOVersion compares the External Secrets API these manifests
// declare against what the cluster actually serves.
//
// Every service shares one apiVersion, so an ESO upgrade that drops it
// breaks all of them at once - and quietly: the ExternalSecret stops
// refreshing while the Secret it already created lingers, so pods keep
// running on credentials nobody is renewing. This turns that into a
// message before the manifests are applied.
//
// A cluster that cannot be reached is not a failure. This runs in CI,
// which has no kubeconfig, and a check that cannot run should not be
// the reason a build goes red.
func checkESOVersion(add func(string, ...any)) {
	// -o name omits the version, which is the thing being compared.
	// The APIVERSION column carries it.
	out, err := exec.Command("kubectl", "api-resources",
		"--api-group=external-secrets.io",
		"--no-headers", "-o", "wide").Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return // no cluster, or no ESO in it: nothing to compare against
	}
	// Field-wise, not Contains: "external-secrets.io/v1" is a prefix of
	// "external-secrets.io/v1beta1", so a substring test reports a match
	// for a version the cluster does not serve.
	for _, line := range strings.Split(string(out), "\n") {
		for _, f := range strings.Fields(line) {
			if f == render.ESOAPIVersion {
				return
			}
		}
	}
	add("the cluster does not serve %s, which every rendered ExternalSecret declares.\n"+
		"    upgrading External Secrets means changing render.ESOAPIVersion and "+
		"re-rendering every service", render.ESOAPIVersion)
}

func runCheck(dir string) error {

	var problems []string
	add := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }

	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no %s/ directory", dir)
	}
	dir = manifestDir(dir)

	checkESOVersion(add)

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

	// The spec lives beside the service, not in deploy/.
	checkSpec(".", add)

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
		// Parse it: regexes over lines cannot tell valid YAML from
		// garbage, and shipping garbage is the failure this guards.
		var doc any
		for i, chunk := range strings.Split(string(b), "\n---\n") {
			if strings.TrimSpace(chunk) == "" {
				continue
			}
			if err := yaml.Unmarshal([]byte(chunk), &doc); err != nil {
				add("%s: document %d is not valid YAML: %v", p, i+1, err)
			}
		}
		for i, line := range strings.Split(string(b), "\n") {
			if changeme.MatchString(line) {
				add("%s:%d still contains a CHANGEME placeholder", p, i+1)
			}
			if ref, ok := config.ImageRefInLine(line); ok && config.IsAbbreviatedSHA(ref) {
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
