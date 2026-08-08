# create-nanoflare

Create a Nanoflare Worker:

```sh
npm create nanoflare@latest my-worker
```

The initializer writes a minimal JavaScript Worker and `nanoflare.json`. It does
not install dependencies or contact a Nanoflare server.

```sh
cd my-worker
nanoflare create
nanoflare deploy
```

Pass `-- --template starter` to select a template explicitly, or `-- --help`
to see all options.
