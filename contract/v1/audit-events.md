# KAS Audit Events contract v1

## Endpoint

`POST /internal/v1/audit/events`

Headers:

- `X-KAS-Service: <service-id>`
- `Authorization: Bearer <64-character hexadecimal token>`
- `Content-Type: application/json`

Authentication and tenant authorization use the same service allowlist, credentials and central `tenant_registry` as the configuration endpoint. The tenant must be registered, active and allowed for the authenticated service, and the service must explicitly have the `audit:write` capability. The body `service_id` must exactly match the authenticated `X-KAS-Service`; it cannot replace that identity. Registry state and capabilities are loaded at startup and changes require restart. Deactivating a tenant blocks new events but does not delete or alter previously stored events.

`callers.<service-id>.capabilities` accepts `config:read`, `audit:write`, `audit:read`, `events:publish` and/or `events:consume` (see [events.md](events.md)). An omitted or empty list grants no business capability and is denied by default. Unknown or duplicate capability names reject startup.

## Request

Maximum request body: 8 KiB. Unknown fields are rejected. No arbitrary payload or free-form metadata is accepted.

```json
{
  "contract_version": "v1",
  "event_id": "evt.demo-01",
  "tenant_id": "tenant-demo-a",
  "service_id": "fono-node-local",
  "actor": "user.demo-01",
  "action": "settings.updated",
  "resource": "setting.max_meta_keys",
  "result": "success",
  "correlation_id": "corr.demo-01",
  "occurred_at": "2026-11-16T12:00:00Z"
}
```

Two optional fields, `entity_type` and `entity_id`, identify the entity the action was about so records can be read back by entity. Send both or neither; both follow the identifier format (`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`) and must carry identifiers only. Existing emitters that omit them keep working unchanged.

All identifiers in this example are fictional. `actor` must use an opaque reference shaped as `user.<reference>`, `service.<reference>` or `system.<reference>`. It is a declaration by the authenticated emitter; KAS does not verify that it names a real user. Do not put names, email addresses, tokens, clinical data, personal data or financial details into fields. `action` uses a dotted lowercase action name; `result` is one of `success`, `failure` or `denied`. `occurred_at` is an RFC3339 timestamp supplied by the emitter.

## Success

HTTP `201 Created` is returned only after the event is durably stored: the JSON Lines record has been written and `Sync` has succeeded, or the PostgreSQL `INSERT` has been committed:

```json
{
  "contract_version": "v1",
  "event": {
    "contract_version": "v1",
    "event_id": "evt.demo-01",
    "tenant_id": "tenant-demo-a",
    "service_id": "fono-node-local",
    "actor": "user.demo-01",
    "action": "settings.updated",
    "resource": "setting.max_meta_keys",
    "result": "success",
    "correlation_id": "corr.demo-01",
    "occurred_at": "2026-11-16T12:00:00Z",
    "received_at": "2026-11-16T15:00:00Z"
  }
}
```

### Idempotency

An event's identity is `(tenant_id, service_id, event_id)`; the same `event_id` under another tenant or service is a different event. Sending the same identity again with identical content (every field, compared exactly as sent, including the `occurred_at` text) writes nothing and returns HTTP `200 OK` with the originally stored event, including its original `received_at`. Sending the same identity with different content returns `409 event_conflict` and writes nothing. A client whose response was lost can therefore retry safely with the same body. This is deduplication by identity, not an exactly-once delivery guarantee.

`occurred_at` is the emitter's claimed time. `received_at` is assigned by the KAS process in UTC when it accepts the event for recording. They are separate values and neither is cryptographic proof of time.

## Errors

Errors use `{ "contract_version": "v1", "error": { "code": "...", "message": "..." } }`.

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | `invalid_request` | Malformed JSON, unknown field or multiple JSON values |
| 400 | `invalid_event` | Invalid contract, identifier, action, result or timestamp |
| 401 | `unauthorized` | Unknown service or invalid/missing credential |
| 403 | `tenant_forbidden` | Authenticated service is not allowed for the requested tenant |
| 403 | `tenant_inactive` | Tenant is registered but inactive; new audit events are denied |
| 403 | `capability_forbidden` | Authenticated service lacks the `audit:write` capability |
| 404 | `tenant_not_found` | Requested tenant is absent from `tenant_registry` |
| 403 | `service_identity_mismatch` | Body service ID differs from authenticated service |
| 409 | `event_conflict` | The same `(tenant_id, service_id, event_id)` was already stored with different content |
| 413 | `payload_too_large` | Request exceeds 8 KiB |
| 415 | `unsupported_media_type` | Content type is not JSON |
| 503 | `audit_unavailable` | Audit storage is missing or failed to write/sync |
| 503 | `capacity_exceeded` | Shared HTTP concurrency limit was reached |

## Local JSON Lines storage

