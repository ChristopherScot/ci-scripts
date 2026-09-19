package render

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
	"gopkg.in/yaml.v3"
)

// mustAll renders and fails the test on error, so cases that are not
// about error handling read as one line.
func mustAll(t *testing.T, c *config.Config) []Output {
	t.Helper()
	outs, err := All(c, Source{})
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

// A key naming its own path reaches remoteRef.key as that path, while
// the rest of the keys keep the service's. One ExternalSecret, one
// Secret, one Vault role - only the key differs per entry.
func TestAKeyCanReadFromAnotherVaultPath(t *testing.T) {
	c := base()
	c.Secrets = &config.Secrets{
		VaultPath: "cert-manager/route53",
		Keys: []config.SecretKey{
			{Env: "AWS_ACCESS_KEY_ID", Property: "access_key_id"},
			{Env: "NTFY_TOKEN", Property: "grafana_token", Path: "ntfy/config"},
		},
	}

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if strings.HasSuffix(o.Path, "externalsecret.yaml") {
			body = o.Body
		}
	}
	if body == "" {
		t.Fatal("no externalsecret rendered")
	}
	for _, want := range []string{
		"remoteRef: { key: cert-manager/route53, property: access_key_id }",
		"remoteRef: { key: ntfy/config, property: grafana_token }",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	if n := strings.Count(body, "kind: ExternalSecret"); n != 1 {
		t.Errorf("rendered %d ExternalSecrets, want 1", n)
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
	_, err := All(mustConfig(t, c), Source{})
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
	_, err := All(mustConfig(t, c), Source{})
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

// A service can be mounted under a prefix so several share one hostname.
// The prefix is stripped before the request reaches the service, which is
// what lets the service keep its own unprefixed routes.
func TestIngressPathMountsTheServiceUnderAPrefix(t *testing.T) {
	c := base()
	c.Ingress = &config.Ingress{
		Hosts: config.IngressHosts("pokemon.example.com"),
		Path:  "/api",
	}
	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "ingress.yaml" {
			body = o.Body
		}
	}
	if body == "" {
		t.Fatal("no ingress rendered")
	}
	// The trailing (/|$) is what stops /apifoo matching a service mounted
	// on /api - the reason this is a regex rather than "/api(.*)".
	if !strings.Contains(body, "path: /api(/|$)(.*)") {
		t.Errorf("rule path does not guard the prefix boundary:\n%s", body)
	}
	// rewrite-target without use-regex silently does nothing, and the
	// capture group without rewrite-target routes the prefix through to
	// the service. Both are needed or neither works.
	for _, want := range []string{
		`nginx.ingress.kubernetes.io/use-regex: "true"`,
		"nginx.ingress.kubernetes.io/rewrite-target: /$2",
		"pathType: ImplementationSpecific",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ingress missing %q:\n%s", want, body)
		}
	}
}

// The ordinary case stays a plain prefix match. A regex path and a
// rewrite on every service would be a behaviour change for all of them
// to serve one that wanted a prefix.
func TestIngressWithoutAPathIsAPlainPrefix(t *testing.T) {
	c := base()
	c.Ingress = &config.Ingress{Hosts: config.IngressHosts("svc.example.com")}
	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "ingress.yaml" {
			body = o.Body
		}
	}
	if !strings.Contains(body, "path: /\n") || !strings.Contains(body, "pathType: Prefix") {
		t.Errorf("default ingress is not a plain prefix match:\n%s", body)
	}
	if strings.Contains(body, "rewrite-target") {
		t.Errorf("a service with no ingress.path should not rewrite:\n%s", body)
	}
}

