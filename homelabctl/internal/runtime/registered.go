package runtime

// The runtimes that ship with homelabctl. Adding a language means adding a
// directory under templates/ and an entry here - no other code changes.
func init() {
	Register(embedded{
		name: "go", dir: "go", hardened: true,
		files: map[string]string{
			"go.mod.tmpl":       "go.mod",
			"main.go.tmpl":      "main.go",
			"main_test.go.tmpl": "main_test.go",
			"gitignore":         ".gitignore",
			"dockerignore":      ".dockerignore",
		},
	})

	Register(embedded{
		name: "node", dir: "node", hardened: true,
		files: map[string]string{
			"package.json.tmpl": "package.json",
			"server.js.tmpl":    "server.js",
			"gitignore":         ".gitignore",
			"dockerignore":      ".dockerignore",
		},
	})
}
