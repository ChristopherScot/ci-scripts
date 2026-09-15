package render

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// The reason patches exist: a whole-file override opts a service out of
// every future convention change for that file, silently and permanently.
// A patch must leave the generated base intact.
func TestPatchKeepsGeneratedConventions(t *testing.T) {
	c := base()
	c.Patches = map[string]string{
		"Deployment": "spec:\n  replicas: 3\n",
	}
	outs, err := AllErr(mustConfig(t, c), "ghcr.io/o/svc:latest")
	if err != nil {
		t.Fatalf("AllErr() = %v", err)
	}

	var dep string
	for _, o := range outs {
		if o.Path == "deployment.yaml" {
			dep = o.Body
		}
	}
	if !strings.Contains(dep, "replicas: 3") {
		t.Errorf("patch did not apply:\n%s", dep)
	}
	// Every one of these would be lost under a whole-file override.
	for _, convention := range []string{
		"seccompProfile", "runAsNonRoot", "readOnlyRootFilesystem",
		"maxUnavailable", "terminationGracePeriodSeconds",
		"k8s.grafana.com/scrape",
	} {
		if !strings.Contains(dep, convention) {
			t.Errorf("patch destroyed the generated convention %q", convention)
		}
	}
}

// Nested maps merge rather than replace, or adding one annotation would
// drop the ones the tool generates.
func TestPatchMergesNestedMapsRatherThanReplacing(t *testing.T) {
	c := base()
	c.Patches = map[string]string{
		"Deployment": "spec:\n  template:\n    metadata:\n      annotations:\n        example.com/owner: platform\n",
	}
	outs, err := AllErr(mustConfig(t, c), "img")
	if err != nil {
		t.Fatalf("AllErr() = %v", err)
	}
	for _, o := range outs {
		if o.Path != "deployment.yaml" {
			continue
		}
		if !strings.Contains(o.Body, "example.com/owner") {
			t.Error("added annotation missing")
		}
		if !strings.Contains(o.Body, "k8s.grafana.com/scrape") {
			t.Error("generated annotations were replaced rather than merged")
		}
	}
}

// A patch naming a kind the service never generates applies to nothing; a
// typo must not pass silently.
func TestUnknownPatchKindIsRejected(t *testing.T) {
	c := base()
	c.Patches = map[string]string{"Deploymnet": "spec:\n  replicas: 3\n"}
	_, err := AllErr(mustConfig(t, c), "img")
	if err == nil || !strings.Contains(err.Error(), "Deploymnet") {
		t.Fatalf("AllErr() = %v, want an error naming the unknown kind", err)
	}
	// The message should say what IS available.
	if !strings.Contains(err.Error(), "Deployment") {
		t.Errorf("error should list the valid kinds: %v", err)
	}
}

func TestMalformedPatchIsReported(t *testing.T) {
	c := base()
	c.Patches = map[string]string{"Deployment": "spec:\n  this: is: not: yaml\n"}
	if _, err := AllErr(mustConfig(t, c), "img"); err == nil {
		t.Fatal("AllErr() accepted a malformed patch")
	}
}

// A cron job is patchable by its own kind, not by Deployment.
func TestPatchAppliesToCronJobKind(t *testing.T) {
	c := base()
	c.Kind = config.KindCronJob
	c.Schedule = "0 3 * * *"
	c.Patches = map[string]string{"CronJob": "spec:\n  suspend: true\n"}
	outs, err := AllErr(mustConfig(t, c), "img")
	if err != nil {
		t.Fatalf("AllErr() = %v", err)
	}
	for _, o := range outs {
		if o.Path == "cronjob.yaml" && !strings.Contains(o.Body, "suspend: true") {
			t.Errorf("patch did not reach the cron job:\n%s", o.Body)
		}
	}
}
