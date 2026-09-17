package runtime

import (
	"slices"
	"strings"
	"testing"
)

func testParams() Params {
	return Params{Name: "svc", Module: "github.com/o/svc", Owner: "o", Port: 3000,
		Image: "ghcr.io/o/svc"}
}

// Templates live in files and are only exercised at generate time, so a
// typo would otherwise surface as a panic in front of a user creating a
// service. Render every registered runtime here instead.
func TestEveryRuntimeRendersCleanly(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			r, err := Get(name)
			if err != nil {
				t.Fatalf("Get(%q) = %v", name, err)
			}
			a := r.Artifacts(testParams())

			if len(a.Files) == 0 {
				t.Fatal("Artifacts returned no files")
			}
			for _, f := range a.Files {
				if f.Path == "" {
					t.Error("file with empty path")
				}
				if strings.Contains(f.Body, "{{") {
					t.Errorf("%s still contains an unrendered action:\n%s", f.Path, f.Body)
				}
			}
			if strings.TrimSpace(a.Workflow) == "" {
				t.Error("Workflow is empty")
			}
			// A GitHub Actions ${{ }} expression must survive templating.
			if !strings.Contains(a.Workflow, "${{") {
				t.Error("workflow has no ${{ }} expressions; templating likely ate them")
			}
			if strings.Contains(a.Workflow, "{{ .") {
				t.Errorf("workflow contains an unrendered action:\n%s", a.Workflow)
			}

			if a.Deployable {
				if !strings.Contains(a.Dockerfile, "EXPOSE 3000") {
					t.Errorf("Dockerfile did not substitute Port:\n%s", a.Dockerfile)
				}
			} else if a.Dockerfile != "" {
				t.Errorf("non-deployable runtime produced a Dockerfile:\n%s", a.Dockerfile)
			}
		})
	}
}

// A hardened runtime's image must run as the uid the manifests expect, or
// the pod cannot exec its binary and fails with no logs.
func TestHardenedRuntimesRunAsNonroot(t *testing.T) {
	for _, name := range Names() {
		r, _ := Get(name)
		a := r.Artifacts(testParams())
		if !r.SupportsHardened() || !a.Deployable {
			continue
		}
		if !strings.Contains(a.Dockerfile, "65532") {
			t.Errorf("runtime %q claims hardened support but its Dockerfile does not use uid 65532", name)
		}
	}
}

// A CLI ships a self-update command, which is the reason the shape exists;
// without it users have no way to get a new version.
func TestCLIRuntimesShipSelfUpdate(t *testing.T) {
	p := Params{Name: "mytool", Module: "github.com/o/mytool", Owner: "o", Port: 3000}
	for _, name := range Names() {
		r, _ := Get(name)
		a := r.Artifacts(p)
		if a.Deployable {
			continue
		}
		var hasUpdate, hasVersion bool
		for _, f := range a.Files {
			switch f.Path {
			case "update.go":
				hasUpdate = true
				if !strings.Contains(f.Body, `repoOwner = "o"`) {
					t.Errorf("%s: update.go did not substitute Owner", name)
				}
				if !strings.Contains(f.Body, `repoName  = "mytool"`) {
					t.Errorf("%s: update.go did not substitute Name", name)
				}
			case "VERSION":
				hasVersion = true
			}
		}
		if !hasUpdate {
			t.Errorf("CLI runtime %q ships no update.go", name)
		}
		if !hasVersion {
			t.Errorf("CLI runtime %q ships no VERSION file", name)
		}
		// The release workflow must name assets the way update looks for
		// them, or self-update fails for everyone.
		if !strings.Contains(a.Workflow, "mytool_${GOOS}_${GOARCH}.tar.gz") {
			t.Errorf("CLI runtime %q: release asset name does not match what update expects", name)
		}
		if !strings.Contains(a.Workflow, "main.Version=") {
			t.Errorf("CLI runtime %q: release does not inject Version, so the binary reports dev and refuses to update", name)
		}
	}
}

