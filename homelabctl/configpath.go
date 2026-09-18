package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// configName is the file every command works from.
const configName = "config.yaml"

// findConfig locates the service's config.yaml by walking up from the
// working directory, the way git finds .git.
//
// Commands used to read "config.yaml" relative to the working
// directory, so `homelabctl render` from api/ or deploy/ failed with
// "open config.yaml: no such file or directory" - a message about a
// file that exists two directories up. A tool that operates on a repo
// should work anywhere inside it.
//
// It stops at a filesystem boundary rather than walking to /: reaching
// the root would find someone else's config.yaml in a home directory
// and act on the wrong service.
func findConfig() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	start := dir

	for {
		candidate := filepath.Join(dir, configName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		// A repository boundary is as far up as a service can be. Going
		// past it would pick up an unrelated config.yaml.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("no %s in %s or any parent up to the repository root.\n"+
		"cd into a service directory - the one holding its %s - and run this there",
		configName, start, configName)
}

// repoRoot is the directory holding .git, walking up from the working
// directory. Empty when there is none.
//
// Used to tell "I am inside the repo already" from "I need to descend
// into it", which --parent-repo previously answered by comparing the
// working directory's BASENAME to the repo name. That is only right in
// the repo root: from services/alpha the basename is alpha, so init
// descended anyway and produced services/alpha/<repo>/services/<name>.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
