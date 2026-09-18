package main

import (
	"errors"
	"fmt"
	"io"
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

// isYAML reports whether a filename is a manifest this should read.
//
// Both spellings: a file named db.yml is copied into deploy/ and listed
// in resources: exactly like db.yaml, and checking only one extension
// left the other entirely unvalidated.
func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

// checkResources cross-references kustomization.yaml against the
// directory, in both directions.
//
// This is the check that catches a whole class rather than one bug.
// Argo applies exactly what `resources:` lists, so the two ways for
// that list to be wrong are both silent:
//
//   - a manifest on disk that nothing lists is never applied. A user
//     adds a PVC, commits it, sees it in git, and it is not deployed.
//   - a listed file that is not on disk fails the whole sync, taking
//     the Deployment and Service with it.
//
// Three separate bugs produced the first shape - a manifest whose name
// collided with a generated file, a stale kustomization.yaml kept by
// `init` on a re-run, and a .yml file check never opened - and each
// would have been caught here without knowing anything about how it
// arose.
//
// Duplicates are reported too: a name listed twice makes kustomize
// refuse the directory outright.
func checkResources(dir, kustomization string, add func(string, ...any)) {
	var k struct {
		Resources []string `yaml:"resources"`
	}
	if err := yaml.Unmarshal([]byte(kustomization), &k); err != nil {
		return // reported as invalid YAML by the caller's own loop
	}

	listed := map[string]int{}
	for _, r := range k.Resources {
		listed[r]++
	}
	for name, n := range listed {
		if n > 1 {
			add("kustomization.yaml lists %s %d times; kustomize refuses a duplicate resource", name, n)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			add("kustomization.yaml lists %s, which is not in %s - the whole app fails to sync", name, dir)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) || e.Name() == "kustomization.yaml" {
			continue
		}
		if listed[e.Name()] == 0 {
			add("%s is in %s but not listed in kustomization.yaml, so Argo never applies it",
				e.Name(), dir)
		}
	}
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
	if err == nil {
		checkResources(dir, string(kb), add)
	}
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
		// .yml as well as .yaml. A manifest named db.yml - the extension
		// this repo uses for openapi.yml - was copied into deploy/ and
		// listed in resources:, and check never opened it. A CHANGEME
		// placeholder in db.yml passed where the same file as db.yaml
		// was correctly flagged.
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// Parse it: regexes over lines cannot tell valid YAML from
		// garbage, and shipping garbage is the failure this guards.
		// Decoded, not split on a literal "\n---\n": CRLF makes the
		// separator "---\r" and a trailing space makes it "--- ", and
		// yaml.Unmarshal then reads only the FIRST document of the
		// stream and returns nil. Everything after it went unchecked.
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		for i := 1; ; i++ {
			var doc any
			err := dec.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				add("%s: document %d is not valid YAML: %v", p, i, err)
				break
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
