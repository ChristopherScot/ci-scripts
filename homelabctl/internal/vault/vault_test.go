package vault

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

// A 404 from Vault must be an ErrNotFound a caller can check, while a
// transport failure must not be - that is the distinction a shell exit
// status could not make, and the reason this package exists.
//
// Against a real server rather than comparing two sentinels to each
// other, which would only test the Go runtime.
func TestNotFoundComesFromA404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errors":[]}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{
			"bound_service_account_names":["svc"],
			"bound_service_account_namespaces":["ns"],
			"token_policies":["svc"],
			"token_ttl":3600}}`))
	}))
	defer srv.Close()

	t.Setenv("VAULT_ADDR", srv.URL)
	c, err := New("t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadRole(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadRole on a 404 = %v, want ErrNotFound", err)
	}

	// The round trip the drift check depends on: what WriteRole sends
	// must be what ReadRole returns. TTL is the one that used to differ -
	// "1h" out, 3600 back.
	got, err := c.ReadRole(context.Background(), "svc")
	if err != nil {
		t.Fatalf("ReadRole: %v", err)
	}
	want := Role{ServiceAccounts: []string{"svc"}, Namespaces: []string{"ns"},
		Policies: []string{"svc"}, TTLSeconds: 3600}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("ReadRole = %+v, want %+v", *got, want)
	}
}

// An unreachable Vault must NOT look like a missing role.
func TestUnreachableIsNotNotFound(t *testing.T) {
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1")
	c, err := New("t")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadRole(context.Background(), "whatever")
	if err == nil {
		t.Fatal("reading from an unreachable Vault succeeded")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("an unreachable Vault reported ErrNotFound: %v", err)
	}
}

// Silently yielding nil made "binds no namespaces" indistinguishable
// from "Vault sent something unexpected" - a drift check built on that
// would report a role as unbound, which is the unsafe direction.
func TestStrsRejectsAnUnexpectedShape(t *testing.T) {
	got, err := strs([]any{"a", "b"})
	if err != nil || len(got) != 2 {
		t.Errorf("strs([a b]) = %v, %v; want the two strings", got, err)
	}
	if got, err := strs(nil); err != nil || got != nil {
		t.Errorf("strs(nil) = %v, %v; want nil, nil", got, err)
	}
	for _, bad := range []any{"not a list", []any{"a", 3}, 42} {
		if _, err := strs(bad); err == nil {
			t.Errorf("strs(%v) accepted a shape it cannot represent", bad)
		}
	}
}