// argocd.json tells the ApplicationSet where to fetch a service's
// manifests. Argo needs the directory holding kustomization.yaml, which
// is deploy/<name>/ - pointing it at deploy/ finds no kustomization and
// syncs the app as a plain directory, which silently skips image
// automation.
func TestAppEntryPointsAtTheManifestDirectory(t *testing.T) {
	c := base()
	src := Source{RepoURL: "https://github.com/o/mono", Path: "services/svc/deploy"}

	var body string
	for _, o := range mustAllSrc(t, mustConfig(t, c), src) {
		if o.Path == AppEntryFile {
			body = o.Body
		}
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%s is not valid JSON: %v", AppEntryFile, err)
	}
	if got["repoURL"] != src.RepoURL {
		t.Errorf("repoURL = %q, want %q", got["repoURL"], src.RepoURL)
	}
	if want := "services/svc/deploy/svc"; got["manifestPath"] != want {
		t.Errorf("manifestPath = %q, want %q", got["manifestPath"], want)
	}
}

// A service that is not in a repo yet renders no source rather than a
// wrong one: the generator skips an entry with no repoURL, where a bare
// path would point Argo at some other repository's root.
func TestAppEntryOmitsAnUnknownSource(t *testing.T) {
	var body string
	for _, o := range mustAll(t, mustConfig(t, base())) {
		if o.Path == AppEntryFile {
			body = o.Body
		}
	}
	for _, unwanted := range []string{"repoURL", "manifestPath"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("%s carries %q with no source:\n%s", AppEntryFile, unwanted, body)
		}
	}
}

func mustAllSrc(t *testing.T, c *config.Config, src Source) []Output {
	t.Helper()
	outs, err := All(c, src)
	if err != nil {
		t.Fatalf("All() = %v", err)
	}
	return outs
}

// A hand-written manifest is copied verbatim and listed as a resource.
//
// Verbatim matters: the author wrote a CRD this tool has never heard of,
// and any reformatting would be this tool having an opinion about a
// shape it does not understand.
func TestManifestsAreCopiedAndListed(t *testing.T) {
	const db = `apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: pokedex-db
spec:
  instances: 2
`
	c := config.Defaults()
	c.Name = "pokedex"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/pokedex"
	c.Manifests = []string{"db.yaml"}

	outs, err := All(&c, Source{Manifests: map[string]string{"db.yaml": db}})
	if err != nil {
		t.Fatalf("All: %v", err)
	}

	var got string
	for _, o := range outs {
		if o.Path == ManifestDir+"/db.yaml" {
			got = o.Body
		}
	}
	if got != db {
		t.Errorf("db.yaml was not copied verbatim:\ngot:\n%s\nwant:\n%s", got, db)
	}

	// In kustomization.yaml, or Argo never applies it.
	for _, o := range outs {
		if o.Path != "kustomization.yaml" {
			continue
		}
		if !strings.Contains(o.Body, "- "+ManifestDir+"/db.yaml") {
			t.Errorf("kustomization.yaml does not list db.yaml:\n%s", o.Body)
		}
		return
	}
	t.Error("no kustomization.yaml rendered")
}

// A file that is not a Kubernetes manifest has to fail the render.
//
// Argo applies everything under resources:, so a stray non-manifest does
// not fail alone - it fails the SYNC, taking the Deployment and Service
// with it. Failing here turns a cluster-wide outage into a build error.
func TestManifestsMustBeKubernetes(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no apiVersion or kind", "just: some values\nnested:\n  a: 1\n"},
		{"empty", "\n"},
		{"not yaml at all", "{{{ this is not yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Defaults()
			c.Name = "pokedex"
			c.Team = "platform"
			c.Runtime = "go"
			c.Port = 8080
			c.Image.Repository = "ghcr.io/example/pokedex"
			c.Manifests = []string{"db.yaml"}

			_, err := All(&c, Source{Manifests: map[string]string{"db.yaml": tc.body}})
			if err == nil {
				t.Fatal("rendered a file that would fail the whole Argo sync")
			}
		})
	}
}

// A manifest named in config but absent from the Source is an error, not
// a silent omission: rendering without it drops the resource from
// kustomization.yaml, and Argo prunes what is no longer listed.
func TestMissingManifestIsAnError(t *testing.T) {
	c := config.Defaults()
	c.Name = "pokedex"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/pokedex"
	c.Manifests = []string{"db.yaml"}

	if _, err := All(&c, Source{}); err == nil {
		t.Fatal("a manifest that was never read rendered as if it did not exist")
	}
}

