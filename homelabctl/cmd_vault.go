package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/vault"
	"github.com/spf13/cobra"
)

// The Vault role a service needs is fully determined by its config: the
// policy reads its own kv path, and the role binds its own ServiceAccount
// in its own namespace. Nothing about it needs a human to retype it into a
// bash array in another repo - which is a step that gets skipped, and then
// the SecretStore references a role that does not exist.
func vaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "print or apply the Vault policy and role a service needs",
		Long: "Derives the Vault policy and Kubernetes auth role from the service's\n" +
			"config. Prints them by default; --apply writes them to Vault.",
		Args: cobra.NoArgs,
	}
	var apply bool
	cmd.Flags().BoolVar(&apply, "apply", false, "write to Vault instead of printing")
	cmd.RunE = func(*cobra.Command, []string) error {
		path, err := findConfig()
		if err != nil {
			return err
		}
		c, err := config.Load(path)
		if err != nil {
			return err
		}
		if c.Secrets == nil {
			return fmt.Errorf("%s declares no secrets, so it needs no Vault role", path)
		}
		if apply {
			return applyVault(c)
		}
		fmt.Print(vaultCommands(c))
		return nil
	}
	return cmd
}

// vaultPolicy grants read on this service's own kv path and nothing else,
// matching the convention in bootstrap/vault-policies.sh: compromising one
// pod must not expose another app's secrets.
func vaultPolicy(c *config.Config) string {
	// Grant the declared path and everything under it - not an ancestor.
	//
	// Truncating to the first segment would mean `vaultPath:
	// shared/myapp/config` grants read on kv/data/shared/*, i.e. every
	// service filed under that prefix. The isolation that matters is
	// between services, so the grant must never be broader than what the
	// service declared.
	base := strings.Trim(c.Secrets.VaultPath, "/")
	return fmt.Sprintf(`path "kv/data/%s" {
  capabilities = ["read"]
}
path "kv/data/%s/*" {
  capabilities = ["read"]
}
path "kv/metadata/%s" {
  capabilities = ["read", "list"]
}
path "kv/metadata/%s/*" {
  capabilities = ["read", "list"]
}
`, base, base, base, base)
}

// serviceRole is the single definition of the role this service needs.
// vaultCommands prints it and applyVault writes it, so the preview cannot
// drift from what actually lands in Vault.
func serviceRole(c *config.Config) vault.Role {
	return vault.Role{
		ServiceAccounts: []string{c.ServiceAccountName()},
		Namespaces:      []string{c.Namespace},
		Policies:        []string{c.VaultPolicyName()},
		TTLSeconds:      3600,
	}
}

// vaultCommands renders what --apply would run, so it can be reviewed,
// pasted, or committed before anything touches Vault.
func vaultCommands(c *config.Config) string {
	r := serviceRole(c)
	return fmt.Sprintf(`# Vault policy and role for %s, derived from its config.
# Apply with: homelabctl vault config.yaml --apply

vault policy write %s - <<'POLICY'
%sPOLICY

vault write auth/kubernetes/role/%s \
  bound_service_account_names=%s \
  bound_service_account_namespaces=%s \
  policies=%s \
  ttl=%s
`, c.Name, c.VaultPolicyName(), vaultPolicy(c), c.VaultRoleName(),
		strings.Join(r.ServiceAccounts, ","), strings.Join(r.Namespaces, ","),
		strings.Join(r.Policies, ","), fmt.Sprintf("%ds", r.TTLSeconds))
}

// applyVault writes the policy and role over Vault's HTTP API. Both are
// replace-on-write, so this is idempotent: running it twice leaves the
// same state as running it once.
func applyVault(c *config.Config) error {
	token, err := vault.Token()
	if err != nil {
		return err
	}
	cl, err := vault.New(token)
	if err != nil {
		return err
	}
	ctx := context.Background()

	if err := cl.WritePolicy(ctx, c.VaultPolicyName(), vaultPolicy(c)); err != nil {
		return err
	}
	fmt.Printf("wrote policy %s\n", c.VaultPolicyName())

	want := serviceRole(c)
	if err := cl.WriteRole(ctx, c.VaultRoleName(), want); err != nil {
		return err
	}

	// Read it back rather than trusting the write. Vault accepts a role
	// and normalises it - a TTL sent as a duration comes back as
	// seconds - so "no error" does not mean "what the config asked for".
	// This is also the only thing that would notice a policy name the
	// role does not actually carry, which is how a pod ends up
	// authenticating successfully and still being denied every read.
	got, err := cl.ReadRole(ctx, c.VaultRoleName())
	if err != nil {
		return fmt.Errorf("role was written but could not be read back: %w", err)
	}
	if diff := roleDiff(want, *got); diff != "" {
		return fmt.Errorf("role %s does not match the config after writing:\n%s",
			c.VaultRoleName(), diff)
	}

	fmt.Printf("wrote role auth/kubernetes/role/%s (sa=%s ns=%s) at %s\n",
		c.VaultRoleName(), c.ServiceAccountName(), c.Namespace, cl.Address())
	fmt.Println("verified: reads back as written")
	return nil
}

// roleDiff reports how a role in Vault differs from what the config
// asks for, as lines a person can act on. Empty means they agree.
func roleDiff(want, got vault.Role) string {
	var b strings.Builder
	cmp := func(field string, w, g []string) {
		if !slices.Equal(w, g) {
			fmt.Fprintf(&b, "  %s: want %v, got %v\n", field, w, g)
		}
	}
	cmp("bound_service_account_names", want.ServiceAccounts, got.ServiceAccounts)
	cmp("bound_service_account_namespaces", want.Namespaces, got.Namespaces)
	cmp("token_policies", want.Policies, got.Policies)
	if want.TTLSeconds != got.TTLSeconds {
		fmt.Fprintf(&b, "  token_ttl: want %ds, got %ds\n", want.TTLSeconds, got.TTLSeconds)
	}
	return b.String()
}
