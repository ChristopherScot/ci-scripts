package config

import (
	"strings"
	"testing"
)

func TestValidateReportsAllProblemsAtOnce(t *testing.T) {
	err := (&Config{}).Validate()
	if err == nil {
		t.Fatal("empty config validated")
	}
	for _, want := range []string{"name is required", "team is required", "runtime is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q; got:\n%s", want, err)
		}
	}
}

// Authelia resolves on the LAN only, so pairing it with a public host
// yields an endpoint that dead-ends off-network - invisible until someone
// tries it from cellular.
func TestPublicIngressRejectsAuthelia(t *testing.T) {
	c := &Config{Name: "a", Team: "t", Runtime: "go-service",
		Ingress: &Ingress{Host: "h.example.com", Public: true, Authelia: true}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "authelia") {
		t.Errorf("Validate() = %v, want an authelia/public conflict", err)
	}
}

func TestDefaultsAppliedByValidate(t *testing.T) {
	c := &Config{Name: "a", Team: "t", Runtime: "go-service"}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if c.Namespace != "a" || c.Replicas != 1 || !c.Hardened() {
		t.Errorf("defaults not applied: ns=%q replicas=%d hardened=%v", c.Namespace, c.Replicas, c.Hardened())
	}
}

func TestInvalidNameRejected(t *testing.T) {
	for _, n := range []string{"With-Caps", "1leading", "has_underscore", "-leading"} {
		c := &Config{Name: n, Team: "t", Runtime: "go-service"}
		if err := c.Validate(); err == nil {
			t.Errorf("name %q was accepted", n)
		}
	}
}
