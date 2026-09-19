package main

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// The failure this exists for: regenerating a live service dropped three
// env vars the config never declared, and the pod crashlooped on startup.
func TestEnvDriftIsBlocking(t *testing.T) {
	c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
		Env: map[string]config.EnvValue{"KEEP": config.EnvLiteral("1")}}
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	live := &liveState{Env: []string{"PORT", "KEEP", "BASE_URL", "NTFY_URL"}}

	f := checkEnvDrift(c, live)
	if len(f) != 1 || !f[0].Blocking {
		t.Fatalf("expected one blocking finding, got %+v", f)
	}
	for _, want := range []string{"BASE_URL", "NTFY_URL"} {
		if !strings.Contains(f[0].Message, want) {
			t.Errorf("finding does not name the dropped var %q: %s", want, f[0].Message)
		}
	}
	if strings.Contains(f[0].Message, "KEEP") {
		t.Error("a declared var was reported as dropped")
	}
}

// PORT is set by the renderer, not the config, so it must not be reported.
func TestPortIsNotReportedAsDrift(t *testing.T) {
	c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000}
	_ = c.Complete()
	if f := checkEnvDrift(c, &liveState{Env: []string{"PORT"}}); len(f) != 0 {
		t.Errorf("PORT reported as drift: %+v", f)
	}
}

// Renaming the ServiceAccount breaks Vault auth, because the role binds to
// the old name and that binding is not a Kubernetes object.
func TestServiceAccountRenameIsBlocking(t *testing.T) {
	c := &config.Config{Name: "newname", Team: "t", Runtime: "go-service", Port: 3000,
		Secrets: &config.Secrets{VaultPath: "p", Keys: config.EnvKeys("K")}}
	_ = c.Complete()

	f := checkServiceAccount(c, &liveState{ServiceAccount: "oldname"})
	if len(f) != 1 || !f[0].Blocking {
		t.Fatalf("expected a blocking finding, got %+v", f)
	}
	// The fix must tell the user to ADD the new binding before removing
	// the old one; replacing it outright is what caused the outage.
	if !strings.Contains(f[0].Fix, "oldname,newname") {
		t.Errorf("fix should show adding both names, got: %s", f[0].Fix)
	}
}

func TestNoFindingWhenServiceAccountMatches(t *testing.T) {
	c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
		Secrets: &config.Secrets{VaultPath: "p", Keys: config.EnvKeys("K")}}
	_ = c.Complete()
	if f := checkServiceAccount(c, &liveState{ServiceAccount: "svc"}); len(f) != 0 {
		t.Errorf("matching SA reported as drift: %+v", f)
	}
}

// Removing `secrets:` makes render drop serviceAccountName entirely, so the
// pod silently reverts to the default account - an identity change that is
// invisible in a diff unless you notice a line disappeared.
func TestDroppingSecretsRevertsToDefaultAccount(t *testing.T) {
	c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000}
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	f := checkServiceAccount(c, &liveState{ServiceAccount: "svc"})
	if len(f) != 1 || !f[0].Blocking {
		t.Fatalf("expected a blocking finding, got %+v", f)
	}
	if !strings.Contains(f[0].Message, "default") {
		t.Errorf("finding should say the pod reverts to the default account: %s", f[0].Message)
	}
}

// A pod that never had a ServiceAccount reports "default"; that is not drift.
func TestDefaultAccountIsNotDrift(t *testing.T) {
	c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000}
	_ = c.Complete()
	if f := checkServiceAccount(c, &liveState{ServiceAccount: "default"}); len(f) != 0 {
		t.Errorf("an unset ServiceAccount reported as drift: %+v", f)
	}
}

// Secrets reach the pod through envFrom, not `env:`, so a service using
// `secrets:` must not be told its own secret keys are undeclared drift.
// The advice that came with that finding - "add them under env:" - would
// have put the secret in plaintext in config.yaml.
func TestEnvDriftIgnoresSecretKeys(t *testing.T) {
	c := &config.Config{
		Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
		Image:   config.Image{Repository: "ghcr.io/o/svc"},
		Env:     map[string]config.EnvValue{"API_URL": config.EnvLiteral("http://x")},
		Secrets: &config.Secrets{VaultPath: "svc", Keys: config.EnvKeys("API_KEY")},
	}
	_ = c.Complete()

	live := &liveState{Env: []string{"PORT", "API_URL", "API_KEY"}}
	if f := checkEnvDrift(c, live); len(f) != 0 {
		t.Errorf("reported drift for a secret-provided env var: %v", f[0].Message)
	}
}

// A genuinely undeclared variable is still drift.
func TestEnvDriftStillCatchesAnUndeclaredVar(t *testing.T) {
	c := &config.Config{
		Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
		Image:   config.Image{Repository: "ghcr.io/o/svc"},
		Secrets: &config.Secrets{VaultPath: "svc", Keys: config.EnvKeys("API_KEY")},
	}
	_ = c.Complete()

	live := &liveState{Env: []string{"PORT", "API_KEY", "FORGOTTEN"}}
	f := checkEnvDrift(c, live)
	if len(f) != 1 || !strings.Contains(f[0].Message, "FORGOTTEN") {
		t.Errorf("did not catch an undeclared env var: %v", f)
	}
}
