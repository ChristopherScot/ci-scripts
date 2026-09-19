# node-service

A web service written in TypeScript, running on
[Fastify](https://fastify.dev/).

```sh
homelabctl init myservice --runtime node-service
```

Pick this over `go-service` when you or your team would rather write
TypeScript, or when the service needs a library that exists in the npm
ecosystem and not in Go.

## What you get

```
myservice/
  server.ts          your routes — this is where you work
  server.test.ts     tests
  package.json       dependencies and scripts
  tsconfig.json      TypeScript settings
  vite.config.ts     how the production bundle is built
  config.yaml        how this service is deployed
  deploy/            Kubernetes manifests, generated
  Dockerfile         how the container is built
```

Already working, with no setup from you:

- **A health check** at `/healthz`, which Kubernetes uses to tell whether
  your service is alive.
- **Metrics** at `/metrics`, scraped automatically and visible in Grafana.
- **Structured logs** carrying the service name, team and version, in the
  same shape a `go-service` emits — so one Loki query finds either.
- **Graceful shutdown**.
- **A container build and deploy pipeline**, already wired up.

Three routes are scaffolded: `/healthz`, `/metrics`, and `/` which returns
the service name.

## Working on it

```sh
cd myservice
npm install
npm start
```

Then in another terminal:

```sh
curl localhost:3000/healthz
```

| command | what it does |
|---|---|
| `npm start` | run the service |
| `npm run dev` | run it, rebuilding and restarting on every save |
| `npm run dev:fast` | restart on save without rebuilding — quicker, less like production |
| `npm test` | run the tests |
| `npm run typecheck` | check types without running |

### TypeScript with no build step

`npm start` runs `node server.ts` directly. Node 22 strips the types as it
loads the file, so there is no compile step and no `dist/` directory while
you are working.

**Stripping is not checking.** Node ignores your types rather than
verifying them, so a type error will not stop the service from starting.
`npm run typecheck` is what actually checks them, and CI runs it before
the tests.

This is why the tooling table in the main guide asks for **Node 22 or
newer**. On an older Node, `node server.ts` simply fails.

## Adding a route

Open `server.ts` and add it:

```ts
app.get('/things/:id', async (request, reply) => {
  const { id } = request.params as { id: string }
  return reply.send({ id, name: 'a thing' })
})
```

Fastify infers the types of `request` and `reply` from the route, so you
rarely need to annotate them.

Then add a test in `server.test.ts`:

```ts
test('returns a thing', async () => {
  const res = await app.inject({ method: 'GET', url: '/things/abc' })
  assert.equal(res.statusCode, 200)
})
```

`app.inject()` calls your routes directly without opening a port, so the
tests are fast and need no cleanup.

## Which files are yours

| file | yours to edit? |
|---|---|
| `server.ts` | **yes** — this is where your code goes |
| `server.test.ts` | **yes** |
| `package.json` | **yes** — add dependencies here |
| `config.yaml` | **yes** — how it deploys |
| `tsconfig.json`, `vite.config.ts` | yours, but rarely need changing |
| `Dockerfile`, CI, `.gitignore` | yours after the first generation |
| `deploy/` | **no** — `homelabctl render` rewrites it |

Nothing in a `node-service` is regenerated after `init`. There is no
`homelabctl regen` step here — that command is for services with an
`openapi.yml`.

## What ships is not quite what you run

Worth knowing, because it occasionally explains a surprise.

Locally, `node server.ts` runs your source. The container runs a bundled
version built by Vite instead — one file, with the dependencies inlined,
so the image needs no `node_modules`.

CI runs the tests against the **bundle**, not just against your source, so
a difference between the two shows up in CI rather than in the cluster.
If something works locally and fails in the container, this is the first
thing to suspect.

## Adding dependencies

Normally:

```sh
npm install some-package
```

Commit both `package.json` and `package-lock.json`. The container build
uses the lockfile, so a dependency that is not in it will not be
installed.

## No generated client

Unlike a spec-first `go-service`, a `node-service` does not publish a
client library for other services to import. Callers write their own HTTP
requests.

If you want other services to have a generated client, use `go-service`
instead.

## Deploying

Covered in the [main guide](../../README.md#quickstart-your-first-service).
In short: push, then once ever run

```sh
homelabctl register
```

and merge the PR it opens. Every push after that deploys itself.
