package runtime

import (
	"slices"
	"strings"
	"testing"
)

func testParams() Params {
	return Params{Name: "svc", Module: "github.com/o/svc", Owner: "o", Port: 3000,
		Image: "ghcr.io/o/svc", Spec: true}
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
	p := Params{Name: "mytool", Module: "github.com/o/mytool", Owner: "o", Port: 3000, Spec: true}
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

// The same for go-cli, which had NONE of this: a monorepo CLI rebuilt on
// every unrelated push, ran `go test ./...` from the repo root where its
// go.mod is not, and - worst - gated its release on `grep -qx VERSION`
// against a file at services/<name>/VERSION. That match can never
// succeed, so no monorepo CLI could ever publish a release, and a gate
// that stays shut looks exactly like a commit that did not bump the
// version.
func TestMonorepoCLIWorkflowIsScopedToItsDirectory(t *testing.T) {
	r, _ := Get("go-cli")
	p := testParams()
	p.PathFilter = "services/svc"
	a := r.Artifacts(p)

	for _, want := range []string{
		"services/svc/**",                 // path filter
		"working-directory: services/svc", // tests and build run there
		"grep -qx 'services/svc/VERSION'", // the release gate can open
		"go-version-file: services/svc/go.mod",
		"services/svc/checksums.txt", // assets are found where built
	} {
		if !strings.Contains(a.Workflow, want) {
			t.Errorf("monorepo CLI workflow missing %q", want)
		}
	}
}

// A dedicated repo must not gain any of that: the CLI is at the root, and
// a working-directory or a prefixed VERSION path would point at nothing.
func TestSingleRepoCLIWorkflowStaysAtTheRoot(t *testing.T) {
	r, _ := Get("go-cli")
	a := r.Artifacts(testParams()) // no PathFilter

	if !strings.Contains(a.Workflow, "grep -qx 'VERSION'") {
		t.Error("single-repo CLI should gate on a bare VERSION")
	}
	// Checked against non-comment lines only: the workflow explains the
	// monorepo case in a comment, and matching that would be testing the
	// prose rather than the YAML.
	var live []string
	for _, l := range strings.Split(a.Workflow, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			live = append(live, l)
		}
	}
	yaml := strings.Join(live, "\n")
	for _, unwanted := range []string{"working-directory:", "services/"} {
		if strings.Contains(yaml, unwanted) {
			t.Errorf("single-repo CLI workflow should not contain %q", unwanted)
		}
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
		p := Params{Name: "svc", Module: "example.com/svc", Port: 3000, Spec: true}

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
		files := r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000, Spec: true}).Files

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
			cmds := r.Lock()
			if len(cmds) == 0 {
				t.Errorf("%s generates %s but declares no Lock command; its scaffold will not build",
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
			Spec: true,
			Name: "svc", Team: "platform",
			Module: "example.com/svc", Port: 3000,
		})
		if !a.Deployable {
			continue // a CLI writes to a terminal, not an aggregator
		}

		// Across every source file the runtime ships, not one filename:
		// the property is that the SERVICE does these things, and which
		// file holds them is an implementation detail that has moved
		// once already.
		var entry string
		for _, f := range a.Files {
			if strings.HasSuffix(f.Path, ".go") || strings.HasSuffix(f.Path, ".js") {
				entry += f.Body
			}
		}
		if entry == "" {
			t.Errorf("%s: no source files among its artifacts", name)
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
			Spec: true,
			Name: "svc", Team: "platform",
			Module: "example.com/svc", Port: 3000,
		})
		if !a.Deployable {
			continue
		}

		// Across every source file the runtime ships: the property is
		// that the SERVICE does these things, not that one named file
		// does.
		var entry string
		for _, f := range a.Files {
			if strings.HasSuffix(f.Path, ".go") || strings.HasSuffix(f.Path, ".js") {
				entry += f.Body
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
	for _, f := range r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000, Spec: true}).Files {
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
		a := r.Artifacts(Params{Name: "svc", Team: "t", Module: "example.com/svc", Port: 3000, Spec: true})

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
		for _, f := range r.Artifacts(Params{Name: "svc", Module: "example.com/svc", Port: 3000, Spec: true}).Files {
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
			Spec: true,
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
	a := r.Artifacts(Params{Name: "svc", Team: "t", Module: "example.com/svc", Port: 3000, Spec: true})

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

	// init runs Generate before Lock, and must: api/ does not exist until
	// ogen has run, so resolving imports would fail.
	if len(r.Generate(testParams())) == 0 {
		t.Error("go-service generates nothing, so the spec derives no code")
	}
	if len(r.Lock()) == 0 {
		t.Error("go-service locks nothing, so a scaffold has no go.sum")
	}

	// regen runs Generate and Lock and CI diffs the result, so neither
	// may depend on what the world published today. Only Upgrade may.
	for phase, cmds := range map[string][][]string{
		"Generate": r.Generate(testParams()),
		"Lock":     r.Lock(),
	} {
		for _, cmd := range cmds {
			if strings.Contains(strings.Join(cmd, " "), " -u") {
				t.Errorf("%s runs an upgrading command, which belongs in Upgrade: %v", phase, cmd)
			}
		}
	}
	if len(r.Upgrade()) == 0 {
		t.Error("go-service never upgrades, so a new service starts on stale transitive versions")
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
		Spec: true,
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
		Spec: true,
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
		Spec: true,
		Name: "svc", Team: "t", Module: "example.com/svc", Owner: "acme",
		Port: 3000, SpecVersion: InitialSpecVersion,
	})

	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Body
	}

	// The manifest sits WITH the client, not at the service root. It was
	// at the root so `npm install github:owner/repo` could find it, but
	// npm's git installer reads package.json from the REPOSITORY root -
	// which a monorepo service is not - so that never worked for both
	// layouts. The client is published to npmjs instead.
	//
	// Beside index.js is also where it has to be for the client to read
	// its own version at runtime: index.js imports ./package.json.
	pkg, ok := files["clients/ts/package.json"]
	if !ok {
		t.Fatal("no clients/ts/package.json; there is nothing to publish")
	}
	if !strings.Contains(pkg, `"version": "`+InitialSpecVersion+`"`) {
		t.Error("package.json version does not track the spec version")
	}
	// Publishing a scoped package defaults to private, which fails for an
	// account without a paid plan - and fails at publish time, after the
	// Go tag for the same spec version has already been pushed.
	if !strings.Contains(pkg, `"access": "public"`) {
		t.Error("package.json does not request public access; a scoped publish would be private")
	}
	if files["package.json"] != "" {
		t.Error("a package.json at the service root is the old git-install layout")
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
	// Relative to index.js, which is the same directory. It used to be
	// '../../package.json' because the manifest was at the service root;
	// with the manifest moved, that path points outside the published
	// tarball and the client fails to load for every consumer.
	if !strings.Contains(js, "from './package.json'") {
		t.Error("the client's package.json import does not point beside it")
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

// A teammate cloning a scaffolded service lands on a Go repo containing
// openapi.yml, generate.go, api/, clients/ts/ and a package.json. The
// per-file comments explain each one once opened; the README is what
// orients someone before they open anything.
func TestGoServiceShipsAReadme(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a := r.Artifacts(Params{
		Spec: true,
		Name: "svc", Team: "platform", Module: "example.com/svc", Owner: "acme",
		Port: 3000, SpecVersion: InitialSpecVersion,
	})

	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Body
	}

	readme, ok := files["README.md"]
	if !ok {
		t.Fatal("go-service ships no README")
	}

	// Templated, not generic.
	for _, want := range []string{"# svc", "platform", "acme"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not mention %q, so it is not about this service", want)
		}
	}

	// Every path it points at has to exist, or it sends people to files
	// that are not there. config.yaml and deploy/ are excluded: init
	// writes those, not the runtime.
	for _, path := range []string{"openapi.yml", "main.go", "api/client.go", "api/paging.go"} {
		if !strings.Contains(readme, path) {
			continue // not described; nothing to verify
		}
		if _, ok := files[path]; !ok {
			t.Errorf("README describes %s, which the runtime does not generate", path)
		}
	}

	// The instruction that matters most: the spec is the source of truth.
	if !strings.Contains(readme, "homelabctl regen") {
		t.Error("README does not say how to regenerate after a spec change")
	}
}

