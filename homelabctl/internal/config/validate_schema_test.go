package config

import (
	"strings"
	"testing"
)

// A malformed Schema is a build mistake, not a runtime condition, so
// mustCompileSchema panics. This is what turns that into a test failure.
func TestSchemaCompiles(t *testing.T) {
	if compiled == nil {
		t.Fatal("Schema did not compile")
	}
}

// The constraints the schema states are now enforced by the CLI, not
// only shown in an editor. Each of these decodes into a valid Go value,
// so nothing but the schema rejects them.
func TestSchemaConstraintsAreEnforced(t *testing.T) {
	base := "name: a\nteam: t\nruntime: go-service\n"
	for _, tc := range []struct{ name, yaml, want string }{
		{"replicas below minimum", base + "replicas: 0\n", "replicas"},
		{"port above maximum", base + "port: 70000\n", "port"},
		{"port below minimum", base + "port: 0\n", "port"},
		{"name is not a DNS label", "name: With-Caps\nteam: t\nruntime: go-service\n", "name"},
		{"unknown top-level key", base + "bogus: 1\n", "bogus"},
		{"unknown nested key", base + "ingress:\n  host: h.example.com\n  publik: true\n", "publik"},
		{"kind outside the enum", base + "kind: daemonset\n", "kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadYAMLErr(t, tc.yaml)
			if err == nil {
				t.Fatalf("accepted:\n%s", tc.yaml)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name %q: %v", tc.want, err)
			}
		})
	}
}

// Schema validation must not reject what the tool itself writes.
func TestScaffoldedConfigPassesItsOwnSchema(t *testing.T) {
	valid := "name: svc\nteam: t\nruntime: go-service\nport: 3000\n" +
		"image:\n  repository: ghcr.io/o/svc\n" +
		"secrets:\n  vaultPath: svc\n  keys: [TOKEN]\n" +
		"probes:\n  path: /health\n" +
		"resources:\n  cpuRequest: 10m\n  memoryRequest: 16Mi\n  memoryLimit: 64Mi\n" +
		"env:\n  LOG_LEVEL: info\n"
	if _, err := loadYAMLErr(t, valid); err != nil {
		t.Fatalf("a valid config was rejected: %v", err)
	}
}

// Several mistakes should be reported together, not one per run.
func TestSchemaReportsEveryProblemAtOnce(t *testing.T) {
	_, err := loadYAMLErr(t, "name: a\nteam: t\nruntime: go-service\nreplicas: 0\nport: 70000\n")
	if err == nil {
		t.Fatal("accepted a config with two problems")
	}
	for _, want := range []string{"replicas", "port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q; got:\n%v", want, err)
		}
	}
}

// The cross-field rules a schema cannot express stay in Validate, so
// they must still fire after schema validation passes.
func TestSemanticRulesStillApply(t *testing.T) {
	_, err := loadYAMLErr(t, "name: a\nteam: t\nruntime: go-service\n"+
		"ingress:\n  host: h.example.com\n  public: true\n  authelia: true\n")
	if err == nil || !strings.Contains(err.Error(), "authelia") {
		t.Errorf("Load() = %v, want the authelia/public conflict", err)
	}
}
