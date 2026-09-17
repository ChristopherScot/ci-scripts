# Two stages so the shipped image has no npm, no shell and no build tools:
# the builder installs dependencies, the runtime carries only node plus
# what was installed. Matches the go-service template, which ships
# distroless for the same reason.
#
# distroless/nodejs22-debian12:nonroot already runs as uid 65532 - the uid
# the generated securityContext expects - so there is no adduser step to
# drift out of sync, and ca-certificates are included for outbound HTTPS.
#
# Neither stage is pinned by digest. A floating tag means a base change
# lands on the next build rather than when you choose it; pin both FROMs
# to a digest if that matters more than picking up patch releases.
FROM node:24-alpine AS build

WORKDIR /app
COPY package*.json ./
# `npm ci` needs package-lock.json, which `homelabctl init` generates and
# CI regenerates. --omit=dev keeps devDependencies out of the image.
#
# mkdir -p guards the case where every dependency is a devDependency:
# npm then installs nothing, creates no node_modules, and the COPY below
# fails - making the service unbuildable for a reason that reads as a
# missing file rather than an empty install.
RUN npm ci --omit=dev && mkdir -p node_modules

FROM gcr.io/distroless/nodejs24-debian12:nonroot

# Stamped into the image so every log line says which build produced it.
# CI passes --build-arg VERSION=$GITHUB_SHA; a local build gets "dev".
ARG VERSION=dev
ENV VERSION=$VERSION

WORKDIR /app
COPY --from=build --chown=65532:65532 /app/node_modules ./node_modules
COPY --chown=65532:65532 . .

USER 65532:65532
EXPOSE {{ .Port }}
# The distroless nodejs image's entrypoint is already `node`.
CMD ["server.js"]
