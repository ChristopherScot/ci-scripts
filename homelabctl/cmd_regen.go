package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

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
		Use:   "regen [config.yaml]",
		Short: "regenerate clients from the spec and sync their versions",
		Long: "Regenerate everything derived from openapi.yml: the server\n" +
			"interface, both clients, and the versions they report.\n\n" +
			"Run this after editing the spec. With --check it changes nothing\n" +
			"and fails if anything is out of date, which is what CI wants.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := defaultConfigPath
			if len(args) == 1 {
				path = args[0]
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

	// Generate only, never ResolveDeps: regenerating must be
	// deterministic. `go get -u` belongs to creating a service, and
	// running it here would mean CI - which runs this and diffs - failed
	// on any day a dependency published.
	if err := tidy(dir, r.Generate()); err != nil {
		return err
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

	pkgPath := filepath.Join(dir, "package.json")
	if b, err := os.ReadFile(pkgPath); err == nil {
		updated, changed, err := setPackageVersion(b, version)
		if err != nil {
			return nil, err
		}
		if changed {
			stale = append(stale, "package.json")
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
