// The production build: one file, no node_modules.
//
// Locally this service runs `node server.ts` directly - Node strips the
// types, nothing is compiled, and the edit-run loop has no build step.
// The image runs a bundle instead, for one reason: inlining the
// dependencies means the container needs no node_modules, and THAT is
// what lets this service depend on a generated client in a sibling
// directory via `file:`. A symlinked sibling cannot be copied into an
// image; an inlined one does not need to be.
//
// The two paths are a real cost - what you run locally is not byte-wise
// what ships - so CI runs the BUNDLE, not just builds it.
import { defineConfig } from 'vite'

export default defineConfig({
  build: {
    ssr: true,
    // Matches the distroless runtime, so Vite does not downlevel syntax
    // the shipped Node can run natively.
    target: 'node22',
    outDir: 'dist',
    // The bundle is one file and nothing reads it but Node; a sourcemap
    // would double the image's only layer for a stack trace that still
    // points at inlined code.
    sourcemap: false,
    rollupOptions: {
      input: 'server.ts',
      output: {
        format: 'esm',
        entryFileNames: 'server.js',
        inlineDynamicImports: true,
      },
      // node: builtins stay external - they ARE the runtime.
      external: [/^node:/],
    },
  },
  ssr: {
    // Everything else is inlined. Without this Vite externalises
    // dependencies, the bundle is a few hundred bytes of imports, and
    // the image needs node_modules after all.
    noExternal: true,
  },
})
