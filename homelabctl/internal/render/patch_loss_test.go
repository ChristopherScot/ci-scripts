package render

import (
	"strings"
	"testing"
)

// The patch that took pokedex down. It means "add DATABASE_URL", and
// because a patched list replaces rather than merges, it deletes every
// other field on the container.
//
// Before this guard, render succeeded and Argo refused the sync for 25
// minutes with "spec.template.spec.containers[0].image: Required
// value" - a message that names the symptom, arrives after the change
// is merged, and says nothing about the patch that caused it.
const outagePatch = `spec:
  template:
    spec:
      containers:
        - name: svc
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: svc-db-app
                  key: uri
`

func TestPatchThatStripsTheContainerIsRefused(t *testing.T) {
	c := mustConfig(t, base())
	c.Patches = map[string]string{"Deployment": outagePatch}

	_, err := All(c, Source{})
	if err == nil {
		t.Fatal("render succeeded; the patch silently removed the container's image")
	}

	// The message has to name what was lost and what to do instead.
	// The Kubernetes error names neither.
	for _, want := range []string{"image", "removed", "env:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// The guard must not make `patches:` useless for what it is actually
// for. A patch that only touches maps merges as it always did.
func TestPatchThatAddsAnAnnotationIsFine(t *testing.T) {
	c := mustConfig(t, base())
	c.Patches = map[string]string{"Deployment": `spec:
  template:
    metadata:
      annotations:
        example.com/owner: platform
`}

	body := findOutput(t, c, "deployment.yaml")
	if !strings.Contains(body, "example.com/owner") {
		t.Errorf("the annotation did not land:\n%s", body)
	}
	// And the container is still whole.
	if !strings.Contains(body, "image:") {
		t.Error("an annotation patch removed the image")
	}
}

// A patch that restates the container is allowed: it loses nothing, so
// the author has already done the work the guard asks for.
func TestPatchThatRestatesTheContainerIsAllowed(t *testing.T) {
	whole := findOutput(t, mustConfig(t, base()), "deployment.yaml")
	if !strings.Contains(whole, "image:") {
		t.Fatal("the generated deployment has no image to preserve")
	}

	c := mustConfig(t, base())
	c.Patches = map[string]string{"Deployment": `spec:
  template:
    spec:
      containers:
        - name: svc
          image: ghcr.io/o/svc:latest
          ports:
            - containerPort: 3000
          env:
            - name: EXTRA
              value: "1"
          resources:
            requests:
              cpu: 10m
          volumeMounts:
            - name: tmp
              mountPath: /tmp
          securityContext:
            allowPrivilegeEscalation: false
          readinessProbe:
            httpGet:
              path: /healthz
              port: 3000
          livenessProbe:
            httpGet:
              path: /healthz
              port: 3000
`}
	if _, err := All(c, Source{}); err != nil {
		t.Fatalf("a patch that keeps every field was refused: %v", err)
	}
}

// A CronJob buries its pod template one level deeper. The guard has to
// find the container there too, or the shape with no readiness probe
// is the one that can still be silently stripped.
func TestGuardCoversCronJobs(t *testing.T) {
	c := base()
	c.Kind = "cronjob"
	c.Schedule = "0 3 * * *"
	c = mustConfig(t, c)
	c.Patches = map[string]string{"CronJob": `spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: svc
              env:
                - name: X
                  value: "1"
`}
	_, err := All(c, Source{})
	if err == nil {
		t.Fatal("a CronJob patch stripped the container and was accepted")
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("error does not name the lost image:\n%s", err)
	}
}
