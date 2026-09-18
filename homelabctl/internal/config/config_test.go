package config

import (
	"os"
	"path/filepath"
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
	c := Defaults()
	c.Name, c.Team, c.Runtime = "a", "t", "go-service"
	if err := c.Complete(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if c.Namespace != "a" || c.Replicas != 1 || !c.Hardened {
		t.Errorf("defaults not applied: ns=%q replicas=%d hardened=%v", c.Namespace, c.Replicas, c.Hardened)
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

// The whole point of decoding over Defaults(): an omitted key keeps its
// default instead of becoming Go's zero value.
func TestOmittedFieldsKeepTheirDefaults(t *testing.T) {
	c := loadYAML(t, "name: a\nteam: t\nruntime: go-service\n")

	if !c.Hardened {
		t.Error("hardened defaulted to false; an omitted key shipped an unhardened pod")
	}
	if !c.Metrics {
		t.Error("metrics defaulted to false; the pod would never be scraped")
	}
	if !c.Spec {
		t.Error("spec defaulted to false; the service would scaffold specless")
	}
	if c.Port != DefaultPort {
		t.Errorf("port = %d, want %d", c.Port, DefaultPort)
	}
	if c.Probes.Path != DefaultProbePath {
		t.Errorf("probes.path = %q, want %q", c.Probes.Path, DefaultProbePath)
	}
	if c.Resources.MemoryLimit != "64Mi" {
		t.Errorf("resources.memoryLimit = %q, want 64Mi", c.Resources.MemoryLimit)
	}
}

// The other half: an explicit false has to win over the default, which is
// the case a plain bool cannot express without seeding.
func TestExplicitFalseOverridesTheDefault(t *testing.T) {
	c := loadYAML(t, "name: a\nteam: t\nruntime: go-service\nhardened: false\nspec: false\n")

	if c.Hardened {
		t.Error("hardened: false was ignored")
	}
	if c.Spec {
		t.Error("spec: false was ignored")
	}
	// Untouched keys still default.
	if !c.Metrics {
		t.Error("metrics was disabled by a neighbouring key")
	}
}

// A partial nested block must not wipe its siblings' defaults.
func TestPartialNestedBlockKeepsSiblingDefaults(t *testing.T) {
	c := loadYAML(t, "name: a\nteam: t\nruntime: go-service\n"+
		"resources:\n  cpuRequest: 50m\n")

	if c.Resources.CPURequest != "50m" {
		t.Errorf("cpuRequest = %q, want 50m", c.Resources.CPURequest)
	}
	if c.Resources.MemoryRequest != "32Mi" {
		t.Errorf("memoryRequest = %q, want the default 32Mi", c.Resources.MemoryRequest)
	}
}

// The typo that motivated all of this. It must be an error, not silence.
func TestTypoIsRejected(t *testing.T) {
	if _, err := loadYAMLErr(t, "name: a\nteam: t\nruntime: go-service\nhardend: false\n"); err == nil {
		t.Fatal("`hardend: false` was accepted; it would ship an unhardened deploy")
	}
}

// Nested keys were NOT checked before: the hand-maintained key list only
// covered the top level, so `ingress.publik` shipped a LAN-only ingress
// when the author asked for a public one.
func TestNestedTypoIsRejected(t *testing.T) {
	_, err := loadYAMLErr(t, "name: a\nteam: t\nruntime: go-service\n"+
		"ingress:\n  host: h.example.com\n  publik: true\n")
	if err == nil {
		t.Fatal("`ingress.publik` was accepted; the ingress would not be public")
	}
	if !strings.Contains(err.Error(), "publik") {
		t.Errorf("error does not name the offending key: %v", err)
	}
}

// An empty file is not a parse error - it is a config that omits
// everything, and should fail validation by naming what is missing.
func TestEmptyFileReportsMissingFieldsNotAParseError(t *testing.T) {
	_, err := loadYAMLErr(t, "")
	if err == nil {
		t.Fatal("an empty config validated")
	}
	if strings.Contains(err.Error(), "EOF") {
		t.Errorf("empty file reported as a parse failure: %v", err)
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error does not say what to fix: %v", err)
	}
}

func loadYAML(t *testing.T, body string) *Config {
	t.Helper()
	c, err := loadYAMLErr(t, body)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	return c
}

func loadYAMLErr(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}
