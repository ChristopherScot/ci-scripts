package render

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// mustAll renders and fails the test on error, so cases that are not
// about error handling read as one line.
func mustAll(t *testing.T, c *config.Config, imageRef string) []Output {
	t.Helper()
	outs, err := All(c, imageRef)
	if err != nil {
		t.Fatalf("All() = %v", err)
	}
	return outs
}

func mustConfig(t *testing.T, c *config.Config) *config.Config {
	t.Helper()
	if err := c.Complete(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	return c
}

// Built from Defaults() rather than a bare literal, because that is how
// Load builds one: hardening and metrics are on by default, and a test
// that started from the zero value would render an unhardened pod and
// quietly assert against it.
func base() *config.Config {
	c := config.Defaults()
	c.Name, c.Team, c.Runtime, c.Port = "svc", "t", "go-service", 3000
	c.Image = config.Image{Repository: "ghcr.io/o/svc"}
	return &c
}

// The failure that cost the most: without a kustomization,
// argocd-image-updater skips the app and it stays on a stale image
// forever, with only a log line to say so.
func TestAlwaysRendersKustomizationWithImages(t *testing.T) {
	out := mustAll(t, mustConfig(t, base()), "ghcr.io/o/svc:latest")

	var k string
	for _, o := range out {
		if o.Path == "kustomization.yaml" {
			k = o.Body
		}
	}
	if k == "" {
		t.Fatal("no kustomization.yaml rendered")
	}
	if !strings.Contains(k, "images:") {
		t.Error("kustomization.yaml has no images: block for image-updater to write")
	}
	if !strings.Contains(k, "ghcr.io/o/svc") {
		t.Error("kustomization.yaml images: does not name the image")
	}
}

// Every rendered file must be listed, or Argo silently does not apply it.
func TestKustomizationListsEveryResource(t *testing.T) {
	c := base()
	c.Ingress = &config.Ingress{Host: "svc.example.com"}
	c.Secrets = &config.Secrets{VaultPath: "svc/config", Keys: config.EnvKeys("TOKEN")}
	out := mustAll(t, mustConfig(t, c), "ghcr.io/o/svc:latest")

	var k string
	for _, o := range out {
		if o.Path == "kustomization.yaml" {
			k = o.Body
		}
	}
	for _, o := range out {
		if o.Path == "kustomization.yaml" {
			continue
		}
		if !strings.Contains(k, o.Path) {
			t.Errorf("kustomization.yaml does not list %s", o.Path)
		}
	}
}

func TestApplicationHasFullImageUpdaterAnnotations(t *testing.T) {
	app := Application(mustConfig(t, base()), "https://github.com/o/homelab", "svc")
	for _, want := range []string{
		"image-list:",
		"update-strategy: digest",
		"write-back-method: git",
		// Without this the updater writes a separate .argocd-source file,
		// leaving two places that both claim to set the image.
		"write-back-target: kustomization",
		// Upstream warns that git write-back needs an explicit branch when
		// targetRevision is HEAD.
		"git-branch: main",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("Application missing %q", want)
		}
	}
}

// Overrides carry other templating languages - an ExternalSecret body uses
// ESO's own {{ .username }} and b64enc - so only the image placeholder may
// be substituted, and the rest must survive untouched.
func TestOverrideSubstitutesOnlyImagePlaceholder(t *testing.T) {
	c := base()
	c.Overrides = map[string]string{
		"deployment.yaml": "image: " + ImagePlaceholder + "\nbody: {{ .username | b64enc }}\n",
	}
	for _, o := range mustAll(t, mustConfig(t, c), "ghcr.io/o/svc@sha256:abc") {
		if o.Path != "deployment.yaml" {
			continue
		}
		if !strings.Contains(o.Body, "ghcr.io/o/svc@sha256:abc") {
			t.Error("ImageURL placeholder was not substituted in the override")
		}
		if !strings.Contains(o.Body, "{{ .username | b64enc }}") {
			t.Error("override's other templating was mangled")
		}
	}
}

func TestHardenedByDefault(t *testing.T) {
	for _, o := range mustAll(t, mustConfig(t, base()), "img") {
		if o.Path != "deployment.yaml" {
			continue
		}
		for _, want := range []string{"runAsNonRoot: true", "runAsUser: 65532", "readOnlyRootFilesystem: true"} {
			if !strings.Contains(o.Body, want) {
				t.Errorf("deployment missing %q", want)
			}
		}
	}
}

func TestIngressClassFollowsPublic(t *testing.T) {
	for _, tc := range []struct {
		public bool
		want   string
	}{
		{true, "ingressClassName: public"},
		{false, "ingressClassName: external"},
	} {
		c := base()
		c.Ingress = &config.Ingress{Host: "h.example.com", Public: tc.public}
		found := false
		for _, o := range mustAll(t, mustConfig(t, c), "img") {
			if o.Path == "ingress.yaml" && strings.Contains(o.Body, tc.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("public=%v: expected %q", tc.public, tc.want)
		}
	}
}

// A cron job is a different Kubernetes shape, not a different language: it
// gets no Service, no probes and no rollout strategy, because a pod that
// exits on purpose has nothing to keep ready.
func TestCronJobRendersJobShapeNotDeployment(t *testing.T) {
	c := base()
	c.Kind = config.KindCronJob
	c.Schedule = "*/5 * * * *"
	c.TimeZone = "America/New_York"
	out := mustAll(t, mustConfig(t, c), "ghcr.io/o/svc:latest")

	var paths []string
	var cron string
	for _, o := range out {
		paths = append(paths, o.Path)
		if o.Path == "cronjob.yaml" {
			cron = o.Body
		}
	}
	if cron == "" {
		t.Fatalf("no cronjob.yaml rendered; got %v", paths)
	}
	for _, unwanted := range []string{"deployment.yaml", "service.yaml"} {
		for _, p := range paths {
			if p == unwanted {
				t.Errorf("cron job rendered %s, which it has no use for", unwanted)
			}
		}
	}
	for _, want := range []string{
		`schedule: "*/5 * * * *"`,
		// Without an explicit zone Kubernetes uses UTC, so a schedule
		// reading 3am fires at 11pm the evening before in ET.
		`timeZone: "America/New_York"`,
		"concurrencyPolicy: Forbid",
		"restartPolicy: Never",
		"activeDeadlineSeconds",
	} {
		if !strings.Contains(cron, want) {
			t.Errorf("cronjob.yaml missing %q", want)
		}
	}
	if strings.Contains(cron, "readinessProbe") {
		t.Error("cron job has a readiness probe; a job that exits is never ready")
	}
}

// Hardening and secrets are workload-independent - they must reach the
// container whichever shape wraps it.
func TestCronJobStillHardenedAndGetsSecrets(t *testing.T) {
	c := base()
	c.Kind = config.KindCronJob
	c.Schedule = "0 3 * * *"
	c.Secrets = &config.Secrets{VaultPath: "svc/config", Keys: config.EnvKeys("TOKEN")}
	for _, o := range mustAll(t, mustConfig(t, c), "img") {
		if o.Path != "cronjob.yaml" {
			continue
		}
		for _, want := range []string{"runAsNonRoot: true", "seccompProfile", "secretRef", "svc-secrets"} {
			if !strings.Contains(o.Body, want) {
				t.Errorf("cronjob.yaml missing %q", want)
			}
		}
	}
}