// ogen instruments every operation with OpenTelemetry by default. Nothing
// here collects traces - no tracer is configured and the cluster runs no
// backend - so it cost a consumer 13 modules and ~1.5MB of binary to
// produce spans that went nowhere.
func TestGoServiceDisablesUnusedInstrumentation(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	a := r.Artifacts(Params{
		Spec: true,
		Name: "svc", Team: "t", Module: "example.com/svc", Owner: "acme",
		Port: 3000, SpecVersion: InitialSpecVersion,
	})

	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Body
	}

	cfg, ok := files["ogen.yml"]
	if !ok {
		t.Fatal("no ogen.yml, so generation takes every default")
	}
	if !strings.Contains(cfg, "ogen/otel") {
		t.Error("ogen.yml does not disable otel instrumentation")
	}

	// The config only applies if the generate directive passes it.
	gen, ok := files["generate.go"]
	if !ok {
		t.Fatal("no generate.go")
	}
	if !strings.Contains(gen, "ogen.yml") {
		t.Error("generate.go does not pass --config, so ogen.yml is ignored")
	}
}

// `go build` with no -o names the binary after the last element of the
// module path, not the service. go-shlink-redirector is the repo that
// proves the difference: a .gitignore keyed on the service name would
// leave a multi-megabyte binary to be committed by the first `git add -A`.
func TestGitignoreCoversTheBuiltBinary(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	p := testParams()
	p.Name = "shlink-redirector"
	p.Module = "github.com/ChristopherScot/go-shlink-redirector"

	var ignore string
	for _, f := range r.Artifacts(p).Files {
		if f.Path == ".gitignore" {
			ignore = f.Body
		}
	}
	if ignore == "" {
		t.Fatal("go-service ships no .gitignore")
	}
	if !strings.Contains(ignore, "/go-shlink-redirector") {
		t.Errorf(".gitignore does not cover the built binary:\n%s", ignore)
	}
}