// Files names exactly what All renders, and renders nothing to do it.
//
// init needs the deploy plane's inventory so `--overwrite` can validate
// a filename. It used to get that by calling All with a zero Source,
// which has no git remote and no manifest contents - so it wrote an
// argocd.json missing repoURL and manifestPath, and failed outright on
// any config carrying manifests:.
//
// Compared as sets against a real render: if the two ever disagree,
// --overwrite either refuses a file that exists or accepts one that
// does not.
func TestFilesNamesWhatAllRenders(t *testing.T) {
	for _, tc := range []struct {
		name string
		with func(*config.Config)
	}{
		{"plain service", func(*config.Config) {}},
		{"with ingress", func(c *config.Config) {
			c.Ingress = &config.Ingress{Hosts: []config.IngressHost{{Name: "x.example.com"}}}
		}},
		{"with manifests", func(c *config.Config) { c.Manifests = []string{"db.yaml"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := config.Defaults()
			c.Name = "svc"
			c.Team = "platform"
			c.Runtime = "go"
			c.Port = 8080
			c.Image.Repository = "ghcr.io/example/svc"
			tc.with(&c)

			src := Source{RepoURL: "https://github.com/example/svc", Path: "deploy"}
			if len(c.Manifests) > 0 {
				src.Manifests = map[string]string{
					"db.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: db\n",
				}
			}
			outs, err := All(&c, src)
			if err != nil {
				t.Fatalf("All: %v", err)
			}

			rendered := map[string]bool{}
			for _, o := range outs {
				rendered[o.Path] = true
			}
			listed := map[string]bool{}
			for _, p := range Files(&c) {
				listed[p] = true
			}

			for p := range rendered {
				if !listed[p] {
					t.Errorf("All renders %q but Files does not name it", p)
				}
			}
			for p := range listed {
				if !rendered[p] {
					t.Errorf("Files names %q but All does not render it", p)
				}
			}
		})
	}
}

// Files must not need a Source, because its caller has no git remote.
//
// The whole reason it exists: asking "what files are there" used to
// require the inputs for "produce the files", which init cannot supply.
func TestFilesNeedsNoSource(t *testing.T) {
	c := config.Defaults()
	c.Name = "svc"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/svc"
	c.Manifests = []string{"db.yaml"} // would make All fail with no Source

	got := Files(&c)
	if len(got) == 0 {
		t.Fatal("Files returned nothing")
	}
	var sawManifest, sawEntry bool
	for _, p := range got {
		if p == ManifestDir+"/db.yaml" {
			sawManifest = true
		}
		if p == AppEntryFile {
			sawEntry = true
		}
	}
	if !sawManifest {
		t.Error("Files omits a manifest named in config")
	}
	if !sawEntry {
		t.Errorf("Files omits %s", AppEntryFile)
	}
}

// A manifest sharing a generated file's name is no longer a collision.
//
// They used to share one namespace with no arbitration. A hand-written
// service.yaml was appended after the generated Service, so it replaced
// it on disk and appeared twice in resources: - kustomize refused the
// directory, and under prune: true Argo deletes the live Service. A
// hand-written argocd.json was excluded from resources: by design AND
// overwritten, so the resource existed nowhere while render, check and
// kustomize all reported success.
//
// Hand-written manifests render under manifests/ now, so the two
// namespaces are different directories and the collision cannot be
// expressed. This asserts that rather than asserting an error.
func TestManifestsCannotShadowGeneratedFiles(t *testing.T) {
	const doc = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n"
	for _, name := range []string{"service.yaml", "deployment.yaml", "kustomization.yaml"} {
		t.Run(name, func(t *testing.T) {
			c := config.Defaults()
			c.Name = "svc"
			c.Team = "platform"
			c.Runtime = "go"
			c.Port = 8080
			c.Image.Repository = "ghcr.io/example/svc"
			c.Manifests = []string{name}

			outs, err := All(&c, Source{Manifests: map[string]string{name: doc}})
			if err != nil {
				t.Fatalf("All: %v", err)
			}

			// The generated file keeps its own body at the deploy root,
			// and the hand-written one lands under manifests/.
			var atRoot, nested string
			for _, o := range outs {
				switch o.Path {
				case name:
					atRoot = o.Body
				case ManifestDir + "/" + name:
					nested = o.Body
				}
			}
			if nested != doc {
				t.Errorf("hand-written %s did not render under %s/", name, ManifestDir)
			}
			if atRoot == doc {
				t.Errorf("hand-written %s replaced the generated one at the deploy root", name)
			}
		})
	}
}

// The same file listed twice renders twice into resources:, which
// kustomize rejects as a duplicate id.
func TestManifestsRejectDuplicates(t *testing.T) {
	c := config.Defaults()
	c.Name = "svc"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/svc"
	c.Manifests = []string{"db.yaml", "db.yaml"}

	_, err := All(&c, Source{Manifests: map[string]string{
		"db.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: db\n",
	}})
	if err == nil {
		t.Fatal("a manifest listed twice was accepted")
	}
}

// Every document in a multi-document file has to be validated, whatever
// the file's line endings.
//
// validManifest used to split on the literal "\n---\n". CRLF makes the
// separator "---\r" and a trailing space makes it "--- ", so the split
// never fired - and yaml.Unmarshal then decodes only the FIRST document
// of a stream and returns nil. Everything after it went unchecked, so a
// file edited on Windows passed render and check, and kustomize refused
// the whole directory.
func TestValidManifestSeesEveryDocument(t *testing.T) {
	const good = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n"
	const bad = "this is: not a manifest\n"

	for _, tc := range []struct{ name, sep string }{
		{"unix", "\n---\n"},
		{"crlf", "\r\n---\r\n"},
		{"trailing space", "\n--- \n"},
		{"comment after marker", "\n--- # note\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := good + tc.sep + bad
			if err := validManifest("db.yaml", body); err == nil {
				t.Errorf("second document went unvalidated with %q separator", tc.sep)
			}
		})
	}

	// And a genuinely valid multi-document file still passes.
	both := good + "\n---\n" + "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n"
	if err := validManifest("db.yaml", both); err != nil {
		t.Errorf("a valid two-document manifest was rejected: %v", err)
	}
}

