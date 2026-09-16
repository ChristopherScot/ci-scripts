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
	if err := c.Complete(); err != nil {
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

func TestCronJobValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"schedule required", func(c *Config) { c.Kind = KindCronJob }, "schedule is required"},
		{"no ingress", func(c *Config) {
			c.Kind, c.Schedule = KindCronJob, "* * * * *"
			c.Ingress = &Ingress{Host: "h.example.com"}
		}, "cannot have an ingress"},
		{"schedule needs cronjob", func(c *Config) { c.Schedule = "* * * * *" }, "only meaningful for kind: cronjob"},
		{"unknown kind", func(c *Config) { c.Kind = "daemonset" }, "not one of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Name: "svc", Team: "t", Runtime: "go-service", Port: 3000}
			tc.mut(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// Validate must not touch the Config. It used to apply defaults first,
// which meant a failed validation still mutated the receiver: a caller
// that validated, saw an error, fixed one field and validated again was
// working on a half-defaulted struct.
func TestValidateDoesNotMutate(t *testing.T) {
	c := Config{Name: "svc"} // missing runtime and image: will fail
	if err := c.Validate(); err == nil {
		t.Fatal("Validate() accepted a config with no runtime or image")
	}
	if c.Kind != "" || c.Replicas != 0 || c.Port != 0 || c.Probes != nil || c.Resources != nil {
		t.Errorf("Validate() mutated the Config: kind=%q replicas=%d port=%d probes=%v resources=%v",
			c.Kind, c.Replicas, c.Port, c.Probes, c.Resources)
	}
}

// Complete is the one that fills things in.
func TestCompleteAppliesDefaults(t *testing.T) {
	c := Config{Name: "svc", Team: "t", Runtime: "go-service",
		Image: Image{Repository: "ghcr.io/o/svc"}}
	if err := c.Complete(); err != nil {
		t.Fatalf("Complete() = %v", err)
	}
	if c.Kind != KindService || c.Replicas != 1 || c.Port != DefaultPort ||
		c.Probes == nil || c.Resources == nil {
		t.Errorf("Complete() left the Config incomplete: %+v", c)
	}
}
