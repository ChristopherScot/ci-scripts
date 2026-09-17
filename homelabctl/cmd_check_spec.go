package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
		Paths map[string]map[string]struct {
			OperationID string                    `yaml:"operationId"`
			Summary     string                    `yaml:"summary"`
			Responses   map[string]map[string]any `yaml:"responses"`
		} `yaml:"paths"`
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
	for route, methods := range spec.Paths {
		for method, op := range methods {
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

// clientVersionConst matches the generated constant in client.go.
var clientVersionConst = regexp.MustCompile(`ClientVersion\s*=\s*"([^"]*)"`)

func checkClientVersion(dir, specVersion string, add func(string, ...any)) {
	if specVersion == "" {
		add("openapi.yml has no info.version - the client has no version to report")
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "api", "client.go"))
	if err != nil {
		return // no generated client in this service
	}
	m := clientVersionConst.FindSubmatch(b)
	if m == nil {
		return
	}
	if got := string(m[1]); got != specVersion {
		add("client.go reports ClientVersion %q but openapi.yml says %q - bump both, or X-Client-Version lies", got, specVersion)
	}
}
