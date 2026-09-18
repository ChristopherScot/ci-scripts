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
	c.Ingress = &config.Ingress{Hosts: config.IngressHosts("svc.example.com")}
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
		c.Ingress = &config.Ingress{Hosts: config.IngressHosts("h.example.com"), Public: tc.public}
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

// ssl-redirect is a RESOURCE-scoped annotation, so a certified host and
// an uncertifiable one cannot share an Ingress: turning the redirect off
// for the LAN name turns it off for the FQDN too. Three services in this
// cluster were merged that way and two served their admin UI over plain
// HTTP as a result.
func TestMixedHostsRenderAsTwoIngresses(t *testing.T) {
	c := base()
	c.Ingress = &config.Ingress{Hosts: config.IngressHosts("svc.example.com", "svc.lab")}

	var ing string
	for _, o := range mustAll(t, mustConfig(t, c), "ghcr.io/o/svc:latest") {
		if strings.HasSuffix(o.Path, "ingress.yaml") {
			ing = o.Body
		}
	}
	if ing == "" {
		t.Fatal("no ingress rendered")
	}

	docs := strings.Split(ing, "---")
	if len(docs) != 2 {
		t.Fatalf("got %d Ingress documents, want 2:\n%s", len(docs), ing)
	}
	tlsDoc, lanDoc := docs[0], docs[1]

	// The certified half: an issuer, a tls block, and no redirect
	// override - so HTTPS is still enforced for this name.
	if !strings.Contains(tlsDoc, "cert-manager.io/cluster-issuer") {
		t.Error("the certified Ingress has no issuer")
	}
	if !strings.Contains(tlsDoc, "svc.example.com") || strings.Contains(tlsDoc, "svc.lab") {
		t.Errorf("the certified Ingress should hold only the certifiable host:\n%s", tlsDoc)
	}
	if strings.Contains(tlsDoc, "ssl-redirect") {
		t.Errorf("the certified host lost its HTTPS redirect:\n%s", tlsDoc)
	}

	// The plain half: no issuer at all, which is what makes an
	// impossible ACME order structurally impossible rather than avoided.
	if strings.Contains(lanDoc, "cert-manager.io/cluster-issuer") {
		t.Errorf("the plain Ingress names an issuer; cert-manager would retry an order forever:\n%s", lanDoc)
	}
	if strings.Contains(lanDoc, "tls:") {
		t.Errorf("the plain Ingress has a tls block for a name no CA will sign:\n%s", lanDoc)
	}
	if !strings.Contains(lanDoc, `ssl-redirect: "false"`) {
		t.Errorf("the plain host would 308 to a certificate that cannot cover it:\n%s", lanDoc)
	}
}

// The ordinary case must stay one resource.
func TestSingleCertifiableHostRendersOneIngress(t *testing.T) {
	c := base()
	c.Ingress = &config.Ingress{Hosts: config.IngressHosts("svc.example.com")}

	for _, o := range mustAll(t, mustConfig(t, c), "ghcr.io/o/svc:latest") {
		if strings.HasSuffix(o.Path, "ingress.yaml") {
			if strings.Contains(o.Body, "---") {
				t.Errorf("a single host rendered two Ingresses:\n%s", o.Body)
			}
			if strings.Contains(o.Body, "ssl-redirect") {
				t.Errorf("a certified host should keep its redirect:\n%s", o.Body)
			}
		}
	}
}
