package render

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Patches let a service adjust generated manifests without taking
// ownership of them.
//
// The previous mechanism replaced a whole file. That is the way a platform
// tool rots: adding one annotation meant pasting the entire generated
// Deployment into config.yaml and maintaining a fork of it forever, so
// every later convention change - a new PSA level, a probe timing fix,
// a securityContext tightening - silently skipped that service. The
// failure is invisible and permanent.
//
// A patch is a strategic merge: supply only what differs, keyed by
// resource kind rather than by filename. The base keeps flowing, and
// render's file layout stays an implementation detail rather than API.

// applyPatches merges each patch into the matching document. Keyed by
// resource kind (Deployment, CronJob, Service, Ingress, Namespace...),
// because kind is the stable thing - a filename is render's business.
func applyPatches(body string, patches map[string]string, imageRef string) (string, error) {
	if len(patches) == 0 {
		return body, nil
	}

	docs := strings.Split(body, "\n---\n")
	for i, doc := range docs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var node map[string]any
		if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
			return "", fmt.Errorf("parse generated document %d: %w", i+1, err)
		}
		kind, _ := node["kind"].(string)
		patch, ok := patches[kind]
		if !ok {
			continue
		}

		var overlay map[string]any
		if err := yaml.Unmarshal([]byte(strings.ReplaceAll(patch, ImagePlaceholder, imageRef)), &overlay); err != nil {
			return "", fmt.Errorf("parse patch for kind %s: %w", kind, err)
		}
		merged := mergeMaps(node, overlay)

		b, err := yaml.Marshal(merged)
		if err != nil {
			return "", fmt.Errorf("re-marshal %s: %w", kind, err)
		}
		docs[i] = strings.TrimRight(string(b), "\n")
	}
	return strings.Join(docs, "\n---\n"), nil
}

// mergeMaps overlays src onto dst recursively. Maps merge; every other
// type replaces, including lists - a list merge would need a merge key per
// field and would surprise more often than it helped. Replacing a list is
// visible in the patch; a half-merged list is not.
func mergeMaps(dst, src map[string]any) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := out[k].(map[string]any); ok {
				out[k] = mergeMaps(existing, sub)
				continue
			}
		}
		out[k] = v
	}
	return out
}

// PatchedKinds lists the resource kinds a rendered service contains, so an
// unknown patch key can be reported with the valid options rather than a
// bare rejection.
func PatchedKinds(outs []Output) []string {
	seen := map[string]bool{}
	for _, o := range outs {
		for _, doc := range strings.Split(o.Body, "\n---\n") {
			var node map[string]any
			if yaml.Unmarshal([]byte(doc), &node) != nil {
				continue
			}
			if k, ok := node["kind"].(string); ok && k != "" {
				seen[k] = true
			}
		}
	}
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}
