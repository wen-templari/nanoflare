## Tool chain
- dev server ( bindings in dev env, bundling , typescript, run in workerd... )
- Generate type from worker config file (https://developers.cloudflare.com/workers/languages/typescript/)
- worker init

## Deployments

- support deploy to different envs (dev, staging, prod) with different config files.
- host name: 
  - ${worker_name}-${org}.${base}
  - ${worker_name}-${hash}-${org}.${base}
  - ${worker_name}-${env}-${org}.${base}
  - ${custom_host_name}

## Access control

sso for control panel