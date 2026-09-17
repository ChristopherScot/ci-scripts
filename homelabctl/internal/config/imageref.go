package config

import (
	"regexp"
	"strings"
)

// An abbreviated git SHA is not a registry tag. `docker push` creates the
// tag it is given; a 7-to-12 character hex string looks like a commit but
// names nothing in the registry, so the pod gets ImagePullBackOff with no
// hint that the tag was the problem.
//
// This lives here rather than beside either caller because `render`
// rejects such a reference on the way in and `check` finds one already
// written into a manifest - the same rule at two points in the pipeline,
// and it was previously two regexes that could drift apart.
var abbreviatedSHA = regexp.MustCompile(`:[0-9a-f]{7,12}$`)

// IsAbbreviatedSHA reports whether ref is tagged with a short git SHA.
//
// A digest reference is not: `@sha256:...` names an immutable manifest
// and is exactly what argocd-image-updater writes.
func IsAbbreviatedSHA(ref string) bool {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "@sha256:") {
		return false
	}
	return abbreviatedSHA.MatchString(ref)
}

// imageLine matches the image field of a manifest, capturing the
// reference so callers can apply IsAbbreviatedSHA to it.
var imageLine = regexp.MustCompile(`^\s*image:\s*(\S+)\s*$`)

// ImageRefInLine returns the image reference a manifest line sets, and
// whether the line sets one at all.
func ImageRefInLine(line string) (string, bool) {
	m := imageLine.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}