// A monorepo service must only rebuild when its own directory changes.
func TestMonorepoWorkflowIsPathFiltered(t *testing.T) {
	r, _ := Get("go-service")
	p := testParams()
	p.PathFilter = "services/svc"
	a := r.Artifacts(p)

	if !strings.Contains(a.Workflow, "services/svc/**") {
		t.Error("monorepo workflow has no path filter; every push would rebuild every service")
	}
	if !strings.Contains(a.Workflow, "working-directory: services/svc") {
		t.Error("monorepo workflow does not set working-directory")
	}
}

func TestGetUnknownRuntimeListsAvailable(t *testing.T) {
	_, err := Get("cobol")
	if err == nil {
		t.Fatal("Get(cobol) succeeded")
	}
	if !strings.Contains(err.Error(), "go-service") {
		t.Errorf("error should list available runtimes; got %v", err)
	}
}

// Go randomizes map iteration order, so ranging over the file map made
// every scaffold list its files in a different order.
func TestArtifactsAreDeterministic(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		p := Params{Name: "svc", Module: "example.com/svc", Port: 3000}

		var first []string
		for i := 0; i < 25; i++ {
			var paths []string
			for _, f := range r.Artifacts(p).Files {
				paths = append(paths, f.Path)
			}
			if i == 0 {
				first = paths
				continue
			}
			if !slices.Equal(paths, first) {
				t.Fatalf("%s: file order changed between runs:\n  run 0: %v\n  run %d: %v",
					name, first, i, paths)
			}
		}
	}
}

// Dependency resolution used to be inferred from a "go-"/"node-" prefix on
// the runtime name, in a switch at the call site. A runtime whose name did
// not start with one of those silently got no lockfile - and a Go service
// without go.sum does not build at all. Every runtime that generates a
// manifest of dependencies must declare how to lock it.
func TestRuntimesDeclareDependencyResolution(t *testing.T) {
	// An application must lock its dependencies or it does not build
	// reproducibly.
	manifests := map[string]string{
		"go.mod":       "go",
		"package.json": "npm",
	}

	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		files := r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000}).Files

		for _, f := range files {
			tool, needsLock := manifests[f.Path]
			if !needsLock {
				continue
			}
			// go-service's package.json describes the client it
			// publishes, not an application it builds. A published
			// library ships no lockfile: the consumer's lockfile pins
			// what it resolves, and shipping one would pin nothing for
			// anybody.
			if f.Path == "package.json" && name == "go-service" {
				continue
			}
			cmds := r.ResolveDeps()
			if len(cmds) == 0 {
				t.Errorf("%s generates %s but declares no ResolveDeps; its scaffold will not build",
					name, f.Path)
				continue
			}
			// A runtime may resolve more than one ecosystem - go-service
			// locks Go modules and generates its TypeScript client - so
			// this asks that the right tool is among the commands, not
			// that every command uses it.
			var found bool
			for _, argv := range cmds {
				if len(argv) > 0 && argv[0] == tool {
					found = true
				}
			}
			if !found {
				t.Errorf("%s generates %s but no resolve command runs %q: %v", name, f.Path, tool, cmds)
			}
		}
	}
}

// A log line that does not say which service emitted it is worth little
// outside its Loki label context - in a ticket, an alert, or a terminal.
// A deployable runtime must stamp its identity onto the default logger.
func TestDeployableRuntimesStampLogContext(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		a := r.Artifacts(Params{
			Name: "svc", Team: "platform",
			Module: "example.com/svc", Port: 3000,
		})
		if !a.Deployable {
			continue // a CLI writes to a terminal, not an aggregator
		}

		var entry string
		for _, f := range a.Files {
			if f.Path == "main.go" || f.Path == "server.js" {
				entry = f.Body
			}
		}
		if entry == "" {
			t.Errorf("%s: no entrypoint file among its artifacts", name)
			continue
		}
		for _, want := range []string{"svc", "platform"} {
			if !strings.Contains(entry, want) {
				t.Errorf("%s: entrypoint does not carry %q in its log context", name, want)
			}
		}
	}
}

