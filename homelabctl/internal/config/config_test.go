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
		Ingress: &Ingress{Hosts: IngressHosts("h.example.com"), Public: true, Authelia: true}}
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
			c.Ingress = &Ingress{Hosts: IngressHosts("h.example.com")}
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
		"ingress:\n  hosts: [h.example.com]\n  publik: true\n")
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

// A bare key derives its Vault property by lowercasing; a mapping states
// it. The mapping form exists because the derivation is wrong whenever
// the variable repeats its own app name - SHLINK_API_KEY under vaultPath
// `shlink` would ask for shlink/shlink_api_key, while the convention
// across this cluster is that the property does not repeat the path.
func TestSecretKeysDeriveOrStateTheirProperty(t *testing.T) {
	c := loadYAML(t, "name: a\nteam: t\nruntime: go-service\n"+
		"secrets:\n  vaultPath: shlink\n  keys:\n"+
		"    - NTFY_TOKEN\n"+
		"    - SHLINK_API_KEY: api-key\n")

	want := []SecretKey{
		{Env: "NTFY_TOKEN", Property: "ntfy_token"},
		{Env: "SHLINK_API_KEY", Property: "api-key"},
	}
	if len(c.Secrets.Keys) != len(want) {
		t.Fatalf("got %d keys, want %d", len(c.Secrets.Keys), len(want))
	}
	for i, w := range want {
		if c.Secrets.Keys[i] != w {
			t.Errorf("key %d = %+v, want %+v", i, c.Secrets.Keys[i], w)
		}
	}
}

// A mapping with more than one entry is a typo - almost certainly a
// missing "- " on the following line - and silently dropping one of them
// would leave the pod short an environment variable.
func TestMultiEntrySecretKeyMappingIsRejected(t *testing.T) {
	_, err := loadYAMLErr(t, "name: a\nteam: t\nruntime: go-service\n"+
		"secrets:\n  vaultPath: p\n  keys:\n    - A: one\n      B: two\n")
	if err == nil {
		t.Fatal("a two-entry mapping was accepted")
	}
}

// vaultPath is interpolated into a Vault policy, so anything Vault
// reads as a pattern widens the grant: "*" yields read on every secret
// in the mount, which is the cluster-wide role the per-app convention
// exists to replace.
// Both readers of vaultPath take it literally, so a pattern is not a
// broad grant - it is a path that resolves to nothing. Refusing it here
// turns a sync-time failure in the cluster into a validate-time error.
func TestVaultPathMustNameOneLiteralPath(t *testing.T) {
	for _, bad := range []string{"*", "shlink/*", "kv/*", "../other", "a/../../b", "/leading", "trailing/"} {
		c := Defaults()
		c.Name, c.Team, c.Runtime = "mysvc", "t", "go-service"
		c.Secrets = &Secrets{VaultPath: bad, Keys: EnvKeys("TOK")}
		if err := c.Complete(); err == nil {
			t.Errorf("vaultPath %q was accepted", bad)
		}
	}
}

// A service that fronts another legitimately reads its secret - the
// shlink redirector reads shlink's api-key - so the path is not
// required to equal the service name.
func TestVaultPathMayNameAnotherService(t *testing.T) {
	for _, ok := range []string{"shlink", "approvald/config", "mysvc", "team/mysvc/config"} {
		c := Defaults()
		c.Name, c.Team, c.Runtime = "mysvc", "t", "go-service"
		c.Secrets = &Secrets{VaultPath: ok, Keys: EnvKeys("TOK")}
		if err := c.Complete(); err != nil {
			t.Errorf("vaultPath %q was refused: %v", ok, err)
		}
	}
}

// A malformed prefix produces an Ingress nginx accepts and routes wrongly,
// which looks like a bug in the service rather than in its config.
func TestIngressPathMustBeAUsablePrefix(t *testing.T) {
	for _, bad := range []string{"api", "/api/", "/a//b"} {
		c := Defaults()
		c.Name, c.Team, c.Runtime = "svc", "t", "go-service"
		c.Ingress = &Ingress{Hosts: IngressHosts("svc.example.com"), Path: bad}
		if err := c.Complete(); err == nil {
			t.Errorf("ingress.path %q was accepted", bad)
		}
	}
	for _, ok := range []string{"", "/", "/api", "/api/v2"} {
		c := Defaults()
		c.Name, c.Team, c.Runtime = "svc", "t", "go-service"
		c.Ingress = &Ingress{Hosts: IngressHosts("svc.example.com"), Path: ok}
		if err := c.Complete(); err != nil {
			t.Errorf("ingress.path %q was refused: %v", ok, err)
		}
	}
}

