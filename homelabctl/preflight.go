package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// Preflight compares a config against what is actually running, so a change
// that would break a live service is refused at the command line rather
// than discovered after it is applied.
//
// This exists because of a real incident: regenerating approvald's
// manifests silently dropped three env vars the config did not declare, and
// separately renamed its ServiceAccount while the Vault role still
// authorized the old name. Both were invisible until the pod crashlooped.
//
// Every check is advisory when the cluster is unreachable - this must still
// work offline, so a missing kubectl is "unknown", never "fine".
type Finding struct {
	// Blocking findings would break a running service; others are warnings.
	Blocking bool
	Message  string
	Fix      string
}

// preflight reports what would change about a service that already exists.
// An empty result means the change is additive or the service is new.
func preflight(c *config.Config) ([]Finding, error) {
	if err := clusterReachable(); err != nil {
		return nil, fmt.Errorf("cannot check live state: %w", err)
	}

	var out []Finding
	live, err := liveDeployment(c.Namespace, c.Name)
	if err != nil || live == nil {
		// Not found under this name. A rename puts the running service
		// under the OLD name, so looking only here would report "new
		// service" for exactly the change most likely to break something.
		if others := otherDeployments(c.Namespace, c.Name); len(others) > 0 {
			return []Finding{{
				Blocking: true,
				Message: fmt.Sprintf("no deployment named %q in namespace %q, but it holds: %s",
					c.Name, c.Namespace, strings.Join(others, ", ")),
				Fix: "if this is a rename, the old deployment, its ServiceAccount and any Vault " +
					"role binding must be migrated deliberately - a rename here leaves the old " +
					"one running and the new one unauthorized",
			}}, nil
		}
		// Genuinely nothing deployed: first install.
		return nil, nil
	}

	out = append(out, checkEnvDrift(c, live)...)
	out = append(out, checkServiceAccount(c, live)...)
	return out, nil
}

// checkEnvDrift catches the failure that took approvald down: the config
// declares fewer env vars than the running pod has, so regenerating drops
// them and the service starts crashlooping on a missing variable.
func checkEnvDrift(c *config.Config, live *liveState) []Finding {
	declared := map[string]bool{"PORT": true}
	for k := range c.Env {
		declared[k] = true
	}

	var missing []string
	for _, name := range live.Env {
		if !declared[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return []Finding{{
		Blocking: true,
		Message: fmt.Sprintf("the running deployment sets %d env var(s) this config does not declare: %s",
			len(missing), strings.Join(missing, ", ")),
		Fix: "add them under `env:` in config.yaml, or the regenerated pod will fail to start",
	}}
}

// checkServiceAccount catches an identity rename. The Vault role binds to a
// ServiceAccount by name, and that binding lives outside Kubernetes - so
// renaming here without rebinding there leaves the store unable to
// authenticate, with the secret silently stale.
func checkServiceAccount(c *config.Config, live *liveState) []Finding {
	if c.Secrets == nil || live.ServiceAccount == "" || live.ServiceAccount == c.Name {
		return nil
	}
	return []Finding{{
		Blocking: true,
		Message: fmt.Sprintf("this would rename the ServiceAccount from %q to %q",
			live.ServiceAccount, c.Name),
		Fix: fmt.Sprintf("Vault's role binds to the old name. Add the new one BEFORE applying, "+
			"verify, then remove the old:\n"+
			"      vault write auth/kubernetes/role/%s bound_service_account_names=%s,%s ...",
			c.Name, live.ServiceAccount, c.Name),
	}}
}

type liveState struct {
	Env            []string
	ServiceAccount string
}

func liveDeployment(namespace, name string) (*liveState, error) {
	out, err := exec.Command("kubectl", "get", "deployment", name,
		"-n", namespace, "-o", "json").Output()
	if err != nil {
		return nil, err // not deployed, or no access
	}
	var d struct {
		Spec struct {
			Template struct {
				Spec struct {
					ServiceAccountName string `json:"serviceAccountName"`
					Containers         []struct {
						Env []struct {
							Name string `json:"name"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, err
	}
	s := &liveState{ServiceAccount: d.Spec.Template.Spec.ServiceAccountName}
	for _, ctr := range d.Spec.Template.Spec.Containers {
		for _, e := range ctr.Env {
			s.Env = append(s.Env, e.Name)
		}
	}
	return s, nil
}

// otherDeployments lists deployments in the namespace that are not the one
// we expected, which is what a rename looks like from here.
func otherDeployments(namespace, except string) []string {
	out, err := exec.Command("kubectl", "get", "deployments", "-n", namespace,
		"-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, n := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if n != "" && n != except {
			names = append(names, n)
		}
	}
	return names
}

// clusterReachable returns why the cluster cannot be reached, rather than
// a bool - "kubectl is not installed" and "the API server is down" need
// different fixes and a bool cannot tell them apart.
func clusterReachable() error {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return fmt.Errorf("kubectl not found on PATH: %w", err)
	}
	if err := exec.Command("kubectl", "cluster-info", "--request-timeout=3s").Run(); err != nil {
		return fmt.Errorf("kubectl cannot reach the cluster: %w", err)
	}
	return nil
}

// reportPreflight prints findings and reports whether any would break a
// running service.
func reportPreflight(findings []Finding) bool {
	var blocked bool
	for _, f := range findings {
		label := "warning"
		if f.Blocking {
			label = "WOULD BREAK"
			blocked = true
		}
		fmt.Printf("\n  %s: %s\n", label, f.Message)
		if f.Fix != "" {
			fmt.Printf("      %s\n", f.Fix)
		}
	}
	return blocked
}
