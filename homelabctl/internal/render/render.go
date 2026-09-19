// Package render turns a config into Kubernetes manifests and the Argo
// Application. Nothing here is language-specific: a Go service and a Node
// service produce identical manifests given identical config.
//
// # If a second deployment target is ever added
//
// This package IS the Kubernetes target. The intended shape for a second
// one - Terraform for an AWS deploy, say - is a sibling package with the
// same signature, config -> []Output, chosen by a `deploymentTarget:`
// field in config.yaml. Output{Path, Body} is already target-neutral: a
// .tf file is text with a path.
//
// No interface until that second target is real code. Go's implicit
// satisfaction means one can be lifted out of these function signatures
// later with no edits here, so declaring one now would buy nothing and
// fix the wrong methods - internal/runtime is the cautionary example,
// where a Runtime interface written for "the runtime that eventually
// needs more than data" still has exactly one implementation.
//
// What the config would need, from an audit of the current schema:
//
//   - Portable as-is: name, team, runtime, kind, image, port, env,
//     secrets (the NAME: property shape maps onto Secrets Manager's
//     JSON-key selector almost exactly), probes.path.
//   - Kubernetes-only: namespace, hardened, metrics, ingress.authelia,
//     and above all patches/overrides - keyed by Kubernetes resource
//     Kind and generated filename, so they have no AWS meaning at all.
//     These belong under a `kubernetes:` block if the config ever
//     grows per-target sections.
//   - Does not survive translation: resources. Fargate sells discrete
//     (cpu, memory) pairs - 256 CPU units allows only 512/1024/2048 MiB
//   - so a 10m CPU request has no expression there at all; the floor
//     is 0.25 vCPU. replicas is clean for ECS desiredCount and
//     meaningless for Lambda, whose nearest concept is a concurrency
//     ceiling rather than a target. schedule is a trap: Kubernetes uses
//     5-field cron, EventBridge 6-field with a mandatory year, so
//     "0 3 * * *" is not valid there.
//
// The discipline that keeps this from becoming a lowest-common-
// denominator schema: the shared core shrinks when a target is added,
// never grows, and a target is allowed to REFUSE a config rather than
// invent a mapping - `hardened: true` means runAsNonRoot, drop ALL and
// a seccomp profile, and Fargate offers almost none of that, so a
// silent partial mapping would be a security lie. Refusing is the same
// instinct as ShadowedPatches and UnknownOverrides here.
//
// Per-target validation needs no new machinery: config.Schema is JSON
// Schema, and if/then/allOf on a `deploymentTarget:` discriminator lets
// one document require the aws block for aws targets, forbid it for
// kubernetes, and constrain cpu to Fargate's legal values. Verified
// against santhosh-tekuri/jsonschema v6, the compiler Load already
// uses, and Load validates the raw document BEFORE decoding - which is
// what makes conditional validation possible, since defaulting erases
// the difference between an omitted key and a rejected one.
package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

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

// UnknownOverrides returns override keys that name no generated file -
// a typo like "deploymnet.yaml", which otherwise overrides nothing while
// the author believes their file is in charge. All rejects them.
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
// imageRef is what a rendered manifest names. Always the repository at
// :latest, because argocd-image-updater owns the actual version: it
// resolves :latest to a digest and writes THAT into kustomization.yaml,
// which is why every committed deployment.yaml in this cluster says
// :latest and none carries a digest.
//
// Rendering therefore needs no image argument, and not having one is
// what makes All deterministic - the same config produces the same
// manifests, so CI can render and diff.
func imageRef(c *config.Config) string {
	return c.Image.Repository + ":latest"
}

// ManifestDir is where hand-written manifests are rendered, relative to
// the service's deploy directory.
//
// A subdirectory rather than the deploy root, so a hand-written file
// cannot take a generated file's name. Flat, they shared one namespace
// with no arbitration: manifests: [service.yaml] was written after the
// generated Service and replaced it on disk, and under prune: true Argo
// deletes the live one. manifests: [argocd.json] was worse - excluded
// from resources: by design AND overwritten, so the resource existed
// nowhere while every command reported success.
//
// A collision check catches all of that, and is kept. This makes the
// collision unrepresentable instead, which is the stronger guarantee:
// the two namespaces are now separate directories.
const ManifestDir = "manifests"

