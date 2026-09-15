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

// A patch must change only what it names. Decoding to map[string]any and
// re-marshalling rewrites the whole document alphabetically at a different
// indent, which turns a one-line patch into a hundred-line diff and makes
// patched services unreviewable in git.
func TestPatchPreservesSurroundingFormatting(t *testing.T) {
	before := findOutput(t, mustConfig(t, base()), "deployment.yaml")

	c := base()
	c.Patches = map[string]string{"Deployment": "spec:\n  replicas: 4\n"}
	after := findOutput(t, mustConfig(t, c), "deployment.yaml")

	if !strings.Contains(after, "replicas: 4") {
		t.Fatalf("patch did not apply:\n%s", after)
	}
	for _, line := range strings.Split(before, "\n") {
		if strings.TrimSpace(line) == "" || strings.Contains(line, "replicas:") {
			continue
		}
		if !strings.Contains(after, line) {
			t.Errorf("patch reformatted an untouched line: %q", line)
		}
	}
}

// Every file ends with a newline; a patched one used to lose it, which
// shows up as a spurious "\ No newline at end of file" in every review.
func TestPatchedOutputKeepsTrailingNewline(t *testing.T) {
	c := base()
	c.Patches = map[string]string{"Deployment": "spec:\n  replicas: 2\n"}
	if body := findOutput(t, mustConfig(t, c), "deployment.yaml"); !strings.HasSuffix(body, "\n") {
		t.Error("patched output lost its trailing newline")
	}
}

// Kubernetes accepts a duplicated env var and silently keeps the last one,
// so a config setting PORT explicitly used to get it twice with the
// generated value winning.
func TestExplicitPortEnvIsNotDuplicated(t *testing.T) {
	c := base()
	c.Env = map[string]string{"PORT": "3000"}
	body := findOutput(t, mustConfig(t, c), "deployment.yaml")
	if n := strings.Count(body, "- name: PORT"); n != 1 {
		t.Errorf("PORT emitted %d times, want 1:\n%s", n, body)
	}
}

// An explicit value must win over the generated default rather than being
// silently discarded.
func TestExplicitEnvOverridesGeneratedDefault(t *testing.T) {
	c := base()
	c.Env = map[string]string{"PORT": "9999"}
	body := findOutput(t, mustConfig(t, c), "deployment.yaml")
	if !strings.Contains(body, `value: "9999"`) {
		t.Errorf("explicit PORT was discarded:\n%s", body)
	}
	if strings.Count(body, "- name: PORT") != 1 {
		t.Errorf("PORT emitted more than once:\n%s", body)
	}
}

// Deployment and CronJob are the same container; a second hand-rolled copy
// of the spec is how PORT came to be duplicated in one shape and not the
// other.
func TestWorkloadShapesShareContainerSpec(t *testing.T) {
	for _, field := range []string{"- name: PORT", "allowPrivilegeEscalation: false", "readOnlyRootFilesystem: true"} {
		if body := findOutput(t, mustConfig(t, base()), "deployment.yaml"); !strings.Contains(body, field) {
			t.Errorf("deployment missing %q", field)
		}

		cron := base()
		cron.Kind = config.KindCronJob
		cron.Schedule = "0 3 * * *"
		if body := findOutput(t, mustConfig(t, cron), "cronjob.yaml"); !strings.Contains(body, field) {
			t.Errorf("cronjob missing %q", field)
		}
	}
}

func findOutput(t *testing.T, c *config.Config, name string) string {
	t.Helper()
	outs, err := AllErr(c, "ghcr.io/o/svc:v1")
	if err != nil {
		t.Fatalf("AllErr() = %v", err)
	}
	for _, o := range outs {
		if o.Path == name {
			return o.Body
		}
	}
	t.Fatalf("no %s in rendered output", name)
	return ""
}
