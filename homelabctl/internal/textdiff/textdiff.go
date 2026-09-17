// Package textdiff computes line diffs with surrounding context.
//
// It knows nothing about YAML, manifests or Kubernetes: callers normalise
// their input and decide how to render the result. Split out of the diff
// command so it can be tested directly - the command package cannot be
// imported, and this is the one piece of real algorithm in the tool.
package textdiff

// Hunks diffs two line sequences and returns runs of change with
// surrounding context, each line prefixed by ' ', '-' or '+'.
//
// A presence-based comparison is not enough: a line can appear in both
// versions at different COUNTS - the common case being a value rendered
// twice - and a diff that reports a file as changed while printing
// nothing is worse than printing no diff at all. So this does a real
// longest-common-subsequence walk, which gets duplicates right.
func Hunks(old, new []string) [][]string {
	return hunks(lcsOps(old, new))
}

type op struct {
	kind byte // ' ' context, '-' removed, '+' added
	text string
}

// lcsOps walks the two line sequences into an edit script.
func lcsOps(a, b []string) []op {
	// table[i][j] = length of the LCS of a[i:] and b[j:].
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			table[i][j] = max(table[i+1][j], table[i][j+1])
		}
	}

	var ops []op
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			ops = append(ops, op{' ', a[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			ops = append(ops, op{'-', a[i]})
			i++
		default:
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < len(a); i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < len(b); j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}

// contextLines is how much unchanged YAML to show around a change. Enough
// to see which block a line belongs to - a bare `value: "3000"` means
// nothing without the key above it.
const contextLines = 3

// hunks groups the edit script into runs of change plus surrounding
// context, so a one-line change in a 90-line manifest prints as a few
// lines rather than the whole file.
func hunks(ops []op) [][]string {
	keep := make([]bool, len(ops))
	for i, o := range ops {
		if o.kind == ' ' {
			continue
		}
		for j := max(0, i-contextLines); j < min(len(ops), i+contextLines+1); j++ {
			keep[j] = true
		}
	}

	var out [][]string
	var cur []string
	for i, o := range ops {
		if !keep[i] {
			if cur != nil {
				out, cur = append(out, cur), nil
			}
			continue
		}
		cur = append(cur, string(o.kind)+" "+o.text)
	}
	if cur != nil {
		out = append(out, cur)
	}
	return out
}
