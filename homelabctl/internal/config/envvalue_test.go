package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The reason the dual form exists: every config written before
// secretKeyRef must decode exactly as it always did.
func TestEnvValueDecodesBareScalar(t *testing.T) {
	var got map[string]EnvValue
	const src = "LOG_LEVEL: debug\nPORT: \"3000\"\n"
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if got["LOG_LEVEL"].Literal != "debug" {
		t.Errorf("LOG_LEVEL = %q, want debug", got["LOG_LEVEL"].Literal)
	}
	if got["LOG_LEVEL"].Secret != nil {
		t.Error("a bare scalar decoded as a secret reference")
	}
	if got["PORT"].Literal != "3000" {
		t.Errorf("PORT = %q, want 3000", got["PORT"].Literal)
	}
}

func TestEnvValueDecodesSecretKeyRef(t *testing.T) {
	var got map[string]EnvValue
	const src = "DATABASE_URL:\n  secretKeyRef:\n    name: pokedex-db-app\n    key: uri\n"
	if err := yaml.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	ref := got["DATABASE_URL"].Secret
	if ref == nil {
		t.Fatal("DATABASE_URL did not decode as a secret reference")
	}
	if ref.Name != "pokedex-db-app" || ref.Key != "uri" {
		t.Errorf("got %s/%s, want pokedex-db-app/uri", ref.Name, ref.Key)
	}
	if got["DATABASE_URL"].Literal != "" {
		t.Errorf("a reference also set Literal to %q", got["DATABASE_URL"].Literal)
	}
}

// A half-written reference should fail at load, naming the file,
// rather than rendering a manifest that applies cleanly and leaves the
// pod in CreateContainerConfigError.
func TestEnvValueRejectsIncompleteRef(t *testing.T) {
	for name, src := range map[string]string{
		"no key":      "X:\n  secretKeyRef:\n    name: a\n",
		"no name":     "X:\n  secretKeyRef:\n    key: b\n",
		"empty ref":   "X:\n  secretKeyRef: {}\n",
		"wrong field": "X:\n  configMapKeyRef:\n    name: a\n    key: b\n",
	} {
		t.Run(name, func(t *testing.T) {
			var got map[string]EnvValue
			err := yaml.Unmarshal([]byte(src), &got)
			if err == nil {
				t.Fatalf("decoded without error, want a failure")
			}
			// The message has to say what the shape should be; a
			// reader hits this while writing YAML, not Go.
			if !strings.Contains(err.Error(), "secretKeyRef") {
				t.Errorf("error %q does not mention secretKeyRef", err)
			}
		})
	}
}

// Round-trip: a config that is loaded and written back must not change
// shape. The asymmetry this guards against is the one SecretKey's
// comment describes - it stays silent until something re-marshals, and
// then fails on a file nobody edited.
func TestEnvValueRoundTrips(t *testing.T) {
	// name before key, matching the struct and Kubernetes' own docs.
	const src = "DATABASE_URL:\n    secretKeyRef:\n        name: pokedex-db-app\n        key: uri\nLOG_LEVEL: debug\n"

	var decoded map[string]EnvValue
	if err := yaml.Unmarshal([]byte(src), &decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	out, err := yaml.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	if string(out) != src {
		t.Errorf("round trip changed the file:\n got:\n%s\nwant:\n%s", out, src)
	}
}
