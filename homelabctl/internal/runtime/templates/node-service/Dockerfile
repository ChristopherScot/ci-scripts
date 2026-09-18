# Two stages so the shipped image has no npm, no shell and no build
# tools. Matches the go-service template, which ships distroless for the
# same reason.
#
# distroless/nodejs24-debian12:nonroot already runs as uid 65532 - the
# uid the generated securityContext expects - so there is no adduser step
# to drift out of sync, and ca-certificates are included for outbound
# HTTPS.
#
# Neither stage is pinned by digest. A floating tag means a base change
# lands on the next build rather than when you choose it; pin both FROMs
# to a digest if that matters more than picking up patch releases.
FROM node:24-alpine AS build

WORKDIR /repo

# The REPOSITORY root is the build context, not this service's directory.
#
# That is what lets a service depend on a generated client in a sibling
# directory - `file:../<api>/clients/ts` - which is what makes extending
# the API and using the extension a single commit rather than two PRs
# with a publish in between. A context of this directory alone could not
# see the sibling at all.
COPY . .

WORKDIR /repo/{{ .ServiceDir }}

# Dev dependencies included, unlike a plain runtime install: the bundler
# and the TypeScript compiler are both devDependencies, and neither ends
# up in the image.
RUN npm ci

# One file out. Vite inlines every dependency (ssr.noExternal), so the
# runtime stage needs no node_modules at all - which is also why the
# sibling client's symlink never reaches the container, the thing that
# broke when this used an npm workspace.
RUN npm run build

FROM gcr.io/distroless/nodejs24-debian12:nonroot

# Stamped into the image so every log line says which build produced it.
# CI passes --build-arg VERSION=$GITHUB_SHA; a local build gets "dev".
ARG VERSION=dev
ENV VERSION=$VERSION

WORKDIR /app
# Just the bundle. No node_modules, no source, no package.json - there is
# nothing else the process needs.
COPY --from=build --chown=65532:65532 /repo/{{ .ServiceDir }}/dist/server.js ./

USER 65532:65532
EXPOSE {{ .Port }}
# The distroless nodejs image's entrypoint is already `node`.
CMD ["server.js"]
