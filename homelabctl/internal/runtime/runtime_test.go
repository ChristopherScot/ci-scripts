package runtime

import (
	"strings"
	"testing"
)

// Templates live in files and are only exercised at generate time, so a
// typo in one would otherwise surface as a panic in front of a user
// creating a service. Render every registered runtime here instead.
func TestEveryRuntimeRendersCleanly(t *testing.T) {
	p := Params{Name: "svc", Module: "github.com/o/svc", Port: 3000}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			r, err := Get(name)
			if err != nil {
				t.Fatalf("Get(%q) = %v", name, err)
			}
			switch r.Kind() {
			case KindService:
				if d := r.Dockerfile(p); !strings.Contains(d, "EXPOSE 3000") {
					t.Errorf("Dockerfile did not substitute Port:\n%s", d)
				}
			case KindCLI:
				// A CLI is never containerised.
				if d := r.Dockerfile(p); d != "" {
					t.Errorf("CLI runtime produced a Dockerfile:\n%s", d)
				}
			}
			if s := r.BuildSteps(p); strings.TrimSpace(s) == "" {
				t.Error("BuildSteps is empty")
			}
			files := r.Files(p)
			if len(files) == 0 {
				t.Fatal("Files returned nothing")
			}
			for _, f := range files {
				if f.Path == "" {
					t.Error("file with empty path")
				}
				if strings.Contains(f.Body, "{{") {
					t.Errorf("%s still contains an unrendered action:\n%s", f.Path, f.Body)
				}
			}
		})
	}
}

// A hardened runtime's image must run as the uid the manifests expect, or
// the pod cannot exec its binary and fails with no logs.
func TestHardenedRuntimesRunAsNonroot(t *testing.T) {
	p := Params{Name: "svc", Module: "m", Port: 3000}
	for _, name := range Names() {
		r, _ := Get(name)
		if !r.SupportsHardened() {
			continue
		}
		if d := r.Dockerfile(p); !strings.Contains(d, "USER 65532") {
			t.Errorf("runtime %q claims hardened support but its Dockerfile does not USER 65532", name)
		}
	}
}

// A CLI ships a self-update command, which is the reason the kind exists;
// without it users have no way to get a new version.
func TestCLIRuntimesShipSelfUpdate(t *testing.T) {
	p := Params{Name: "mytool", Module: "github.com/o/mytool", Owner: "o", Port: 3000}
	for _, name := range Names() {
		r, _ := Get(name)
		if r.Kind() != KindCLI {
			continue
		}
		var hasUpdate, hasVersion bool
		for _, f := range r.Files(p) {
			if f.Path == "update.go" {
				hasUpdate = true
				if !strings.Contains(f.Body, `repoOwner = "o"`) {
					t.Errorf("%s: update.go did not substitute Owner", name)
				}
				if !strings.Contains(f.Body, `repoName  = "mytool"`) {
					t.Errorf("%s: update.go did not substitute Name", name)
				}
			}
			if f.Path == "VERSION" {
				hasVersion = true
			}
		}
		if !hasUpdate {
			t.Errorf("CLI runtime %q ships no update.go", name)
		}
		if !hasVersion {
			t.Errorf("CLI runtime %q ships no VERSION file", name)
		}
	}
}

func TestGetUnknownRuntimeListsAvailable(t *testing.T) {
	_, err := Get("cobol")
	if err == nil {
		t.Fatal("Get(cobol) succeeded")
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("error should list available runtimes; got %v", err)
	}
}
