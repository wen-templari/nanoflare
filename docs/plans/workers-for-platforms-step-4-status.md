# Step 4 — Namespace, script and grant model

Updated: 2026-10-18. Status: implemented and verified locally in the working tree. Not committed or deployed. This is the native control-plane/resource model for trusted execution; dynamic dispatch and a customer-cell runner are not implemented by this step.

## Resource contract

Organizations own dispatch namespaces. Names are unique within an organization; immutable namespace IDs are used in membership and grants. Namespace names may be renamed. Namespace trust is immutable and only `trusted` is accepted; requesting an untrusted profile returns 422. `customer_mode` is immutable: `organization` uses the organization as the customer boundary, while `platform` requires a customer ID for each customer script. Customer IDs are platform-assigned domain labels, not a new login or customer-management service.

Namespace scripts are ordinary immutable Worker IDs with additional membership metadata: namespace ID, script name, kind (`customer` or `platform`), private exposure, and optional customer ID. Script names are unique across the namespace, including different customers. The same script name may exist in a different namespace or organization. Names/membership, kind, customer and exposure are fixed at creation; use the existing deployment APIs to update a script's code, bindings and variables. Script renaming or membership transfer is not provided.

All namespace members initially have private exposure and need no hostname. Existing public Workers retain their hostnames and have no platform metadata. A public platform entry Worker can continue to be an existing public Worker; namespace-wide runtime bindings arrive in step 6. A namespace script's `platform` kind is a model classification, not an automatic runtime dispatch capability.

## Native API

Base: `/v1/organizations/{orgID}/dispatch-namespaces`.

| Method / suffix                                    | Behavior                                    | Required scope                         |
| -------------------------------------------------- | ------------------------------------------- | -------------------------------------- |
| GET / POST base                                    | List / create namespace                     | `namespaces:read` / `namespaces:write` |
| GET / PATCH / DELETE `/{namespaceID}`              | Read / rename / delete                      | `namespaces:read` / `namespaces:write` |
| PUT `/{namespaceID}/grants`                        | Replace grants with `expected_revision` CAS | `namespaces:dispatch`                  |
| GET `/{namespaceID}/policies`                      | Read immutable policy history               | `namespaces:read`                      |
| GET / POST `/{namespaceID}/scripts`                | List / create private script                | `workers:read` / `workers:write`       |
| GET / DELETE `/{namespaceID}/scripts/{scriptName}` | Resolve metadata / delete script            | `workers:read` / `workers:write`       |

Creating a script with kind `platform` additionally requires `namespaces:dispatch`. Ordinary deployment permissions do not imply namespace administration or dispatch authority. Default viewer/member roles receive namespace read access; default admin/owner roles also receive namespace write/dispatch permissions. Existing exact role-default membership/invite scope sets are upgraded; custom restrictions and previously issued PAT/OAuth scope grants are not widened. Reissue or explicitly update credentials to grant new scopes.

The control plane remains organization-scoped: an authorized platform operator can administer its customers' scripts and grants. Per-customer end-user authentication, customer-limited deployment tokens and runtime namespace dispatch are later integration concerns, not claimed by these org administration endpoints.

Namespace deletion returns 409 while scripts or resource grants remain. There is no implicit cascade. Delete scripts and clear grants explicitly before deleting the namespace. Deleting an empty namespace also removes its retained policy history. Worker IDs returned by script creation work with the existing Worker/deployment/secrets APIs.

Example creation bodies:

```json
{ "name": "production", "trust": "trusted", "customer_mode": "platform" }
```

```json
{ "script_name": "alpha", "kind": "customer", "customer_id": "customer-a" }
```

## Grants and policy revisions

Namespace grants identify a resource kind (`kv`, `db`, `objects`), immutable resource ID and exact customer ID. Empty customer IDs represent organization-mode resources or platform scripts; they are not wildcards. Resource ownership must match the namespace organization. Assets and secrets retain their existing exact Worker/deployment ownership rather than becoming namespace-wide grants.

