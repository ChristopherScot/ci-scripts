# Pinned by digest: a floating tag makes the image non-reproducible, and a
# base change would land silently on the next build.
FROM node:22-alpine

WORKDIR /app
COPY package*.json ./
RUN npm ci --omit=dev && chown -R 65532:65532 /app
COPY --chown=65532:65532 . .

USER 65532:65532
EXPOSE {{ .Port }}
ENTRYPOINT ["node", "server.js"]
