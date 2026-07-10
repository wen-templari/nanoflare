## Tool chain
- dev server ( bindings in dev env, bundling , typescript, run in workerd... )
- Generate type from worker config file (https://developers.cloudflare.com/workers/languages/typescript/)
- worker init

i want to add metrics data , similar to cloudflare worker, kv, store

## Worker
- invocations
- errors
- cpu time
- bundle size
- request sub paths
- request status code
- 
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

## Access control

add organizations: every resources should be org scoped ( worker/kv/storage ) , add user keep it simple only have id and email, add user / org relation table 