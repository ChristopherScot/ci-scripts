package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// mustAll renders and fails the test on error, so cases that are not
// about error handling read as one line.
func mustAll(t *testing.T, c *config.Config) []Output {
	t.Helper()
	outs, err := All(c)
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
	out := mustAll(t, mustConfig(t, base()))

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
	out := mustAll(t, mustConfig(t, c))

	var k string
	for _, o := range out {
		if o.Path == "kustomization.yaml" {
			k = o.Body
		}
	}
	for _, o := range out {
		// kustomization.yaml would list itself; argocd.json is input for
		// the ApplicationSet generator rather than a resource. Everything
		// else is a manifest Argo has to apply, and one missing here is
		// one that silently never reaches the cluster.
		if o.Path == "kustomization.yaml" || o.Path == AppEntryFile {
			continue
		}
		if !strings.Contains(k, o.Path) {
			t.Errorf("kustomization.yaml does not list %s", o.Path)
		}
	}
	// The converse: a non-manifest listed here makes Argo apply a JSON
	// file as a Kubernetes resource, which fails the entire sync.
	if strings.Contains(k, AppEntryFile) {
		t.Errorf("kustomization.yaml lists %s, which Argo would try to apply:\n%s", AppEntryFile, k)
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
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path != "deployment.yaml" {
			continue
		}
		// :latest, because that is what a rendered manifest always
		// names - image-updater resolves it to a digest and writes that
		// into kustomization.yaml.
		if !strings.Contains(o.Body, "ghcr.io/o/svc:latest") {
			t.Errorf("ImageURL placeholder was not substituted in the override:\n%s", o.Body)
		}
		if !strings.Contains(o.Body, "{{ .username | b64enc }}") {
			t.Error("override's other templating was mangled")
		}
	}
}

