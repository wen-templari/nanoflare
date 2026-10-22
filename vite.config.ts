import { defineConfig } from "vite-plus";

export default defineConfig({
  fmt: {
    // Qualification records are immutable evidence referenced by byte digests.
    ignorePatterns: ["scripts/wfp-*/**/*.json"],
    sortImports: {
      groups: [
        "type-import",
        ["value-builtin", "value-external"],
        "type-internal",
        "value-internal",
        ["type-parent", "type-sibling", "type-index"],
        ["value-parent", "value-sibling", "value-index"],
        "unknown",
      ],
    },
  },
  lint: {
    ignorePatterns: ["packages/create-nanoflare/templates/**"],
    jsPlugins: [{ name: "vite-plus", specifier: "vite-plus/oxlint-plugin" }],
    rules: { "vite-plus/prefer-vite-plus-imports": "error" },
    options: { typeAware: true, typeCheck: true },
  },
  run: {
    cache: true,
  },
  staged: { "*": "vp check --fix" },
});
