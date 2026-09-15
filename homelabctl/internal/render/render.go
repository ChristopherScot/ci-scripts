// Package render turns a config into Kubernetes manifests and the Argo
// Application. Nothing here is language-specific: a Go service and a Node
// service produce identical manifests given identical config.
package render

import (
	"fmt"
	"sort"
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
func All(c *config.Config, imageRef string) []Output {
	var out []Output
	add := func(path, body string) {
		if ov, ok := c.Overrides[path]; ok {
			body = strings.ReplaceAll(ov, ImagePlaceholder, imageRef)
		}
		out = append(out, Output{Path: path, Body: body})
	}

	add("namespace.yaml", namespace(c))

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
	return out
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

func namespace(c *config.Config) string {
	// Pod Security Admission enforces at the namespace what the container
	// securityContext only requests. The level must match what the pod
	// actually asks for: enforcing `restricted` on a hardened:false
	// service rejects its own pod at admission, and Argo still reports
	// Synced while nothing runs.
	level := "restricted"
	if !c.Hardened() {
		level = "baseline"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    pod-security.kubernetes.io/enforce: %s
    pod-security.kubernetes.io/enforce-version: latest
`, c.Namespace, level)
}

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

	b.WriteString("          env:\n")
	fmt.Fprintf(&b, "            - name: PORT\n              value: %q\n", fmt.Sprintf("%d", c.Port))
	for _, k := range sortedKeys(c.Env) {
		fmt.Fprintf(&b, "            - name: %s\n              value: %q\n", k, c.Env[k])
	}
	if c.Secrets != nil {
		fmt.Fprintf(&b, "          envFrom:\n            - secretRef:\n                name: %s\n", c.SecretName())
	}

	fmt.Fprintf(&b, `          resources:
            requests:
              cpu: %s
              memory: %s
            limits:
              memory: %s
`, c.Resources.CPURequest, c.Resources.MemoryRequest, c.Resources.MemoryLimit)

	if c.Hardened() {
		// readOnlyRootFilesystem without a writable /tmp breaks anything
		// that calls os.CreateTemp - and Node writes there routinely.
		b.WriteString(`          volumeMounts:
            - name: tmp
              mountPath: /tmp
`)
		b.WriteString(`          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            runAsUser: 65532
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
`)
	}
	// Readiness polls faster than liveness: a slow readiness probe leaves a
	// rolling pod taking traffic before it is ready, while an aggressive
	// liveness probe restarts pods that are merely busy.
	fmt.Fprintf(&b, `          readinessProbe:
            httpGet: { path: %s, port: %d }
            initialDelaySeconds: 2
            periodSeconds: 5
            timeoutSeconds: 3
            failureThreshold: 3
          livenessProbe:
            httpGet: { path: %s, port: %d }
            initialDelaySeconds: 10
            periodSeconds: 30
            timeoutSeconds: 5
            failureThreshold: 5
`, c.Probes.Path, c.Port, c.Probes.Path, c.Port)

	if c.Hardened() {
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
	if !c.Metrics() {
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
	fmt.Fprintf(&b, "%s  - name: PORT\n%s    value: \"%d\"\n", pad, pad, c.Port)
	for _, k := range sortedKeys(c.Env) {
		fmt.Fprintf(&b, "%s  - name: %s\n%s    value: %q\n", pad, k, pad, c.Env[k])
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

	if c.Hardened() {
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
	if c.Hardened() {
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
			k, c.Secrets.VaultPath, strings.ToLower(k))
	}
	return b.String()
}

func ingress(c *config.Config) string {
	class := "external"
	if c.Ingress.Public {
		class = "public"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
`, c.Name, c.Namespace)
	if c.Ingress.Authelia {
		b.WriteString(`    nginx.ingress.kubernetes.io/auth-url: "http://authelia.authelia.svc.cluster.local/api/verify"
    nginx.ingress.kubernetes.io/auth-signin: "https://auth.home.chrisscotmartin.com/?rd=$scheme://$host$escaped_request_uri"
`)
	}
	fmt.Fprintf(&b, `spec:
  ingressClassName: %s
  tls:
    - hosts: [%s]
      secretName: %s-tls
  rules:
    - host: %s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: %s
                port:
                  number: 80
`, class, c.Ingress.Host, c.Name, c.Ingress.Host, c.Name)
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
