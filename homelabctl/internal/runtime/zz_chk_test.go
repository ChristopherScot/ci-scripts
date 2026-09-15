package runtime

import "testing"

func TestFileOrderStable(t *testing.T) {
	r, _ := Get("go-cli")
	var first []string
	for i := 0; i < 20; i++ {
		a := r.Artifacts(testParams())
		var paths []string
		for _, f := range a.Files {
			paths = append(paths, f.Path)
		}
		if i == 0 {
			first = paths
			continue
		}
		for j := range paths {
			if paths[j] != first[j] {
				t.Logf("order differs on run %d: %v vs %v", i, paths, first)
				return
			}
		}
	}
	t.Log("order was stable across 20 runs (unexpected for map range)")
}
