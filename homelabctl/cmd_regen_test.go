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
	// clients/ts/, where the generated client actually lives. The
	// fixture used to write this at the service root, which is why a
	// path that had stopped matching reality still passed.
	write("clients/ts/package.json", "{\n  \"name\": \"@o/t-client\",\n  \"version\": \""+pkgVersion+"\"\n}\n")
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
	for _, f := range []string{"api/client.go", "clients/ts/package.json"} {
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

// regen has no --owner: it is given a service directory and nothing
// else. The owner is the npm scope the TypeScript client publishes
// under, so losing it renders "@/name-client", which npm rejects - at
// publish time, long after regen reported success.
func TestOwnerComesFromTheModulePath(t *testing.T) {
	for _, tc := range []struct{ mod, want string }{
		{"module github.com/acme/svc\n", "acme"},
		{"module github.com/acme/mono/services/svc\n\ngo 1.22\n", "acme"},
		{"module svc\n", ""}, // no owner to find
		{"", ""},             // no go.mod at all
	} {
		dir := t.TempDir()
		if tc.mod != "" {
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tc.mod), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := ownerFromModule(dir); got != tc.want {
			t.Errorf("ownerFromModule(%q) = %q, want %q", tc.mod, got, tc.want)
		}
	}
}

// The TypeScript client's package.json must carry the SPEC's version,
// not the version a service was created with.
//
// Two separate bugs made it lie, and both were invisible:
//   - syncVersions looked for package.json at the service root, where it
//     used to live. os.ReadFile failed silently after the client moved to
//     clients/ts/, so nothing was ever rewritten.
//   - artifactParams hardcoded InitialSpecVersion, so regen rendered the
//     template with 0.1.0 whatever openapi.yml said.
//
// The result was an npm package advertising 0.1.0 while containing a
// 0.2.0 API - a consumer pinning ^0.1.0 would get battle endpoints it
// had no reason to expect.
func TestSpecVersionReachesTheTypeScriptClient(t *testing.T) {
	dir := regenFixture(t, "0.4.0", "0.1.0", "0.1.0")

	stale, err := syncVersions(dir, "0.4.0", false)
	if err != nil {
		t.Fatal(err)
	}
	var sawPkg bool
	for _, f := range stale {
		if f == filepath.Join("clients", "ts", "package.json") {
			sawPkg = true
		}
	}
	if !sawPkg {
		t.Errorf("stale = %v, does not name the TypeScript client's package.json", stale)
	}

	b, err := os.ReadFile(filepath.Join(dir, "clients", "ts", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"version": "0.4.0"`) {
		t.Errorf("client package.json was not synced to the spec:\n%s", b)
	}
}

// specVersionIn is what feeds the templates, so a wrong answer here
// renders a client that reports the wrong API version.
func TestSpecVersionIn(t *testing.T) {
	dir := regenFixture(t, "1.2.3", "0.1.0", "0.1.0")
	if got := specVersionIn(dir); got != "1.2.3" {
		t.Errorf("specVersionIn = %q, want 1.2.3", got)
	}
	// No spec is not an error: a specless service falls back to the
	// initial version rather than failing to scaffold.
	if got := specVersionIn(t.TempDir()); got != "" {
		t.Errorf("specVersionIn with no spec = %q, want empty", got)
	}
}