// public is a property of a HOST, not of the whole Ingress.
//
// It used to be all-or-nothing, so a service with a LAN name and a WAN
// name had to put both on one controller: exposing
// pokemon.chrisscotmartin.com would have dragged
// pokemon.home.chrisscotmartin.com onto the public controller with it -
// a name resolving to 192.168.50.225, served by the controller facing
// the internet.
func TestPublicIsPerHost(t *testing.T) {
	yes := true
	c := config.Defaults()
	c.Name = "svc"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/svc"
	c.Ingress = &config.Ingress{Hosts: []config.IngressHost{
		{Name: "svc.home.example.com", TLS: true},
		{Name: "svc.example.com", TLS: true, Public: &yes},
	}}

	outs, err := All(&c, Source{})
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var body string
	for _, o := range outs {
		if o.Path == "ingress.yaml" {
			body = o.Body
		}
	}

	// The LAN host must NOT be on the public controller. Checked by
	// splitting the documents, because both class names appear in one
	// file and a Contains over the whole thing proves nothing.
	for _, doc := range strings.Split(body, "\n---\n") {
		public := strings.Contains(doc, "ingressClassName: public")
		if public && strings.Contains(doc, "svc.home.example.com") {
			t.Error("the LAN host was routed via the public controller")
		}
		if !public && strings.Contains(doc, "host: svc.example.com") {
			t.Error("the public host was routed via the LAN controller")
		}
	}
}

// A public Ingress is always named -public, even when it is the only
// one.
//
// The name is how you tell what an Ingress is without reading its spec.
// An Ingress called "ntfy" sitting on the public controller reads as
// internal at a glance, which is the wrong way for that mistake to go.
//
// The cost is a one-time rename for services already public via
// ingress.public: Argo deletes the old object and creates the new one,
// dropping the route briefly. Paid once.
func TestPublicIngressIsAlwaysNamedPublic(t *testing.T) {
	c := config.Defaults()
	c.Name = "svc"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/svc"
	c.Ingress = &config.Ingress{
		Hosts:  []config.IngressHost{{Name: "svc.example.com", TLS: true}},
		Public: true,
	}

	outs, err := All(&c, Source{})
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	for _, o := range outs {
		if o.Path != "ingress.yaml" {
			continue
		}
		if !strings.Contains(o.Body, "name: svc-public") {
			t.Errorf("a public Ingress is not named -public, so it reads as internal:\n%s", o.Body)
		}
	}
}

