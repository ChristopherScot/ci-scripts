package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
		Use:   "vault [config.yaml]",
		Short: "print or apply the Vault policy and role a service needs",
		Long: "Derives the Vault policy and Kubernetes auth role from the service's\n" +
			"config. Prints them by default; --apply writes them to Vault.",
		Args: cobra.MaximumNArgs(1),
	}
	var apply bool
	cmd.Flags().BoolVar(&apply, "apply", false, "write to Vault instead of printing")
	cmd.RunE = func(_ *cobra.Command, args []string) error {
		path := defaultConfigPath
		if len(args) == 1 {
			path = args[0]
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
		TTL:             "1h",
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
		strings.Join(r.Policies, ","), r.TTL)
}

// applyVault writes the policy and role over Vault's HTTP API. Both are
// replace-on-write, so this is idempotent: running it twice leaves the
// same state as running it once.
func applyVault(c *config.Config) error {
	token, err := vaultToken()
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

	if err := cl.WriteRole(ctx, c.VaultRoleName(), serviceRole(c)); err != nil {
		return err
	}
	fmt.Printf("wrote role auth/kubernetes/role/%s (sa=%s ns=%s) at %s\n",
		c.VaultRoleName(), c.ServiceAccountName(), c.Namespace, vault.Address())
	return nil
}

// vaultToken reads the token to authenticate with, preferring the
// environment so callers can supply a narrower one than root.
func vaultToken() (string, error) {
	if t := os.Getenv("VAULT_TOKEN"); t != "" {
		return t, nil
	}
	out, err := exec.Command("op", "read", "op://Employee/homelab-vault-root/password").Output()
	if err != nil {
		// op explains itself on stderr - not signed in, item renamed,
		// wrong vault - and Output() hides that behind "exit status 1".
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("no VAULT_TOKEN set and reading one from 1Password failed: %s",
				bytes.TrimSpace(ee.Stderr))
		}
		return "", fmt.Errorf("no VAULT_TOKEN set and could not read one from 1Password: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
