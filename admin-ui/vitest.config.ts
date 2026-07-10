// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

export default defineConfig({
  resolve: {
    // Mirrors tsconfig.json's "@/*": ["./*"] — Next.js's own bundler reads
    // that directly, but vitest's Vite pipeline needs it spelled out here too.
    alias: { "@": fileURLToPath(new URL(".", import.meta.url)) },
  },
  // tsconfig.json sets "jsx": "preserve" (Next.js's own bundler applies the
  // React 19 automatic runtime); vitest's esbuild transform needs the same
  // told explicitly, or a .tsx file using JSX without an explicit React
  // import fails with "React is not defined".
  esbuild: { jsx: "automatic" },
  test: {
    environment: "node",
    // Auto-unmount rendered components after each test (jsdom files); harmless
    // for the node-env lib tests that never render.
    setupFiles: ["./vitest.setup.ts"],
    include: ["**/*.test.ts", "**/*.test.tsx"],
    exclude: ["node_modules/**", ".next/**"],
  },
});