func TestBinaryNameComesFromTheModulePath(t *testing.T) {
	for _, tc := range []struct{ module, name, want string }{
		{"github.com/o/go-shlink-redirector", "shlink-redirector", "go-shlink-redirector"},
		{"github.com/o/widget", "widget", "widget"},
		{"github.com/o/platform/services/widget", "widget", "widget"},
		{"", "fallback", "fallback"},
	} {
		if got := (Params{Module: tc.module, Name: tc.name}).BinaryName(); got != tc.want {
			t.Errorf("BinaryName(%q) = %q, want %q", tc.module, got, tc.want)
		}
	}
}

// The workflow does not go through renderFiles - Artifacts carries it
// separately - so it once missed the specless swap entirely and a
// service scaffolded with --no-spec got CI that ran `homelabctl regen`
// and diffed against an openapi.yml it does not have. It failed on the
// first push, which is the worst place to find out.
func TestSpeclessServiceGetsSpeclessCI(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	p := testParams()
	p.Spec = false

	// Checked against the RUNNABLE lines only: the header comment names
	// openapi.yml to explain why the spec-first jobs are absent, and
	// that explanation is the point rather than a leak.
	var steps []string
	for _, line := range strings.Split(r.Artifacts(p).Workflow, "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			steps = append(steps, line)
		}
	}
	wf := strings.Join(steps, "\n")
	for _, absent := range []string{"openapi.yml", "homelabctl regen", "info.version"} {
		if strings.Contains(wf, absent) {
			t.Errorf("specless CI runs %q, which this service has no source for:\n%s", absent, wf)
		}
	}
	// It must still build and check the manifests.
	for _, required := range []string{"docker/build-push-action", "check deploy"} {
		if !strings.Contains(wf, required) {
			t.Errorf("specless CI lost %q", required)
		}
	}
}

// And the spec-first default keeps its regen check.
func TestSpecFirstServiceKeepsTheStalenessCheck(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	wf := r.Artifacts(testParams()).Workflow
	if !strings.Contains(wf, "homelabctl regen") {
		t.Error("spec-first CI no longer checks that generated code is current")
	}
}

