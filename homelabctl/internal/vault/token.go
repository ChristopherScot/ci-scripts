package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Token finds a Vault token the way every other Vault-aware tool does,
// so a person who can already use `vault` can use this without setting
// anything up.
//
// In order:
//
//   - VAULT_TOKEN, for CI and scripts.
//   - VAULT_TOKEN_FILE, for a token mounted somewhere else. Explicit
//     beats ambient, so this is checked before the file below.
//   - ~/.vault-token, which `vault login` writes. This is the one that
//     matters for a human: they log in once, however their Vault is
//     configured, and every tool picks it up.
//
// It deliberately shells out to nothing. This used to run
// `op read op://Employee/homelab-vault-root/password`, which hardcoded
// one person's 1Password account, vault name and item - and handed back
// the ROOT token, so creating one service's role ran with sudo on every
// path. Anyone else got "exit status 1".
//
// How the token is obtained is the operator's choice, not this tool's:
// `vault login -method=oidc` if Vault has OIDC, userpass, a token from
// a password manager exported into the environment. All of them end up
// in one of the three places above.
func Token() (string, error) {
	if t := strings.TrimSpace(os.Getenv("VAULT_TOKEN")); t != "" {
		return t, nil
	}
	if p := strings.TrimSpace(os.Getenv("VAULT_TOKEN_FILE")); p != "" {
		t, err := readTokenFile(p)
		if err != nil {
			return "", err
		}
		if t != "" {
			return t, nil
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		t, err := readTokenFile(filepath.Join(home, ".vault-token"))
		if err == nil && t != "" {
			return t, nil
		}
	}
	return "", fmt.Errorf("no Vault token: set VAULT_TOKEN, or run `vault login` " +
		"(which writes ~/.vault-token), or point VAULT_TOKEN_FILE at one")
}

func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}
