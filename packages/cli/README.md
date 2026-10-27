# Nanoflare CLI

Private dynamic namespace targets can use
`nanoflare check --runtime-profile private-dynamic` for advisory compatibility
warnings and `privateDynamicProfile` from `@nanoflare/cli/vite` alongside the
Cloudflare Vite plugin. See
[runtime differences and diagnostics](../../docs/guides/private-dynamic-runtime.md).

Install the Nanoflare command-line interface:

```sh
npm install -g @nanoflare/cli
nanoflare --help
```

Or run it without a global install:

```sh
npx @nanoflare/cli --help
```

The package downloads only the native binary matching your operating system and CPU architecture.
