package runtime

// The runtimes that ship with homelabctl. Adding a language means adding a
// directory under templates/ and an entry here - no other code changes.
func init() {
	Register(embedded{
		name: "go-service", dir: "go-service", deployable: true, hardened: true,
		// `go get -u` first: tidy alone would resolve the transitive
		// graph to the minimums client_golang declares, which are older
		// than what is released. Without either there is no go.sum and
		// the service does not build at all.
		resolve: [][]string{
			{"go", "get", "-u", "./..."},
			{"go", "mod", "tidy"},
		},
		files: map[string]string{
			"go.mod.tmpl":       "go.mod",
			"main.go.tmpl":      "main.go",
			"main_test.go.tmpl": "main_test.go",
			"gitignore":         ".gitignore",
			"dockerignore":      ".dockerignore",
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
			"package.json.tmpl": "package.json",
			"server.js.tmpl":    "server.js",
			"gitignore":         ".gitignore",
			"dockerignore":      ".dockerignore",
		},
	})
}
