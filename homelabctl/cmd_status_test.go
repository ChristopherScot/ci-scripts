package main

import (
	"bytes"
	"strings"
	"testing"
)

// runStatus is the reporting half, separated from the kubectl call so a
// test can feed it the shapes Argo actually produces.
func TestStatusReportsWhatArgoSays(t *testing.T) {
	for _, tc := range []struct {
		name    string
		app     argoApp
		wantErr bool
		want    []string
	}{
		{
			name: "healthy is quiet and succeeds",
			app: func() argoApp {
				var a argoApp
				a.Status.Sync.Status = "Synced"
				a.Status.Health.Status = "Healthy"
				return a
			}(),
			want: []string{"Synced / Healthy"},
		},
		{
			name: "a rejected manifest shows the API server's own message",
			app: func() argoApp {
				var a argoApp
				a.Status.Sync.Status = "OutOfSync"
				a.Status.Health.Status = "Degraded"
				a.Status.Conditions = append(a.Status.Conditions, struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				}{"SyncError", `Cluster.postgresql.cnpg.io "db" is invalid: spec.instances`})
				return a
			}(),
			wantErr: true,
			want:    []string{"SyncError", "spec.instances", "OutOfSync / Degraded"},
		},
		{
			name: "degraded without a condition still fails",
			app: func() argoApp {
				var a argoApp
				a.Status.Sync.Status = "Synced"
				a.Status.Health.Status = "Degraded"
				return a
			}(),
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := report(&buf, "svc", tc.app)
			if tc.wantErr && err == nil {
				t.Error("an unhealthy service reported success")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("a healthy service reported: %v", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("output missing %q:\n%s", w, buf.String())
				}
			}
		})
	}
}

// A 40-char sha is trimmed; a branch or tag is left alone.
func TestShortRev(t *testing.T) {
	if got := shortRev(strings.Repeat("a", 40)); got != "aaaaaaa" {
		t.Errorf("sha not shortened: %q", got)
	}
	if got := shortRev("v1.2.3"); got != "v1.2.3" {
		t.Errorf("tag was mangled: %q", got)
	}
}

// A hand-written manifest is a RESOURCE inside the service's
// Application, not an Application of its own.
//
// A CNPG Cluster from manifests/ has no Argo app to look up, so without
// per-resource reporting a failed database reads only as "the service
// is Degraded" with no clue which resource or why.
func TestStatusNamesTheFailingResource(t *testing.T) {
	var a argoApp
	a.Status.Sync.Status = "OutOfSync"
	a.Status.Health.Status = "Degraded"
	a.Status.Resources = append(a.Status.Resources,
		struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Health struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"health"`
		}{Kind: "Deployment", Name: "svc", Status: "Synced"},
	)
	bad := a.Status.Resources[0]
	bad.Kind, bad.Name, bad.Status = "Cluster", "svc-db", "OutOfSync"
	bad.Health.Status = "Degraded"
	bad.Health.Message = "instance svc-db-1 is not ready"
	a.Status.Resources = append(a.Status.Resources, bad)

	var buf bytes.Buffer
	if err := report(&buf, "svc", a); err == nil {
		t.Error("a degraded service reported success")
	}
	out := buf.String()
	if !strings.Contains(out, "cluster/svc-db") {
		t.Errorf("the failing resource was not named:\n%s", out)
	}
	if !strings.Contains(out, "instance svc-db-1 is not ready") {
		t.Errorf("the resource's own message was dropped:\n%s", out)
	}
	// Healthy resources stay quiet, or the one line that matters is
	// buried under every Service and Deployment.
	if strings.Contains(out, "deployment/svc") {
		t.Errorf("a healthy resource was listed:\n%s", out)
	}
}
