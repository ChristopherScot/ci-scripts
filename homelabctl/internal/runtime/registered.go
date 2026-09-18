package runtime

// The runtimes that ship with homelabctl. Adding a language means adding a
// directory under templates/ and an entry here - no other code changes.
func init() {
	Register(embedded{
		name: "go-service", dir: "go-service", deployable: true, hardened: true,
		// Everything derived from openapi.yml. Deterministic: the same
		// spec produces the same output, so CI can run these and fail on
		// a diff.
		generate: [][]string{
			{"go", "generate", "./..."},
			// The TypeScript client's types come from the same spec.
			// Pinned to openapi-typescript 7 because it requires
			// TypeScript ^5 and breaks on 7; running it through npx
			// keeps that constraint out of the service's own
			// dependencies, which stay current.
			{"npx", "--yes", "openapi-typescript@7", "openapi.yml", "-o", "clients/ts/schema.d.ts"},
		},
		// Without go.sum the service does not build at all.
		lock: [][]string{{"go", "mod", "tidy"}},
		// A new service starts on current transitive versions; tidy alone
		// resolves to the minimums each dependency declares, which are
		// older than what is released.
		upgrade: [][]string{{"go", "get", "-u", "./..."}},
		files: map[string]tmpl{
			"go.mod.tmpl":       {dst: "go.mod"},
			"main.go.tmpl":      {dst: "main.go"},
			"main_test.go.tmpl": {dst: "main_test.go", plain: "main_test_plain.go.tmpl"},
			"server.go.tmpl":    {dst: "server.go", plain: "server_plain.go.tmpl"},
			"README.md.tmpl":    {dst: "README.md"},

			// Everything openapi.yml feeds. Dropped for a service built
			// without one, which has no spec to derive them from.
			"openapi.yml.tmpl":    {dst: "openapi.yml", specOnly: true},
			"generate.go.tmpl":    {dst: "generate.go", specOnly: true},
			"ogen.yml.tmpl":       {dst: "ogen.yml", specOnly: true},
			"client.go.tmpl":      {dst: "api/client.go", specOnly: true},
			"client_test.go.tmpl": {dst: "api/client_test.go", specOnly: true},
			"paging.go.tmpl":      {dst: "api/paging.go", specOnly: true},
			"paging_test.go.tmpl": {dst: "api/paging_test.go", specOnly: true},

			// The TypeScript client. Its package.json sits WITH the code
			// it describes rather than at the service root: it used to be
			// at the root so `npm install github:owner/repo` could find
			// it, but that never worked in a monorepo - npm's git
			// installer reads package.json from the REPOSITORY root, and
			// a monorepo puts the service at services/<name>/. The client
			// is published to npmjs instead, so nothing installs from git
			// and the manifest can live where it belongs.
			"clients_ts_package.json.tmpl": {dst: "clients/ts/package.json", specOnly: true},
			"clients_ts_index.js.tmpl":     {dst: "clients/ts/index.js", specOnly: true},
			"clients_ts_index.d.ts.tmpl":   {dst: "clients/ts/index.d.ts", specOnly: true},

			"gitignore.tmpl": {dst: ".gitignore"},
			"dockerignore":   {dst: ".dockerignore"},

			// Carried on Artifacts rather than written from this map -
			// setupLocal decides where CI lands, which differs in a
			// monorepo. It is declared here so its specless variant has
			// one home with every other file's, rather than a second
			// table only the workflow uses.
			"workflow.yaml": {dst: "", plain: "workflow_plain.yaml"},
		},
	})

	// A CLI is not deployed: no Dockerfile, no manifests, no Argo app. It
	// cross-compiles and publishes release assets, and ships the same
	// self-update command homelabctl uses.
	Register(embedded{
		name: "go-cli", dir: "go-cli", deployable: false, hardened: false,
		lock:    [][]string{{"go", "mod", "tidy"}},
		upgrade: [][]string{{"go", "get", "-u", "./..."}},
		files: map[string]tmpl{
			"go.mod.tmpl":        {dst: "go.mod"},
			"main.go.tmpl":       {dst: "main.go"},
			"update.go.tmpl":     {dst: "update.go"},
			"main_test.go.tmpl":  {dst: "main_test.go"},
			"completion.go.tmpl": {dst: "completion.go"},
			"VERSION.tmpl":       {dst: "VERSION"},
			"gitignore.tmpl":     {dst: ".gitignore"},
		},
	})

	Register(embedded{
		name: "node-service", dir: "node-service", deployable: true, hardened: true,
		// Generates package-lock.json, which the Dockerfile's `npm ci`
		// requires and which is not otherwise created.
		// npm resolves ^ ranges to the newest matching release already,
		// so locking and upgrading are the same command here.
		lock: [][]string{{"npm", "install", "--package-lock-only"}},
		files: map[string]tmpl{
			"package.json.tmpl":   {dst: "package.json"},
			"server.js.tmpl":      {dst: "server.js"},
			"server.test.js.tmpl": {dst: "server.test.js"},
			"gitignore":           {dst: ".gitignore"},
			"dockerignore":        {dst: ".dockerignore"},
		},
	})
}
