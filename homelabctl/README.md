# homelabctl

Scaffolds services and renders their Kubernetes manifests. One tool, one
config schema, one place that encodes the cluster's conventions.

A Go service is **spec-first**: `openapi.yml` is the source of truth, and
the server interface, both clients and their validation are generated from
it. Add a path to the spec, regenerate, and the build fails until the
handler exists.

```sh
homelabctl init myservice --runtime go-service --host myservice.example.com --public
homelabctl init mytool --runtime go-cli

homelabctl regen                 # after editing openapi.yml
homelabctl regen --check         # what CI runs; exits 1 if anything is stale

homelabctl render config.yaml ghcr.io/owner/svc@sha256:...  --out .
homelabctl check deploy          # deploy misconfigurations and spec problems
homelabctl diff                  # what would change in the GitOps repo
homelabctl update
```

## How a service deploys

```
push to main
  -> GitHub Actions builds and pushes the image        (GITHUB_TOKEN only)
  -> argocd-image-updater sees the new digest          (in-cluster)
  -> commits it to the homelab repo                    (Vault-backed PAT)
  -> ArgoCD syncs                                      -> new pod
```

**No homelab credential lives in a service repo.** CI needs only
`GITHUB_TOKEN`, which Actions issues per run. The credential that can write
the homelab repo is in the cluster: a fine-grained PAT scoped to
`contents:write` on that one repo, stored in Vault, projected by External
Secrets. One place to rotate.

## Adding a runtime

A runtime is usually **no Go code at all**: a directory of template files
under `internal/runtime/templates/<name>/`, plus one entry in
`registered.go`. Nothing else changes — manifests, the Argo Application,
the image-updater annotations and CI are identical across languages.

```go
Register(embedded{
    name: "python-service", dir: "python-service",
    deployable: true, hardened: true,
    // See "The three build phases" below. Omit generate/upgrade if the
    // language has neither; omit lock only if it has no lockfile, since
    // a scaffold whose dependencies are unpinned may not build.
    lock:    [][]string{{"pip-compile", "requirements.in"}},
    files: map[string]string{
        "main.py.tmpl":    "main.py",
        "gitignore":       ".gitignore",
    },
})
```

The directory supplies `Dockerfile`, `steps.yaml` (the CI build steps) and
`workflow.yaml`, each rendered with the service's `Params`.

Write Go only if a runtime needs behaviour the templates cannot express —
build steps that inspect a lockfile, say. Then implement `runtime.Runtime`
and `Register` it, rather than adding a special case to `embedded`. That
type lives in `internal/runtime` alongside the others: the package is
internal and `embedded`'s fields are unexported, so a runtime cannot be
added from outside this module.

Three ship today:

| runtime | kind | produces |
|---|---|---|
| `go-service` | service | image + manifests + Argo Application |
| `node-service` | service | image + manifests + Argo Application |
| `go-cli` | cli | cross-compiled release assets, with self-update |

A CLI is not deployed, so it gets no Dockerfile, no manifests and no Argo
Application. Its CI cross-compiles on a VERSION bump and publishes assets
named to match what its generated `update` command looks for.

## Spec-first services

A `go-service` describes its API once, in `openapi.yml`. Everything else is
derived:

