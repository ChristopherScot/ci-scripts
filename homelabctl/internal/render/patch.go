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
//
// The merge happens on the yaml.Node tree rather than on map[string]any.
// Decoding to a map throws away key order, indentation and comments, so
// re-marshalling rewrites the whole document alphabetically at a
// different indent: a one-line annotation patch would produce a
// hundred-line diff, and every patched service would have unreadable
// history. Patching the node tree touches only the keys the patch names.
func applyPatches(body string, patches map[string]string, imageRef string) (string, error) {
	if len(patches) == 0 {
		return body, nil
	}

	docs := strings.Split(body, "\n---\n")
	for i, doc := range docs {
		if strings.TrimSpace(doc) == "" {
			continue
		}
		var root yaml.Node
		if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
			return "", fmt.Errorf("parse generated document %d: %w", i+1, err)
		}
		if len(root.Content) == 0 {
			continue
		}
		patch, ok := patches[documentKind(&root)]
		if !ok {
			continue
		}

		var overlay yaml.Node
		text := strings.ReplaceAll(patch, ImagePlaceholder, imageRef)
		if err := yaml.Unmarshal([]byte(text), &overlay); err != nil {
			return "", fmt.Errorf("parse patch for kind %s: %w", documentKind(&root), err)
		}
		if len(overlay.Content) == 0 {
			continue
		}
		// What the container had before the patch, so a field the merge
		// drops can be named rather than discovered in the cluster.
		before := containerFields(root.Content[0])
		mergeNodes(root.Content[0], overlay.Content[0])
		if err := checkNothingLost(documentKind(&root), before, containerFields(root.Content[0])); err != nil {
			return "", err
		}

		var b strings.Builder
		enc := yaml.NewEncoder(&b)
		// Match the generated manifests, which are written by hand at two
		// spaces. The default of four would reindent every patched file.
		enc.SetIndent(2)
		if err := enc.Encode(root.Content[0]); err != nil {
			return "", fmt.Errorf("re-marshal %s: %w", documentKind(&root), err)
		}
		enc.Close()
		docs[i] = strings.TrimRight(b.String(), "\n")
	}
	// Re-joining trims the trailing newline off the last document; without
	// restoring it every patched file ends without one, which shows up as
	// a spurious "\ No newline at end of file" in every review.
	out := strings.Join(docs, "\n---\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

// containerFields is the set of keys on the first container of a pod
// template, or nil for a document that has none.
//
// Only the first: patches address containers[0], because that is the
// only one these manifests generate.
func containerFields(doc *yaml.Node) map[string]bool {
	// Deployment and CronJob bury the pod template at different depths.
	for _, path := range [][]string{
		{"spec", "template", "spec", "containers"},
		{"spec", "jobTemplate", "spec", "template", "spec", "containers"},
	} {
		node := doc
		for _, key := range path {
			if node = mappingNode(node, key); node == nil {
				break
			}
		}
		if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
			continue
		}
		first := node.Content[0]
		if first.Kind != yaml.MappingNode {
			continue
		}
		out := map[string]bool{}
		for i := 0; i+1 < len(first.Content); i += 2 {
			out[first.Content[i].Value] = true
		}
		return out
	}
	return nil
}

// checkNothingLost refuses a patch that removed a field from the
// generated container.
//
// A patched list REPLACES rather than merges - see mergeNodes, where
// that is deliberate - and `containers` is a list. So a patch that
// means "add one env var" and writes the container entry to say where
// to add it silently deletes everything else on it: image, ports,
// resources, probes, securityContext.
//
// That is not hypothetical. It happened: a patch adding DATABASE_URL
// to pokedex stripped the image, and Argo refused every sync for 25
// minutes with "spec.template.spec.containers[0].image: Required
// value" - a message that names the symptom, arrives after the merge
// request is already merged, and says nothing about the patch that
// caused it. Failing here costs a second and names the field.
func checkNothingLost(kind string, before, after map[string]bool) error {
	if before == nil || after == nil {
		return nil
	}
	var lost []string
	for field := range before {
		if !after[field] {
			lost = append(lost, field)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	sort.Strings(lost)
	return fmt.Errorf(
		"the %s patch removed %s from the container.\n"+
			"  A patched list replaces the generated one rather than merging into it,\n"+
			"  so a patch naming `containers` has to restate every field it wants kept.\n"+
			"  For an environment variable, use `env:` in config.yaml instead - it merges.",
		kind, strings.Join(lost, ", "))
}

// documentKind reads the `kind` field of a parsed document.
func documentKind(root *yaml.Node) string {
	if len(root.Content) == 0 {
		return ""
	}
	return mappingValue(root.Content[0], "kind")
}

func mappingValue(m *yaml.Node, key string) string {
	if m.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1].Value
		}
	}
	return ""
}

// mergeNodes overlays src onto dst in place. Mappings merge key by key,
// appending keys the base does not have; every other kind replaces,
// including sequences - a list merge would need a merge key per field and
// would surprise more often than it helped. Replacing a list is visible in
// the patch; a half-merged list is not.
func mergeNodes(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		*dst = *src
		return
	}
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, val := src.Content[i], src.Content[i+1]
		if existing := mappingNode(dst, key.Value); existing != nil {
			mergeNodes(existing, val)
			continue
		}
		dst.Content = append(dst.Content, key, val)
	}
}

// mappingNode returns the value node for key, or nil.
func mappingNode(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// PatchedKinds lists the resource kinds a rendered service contains, so an
// unknown patch key can be reported with the valid options rather than a
// bare rejection.
func PatchedKinds(outs []Output) []string {
	seen := map[string]bool{}
	for _, o := range outs {
		for _, k := range documentKinds(o.Body) {
			seen[k] = true
		}
	}
	kinds := make([]string, 0, len(seen))
	for k := range seen {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// documentKinds lists the `kind:` of every YAML document in body, in the
// order they appear. A file holds more than one when a resource is split
// - ingress.yaml carries two Ingresses when the hosts cannot share a
// certificate - so a caller asking "what is in this file" has to see all
// of them.
//
// A document that does not parse is skipped rather than reported: this
// answers a question about content, and the callers that must reject bad
// YAML do so where it is generated or patched.
func documentKinds(body string) []string {
	var kinds []string
	for _, doc := range strings.Split(body, "\n---\n") {
		var node map[string]any
		if yaml.Unmarshal([]byte(doc), &node) != nil {
			continue
		}
		if k, ok := node["kind"].(string); ok && k != "" {
			kinds = append(kinds, k)
		}
	}
	return kinds
}
