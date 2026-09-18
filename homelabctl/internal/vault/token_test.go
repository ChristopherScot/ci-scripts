package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenPrefersTheEnvironment(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "from-env")
	got, err := Token()
	if err != nil || got != "from-env" {
		t.Errorf("Token() = %q, %v; want from-env", got, err)
	}
}

// `vault login` writes this file, whatever auth method it used. Reading
// it is what lets someone who can already use `vault` use this tool
// without setting anything else up.
func TestTokenReadsTheFileVaultLoginWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".vault-token"), []byte("from-login\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Token()
	if err != nil || got != "from-login" {
		t.Errorf("Token() = %q, %v; want from-login", got, err)
	}
}

func TestTokenHonoursAnExplicitFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	if err := os.WriteFile(p, []byte("  from-file  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_TOKEN_FILE", p)
	got, err := Token()
	if err != nil || got != "from-file" {
		t.Errorf("Token() = %q, %v; want from-file trimmed", got, err)
	}
}

// With nothing available the error has to say what to do. It used to
// shell out to `op`, which failed with "exit status 1" on any machine
// that was not one particular laptop.
func TestTokenSaysHowToGetOne(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("VAULT_TOKEN_FILE", "")
	t.Setenv("HOME", t.TempDir())
	_, err := Token()
	if err == nil {
		t.Fatal("Token() found one from nowhere")
	}
	for _, want := range []string{"VAULT_TOKEN", "vault login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