// A runtime's memory floor is a property of the runtime.
//
// One pair of numbers served every runtime, sized for Go. The deployed
// pokedex-web sat at 42Mi a minute after starting against a 64Mi limit
// and was OOMKilled after five hours - exit 137, a 502 for whoever was
// looking. The Go service beside it uses 8Mi.
func TestMemoryDefaultsFollowTheRuntime(t *testing.T) {
	node := Defaults()
	node.Name, node.Team, node.Runtime = "svc", "platform", "node-service"
	node.Port = 3000
	node.Image.Repository = "ghcr.io/example/svc"
	if err := node.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if node.Resources.MemoryLimit != "256Mi" {
		t.Errorf("node-service limit = %q, want 256Mi; a Node process does not fit in Go's",
			node.Resources.MemoryLimit)
	}

	goSvc := Defaults()
	goSvc.Name, goSvc.Team, goSvc.Runtime = "svc", "platform", "go-service"
	goSvc.Port = 8080
	goSvc.Image.Repository = "ghcr.io/example/svc"
	if err := goSvc.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if goSvc.Resources.MemoryLimit != "64Mi" {
		t.Errorf("go-service limit = %q, want 64Mi unchanged", goSvc.Resources.MemoryLimit)
	}
}

// An explicit resources: block wins over whatever the runtime would
// default to - otherwise a service that genuinely needs more has no way
// to say so.
func TestExplicitResourcesOverrideTheRuntimeDefault(t *testing.T) {
	c := Defaults()
	c.Name, c.Team, c.Runtime = "svc", "platform", "node-service"
	c.Port = 3000
	c.Image.Repository = "ghcr.io/example/svc"
	c.Resources = &Resources{CPURequest: "10m", MemoryRequest: "200Mi", MemoryLimit: "512Mi"}
	if err := c.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if c.Resources.MemoryLimit != "512Mi" || c.Resources.MemoryRequest != "200Mi" {
		t.Errorf("explicit resources were overwritten: %+v", c.Resources)
	}
}

// A range violation must not read as an exact requirement.
//
// The library prints "minimum: got -1, want 1" for `minimum: 1`, which
// reads as "1 is the only valid value". It is not - replicas: 2 is
// fine - and somebody checking that wording goes looking for a
// constraint that does not exist.
func TestRangeErrorsSayAtLeastAndAtMost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"minimum: got -1, want 1", "minimum: got -1, want at least 1"},
		{"maximum: got 99,999, want 65,535", "maximum: got 99,999, want at most 65,535"},
		{"type: got string, want integer", "type: got string, want integer"},
	} {
		if got := clarifyBound(tc.in); got != tc.want {
			t.Errorf("clarifyBound(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// And the values a bound permits are actually accepted, so the wording
// is describing real behaviour.
func TestReplicasAboveOneAreValid(t *testing.T) {
	for _, n := range []int{1, 2, 3, 10} {
		c := Defaults()
		c.Name, c.Team, c.Runtime = "svc", "platform", "go-service"
		c.Port = 8080
		c.Image.Repository = "ghcr.io/example/svc"
		c.Replicas = n
		if err := c.Complete(); err != nil {
			t.Errorf("replicas: %d was rejected: %v", n, err)
		}
	}
	// Zero means "unset" to Complete, because it is Go's zero value and
	// a struct built in code cannot say "explicitly zero" - it defaults
	// to 1. A config FILE does not have that ambiguity, and the schema
	// rejects `replicas: 0` before Complete ever sees it, which is the
	// path a user takes.
	c := Defaults()
	c.Name, c.Team, c.Runtime = "svc", "platform", "go-service"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/svc"
	c.Replicas = 0
	if err := c.Complete(); err != nil {
		t.Errorf("an unset replicas should default, not fail: %v", err)
	}
	if c.Replicas != 1 {
		t.Errorf("unset replicas defaulted to %d, want 1", c.Replicas)
	}
	// Negative is unambiguous, and must fail.
	c.Replicas = -1
	if err := c.Complete(); err == nil {
		t.Error("replicas: -1 was accepted")
	}
}
