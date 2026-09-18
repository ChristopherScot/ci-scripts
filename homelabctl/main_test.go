package main

import (
	"testing"
)

func TestVersionIsSet(t *testing.T) {
	if Version == "" {
		t.Error("Version is empty; it should default to \"dev\" and be set via ldflags at release")
	}
}

// The root wires every subcommand; if one is dropped the CLI still compiles
// and the command silently disappears.
func TestRootHasExpectedCommands(t *testing.T) {
	want := map[string]bool{
		"init": false, "render": false, "check": false,
		"update": false, "version": false, "completion": false, "vault": false,
	}
	for _, c := range rootCmd().Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("root command is missing %q", name)
		}
	}
}

// Cobra builds its completion command during Execute, so `install` has to
// be attached after forcing it into existence. If that ordering regresses
// the subcommand silently disappears.
func TestCompletionInstallIsWired(t *testing.T) {
	for _, c := range rootCmd().Commands() {
		if c.Name() != "completion" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "install" {
				if sub.Flags().Lookup("file") == nil {
					t.Error("completion install has no --file flag")
				}
				return
			}
		}
		t.Fatal("completion has no install subcommand")
	}
	t.Fatal("no completion command on root")
}

func TestIsNewer(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
		why             string
	}{
		{"v1.2.0", "v1.1.0", true, "ordinary bump"},
		{"v1.1.0", "v1.2.0", false, "older is not newer"},
		{"v1.1.0", "v1.1.0", false, "equal is not newer"},
		{"v1.1.1", "v1.1", true, "an omitted patch reads as .0"},

		// The bug that motivated using x/mod/semver. Comparing dotted
		// components pairwise made 10 sort below 2, so once any
		// component reached double digits update went quiet: it
		// reported "already up to date" forever.
		{"v1.10.0", "v1.2.0", true, "10 is newer than 2, not older"},
		{"v1.2.0", "v1.10.0", false, "and the reverse still holds"},
		{"v2.0.0", "v1.99.99", true, "major wins over any minor"},

		// A prerelease sorts before its release, so someone on a release
		// is never offered an rc, and someone on an rc is offered the
		// release.
		{"v1.0.0", "v1.0.0-rc1", true, "release supersedes its rc"},
		{"v1.0.0-rc1", "v1.0.0", false, "an rc does not supersede the release"},

		// A version that does not parse yields false rather than a
		// meaningless comparison: leaving someone on a working binary
		// beats talking them into replacing it.
		{"not-a-version", "v1.0.0", false, "unparseable latest"},
		{"v1.0.0", "garbage", false, "unparseable current"},
		{"dev", "v1.0.0", false, "a dev build is not a version"},
	} {
		if got := isNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v (%s)",
				tc.latest, tc.current, got, tc.want, tc.why)
		}
	}
}

// The caller hands isNewer whatever the tag and the ldflags say, which
// may or may not carry a v. Both spellings must reach semver the same
// way, because semver.IsValid rejects the unprefixed one - and an
// invalid version means "not newer", i.e. update would go silent.
func TestEnsureVAcceptsEitherSpelling(t *testing.T) {
	for _, in := range []string{"1.2.3", "v1.2.3"} {
		if got := ensureV(in); got != "v1.2.3" {
			t.Errorf("ensureV(%q) = %q, want %q", in, got, "v1.2.3")
		}
	}
	if !isNewer(ensureV("1.10.0"), ensureV("v1.2.0")) {
		t.Error("a mixed-spelling comparison did not reach semver intact")
	}
}

// The guard moved to `check`, which reads committed manifests. render
// no longer takes an image ref at all: argocd-image-updater owns the
// running version, so a rendered manifest always names :latest.
