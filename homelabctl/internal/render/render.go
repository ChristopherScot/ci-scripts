// Package render turns a config into Kubernetes manifests and the Argo
// Application. Nothing here is language-specific: a Go service and a Node
// service produce identical manifests given identical config.
package render

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// ImagePlaceholder is substituted in overrides so a hand-written manifest
// still tracks the built image. Replaced literally rather than by running
// the override through text/template, because overrides routinely contain
// other templating languages - an ExternalSecret body carries ESO's own
// {{ .username }} and b64enc, which Go's parser rejects.
const ImagePlaceholder = "{{ .ImageURL }}"

// Output is one rendered file.
type Output struct {
	Path string
	Body string
}

// UnknownOverrides returns override keys that name no generated file.
// Silently ignoring them means an author believes their override applied
// when it did not.
func UnknownOverrides(c *config.Config, outs []Output) []string {
	if len(c.Overrides) == 0 {
		return nil
	}
	known := make(map[string]bool, len(outs))
	for _, o := range outs {
		known[o.Path] = true
	}
	var unknown []string
	for k := range c.Overrides {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// All renders every manifest for a service. imageRef is the full image
// reference to deploy; pass a full 40-char SHA or a digest, never an
// abbreviated SHA - short tags do not exist in the registry and produce
// ImagePullBackOff.
//
// It reports a patch that could not be applied rather than silently
// emitting an unpatched manifest. This used to be split in two - a
// convenience All() that dropped the error to keep a simple signature,
// and an AllErr() that recovered it by applying every patch a SECOND
// time. Any caller reaching for the shorter name got manifests with their
// patches quietly missing, which is the exact failure this package exists
// to prevent: Argo reports Synced and Healthy, and the config lied.
func All(c *config.Config, imageRef string) ([]Output, error) {
	var out []Output
	var err error
	add := func(path, body string) {
		if err != nil {
			return
		}
		// Whole-file override wins if present, but it is the loud escape
		// hatch; patches are the ordinary way to adjust a manifest.
		if ov, ok := c.Overrides[path]; ok {
			body = strings.ReplaceAll(ov, ImagePlaceholder, imageRef)
		} else if body, err = applyPatches(body, c.Patches, imageRef); err != nil {
			return
		}
		out = append(out, Output{Path: path, Body: body})
	}

	// Language and shape are independent: the runtime decided how this is
	// built, the kind decides what it becomes. A cron job has no Service,
	// no probes and no rollout strategy - a pod that exits on purpose has
	// nothing to keep ready.
	if c.IsCronJob() {
		add("cronjob.yaml", cronJob(c, imageRef))
	} else {
		add("deployment.yaml", deployment(c, imageRef))
		add("service.yaml", service(c))
	}
	if c.Secrets != nil {
		add("externalsecret.yaml", externalSecret(c))
	}
	if c.Ingress != nil {
		add("ingress.yaml", ingress(c))
	}
	// Last: it lists the files above.
	add("kustomization.yaml", kustomization(c, out))
	if err != nil {
		return nil, err
	}

	if unknown := UnknownPatchKinds(c, out); len(unknown) > 0 {
		return nil, fmt.Errorf("patches name kind(s) this service does not generate: %s (it has: %s)",
			strings.Join(unknown, ", "), strings.Join(PatchedKinds(out), ", "))
	}
	return out, nil
}

// UnknownPatchKinds reports patch keys naming a resource kind this service
// never generates - a typo that would otherwise apply to nothing, silently.
func UnknownPatchKinds(c *config.Config, outs []Output) []string {
	if len(c.Patches) == 0 {
		return nil
	}
	have := map[string]bool{}
	for _, k := range PatchedKinds(outs) {
		have[k] = true
	}
	var unknown []string
	for k := range c.Patches {
		if !have[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func resourceNames(out []Output) []string {
	var names []string
	for _, o := range out {
		if o.Path != "kustomization.yaml" {
			names = append(names, o.Path)
		}
	}
	return names
}

// kustomization is not optional. argocd-image-updater can only write image
// bumps into Kustomize, Helm or Plugin sources; without this file Argo
// classifies the app as Directory, the updater skips it with only a log
// warning, and the app sits Synced/Healthy on a stale image forever.
func kustomization(c *config.Config, out []Output) string {
	var b strings.Builder
	b.WriteString("# Required: argocd-image-updater writes the image digest into the\n")
	b.WriteString("# `images:` block below. Without this file the app renders as a plain\n")
	b.WriteString("# Directory and image automation silently skips it.\n")
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
	for _, n := range resourceNames(out) {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	fmt.Fprintf(&b, "images:\n  - name: %s\n    newTag: latest\n", c.Image.Repository)
	return b.String()
}

// No namespace.yaml is rendered, deliberately.
//
// A Namespace is shared infrastructure, and this tool works at the level
// of one app. Pod Security Admission is enforced by a label on the
// NAMESPACE, so rendering one meant a service imposing its own security
// level on every neighbour: adding the redirector to `shlink` would have
// labelled that namespace `restricted`, which shlink itself and
// shlink-web cannot meet. They would have kept running - PSA gates
// admission, not running pods - and then failed to start again after any
// rollout or node drain, as an outage nobody would connect to a change
// in a different service.
//
// What this tool CAN do is make its own pod acceptable anywhere,
// including in a namespace someone else has locked down. The generated
// securityContext meets `restricted` on its own: runAsNonRoot,
// allowPrivilegeEscalation false, all capabilities dropped,
// seccompProfile RuntimeDefault, and never privileged. Verified by
// dry-running the generated pod into a restricted namespace.
//
// Creating the namespace stays with whoever owns it. The Argo
// Application sets CreateNamespace=true, so a new one still appears
// without anyone applying YAML by hand; it simply arrives unlabelled,
// and its security level is set by the person who owns the namespace
// rather than by whichever app happened to be generated last.
func deployment(c *config.Config, imageRef string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    team: %s
spec:
  replicas: %d
  # At one replica the default rolling update takes the only pod down
  # first; surging instead keeps the service up across a deploy.
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  revisionHistoryLimit: 3
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
%s      labels:
        app: %s
        team: %s
    spec:
      # Required by the restricted Pod Security Standard, and the cheapest
      # hardening available.
%s      # Longer than the server's 20s drain so shutdown finishes before
      # SIGKILL.
      terminationGracePeriodSeconds: 30
      securityContext:
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: %s
          image: %s
          ports:
            - containerPort: %d
`, c.Name, c.Namespace, c.Name, c.Team, c.Replicas, c.Name, metricsAnnotations(c), c.Name, c.Team, serviceAccountName(c), c.Name, imageRef, c.Port)

	// Shared with the CronJob: env, secrets, resources and hardening are
	// the same container either way, and a second copy here is how PORT
	// came to be emitted twice in one shape and once in the other.
	b.WriteString(containerBody(c, "          "))
	// Readiness polls faster than liveness: a slow readiness probe leaves a
	// rolling pod taking traffic before it is ready, while an aggressive
	// liveness probe restarts pods that are merely busy.
	fmt.Fprintf(&b, `          readinessProbe:
            httpGet:
              path: %s
              port: %d
            initialDelaySeconds: 2
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
          livenessProbe:
            httpGet:
              path: %s
              port: %d
            initialDelaySeconds: 10
            periodSeconds: 30
            timeoutSeconds: 5
            failureThreshold: 5
`, c.Probes.Path, c.Port, c.Probes.Path, c.Port)

	if c.Hardened {
		b.WriteString(`      volumes:
        - name: tmp
          emptyDir: {}
`)
	}
	return b.String()
}

// serviceAccountName runs the pod under the ServiceAccount the Vault role
// is bound to. Without it the pod runs as `default`, so the per-app
// identity the ExternalSecret sets up is not the identity the workload
// actually has - which only bites once something gives the pod direct
// Vault or API access.
func serviceAccountName(c *config.Config) string {
	sa := c.ServiceAccountName()
	if sa == "" {
		return ""
	}
	return fmt.Sprintf("      serviceAccountName: %s\n", sa)
}

// metricsAnnotations wires the pod into the cluster's metrics collection.
// Alloy discovers scrape targets by annotation - nothing is collected
// without these, and the absence is silent.
func metricsAnnotations(c *config.Config) string {
	// No port means nothing to scrape. A cronjob has no Service and no
	// port, and annotating one anyway pointed Alloy at port 0 forever.
	if !c.Metrics || c.Port == 0 {
		return ""
	}
	return fmt.Sprintf(`      annotations:
        k8s.grafana.com/scrape: "true"
        k8s.grafana.com/metrics.path: "/metrics"
        k8s.grafana.com/metrics.portNumber: "%d"
`, c.Port)
}

// cronJob renders a scheduled workload. It shares the container spec with
// deployment - image, env, resources, hardening - and differs in
// everything about lifecycle.
// containerBody renders the container fields that are identical across
// workload shapes - env, secrets, resources, hardening. Probes belong to
// the deployment only: a job that runs to completion has no readiness to
// report. pad is the indent the caller nests it at.
func containerBody(c *config.Config, pad string) string {
	var b strings.Builder
	b.WriteString(pad + "env:\n")
	// PORT is a default, not an addition. Emitting it unconditionally and
	// then ranging over Env would write the key TWICE for a config that
	// sets it explicitly; Kubernetes accepts that and silently keeps the
	// last one, so the duplicate is invisible until the wrong value wins.
	env := map[string]string{"PORT": strconv.Itoa(c.Port)}
	for k, v := range c.Env {
		env[k] = v
	}
	// PORT leads, then the rest sorted. Sorting it in with the others
	// would reorder every already-rendered manifest, so the first diff
	// after this change would be churn in every service at once.
	keys := []string{"PORT"}
	for _, k := range sortedKeys(env) {
		if k != "PORT" {
			keys = append(keys, k)
		}
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "%s  - name: %s\n%s    value: %q\n", pad, k, pad, env[k])
	}
	if c.Secrets != nil {
		fmt.Fprintf(&b, "%senvFrom:\n%s  - secretRef:\n%s      name: %s\n",
			pad, pad, pad, c.SecretName())
	}
	fmt.Fprintf(&b, `%sresources:
%s  requests:
%s    cpu: %s
%s    memory: %s
%s  limits:
%s    memory: %s
`, pad, pad, pad, c.Resources.CPURequest, pad, c.Resources.MemoryRequest,
		pad, pad, c.Resources.MemoryLimit)

	if c.Hardened {
		fmt.Fprintf(&b, `%svolumeMounts:
%s  - name: tmp
%s    mountPath: /tmp
%ssecurityContext:
%s  allowPrivilegeEscalation: false
%s  runAsNonRoot: true
%s  runAsUser: 65532
%s  readOnlyRootFilesystem: true
%s  capabilities:
%s    drop: [ALL]
`, pad, pad, pad, pad, pad, pad, pad, pad, pad, pad)
	}
	return b.String()
}

func cronJob(c *config.Config, imageRef string) string {
	var b strings.Builder
	tz := c.TimeZone
	if tz == "" {
		tz = "UTC"
	}
	fmt.Fprintf(&b, `apiVersion: batch/v1
kind: CronJob
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    team: %s
spec:
  schedule: "%s"
  # Without an explicit zone Kubernetes schedules in UTC, so a schedule
  # that reads as 3am fires at 11pm the previous evening in ET.
  timeZone: "%s"
  # Forbid: a run that overruns its interval must not have a second copy
  # started alongside it.
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 1
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      # A hung job must die rather than block every later run.
      activeDeadlineSeconds: 600
      backoffLimit: 2
      template:
        metadata:
%s        spec:
          restartPolicy: Never
          securityContext:
            seccompProfile:
              type: RuntimeDefault
%s          containers:
            - name: %s
              image: %s
`, c.Name, c.Namespace, c.Name, c.Team, c.Schedule, tz,
		indentBlock(metricsAnnotations(c), "  "), indentBlock(serviceAccountName(c), "    "),
		c.Name, imageRef)

	b.WriteString(containerBody(c, "              "))
	if c.Hardened {
		b.WriteString(`          volumes:
            - name: tmp
              emptyDir: {}
`)
	}
	return b.String()
}

// indentBlock shifts an already-rendered YAML fragment deeper, so the
// container and pod pieces can be shared between workload shapes that nest
// them at different depths.
func indentBlock(s, pad string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(pad + line + "\n")
	}
	return b.String()
}

func service(c *config.Config) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
spec:
  selector:
    app: %s
  ports:
    - port: 80
      targetPort: %d
`, c.Name, c.Namespace, c.Name, c.Port)
}

// externalSecret follows the per-app Vault convention: each service reads
// only its own kv path, via a role bound to its own ServiceAccount and
// namespace, so compromising one pod does not expose another app's secrets.
func externalSecret(c *config.Config) string {
	var b strings.Builder
	sa, store, secret := c.ServiceAccountName(), c.SecretStoreName(), c.SecretName()
	fmt.Fprintf(&b, `apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: %s
  namespace: %s
spec:
  provider:
    vault:
      server: http://vault.default.svc.cluster.local:8200
      path: kv
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: %s
          serviceAccountRef:
            name: %s
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: %s
  namespace: %s
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: %s
    kind: SecretStore
  target:
    name: %s
  data:
`, sa, c.Namespace, store, c.Namespace, c.VaultRoleName(), sa, secret, c.Namespace, store, secret)
	for _, k := range c.Secrets.Keys {
		fmt.Fprintf(&b, "    - secretKey: %s\n      remoteRef: { key: %s, property: %s }\n",
			k.Env, c.Secrets.VaultPath, k.Property)
	}
	return b.String()
}

// ingress renders one or two Ingress resources: one for the names that
// have a certificate, one for the names that cannot have one.
//
// They must be separate resources because ssl-redirect is a
// RESOURCE-scoped annotation, not a per-host one. Put a plain-HTTP name
// in the same Ingress as a certified one and there is no correct
// setting: leave the redirect on and the plain name 308s to a
// certificate that does not cover it; turn it off and the certified name
// stops being redirected too. Three services in this cluster were
// merged, and two of them served their admin UI over plain HTTP as a
// result.
//
// The plain resource deliberately carries no cluster-issuer annotation.
// That is what makes an impossible ACME order structurally impossible
// rather than merely avoided - cert-manager never looks at it.
func ingress(c *config.Config) string {
	class := "external"
	if c.Ingress.Public {
		class = "public"
	}

	var certified, plain []config.IngressHost
	for _, h := range c.Ingress.Hosts {
		if h.TLS {
			certified = append(certified, h)
		} else {
			plain = append(plain, h)
		}
	}

	var b strings.Builder
	if len(certified) > 0 {
		b.WriteString(ingressDoc(c, class, c.Name, certified, true))
	}
	if len(plain) > 0 {
		if b.Len() > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(ingressDoc(c, class, c.Name+"-lan", plain, false))
	}
	return b.String()
}

// ingressDoc renders one Ingress. tls decides whether it gets a
// certificate and the annotations that go with one.
func ingressDoc(c *config.Config, class, name string, hosts []config.IngressHost, tls bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  annotations:
`, name, c.Namespace)
	if tls {
		b.WriteString("    cert-manager.io/cluster-issuer: letsencrypt-prod\n")
	} else {
		// These names have no certificate, so a redirect to https sends
		// the browser to one that cannot match. Confined to this
		// resource, so the certified names keep their redirect.
		b.WriteString("    nginx.ingress.kubernetes.io/ssl-redirect: \"false\"\n")
	}
	if c.Ingress.Authelia {
		b.WriteString(`    nginx.ingress.kubernetes.io/auth-url: "http://authelia.authelia.svc.cluster.local/api/verify"
    nginx.ingress.kubernetes.io/auth-signin: "https://auth.home.chrisscotmartin.com/?rd=$scheme://$host$escaped_request_uri"
`)
	}
	fmt.Fprintf(&b, "spec:\n  ingressClassName: %s\n", class)
	if tls {
		names := make([]string, len(hosts))
		for i, h := range hosts {
			names[i] = h.Name
		}
		fmt.Fprintf(&b, "  tls:\n    - hosts: [%s]\n      secretName: %s-tls\n",
			strings.Join(names, ", "), c.Name)
	}
	b.WriteString("  rules:\n")
	for _, h := range hosts {
		fmt.Fprintf(&b, `    - host: %s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: %s
                port:
                  number: 80
`, h.Name, c.Name)
	}
	return b.String()
}

// Application renders the Argo Application, including the full
// image-updater annotation set. Omitting write-back-target makes the
// updater write a separate .argocd-source file, leaving two places that
// both claim to set the image; omitting git-branch is warned against
// upstream when targetRevision is HEAD.
func Application(c *config.Config, repoURL, path string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s
  namespace: argocd
  labels:
    team: %s
  annotations:
    argocd-image-updater.argoproj.io/image-list: %s=%s:latest
    argocd-image-updater.argoproj.io/%s.update-strategy: digest
    argocd-image-updater.argoproj.io/write-back-method: git
    argocd-image-updater.argoproj.io/write-back-target: kustomization
    argocd-image-updater.argoproj.io/git-branch: main
  finalizers:
    - resources-finalizer.argocd.argoproj.io
spec:
  project: default
  source:
    repoURL: '%s'
    targetRevision: HEAD
    path: %s
  destination:
    server: 'https://kubernetes.default.svc'
    namespace: %s
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
`, c.Name, c.Team, c.Name, c.Image.Repository, c.Name, repoURL, path, c.Namespace)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
