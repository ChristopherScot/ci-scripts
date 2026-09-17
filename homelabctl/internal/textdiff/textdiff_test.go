package textdiff

import (
	"strings"
	"testing"
)

func lines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// flatten joins every hunk so a test can assert on the whole diff.
func flatten(hs [][]string) string {
	var all []string
	for _, h := range hs {
		all = append(all, h...)
	}
	return strings.Join(all, "\n")
}

func TestIdenticalInputProducesNoHunks(t *testing.T) {
	in := lines("a\nb\nc")
	if got := Hunks(in, in); len(got) != 0 {
		t.Errorf("Hunks(x, x) = %v, want no hunks", got)
	}
}

// The bug this package was extracted from: a presence-based comparison
// reported a file as changed and then printed nothing, because the only
// difference was how many TIMES a line appeared.
func TestDuplicateLineIsReported(t *testing.T) {
	old := lines("env:\n  - name: PORT\n  - name: HOST")
	nw := lines("env:\n  - name: PORT\n  - name: PORT\n  - name: HOST")

	got := flatten(Hunks(old, nw))
	if got == "" {
		t.Fatal("Hunks reported no difference between 1 and 2 copies of a line")
	}
	if n := strings.Count(got, "+   - name: PORT"); n != 1 {
		t.Errorf("want exactly one added PORT line, got %d:\n%s", n, got)
	}
}

func TestAdditionAndRemoval(t *testing.T) {
	got := flatten(Hunks(lines("a\nb\nc"), lines("a\nX\nc")))
	if !strings.Contains(got, "- b") {
		t.Errorf("removal not reported:\n%s", got)
	}
	if !strings.Contains(got, "+ X") {
		t.Errorf("addition not reported:\n%s", got)
	}
	if !strings.Contains(got, "  a") {
		t.Errorf("context not included:\n%s", got)
	}
}

func TestEmptySides(t *testing.T) {
	if got := flatten(Hunks(nil, lines("a\nb"))); !strings.Contains(got, "+ a") {
		t.Errorf("new file not reported as all additions:\n%s", got)
	}
	if got := flatten(Hunks(lines("a\nb"), nil)); !strings.Contains(got, "- a") {
		t.Errorf("deleted file not reported as all removals:\n%s", got)
	}
}

// A change in a large file must print a few lines, not the whole file.
func TestDistantChangesDoNotDragInTheWholeFile(t *testing.T) {
	var a, b []string
	for i := 0; i < 100; i++ {
		a = append(a, "line")
		b = append(b, "line")
	}
	b[50] = "changed"

	hs := Hunks(a, b)
	var total int
	for _, h := range hs {
		total += len(h)
	}
	// One change, contextLines either side, plus the -/+ pair.
	if total > 2*contextLines+2 {
		t.Errorf("one change produced %d lines, want <= %d", total, 2*contextLines+2)
	}
}

// Two changes far apart belong to separate hunks, not one run spanning
// everything between them.
func TestSeparateChangesGiveSeparateHunks(t *testing.T) {
	var a []string
	for i := 0; i < 60; i++ {
		a = append(a, "line")
	}
	b := append([]string(nil), a...)
	b[5], b[50] = "first", "second"

	if hs := Hunks(a, b); len(hs) != 2 {
		t.Errorf("got %d hunks for two distant changes, want 2", len(hs))
	}
}