// Defaults a deployable service should not have to remember. Each of
// these was written by hand in approvald first; a template that omits
// them makes every new service rediscover the same things.
func TestDeployableRuntimesSetServiceDefaults(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		a := r.Artifacts(Params{
			Name: "svc", Team: "platform",
			Module: "example.com/svc", Port: 3000,
		})
		if !a.Deployable {
			continue
		}

		var entry string
		for _, f := range a.Files {
			if f.Path == "main.go" || f.Path == "server.js" {
				entry = f.Body
			}
		}

		// Requests are logged, and the counter that Alloy scrapes counts
		// them - the annotation is otherwise pointed at runtime metrics
		// that say nothing about the service.
		for _, want := range []string{"request", "http_requests_total", "duration_ms"} {
			if !strings.Contains(entry, want) {
				t.Errorf("%s: no %s in its entrypoint", name, want)
			}
		}

		// The route label and log field must come from a matched route,
		// never the raw URL: a path can carry a token or an id, and this
		// reaches both the log aggregator and a metric label.
		if !strings.Contains(entry, "route") {
			t.Errorf("%s: does not log a route", name)
		}

		// Self-observation: /metrics is scraped every 15s and /healthz
		// probed as often. Logging them buries real traffic.
		if !strings.Contains(entry, "/metrics") || !strings.Contains(entry, "/healthz") {
			t.Errorf("%s: does not exclude its own probe endpoints", name)
		}
	}
}

// A Server with no timeouts lets a slow client hold a connection open
// indefinitely. approvald sets these; the template must too.
func TestGoServiceSetsServerTimeouts(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	var main string
	for _, f := range r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000}).Files {
		if f.Path == "main.go" {
			main = f.Body
		}
	}
	for _, want := range []string{"ReadHeaderTimeout", "ReadTimeout", "WriteTimeout", "IdleTimeout"} {
		if !strings.Contains(main, want) {
			t.Errorf("go-service does not set %s", want)
		}
	}
}

// A scaffold inherits whatever the template pins, so a stale pin is a
// stale starting point for every service created afterwards. These assert
// the floor, not the ceiling: bump them when the template is bumped.
func TestTemplatesPinSupportedVersions(t *testing.T) {
	// The Node runtime and the Go directive across every artifact a
	// runtime produces, keyed by what must appear.
	wants := map[string][]string{
		"go-service":   {"go 1.27", "client_golang v1.24"},
		"go-cli":       {"go 1.27"},
		"node-service": {"nodejs24", "node:24", "node-version: 24", "fastify", "prom-client"},
	}

	for name, want := range wants {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		a := r.Artifacts(Params{Name: "svc", Team: "t", Module: "example.com/svc", Port: 3000})

		// Everything the runtime emits, so a version can be asserted
		// wherever it lives - go.mod, Dockerfile or CI.
		var all strings.Builder
		for _, f := range a.Files {
			all.WriteString(f.Body)
		}
		all.WriteString(a.Dockerfile)
		all.WriteString(a.Workflow)

		for _, w := range want {
			if !strings.Contains(all.String(), w) {
				t.Errorf("%s: no %q in its artifacts - a version pin was bumped in one place only", name, w)
			}
		}
	}
}

// Both Go runtimes must agree on the toolchain: CI reads go-version-file,
// so a split means two services built by different compilers.
func TestGoRuntimesAgreeOnToolchain(t *testing.T) {
	var seen string
	for _, name := range []string{"go-service", "go-cli"} {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}
		for _, f := range r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000}).Files {
			if f.Path != "go.mod" {
				continue
			}
			for _, line := range strings.Split(f.Body, "\n") {
				if !strings.HasPrefix(line, "go ") {
					continue
				}
				if seen == "" {
					seen = line
				} else if line != seen {
					t.Errorf("go runtimes disagree on toolchain: %q vs %q", seen, line)
				}
			}
		}
	}
	if seen == "" {
		t.Fatal("no go directive found in either Go runtime's go.mod")
	}
}

