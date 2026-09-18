package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// checkSpec reports problems in an OpenAPI spec that generate cleanly but
// produce a worse service.
//
// It deliberately does not re-validate the spec's structure: ogen already
// refuses to generate from a broken one, with a better error than this
// could produce. These are the things that generate FINE and still cost
// you something.
// operation is the part of an OpenAPI operation this check reads.
type operation struct {
	OperationID string                    `yaml:"operationId"`
	Summary     string                    `yaml:"summary"`
	Responses   map[string]map[string]any `yaml:"responses"`
}

// httpMethods are the keys in a path item that describe an operation.
// The others - parameters, summary, description, servers, $ref - are
// part of the path, not of any one method.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

func checkSpec(dir string, add func(string, ...any)) {
	path := filepath.Join(dir, "openapi.yml")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return // not a spec-first service
	}
	if err != nil {
		add("reading %s: %v", path, err)
		return
	}

	var spec struct {
		Info struct {
			Version string `yaml:"version"`
		} `yaml:"info"`
		// map[string]yaml.Node, not map[string]operation: a path item
		// holds more than operations. OpenAPI allows `parameters`,
		// `summary`, `description` and `$ref` beside the methods, and
		// `parameters` is a LIST - decoding it as an operation struct
		// fails the whole file with "cannot unmarshal !!seq", which is
		// what a shared path parameter used to do here.
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(b, &spec); err != nil {
		add("%s is not valid YAML: %v", path, err)
		return
	}

	// The client reports info.version in X-Client-Version. If the spec is
	// bumped and the constant is not, that header lies - and it lies
	// silently, which is worse than not sending it.
	checkClientVersion(dir, spec.Info.Version, add)

	seen := map[string]string{}
	for route, item := range spec.Paths {
		for method, node := range item {
			// Only the HTTP methods are operations. Everything else in a
			// path item is legitimate and not ours to check.
			if !httpMethods[method] {
				continue
			}
			var op operation
			if err := node.Decode(&op); err != nil {
				add("%s %s: %v", strings.ToUpper(method), route, err)
				continue
			}
			where := fmt.Sprintf("%s %s", strings.ToUpper(method), route)

			// Without an operationId ogen invents a name from the path,
			// so renaming a route silently renames the Go method and the
			// metric label that tracks it.
			switch {
			case op.OperationID == "":
				add("%s: no operationId - the generated method name and its metric label will change if the path does", where)
			default:
				if prev, dup := seen[op.OperationID]; dup {
					add("%s: operationId %q is already used by %s - generation will collide", where, op.OperationID, prev)
				}
				seen[op.OperationID] = where
			}

			// A `default` response is what lets ogen generate convenient
			// errors; without one every handler error becomes an empty
			// 500 that the spec does not describe.
			if _, ok := op.Responses["default"]; !ok {
				add("%s: no `default` response - handler errors will render as an undocumented empty 500", where)
			}

			// The summary becomes the doc comment on the generated
			// interface method, which is where someone implementing it
			// looks first.
			if op.Summary == "" {
				add("%s: no summary - the generated interface method will have no documentation", where)
			}
		}
	}
}

// checkClientVersion reports clients whose version has fallen behind the
// spec's. It asks regen what is stale rather than re-deriving it: regen
// is what fixes this, and two implementations of "which files carry the
// version" would drift exactly the way the versions themselves do.
func checkClientVersion(dir, specVersion string, add func(string, ...any)) {
	if specVersion == "" {
		add("openapi.yml has no info.version - the client has no version to report")
		return
	}
	stale, err := syncVersions(dir, specVersion, true)
	if err != nil {
		add("checking client versions: %v", err)
		return
	}
	for _, f := range stale {
		add("%s does not report version %q - run `homelabctl regen`; otherwise X-Client-Version lies and consumers install a version the API does not claim",
			f, specVersion)
	}
}
