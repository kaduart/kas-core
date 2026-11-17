# KAS Audit Events contract v1

## Endpoint

`POST /internal/v1/audit/events`

Headers:

- `X-KAS-Service: <service-id>`
- `Authorization: Bearer <64-character hexadecimal token>`
- `Content-Type: application/json`

Authentication and tenant authorization use the same service allowlist, credentials and central `tenant_registry` as the configuration endpoint. The tenant must be registered, active and allowed for the authenticated service, and the service must explicitly have the `audit:write` capability. The body `service_id` must exactly match the authenticated `X-KAS-Service`; it cannot replace that identity. Registry state and capabilities are loaded at startup and changes require restart. Deactivating a tenant blocks new events but does not delete or alter previously stored events.

`callers.<service-id>.capabilities` accepts `config:read` and/or `audit:write`. An omitted or empty list grants no business capability and is denied by default. Unknown or duplicate capability names reject startup.

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

All identifiers in this example are fictional. `actor` must use an opaque reference shaped as `user.<reference>`, `service.<reference>` or `system.<reference>`. It is a declaration by the authenticated emitter; KAS does not verify that it names a real user. Do not put names, email addresses, tokens, clinical data, personal data or financial details into fields. `action` uses a dotted lowercase action name; `result` is one of `success`, `failure` or `denied`. `occurred_at` is an RFC3339 timestamp supplied by the emitter.

## Success

HTTP `201 Created` is returned only after the JSON Lines record has been written and `Sync` has succeeded:

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
| 413 | `payload_too_large` | Request exceeds 8 KiB |
| 415 | `unsupported_media_type` | Content type is not JSON |
| 503 | `audit_unavailable` | Audit storage is missing or failed to write/sync |
| 503 | `capacity_exceeded` | Shared HTTP concurrency limit was reached |

## Local JSON Lines storage

The executable defaults to `./data/audit.jsonl`; override with `-audit-data-dir`. The directory also contains `audit.lock`. Only one process may own that directory. A second process fails closed. An abrupt termination can leave a stale lock; after confirming the owner process is gone, an operator may remove only the lock file manually.

At startup, the entire existing JSON Lines file is validated. A malformed, oversized or incomplete line prevents opening; no records are skipped or discarded automatically. Preserve the file and investigate before manual repair. A valid file can be reopened after process restart and new records appended.

The store serializes writes within one process and synchronizes each complete line before acknowledging it. It is local append-only storage, not tamper-evident or immutable storage. It does not guarantee exactly-once delivery; retries after an uncertain response can create duplicates. There is no search, edit or delete API, retention, backup, replication or recovery automation. This contract is a local pilot, not a production readiness claim.
