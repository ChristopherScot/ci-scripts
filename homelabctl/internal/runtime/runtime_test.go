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
			if d := r.Dockerfile(p); !strings.Contains(d, "EXPOSE 3000") {
				t.Errorf("Dockerfile did not substitute Port:\n%s", d)
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

func TestGetUnknownRuntimeListsAvailable(t *testing.T) {
	_, err := Get("cobol")
	if err == nil {
		t.Fatal("Get(cobol) succeeded")
	}
	if !strings.Contains(err.Error(), "go") {
		t.Errorf("error should list available runtimes; got %v", err)
	}
}
