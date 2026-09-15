package runtime

// The runtimes that ship with homelabctl. Adding a language means adding a
// directory under templates/ and an entry here - no other code changes.
func init() {
	Register(embedded{
		name: "go-service", dir: "go-service", kind: KindService, hardened: true,
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
		name: "go-cli", dir: "go-cli", kind: KindCLI, hardened: false,
		files: map[string]string{
			"go.mod.tmpl":       "go.mod",
			"main.go.tmpl":      "main.go",
			"update.go.tmpl":    "update.go",
			"main_test.go.tmpl": "main_test.go",
			"VERSION.tmpl":      "VERSION",
			"gitignore":         ".gitignore",
		},
	})

	Register(embedded{
		name: "node-service", dir: "node-service", kind: KindService, hardened: true,
		files: map[string]string{
			"package.json.tmpl": "package.json",
			"server.js.tmpl":    "server.js",
			"gitignore":         ".gitignore",
			"dockerignore":      ".dockerignore",
		},
	})
}