// Two Ingresses must not claim the same TLS secret.
//
// The secret was named after the SERVICE, so a service with a LAN host
// and a public one rendered two Ingresses pointing at one secret for
// different host sets. cert-manager would issue for one, overwrite the
// other's certificate, and keep going - a renewal loop that presents
// the wrong certificate half the time.
func TestEachIngressGetsItsOwnTLSSecret(t *testing.T) {
	yes := true
	c := config.Defaults()
	c.Name = "svc"
	c.Team = "platform"
	c.Runtime = "go"
	c.Port = 8080
	c.Image.Repository = "ghcr.io/example/svc"
	c.Ingress = &config.Ingress{Hosts: []config.IngressHost{
		{Name: "svc.home.example.com", TLS: true},
		{Name: "svc.example.com", TLS: true, Public: &yes},
	}}

	outs, err := All(&c, Source{})
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	var body string
	for _, o := range outs {
		if o.Path == "ingress.yaml" {
			body = o.Body
		}
	}

	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		_, secret, ok := strings.Cut(strings.TrimSpace(line), "secretName: ")
		if !ok {
			continue
		}
		if seen[secret] {
			t.Errorf("two Ingresses share the TLS secret %q; cert-manager would fight over it", secret)
		}
		seen[secret] = true
	}
	if len(seen) != 2 {
		t.Errorf("expected two TLS secrets, got %d: %v", len(seen), seen)
	}
}

// Scrape annotations have to land on the POD template's metadata, where
// Alloy looks for them - not merely somewhere in the file.
//
// This is a structural assertion rather than a string match on purpose.
// The CronJob rendered its annotations at an indent that made them a
// sibling of `metadata:` instead of a child, so the block was present in
// the YAML, the manifest applied cleanly, and Kubernetes dropped the
// annotations as an unknown field on PodSpec. Every cronjob with
// metrics: true was invisible to Alloy, and nothing said so. A
// substring check for "k8s.grafana.com/scrape" passes on the broken
// output.
func TestScrapeAnnotationsLandOnThePodTemplate(t *testing.T) {
	for _, tc := range []struct {
		kind string
		file string
	}{
		{config.KindService, "deployment.yaml"},
		{config.KindCronJob, "cronjob.yaml"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			c := base()
			c.Kind = tc.kind
			c.Metrics = true
			c.Port = 3000
			if tc.kind == config.KindCronJob {
				c.Schedule = "*/5 * * * *"
			}

			var body string
			for _, o := range mustAll(t, mustConfig(t, c)) {
				if o.Path == tc.file {
					body = o.Body
				}
			}
			if body == "" {
				t.Fatalf("no %s rendered", tc.file)
			}

			var doc map[string]any
			if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatalf("parsing %s: %v", tc.file, err)
			}
			tmpl := podTemplate(t, doc, tc.kind)
			meta, _ := tmpl["metadata"].(map[string]any)
			ann, _ := meta["annotations"].(map[string]any)
			if got := ann["k8s.grafana.com/scrape"]; got != "true" {
				t.Errorf("pod template metadata.annotations = %v, want the scrape annotations\n%s", ann, body)
			}
			if got := ann["k8s.grafana.com/metrics.portNumber"]; got != "3000" {
				t.Errorf("metrics.portNumber = %v, want \"3000\"", got)
			}
		})
	}
}

// podTemplate digs out spec.template (Deployment) or
// spec.jobTemplate.spec.template (CronJob).
func podTemplate(t *testing.T, doc map[string]any, kind string) map[string]any {
	t.Helper()
	spec, _ := doc["spec"].(map[string]any)
	if kind == config.KindCronJob {
		jt, _ := spec["jobTemplate"].(map[string]any)
		spec, _ = jt["spec"].(map[string]any)
	}
	tmpl, ok := spec["template"].(map[string]any)
	if !ok {
		t.Fatalf("no pod template in %v", doc)
	}
	return tmpl
}

