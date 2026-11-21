# Module catalog (contract v1)

The KAS is sold by modules. The catalog is fixed in code
(`internal/configstore/modules.go`); which tenant contracted which module is
configuration, changed only by the administrator. Spec: "KAS por módulos:
catálogo e segunda empresa", sections 7 and 8.1 (decisions D1–D5 approved on
2026-11-20).

## Catalog

| Module | State | Capabilities | Depends on |
| --- | --- | --- | --- |
| `scheduling` | activatable | `scheduling:read`, `scheduling:compute`, `scheduling:write` | — |
| `attribution` | activatable | `attribution:import`, `attribution:read` | — |
| `reports` | activatable | `reports:read` | `attribution` |
| `process` | activatable (legacy-open) | `process:define`, `process:run`, `process:task`, `process:command`, `process:event` | — |
| `crm`, `omnichannel`, `ai` | planned (cannot be contracted) | — | — |

`config:read`, `audit:*` and `events:*` belong to the core and are never gated.

## Configuration

```json
"modules": {
  "escola-demo": {
    "attribution": { "status": "active" },
    "reports":     { "status": "active" }
  }
}
```

`status` is `active` or `paused`. The configuration is refused at startup when it:

- names an unregistered tenant, an unknown module or a planned module;
- contracts a module without its dependency, or keeps a module active while a
  dependency is paused;
- gives a service a module capability for a tenant that did not contract the
  module (active or paused).

A tenant **without** a `modules` block keeps today's behavior: `process` stays
open (legacy-open), and attribution/reports are absent. A tenant **with** a block
must list `process` to use it.

## Gate

Every route checks, in order: tenant registered and active → service authorized
for the tenant → capability held → module of the capability active. The new
refusals are:

| HTTP | Code | When |
| --- | --- | --- |
| 403 | `module_not_contracted` | the tenant did not contract the module |
| 403 | `module_paused` | the module is paused; data is kept, use is refused |

Each module refusal is written to the tenant's audit trail (when audit is
enabled): actor `system.kas-core`, action `module.access_denied`, result
`denied`, resource `module:<key>`, entity `capability` / `<capability>`.
Identifiers only.

## `GET /internal/v1/modules`

Any authenticated service of an active tenant it is authorized for. Only the
`tenant_id` parameter is accepted (required for a multi-tenant service).

```json
{ "contract_version": "v1", "tenant_id": "escola-demo", "config_version": "pilot-2026-11-21",
  "modules": [
    { "key": "attribution", "contract": "active",
      "integration": { "ready": false, "missing": ["receipts"] },
      "caller_capabilities": ["attribution:import"] },
    { "key": "reports", "contract": "active", "depends_on": ["attribution"] },
    { "key": "crm", "contract": "planned" } ] }
```

`integration` appears only for an active or paused module that registered a
readiness check (Attribution does, in its own change). `missing` lists what is
still needed; empty means ready.

## State changes on the outbox (`-modules-store postgres`)

At startup the KAS compares the configured states with the last applied ones
(`module_states`). Each change writes `module_state_history` and publishes, in
the same transaction, one event from service `kas-core`:

| Change | Event type |
| --- | --- |
| to `active` | `module.activated` |
| to `paused` | `module.paused` |
| removed from the block | `module.removed` |

Payload: `{"module": "<key>", "status": "<to>", "previous_status": "<from>"}`.
Event id: `module-state-<history id>`. Running again with the same configuration
changes nothing and publishes nothing. Declare the types in `event_types` with a
consumer to receive them. Requires `-events-store postgres` (same database);
migrations with `-modules-migrate-only`, runtime grants in
`deploy/grants-modules.sql` (history is INSERT/SELECT only).
