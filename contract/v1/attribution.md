# KAS Attribution (contract v1)

Revenue by campaign with a level of evidence, computed from four generic inputs
that any company can send: **customers**, **contacts**, **receipts** and
**refunds**. The module knows nothing about patients, packages, students or any
other business entity: a school, a clinic or a shop maps its own records onto
these four kinds. Spec: "KAS por módulos", sections 8.2 to 8.5 (D1–D5 approved
on 2026-11-20).

Requires the module `attribution` contracted and active for the tenant
(contract/v1/modules.md). A paused module answers 403 `module_paused` and keeps
every stored input; reactivating it gives the same numbers.

## Routes

| Route | Capability |
| --- | --- |
| `POST /internal/v1/attribution/{customers\|contacts\|receipts\|refunds}:batch` | `attribution:import` |
| `GET /internal/v1/attribution/revenue` | `attribution:read` |

Authentication is the usual service identity (`X-KAS-Service` + bearer token).
`tenant_id` is optional when the service serves one tenant.

## Import

```json
{
  "contract_version": "v1",
  "tenant_id": "escola-demo",
  "batch_id": "rec-2026-11-0001",
  "actor": "user.fin-07",
  "items": [
    {"external_id": "rec-1", "customer_ref": "fam-12", "amount_cents": 48000,
     "received_at": "2026-11-05T13:00:00Z", "payment_ref": "pix-881"}
  ]
}
```

- 1 to 500 items per batch, body up to 1 MiB, strict JSON (unknown or repeated
  keys are refused).
- **Idempotent by batch**: the same `batch_id` with the same content answers 200
  with the original result and `"duplicate": true`, writing nothing. The same
  `batch_id` with other content is 409 `batch_conflict`.
- **Idempotent by item**: the key is tenant + kind + `external_id`. Same content
  is `unchanged`; other content is a new version (`updated`), the old version is
  kept. `"voided": true` removes the item from reports as a new version; nothing
  is ever deleted.
- An invalid line does not stop the batch: it is listed in `rejected` with its
  index and a code. Two lines with the same `external_id` in one batch: the
  second is `duplicate_in_batch`.
- Each new batch is recorded in the audit trail (`attribution.batch_imported`,
  entity `attribution_batch` / `batch_id`) with identifiers only.

Answer (201 new, 200 replay):

```json
{"contract_version":"v1","tenant_id":"escola-demo","batch_id":"rec-2026-11-0001","kind":"receipts",
 "created":1,"updated":0,"unchanged":0,"rejected":[],"duplicate":false}
```

### Fields per kind

| Kind | Required | Optional |
| --- | --- | --- |
| customers | `external_id` | `phone` or `email` (only a fingerprint is kept), `unit`, `voided` |
| contacts | `external_id`, `customer_ref`, `source`, `contacted_at` | `campaign`, `ad_id`, `gclid`, `fbclid`, `ttclid`, `ctwa_clid`, `method`, `acquisition`, `voided` |
| receipts | `external_id`, `customer_ref`, `amount_cents` (> 0), `received_at` | `payment_ref`, `unit`, `voided` |
| refunds | `external_id`, `amount_cents` (> 0), `refunded_at`, and `receipt_ref` or `payment_ref` | `customer_ref`, `unit`, `voided` |

- `source`: `google_ads`, `meta_ads`, `tiktok_ads`, `instagram`, `organic`,
  `referral`, `other`.
- `method`: free lowercase code; `ctwa`, `site_signature` and `token` count as a
  strong signal.
- Times are RFC 3339. Money is integer cents (BRL).
- A field of another kind is refused (`field_not_allowed`) so a misplaced column
  is caught.
- References and text start with a letter or digit (no cell can start a
  spreadsheet formula).

Rejection codes: `external_id_invalid`, `reference_invalid`, `text_invalid`,
`field_not_allowed`, `phone_invalid`, `email_invalid`, `customer_ref_required`,
`source_invalid`, `method_invalid`, `contacted_at_invalid`, `amount_invalid`,
`received_at_invalid`, `receipt_or_payment_ref_required`, `refunded_at_invalid`,
`duplicate_in_batch`.

### Personal data

Phones are normalized (55 + area code + last 8 digits) and e-mails lowercased,
then turned into an HMAC fingerprint keyed by a secret
(`-attribution-fingerprint-key-env`, 32+ bytes) and by tenant. The raw value is
never stored, logged or audited. Customers with the same fingerprint are one
household. Changing the key breaks matching with inputs already stored.

### Money

The Attribution tables are the only place where the KAS stores money
(decision D2). Events, audit records and logs carry no amounts and no personal
data.

## CSV files

`kas-attribution-import` reads a UTF-8, comma-separated file whose header uses
the field names above (any order; a BOM is accepted; unknown or repeated columns
are refused) and sends it in batches of 500 with ids `<batch-id>-0001`, `-0002`…
Running it again with the same `-batch-id` is safe. `acquisition` and `voided`
accept `true/false`, `1/0`, `sim/não`.

```
KAS_ATTR_TOKEN=... kas-attribution-import -kas https://kas.example -service escola-sis \
  -token-env KAS_ATTR_TOKEN -kind receipts -file recebimentos.csv -batch-id rec-2026-11 -actor user.fin-07
```

It prints a JSON summary with the refused lines by line number of the file.

## Revenue report

`GET /internal/v1/attribution/revenue?from=2026-11-01&to=2026-11-30&unit=centro&format=json`

- `from`/`to`: cash date, RFC 3339 or `YYYY-MM-DD` (UTC; a plain `to` includes
  the whole day). `unit` filters receipts and refunds (a refund without unit
  inherits its receipt's). `model` only accepts
  `primeiro-contato-por-recebimento-v1` (anything else: 400 `model_unsupported`).
- `format=csv` answers one row per campaign line, one `sem_vinculo` row and one
  `total` row.

Rules (the same as the Fono Inova CRM, so both reconcile):

- **Model** `primeiro-contato-por-recebimento-v1`: each receipt is credited to
  the household's acquisition contact (its date moved earlier only by an earlier
  contact of the same touch), otherwise to the earliest contact; a contact after
  the receipt never takes credit.
- **Same touch** `touch-v2-precedencia`: click id → ad → campaign; the first
  level present on both sides decides; another source is never the same touch.
- **Levels**: `comprovada` = strong signal (click id or strong method) on a
  contact point used by one household only; `provavel` = shared contact point or
  weak signal; `sem_vinculo` = no contact before the receipt, with reasons.
- **Refunds** `estorno-v2-rateio-por-recebimento`: with `receipt_ref`, the refund
  follows that receipt; with only `payment_ref`, it is split over the receipts of
  that payment up to the refund date in proportion to their amounts (remainder
  cents to the oldest).
- **Invariant**: `comprovada + provavel + sem_vinculo = total_liquido_centavos`
  (`invariante.fecha`).

## Integration readiness

`GET /internal/v1/modules` shows `integration.missing` for attribution:
`contacts` and/or `receipts` until at least one of each was received.

## Enabling

```
kas-core -events-store postgres -attribution-store postgres -attribution-migrate-only   # migration role
psql ... -f deploy/grants-attribution.sql
KAS_ATTRIBUTION_FINGERPRINT_KEY=... kas-core -events-store postgres -attribution-store postgres -attribution-auto-migrate=false
```

Off by default (`-attribution-store none`: routes answer 503
`attribution_unavailable`).
