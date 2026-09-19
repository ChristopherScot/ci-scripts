package render

import (
	"strings"
	"testing"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/config"
)

// An env var is resolved when the container starts and never again, so
// a rotated credential does not reach a running pod. Without the
// annotation the failure is "the password changed and the pod is still
// using the old one", which nothing reports.
func TestReloaderFollowsAnEnvSecretRef(t *testing.T) {
	c := base()
	c.Env = map[string]config.EnvValue{
		"DATABASE_URL": {Secret: &config.SecretKeyRef{Name: "svc-db-app", Key: "uri"}},
	}
	body := findOutput(t, mustConfig(t, c), "deployment.yaml")
	if !strings.Contains(body, `reloader.stakater.com/secret-reload-on-change: "svc-db-app"`) {
		t.Errorf("no reload annotation for the referenced secret:\n%s", body)
	}
}

// The ESO-synced secret counts too - it was already unwatched before
// this change, so every service using `secrets:` was on manual restart.
func TestReloaderFollowsTheServiceSecret(t *testing.T) {
	c := base()
	c.Secrets = &config.Secrets{VaultPath: "svc/config", Keys: config.EnvKeys("TOKEN")}
	body := findOutput(t, mustConfig(t, c), "deployment.yaml")
	if !strings.Contains(body, `reloader.stakater.com/secret-reload-on-change: "svc-secrets"`) {
		t.Errorf("no reload annotation for the service secret:\n%s", body)
	}
}

// Both sources, sorted and deduplicated, in one comma-separated value.
func TestReloaderMergesBothSources(t *testing.T) {
	c := base()
	c.Secrets = &config.Secrets{VaultPath: "svc/config", Keys: config.EnvKeys("TOKEN")}
	c.Env = map[string]config.EnvValue{
		"DATABASE_URL": {Secret: &config.SecretKeyRef{Name: "svc-db-app", Key: "uri"}},
		"CACHE_URL":    {Secret: &config.SecretKeyRef{Name: "svc-db-app", Key: "host"}},
	}
	body := findOutput(t, mustConfig(t, c), "deployment.yaml")
	if !strings.Contains(body, `reloader.stakater.com/secret-reload-on-change: "svc-db-app,svc-secrets"`) {
		t.Errorf("secrets not merged and sorted into one annotation:\n%s", body)
	}
}

// A service reading no secrets gets no annotation - an annotation
// naming nothing is a line that says nothing.
func TestReloaderAbsentWithoutSecrets(t *testing.T) {
	body := findOutput(t, mustConfig(t, base()), "deployment.yaml")
	if strings.Contains(body, "reloader.stakater.com") {
		t.Errorf("a service with no secrets got a reload annotation:\n%s", body)
	}
}

// Metrics and reload annotations share one block. Two `annotations:`
// keys would be a duplicate mapping key, and YAML keeps the last -
// so the metrics annotations would silently disappear and Alloy would
// stop scraping with nothing to say about it.
func TestAnnotationsShareOneBlock(t *testing.T) {
	c := base()
	c.Env = map[string]config.EnvValue{
		"DATABASE_URL": {Secret: &config.SecretKeyRef{Name: "svc-db-app", Key: "uri"}},
	}
	body := findOutput(t, mustConfig(t, c), "deployment.yaml")

	if n := strings.Count(body, "annotations:"); n != 1 {
		t.Errorf("%d `annotations:` keys, want 1 - a duplicate key drops one block:\n%s", n, body)
	}
	for _, want := range []string{"k8s.grafana.com/scrape", "reloader.stakater.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing from the merged block:\n%s", want, body)
		}
	}
}

// A cronjob has no port and so no metrics annotations, but it still
// reads secrets. The reload annotation must not depend on the metrics
// block existing.
func TestReloaderOnACronJobWithoutMetrics(t *testing.T) {
	c := base()
	c.Kind = "cronjob"
	c.Schedule = "0 3 * * *"
	c.Env = map[string]config.EnvValue{
		"DATABASE_URL": {Secret: &config.SecretKeyRef{Name: "svc-db-app", Key: "uri"}},
	}
	body := findOutput(t, mustConfig(t, c), "cronjob.yaml")
	if !strings.Contains(body, "reloader.stakater.com/secret-reload-on-change") {
		t.Errorf("a cronjob reading a secret got no reload annotation:\n%s", body)
	}
}
