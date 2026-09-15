# homelabctl

Scaffolds services and renders their Kubernetes manifests. One tool, one
config schema, one place that encodes the cluster's conventions.

```sh
homelabctl init myservice --runtime go-service --host myservice.example.com --public
homelabctl init mytool --runtime go-cli
homelabctl render config.yaml ghcr.io/owner/svc@sha256:...  --out .
homelabctl check deploy
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

Implement `runtime.Runtime` and call `Register` from an `init`. Nothing
else changes — manifests, the Argo Application, the image-updater
annotations and CI are identical across languages.

A runtime is usually **no Go code at all**: a directory of template files
under `internal/runtime/templates/<name>/`, plus one entry in
`registered.go`.

```go
Register(embedded{
    name: "python-service", dir: "python-service",
    deployable: true, hardened: true,
    files: map[string]string{
        "main.py.tmpl":    "main.py",
        "gitignore":       ".gitignore",
    },
})
```

The directory supplies `Dockerfile`, `steps.yaml` (the CI build steps) and
`workflow.yaml`, each rendered with the service's `Params`. Write Go only
if a runtime needs behaviour the templates cannot express.

Three ship today:

| runtime | kind | produces |
|---|---|---|
| `go-service` | service | image + manifests + Argo Application |
| `node-service` | service | image + manifests + Argo Application |
| `go-cli` | cli | cross-compiled release assets, with self-update |

A CLI is not deployed, so it gets no Dockerfile, no manifests and no Argo
Application. Its CI cross-compiles on a VERSION bump and publishes assets
named to match what its generated `update` command looks for.

## Things that fail silently, and what this tool does about them

Each of these presented as a `Synced/Healthy` app with nothing shipping:

| Failure | Guard |
|---|---|
| No `kustomization.yaml` → image-updater skips the app, since it can only write into Kustomize/Helm/Plugin sources | always rendered; `check` fails without it |
| Abbreviated image SHA → `ImagePullBackOff`, because short SHAs are not registry tags | `render` and `check` reject them |
| Missing `write-back-target` → the updater writes a separate `.argocd-source` file, so two files claim to set the image | always in the rendered Application |
| Private package → `Could not get tags from registry: unauthorized` | documented; make the package public or give the updater a credential |
| Root Dockerfile under a hardened pod → `permission denied`, no logs | runtimes build nonroot 65532 images matching the rendered securityContext |

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