Every grant replacement requires the current `expected_revision`, increments the namespace policy revision and appends an immutable grant snapshot in the same repository transaction/lock. Concurrent writers produce one winner and a 409 conflict. Grant changes do not silently update Worker bindings. Deployment validation requires every declared KV/database/object binding to be allowed for that script's namespace/customer, including calls through the generic deployment service.

Runtime authorization checks the exact deployment binding plus the current namespace/customer grant. Revocation therefore rejects new runtime calls immediately, including calls from still-live old revisions. Already-authorized effects may finish. Regranting a resource can reauthorize a live revision that already declared that binding; it does not add bindings to a stored deployment. Freezing additional namespace policy references into publication/invocation artifacts remains step 5/6 work; no new invocation lease system is claimed here.

## Private exposure and service targeting

Private scripts are excluded from Traefik routers and public gateway routes, including asset access. They receive no public TCP socket from the config generator. The runner may still load them behind its existing private Unix sockets; direct public gateway requests return 404. Hostname updates on a private script are rejected.

Service bindings can use `worker_id` for an immutable target. An explicit ID is required for private targets, which must belong to the same namespace/customer as the caller. This does not implement namespace-wide dispatch. Legacy public name-based service bindings remain supported when exactly one Worker in the organization matches. Multiple revisions of the same Worker do not constitute duplicate Worker identities.

Legacy duplicate display names are preserved, rather than arbitrarily renamed or assigned to namespaces. PostgreSQL exposes `dispatch_legacy_name_conflicts` for migration diagnostics. New service deployments reject ambiguous legacy names; config generation never chooses an arbitrary Worker from duplicate identities. Resolve a legacy conflict by using `worker_id` or changing the naming arrangement before relying on that binding.

## Storage and migration

Both in-memory and PostgreSQL repositories implement the model. PostgreSQL adds namespace/policy tables and nullable JSON platform metadata on Workers. A generated namespace ID column enforces namespace membership with foreign keys, including organization ownership, and an index enforces namespace/script uniqueness. Hostname uniqueness becomes a partial index for nonempty hostnames so several private scripts can coexist without hostnames. A check constraint prevents namespace members from acquiring a hostname. Migration DDL, indexes and role-default scope changes are transactional and repeatable.

Legacy Workers remain `platform = null`: no automatic namespace import, exposure change, host rewrite, deployment rewrite or runtime-token rotation. Migration tests compare original Worker identity and active deployment records before/after reconstructing and upgrading the prior schema. The duplicate-name diagnostics view records ambiguity without breaking unrelated legacy names.

Runner/control-plane binaries should be upgraded together; an older runner would not understand the private metadata. Use the normal migration/config regeneration workflow. No migration or rollout against a deployed environment has been performed here.

## Validation

Passed locally with an isolated PostgreSQL 17 instance and the installed native workerd executable on PATH:

- Full `go test ./... -count=1 -timeout=120s`.
- Race checks for API, database, domain, config, runtime, runner and daemon packages. The final namespace create/delete race was additionally tested in the full suite.
- The same namespace model/authorization matrix against memory and real PostgreSQL: org/customer separation, script uniqueness, hostname-free creation, policy history/CAS, immediate revocation, KV/database/object grants, private metadata, private gateway denial, duplicate-create races, and safe namespace create/delete ordering.
- API route scope guards, strict immutable-field rejection, grant authority separate from ordinary deployment permissions, and namespace dependency checks.
- Migration preservation of legacy identity, credentials, public hostname uniqueness and active revisions; duplicate legacy-name diagnostics; default membership permission upgrade with custom restrictions preserved; repeatable migration.
- Config tests for public route/TCP exclusion and retention of private Unix sockets.
- Regenerated OpenAPI/TypeScript contracts; schema `tsc --noEmit` and UI `tsc -b` passed.

The OpenAPI contract is generated from the native operation catalogue. No Cloudflare management API compatibility is claimed. CLI/console namespace workflows are deferred to the tooling step.

## Next step

Proceed to step 5: immutable artifact lookup and publication outcomes, using the stable namespace/script/Worker IDs and grant/policy records added here. Step 6 integrates real private dispatch and invocation lifetime. CPU enforcement remains in its separate workstream; hardened sandbox, egress, production budget/cell integration and untrusted release gates remain open.
