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
	"bytes"
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

	// What is registered now, if anything. An existing entry is not a
	// reason to stop: argocd.json gains fields - repoURL and path did -
	// and a service whose entry predates them renders an Application
	// pointing nowhere. Registering has to mean "make it current", or
	// every schema change becomes a hand-edit of every service.
	//
	// The blob sha is also what lets the update be a PUT rather than a
	// create, which the API rejects for a file that exists.
	// Two calls rather than one with a concatenating --jq: the API
	// returns base64 WITH newlines, so joining content and sha into one
	// string and splitting it back gives a truncated sha and a 409 on
	// the write.
	var existing, blobSHA string
	if out, err := exec.Command("gh", "api", "repos/"+slug+"/contents/"+path, "--jq", ".sha").Output(); err == nil {
		blobSHA = strings.TrimSpace(string(out))
	}
	// The branch may already carry a version from an earlier run, whose
	// PR is still open. The write is against the BRANCH, so its sha is
	// the one the API wants - main's is stale there, and no sha at all
	// is a 422 on a file that exists.
	if out, err := exec.Command("gh", "api",
		"repos/"+slug+"/contents/"+path+"?ref="+branch, "--jq", ".sha").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			blobSHA = s
		}
	}
	if blobSHA != "" {
		if out, err := exec.Command("gh", "api", "repos/"+slug+"/contents/"+path, "--jq", ".content").Output(); err == nil {
			if raw, err := base64.StdEncoding.DecodeString(
				strings.ReplaceAll(strings.TrimSpace(string(out)), "\n", "")); err == nil {
				existing = string(raw)
			}
		}
	}
	if existing == entry {
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

	fields := map[string]string{
		"message": "argo: register " + name,
		"content": base64.StdEncoding.EncodeToString([]byte(entry)),
		"branch":  branch,
	}
	if blobSHA != "" {
		// Replacing a file needs the sha of what is being replaced; the
		// API refuses the write without it.
		fields["message"] = "argo: update " + name
		fields["sha"] = blobSHA
	}
	body, _ := json.Marshal(fields)
	put := exec.Command("gh", "api", "--method", "PUT", "repos/"+slug+"/contents/"+path, "--input", "-")
	put.Stdin = strings.NewReader(string(body))
	put.Stderr = os.Stderr
	if err := put.Run(); err != nil {
		return "", fmt.Errorf("writing %s to %s: %w", path, slug, err)
	}

	title, prBody := "argo: register "+name, "Adds `"+path+"` so the homelabctl-services "+
		"ApplicationSet generates an Application for `"+name+"`.\n\nIts manifests stay in "+
		"the service's own repo; this file only tells Argo where to find them."
	if blobSHA != "" {
		title = "argo: update " + name
		prBody = "Brings `" + path + "` up to date with what `homelabctl render` produces.\n\n" +
			"A stale entry renders an Application with fields the template expects and the " +
			"file does not carry."
	}
	cmd := exec.Command("gh", "pr", "create",
		"--repo", slug, "--head", branch, "--base", "main",
		"--title", title, "--body", prBody,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pr, err := cmd.Output()
	if err != nil {
		// Only "it already exists" is not a failure.
		//
		// This used to return ("", nil) for ANY error, and the caller
		// prints that as "<name> is already registered with Argo" - so
		// an expired token, a network outage and a rate limit all
		// reported success. The file is pushed to the branch by then,
		// so nothing else would have noticed either.
		//
		// gh says "a pull request for branch ... already exists"; the
		// message is matched rather than the exit code because gh uses
		// 1 for everything.
		if strings.Contains(stderr.String(), "already exists") {
			return "", nil
		}
		return "", fmt.Errorf("opening the PR on %s: %w\n%s",
			slug, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(pr)), nil
}
