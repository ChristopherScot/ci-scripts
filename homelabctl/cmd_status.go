package main

import (
	"encoding/json"
	"fmt"
	"io"
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
	return &cobra.Command{
		Use:   "status",
		Short: "what Argo says about this service in the cluster",
		Long: "Reads the Argo Application for this service and reports sync state,\n" +
			"health, and any conditions - which is where a manifest the API\n" +
			"server rejected shows up.\n\n" +
			"Needs cluster access, so it is a local command rather than a CI step.",
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
			return runStatus(cmd.OutOrStdout(), c.AppName())
		},
	}
}

// argoApp is the part of an Application's status worth reporting.
type argoApp struct {
	Status struct {
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
	} `json:"status"`
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