// Pod labels have to be on the pod, not only on the workload. Loki and
// the Grafana dashboards select on app and team, and a CronJob's pods
// carried neither - so a cronjob's logs were unqueryable by the labels
// every other workload here is found with.
func TestCronJobPodsCarryAppAndTeamLabels(t *testing.T) {
	c := base()
	c.Kind = config.KindCronJob
	c.Schedule = "*/5 * * * *"

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "cronjob.yaml" {
			body = o.Body
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parsing cronjob.yaml: %v", err)
	}
	meta, _ := podTemplate(t, doc, config.KindCronJob)["metadata"].(map[string]any)
	labels, _ := meta["labels"].(map[string]any)
	if labels["app"] != c.Name || labels["team"] != c.Team {
		t.Errorf("pod labels = %v, want app=%s team=%s\n%s", labels, c.Name, c.Team, body)
	}
}

// A port is a thing a server listens on. A cronjob has none, and
// PORT="0" is not a default - it is a wrong value dressed as one.
func TestCronJobGetsNoPortEnvVar(t *testing.T) {
	c := base()
	c.Kind = config.KindCronJob
	c.Schedule = "*/5 * * * *"
	c.Port = 0

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "cronjob.yaml" {
			body = o.Body
		}
	}
	if strings.Contains(body, "PORT") {
		t.Errorf("a cronjob was given a PORT:\n%s", body)
	}

	// And `env:` with nothing under it parses as null rather than as an
	// empty list - a key that says nothing.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parsing cronjob.yaml: %v", err)
	}
	spec, _ := podTemplate(t, doc, config.KindCronJob)["spec"].(map[string]any)
	containers, _ := spec["containers"].([]any)
	if len(containers) == 0 {
		t.Fatal("no containers")
	}
	ctr, _ := containers[0].(map[string]any)
	if v, present := ctr["env"]; present && v == nil {
		t.Error("env: is present but null; omit the key when there is nothing in it")
	}
}

// A cronjob that does declare env vars still gets them.
func TestCronJobKeepsItsOwnEnvVars(t *testing.T) {
	c := base()
	c.Kind = config.KindCronJob
	c.Schedule = "*/5 * * * *"
	c.Port = 0
	c.Env = map[string]config.EnvValue{"LOG_LEVEL": config.EnvLiteral("debug")}

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "cronjob.yaml" {
			body = o.Body
		}
	}
	if !strings.Contains(body, "name: LOG_LEVEL") {
		t.Errorf("a cronjob lost its env vars:\n%s", body)
	}
}

// The client floor reaches the pod as MIN_VERSION, so raising it is a
// config edit and a restart rather than a rebuild. That matters because
// the floor is raised in response to a client actively causing harm.
func TestMinVersionReachesThePodAsAnEnvVar(t *testing.T) {
	c := base()
	c.MinVersion = "v0.3.0"

	var body string
	for _, o := range mustAll(t, mustConfig(t, c)) {
		if o.Path == "deployment.yaml" {
			body = o.Body
		}
	}
	if !strings.Contains(body, "name: MIN_VERSION") || !strings.Contains(body, `value: "v0.3.0"`) {
		t.Errorf("MIN_VERSION did not reach the manifest:\n%s", body)
	}
}

// A floor every real client clears is a line that says nothing. Absent
// and explicitly-inert both render nothing.
func TestAnInertFloorIsNotWrittenToTheManifest(t *testing.T) {
	for _, v := range []string{"", config.DefaultMinVersion} {
		c := base()
		c.MinVersion = v

		var body string
		for _, o := range mustAll(t, mustConfig(t, c)) {
			if o.Path == "deployment.yaml" {
				body = o.Body
			}
		}
		if strings.Contains(body, "MIN_VERSION") {
			t.Errorf("minVersion %q was written to the manifest:\n%s", v, body)
		}
	}
}
