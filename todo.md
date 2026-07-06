## Tool chain
- dev server ( bindings in dev env, bundling , typescript, run in workerd... )
- Generate type from worker config file (https://developers.cloudflare.com/workers/languages/typescript/)
- worker init

## Metrics
Worker
- bundle size
- cpu time
- requests per second
- request sub paths

KV
- read/write

Object storage
- read/write
- size

## Worker triggers 

https://developers.cloudflare.com/workers/configuration/cron-triggers/

## Env based deployments

support deploy to different envs (dev, staging, prod) with different config files.


## Design review

- should we keep using X-Nanoflare-* for kv namespace and object storagebucket?
****