// cache-from/cache-to type=gha require the buildx driver. Without
// setup-buildx-action the default docker driver cannot export a cache
// and the build fails with "Cache export is not supported for the docker
// driver" - on the first push of every service scaffolded this way,
// after everything else has already passed.
// EVERY runtime, not just go-service. This asserted go-service alone and
// so missed node-service shipping a gha cache with no buildx for as long
// as that runtime has existed: every node image build failed with "Cache
// export is not supported for the docker driver", after the tests had
// passed.
func TestWorkflowsSetUpBuildxForTheirCache(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			label string
			spec  bool
		}{{"spec-first", true}, {"specless", false}} {
			t.Run(name+"/"+tc.label, func(t *testing.T) {
				p := testParams()
				p.Spec = tc.spec
				wf := r.Artifacts(p).Workflow
				if !strings.Contains(wf, "cache-to: type=gha") {
					return // no cache, no buildx needed
				}
				if !strings.Contains(wf, "docker/setup-buildx-action") {
					t.Errorf("uses a gha cache without setting up buildx; the build fails on the docker driver:\n%s", wf)
				}
			})
		}
	}
}

// regen passed a bare `false` to Generate, meaning "spec not disabled" -
// but the parameter meant "spec enabled", so it generated nothing and
// still reported success. Every `homelabctl regen` was a no-op, and CI's
// --check could not have caught a stale api/ because nothing regenerated
// it to compare against.
//
// Generate takes Params now, so there is one way to spell it and it
// cannot be inverted at a call site. This asserts the behaviour rather
// than the signature.
func TestGenerateFollowsTheConfigsSpecSetting(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}

	spec := testParams()
	if len(r.Generate(spec)) == 0 {
		t.Error("a spec-first service generates nothing; regen would be a no-op")
	}

	specless := testParams()
	specless.Spec = false
	if got := r.Generate(specless); len(got) != 0 {
		t.Errorf("a specless service would run %v, against a spec it does not have", got)
	}
}

// main.go tells the reader it is theirs and lists what is not. That
// claim has to stay true, and it differs by variant: a specless service
// has no api/ or clients/ to lose, so naming them would send someone
// looking for a directory that does not exist.
func TestMainDocumentsWhatItOwns(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	mainOf := func(p Params) string {
		for _, f := range r.Artifacts(p).Files {
			if f.Path == "main.go" {
				return f.Body
			}
		}
		t.Fatal("no main.go")
		return ""
	}

	spec := mainOf(testParams())
	for _, want := range []string{"THIS FILE IS YOURS", "api/, clients/", "deploy/*.yaml"} {
		if !strings.Contains(spec, want) {
			t.Errorf("spec-first main.go does not mention %q", want)
		}
	}

	p := testParams()
	p.Spec = false
	if s := mainOf(p); strings.Contains(s, "api/, clients/") {
		t.Error("specless main.go claims api/ and clients/, which it does not have")
	}
}

// A CLI's main.go documents that the whole repo is the author's, and
// names the two couplings that outlive that: VERSION driving releases,
// and the release asset name update.go downloads.
func TestCLIMainDocumentsItsCouplings(t *testing.T) {
	r, err := Get("go-cli")
	if err != nil {
		t.Fatal(err)
	}
	a := r.Artifacts(testParams())

	var mainGo, updateGo string
	for _, f := range a.Files {
		switch f.Path {
		case "main.go":
			mainGo = f.Body
		case "update.go":
			updateGo = f.Body
		}
	}
	for _, want := range []string{"THIS WHOLE REPO IS YOURS", "VERSION", "update.go"} {
		if !strings.Contains(mainGo, want) {
			t.Errorf("go-cli main.go does not mention %q", want)
		}
	}
	// It must not claim the service-only machinery applies.
	if strings.Contains(mainGo, "deploy/*.yaml") {
		t.Error("go-cli main.go names deploy/, which a CLI does not have")
	}

	// The documented asset name has to be the one both sides use: rename
	// either and self-update fails against a release that looks fine.
	asset := testParams().Name + "_%s_%s.tar.gz"
	if !strings.Contains(updateGo, asset) {
		t.Errorf("update.go does not download %q", asset)
	}
	if !strings.Contains(a.Workflow, testParams().Name+"_${GOOS}_${GOARCH}.tar.gz") {
		t.Error("CI does not build the asset name update.go downloads")
	}
}

