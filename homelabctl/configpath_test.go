package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A tool that operates on a repo should work anywhere inside it.
// Commands read "config.yaml" relative to the working directory, so
// `homelabctl render` from api/ failed with "open config.yaml: no such
// file or directory" - about a file that exists two directories up.
func TestFindConfigWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, configName), []byte("name: svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "api", "internal")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(deep); err != nil {
		t.Fatal(err)
	}

	got, err := findConfig()
	if err != nil {
		t.Fatalf("findConfig from a subdirectory: %v", err)
	}
	// Compare resolved paths: a temp dir is behind a symlink on macOS.
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(filepath.Join(root, configName))
	if gotReal != wantReal {
		t.Errorf("findConfig = %q, want %q", gotReal, wantReal)
	}
}

// The walk stops at a repository boundary. Continuing to / would find an
// unrelated config.yaml in a home directory and act on the wrong
// service - a wrong answer being worse than no answer.
func TestFindConfigStopsAtTheRepoRoot(t *testing.T) {
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, configName), []byte("name: wrong\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(outer, "repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}

	if got, err := findConfig(); err == nil {
		t.Errorf("findConfig walked past the repo root and found %q", got)
	}
}

// With nothing to find, the error says what to do rather than naming a
// file that was never going to be there.
func TestFindConfigExplainsItself(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	_, err := findConfig()
	if err == nil {
		t.Fatal("findConfig succeeded with no config anywhere")
	}
	for _, want := range []string{configName, "cd into a service directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// A monorepo holds several services, so walking up has to stop at the
// nearest one. Finding the repo root's config - or another service's -
// would act on the wrong thing while appearing to work.
func TestFindConfigPicksTheNearestServiceInAMonorepo(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"alpha", "beta"} {
		dir := filepath.Join(repo, "services", svc)
		if err := os.MkdirAll(filepath.Join(dir, "api"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, configName), []byte("name: "+svc+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wd, _ := os.Getwd()
	defer os.Chdir(wd)

	for _, tc := range []struct{ from, want string }{
		{filepath.Join("services", "alpha"), "alpha"},
		{filepath.Join("services", "alpha", "api"), "alpha"},
		{filepath.Join("services", "beta"), "beta"},
		{filepath.Join("services", "beta", "api"), "beta"},
	} {
		if err := os.Chdir(filepath.Join(repo, tc.from)); err != nil {
			t.Fatal(err)
		}
		got, err := findConfig()
		if err != nil {
			t.Errorf("from %s: %v", tc.from, err)
			continue
		}
		body, _ := os.ReadFile(got)
		if !strings.Contains(string(body), "name: "+tc.want) {
			t.Errorf("from %s: found %s, which is not %s's", tc.from, got, tc.want)
		}
	}

	// The repo root is not a service. Answering with some service's
	// config would be worse than refusing.
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	if got, err := findConfig(); err == nil {
		t.Errorf("at the repo root findConfig returned %q, but no service lives there", got)
	}
}

// --parent-repo puts a service at the monorepo's root, wherever it was
// run from. It used to ask whether the WORKING DIRECTORY's basename was
// the repo name, which is only true in the root: from services/alpha
// the answer was no, so init descended and wrote
// services/alpha/<repo>/services/<name>.
func TestRepoRootIsFoundFromAnySubdirectory(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(repo, "services", "alpha", "api")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	wd, _ := os.Getwd()
	defer os.Chdir(wd)

	want, _ := filepath.EvalSymlinks(repo)
	for _, from := range []string{repo, filepath.Join(repo, "services"), deep} {
		if err := os.Chdir(from); err != nil {
			t.Fatal(err)
		}
		got, _ := filepath.EvalSymlinks(repoRoot())
		if got != want {
			t.Errorf("from %s: repoRoot = %q, want %q", from, got, want)
		}
	}
}

// Outside a repository there is no root, and the caller falls back to
// treating --parent-repo as a directory to create.
func TestRepoRootIsEmptyOutsideARepository(t *testing.T) {
	dir := t.TempDir()
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if got := repoRoot(); got != "" {
		t.Errorf("repoRoot outside a repository = %q, want empty", got)
	}
}