func TestHardenedByDefault(t *testing.T) {
	for _, o := range mustAll(t, mustConfig(t, base())) {
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
		for _, o := range mustAll(t, mustConfig(t, c)) {
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
	out := mustAll(t, mustConfig(t, c))

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
	for _, o := range mustAll(t, mustConfig(t, c)) {
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
	for _, o := range mustAll(t, mustConfig(t, c)) {
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

	for _, o := range mustAll(t, mustConfig(t, c)) {
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

// A Namespace is shared infrastructure and this tool works at the level
// of one app, so it must not render one: Pod Security Admission is a
// namespace LABEL, and rendering it meant a service imposing its own
// security level on every neighbour. Adding the redirector to `shlink`
// would have labelled that namespace restricted, which shlink itself
// cannot meet - and its pods would have kept running until the next
// rollout, then failed to start.
func TestNoNamespaceIsRendered(t *testing.T) {
	for _, o := range mustAll(t, mustConfig(t, base())) {
		if strings.Contains(o.Path, "namespace") {
			t.Errorf("rendered %s; a namespace belongs to whoever owns it", o.Path)
		}
		if strings.Contains(o.Body, "pod-security.kubernetes.io") {
			t.Errorf("%s sets a namespace-wide security level:\n%s", o.Path, o.Body)
		}
	}
}

// The other half of that bargain: if this tool will not lock down a
// namespace, its pod has to be acceptable in one that someone else has.
// These are exactly the five things PSA `restricted` requires.
func TestPodMeetsRestrictedWithoutTheNamespaceLabel(t *testing.T) {
	var dep string
	for _, o := range mustAll(t, mustConfig(t, base())) {
		if strings.HasSuffix(o.Path, "deployment.yaml") {
			dep = o.Body
		}
	}
	if dep == "" {
		t.Fatal("no deployment rendered")
	}
	for _, required := range []string{
		"runAsNonRoot: true",
		"allowPrivilegeEscalation: false",
		"drop: [ALL]",
		"type: RuntimeDefault",
	} {
		if !strings.Contains(dep, required) {
			t.Errorf("missing %q; the pod would be rejected by a restricted namespace:\n%s", required, dep)
		}
	}
	if strings.Contains(dep, "privileged: true") {
		t.Error("the pod asks to be privileged, which no restricted namespace admits")
	}
}

// The reason config refuses a wildcard in vaultPath: it arrives here as
// remoteRef.key, which ESO fetches literally. Nothing between the config
// and Vault expands it, so a pattern would name a secret that does not
// exist and fail at sync time rather than at validate time.
func TestVaultPathReachesRemoteRefVerbatim(t *testing.T) {
	c := base()
	c.Secrets = &config.Secrets{VaultPath: "team/svc/config", Keys: config.EnvKeys("TOK")}

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if strings.HasSuffix(o.Path, "externalsecret.yaml") {
			body = o.Body
		}
	}
	if body == "" {
		t.Fatal("no externalsecret rendered")
	}
	if !strings.Contains(body, "key: team/svc/config") {
		t.Errorf("vaultPath was not passed through as remoteRef.key:\n%s", body)
	}
}

// Every service declares the same External Secrets apiVersion, so when
// ESO drops one they all break at the same moment - and quietly: the
// ExternalSecret stops refreshing while the Secret it already made
// lingers, so pods run on credentials nobody is renewing.
//
// One constant is what lets `check` compare it against the cluster.
func TestExternalSecretUsesTheSharedAPIVersion(t *testing.T) {
	c := base()
	c.Secrets = &config.Secrets{VaultPath: "svc", Keys: config.EnvKeys("TOK")}

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if strings.HasSuffix(o.Path, "externalsecret.yaml") {
			body = o.Body
		}
	}
	if body == "" {
		t.Fatal("no externalsecret rendered")
	}
	// Both the SecretStore and the ExternalSecret, not just one.
	if got := strings.Count(body, "apiVersion: "+ESOAPIVersion); got != 2 {
		t.Errorf("declared %s %d times, want 2:\n%s", ESOAPIVersion, got, body)
	}
	if strings.Contains(body, "ESO_API_VERSION") {
		t.Error("the placeholder survived into the manifest")
	}
}

// The failure this package exists to prevent: an override replaces a
// file wholesale, so a patch naming a kind inside that file never runs.
// It used to happen in silence - render succeeded, Argo reported Synced
// and Healthy, and the annotation the author wrote was simply absent.
//
// Neither existing check caught it: UnknownPatchKinds asks whether the
// kind is generated, and Deployment is.
func TestOverrideShadowingAPatchIsAnError(t *testing.T) {
	c := base()
	c.Patches = map[string]string{
		"Deployment": "metadata:\n  annotations:\n    example.com/x: \"1\"\n",
	}
	c.Overrides = map[string]string{
		"deployment.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: svc\n",
	}
	_, err := All(mustConfig(t, c))
	if err == nil {
		t.Fatal("an override swallowed a patch and render reported success")
	}
	// Name both halves: which patch, and which file ate it.
	for _, want := range []string{"Deployment", "deployment.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// Overrides and patches coexist as long as they do not touch the same
// file - that is the ordinary case, and making the collision an error
// must not make the whole combination one.
func TestOverrideAndPatchOnDifferentFilesBothApply(t *testing.T) {
	c := base()
	c.Patches = map[string]string{
		"Service": "metadata:\n  annotations:\n    example.com/svc: \"1\"\n",
	}
	c.Overrides = map[string]string{
		"deployment.yaml": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: svc\n",
	}
	var svc string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "service.yaml" {
			svc = o.Body
		}
	}
	if !strings.Contains(svc, "example.com/svc") {
		t.Errorf("the Service patch did not apply:\n%s", svc)
	}
}

// A misspelt override path overrides nothing, while the author believes
// their file is in charge of that manifest. UnknownOverrides could
// already see this; nothing called it, so render accepted the typo.
func TestOverrideNamingNoGeneratedFileIsAnError(t *testing.T) {
	c := base()
	c.Overrides = map[string]string{"deploymnet.yaml": "kind: Deployment\n"}
	_, err := All(mustConfig(t, c))
	if err == nil {
		t.Fatal("a misspelt override path was accepted")
	}
	// The typo and a real path, so the fix is readable from the error.
	for _, want := range []string{"deploymnet.yaml", "deployment.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// The ApplicationSet template reads these keys by name, so a rename here
// that is not matched in the template renders an empty string into a live
// Application - a namespace of "" would deploy the service to the wrong
// place, or fail the sync, with nothing in this repo to catch it.
func TestAppEntryCarriesTheGeneratorsParameters(t *testing.T) {
	c := base()
	c.Team = "me-myself-and-i"
	c.Namespace = "elsewhere" // deliberately not the service name
	out := mustAll(t, mustConfig(t, c))

	var body string
	for _, o := range out {
		if o.Path == AppEntryFile {
			body = o.Body
		}
	}
	if body == "" {
		t.Fatalf("no %s rendered", AppEntryFile)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%s is not valid JSON: %v\n%s", AppEntryFile, err, body)
	}
	for k, want := range map[string]string{
		"name":      "svc",
		"team":      "me-myself-and-i",
		"namespace": "elsewhere",
		"image":     "ghcr.io/o/svc",
	} {
		if got[k] != want {
			t.Errorf("%s[%q] = %q, want %q", AppEntryFile, k, got[k], want)
		}
	}
	// The template appends :latest, so a tag here would produce
	// "repo:latest:latest" and an image that does not exist.
	if strings.Contains(got["image"], ":") {
		t.Errorf("image %q carries a tag; the template appends :latest", got["image"])
	}
}