// The three parallel maps this replaced - files, specFiles, specSwaps -
// were keyed on the same template names and aligned by hand. A typo in
// one was silent: a name absent from files resolved to "", so a
// spec-only file was written to an empty destination.
//
// Artifacts consults specOnly to decide what a specless service skips,
// so the pairing has to hold for every spec-only entry. One map per
// template cannot disagree with itself; this asserts the property.
func TestEverySpecOnlyTemplateHasADestination(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := r.(embedded)
		if !ok {
			continue // a runtime that is not template-backed has no map
		}
		for src, tm := range e.files {
			if tm.specOnly && tm.dst == "" {
				t.Errorf("%s: spec-only template %q has no destination", name, src)
			}
		}
	}
}

// Every declared template has to exist, or it panics at render time in
// front of someone creating a service. A dst of "" is the one legal
// exception: the workflow, which Artifacts carries separately.
func TestEveryDeclaredTemplateResolves(t *testing.T) {
	for _, name := range Names() {
		r, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range r.Artifacts(testParams()).Files {
			if f.Path == "" {
				t.Errorf("%s: a file was rendered with no destination", name)
			}
			if f.Body == "" {
				t.Errorf("%s: %s rendered empty", name, f.Path)
			}
		}
	}
}

// The manifests are derived from config.yaml, so a config change nobody
// re-rendered leaves them describing the old service - and `check
// deploy` does not notice, because it validates what is there rather
// than comparing it to what the config says.
//
// CI renders and diffs, the same guard it already runs for generated
// code. render takes no image ref and preflight degrades to a notice
// without a cluster, so the output is deterministic there.
func TestWorkflowsCheckManifestsAgainstConfig(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		spec bool
	}{{"spec-first", true}, {"specless", false}} {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams()
			p.Spec = tc.spec
			wf := r.Artifacts(p).Workflow
			if !strings.Contains(wf, "homelabctl render") {
				t.Error("CI does not re-render, so a stale manifest ships unnoticed")
			}
			if !strings.Contains(wf, "git diff --exit-code -- deploy/") {
				t.Error("CI renders but does not diff, so the render proves nothing")
			}
		})
	}
}

// Action inputs that name a file resolve from the REPOSITORY ROOT. They
// are not shell commands, so defaults.run.working-directory does not
// apply to them - and every runtime got this wrong at once, so a
// monorepo service failed in CI before running a single test:
//
//	go-service    "the specified go version file at: go.mod does not exist"
//	go-cli        the same
//	node-service  "Dependencies lock file is not found in .../<repo>"
//
// Each failure is at setup time, which is why none of them was caught by
// the workflow being otherwise correct.
func TestSetupActionsGetRepoRootPathsInAMonorepo(t *testing.T) {
	for _, tc := range []struct {
		runtime string
		inputs  []string
	}{
		// The tag job reads and diffs openapi.yml, which is not at the
		// repo root in a monorepo. It failed with "openapi.yml has no
		// info.version" against a spec that has one.
		{"go-service", []string{
			"go-version-file: services/svc/go.mod",
			"cache-dependency-path: services/svc/go.sum",
			"spec=services/svc/openapi.yml",
		}},
		{"go-cli", []string{"go-version-file: services/svc/go.mod", "cache-dependency-path: services/svc/go.sum"}},
		// node-service names its own directory. cache-dependency-path is
		// an action input, so it resolves from the repo root even though
		// `npm ci` runs in the service directory - the one path here
		// that is not relative to the working directory.
		{"node-service", []string{"cache-dependency-path: services/svc/package-lock.json"}},
	} {
		r, err := Get(tc.runtime)
		if err != nil {
			t.Fatal(err)
		}
		p := testParams()
		p.PathFilter = "services/svc"
		wf := r.Artifacts(p).Workflow

		for _, want := range tc.inputs {
			if !strings.Contains(wf, want) {
				t.Errorf("%s: workflow missing %q", tc.runtime, want)
			}
		}
	}
}

