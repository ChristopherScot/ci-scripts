package main

import (
	"os"
	"path/filepath"
	"testing"
)

// `render --out deploy` writes deploy/<service>/, so check has to look
// there. It used to look in deploy/ itself and reported a missing
// kustomization.yaml for every service - a message naming a real hazard
// that was not actually happening, which is worse than no check.
func TestManifestDirFindsTheRenderedLayout(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "shlink-redirector")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(svc, "kustomization.yaml"), []byte("images:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := manifestDir(dir); got != svc {
		t.Errorf("manifestDir = %q, want %q", got, svc)
	}
}

// A flat directory - what someone gets copying manifests by hand - must
// still be checked where they say it is.
func TestManifestDirKeepsAFlatLayout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("images:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := manifestDir(dir); got != dir {
		t.Errorf("manifestDir = %q, want the directory itself %q", got, dir)
	}
}

// Two services under one directory is ambiguous; guessing one would
// check the wrong thing silently.
func TestManifestDirDoesNotGuessBetweenServices(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b"} {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "kustomization.yaml"), []byte("images:\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := manifestDir(dir); got != dir {
		t.Errorf("manifestDir = %q, want it to fall back to %q", got, dir)
	}
}
