# go-service

A web service written in Go. This is the default, and the right choice if
you are unsure.

```sh
homelabctl init myservice --runtime go-service
```

## What you get

A working HTTP service that deploys to the cluster, plus a ready-made
client library that other services can import instead of hand-writing HTTP
calls.

```
myservice/
  openapi.yml     your API, described once
  main.go         starts and stops the program
  server.go       your handlers — this is where you work
  api/            generated from openapi.yml; do not edit
  clients/ts/     a TypeScript client, generated
  config.yaml     how this service is deployed
  deploy/         Kubernetes manifests, generated
  Dockerfile      how the container is built
```

Out of the box it already has:

- **A health check** at `/healthz`, which Kubernetes uses to tell whether
  your service is alive.
- **Metrics** at `/metrics`, scraped automatically and visible in Grafana.
- **Structured logs** with the service name, team and version on every
  line, so they are searchable in Loki.
- **Graceful shutdown** — in-flight requests finish before the pod stops.
- **A container build and deploy pipeline**, already wired up.

You do not have to set any of that up.

## Working on it

```sh
cd myservice
go test ./...
go run .            # listening on port 3000
```

In another terminal:

```sh
curl localhost:3000/healthz
```

If you installed [wgo](https://github.com/bokwoon95/wgo), use `wgo run .`
and it restarts every time you save.

## Spec-first: how to add an endpoint

Your API is described in `openapi.yml`, and the Go types and routing are
generated from that description. **You change the API by editing
`openapi.yml`, not by editing Go.**

To add an endpoint:

**1.** Add it to `openapi.yml`:

```yaml
paths:
  /things/{id}:
    get:
      operationId: getThing
      summary: Fetch one thing.
      parameters:
        - name: id
          in: path
          required: true
          schema: { type: string }
      responses:
        '200':
          description: The thing.
          content:
            application/json:
              schema:
                type: object
                properties:
                  id: { type: string }
                  name: { type: string }
```

**2.** Regenerate:

```sh
homelabctl regen
```

**3.** Build. It will fail:

```
service does not implement api.Handler (missing method GetThing)
```

**4.** Write that method in `server.go`. Now it compiles.

That failure is the feature. Your code cannot drift from your API
documentation, because it will not build if it does.

### Bump the version when the API changes

`openapi.yml` has an `info.version`. When you change the API, bump it and
run `homelabctl regen`. That version is what the generated clients report,
and CI uses it to decide when to publish a release.

## Which files are yours

This matters — some files get rewritten and your edits would be lost.

| file | yours to edit? |
|---|---|
| `server.go` | **yes** — this is where your code goes |
| `main.go` | **yes** — startup, shutdown, extra listeners |
| `openapi.yml` | **yes** — this is your API |
| `config.yaml` | **yes** — how it deploys |
| `Dockerfile`, CI, `.gitignore` | yours after the first generation |
| `api/` | **no** — `homelabctl regen` rewrites it |
| `clients/ts/` | **no** — `homelabctl regen` rewrites it |
| `deploy/` | **no** — `homelabctl render` rewrites it |

Because the Dockerfile and CI are generated once and then left alone, a
later improvement to homelabctl's conventions will not reach them
automatically.

## No spec, if you do not want one

For a webhook receiver or anything with one or two endpoints, a full API
description is overhead:

```sh
homelabctl init myservice --runtime go-service --no-spec
```

You then write routes by hand in `server.go`, and there is no
`openapi.yml`, no `api/` directory and no generated client. Everything
else — health check, metrics, logging, deploy pipeline — is the same.

`homelabctl regen` on a service like this just says there is nothing to
regenerate.

Pick a spec if other services will call yours. Pick `--no-spec` if nothing
will.

## The client other people use

Because your API is described, anyone calling your service gets a
generated client rather than hand-written HTTP. It already handles
timeouts, retries and giving up on a service that is down.

**In Go:**

```go
import "github.com/owner/myservice/api"

c, _ := api.NewClient(url, api.WithClient(api.NewHTTPClient(api.HTTPOptions{
    Name: "my-caller",   // shows up in your service's logs
})))
thing, err := c.GetThing(ctx, api.GetThingParams{ID: "abc"})
```

`HTTPOptions` carries the defaults — a 5 second timeout and one retry —
so even an empty one is doing work. Setting `Name` is worth the extra
line: your service logs it on every request, so you can tell who is
calling. A caller that leaves it out is logged as `unknown`.

They install it with `go get github.com/owner/myservice@v0.2.0`.

**In TypeScript:**

```sh
npm install @owner/myservice-client
```

```ts
import createClient from '@owner/myservice-client'

const client = createClient({ baseUrl: 'https://myservice.example.com' })
```

The first TypeScript release is published by hand — `homelabctl init`
prints the two commands. After that CI publishes each new version
automatically when you bump `info.version`.

## Deploying

Covered in the [main guide](../../README.md#quickstart-your-first-service).
In short: push, then once ever run

```sh
homelabctl register
```

and merge the PR it opens. Every push after that deploys itself.
