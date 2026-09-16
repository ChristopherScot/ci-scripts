package main

import (
	"errors"
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
	}{
		{"1.2.0", "1.1.0", true},
		{"1.1.0", "1.2.0", false},
		{"1.1.0", "1.1.0", false},
		{"1.1.1", "1.1", true},
	} {
		if got := isNewer(tc.latest, tc.current); got != tc.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

// An abbreviated SHA is not a registry tag and yields ImagePullBackOff, so
// render must refuse one rather than generate a manifest that cannot pull.
func TestRenderRejectsAbbreviatedSHA(t *testing.T) {
	err := runRender(renderOpts{
		cfgPath:  "nonexistent.yaml",
		imageRef: "ghcr.io/o/x:abc1234",
		out:      ".",
	})
	if !errors.Is(err, errAbbreviatedSHA) {
		t.Errorf("runRender with short SHA = %v, want errAbbreviatedSHA", err)
	}
}
