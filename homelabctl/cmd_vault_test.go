package main

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// A policy must never grant more than the service declared. Truncating the
// path to its first segment meant `shared/myapp/config` granted read on
// kv/data/shared/* - every service filed under that prefix.
// Validate refuses a leading or trailing slash now, so the renderer's
// own trimming is defence in depth rather than the only guard. Tested
// directly, since a config carrying one no longer loads.
func TestPolicyTrimsSlashesItIsHandedAnyway(t *testing.T) {
	c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
		Secrets: &config.Secrets{VaultPath: "/leading/slash/", Keys: config.EnvKeys("K")}}
	if got := vaultPolicy(c); !strings.Contains(got, `path "kv/data/leading/slash"`) {
		t.Errorf("policy did not trim the slashes:\n%s", got)
	}
}

func TestPolicyNeverGrantsAnAncestorPath(t *testing.T) {
	for _, tc := range []struct {
		vaultPath string
		wantGrant string
		denied    []string
	}{
		{"approvald/config", "kv/data/approvald/config", []string{"kv/data/approvald/*"}},
		{"shared/myapp/config", "kv/data/shared/myapp/config", []string{"kv/data/shared/*", "kv/data/shared/myapp/*"}},
	} {
		t.Run(tc.vaultPath, func(t *testing.T) {
			c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
				Secrets: &config.Secrets{VaultPath: tc.vaultPath, Keys: config.EnvKeys("K")}}
			if err := c.Complete(); err != nil {
				t.Fatal(err)
			}
			got := vaultPolicy(c)
			if !strings.Contains(got, `path "`+tc.wantGrant+`"`) {
				t.Errorf("policy does not grant %q:\n%s", tc.wantGrant, got)
			}
			for _, d := range tc.denied {
				if strings.Contains(got, `path "`+d+`"`) {
					t.Errorf("policy grants the broader path %q, which reaches other services:\n%s", d, got)
				}
			}
		})
	}
}

// The role must bind only this service's own ServiceAccount in its own
// namespace; a wider binding lets another pod assume it.
func TestRoleBindsOnlyItsOwnServiceAccount(t *testing.T) {
	c := &config.Config{Name: "svc", Namespace: "svc-ns", Team: "t",
		Runtime: "go-service", Port: 3000,
		Secrets: &config.Secrets{VaultPath: "svc/config", Keys: config.EnvKeys("K")}}
	if err := c.Complete(); err != nil {
		t.Fatal(err)
	}
	got := vaultCommands(c)
	for _, want := range []string{
		"bound_service_account_names=svc",
		"bound_service_account_namespaces=svc-ns",
		"policies=svc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("role is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bound_service_account_names=*") {
		t.Error("role binds a wildcard ServiceAccount")
	}
}

// The policy has to cover every path the keys name, not just vaultPath.
//
// Without this, a key reading `ntfy/config` renders an ExternalSecret
// asking Vault for a path the role cannot read: the manifests apply
// cleanly, ESO never syncs, and the pod starts without the variable -
// the exact silent failure the secrets block exists to prevent.
func TestVaultPolicyCoversEveryPathTheKeysName(t *testing.T) {
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = "ddns", "t", "go-service"
	c.Secrets = &config.Secrets{
		VaultPath: "cert-manager/route53",
		Keys: []config.SecretKey{
			{Env: "AWS_ACCESS_KEY_ID", Property: "access_key_id"},
			{Env: "NTFY_TOKEN", Property: "grafana_token", Path: "ntfy/config"},
		},
	}
	got := vaultPolicy(&c)
	for _, want := range []string{
		`path "kv/data/cert-manager/route53"`,
		`path "kv/data/ntfy/config"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("policy does not grant %s:\n%s", want, got)
		}
	}
}

// A path named by two keys is granted once, not twice.
func TestVaultPolicyDoesNotRepeatAPath(t *testing.T) {
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = "a", "t", "go-service"
	c.Secrets = &config.Secrets{
		VaultPath: "a/config",
		Keys: []config.SecretKey{
			{Env: "ONE", Property: "one", Path: "shared/config"},
			{Env: "TWO", Property: "two", Path: "shared/config"},
		},
	}
	if n := strings.Count(vaultPolicy(&c), `path "kv/data/shared/config" {`); n != 1 {
		t.Errorf("granted kv/data/shared/config %d times, want 1", n)
	}
}

// A service with no cross-path keys gets exactly the policy it got
// before this feature existed.
func TestVaultPolicyIsUnchangedWithoutCrossPathKeys(t *testing.T) {
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = "a", "t", "go-service"
	c.Secrets = &config.Secrets{VaultPath: "a/config", Keys: config.EnvKeys("TOK")}
	want := `path "kv/data/a/config" {
  capabilities = ["read"]
}
path "kv/data/a/config/*" {
  capabilities = ["read"]
}
path "kv/metadata/a/config" {
  capabilities = ["read", "list"]
}
path "kv/metadata/a/config/*" {
  capabilities = ["read", "list"]
}
`
	if got := vaultPolicy(&c); got != want {
		t.Errorf("policy =\n%s\nwant\n%s", got, want)
	}
}
