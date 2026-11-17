# KAS Events contract v1 (outbox, phases 1 and 2)

Durable event publication with identity-based idempotency, and delivery to consumers with leases, retries and a `dead` state. The outbox lives in PostgreSQL. **Delivery is at-least-once, not exactly-once**: consumers must be idempotent. There is no ordering guarantee, not even per subject or tenant.

## Endpoint

`POST /internal/v1/events`

Headers: `X-KAS-Service`, `Authorization: Bearer <64-character hexadecimal token>`, `Content-Type: application/json`.

Authentication and tenant authorization are the same as for the other endpoints. The tenant must be registered, active and allowed for the service, and the service must have the capability **`events:publish`** (`callers.<service-id>.capabilities`). The body `service_id` must equal the authenticated `X-KAS-Service`. Event types and consumers are declared in the startup configuration; changes require a restart.

Publication is enabled with `-events-store postgres`. Without it, the endpoint answers `503 events_unavailable`.

## Request

Maximum request body: 20 KiB. Unknown fields and duplicate keys are rejected.

```json
{
  "contract_version": "v1",
  "event_id": "evt.demo-01",
  "tenant_id": "tenant-demo-a",
  "service_id": "fono-node-local",
  "event_type": "settings.updated",
  "correlation_id": "corr.demo-01",
  "occurred_at": "2026-11-17T12:00:00Z",
  "payload": { "setting_key": "logging.max_meta_keys", "version": 3 }
}
```

All identifiers in this example are fictional. `event_type` must be declared in the configuration `event_types` (dotted lowercase name). `occurred_at` is an RFC3339 timestamp supplied by the emitter; `received_at` is assigned by the KAS process.

### Payload: identifiers only

The payload is a JSON object of at most **16 KiB**. **The contract forbids clinical data and personal data**: send identifiers only, never names, contact details, document numbers, free text, clinical or financial values. KAS enforces a shape that narrows what can be sent:

- at most 32 keys; keys are lowercase snake_case (`^[a-z][a-z0-9_]{0,63}$`);
- values are only identifier-shaped strings (`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`), 64-bit integers or booleans — no nesting, arrays, `null`, fractions or free text.

This cannot detect personal data that is written in an identifier-like form (for example a bare document number). The emitter is responsible for sending identifiers only.

The payload is stored and returned in canonical form (sorted keys, no whitespace).

## Idempotency

An event's identity is `(tenant_id, service_id, event_id)`; the same `event_id` under another tenant or service is a different event.

- First publication: **`201 Created`**, only after the event and its deliveries are committed in one transaction.
- Identical resend (every field, compared as sent, including the `occurred_at` text and the payload content): **`200 OK`** with the originally stored event and its original `received_at`. Nothing is written and no deliveries are added.
- Same identity with different content: **`409 event_conflict`**; nothing is written.

A client whose response was lost can retry with the same body. This is deduplication by identity, not an exactly-once guarantee.

## Errors

Errors use `{ "contract_version": "v1", "error": { "code": "...", "message": "..." } }`.

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | `invalid_request` | Malformed JSON, unknown field, duplicate key or multiple JSON values |
| 400 | `invalid_event` | Invalid identifier, timestamp, missing payload, or a payload outside the allowed shape |
| 401 | `unauthorized` | Unknown service or invalid/missing credential |
| 403 | `tenant_forbidden` / `tenant_inactive` / `capability_forbidden` / `service_identity_mismatch` | Authorization failures |
| 404 | `tenant_not_found` | Tenant is absent from `tenant_registry` |
| 409 | `event_conflict` | Same identity already published with different content |
| 413 | `payload_too_large` | Payload above 16 KiB or request above 20 KiB |
| 415 | `unsupported_media_type` | Content type is not JSON |
| 422 | `unknown_event_type` | Event type is not declared in the configuration |
| 503 | `events_unavailable` | Publication disabled, or storage failed to commit |
| 503 | `capacity_exceeded` | Shared HTTP concurrency limit was reached |

## Configuration

```json
"event_types": ["settings.updated"],
"event_consumers": {
  "consumer-demo": { "event_types": ["settings.updated"], "tenants": ["tenant-demo-a"] }
}
```

When an event is published, one delivery is created for every consumer subscribed to its type **and** its tenant. A consumer never receives events from a tenant it is not listed for. Consumers added to the configuration later receive no deliveries for events published earlier.

## Delivery (phase 2)

Consumers are in-process handlers, used for tests; **there is no webhook and no external call in this phase**. The shipped executable registers no handlers, so deliveries stay `pending` until a later phase adds a consumption mechanism (pull is the proposed next step).

A delivery has one of four states: `pending`, `in_flight`, `delivered`, `dead`.

- A worker claims due deliveries with `FOR UPDATE SKIP LOCKED` and a **lease** (`locked_until`, default 60 s). Claiming counts as an attempt.
- Success marks the delivery `delivered`. A failure returns it to `pending` after an exponential **backoff with jitter** (default base 2 s, doubling, ±20%, cap 5 min).
- After `max_attempts` (default 5) failures, or immediately for a permanent failure (`ErrPermanent`), the delivery becomes **`dead`**. Dead rows are kept.
- If a worker stops without settling, its lease expires and another worker claims the delivery again (a new attempt). A delivery whose lease expired with no attempts left becomes `dead` (`lease_expired`).
- A worker that finishes after its lease was taken over cannot overwrite the outcome; the second handler run is a duplicate delivery.
- Handlers run with a timeout (default 30 s, always below the lease). Panics are recovered and retried. Only short error codes are stored (`handler_error`, `handler_timeout`, `handler_panic`, `handler_permanent`, `handler_cancelled`, `max_attempts`, `lease_expired`), never error text or payloads.

**Why duplicates happen:** a crash, a timeout or a lost lease after the handler did its work but before the outcome was recorded leads to a second delivery. Handlers must deduplicate by `(tenant_id, service_id, event_id)`.

## Retention

Documented policy for a later cleanup mechanism: keep `delivered` deliveries and their events for **30 days**, and `dead` deliveries for **90 days**. **Nothing is deleted automatically in this phase**, and the runtime role has no `DELETE` privilege. Without cleanup, tables grow without bound. The columns `delivered_at` and `dead_at` support computing the retention windows.

## Database roles

Use two roles. The migration role owns the schema and runs `-events-migrate-only` (it reads `KAS_EVENTS_MIGRATION_DATABASE_URL`). The runtime role runs the server (and workers) and needs only:

- `outbox_events`: `INSERT`, `SELECT` — never `UPDATE` or `DELETE`;
- `outbox_deliveries`: `INSERT`, `SELECT`, `UPDATE` — never `DELETE` or `TRUNCATE`;
- `events_schema_migrations`: `SELECT`; plus `USAGE` on the schema and `CONNECT` on the database.

Start the server with `-events-auto-migrate=false` so it only verifies that migrations were applied. The runtime role can still rewrite delivery state; nothing here is tamper-evident or immutable.
