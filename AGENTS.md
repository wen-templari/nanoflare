# Release management

Release Please manages public release versions after every push to `main`.

- Use Conventional Commit messages for user-facing changes. `fix:` creates a patch
  release and `feat:` creates a minor release; non-conventional commits do not
  create a release PR.
- Core Go binaries, `@nanoflare/cli`, and every platform-specific CLI package
  share the root `VERSION` value and release with `vX.Y.Z` tags. Do not manually
  change just one of these package versions.
- `@nanoflare/vite-plugin` releases independently with
  `vite-plugin-vX.Y.Z` tags. Changes that should release it must be under
  `packages/vite-plugin/`.
- Release Please opens a release PR. Merging that PR creates the GitHub release
  and tag, which trigger the existing Go and npm publishing workflows. Do not
  manually create a replacement release tag once Release Please is active.
- The workflow requires the repository secret `RELEASE_PLEASE_TOKEN`; it must
  be a GitHub App token or bot PAT rather than the default `GITHUB_TOKEN` so
  downstream publishing workflows are triggered.
