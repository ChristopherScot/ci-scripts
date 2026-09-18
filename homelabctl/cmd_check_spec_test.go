package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func problemsFor(t *testing.T, spec string) []string {
	t.Helper()
	dir := t.TempDir()
	if spec != "" {
		if err := os.WriteFile(filepath.Join(dir, "openapi.yml"), []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	checkSpec(dir, func(f string, a ...any) {
		got = append(got, strings.ToLower(fmt.Sprintf(f, a...)))
	})
	return got
}

func hasProblem(got []string, want string) bool {
	for _, g := range got {
		if strings.Contains(g, want) {
			return true
		}
	}
	return false
}

// A service that is not spec-first must not be reported as broken.
func TestCheckSpecIgnoresMissingSpec(t *testing.T) {
	if got := problemsFor(t, ""); len(got) != 0 {
		t.Errorf("reported %v for a service with no spec", got)
	}
}

func TestCheckSpecAcceptsAWellFormedSpec(t *testing.T) {
	got := problemsFor(t, `
openapi: 3.0.3
info: { title: ok, version: 0.1.0 }
paths:
  /thing:
    get:
      operationId: getThing
      summary: Fetch the thing.
      responses:
        '200': { description: OK }
        default: { description: Error }
`)
	if len(got) != 0 {
		t.Errorf("reported %v for a well-formed spec", got)
	}
}

func TestCheckSpecFindsProblems(t *testing.T) {
	got := problemsFor(t, `
openapi: 3.0.3
info: { title: bad, version: 0.1.0 }
paths:
  /a:
    get:
      responses:
        '200': { description: OK }
  /b:
    get:
      operationId: dup
      summary: First.
      responses:
        '200': { description: OK }
        default: { description: Error }
  /c:
    get:
      operationId: dup
      summary: Second.
      responses:
        '200': { description: OK }
        default: { description: Error }
`)
	for _, want := range []string{"no operationid", "no summary", "no `default` response", "already used by"} {
		if !hasProblem(got, want) {
			t.Errorf("did not report %q; got %v", want, got)
		}
	}
}

func TestCheckSpecReportsUnparseableYAML(t *testing.T) {
	got := problemsFor(t, "paths: [this: is: not: a: mapping")
	if !hasProblem(got, "not valid yaml") {
		t.Errorf("accepted unparseable YAML; got %v", got)
	}
}

// A path item holds more than operations: OpenAPI allows `parameters`,
// `summary`, `description` and `$ref` beside the methods. `parameters`
// is a LIST, and decoding every key as an operation struct failed the
// whole file with "cannot unmarshal !!seq" - so a spec that shared a
// path parameter could not be checked at all, and CI reported the spec
// as invalid YAML when it was correct.
func TestPathLevelKeysAreNotOperations(t *testing.T) {
	got := problemsFor(t, `
openapi: 3.0.3
info: { title: t, version: 0.1.0 }
paths:
  /d/{id}:
    summary: A path whose parameters are shared by its methods.
    parameters:
      - { name: id, in: path, required: true, schema: { type: string } }
    get:
      operationId: getThing
      summary: Get it.
      responses:
        '200': { description: ok }
        default: { description: err }
`)
	for _, p := range got {
		if strings.Contains(p, "not valid YAML") || strings.Contains(p, "unmarshal") {
			t.Errorf("a shared path parameter broke parsing: %s", p)
		}
		if strings.Contains(strings.ToUpper(p), "PARAMETERS ") || strings.Contains(strings.ToUpper(p), "SUMMARY ") {
			t.Errorf("a path-level key was checked as if it were an operation: %s", p)
		}
	}
}
