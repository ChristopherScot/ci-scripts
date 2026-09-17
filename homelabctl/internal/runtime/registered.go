package runtime

// The runtimes that ship with homelabctl. Adding a language means adding a
// directory under templates/ and an entry here - no other code changes.
func init() {
	Register(embedded{
		name: "go-service", dir: "go-service", deployable: true, hardened: true,
		// Generate from the spec FIRST: api/ does not exist until ogen
		// runs, so `go get` and the build would both fail on a missing
		// import. `go get -u` then upgrades the transitive graph, which
		// tidy alone resolves to the minimums each dependency declares -
		// older than what is released. Without tidy there is no go.sum
		// and the service does not build at all.
		resolve: [][]string{
			{"go", "generate", "./..."},
			{"go", "get", "-u", "./..."},
			{"go", "mod", "tidy"},
			// The TypeScript client's types come from the same spec.
			// Pinned to openapi-typescript 7 because it requires
			// TypeScript ^5 and breaks on 7; running it through npx
			// keeps that constraint out of the service's own
			// dependencies, which stay current.
			{"npx", "--yes", "openapi-typescript@7", "openapi.yml", "-o", "clients/ts/schema.d.ts"},
		},
		files: map[string]string{
			"go.mod.tmpl":         "go.mod",
			"main.go.tmpl":        "main.go",
			"main_test.go.tmpl":   "main_test.go",
			"openapi.yml.tmpl":    "openapi.yml",
			"generate.go.tmpl":    "generate.go",
			"client.go.tmpl":      "api/client.go",
			"client_test.go.tmpl": "api/client_test.go",
			"paging.go.tmpl":      "api/paging.go",
			"paging_test.go.tmpl": "api/paging_test.go",

			// The TypeScript client. package.json sits at the repo root
			// because npm looks for it there when installing from git -
			// `files` keeps the Go source out of the published tarball.
			"clients_ts_package.json.tmpl": "package.json",
			"clients_ts_index.js.tmpl":     "clients/ts/index.js",
			"clients_ts_index.d.ts.tmpl":   "clients/ts/index.d.ts",
			"gitignore":                    ".gitignore",
			"dockerignore":                 ".dockerignore",
		},
	})

	// A CLI is not deployed: no Dockerfile, no manifests, no Argo app. It
	// cross-compiles and publishes release assets, and ships the same
	// self-update command homelabctl uses.
	Register(embedded{
		name: "go-cli", dir: "go-cli", deployable: false, hardened: false,
		resolve: [][]string{
			{"go", "get", "-u", "./..."},
			{"go", "mod", "tidy"},
		},
		files: map[string]string{
			"go.mod.tmpl":        "go.mod",
			"main.go.tmpl":       "main.go",
			"update.go.tmpl":     "update.go",
			"main_test.go.tmpl":  "main_test.go",
			"completion.go.tmpl": "completion.go",
			"VERSION.tmpl":       "VERSION",
			"gitignore":          ".gitignore",
		},
	})

	Register(embedded{
		name: "node-service", dir: "node-service", deployable: true, hardened: true,
		// Generates package-lock.json, which the Dockerfile's `npm ci`
		// requires and which is not otherwise created.
		// npm resolves ^ ranges to the newest matching release already.
		resolve: [][]string{{"npm", "install", "--package-lock-only"}},
		files: map[string]string{
			"package.json.tmpl":   "package.json",
			"server.js.tmpl":      "server.js",
			"server.test.js.tmpl": "server.test.js",
			"gitignore":           ".gitignore",
			"dockerignore":        ".dockerignore",
		},
	})
}
