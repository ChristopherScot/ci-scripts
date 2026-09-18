package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// The bug this guards: configYAML used to hand-write seven fields and
// silently drop fourteen, so a Config that had been through Load could
// not be written back without losing most of itself.
func TestConfigYAMLRoundTrips(t *testing.T) {
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = "svc", "t", "go-service"
	c.Module = "github.com/o/go-svc"
	c.Namespace = "other"
	c.Port = 8080
	c.Replicas = 3
	c.Image = config.Image{Repository: "ghcr.io/o/svc"}
	c.Env = map[string]string{"LOG_LEVEL": "debug"}
	c.Secrets = &config.Secrets{VaultPath: "svc", Keys: []config.SecretKey{{Env: "TOKEN", Property: "api-key"}}}
	c.Ingress = &config.Ingress{Hosts: config.IngressHosts("svc.example.com", "svc.lab"), Authelia: true}
	c.Probes = &config.Probes{Path: "/health"}
	c.Resources = &config.Resources{CPURequest: "50m", MemoryRequest: "64Mi", MemoryLimit: "128Mi"}
	c.Patches = map[string]string{"Deployment": "spec: {}"}
	c.Hardened, c.Metrics, c.Spec = false, false, false

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(configYAML(&c)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("the config we just wrote does not load: %v\n%s", err, configYAML(&c))
	}
	if !reflect.DeepEqual(*got, c) {
		t.Errorf("round trip lost data:\n written:\n%s\n got: %+v\nwant: %+v", configYAML(&c), *got, c)
	}
}

// Defaults must not be restated: a spelled-out default stops tracking the
// default when it later changes.
func TestConfigYAMLOmitsDefaults(t *testing.T) {
	c := config.Defaults()
	c.Name, c.Team, c.Runtime = "svc", "t", "go-service"
	c.Namespace = "svc"
	c.Image = config.Image{Repository: "ghcr.io/o/svc"}

	out := configYAML(&c)
	for _, unwanted := range []string{"kind:", "replicas:", "namespace:", "probes:", "resources:"} {
		if contains(out, unwanted) {
			t.Errorf("restates the default %q:\n%s", unwanted, out)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
