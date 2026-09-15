package main

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// A policy must never grant more than the service declared. Truncating the
// path to its first segment meant `shared/myapp/config` granted read on
// kv/data/shared/* - every service filed under that prefix.
func TestPolicyNeverGrantsAnAncestorPath(t *testing.T) {
	for _, tc := range []struct {
		vaultPath string
		wantGrant string
		denied    []string
	}{
		{"approvald/config", "kv/data/approvald/config", []string{"kv/data/approvald/*"}},
		{"shared/myapp/config", "kv/data/shared/myapp/config", []string{"kv/data/shared/*", "kv/data/shared/myapp/*"}},
		{"/leading/slash/", "kv/data/leading/slash", nil},
	} {
		t.Run(tc.vaultPath, func(t *testing.T) {
			c := &config.Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000,
				Secrets: &config.Secrets{VaultPath: tc.vaultPath, Keys: []string{"K"}}}
			if err := c.Validate(); err != nil {
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
		Secrets: &config.Secrets{VaultPath: "svc/config", Keys: []string{"K"}}}
	if err := c.Validate(); err != nil {
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
