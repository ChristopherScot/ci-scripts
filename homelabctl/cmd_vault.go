package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/spf13/cobra"
)

// The Vault role a service needs is fully determined by its config: the
// policy reads its own kv path, and the role binds its own ServiceAccount
// in its own namespace. Nothing about it needs a human to retype it into a
// bash array in another repo - which is a step that gets skipped, and then
// the SecretStore references a role that does not exist.
func vaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault <config.yaml>",
		Short: "print or apply the Vault policy and role a service needs",
		Long: "Derives the Vault policy and Kubernetes auth role from the service's\n" +
			"config. Prints them by default; --apply writes them to Vault.",
		Args: cobra.ExactArgs(1),
	}
	var apply bool
	cmd.Flags().BoolVar(&apply, "apply", false, "write to Vault instead of printing")
	cmd.RunE = func(_ *cobra.Command, args []string) error {
		c, err := config.Load(args[0])
		if err != nil {
			return err
		}
		if c.Secrets == nil {
			return fmt.Errorf("%s declares no secrets, so it needs no Vault role", args[0])
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

// roleArgs is the single definition of the role. vaultCommands prints
// these and applyVault runs them, so the preview cannot drift from what is
// actually written.
func roleArgs(c *config.Config) []string {
	return []string{
		"write", "auth/kubernetes/role/" + c.Name,
		"bound_service_account_names=" + c.Name,
		"bound_service_account_namespaces=" + c.Namespace,
		"policies=" + c.Name,
		"ttl=1h",
	}
}

// vaultCommands renders what --apply would run, so it can be reviewed,
// pasted, or committed before anything touches Vault.
func vaultCommands(c *config.Config) string {
	args := roleArgs(c)
	return fmt.Sprintf(`# Vault policy and role for %s, derived from its config.
# Apply with: homelabctl vault config.yaml --apply

vault policy write %s - <<'POLICY'
%sPOLICY

vault %s \
  %s
`, c.Name, c.Name, vaultPolicy(c), args[0]+" "+args[1], strings.Join(args[2:], " \\\n  "))
}

// applyVault runs the policy and role writes through the vault-0 pod. Both
// are replace-on-write, so this is idempotent - running it twice leaves the
// same state as running it once.
func applyVault(c *config.Config) error {
	token, err := vaultToken()
	if err != nil {
		return err
	}

	if err := vaultExec(token, vaultPolicy(c), "policy", "write", c.Name, "-"); err != nil {
		return fmt.Errorf("write policy %s: %w", c.Name, err)
	}
	fmt.Printf("wrote policy %s\n", c.Name)

	if err := vaultExec(token, "", roleArgs(c)...); err != nil {
		return fmt.Errorf("write role %s: %w", c.Name, err)
	}
	fmt.Printf("wrote role auth/kubernetes/role/%s (sa=%s ns=%s)\n", c.Name, c.Name, c.Namespace)
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

// vaultExec runs a vault command inside the vault-0 pod. stdin is passed
// explicitly rather than inferred from a trailing "-" in args: inferring it
// means a later arg silently leaves stdin empty, and `vault policy write
// name -` on empty stdin writes an EMPTY POLICY and exits 0 - the service
// then loses all access with no error anywhere.
//
// The token goes in on stdin too, never in argv: argv is readable by any
// local process via ps, appears in the container's process table, and is
// recorded in the API server's audit log for pods/exec. This is the
// cluster's root token.
func vaultExec(token, stdin string, args ...string) error {
	script := `read -r VAULT_TOKEN
export VAULT_TOKEN VAULT_ADDR=http://127.0.0.1:8200
exec vault "$@"`

	full := append([]string{"exec", "-i", "-n", "default", "vault-0", "--",
		"sh", "-c", script, "sh"}, args...)
	cmd := exec.Command("kubectl", full...)
	cmd.Stdin = strings.NewReader(token + "\n" + stdin)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
