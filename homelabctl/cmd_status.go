package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// status answers "is the thing I pushed actually running", which no
// other command can.
//
// `check` validates files and `diff` compares rendered output to what is
// committed - both answer questions about the repo, and both pass for a
// service that Argo is refusing to sync. The API server is the only
// thing that can validate a hand-written CNPG Cluster, because it is the
// only thing that knows the CRD; when it rejects one, the failure lands
// on the Argo Application and nothing in the service repo mentions it.
//
// Deliberately NOT a CI step. Two reasons, either of them sufficient:
// the cluster is LAN-only, so a GitHub runner cannot reach the API
// server or Argo at all; and CI runs before merge while a SyncError
// only exists after Argo has tried to apply the manifest. A pre-merge
// check cannot see a post-merge failure. Putting this in the workflow
// would produce a step that passes because it reached nothing - the
// same shape as a green check on the wrong artifact.
func statusCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "what Argo says about this service in the cluster",
		Long: "Reads the Argo Application for this service and reports sync state,\n" +
			"health, and any conditions - which is where a manifest the API\n" +
			"server rejected shows up.\n\n" +
			"--all reports every service in this repository, which is what you\n" +
			"want after a change that touches more than one.\n\n" +
			"Needs cluster access, so it is a local command rather than a CI step.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if all {
				return runStatusAll(cmd.OutOrStdout())
			}
			path, err := findConfig()
			if err != nil {
				return err
			}
			c, err := config.Load(path)
			if err != nil {
				return err
			}
			return runStatus(cmd.OutOrStdout(), c.AppName())
		},
	}
	cmd.Flags().BoolVar(&all, "all", false,
		"report every service in this repository, not just this one")
	return cmd
}

// argoApp is an Application as kubectl returns it.
type argoApp struct {
	Status argoStatus `json:"status"`
}

// argoStatus is the part of an Application's status worth reporting.
type argoStatus struct {
	Conditions []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"conditions"`
	Health struct {
		Status string `json:"status"`
	} `json:"health"`
	Sync struct {
		Status   string `json:"status"`
		Revision string `json:"revision"`
	} `json:"sync"`
	OperationState struct {
		Phase   string `json:"phase"`
		Message string `json:"message"`
	} `json:"operationState"`
	// Per-resource state, which is where a hand-written manifest shows
	// up: a CNPG Cluster from manifests/ is not its own Application, it
	// is a resource inside this one. Without it, a database that failed
	// to apply reads only as "the service is Degraded".
	Resources []struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Health struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"health"`
	} `json:"resources"`
}

// runStatusAll reports every deployable service in THIS repository.
//
// Scoped to the repo, not to the cluster. Selecting on the ApplicationSet's
// `team` label would report every service this tool manages anywhere -
// standing in the pokemon repo would print approvald and
// shlink-redirector too, which is not what "my stack" means to anyone.
//
// Found by walking for config.yaml from the repository root, so a
// monorepo reports each of its services and a single-service repo
// reports the one. A directory without a config.yaml is not a service:
// pokedex-cli and pokedex-tui are published binaries, not Argo
// Applications, and they correctly do not appear.
func runStatusAll(w io.Writer) error {
	root := repoRoot()
	if root == "" {
		return fmt.Errorf("not in a git repository, so there is no repo to report on")
	}
	configs, err := serviceConfigs(root)
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		return fmt.Errorf("no service config.yaml found under %s", root)
	}

	var unhealthy []string
	for i, cfgPath := range configs {
		c, err := config.Load(cfgPath)
		if err != nil {
			return fmt.Errorf("%s: %w", cfgPath, err)
		}
		if i > 0 {
			fmt.Fprintln(w)
		}
		if err := runStatus(w, c.AppName()); err != nil {
			unhealthy = append(unhealthy, c.AppName())
		}
	}
	if len(unhealthy) > 0 {
		return fmt.Errorf("%d of %d not healthy: %s",
			len(unhealthy), len(configs), strings.Join(unhealthy, ", "))
	}
	return nil
}

// serviceConfigs finds every service config under root.
//
// Skips .git and node_modules rather than walking them: both are large
// and neither contains a service.
func serviceConfigs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "deploy":
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == configName {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

func runStatus(w io.Writer, name string) error {
	out, err := capture("reading the Argo Application for "+name,
		"kubectl", "get", "application", "-n", "argocd", name, "-o", "json")
	if err != nil {
		return err
	}
	var app argoApp
	if err := json.Unmarshal(out, &app); err != nil {
		return fmt.Errorf("parsing the Argo Application for %s: %w", name, err)
	}
	return report(w, name, app)
}

// report prints an Application's state, separated from fetching it so a
// test can feed it the shapes Argo actually produces.
func report(w io.Writer, name string, app argoApp) error {
	s := app.Status
	fmt.Fprintf(w, "%s: %s / %s\n", name, s.Sync.Status, s.Health.Status)
	if rev := s.Sync.Revision; rev != "" {
		fmt.Fprintf(w, "  revision %s\n", shortRev(rev))
	}
	if s.OperationState.Phase != "" {
		fmt.Fprintf(w, "  last sync %s: %s\n",
			strings.ToLower(s.OperationState.Phase),
			strings.TrimSpace(s.OperationState.Message))
	}

	// Any resource that is not fine, named. Healthy ones are omitted:
	// listing every Service and Deployment on every run buries the one
	// line that matters.
	for _, r := range s.Resources {
		bad := r.Status != "" && r.Status != "Synced"
		if h := r.Health.Status; h != "" && h != "Healthy" && h != "Progressing" {
			bad = true
		}
		if !bad {
			continue
		}
		line := fmt.Sprintf("  %s/%s: %s", strings.ToLower(r.Kind), r.Name, r.Status)
		if h := r.Health.Status; h != "" && h != "Healthy" {
			line += " / " + h
		}
		fmt.Fprintln(w, line)
		if m := strings.TrimSpace(r.Health.Message); m != "" {
			fmt.Fprintf(w, "    %s\n", m)
		}
	}

	// Conditions last, because they are the reason to run this.
	for _, c := range s.Conditions {
		fmt.Fprintf(w, "\n%s: %s\n", c.Type, strings.TrimSpace(c.Message))
	}
	if len(s.Conditions) > 0 || s.Health.Status == "Degraded" || s.Sync.Status == "OutOfSync" {
		return fmt.Errorf("%s is not healthy in the cluster", name)
	}
	return nil
}

// shortRev trims a commit sha to something readable, leaving anything
// that is not one alone.
func shortRev(rev string) string {
	if len(rev) == 40 && !strings.ContainsAny(rev, "./") {
		return rev[:7]
	}
	return rev
}
