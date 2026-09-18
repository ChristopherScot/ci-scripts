package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/runtime"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// regen is the one command to run after editing openapi.yml.
//
// Before it existed, a spec change meant remembering `go generate`, then
// `go mod tidy`, then the openapi-typescript invocation with its pinned
// version, then hand-editing the version in two more files - and
// `homelabctl check` existed partly to catch the steps people forgot.
// Running them is cheaper than checking whether they were run.
func regenCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "regen",
		Short: "regenerate clients from the spec and sync their versions",
		Long: "Regenerate everything derived from openapi.yml: the server\n" +
			"interface, both clients, and the versions they report.\n\n" +
			"Run this after editing the spec. With --check it changes nothing\n" +
			"and fails if anything is out of date, which is what CI wants.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			path, err := findConfig()
			if err != nil {
				return err
			}
			return runRegen(path, check)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report what is stale instead of regenerating")
	return cmd
}

func runRegen(cfgPath string, checkOnly bool) error {
	c, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	dir := filepath.Dir(cfgPath)

	// A specless service generates nothing, so there is nothing here to
	// be stale. Saying so beats failing on a missing openapi.yml, which
	// reads like a setup mistake rather than a deliberate choice - and
	// CI runs this on every service.
	if !c.Spec {
		fmt.Println("nothing to regenerate:", c.Name, "is built without a spec (spec: false)")
		return nil
	}

	specVersion, err := specVersion(dir)
	if err != nil {
		return err
	}

	// The version the clients report has to be the spec's, or
	// X-Client-Version tells a server something untrue.
	stale, err := syncVersions(dir, specVersion, checkOnly)
	if err != nil {
		return err
	}

	r, err := runtime.Get(c.Runtime)
	if err != nil {
		return err
	}

	if checkOnly {
		if len(stale) == 0 {
			fmt.Println("clients are up to date with", specVersion)
			return nil
		}
		for _, s := range stale {
			fmt.Println("  -", s)
		}
		return fmt.Errorf("%d file(s) out of date - run `homelabctl regen`", len(stale))
	}

	// Generate and Lock, never Upgrade. Both are deterministic - the
	// same spec and the same manifest give the same output - which is
	// what lets CI run this and fail on a diff. Upgrade belongs to
	// creating a service; running it here would be a red build on any
	// day a dependency published.
	if err := run(dir, r.Generate(artifactParams(c, "", "")), r.Lock()); err != nil {
		return err
	}

	// Rewrite the files the TEMPLATES own, not just the ones ogen writes.
	//
	// regen used to run the generate commands and stop, so a fix to a
	// client template reached new services and never existing ones - the
	// only way to pick it up was to know that `init --overwrite` also
	// regenerates, which is not what the command is called. A latent bad
	// import in the TypeScript client survived a regen this way.
	//
	// Only SpecFiles: everything else the templates write is the
	// author's to edit, and rewriting server.go or config.yaml here
	// would discard their work.
	// The owner, read from go.mod rather than passed in. It is the npm
	// scope the TypeScript client publishes under, so getting it wrong
	// renders "@/name-client" - a package name npm rejects, discovered at
	// publish time rather than here.
	params := artifactParams(c, ownerFromModule(dir), "")
	params.Spec = c.Spec
	rendered := map[string]string{}
	for _, f := range r.Artifacts(params).Files {
		rendered[f.Path] = f.Body
	}
	for _, path := range r.SpecFiles() {
		body, ok := rendered[path]
		if !ok {
			continue
		}
		full := filepath.Join(dir, path)
		if old, err := os.ReadFile(full); err == nil && string(old) == body {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Println("  updated:", path)
	}
	for _, s := range stale {
		fmt.Println("  updated:", s)
	}
	fmt.Println("regenerated from openapi.yml at version", specVersion)
	return nil
}

// specVersion reads info.version, which is the one version a service has.
func specVersion(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "openapi.yml"))
	if err != nil {
		return "", fmt.Errorf("this service has no openapi.yml to regenerate from")
	}
	var spec struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(b, &spec); err != nil {
		return "", fmt.Errorf("parsing openapi.yml: %w", err)
	}
	if spec.Info.Version == "" {
		return "", fmt.Errorf("openapi.yml has no info.version")
	}
	return spec.Info.Version, nil
}

var goClientVersion = regexp.MustCompile(`(ClientVersion\s*=\s*")[^"]*(")`)

// syncVersions rewrites the versions that cannot be derived at runtime,
// and reports what it changed. The TypeScript client is not among them:
// it reads package.json, so there is nothing there to sync.
func syncVersions(dir, version string, checkOnly bool) ([]string, error) {
	var stale []string

	goClient := filepath.Join(dir, "api", "client.go")
	if b, err := os.ReadFile(goClient); err == nil {
		want := goClientVersion.ReplaceAll(b, []byte("${1}"+version+"${2}"))
		if string(want) != string(b) {
			stale = append(stale, "api/client.go")
			if !checkOnly {
				if err := os.WriteFile(goClient, want, 0o644); err != nil {
					return nil, err
				}
			}
		}
	}

	// clients/ts/, not the service root: that is where the generated
	// TypeScript client lives. It used to be at the root, and when it
	// moved this path did not - so os.ReadFile failed silently and the
	// published package advertised whatever version it was created
	// with, regardless of what the spec said.
	pkgRel := filepath.Join("clients", "ts", "package.json")
	pkgPath := filepath.Join(dir, pkgRel)
	if b, err := os.ReadFile(pkgPath); err == nil {
		updated, changed, err := setPackageVersion(b, version)
		if err != nil {
			return nil, err
		}
		if changed {
			stale = append(stale, pkgRel)
			if !checkOnly {
				if err := os.WriteFile(pkgPath, updated, 0o644); err != nil {
					return nil, err
				}
			}
		}
	}
	return stale, nil
}

// setPackageVersion edits the version in place rather than re-marshalling
// the whole file, which would reorder keys and reformat a file a person
// maintains.
func setPackageVersion(b []byte, version string) ([]byte, bool, error) {
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return nil, false, fmt.Errorf("parsing package.json: %w", err)
	}
	if pkg.Version == version {
		return b, false, nil
	}
	re := regexp.MustCompile(`("version"\s*:\s*")[^"]*(")`)
	return re.ReplaceAll(b, []byte("${1}"+version+"${2}")), true, nil
}

// ownerFromModule reads the GitHub owner out of go.mod.
//
// init knows the owner because it asked; regen has only the service
// directory, and the module path is where init recorded it:
//
//	module github.com/<owner>/<repo>[/services/<name>]
//
// Empty when it cannot be determined, which renders an unscoped package
// name rather than a wrong one.
func ownerFromModule(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "module ") {
			continue
		}
		parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "module ")), "/")
		if len(parts) >= 2 {
			return parts[1]
		}
		return ""
	}
	return ""
}

// specVersionIn reads info.version from a service's openapi.yml.
//
// Templates that state the API version - the TypeScript client's
// package.json, the Go client's ClientVersion - are rendered from
// Params, so Params has to carry what the spec says rather than a
// constant. It used to carry InitialSpecVersion always, which meant a
// regenerated client advertised 0.1.0 no matter how far the API had
// moved.
//
// Returns "" when there is no spec or no version; callers fall back to
// InitialSpecVersion, which is right for a service being created.
func specVersionIn(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "openapi.yml"))
	if err != nil {
		return ""
	}
	var spec struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(b, &spec); err != nil {
		return ""
	}
	return spec.Info.Version
}
