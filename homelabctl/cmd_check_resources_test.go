package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkResources is the check that catches a class rather than a bug.
//
// Argo applies exactly what `resources:` lists, so both ways for that
// list to be wrong are silent: a file on disk nothing lists is never
// applied, and a listed file that is absent fails the whole sync.
//
// Three separate bugs produced the first shape - a manifest colliding
// with a generated name, a stale kustomization.yaml kept by init on a
// re-run, and a .yml file check never opened - and each would have been
// caught here without knowing how it arose.
func TestCheckResourcesCrossReferences(t *testing.T) {
	const k = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - service.yaml
`
	for _, tc := range []struct {
		name    string
		files   []string
		kust    string
		wantSub string
	}{
		{
			name:    "a manifest nothing lists is never applied",
			files:   []string{"deployment.yaml", "service.yaml", "db.yaml"},
			kust:    k,
			wantSub: "db.yaml is in",
		},
		{
			name:    "a .yml manifest counts too",
			files:   []string{"deployment.yaml", "service.yaml", "db.yml"},
			kust:    k,
			wantSub: "db.yml is in",
		},
		{
			name:    "a listed file that is absent fails the sync",
			files:   []string{"deployment.yaml"},
			kust:    k,
			wantSub: "lists service.yaml, which is not in",
		},
		{
			name:    "a duplicate entry makes kustomize refuse the directory",
			files:   []string{"deployment.yaml", "service.yaml"},
			kust:    k + "  - service.yaml\n",
			wantSub: "2 times",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range tc.files {
				body := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"
				if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(tc.kust), 0o644); err != nil {
				t.Fatal(err)
			}

			var problems []string
			checkResources(dir, tc.kust, func(f string, a ...any) {
				problems = append(problems, strings.TrimSpace(fmt.Sprintf(f, a...)))
			})

			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, tc.wantSub) {
				t.Errorf("no problem mentioning %q; got:\n%s", tc.wantSub, joined)
			}
		})
	}
}

// A correctly rendered directory produces no complaints, or the check
// is noise and gets ignored.
func TestCheckResourcesQuietWhenConsistent(t *testing.T) {
	dir := t.TempDir()
	const k = `resources:
  - deployment.yaml
  - service.yaml
`
	for _, f := range []string{"deployment.yaml", "service.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f),
			[]byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(k), 0o644); err != nil {
		t.Fatal(err)
	}

	var problems []string
	checkResources(dir, k, func(f string, a ...any) {
		problems = append(problems, fmt.Sprintf(f, a...))
	})
	if len(problems) != 0 {
		t.Errorf("a consistent directory reported problems: %v", problems)
	}
}