// A runtime whose test command finds no test files exits 0, so CI reports
// green on a service with no coverage and no signal that any is missing.
// node-service shipped exactly that: a "test" script and no test file.
func TestRuntimesShipATestFile(t *testing.T) {
	suffixes := []string{"_test.go", ".test.js", ".test.ts", ".spec.js"}

	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%q) = %v", name, err)
		}

		var found string
		for _, f := range r.Artifacts(Params{
			Name: "svc", Team: "t", Module: "example.com/svc", Port: 3000,
		}).Files {
			for _, suffix := range suffixes {
				if strings.HasSuffix(f.Path, suffix) {
					found = f.Path
				}
			}
		}
		if found == "" {
			t.Errorf("%s ships no test file; its CI test step would pass without running anything", name)
		}
	}
}

// go-service is spec-first: the API is described once, in openapi.yml,
// and the compiler refuses to build until the handlers match. Losing any
// piece of this turns the spec back into documentation that drifts.
func TestGoServiceIsSpecFirst(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a := r.Artifacts(Params{Name: "svc", Team: "t", Module: "example.com/svc", Port: 3000})

	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Body
	}

	// The spec itself, and the directive that turns it into code.
	if _, ok := files["openapi.yml"]; !ok {
		t.Error("no openapi.yml: the service has no API contract to generate from")
	}
	gen, ok := files["generate.go"]
	if !ok {
		t.Fatal("no generate.go: nothing regenerates the API")
	}
	if !strings.Contains(gen, "go:generate") || !strings.Contains(gen, "ogen") {
		t.Errorf("generate.go does not invoke ogen:\n%s", gen)
	}

	// init runs Generate before ResolveDeps, and must: api/ does not
	// exist until ogen has run, so `go get` would fail on the import.
	var order []string
	for _, cmd := range append(r.Generate(), r.ResolveDeps()...) {
		order = append(order, strings.Join(cmd, " "))
	}
	joined := strings.Join(order, " | ")
	genAt, tidyAt := strings.Index(joined, "generate"), strings.Index(joined, "tidy")
	if genAt < 0 || tidyAt < 0 || genAt > tidyAt {
		t.Errorf("generation must precede tidy, got: %v", order)
	}

	// Regenerating must be deterministic: `regen` runs Generate and CI
	// diffs the result, so a command whose output depends on the day
	// would be a red build nobody caused.
	for _, cmd := range r.Generate() {
		if strings.Contains(strings.Join(cmd, " "), "-u") {
			t.Errorf("Generate runs an upgrading command, which belongs in ResolveDeps: %v", cmd)
		}
	}

	// CI must reject a spec that was changed without regenerating.
	if !strings.Contains(a.Workflow, "git diff --exit-code") {
		t.Error("CI does not check that generated code is current")
	}
}

// The generated client supplies what ogen deliberately does not: a
// timeout, a bounded retry, a circuit breaker, and the version header a
// server uses to see who is still calling it.
func TestGoServiceClientHasResilienceDefaults(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a := r.Artifacts(Params{
		Name: "svc", Team: "t", Module: "example.com/svc",
		Port: 3000, SpecVersion: InitialSpecVersion,
	})

	var client string
	for _, f := range a.Files {
		if f.Path == "api/client.go" {
			client = f.Body
		}
	}
	if client == "" {
		t.Fatal("go-service ships no client.go")
	}

	for _, want := range []string{
		"X-Client-Version",          // who is calling
		"SingleRetry",               // one retry, not five
		"ExponentialRetry",          // the escape hatch
		"NoRetry",                   // fail fast
		"ErrCircuitOpen",            // stop calling something that is failing
		"ht.Client = (*HTTPClient)", // compile-time proof it plugs into ogen
	} {
		if !strings.Contains(client, want) {
			t.Errorf("client.go has no %s", want)
		}
	}

	// Paging: a cursor loop written by hand is easy to get wrong, and a
	// forgotten cursor update is an infinite loop against a real service.
	var paging string
	for _, f := range a.Files {
		if f.Path == "api/paging.go" {
			paging = f.Body
		}
	}
	if !strings.Contains(paging, "iter.Seq2") {
		t.Error("paging.go does not return a range-able sequence")
	}

	// The version the client reports must be the spec's, not a second
	// number that drifts.
	if !strings.Contains(client, `ClientVersion = "`+InitialSpecVersion+`"`) {
		t.Error("client.go does not report the spec version")
	}
}