The executable defaults to `./data/audit.jsonl`; override with `-audit-data-dir`. The directory also contains `audit.lock`. Only one process may own that directory. A second process fails closed. An abrupt termination can leave a stale lock; after confirming the owner process is gone, an operator may remove only the lock file manually.

At startup, the entire existing JSON Lines file is validated. A malformed, oversized or incomplete line prevents opening; no records are skipped or discarded automatically. Preserve the file and investigate before manual repair. A valid file can be reopened after process restart and new records appended.

The store serializes writes within one process and synchronizes each complete line before acknowledging it. It is local append-only storage, not tamper-evident or immutable storage. At startup it builds an in-memory index of event identities from the whole file (memory grows with the file), so idempotency also holds after a restart. Files written before uniqueness existed may contain duplicate identities; they still open, the first record wins for duplicate detection, and nothing in the file is changed. There is no search, edit or delete API, retention, backup, replication or recovery automation. This contract is a local pilot, not a production readiness claim.

## PostgreSQL storage (optional)

`-audit-store postgres` stores events in `audit_events` (see the README for flags, migrations, TLS and database roles). Uniqueness is enforced by the unique index `audit_events_identity_uidx` on `(tenant_id, service_id, event_id)`. Concurrent identical submissions result in one row. The connection string is read from an environment variable and is never logged. For a host other than loopback, TLS is mandatory (`sslmode=require`, `verify-ca` or `verify-full`; `verify-full` is recommended because `require` does not authenticate the server).

## Reading audit records (`audit:read`)

`GET /v1/audit` returns the audit records of **one tenant**, with the same service authentication headers. It needs the capability `audit:read`, which is separate from `audit:write`: a service that can write cannot read, and the reverse.

Query parameters (all optional; unknown or repeated parameters are rejected with `400 invalid_request`):

| Parameter | Meaning |
|---|---|
| `entity_type`, `entity_id` | Only records with that entity reference. `entity_id` requires `entity_type`. |
| `since` | RFC3339 instant; records recorded at or after it. |
| `limit` | 1 to 200, default 50. Anything else is `400`. |
| `cursor` | The `next_cursor` of the previous page, unchanged. |
| `tenant_id` | Only for a service allowed for several tenants. |

**Tenant.** The tenant comes from the authenticated service. A service allowed for exactly one tenant needs no parameter and can only read that tenant. A service allowed for several must send `tenant_id` (otherwise `400 tenant_required`); that value is authorized like any other request, so it can only select one of the service's own active tenants (`403 tenant_forbidden`, `403 tenant_inactive`, `404 tenant_not_found`). Records of another tenant are never returned, and a cursor carries only a position, never a tenant.

**Order and cursor.** Records are ordered by `recorded_at` and then by the store's record id. `recorded_at` is the `received_at` the KAS process assigned, truncated to microseconds (the finest resolution both stores keep); the id is the PostgreSQL identity, or the line number in the JSON Lines file. The cursor is opaque and stable: paging with any `limit` visits every record once, in the same order, without repeating or skipping, including records that share the same instant.

```json
{
  "contract_version": "v1",
  "items": [ { "contract_version": "v1", "event_id": "evt.demo-01", "tenant_id": "tenant-demo-a", "service_id": "fono-node-local",
               "actor": "user.demo-01", "action": "settings.updated", "resource": "setting.max_meta_keys", "result": "success",
               "correlation_id": "corr.demo-01", "occurred_at": "2026-11-18T12:00:00Z", "received_at": "2026-11-18T12:00:01.123456Z",
               "entity_type": "appointment", "entity_id": "app-1" } ],
  "next_cursor": "djEuMTc2MzQ2NzIwMTEyMzQ1Ni4xMg"
}
```

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | `invalid_request` | Unknown or repeated parameter, invalid `limit`, `since`, `entity_type` or `entity_id` |
| 400 | `invalid_cursor` | Cursor is malformed |
| 400 | `tenant_required` | The service is allowed for several tenants and sent no `tenant_id` |
| 401 | `unauthorized` | Unknown service or invalid credential |
| 403 | `capability_forbidden` / `tenant_forbidden` / `tenant_inactive` | Missing `audit:read`, or the tenant is not allowed or inactive |
| 404 | `tenant_not_found` | The selected tenant is not registered |
| 503 | `audit_unavailable` | Audit storage is missing, failed, or does not support reading |

**Limits.** Records committed *after* a page was read but with an earlier `recorded_at` (a slow writer) can be missed by a scan that is already past that point. To catch them, restart from a `since` slightly before the last position you processed and deduplicate by `event_id`. The JSON Lines store reads from an in-memory index rebuilt from the whole file on startup (memory grows with the file). PostgreSQL reads need only `SELECT` on `audit_events`; migration `0003` adds the `entity_type`/`entity_id` columns (both or neither) and two indexes, `(tenant_id, received_at, id)` and a partial `(tenant_id, entity_type, entity_id, received_at, id)`. Reading returns the stored record; it is neither tamper-evident nor a guarantee that nothing was altered by someone who administers the store.
