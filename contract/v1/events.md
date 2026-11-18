# KAS Events contract v1 (outbox, phases 1 to 3)

Durable event publication with identity-based idempotency, and delivery to consumers with leases, retries and a `dead` state, consumed either by in-process handlers or by pull over HTTP. The outbox lives in PostgreSQL. **Delivery is at-least-once, not exactly-once**: consumers must be idempotent. There is no ordering guarantee, not even per subject or tenant.

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
  "consumer-demo": { "event_types": ["settings.updated"], "tenants": ["tenant-demo-a"], "service": "fono-node-local" }
}
```

`service` binds the consumer to one caller so it can pull over HTTP; that caller needs the capability `events:consume`. A consumer without `service` is for in-process handlers only and cannot be pulled.

When an event is published, one delivery is created for every consumer subscribed to its type **and** its tenant. A consumer never receives events from a tenant it is not listed for. Consumers added to the configuration later receive no deliveries for events published earlier.

## Delivery (phase 2)

Consumers are either in-process handlers (used for tests; the shipped executable registers none) or pull consumers (next section). **There is no webhook and no external call.**

A delivery has one of four states: `pending`, `in_flight`, `delivered`, `dead`.

- A worker claims due deliveries with `FOR UPDATE SKIP LOCKED` and a **lease** (`locked_until`, default 60 s). Claiming counts as an attempt.
- Success marks the delivery `delivered`. A failure returns it to `pending` after an exponential **backoff with jitter** (default base 2 s, doubling, ±20%, cap 5 min).
- After `max_attempts` (default 5) failures, or immediately for a permanent failure (`ErrPermanent`), the delivery becomes **`dead`**. Dead rows are kept.
- If a worker stops without settling, its lease expires and another worker claims the delivery again (a new attempt). A delivery whose lease expired with no attempts left becomes `dead` (`lease_expired`).
- A worker that finishes after its lease was taken over cannot overwrite the outcome; the second handler run is a duplicate delivery.
- Handlers run with a timeout (default 30 s, always below the lease). Panics are recovered and retried. Only short error codes are stored (`handler_error`, `handler_timeout`, `handler_panic`, `handler_permanent`, `handler_cancelled`, `max_attempts`, `lease_expired`), never error text or payloads.

**Why duplicates happen:** a crash, a timeout or a lost lease after the handler did its work but before the outcome was recorded leads to a second delivery. Handlers must deduplicate by `(tenant_id, service_id, event_id)`.

## Pull consumption (phase 3)

A consumer fetches deliveries, handles them, then confirms. All three endpoints are `POST`, JSON, authenticated with `X-KAS-Service` and the service token, and need the capability **`events:consume`**. A service can act only for the consumers bound to it by `service` in the configuration; an unknown consumer, an in-process consumer and another service's consumer all answer the same `403 consumer_forbidden`. The scope is the consumer's declared event types and tenants, **intersected with the service's own tenants**, and inactive tenants are excluded. The request cannot widen it: there is no tenant or type parameter. Consumers added to the configuration later are **not** given events published before (no reprocessing).

### `POST /internal/v1/events/deliveries/claim`

```json
{ "contract_version": "v1", "consumer_id": "consumer-demo", "max_deliveries": 10, "lease_seconds": 60 }
```

`max_deliveries`: 1 to 50 (default 10). `lease_seconds`: 5 to 300 (default 60). Returns `200` with the due deliveries, possibly an empty list:

```json
{ "contract_version": "v1", "deliveries": [ {
    "delivery_id": 7, "attempt": 1, "lease_token": "0123456789abcdef0123456789abcdef",
    "lease_expires_at": "2026-11-17T12:01:00Z",
    "event": { "contract_version": "v1", "event_id": "evt.demo-01", "tenant_id": "tenant-demo-a", "service_id": "fono-node-local",
               "event_type": "settings.updated", "correlation_id": "corr.demo-01", "occurred_at": "2026-11-17T12:00:00Z",
               "received_at": "2026-11-17T12:00:01Z", "payload": { "version": 3 } } } ] }
```

Claiming counts as an attempt. The `lease_token` is a secret for this claim: do not log it. A delivery whose lease expires without ack or nack is handed out again as a new attempt with a new token.

### `POST /internal/v1/events/deliveries/ack` and `.../nack`

```json
{ "contract_version": "v1", "consumer_id": "consumer-demo", "delivery_id": 7, "attempt": 1, "lease_token": "0123456789abcdef0123456789abcdef" }
```

`nack` also accepts `"permanent": true`. Both apply only when `delivery_id`, `attempt`, `consumer_id` and `lease_token` match the current lease; otherwise they answer `409 lease_lost` and change nothing (the lease expired and was taken over, or the delivery was already settled).

- `ack` → `200 {"status":"delivered"}`. A repeated ack of a delivery already delivered for that attempt also answers `200`.
- `nack` → `200 {"status":"pending","next_attempt_at":"..."}`: the delivery returns after the exponential backoff with jitter (base 2 s, doubling, ±20%, cap 5 min). When the attempts are used up (5) or `permanent` is true, it answers `200 {"status":"dead"}`. Dead rows are kept. `nack` is not idempotent: a repeat answers `409`.

There is no field for free text. Only short codes are stored (`consumer_nack`, `consumer_permanent`, `max_attempts`, `lease_expired`).

### Pull errors

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | `invalid_request` | Malformed JSON, unknown field, duplicate key, bad contract version, or a value outside the limits above |
| 401 | `unauthorized` | Unknown service or invalid/missing credential |
| 403 | `capability_forbidden` | The service lacks `events:consume` |
| 403 | `consumer_forbidden` | Unknown consumer, or not bound to the authenticated service |
| 409 | `lease_lost` | The lease token or attempt no longer matches |
| 413 / 415 | `payload_too_large` / `unsupported_media_type` | Body above 4 KiB / not JSON |
| 503 | `events_unavailable` | Event storage is disabled or failed |

Delivery remains **at-least-once**: a consumer that handled a delivery but did not manage to ack (crash, timeout, lost response) will see it again after the lease expires. Pull consumers must be idempotent by `(tenant_id, service_id, event_id)`. There is no ordering.

## Retention

Policy: keep `delivered` deliveries for **30 days** and `dead` deliveries for **90 days**; an event is removed when it has no delivery left and was received more than 30 days ago (events that never had a consumer included). `pending` and `in_flight` deliveries, and their events, are never removed. **Nothing is deleted automatically.** An operator runs the executable with `-events-purge` (add `-events-purge-dry-run` to only count): it reads `KAS_EVENTS_MIGRATION_DATABASE_URL`, deletes in batches of 1000 and exits. It needs `DELETE`, so it must use the migration (schema owner) role; the runtime role cannot delete. In a dry run the event count is a lower bound, because events freed by deliveries that were not deleted yet are not visible. Without running the purge, tables grow without bound.

## Database roles

Use two roles. The migration role owns the schema and runs `-events-migrate-only` (it reads `KAS_EVENTS_MIGRATION_DATABASE_URL`). The runtime role runs the server (and workers) and needs only:

- `outbox_events`: `INSERT`, `SELECT` — never `UPDATE` or `DELETE`;
- `outbox_deliveries`: `INSERT`, `SELECT`, `UPDATE` — never `DELETE` or `TRUNCATE`;
- `events_schema_migrations`: `SELECT`; plus `USAGE` on the schema and `CONNECT` on the database.

Start the server with `-events-auto-migrate=false` so it only verifies that migrations were applied. The purge uses the migration role, never the runtime role. The runtime role can still rewrite delivery state; nothing here is tamper-evident or immutable.
