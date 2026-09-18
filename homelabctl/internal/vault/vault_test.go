package vault

import (
	"errors"
	"testing"
)

// There is deliberately no default address. A guess would write a role
// into whichever Vault answered, and the failure would look like
// success - the role would exist somewhere, just not where the cluster
// authenticates against.
func TestNewRequiresAnExplicitAddress(t *testing.T) {
	t.Setenv("VAULT_ADDR", "")
	if _, err := New("token"); !errors.Is(err, ErrNoAddress) {
		t.Errorf("New() = %v, want ErrNoAddress", err)
	}
}

func TestNewAcceptsAnAddress(t *testing.T) {
	t.Setenv("VAULT_ADDR", "https://vault.example.com")
	if _, err := New("token"); err != nil {
		t.Errorf("New() = %v", err)
	}
}

// A missing role must be distinguishable from an unreachable Vault.
// Shelling out to the CLI lost that distinction in an exit status, which
// is why nothing could verify a role existed.
func TestNotFoundIsItsOwnError(t *testing.T) {
	if errors.Is(ErrNotFound, ErrNoAddress) {
		t.Error("ErrNotFound and ErrNoAddress are not distinguishable")
	}
}

func TestStrsCoercesVaultsUntypedArrays(t *testing.T) {
	got := strs([]any{"a", "b", 3})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("strs() = %v, want the two strings", got)
	}
	if strs(nil) != nil || strs("not a list") != nil {
		t.Error("strs() should yield nil for anything that is not a list")
	}
}
