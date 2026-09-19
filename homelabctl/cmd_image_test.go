package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// One bare line and nothing else: the caller is a shell doing
//
//	echo "ref=$(./homelabctl image)" >> "$GITHUB_OUTPUT"
//
// so anything extra on stdout - a banner, a trailing comment, a second
// line - lands in the workflow's image name.
func TestImagePrintsOnlyTheRepository(t *testing.T) {
	p := writeConfig(t, t.TempDir(),
		"name: svc\nteam: t\nruntime: go-service\nspec: false\n"+
			"image:\n    repository: ghcr.io/o/custom\n")

	var out bytes.Buffer
	if err := runImage(&out, p); err != nil {
		t.Fatalf("runImage() = %v", err)
	}
	if got := out.String(); got != "ghcr.io/o/custom\n" {
		t.Errorf("output = %q, want exactly the repository and a newline", got)
	}
}

// The image need not be named after the service: go-shlink-redirector
// publishes ghcr.io/christopherscot/go-shlink-redirector while its
// service name is shlink-redirector. Whatever config.yaml says is what
// CI must push.
func TestImageReportsANameThatDoesNotMatchTheService(t *testing.T) {
	p := writeConfig(t, t.TempDir(),
		"name: shlink-redirector\nteam: t\nruntime: go-service\nspec: false\n"+
			"image:\n    repository: ghcr.io/o/go-shlink-redirector\n")

	var out bytes.Buffer
	if err := runImage(&out, p); err != nil {
		t.Fatalf("runImage() = %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "ghcr.io/o/go-shlink-redirector" {
		t.Errorf("output = %q, want the repo-named image", got)
	}
}

// An empty images: input does not fail docker/metadata-action - it
// produces no tags, and build-push-action then pushes nothing while the
// job stays green. That is the same silent failure one step over, so
// this has to die here rather than print an empty line.
func TestImageRefusesWhenTheRepositoryIsUnset(t *testing.T) {
	p := writeConfig(t, t.TempDir(),
		"name: svc\nteam: t\nruntime: go-service\nspec: false\n")

	var out bytes.Buffer
	err := runImage(&out, p)
	if err == nil {
		t.Fatalf("an unset repository was accepted, printing %q", out.String())
	}
	if out.Len() != 0 {
		t.Errorf("wrote %q to stdout while failing", out.String())
	}
}

// A rename is a SUPPORTED edit: change config.yaml and CI follows,
// because CI reads this rather than a copy of it. This is the whole
// point of the subcommand, so it is pinned.
func TestImageFollowsARenameInConfig(t *testing.T) {
	dir := t.TempDir()
	p := writeConfig(t, dir,
		"name: svc\nteam: t\nruntime: go-service\nspec: false\n"+
			"image:\n    repository: ghcr.io/o/before\n")

	var first bytes.Buffer
	if err := runImage(&first, p); err != nil {
		t.Fatal(err)
	}

	writeConfig(t, dir,
		"name: svc\nteam: t\nruntime: go-service\nspec: false\n"+
			"image:\n    repository: ghcr.io/o/after\n")

	var second bytes.Buffer
	if err := runImage(&second, p); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(second.String()) != "ghcr.io/o/after" {
		t.Errorf("after the rename = %q, want ghcr.io/o/after", second.String())
	}
	if first.String() == second.String() {
		t.Error("the rename did not reach the output")
	}
}

// An invalid config must fail loudly rather than print something the
// workflow would then push to.
func TestImageRefusesAnInvalidConfig(t *testing.T) {
	p := writeConfig(t, t.TempDir(), "name: svc\nteam: t\nruntime: nonsense\n")
	var out bytes.Buffer
	if err := runImage(&out, p); err == nil {
		t.Errorf("an invalid config was accepted, printing %q", out.String())
	}
}