// manifestPath is where a named manifest is rendered.
func manifestPath(name string) string {
	return ManifestDir + "/" + name
}

// Files lists the paths a service's deploy directory will contain,
// without rendering any of them.
//
// For callers that need to KNOW the file set rather than produce it -
// init, so `--overwrite` can validate a filename against the deploy
// plane's inventory. Rendering to answer that question meant calling
// All with a zero Source, which needs a git remote it does not have at
// scaffold time: the result was an argocd.json missing repoURL and
// manifestPath, and a hard failure for any config carrying manifests:.
//
// Shares selectedFiles with All, so the two cannot disagree about which
// files a config produces. Listing them a second time by hand is the
// bug this function exists to remove.
func Files(c *config.Config) []string {
	paths := make([]string, 0, 8)
	for _, f := range selectedFiles(c) {
		paths = append(paths, f.path)
	}
	for _, name := range c.Manifests {
		paths = append(paths, manifestPath(name))
	}
	paths = append(paths, "kustomization.yaml", AppEntryFile)
	return paths
}

// selected is one generated manifest: the file name and the function
// that produces its body, deferred so Files never renders.
type selected struct {
	path string
	body func(c *config.Config, imageRef string) string
}

// selectedFiles is which generated manifests a config produces.
//
// The single source of truth for that question. All renders these;
// Files names them. Both walk this slice, so a new manifest kind is one
// entry rather than two lists to keep in step.
//
// Excludes kustomization.yaml (it lists the others, so it is appended
// last) and argocd.json (generator input, not a Kubernetes resource).
func selectedFiles(c *config.Config) []selected {
	var out []selected
	// Language and shape are independent: the runtime decided how this
	// is built, the kind decides what it becomes. A cron job has no
	// Service, no probes and no rollout strategy - a pod that exits on
	// purpose has nothing to keep ready.
	if c.IsCronJob() {
		out = append(out, selected{"cronjob.yaml", cronJob})
	} else {
		out = append(out, selected{"deployment.yaml", deployment})
		out = append(out, selected{"service.yaml", func(c *config.Config, _ string) string { return service(c) }})
	}
	if c.Secrets != nil {
		out = append(out, selected{"externalsecret.yaml", func(c *config.Config, _ string) string { return externalSecret(c) }})
	}
	if c.Ingress != nil {
		out = append(out, selected{"ingress.yaml", func(c *config.Config, _ string) string { return ingress(c) }})
	}
	return out
}