// Consuming a service from Go is `go get` on the service repo - api/ is
// committed and self-contained, so there is no publish step. The client
// defaults have to live in that package too: a consumer importing only
// the generated code would get a protocol client with no timeout, no
// retry and no breaker, which is the opposite of the point.
func TestGoServiceClientIsImportable(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a := r.Artifacts(Params{
		Name: "svc", Team: "t", Module: "example.com/svc",
		Port: 3000, SpecVersion: InitialSpecVersion,
	})

	paths := map[string]string{}
	for _, f := range a.Files {
		paths[f.Path] = f.Body
	}

	for _, want := range []string{"api/client.go", "api/paging.go"} {
		body, ok := paths[want]
		if !ok {
			t.Errorf("%s is not generated into api/, so a consumer cannot import it", want)
			continue
		}
		// Anything in package main is unreachable from another module.
		if strings.HasPrefix(strings.TrimSpace(body), "package main") {
			t.Errorf("%s is in package main; a consumer importing api/ cannot use it", want)
		}
	}

	// Nothing in api/ may depend on the service's own main package, or
	// importing it would drag the server in.
	for path, body := range paths {
		if !strings.HasPrefix(path, "api/") {
			continue
		}
		if strings.Contains(body, `"example.com/svc"`) {
			t.Errorf("%s imports the service's main package", path)
		}
	}
}

// A Node service calling a Go service should get the same behaviour a Go
// caller does. Both clients come from the same spec and carry the same
// defaults, so a slow dependency fails the same way in either language.
func TestGoServiceShipsATypeScriptClient(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a := r.Artifacts(Params{
		Name: "svc", Team: "t", Module: "example.com/svc", Owner: "acme",
		Port: 3000, SpecVersion: InitialSpecVersion,
	})

	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Body
	}

	// npm looks for package.json at the repo root when installing from
	// git, which is how a consumer gets this without a registry.
	pkg, ok := files["package.json"]
	if !ok {
		t.Fatal("no root package.json; `npm install git+...` cannot find the client")
	}
	if !strings.Contains(pkg, `"version": "`+InitialSpecVersion+`"`) {
		t.Error("package.json version does not track the spec version")
	}
	// Without `files`, the published tarball carries the Go service too.
	if !strings.Contains(pkg, `"files"`) {
		t.Error("package.json has no files list; the Go source would ship to consumers")
	}

	js, ok := files["clients/ts/index.js"]
	if !ok {
		t.Fatal("no clients/ts/index.js")
	}
	// The version is read from package.json, never copied here - the
	// header cannot then disagree with the version a consumer installed.
	if !strings.Contains(js, "pkg.version") {
		t.Error("the TypeScript client hardcodes its version instead of reading package.json")
	}

	for _, want := range []string{
		"X-Client-Version", "singleRetry", "exponentialRetry",
		"noRetry", "CircuitOpenError", "Breaker",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the TypeScript client has no %s; it does not match the Go client's defaults", want)
		}
	}

	if _, ok := files["clients/ts/index.d.ts"]; !ok {
		t.Error("no clients/ts/index.d.ts; consumers get no types for the client itself")
	}

	// The types come from the spec, so CI has to catch a stale client
	// the same way it catches stale Go - and via the same command a
	// developer runs, or the two can check different things.
	if !strings.Contains(a.Workflow, "homelabctl regen") {
		t.Error("CI does not regenerate from the spec before diffing")
	}
	// regen covers both clients, so CI diffing its output covers the
	// TypeScript one without naming it.
	if !strings.Contains(a.Workflow, "git diff --exit-code") {
		t.Error("CI regenerates but never diffs, so nothing fails on stale output")
	}
}
