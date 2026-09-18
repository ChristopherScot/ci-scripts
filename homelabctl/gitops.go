package main

// Registering a service with Argo.
//
// A service's manifests live in its own repo and Argo reads them there,
// but the ApplicationSet's generator reads argocd.json from the GitOps
// repo - a generator has exactly one repoURL. So one small file has to
// land there, and copying it by hand was the step people forget: the
// service is built, pushed and green, and nothing is deployed because
// nobody remembered the copy.
//
// This opens a pull request rather than committing. Adding a service to
// the cluster is a cluster change, and it gets reviewed like one - the
// same reason app-of-apps syncs with selfHeal but not prune.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ChristopherScot/ci-scripts/homelabctl/internal/render"
)

// gitopsRepoEnv names the GitOps repo, matching what diff already uses
// for the local checkout.
const gitopsRepoEnv = "HOMELAB_REPO"

// gitopsSlug is the owner/repo the ApplicationSet generator reads.
//
// Derived from $HOMELAB_REPO's checkout so there is one place to say
// where the GitOps repo is, rather than a second setting that can
// disagree with the first.
func gitopsSlug() string {
	dir := strings.TrimSpace(os.Getenv(gitopsRepoEnv))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = home + "/homelab"
	}
	out, err := exec.Command("git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), ".git")
	if rest, ok := strings.CutPrefix(url, "git@github.com:"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(url, "https://github.com/"); ok {
		return rest
	}
	return ""
}

// openGitOpsPR proposes registering this service with Argo: one
// argocd.json at <name>/, which the generator's */argocd.json glob
// finds.
//
// Everything here is through the GitHub API rather than a local
// checkout, so it works whether or not the GitOps repo is cloned, and
// leaves no branch behind on this machine.
func openGitOpsPR(name, entry string) (string, error) {
	slug := gitopsSlug()
	if slug == "" {
		return "", fmt.Errorf("could not find the GitOps repo; set %s to a checkout of it", gitopsRepoEnv)
	}
	path := name + "/" + render.AppEntryFile
	branch := "register-" + name

	// Already registered is the ordinary case for a re-run. Returning
	// early rather than opening an empty PR.
	if exec.Command("gh", "api", "repos/"+slug+"/contents/"+path).Run() == nil {
		return "", nil
	}

	head, err := exec.Command("gh", "api", "repos/"+slug+"/git/ref/heads/main", "--jq", ".object.sha").Output()
	if err != nil {
		return "", fmt.Errorf("reading %s main: %w", slug, err)
	}
	sha := strings.TrimSpace(string(head))

	// A branch that already exists means a previous run got this far and
	// the PR is presumably open; reuse it rather than failing.
	ref, _ := json.Marshal(map[string]string{"ref": "refs/heads/" + branch, "sha": sha})
	create := exec.Command("gh", "api", "--method", "POST", "repos/"+slug+"/git/refs", "--input", "-")
	create.Stdin = strings.NewReader(string(ref))
	_ = create.Run()

	body, _ := json.Marshal(map[string]string{
		"message": "argo: register " + name,
		"content": base64.StdEncoding.EncodeToString([]byte(entry)),
		"branch":  branch,
	})
	put := exec.Command("gh", "api", "--method", "PUT", "repos/"+slug+"/contents/"+path, "--input", "-")
	put.Stdin = strings.NewReader(string(body))
	put.Stderr = os.Stderr
	if err := put.Run(); err != nil {
		return "", fmt.Errorf("writing %s to %s: %w", path, slug, err)
	}

	pr, err := exec.Command("gh", "pr", "create",
		"--repo", slug, "--head", branch, "--base", "main",
		"--title", "argo: register "+name,
		"--body", "Adds `"+path+"` so the homelabctl-services ApplicationSet "+
			"generates an Application for `"+name+"`.\n\nIts manifests stay in "+
			"the service's own repo; this file only tells Argo where to find them.",
	).Output()
	if err != nil {
		// The PR may already exist from an earlier run, which is not a
		// failure - the file is on the branch either way.
		return "", nil
	}
	return strings.TrimSpace(string(pr)), nil
}
