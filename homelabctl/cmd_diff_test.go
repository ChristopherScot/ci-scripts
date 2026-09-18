package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
)

// renderInto writes a service's manifests where render would put them,
// and returns the config path.
func renderInto(t *testing.T, dir string) string {
	t.Helper()
	c := config.Defaults()
	c.Name = "svc"
	c.Team = "platform"
	c.Runtime = "go-service"
	c.Port = 8080
	c.Namespace = "svc"
	c.Image.Repository = "ghcr.io/example/svc"

	cfgPath := filepath.Join(dir, "config.yaml")
	const cfgYAML = `name: svc
team: platform
runtime: go-service
namespace: svc
port: 8080
image:
  repository: ghcr.io/example/svc
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// Loaded back, so the test compares against what runDiff will see
	// rather than against a Config built a second way.
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	c = *loaded

	outs, err := render.All(&c, render.Source{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := filepath.Join(dir, "deploy", c.Name)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if err := os.WriteFile(filepath.Join(out, o.Path), []byte(o.Body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cfgPath
}

// A freshly rendered service has no differences.
//
// diff used to compare against the GitOps repo, from when manifests were
// copied there by hand. Argo reads them from the service's own repo now,
// and the GitOps directory holds only argocd.json - so every manifest
// came back "(new)" for a service that was deployed and running. Four
// false differences out of five outputs, on every run, which is how an
// operator learns to ignore the command.
func TestDiffIsQuietWhenNothingChanged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := renderInto(t, dir)

	if err := runDiff(cfgPath); err != nil {
		t.Errorf("a freshly rendered service reports differences: %v", err)
	}
}

// And it still reports a real one.
//
// The inverse matters as much: a diff that never fires is the same
// useless as one that always does.
func TestDiffReportsDrift(t *testing.T) {
	dir := t.TempDir()
	cfgPath := renderInto(t, dir)

	svc := filepath.Join(dir, "deploy", "svc", "service.yaml")
	body, err := os.ReadFile(svc)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(body), "name: svc", "name: svc-drifted", 1)
	if edited == string(body) {
		t.Fatal("test setup: nothing replaced in service.yaml")
	}
	if err := os.WriteFile(svc, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	err = runDiff(cfgPath)
	if err == nil {
		t.Fatal("an edited manifest reported no difference")
	}
	if !strings.Contains(err.Error(), "service.yaml") {
		t.Errorf("error does not name the file that drifted: %v", err)
	}
}

// diff must not claim "up to date" about a directory it never read.
func TestDiffRefusesWhenNothingIsRendered(t *testing.T) {
	dir := t.TempDir()
	cfgPath := renderInto(t, dir)
	if err := os.RemoveAll(filepath.Join(dir, "deploy")); err != nil {
		t.Fatal(err)
	}

	err := runDiff(cfgPath)
	if err == nil {
		t.Fatal("reported success with no rendered manifests to compare")
	}
	if !strings.Contains(err.Error(), "render") {
		t.Errorf("error should point at `homelabctl render`: %v", err)
	}
}