func All(c *config.Config, src Source) ([]Output, error) {
	imageRef := imageRef(c)
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
	for _, f := range selectedFiles(c) {
		add(f.path, f.body(c, imageRef))
	}
	// Hand-written manifests, copied verbatim and listed alongside the
	// generated ones.
	//
	// NOT through add(): add() offers a body to patches and overrides,
	// and neither makes sense here. A patch keyed by kind would apply to
	// a resource this tool does not generate and cannot reason about,
	// and an override replacing a file the author already wrote by hand
	// is just a second copy of it - both would be silent no-ops of
	// exactly the kind ShadowedPatches exists to catch.
	// Duplicates only. A name cannot collide with a generated file any
	// more - those render at the deploy root and these render under
	// manifests/ - but the same file listed twice still appears twice in
	// resources:, which kustomize refuses as a duplicate id.
	seen := map[string]bool{}

	for _, name := range c.Manifests {
		if seen[name] {
			return nil, fmt.Errorf("manifest %q is listed twice", name)
		}
		seen[name] = true

		body, ok := src.Manifests[name]
		if !ok {
			// A named file that was never read is a typo or a deleted
			// file; rendering without it would quietly drop a resource
			// that Argo then prunes from the cluster.
			return nil, fmt.Errorf("manifest %q is listed in config but was not found", name)
		}
		if err := validManifest(name, body); err != nil {
			return nil, err
		}
		out = append(out, Output{Path: manifestPath(name), Body: body})
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
	if unknown := UnknownOverrides(c, out); len(unknown) > 0 {
		return nil, fmt.Errorf("overrides name file(s) this service does not generate: %s (it renders: %s)",
			strings.Join(unknown, ", "), strings.Join(resourceNames(out), ", "))
	}
	// After the manifest checks and deliberately NOT through add(): this
	// is generator input, not a Kubernetes resource. Passing it through
	// add() would offer it to patches keyed by resource kind, let an
	// override replace it, and list it in kustomization.yaml's resources,
	// where Argo would try to apply a JSON file as a manifest.
	entry, err := AppEntry(c, src)
	if err != nil {
		return nil, err
	}
	out = append(out, Output{Path: AppEntryFile, Body: entry})

	if shadowed := ShadowedPatches(c, out); len(shadowed) > 0 {
		return nil, fmt.Errorf("these patches are shadowed by an override and would not apply: %s\n"+
			"an override replaces a file wholesale, so a patch for a kind inside it never runs.\n"+
			"fold the patch into the override, or drop the override and patch the generated file",
			strings.Join(shadowed, ", "))
	}
	return out, nil
}

// ShadowedPatches reports patches that an override prevents from ever
// applying, as "Kind (in path)".
//
// An override replaces a whole file and a patch targets a kind, so when
// an override covers the file containing that kind the patch is skipped
// - previously in silence. That is this package's worst failure mode:
// render succeeds, Argo reports Synced and Healthy, and the adjustment
// the author wrote simply is not there.
//
// Neither existing check caught it. UnknownPatchKinds asks whether the
// kind is generated, and it is; the patch just never reaches the output.
//
// Kinds are read from the override body rather than the generated one,
// because the override is what ends up on disk - and a single file can
// hold several documents, as ingress.yaml does for a split host.
func ShadowedPatches(c *config.Config, outs []Output) []string {
	if len(c.Patches) == 0 || len(c.Overrides) == 0 {
		return nil
	}
	var shadowed []string
	for _, o := range outs {
		ov, ok := c.Overrides[o.Path]
		if !ok {
			continue
		}
		for _, kind := range documentKinds(ov) {
			if _, patched := c.Patches[kind]; patched {
				shadowed = append(shadowed, fmt.Sprintf("%s (in %s)", kind, o.Path))
			}
		}
	}
	sort.Strings(shadowed)
	return shadowed
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

// validManifest rejects a file that would fail the whole Argo sync.
//
// kustomization.yaml lists these under resources:, and Argo applies
// every document it finds there. A file that is not a Kubernetes
// manifest - a stray README, a values file, a JSON blob - does not fail
// on its own: it fails the SYNC, taking the Deployment and Service with
// it. The same reasoning keeps argocd.json out of resourceNames.
//
// Checking apiVersion and kind rather than validating against a schema:
// these resources are by definition kinds this tool does not know, and a
// CRD it has never heard of is the point. This catches the file that is
// not a manifest at all, which is the mistake that actually happens.
func validManifest(name, body string) error {
	// A real YAML decoder, not strings.Split(body, "\n---\n").
	//
	// The split misses the separator whenever the file is not in exactly
	// that shape - CRLF line endings make it "---\r", and "--- " or
	// "--- # note" do not match either. yaml.Unmarshal then decodes only
	// the FIRST document of the stream and returns nil, so everything
	// after it went unvalidated and unreported.
	//
	// A file edited on Windows with a bad second document therefore
	// passed render and check, and kustomize refused the whole
	// directory - the Deployment and Service failed to sync along with
	// it. This function's own error text names that consequence, which
	// it was failing to prevent.
	dec := yaml.NewDecoder(strings.NewReader(body))
	docs := 0
	for {
		var node map[string]any
		err := dec.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("manifest %s: %w", name, err)
		}
		// A document that is only comments decodes to nothing; it is
		// not a resource and not an error.
		if node == nil {
			continue
		}
		docs++
		if node["apiVersion"] == nil || node["kind"] == nil {
			return fmt.Errorf("manifest %s: document %d has no apiVersion/kind, "+
				"so applying it would fail the whole sync", name, docs)
		}
	}
	if docs == 0 {
		return fmt.Errorf("manifest %s is empty", name)
	}
	return nil
}

// resourceNames is what kustomization.yaml lists under `resources:`.
//
// Only Kubernetes manifests belong there. kustomization.yaml would list
// itself, and argocd.json is input for the ApplicationSet generator, not
// a resource - listing either makes Argo try to apply it, and a JSON file
// applied as a manifest fails the whole sync.
func resourceNames(out []Output) []string {
	var names []string
	for _, o := range out {
		if o.Path == "kustomization.yaml" || o.Path == AppEntryFile {
			continue
		}
		names = append(names, o.Path)
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
	// PORT is a default, not an addition. Emitting it unconditionally and
	// then ranging over Env would write the key TWICE for a config that
	// sets it explicitly; Kubernetes accepts that and silently keeps the
	// last one, so the duplicate is invisible until the wrong value wins.
	//
	// A cronjob has no port, and PORT="0" is not a default - it is a
	// wrong value dressed as one. A job that reads it to decide where to
	// listen would bind a random port; one that ignores it is merely
	// carrying a lie. Either way there is nothing to serve.
	env := map[string]string{}
	if c.Port != 0 {
		env["PORT"] = strconv.Itoa(c.Port)
	}
	for k, v := range c.Env {
		env[k] = v
	}
	// PORT leads, then the rest sorted. Sorting it in with the others
	// would reorder every already-rendered manifest, so the first diff
	// after this change would be churn in every service at once.
	var keys []string
	if _, ok := env["PORT"]; ok {
		keys = append(keys, "PORT")
	}
	for _, k := range sortedKeys(env) {
		if k != "PORT" {
			keys = append(keys, k)
		}
	}
	// `env:` with nothing under it parses as null, not as an empty list.
	// Kubernetes accepts it, but it is a key that says nothing.
	if len(keys) > 0 {
		b.WriteString(pad + "env:\n")
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
%s          labels:
            app: %s
            team: %s
        spec:
          restartPolicy: Never
          securityContext:
            seccompProfile:
              type: RuntimeDefault
%s          containers:
            - name: %s
              image: %s
`, c.Name, c.Namespace, c.Name, c.Team, c.Schedule, tz,
		// Four spaces, not two: metricsAnnotations is written for the
		// Deployment, whose pod template sits two levels shallower. At
		// two, the annotations became a SIBLING of metadata rather than
		// a child - present in the file, accepted by the API server,
		// and silently discarded as an unknown field on PodSpec. Every
		// cronjob with metrics: true was invisible to Alloy.
		indentBlock(metricsAnnotations(c), "    "), c.Name, c.Team,
		indentBlock(serviceAccountName(c), "    "),
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
// esoAPIVersion is the External Secrets API these manifests declare.
//
// One constant rather than a literal at each use, because every service
// shares it: when ESO drops a version, every SecretStore and
// ExternalSecret in the fleet becomes unappliable at the same moment,
// and the failure is quiet - the ExternalSecret stops refreshing while
// the Secret it already created lingers, so pods keep running on
// credentials nobody is renewing.
//
// v1beta1 is what this cluster serves (ESO v0.11.0, which does not
// offer v1 at all). ESO 0.16 added v1 and 0.17 removed v1beta1, so
// upgrading past 0.16 means changing this line and re-rendering every
// service. `check` compares it against what the cluster serves, so a
// mismatch is reported rather than discovered at sync time.
// ESOAPIVersion is read by `check`, which compares it against the
// cluster.
const ESOAPIVersion = "external-secrets.io/v1beta1"

func externalSecret(c *config.Config) string {
	var b strings.Builder
	sa, store, secret := c.ServiceAccountName(), c.SecretStoreName(), c.SecretName()
	fmt.Fprintf(&b, `apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
---
apiVersion: ESO_API_VERSION
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
apiVersion: ESO_API_VERSION
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
			k.Env, k.PathUnder(c.Secrets.VaultPath), k.Property)
	}
	return strings.ReplaceAll(b.String(), "ESO_API_VERSION", ESOAPIVersion)
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
	// Grouped by controller AND by certificate, because both decide
	// which Ingress a host belongs on.
	//
	// public used to be a property of the whole Ingress, so a service
	// with a LAN name and a WAN name had to choose one controller for
	// both: exposing pokemon.chrisscotmartin.com dragged
	// pokemon.home.chrisscotmartin.com onto the public controller with
	// it, a name resolving to 192.168.50.225 served by the controller
	// facing the internet. A host is public or it is not.
	type group struct {
		class string
		tls   bool
	}
	// Ordered, so the rendered output does not depend on map iteration.
	order := []group{
		{"external", true}, {"external", false},
		{"public", true}, {"public", false},
	}
	hosts := map[group][]config.IngressHost{}
	for _, h := range c.Ingress.Hosts {
		class := "external"
		if h.IsPublic(c.Ingress.Public) {
			class = "public"
		}
		g := group{class, h.TLS}
		hosts[g] = append(hosts[g], h)
	}

	var b strings.Builder
	for _, g := range order {
		hs := hosts[g]
		if len(hs) == 0 {
			continue
		}
		// One name per document, suffixed by what makes it distinct.
		// Always, so the name says which controller and which
		// certificate posture an Ingress has without reading its spec.
		//
		// The cost is a one-time rename for services already public via
		// ingress.public - ntfy and approvald - where Argo deletes the
		// old object and creates the new one. That drops the route for
		// a moment and re-associates the certificate. Worth doing once
		// rather than carrying a conditional name forever.
		name := c.Name
		if g.class == "public" {
			name += "-public"
		}
		if !g.tls {
			name += "-lan"
		}
		if b.Len() > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(ingressDoc(c, g.class, name, hs, g.tls))
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
	if prefix := c.Ingress.Path; prefix != "" && prefix != "/" {
		// Strip the prefix before the request reaches the service, so a
		// service mounted on /api serves its own /pokemon unchanged and
		// does not need to know where it lives. The capture group in the
		// rule's path is what $2 refers to; use-regex is what makes
		// nginx read that path as a pattern rather than a literal.
		b.WriteString(`    nginx.ingress.kubernetes.io/use-regex: "true"
    nginx.ingress.kubernetes.io/rewrite-target: /$2
`)
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
		// Keyed on the INGRESS name, not the service name.
		//
		// A service with both a LAN host and a public one renders two
		// Ingresses, and c.Name gave them the same secret for different
		// host sets - cert-manager would reissue for one, overwrite the
		// other's certificate, and repeat. The Ingress name is already
		// unique per document, so this follows it.
		//
		// A service with a single Ingress is unaffected: its Ingress is
		// named after the service, so the secret name does not change.
		fmt.Fprintf(&b, "  tls:\n    - hosts: [%s]\n      secretName: %s-tls\n",
			strings.Join(names, ", "), name)
	}
	b.WriteString("  rules:\n")
	// The rule path. "/" is the ordinary case and stays a plain Prefix
	// match; a mounted service needs the regex form so rewrite-target has
	// a $2 to put back. Written as <prefix>(/|$)(.*) rather than
	// <prefix>(.*) so /apifoo does not match a service mounted on /api.
	rulePath, pathType := "/", "Prefix"
	if prefix := c.Ingress.Path; prefix != "" && prefix != "/" {
		rulePath, pathType = prefix+"(/|$)(.*)", "ImplementationSpecific"
	}
	for _, h := range hosts {
		fmt.Fprintf(&b, `    - host: %s
      http:
        paths:
          - path: %s
            pathType: %s
            backend:
              service:
                name: %s
                port:
                  number: 80
`, h.Name, rulePath, pathType, c.Name)
	}
	return b.String()
}

// Source is where a service's manifests live, as Argo has to fetch them.
//
// Not derived here: render is a pure function of config, and this is a
// fact about the working tree - which repo it was cloned from and where
// in it this service sits. The caller reads that from git and passes it
// in, so render stays testable without a repository.
type Source struct {
	RepoURL string // https://github.com/<owner>/<repo>
	Path    string // deploy directory, relative to the repo root

	// Manifests holds the contents of each file named in
	// config.Manifests, keyed by its base name.
	//
	// Read by the caller rather than by All, which takes no filesystem
	// and should not start: init renders a service that does not exist
	// on disk yet, and the render tests build a Config in Go and expect
	// the same output every time. Passing the bytes in keeps All a pure
	// function of its inputs, so "what does this config render to" stays
	// answerable without a working tree.
	Manifests map[string]string
}

// appPath joins the deploy directory to the service's own subdirectory,
// leaving an empty source empty rather than producing a bare name that
// would point Argo at the wrong repository root.
func appPath(deployDir, name string) string {
	if deployDir == "" {
		return ""
	}
	return deployDir + "/" + name
}

// AppEntryFile is where a service publishes its generator input. The
// ApplicationSet's files generator globs for this name, so it is part of
// that resource's contract rather than an arbitrary choice here.
const AppEntryFile = "argocd.json"

// AppParams is what the ApplicationSet generator reads for one service:
// the values its template cannot derive from the directory name alone.
//
// Field names are the generator's parameter names, so the template says
// {{ .namespace }} and this says `json:"namespace"`. Renaming one without
// the other leaves the template rendering an empty string into a live
// Application, so they are declared together here.
type AppParams struct {
	Name      string `json:"name"`
	Team      string `json:"team"`
	Namespace string `json:"namespace"`
	Image     string `json:"image"` // repository, no tag: the template appends :latest

	// Where Argo reads this service's manifests: its OWN repository, at
	// the deploy directory homelabctl renders into. Copying them into
	// the GitOps repo was the last manual step in the chain, and the one
	// people forget - a render that reports success while the cluster
	// keeps running the old manifests.
	//
	// Empty when the service is not in a git repo with an origin, which
	// is a scaffold that has not been pushed yet. The generator skips an
	// entry it cannot locate rather than pointing Argo at nothing.
	RepoURL string `json:"repoURL,omitempty"`
	// Named manifestPath, not path: the ApplicationSet's git file
	// generator supplies its own {{path}} - the directory of the matched
	// argocd.json - and it wins. A field called "path" here is read by
	// the generator and silently ignored, so every Application got the
	// entry's own directory as its source path and failed to sync with
	// no error anywhere.
	ManifestPath string `json:"manifestPath,omitempty"`
}

// AppEntry is the generator input for one service, as deploy/argocd.json.
//
// This replaces rendering a whole Argo Application per service. The
// Application is now one ApplicationSet template in the homelab repo, and
// a service publishes only the handful of values that differ - so a
// convention change (a sync option, an annotation) is one edit to the
// template rather than a re-render of every service.
//
// Marshalled rather than formatted: a name or team containing a quote
// would otherwise produce a file that parses as something else.
func AppEntry(c *config.Config, src Source) (string, error) {
	b, err := json.MarshalIndent(AppParams{
		Name:      c.Name,
		Team:      c.Team,
		Namespace: c.Namespace,
		Image:     c.Image.Repository,
		RepoURL:   src.RepoURL,
		// The service's own directory under deploy/, which is where
		// kustomization.yaml lands - Argo needs the directory holding
		// it, not the one above.
		ManifestPath: appPath(src.Path, c.Name),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
