package render

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// Does AllErr return the PATCHED outputs or the raw All() outputs?
func TestAllErrReturnsPatched(t *testing.T) {
	c := base()
	c.Patches = map[string]string{"Deployment": "spec:\n  replicas: 7\n"}
	outs, err := AllErr(mustConfig(t, c), "img")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if o.Path == "deployment.yaml" {
			t.Logf("contains replicas: 7 = %v", strings.Contains(o.Body, "replicas: 7"))
		}
	}
}

// What does All() do when a patch is malformed? Does it silently emit unpatched?
func TestAllSwallowsPatchError(t *testing.T) {
	c := base()
	c.Patches = map[string]string{"Deployment": "spec:\n  this: is: not: yaml\n"}
	outs := All(mustConfig(t, c), "img")
	for _, o := range outs {
		if o.Path == "deployment.yaml" {
			t.Logf("All() emitted deployment despite bad patch, len=%d", len(o.Body))
		}
	}
}

// Patch applied to a doc whose kind appears in MULTIPLE files
func TestPatchNamespaceKind(t *testing.T) {
	c := base()
	c.Secrets = &config.Secrets{VaultPath: "svc/config", Keys: []string{"TOKEN"}}
	c.Patches = map[string]string{"ServiceAccount": "metadata:\n  annotations:\n    x: y\n"}
	outs, err := AllErr(mustConfig(t, c), "img")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	for _, o := range outs {
		if o.Path == "externalsecret.yaml" {
			t.Logf("externalsecret body:\n%s", o.Body)
		}
	}
}
