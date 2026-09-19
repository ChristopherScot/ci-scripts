<p align="center">
  <img src="assets/logo.png" alt="Platy the Platypus" width="200">
</p>

<h1 align="center">homelabctl</h1>

<p align="center">Create a service, and get it running in the cluster.</p>

---

Writing a small web service is the easy part. Getting it *running* —
containerized, deployed, reachable at a hostname, with its secrets and its
monitoring — is a dozen files of Kubernetes YAML that all have to agree
with each other.

`homelabctl` writes those files for you. You describe the service once, in
a short config file, and it generates the code scaffold, the container
build, the deployment manifests and the CI pipeline.

**New to all this?** Start at [What the pieces
are](#what-the-pieces-are). Otherwise skip to [Quickstart](#quickstart-your-first-service).

## Contents

- [What the pieces are](#what-the-pieces-are)
- [Install](#install)
- [Quickstart: your first service](#quickstart-your-first-service)
- [The four kinds of thing you can make](#the-four-kinds-of-thing-you-can-make)
- [Changing your service](#changing-your-service)
- [Adding secrets](#adding-secrets)
- [Putting it on the internet](#putting-it-on-the-internet)
- [Scheduled jobs](#scheduled-jobs)
- [Adding a database](#adding-a-database)
- [Command reference](#command-reference)
- [When something goes wrong](#when-something-goes-wrong)

## What the pieces are

Skip this if the words below are already familiar.

**Kubernetes** runs containers across a group of machines. You do not
start a program on a particular server; you hand Kubernetes a description
of what you want running, and it makes that true and keeps it true.

**A manifest** is one of those descriptions — a YAML file saying "run this
container image, call it `myservice`, give it this much memory." A working
service needs several: one for the workload, one for the network address,
one for the public hostname, and so on. These are what `homelabctl`
generates.

**A container image** is your program plus everything it needs to run,
built once and runnable anywhere. Your service's CI builds one on every
push.

**ArgoCD** watches a git repository and makes the cluster match it. You do
not deploy by running a command against the cluster — you commit, and Argo
notices. This is called GitOps, and it means git history *is* the deploy
history.

**The homelab repo** is that repository. Registering your service there is
the step that makes Argo start deploying it.

**Vault** stores secrets. Your service never has a password in its code or
its config — it names a path in Vault, and the secret is injected as an
environment variable when the container starts.

Put together, the chain looks like this:

```
you push to main
  → CI builds a container image and pushes it
  → ArgoCD notices the new image
  → ArgoCD updates the cluster
  → your new code is running
```

You do not touch the cluster directly at any point.

## Install

Download the binary for your machine and put it somewhere on your `PATH`:

```sh
# macOS, Apple Silicon
curl -sL https://github.com/ChristopherScot/ci-scripts/releases/latest/download/homelabctl_darwin_arm64.tar.gz | tar xz
mv homelabctl ~/bin/

# macOS, Intel
curl -sL https://github.com/ChristopherScot/ci-scripts/releases/latest/download/homelabctl_darwin_amd64.tar.gz | tar xz
mv homelabctl ~/bin/

# Linux
curl -sL https://github.com/ChristopherScot/ci-scripts/releases/latest/download/homelabctl_linux_amd64.tar.gz | tar xz
mv homelabctl ~/bin/
```

Check it works:

```sh
homelabctl version
```

**"command not found"** means the directory you moved it to is not on your
`PATH`. Add it to your shell config:

```sh
echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

**macOS: "cannot be opened because the developer cannot be verified"** —
macOS quarantines downloaded binaries. Clear it:

```sh
xattr -d com.apple.quarantine ~/bin/homelabctl
```

Later versions install themselves:

```sh
homelabctl update
```

Tab completion, optionally:

```sh
homelabctl completion install
```

### What else you need

Install these before creating your first service. You do not need all of
them for every kind of service — the table after this says which.

| Tool | What it is | Install |
|---|---|---|
| [Go](https://go.dev/doc/install) | the language most services here are written in | go.dev/doc/install |
| [Node.js 22+](https://nodejs.org/en/download) | JavaScript runtime; comes with `npm` | nodejs.org/en/download |
| [git](https://git-scm.com/downloads) | version control | git-scm.com/downloads |
| [gh](https://cli.github.com/) | GitHub's command line tool | cli.github.com |
| [kubectl](https://kubernetes.io/docs/tasks/tools/) | talks to the Kubernetes cluster | kubernetes.io/docs/tasks/tools |

Node must be **version 22 or newer** — check with `node --version`.

Which you need:

| If you are making | You need |
|---|---|
| a Go service or Go tool | Go |
| a Go service with an API spec | Go and Node |
| a Node service | Node |
| anything at all | git, gh, kubectl |

Sign in to GitHub once, so `homelabctl` can create repositories for you:

```sh
gh auth login
```

**Optional but nice:** [wgo](https://github.com/bokwoon95/wgo) restarts a
Go service every time you save a file.

```sh
go install github.com/bokwoon95/wgo@latest
```

You do **not** need to install anything else. A Node service's
dependencies arrive with `npm install`, and the code generators run
on demand.

## Quickstart: your first service

This makes a working HTTP service, gets it into GitHub, and puts it in the
cluster. Ten minutes, most of it waiting for CI.

### 1. Create it

```sh
homelabctl init hello --runtime go-service
```

It asks for your GitHub owner if it cannot work it out, shows you what it
is about to do, and waits for you to confirm. It then creates the GitHub
repo, writes the files, and prints a list of next steps.

You now have a directory called `hello`:

```
hello/
  openapi.yml     what your API looks like
  main.go         starts and stops the program
  server.go       your actual code goes here
  api/            generated from openapi.yml — do not edit
  config.yaml     what this service is, for homelabctl
  deploy/         the Kubernetes manifests, generated
  Dockerfile      how the container gets built
```

**If the last thing it prints is a `WARNING:` about dependencies**, the
files were written but Go could not fetch them — usually a network
blip. Nothing is broken; just run what it tells you:

```sh
cd hello && go mod tidy
```

Until you do, `go build` and `go run` will fail, because Go will not
build without a `go.sum`.

### 2. Run it

```sh
cd hello
go test ./...
go run .
```

It is now listening on port 3000. In another terminal:

```sh
curl localhost:3000/healthz
```

If you installed `wgo`, use `wgo run .` instead of `go run .` and it
restarts whenever you save.

### 3. Write some code

Open `server.go`. That is where your handlers live.

A `go-service` is **spec-first**: `openapi.yml` describes your API, and
the Go types and routing are generated from it. To add an endpoint, add it
to `openapi.yml` first, then:

```sh
homelabctl regen
```

Now `go build` will fail, telling you a method is missing:

```
service does not implement api.Handler (missing method GetThing)
```

That is intentional. Write the method in `server.go` and it compiles. Your
code cannot disagree with your API documentation, because it will not
build if it does.

### 4. Push it

```sh
git add -A
git commit -m "hello: first version"
git push
```

CI builds the container image. Watch it:

```sh
gh run watch
```

### 5. Deploy it

```sh
homelabctl register
```

This opens a pull request against the homelab repo. Merge it, and ArgoCD
starts deploying your service. Every push to `main` from then on deploys
itself — you never run this again.

It prints the PR's URL. If your service is already registered it says so
and does nothing, which is safe to re-run. Anything else — an expired
GitHub token, no network — is reported as an error with the reason.

If it says **"this service has no git remote yet"**, you skipped step 4:
commit and push first, then run this again.

### 6. Check it is running

```sh
homelabctl status          # what Argo made of it
kubectl get pods -n hello  # the pod itself
```

You want `Synced / Healthy` from the first and `Running` from the
second. If you do not get both, jump to [When something goes
wrong](#when-something-goes-wrong).

## The four kinds of thing you can make

Pick with `--runtime` when you run `init`. Each has its own guide with
more detail:

| runtime | what it is | guide |
|---|---|---|
| `go-service` | a Go web service | [docs/runtimes/go-service.md](docs/runtimes/go-service.md) |
| `node-service` | a TypeScript web service | [docs/runtimes/node-service.md](docs/runtimes/node-service.md) |
| `go-cli` | a command-line tool | [docs/runtimes/go-cli.md](docs/runtimes/go-cli.md) |
| `go-tui` | a terminal app | [docs/runtimes/go-tui.md](docs/runtimes/go-tui.md) |

### `go-service` — a Go web service

The default, and the one to pick if you are unsure.

```sh
homelabctl init myservice --runtime go-service
cd myservice
go run .
```

Your API is described in `openapi.yml` and the Go code is generated from
it. After any change to that file, run `homelabctl regen`.

This also gives anyone who wants to call your service a ready-made client
library, in both Go and TypeScript, so they do not have to hand-write HTTP
requests.

**If you do not want a spec** — a webhook receiver, something with one
endpoint — use `--no-spec`:

```sh
homelabctl init myservice --runtime go-service --no-spec
```

You then write your routes by hand in `server.go`, and there is no
`openapi.yml` and no generated client.

More: [docs/runtimes/go-service.md](docs/runtimes/go-service.md).

### `node-service` — a TypeScript web service

```sh
homelabctl init myservice --runtime node-service
cd myservice
npm install
npm start
```

Useful scripts:

| command | does |
|---|---|
| `npm start` | run it |
| `npm run dev` | run it, restarting on every save |
| `npm test` | run the tests |
| `npm run typecheck` | check the types without running |

More: [docs/runtimes/node-service.md](docs/runtimes/node-service.md).

### `go-cli` — a command-line tool

Not deployed to the cluster. Compiled for macOS and Linux and published as
a downloadable release.

```sh
homelabctl init mytool --runtime go-cli
cd mytool
go run . --help
```

To release a version, edit the `VERSION` file and push. CI builds it for
every platform and publishes it. Your users install updates with
`mytool update` — that command is generated for you.

More: [docs/runtimes/go-cli.md](docs/runtimes/go-cli.md).

### `go-tui` — a terminal app

Like `go-cli`, but it draws a full-screen interface instead of printing
and exiting.

```sh
homelabctl init mytui --runtime go-tui
```

Same release process as `go-cli`.

More: [docs/runtimes/go-tui.md](docs/runtimes/go-tui.md).

## Changing your service

Everything about how your service is deployed lives in `config.yaml`. Edit
that file, then:

```sh
homelabctl render
```

That regenerates the manifests in `deploy/`. Commit and push, and Argo
applies them.

A minimal config:

```yaml
name: hello
team: me-myself-and-i
runtime: go-service
image:
  repository: ghcr.io/yourname/hello
```

Everything you can set:

| setting | default | what it does |
|---|---|---|
| `name` | required | the service's name; lowercase letters, digits and hyphens |
| `team` | required | who owns it |
| `runtime` | required | which kind of service this is |
| `port` | `3000` | the port your program listens on |
| `replicas` | `1` | how many copies to run |
| `namespace` | the name | which Kubernetes namespace it lives in |
| `env` | — | environment variables, literal or read from a secret |
| `secrets` | — | secrets from Vault; see below |
| `ingress` | — | a public hostname; see below |
| `probes.path` | `/healthz` | the URL Kubernetes checks to see if you are alive |
| `resources` | filled in for you | how much CPU and memory to request |
| `kind` | `service` | set to `cronjob` to run on a schedule |
| `manifests` | — | extra Kubernetes files you wrote yourself |
| `minVersion` | none | turn away clients older than this; see below |

### Turning away an old client

If a released client is doing harm — a browser tab polling forever on
code that predates a fix — `minVersion` makes the service refuse it:

```yaml
minVersion: v0.3.0
```

Anything reporting an older `Client-Version` gets `410 Gone`, which the
generated clients do not retry. Callers that send no version at all,
like `curl` or the kubelet, are still served.

You will rarely set this. It means "older than this is actively
harmful", not "the current version", so it should trail your real
releases by a long way.

`init` writes sensible `resources` into your `config.yaml` — a Node
service gets more memory than a Go one, because it needs it. Raise
`memoryLimit` if your pod is being restarted with `OOMKilled`.

Two more settings exist — `patches` and `overrides` — for adjusting the
generated manifests by hand. You are unlikely to need them; ask before reaching for
`overrides`, which opts your service out of future improvements.

⚠️ **A `patches:` entry naming a list replaces that list rather than
merging into it.** Patching `containers` to add one field therefore
deletes every other field on the container — its image included. To add
an environment variable, use `env:` above, which merges. `render`
refuses a patch that would drop a field and names what was lost.

After rendering, check the result at any time:

```sh
homelabctl check
```

It inspects the generated `deploy/` directory and prints `deploy
manifests OK`, or lists what is wrong.

## Adding secrets

Never put a password or API key in `config.yaml` — it is committed to git.
Put it in Vault and name it:

```yaml
secrets:
  vaultPath: hello/config
  keys:
    - API_TOKEN
    - DATABASE_URL
```

Each key becomes an environment variable in your container. `API_TOKEN`
reads the property `api_token` at that Vault path.

If the Vault property is not named after the variable, map it:

```yaml
keys:
  - API_TOKEN: legacy_token_name
```

A key can also read from a different service's Vault path, for a
credential that belongs to something else and is shared rather than
reissued:

```yaml
keys:
  - SHARED_TOKEN: other-service/config/api_token
```

After adding secrets, give your service permission to read that path:

```sh
homelabctl vault --apply
```

Run that from the service directory. Without it, the service starts,
cannot read its secrets, and fails in a way that is not obvious from the
logs.

To see what it would do without doing it, leave off `--apply`.

### A credential another tool created

Some credentials are not in Vault because something else mints them. A
database created with `manifests:` writes its own connection details
into a Kubernetes secret, and rotates them there — copying that into
Vault would be a second copy to go stale.

Name the secret and the key directly:

```yaml
env:
  DATABASE_URL:
    secretKeyRef:
      name: hello-db-app
      key: uri
```

`env:` still takes plain values too; both forms live in the same block:

```yaml
env:
  LOG_LEVEL: debug
  DATABASE_URL:
    secretKeyRef: { name: hello-db-app, key: uri }
```

Use `secrets:` for anything you put in Vault yourself, and this for
anything another tool put in a Kubernetes secret.

Either way, your pod restarts automatically when the secret changes —
an environment variable is read once when the container starts, so a
rotated password would otherwise never reach a running pod.

## Putting it on the internet

By default your service has no hostname — it is reachable only from inside
the cluster. To give it one:

```yaml
ingress:
  hosts:
    - hello.example.com
```

Then `homelabctl render`, commit, push.

| setting | what it does |
|---|---|
| `hosts` | the hostnames it answers on |
| `public` | `true` routes from the internet; otherwise LAN only |
| `authelia` | `true` puts a login page in front. LAN only |
| `path` | serve under a prefix like `/api` instead of `/` |

A public hostname:

```yaml
ingress:
  hosts:
    - hello.example.com
  public: true
```

HTTPS certificates are handled for you. A hostname ending in `.lab`,
`.local` or `.internal`, or one with no dots, cannot get a public
certificate and is set up without one automatically.

### One name on the LAN, another on the internet

`public` can be set per host instead, which is what you want when only
some of your hostnames should face the internet:

```yaml
ingress:
  hosts:
    - hello.home.example.com          # LAN only
    - name: hello.example.com         # reachable from the internet
      tls: true
      public: true
```

A host with no `public` of its own follows `ingress.public`, so the
simple form above still means what it always did.

This renders two Ingress objects — one per controller — and the public
one is always named `<service>-public`, so you can tell at a glance
which is which.

Behind a login page, LAN only:

```yaml
ingress:
  hosts:
    - hello.lab
  authelia: true
```

`authelia` cannot cover a public host — the login page only resolves on
the LAN, so an off-LAN request would dead-end. `render` refuses and
names the host it objects to.

## Scheduled jobs

To run on a schedule instead of continuously:

```yaml
kind: cronjob
schedule: "0 3 * * *"
timeZone: America/New_York
```

`schedule` is standard cron syntax — the example is 3am daily.

**Always set `timeZone`.** Without it Kubernetes reads the schedule in
UTC, so a job you wrote for 3am runs at 11pm the previous evening.

A cronjob gets no hostname and no health checks — it runs, finishes, and
waits for the next time.

## Adding a database

Databases are not generated, because they hold data and their setup varies.
Write the file yourself and list it:

```yaml
manifests:
  - db.yaml
```

Put `db.yaml` next to `config.yaml`, not in `deploy/`. `render` copies it
to `deploy/<name>/manifests/` and applies it alongside everything else.
Name it whatever you like — your files live in their own directory, so
they cannot clash with the generated ones.

Every file you list must exist and be a real Kubernetes manifest — one
with `apiVersion` and `kind`. `render` refuses otherwise rather than
quietly leaving the resource out.

⚠️ **Anything holding data needs this annotation**, or removing it from the
list later will delete it:

```yaml
metadata:
  annotations:
    argocd.argoproj.io/sync-options: Prune=false
```

Ask before setting up a database — there may be an existing one to use,
and the cluster has conventions for where they live.

## Command reference

Run these from inside your service directory.

| command | what it does |
|---|---|
| `homelabctl init <name>` | create a new service or tool |
| `homelabctl render` | regenerate `deploy/` after editing `config.yaml` |
| `homelabctl register` | open a PR so Argo starts deploying this service |
| `homelabctl regen` | regenerate code after editing `openapi.yml` |
| `homelabctl check` | look for problems before you push |
| `homelabctl diff` | show what `render` would change in `deploy/` |
| `homelabctl status` | ask Argo what it made of your service (needs cluster access) |
| `homelabctl status --all` | the same, for every service in the repo |
| `homelabctl vault --apply` | give your service access to its secrets |
| `homelabctl update` | update homelabctl itself |
| `homelabctl version` | print the version |

Useful `init` options:

| flag | what it does |
|---|---|
| `--runtime` | which kind of service (default `go-service`) |
| `--no-spec` | a Go service with no API spec |
| `--parent-repo` | add to an existing repo instead of making a new one |
| `--private` | make the GitHub repo private |
| `--dry-run` | show what it would do, and stop |
| `--local-only` | write the files, create nothing on GitHub |

Add `--help` to any command for the full list.

## When something goes wrong

### My pod is not running

```sh
kubectl get pods -n <your-namespace>
```

| what you see | what it means |
|---|---|
| `ImagePullBackOff` | the image is missing or private. Check CI finished, and that the package is public |
| `CrashLoopBackOff` | your program starts and immediately exits. See the logs |
| `OOMKilled` | it used more memory than its limit. Raise `memoryLimit` in `config.yaml`, then re-render |
| `CreateContainerConfigError` | a secret is missing. Did you run `homelabctl vault --apply`? |
| `Pending` | the cluster has no room, or a volume is not ready |

Read the logs:

```sh
kubectl logs -n <your-namespace> -l app=<your-service>
kubectl logs -n <your-namespace> -l app=<your-service> --previous   # the crashed one
```

Get the full story on a pod:

```sh
kubectl describe pod -n <your-namespace> <pod-name>
```

### Argo says Synced and Healthy, but my change is not live

Almost always the image never built. Check CI:

```sh
gh run list --limit 5
```

### I edited config.yaml and nothing happened

Run `homelabctl render`, then commit and push the changed files in
`deploy/`. Editing `config.yaml` alone changes nothing — the manifests are
what the cluster reads.

To check whether you have forgotten this, run `homelabctl diff`. It says
`up to date` when the manifests match your config, and otherwise shows
what changed, file by file.

### I added a file to deploy/ and it is not deployed

Argo applies exactly what `deploy/<name>/kustomization.yaml` lists, so a
file sitting in that directory unlisted is never applied. `homelabctl
check` reports it:

```
- stray.yaml is in deploy/gadget but not listed in
  kustomization.yaml, so Argo never applies it
```

Do not add it to `kustomization.yaml` by hand — `render` regenerates that
file and your edit would be lost. List the manifest in `config.yaml`
instead and re-render. See [Adding a database](#adding-a-database).

### I edited openapi.yml and the build broke

That is the design. Run `homelabctl regen`, then write the handler it says
is missing.

### CI fails with "out of date"

Something generated was not regenerated. Run both, then commit what
changes:

```sh
homelabctl regen
homelabctl render
```

### My service cannot read its secrets

```sh
homelabctl vault --apply
```

Run it from the service directory, after adding `secrets:` to
`config.yaml`.

### Still stuck

```sh
homelabctl check
homelabctl diff
```

`check` looks at the rendered `deploy/` directory for problems that
otherwise fail silently — a file Argo will never apply because nothing
lists it, a listed file that is missing, a leftover `CHANGEME` in a
generated file.

`diff` re-renders your config and compares it against the manifests
committed in `deploy/` — so if you edited `config.yaml` and forgot to run
`homelabctl render`, this is what tells you.

Between them: `diff` catches a stale `deploy/`, `check` catches a broken
one. Neither talks to the cluster — for that, use `homelabctl status`.

### Everything looks fine locally but it is still not running

`check` and `diff` both only look at files. A manifest can be perfectly
valid YAML and still be rejected by Kubernetes once Argo tries to apply
it — a hand-written database, say, with a setting the cluster refuses.

`homelabctl status` asks Argo directly. When everything is fine:

```
$ homelabctl status
pokedex: Synced / Healthy
  revision 5cfdcd2
  last sync succeeded: successfully synced (all tasks run)
```

And when it is not, it names the resource that failed rather than just
calling the whole service unhealthy:

```
$ homelabctl status
pokedex: OutOfSync / Degraded
  last sync failed: one or more objects failed to apply
  cluster/pokedex-db: OutOfSync / Degraded
    instance pokedex-db-1 is not ready
```

Healthy resources are not listed, so anything you see is something to
look at.

If your repo holds several services, `homelabctl status --all` reports
all of them.

It needs to reach the cluster, so run it from your own machine — it is
not something CI can do for you.