| generated | from | by |
|---|---|---|
| `api/oas_*.go` — server interface, types, validation, Go client | the spec | [ogen](https://github.com/ogen-go/ogen) |
| `clients/ts/schema.d.ts` — TypeScript types | the spec | openapi-typescript |

The point is that the **compiler enforces the contract**. Add a path to the
spec, run `homelabctl regen`, and the build fails with
`service does not implement api.Handler (missing method GetThing)` until
you write the handler. The spec cannot drift from the code, because the
code will not compile if it does.

CI runs `homelabctl regen` and diffs, so a spec edited without regenerating
fails the build rather than shipping an API that disagrees with its own
documentation.

`homelabctl check` also reports spec problems that generate *fine* and
still cost you something: a missing `operationId` (the generated method
name and its metric label then follow the path, and change when it does), a
duplicate `operationId`, a missing `default` response (handler errors
render as an undocumented empty 500), a missing summary (the generated
interface method has no documentation). It does not re-validate structure —
ogen already rejects a broken spec with a better error.

### The three build phases

A runtime declares up to three groups of commands, and they are separate
because they differ in one property that matters:

| phase | does | deterministic | run by |
|---|---|---|---|
| `Generate` | rebuilds what the spec derives | yes | `init`, `regen` |
| `Lock` | pins declared dependencies (`go mod tidy`) | yes | `init`, `regen` |
| `Upgrade` | moves dependencies forward (`go get -u`) | **no** | `init` only |

`regen` runs Generate and Lock. It never upgrades: CI runs `regen` and
diffs, so an upgrade there would turn any day a dependency published into a
red build nobody caused.

`init` runs all three, in that order — generation first, because a lockfile
cannot resolve an import that does not exist yet — so a new service starts
on current transitive versions rather than the minimums its direct
dependency declares.

## Clients

Both clients are generated from the spec and carry the same defaults, so a
Node service and a Go service calling the same API behave the same way when
it is slow or failing.

ogen generates correct protocol code and deliberately stops there — no
timeout, no retry, no breaker. `api/client.go` supplies those:

```go
c, err := api.NewClient(url, api.WithClient(api.NewHTTPClient(api.HTTPOptions{
    Timeout: 5 * time.Second,          // the default
    Policy:  api.SingleRetry{},        // the default
    Breaker: &api.Breaker{Threshold: 5, Cooldown: 30 * time.Second},
})))
```

Defaults are chosen for what they do to the **system**: one retry rather
than five, because five can mean five times the traffic to something
already struggling. `ExponentialRetry` (jittered, so a fleet does not retry
in lockstep) and `NoRetry` are the escape hatches. Only idempotent methods
are ever repeated — a POST may already have applied.

`api/paging.go` turns a paged operation into a range loop, so a cursor loop
written by hand — where a forgotten update is an infinite loop against a
real service — is not something every caller reimplements:

```go
for item, err := range api.Paged(ctx, func(ctx context.Context, cursor string) (api.Page[Thing], error) {
    res, err := c.ListThings(ctx, api.ListThingsParams{After: cursor})
    ...
}) {
    if err != nil {
        return err
    }
}
```

### Rate limiting

Off unless `RATE_LIMIT_RPS` is set - a service behind the LAN ingress with
one caller does not need it, and a limit nobody tuned rejects real
traffic. Set it in `config.yaml`'s `env` when you want it, with an
optional `RATE_LIMIT_BURST` (defaults to one second of headroom).

Limits are **per operation**, keyed on the spec's `operationId` rather
than a path pattern that can drift from the route it governs. A cached
read and a write that fans out to three services should not share a
budget:

```go
limiter().For("createThing", Policy{Rate: 1, Burst: 2})
```

**The buckets are per pod, not per service.** `replicas: 3` with
`RATE_LIMIT_RPS=10` admits 30/s in the worst case. That is right for
protecting a pod from one abusive caller - each pod defends itself - and
wrong for enforcing a quota, where the total would depend on how many
replicas happen to be running. A real quota needs shared state (Redis) or
a proxy in front, like Clever's sphinx; this deliberately has neither,
because a limiter that can fail to reach Redis is a new way for the
service to fall over. Set the number per pod accordingly, or put a proxy
in front when you need a true global limit.

A 429 carries `Retry-After`, and both clients honour it in preference to
their own backoff - so a caller importing this service's client backs off
correctly with no code. RFC 9110 allows a delay in seconds or an
HTTP-date; both are handled, and a value further out than
`MaxRetryAfter` (30s) falls back to the policy rather than parking a
request for an hour.

`RateLimit-Limit` and `RateLimit-Remaining` are advisory - the IETF
field is still a draft, so these follow the widely deployed de-facto
names and are safe to ignore. `http_requests_rate_limited_total` is worth
alerting on: a rising count is either an abusive caller or a limit set too
low, and those look identical from outside.

Probes are exempt. Limiting `/healthz` means the kubelet can restart a
pod for being busy.

Every request carries `X-Client-Version`, so a server can see which client
versions still call it before changing something they depend on. It tracks
the spec's `info.version`; `regen` keeps them in step.

## Releasing clients

Neither client needs a registry. **The git tag is the artifact.**

```sh
# Go: api/ is committed and self-contained
go get github.com/owner/myservice@v0.2.0

# TypeScript: npm installs from a git ref
npm install git+https://github.com/owner/myservice#v0.2.0
```

CI tags the repo when `info.version` changes, so a release is deliberate
rather than every push, and one version covers the API and both its
clients.

For npm this works because `package.json` sits at the repo root — npm looks
for it there and has no subdirectory syntax — with `files: [clients/ts]`
keeping the Go source out of what consumers receive. npm records the
resolved commit in the consumer's lockfile rather than the tag, so an
install stays reproducible even if a tag moves.

## Things that fail silently, and what this tool does about them

Each of these presented as a `Synced/Healthy` app with nothing shipping:

| Failure | Guard |
|---|---|
| No `kustomization.yaml` → image-updater skips the app, since it can only write into Kustomize/Helm/Plugin sources | always rendered; `check` fails without it |
| Abbreviated image SHA → `ImagePullBackOff`, because short SHAs are not registry tags | `render` and `check` reject them |
| Missing `write-back-target` → the updater writes a separate `.argocd-source` file, so two files claim to set the image | always in the rendered Application |
| Private package → `Could not get tags from registry: unauthorized` | documented; make the package public or give the updater a credential |
| Root Dockerfile under a hardened pod → `permission denied`, no logs | runtimes build nonroot 65532 images matching the rendered securityContext |
| Spec edited without regenerating → the API disagrees with its documentation | CI runs `regen` and diffs |
| A request path in a metric label → unbounded Prometheus cardinality, and tokens written to a log aggregator | labels come from the spec's operation IDs; an unrouted request is rejected before middleware runs |
| A handler error → ogen returns it to the caller and logs nothing, so a 500 leaves no trace | `NewError` logs it |
| A client version header that lies → you cannot tell who is still calling | `check` fails when `info.version` and the clients disagree |
| A service with no `port` → `targetPort: 0` and probes that can never pass, on manifests that apply cleanly | defaulted from one constant the schema and `init` flag also read |

## Config

`config.yaml` is the source of truth; `render` regenerates every manifest
from it. Use `overrides` only for what the schema cannot express — and
prefer widening the schema.

```yaml
name: approvald
team: me-myself-and-i
runtime: go-service
port: 3000
image:
  repository: ghcr.io/christopherscot/approvald
ingress:
  host: approve.example.com
  public: true
secrets:
  vaultPath: approvald/config
  keys: [NTFY_TOKEN, REGISTER_TOKEN]
```

## Adjusting generated manifests

Use `patches`, keyed by resource kind. Supply only what differs:

```yaml
patches:
  Deployment: |
    spec:
      replicas: 3
      template:
        metadata:
          annotations:
            example.com/owner: platform
```

Nested maps merge, so the annotations the tool generates survive alongside
yours. Everything else it generates — the securityContext, the rollout
strategy, the probe timings, the scrape annotations — keeps applying, and
keeps getting better as the conventions improve.

`overrides` still exists and replaces a whole file. It also opts that
service out of every future convention change for that file, silently and
permanently, so reach for it only when a merge genuinely cannot express
what you need.