// The TypeScript client publishes by OIDC trusted publishing, not with a
// stored token.
//
// npm is retiring 2FA-bypass automation tokens: blocked from account
// operations in August 2026, and from publishing entirely around January
// 2027. A workflow built on NPM_TOKEN would stop working on a date
// nobody here would be watching for.
//
// id-token: write is what lets the job request the OIDC token. Without
// it npm publish fails to authenticate, and the failure reads like a
// registry problem rather than a missing permission.
func TestTypeScriptClientPublishesByOIDC(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	p := testParams()
	p.Spec = true
	wf := r.Artifacts(p).Workflow

	for _, want := range []string{"id-token: write", "npm publish --provenance"} {
		if !strings.Contains(wf, want) {
			t.Errorf("workflow missing %q", want)
		}
	}
	// A token would still work today, which is exactly why this asserts
	// its absence: the thing that breaks in 2027 looks fine now.
	for _, unwanted := range []string{"NPM_TOKEN", "NODE_AUTH_TOKEN"} {
		if strings.Contains(wf, unwanted) {
			t.Errorf("workflow uses %s; trusted publishing needs no stored credential", unwanted)
		}
	}
}

// regen rewrites generated clients and NOTHING the author owns.
//
// This is not a style preference. specOnly means "exists only for a
// spec-first service", which covers openapi.yml and ogen.yml - the spec
// is the author's source of truth, and rewriting it from the template
// replaced a five-endpoint API with the two-endpoint scaffold. The
// files regen may touch are the generated ones, which carry a "do not
// edit" header precisely because they are overwritten.
func TestRegenRewritesOnlyGeneratedFiles(t *testing.T) {
	r, err := Get("go-service")
	if err != nil {
		t.Fatal(err)
	}
	files := r.SpecFiles()
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}

	// Authored: destroying any of these loses work that cannot be
	// recovered from the template.
	for _, authored := range []string{"openapi.yml", "ogen.yml", "server.go", "config.yaml", "main.go"} {
		if set[authored] {
			t.Errorf("regen would overwrite %s, which the author owns", authored)
		}
	}
	// Generated: a fix to one of these templates has to reach services
	// that already exist, or regen is only useful on the day a service
	// is created.
	for _, generated := range []string{"api/client.go", "clients/ts/index.js", "clients/ts/package.json"} {
		if !set[generated] {
			t.Errorf("regen does not rewrite %s; a template fix would never reach existing services", generated)
		}
	}
}

// CI and the image build must agree on where a node service's lockfile
// is, because when they disagree the failure is remote from its cause.
//
// They did once: the workflow installed from a workspace lockfile at the
// repository root while the image still built with the SERVICE directory
// as its context, so `COPY package*.json ./` matched nothing and the
// build died with npm's EUSAGE - "can only install with an existing
// package-lock.json" - about a lockfile sitting one level up, outside
// the context.
//
// One lockfile, beside the service, used by both.
func TestNodeServiceLockfileIsWhereBothCIAndDockerLookForIt(t *testing.T) {
	for _, tc := range []struct{ pathFilter, wantCache, wantContext string }{
		{"", "./package-lock.json", "context: ."},
		{"services/svc", "services/svc/package-lock.json", "context: services/svc"},
	} {
		r, err := Get("node-service")
		if err != nil {
			t.Fatal(err)
		}
		p := testParams()
		p.PathFilter = tc.pathFilter
		a := r.Artifacts(p)

		// setup-node resolves cache-dependency-path from the repository
		// root, so it has to name the service directory explicitly.
		if !strings.Contains(a.Workflow, "cache-dependency-path: "+tc.wantCache) {
			t.Errorf("PathFilter %q: workflow missing cache path %q", tc.pathFilter, tc.wantCache)
		}
		// The build context is the service directory, which is what puts
		// its package-lock.json inside the context for `npm ci`.
		if !strings.Contains(a.Workflow, tc.wantContext) {
			t.Errorf("PathFilter %q: workflow missing %q", tc.pathFilter, tc.wantContext)
		}
		// Installed in the service directory, not hoisted to a workspace
		// root: a published client is an ordinary dependency, and a
		// workspace would link a sibling by symlink that dangles in the
		// runtime image.
		if strings.Contains(a.Workflow, "working-directory: .\n        run: npm ci") {
			t.Errorf("PathFilter %q: npm ci still installs from a workspace root", tc.pathFilter)
		}

		// A plain .dockerignore is read from the context root, which the
		// service directory is.
		var found bool
		for _, f := range a.Files {
			if f.Path == ".dockerignore" {
				found = true
			}
		}
		if !found {
			t.Errorf("PathFilter %q: no .dockerignore", tc.pathFilter)
		}
	}
}

