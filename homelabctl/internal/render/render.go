// Package render turns a config into Kubernetes manifests and the Argo
// Application. Nothing here is language-specific: a Go service and a Node
// service produce identical manifests given identical config.
package render

import (
	"fmt"
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
	add("deployment.yaml", deployment(c, imageRef))
	add("service.yaml", service(c))
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
	return fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", c.Namespace)
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
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
        team: %s
    spec:
      containers:
        - name: %s
          image: %s
          ports:
            - containerPort: %d
`, c.Name, c.Namespace, c.Name, c.Team, c.Replicas, c.Name, c.Name, c.Team, c.Name, imageRef, c.Port)

	b.WriteString("          env:\n")
	fmt.Fprintf(&b, "            - name: PORT\n              value: %q\n", fmt.Sprintf("%d", c.Port))
	for _, k := range sortedKeys(c.Env) {
		fmt.Fprintf(&b, "            - name: %s\n              value: %q\n", k, c.Env[k])
	}
	if c.Secrets != nil {
		fmt.Fprintf(&b, "          envFrom:\n            - secretRef:\n                name: %s-secrets\n", c.Name)
	}

	fmt.Fprintf(&b, `          resources:
            requests:
              cpu: %s
              memory: %s
            limits:
              memory: %s
`, c.Resources.CPURequest, c.Resources.MemoryRequest, c.Resources.MemoryLimit)

	if *c.Hardened {
		b.WriteString(`          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            runAsUser: 65532
            readOnlyRootFilesystem: true
            capabilities:
              drop: [ALL]
`)
	}
	for _, probe := range []string{"livenessProbe", "readinessProbe"} {
		fmt.Fprintf(&b, `          %s:
            httpGet: { path: %s, port: %d }
            initialDelaySeconds: 5
            periodSeconds: 30
`, probe, c.Probes.Path, c.Port)
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
	fmt.Fprintf(&b, `apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-secrets-sa
  namespace: %s
---
apiVersion: external-secrets.io/v1beta1
kind: SecretStore
metadata:
  name: vault-%s
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
            name: external-secrets-sa
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: %s-secrets
  namespace: %s
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-%s
    kind: SecretStore
  target:
    name: %s-secrets
  data:
`, c.Namespace, c.Name, c.Namespace, c.Name, c.Name, c.Namespace, c.Name, c.Name)
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
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
