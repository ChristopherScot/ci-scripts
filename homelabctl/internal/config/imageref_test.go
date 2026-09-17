package config

import "testing"

func TestIsAbbreviatedSHA(t *testing.T) {
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"ghcr.io/o/svc:abc1234", true},
		{"ghcr.io/o/svc:1234567", true},
		{"ghcr.io/o/svc:abcdefabcdef", true},   // 12, the upper bound
		{"ghcr.io/o/svc:abcdef", false},        // 6, too short to be one
		{"ghcr.io/o/svc:abcdefabcdefa", false}, // 13, too long
		{"ghcr.io/o/svc:latest", false},
		{"ghcr.io/o/svc:v1.2.3", false},
		{"ghcr.io/o/svc:abcdefg", false}, // g is not hex
		// A digest is the opposite of the problem: immutable, and what
		// argocd-image-updater writes.
		{"ghcr.io/o/svc@sha256:" + "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", false},
	} {
		if got := IsAbbreviatedSHA(tc.ref); got != tc.want {
			t.Errorf("IsAbbreviatedSHA(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

func TestImageRefInLine(t *testing.T) {
	for _, tc := range []struct {
		line string
		ref  string
		ok   bool
	}{
		{"          image: ghcr.io/o/svc:abc1234", "ghcr.io/o/svc:abc1234", true},
		{"image: ghcr.io/o/svc:latest", "ghcr.io/o/svc:latest", true},
		{"  name: not-an-image", "", false},
		{"  # image: commented out", "", false},
	} {
		ref, ok := ImageRefInLine(tc.line)
		if ok != tc.ok || ref != tc.ref {
			t.Errorf("ImageRefInLine(%q) = %q, %v; want %q, %v", tc.line, ref, ok, tc.ref, tc.ok)
		}
	}
}
