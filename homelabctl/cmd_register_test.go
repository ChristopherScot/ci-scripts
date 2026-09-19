package main

import (
	"os"
	"strings"
	"testing"
)

// render must not carry a --register flag any more.
//
// It made 30% of runRender a block that wrote no files and made one
// network call to a different repository, and it produced flag
// combinations that could not work:
//
//   - `--dry-run --register` was ACCEPTED and silently did nothing,
//     because --dry-run returns before render.All is called. A
//     rehearsal of registration that rehearsed nothing and exited 0.
//   - `--force` gated registration on preflight, which inspects the
//     live Deployment - unrelated to registering.
//   - `--out` wrote manifests elsewhere while the PR still carried the
//     git-derived path.
//
// Separate commands delete all three rather than documenting them, so
// the flag must stay gone.
func TestRenderHasNoRegisterFlag(t *testing.T) {
	if f := renderCmd().Flags().Lookup("register"); f != nil {
		t.Error("render has --register again; --dry-run --register silently does nothing")
	}
	if _, _, err := rootCmd().Find([]string{"register"}); err != nil {
		t.Errorf("no register command: %v", err)
	}
}

// The no-remote guard reads the typed field, before rendering.
//
// It used to be strings.Contains(entry, `"repoURL"`) against the
// rendered JSON - reverse-engineering from serialized text a fact that
// Source carries two calls earlier. A service whose manifests: held a
// file containing the literal text "repoURL" would have matched the
// wrong document and registered with an empty repoURL, which the
// ApplicationSet interpolates straight into a live Application that
// then fetches nothing.
func TestRegisterRefusesWithoutARemote(t *testing.T) {
	// Comments stripped before matching: the fix for this leaves behind
	// an explanation that names the very call it removed, and a plain
	// substring search finds the explanation and calls it the bug. The
	// duration_ms regression test failed on its own fix this way.
	body := codeOnly(readFileForTest(t, "cmd_register.go"))
	if strings.Contains(body, `strings.Contains(entry`) {
		t.Error("the repoURL guard is matching rendered JSON again")
	}
	if !strings.Contains(body, `src.RepoURL == ""`) {
		t.Error("the guard should read Source.RepoURL, the typed field")
	}
	// Before rendering, so an unregisterable service fails immediately
	// rather than after work it discards.
	guard := strings.Index(body, `src.RepoURL == ""`)
	render := strings.Index(body, "render.AppEntry(")
	if guard < 0 || render < 0 || guard > render {
		t.Error("the guard runs after rendering; it should run before")
	}
}

// readFileForTest reads a source file from the package directory.
func readFileForTest(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// codeOnly drops line comments, so a search for a removed construct
// cannot match the comment that explains its removal.
func codeOnly(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		code, _, _ := strings.Cut(line, "//")
		b.WriteString(code)
		b.WriteByte('\n')
	}
	return b.String()
}
