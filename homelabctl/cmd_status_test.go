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
