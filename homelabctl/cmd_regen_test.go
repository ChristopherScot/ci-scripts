package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func regenFixture(t *testing.T, specVersion, goVersion, pkgVersion string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("openapi.yml", "openapi: 3.0.3\ninfo:\n  title: t\n  version: "+specVersion+"\npaths: {}\n")
	write("api/client.go", "package api\n\nconst ClientVersion = \""+goVersion+"\"\n")
	write("package.json", "{\n  \"name\": \"@o/t-client\",\n  \"version\": \""+pkgVersion+"\"\n}\n")
	return dir
}

func TestSyncVersionsReportsWhatIsStale(t *testing.T) {
	dir := regenFixture(t, "0.3.0", "0.1.0", "0.2.0")

	stale, err := syncVersions(dir, "0.3.0", true)
	if err != nil {
		t.Fatalf("syncVersions: %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("stale = %v, want both files", stale)
	}

	// --check must not write.
	b, _ := os.ReadFile(filepath.Join(dir, "api", "client.go"))
	if !strings.Contains(string(b), `"0.1.0"`) {
		t.Error("--check modified api/client.go")
	}
}

func TestSyncVersionsRewritesBoth(t *testing.T) {
	dir := regenFixture(t, "0.3.0", "0.1.0", "0.2.0")

	if _, err := syncVersions(dir, "0.3.0", false); err != nil {
		t.Fatalf("syncVersions: %v", err)
	}
	for _, f := range []string{"api/client.go", "package.json"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "0.3.0") {
			t.Errorf("%s was not updated to the spec version:\n%s", f, b)
		}
	}
}

func TestSyncVersionsIsQuietWhenCurrent(t *testing.T) {
	dir := regenFixture(t, "1.0.0", "1.0.0", "1.0.0")
	stale, err := syncVersions(dir, "1.0.0", false)
	if err != nil {
		t.Fatalf("syncVersions: %v", err)
	}
	if len(stale) != 0 {
		t.Errorf("reported %v for an up-to-date service", stale)
	}
}

// Rewriting package.json by re-marshalling would reorder keys and
// reformat a file a person maintains.
func TestPackageVersionEditPreservesTheFile(t *testing.T) {
	in := []byte("{\n  \"name\": \"@o/t\",\n  \"version\": \"0.1.0\",\n  \"files\": [\n    \"clients/ts\"\n  ]\n}\n")
	out, changed, err := setPackageVersion(in, "0.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("reported no change")
	}
	got := string(out)
	if !strings.Contains(got, `"version": "0.4.0"`) {
		t.Errorf("version not set:\n%s", got)
	}
	// Everything else must survive byte for byte.
	for _, want := range []string{`"name": "@o/t"`, "\"files\": [\n    \"clients/ts\"\n  ]"} {
		if !strings.Contains(got, want) {
			t.Errorf("rewriting the version disturbed the rest of the file:\n%s", got)
		}
	}
}

func TestSpecVersionRequiresAVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openapi.yml"),
		[]byte("openapi: 3.0.3\ninfo:\n  title: t\npaths: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := specVersion(dir); err == nil {
		t.Error("accepted a spec with no info.version")
	}
}