// A TUI is a CLI that draws: same release shape, one extra file, and a
// root command that starts the interface instead of printing help.
func TestGoTUIIsACLIThatDraws(t *testing.T) {
	r, err := Get("go-tui")
	if err != nil {
		t.Fatal(err)
	}
	p := testParams()
	a := r.Artifacts(p)

	// Not deployed: no Dockerfile, no manifests, no Argo app.
	if a.Deployable {
		t.Error("go-tui should not be deployable")
	}
	if a.Dockerfile != "" {
		t.Error("go-tui should have no Dockerfile")
	}

	files := map[string]string{}
	for _, f := range a.Files {
		files[f.Path] = f.Body
	}

	// model.go is the file a TUI has and a CLI does not - it is where the
	// interface lives and the one people edit.
	if _, ok := files["model.go"]; !ok {
		t.Error("no model.go")
	}
	// The same self-update machinery as go-cli, so `<name> update` works.
	for _, want := range []string{"update.go", "completion.go", "VERSION"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}

	// v2, not v1. The APIs are incompatible and v1 is what nearly every
	// example online uses, so pinning the import path is what keeps a
	// generated repo from being pasted full of code that cannot compile.
	if !strings.Contains(files["go.mod"], "charm.land/bubbletea/v2") {
		t.Error("go.mod does not require bubbletea v2 at its charm.land path")
	}

	// The root command runs the program. Without this a bare invocation
	// prints help, which is right for a CLI and useless for a TUI.
	if !strings.Contains(files["main.go"], "RunE:") {
		t.Error("root command has no RunE, so the bare command cannot start the TUI")
	}
}

// Self-update asks GitHub for the latest release of owner/repo, and a
// release belongs to a REPOSITORY. In a monorepo that is the parent, not
// the service: pokedex-tui lives in ChristopherScot/pokemon, so asking
// for ChristopherScot/pokedex-tui 404s against a release that exists.
//
// Nothing catches this at build time - the constant is a plausible
// string either way, and the failure only appears when a user runs
// `<name> update` against a release that is sitting right there.
func TestSelfUpdateNamesTheRepoThatHoldsTheReleases(t *testing.T) {
	for _, tc := range []struct {
		name, module, want string
	}{
		{"dedicated repo", "github.com/owner/svc", "svc"},
		{"monorepo", "github.com/owner/mono/services/svc", "mono"},
		{"module with no host", "svc", "svc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testParams()
			p.Module = tc.module
			if got := p.RepoName(); got != tc.want {
				t.Errorf("RepoName() = %q, want %q", got, tc.want)
			}
		})
	}

	// And it reaches the generated file, for both runtimes that ship one.
	for _, rt := range []string{"go-cli", "go-tui"} {
		r, err := Get(rt)
		if err != nil {
			t.Fatal(err)
		}
		p := testParams()
		p.Module = "github.com/owner/mono/services/svc"
		p.PathFilter = "services/svc"

		for _, f := range r.Artifacts(p).Files {
			if f.Path != "update.go" {
				continue
			}
			if !strings.Contains(f.Body, `repoName = "mono"`) {
				t.Errorf("%s: update.go does not name the parent repo", rt)
			}
		}
	}
}
