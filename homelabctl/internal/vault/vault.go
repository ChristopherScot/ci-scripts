// Package vault talks to Vault's HTTP API.
//
// It replaces shelling out to `kubectl exec -n default vault-0 -- sh -c
// '... exec vault "$@"'`, which was four hops - kubectl, a pod, a shell,
// the vault CLI - and assumed Vault was a pod with that exact name, in
// that namespace, with the binary on its PATH. Any of those changing
// produced a shell error rather than something a caller could act on.
//
// It also could only write. Nothing could ask whether a role existed, so
// a service whose ExternalSecret named a role nobody had created
// rendered clean, synced clean, and started without its secret.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/vault-client-go"
	"github.com/hashicorp/vault-client-go/schema"
)

// Client is a Vault connection scoped to what this tool does: read and
// write the Kubernetes auth roles and ACL policies that a service needs.
type Client struct {
	api  *vault.Client
	addr string
}

// ErrNoAddress is returned when VAULT_ADDR is unset. There is
// deliberately no default: a wrong guess would write a role into
// whichever Vault happened to answer, and the failure would look like
// success.
var ErrNoAddress = errors.New("VAULT_ADDR is not set")

// ErrNotFound is what a caller checks to tell "this role does not exist"
// from "Vault could not be reached", which a shell exit status could not
// distinguish.
var ErrNotFound = errors.New("not found")

// New connects using VAULT_ADDR and the given token.
func New(token string) (*Client, error) {
	addr := strings.TrimSpace(os.Getenv("VAULT_ADDR"))
	if addr == "" {
		return nil, ErrNoAddress
	}
	api, err := vault.New(
		vault.WithAddress(addr),
		vault.WithRequestTimeout(15*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("vault client for %s: %w", addr, err)
	}
	if token != "" {
		if err := api.SetToken(token); err != nil {
			return nil, fmt.Errorf("setting vault token: %w", err)
		}
	}
	return &Client{api: api, addr: addr}, nil
}

// Address is where this client is pointed, for messages that should say
// which Vault they mean.
//
// Read from the struct, not from the environment: a free function would
// re-read VAULT_ADDR, so if it changed after New the success message
// would name a Vault the write never touched - the failure looking like
// success that this package exists to avoid.
func (c *Client) Address() string { return c.addr }

// Role is a Kubernetes auth role, in the terms this tool cares about:
// which ServiceAccount, in which namespace, may assume which policies.
//
// TTLSeconds rather than a duration string: Vault accepts "1h" on write
// but returns 3600 on read, so a Role that round-trips has to hold the
// form both directions agree on. Storing "1h" made ReadRole(WriteRole(r))
// never equal r, which would have made any drift check report a change
// that had not happened.
type Role struct {
	ServiceAccounts []string
	Namespaces      []string
	Policies        []string
	TTLSeconds      int
}

// ReadRole returns the named Kubernetes auth role, or ErrNotFound.
func (c *Client) ReadRole(ctx context.Context, name string) (*Role, error) {
	resp, err := c.api.Auth.KubernetesReadAuthRole(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("role %s: %w", name, ErrNotFound)
		}
		return nil, fmt.Errorf("reading role %s: %w", name, err)
	}
	r := &Role{}
	if r.ServiceAccounts, err = strs(resp.Data["bound_service_account_names"]); err != nil {
		return nil, fmt.Errorf("role %s: bound_service_account_names: %w", name, err)
	}
	if r.Namespaces, err = strs(resp.Data["bound_service_account_namespaces"]); err != nil {
		return nil, fmt.Errorf("role %s: bound_service_account_namespaces: %w", name, err)
	}
	if r.Policies, err = strs(resp.Data["token_policies"]); err != nil {
		return nil, fmt.Errorf("role %s: token_policies: %w", name, err)
	}
	if n, ok := resp.Data["token_ttl"].(json.Number); ok {
		i, err := n.Int64()
		if err != nil {
			return nil, fmt.Errorf("role %s: token_ttl %q: %w", name, n, err)
		}
		r.TTLSeconds = int(i)
	}
	return r, nil
}

// WriteRole creates or replaces a Kubernetes auth role.
func (c *Client) WriteRole(ctx context.Context, name string, r Role) error {
	_, err := c.api.Auth.KubernetesWriteAuthRole(ctx, name,
		schema.KubernetesWriteAuthRoleRequest{
			BoundServiceAccountNames:      r.ServiceAccounts,
			BoundServiceAccountNamespaces: r.Namespaces,
			TokenPolicies:                 r.Policies,
			TokenTtl:                      strconv.Itoa(r.TTLSeconds) + "s",
		})
	if err != nil {
		return fmt.Errorf("writing role %s: %w", name, err)
	}
	return nil
}

// ReadPolicy returns an ACL policy document, or ErrNotFound.
func (c *Client) ReadPolicy(ctx context.Context, name string) (string, error) {
	resp, err := c.api.System.PoliciesReadAclPolicy(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("policy %s: %w", name, ErrNotFound)
		}
		return "", fmt.Errorf("reading policy %s: %w", name, err)
	}
	return resp.Data.Policy, nil
}

// WritePolicy creates or replaces an ACL policy.
func (c *Client) WritePolicy(ctx context.Context, name, document string) error {
	_, err := c.api.System.PoliciesWriteAclPolicy(ctx, name,
		schema.PoliciesWriteAclPolicyRequest{Policy: document})
	if err != nil {
		return fmt.Errorf("writing policy %s: %w", name, err)
	}
	return nil
}

// isNotFound reports whether Vault answered 404. The typed client keeps
// the status code, which is the distinction a shell exit status lost.
//
// The library's own helper rather than a hand-rolled errors.As: it also
// classifies a 404 that arrives via a redirect, which a direct type
// assertion on *ResponseError misses.
func isNotFound(err error) bool {
	return vault.IsErrorStatus(err, http.StatusNotFound)
}

// strs converts one of Vault's untyped JSON arrays into a string slice.
//
// It errors rather than skipping what it does not understand. Silently
// yielding nil made "this role binds no namespaces" indistinguishable
// from "Vault returned a shape we did not expect", and a drift check
// built on that would report a role as unbound - the unsafe direction.
func strs(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected a list, got %T", v)
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		s, ok := i.(string)
		if !ok {
			return nil, fmt.Errorf("expected a list of strings, found %T", i)
		}
		out = append(out, s)
	}
	return out, nil
}
