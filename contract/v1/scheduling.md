# Agenda / scheduling (contract v1)

Generic agenda: resources (people, rooms, equipment), weekly opening rules,
exceptions, services with duration and buffer, and bookings. It holds **no
personal data**: clients are opaque references (`client_ref`, pattern
`^[A-Za-z0-9._:-]{1,128}$`, which keeps names, e-mails and phone numbers out by
construction). Module `scheduling`; see `modules.md`.

Every route takes the tenant from the caller's credentials. For a panel service
(one holding `identity:sessions`) the `X-KAS-Session` header is required and the
tenant must be one of the signed-in person's memberships (`tenant_forbidden`
otherwise). `tenant_id` in the request only chooses among allowed tenants.

## Reads and calculations

| Route | Capability | Purpose |
| --- | --- | --- |
| `GET /internal/v1/scheduling/catalog?tenant_id=` | `scheduling:read` | zone, `read_only`, resources, services |
| `GET /internal/v1/scheduling/bookings?tenant_id=&resource_id=&from=&to=` | `scheduling:read` | bookings touching `[from, to)` (RFC 3339, at most 62 days) |
| `GET /internal/v1/scheduling/availability?tenant_id=&resource_id=&service_id=\|duration_min=&step_min=&from=&to=` | `scheduling:compute` | free slots between two local dates (inclusive) |
| `POST /internal/v1/scheduling/conflicts:check` | `scheduling:compute` | which candidates collide (writes nothing) |
| `POST /internal/v1/scheduling/series:preview` | `scheduling:compute` | expand a recurrence with the tenant's zone and holidays |

`read_only` is true when the tenant's agenda is **mirrored** from another system
(it has mirror rows). Such an agenda is written only by the mirror.

## Writes (phase 4A)

Capability `scheduling:write`. A panel service may write only for a person whose
role in the tenant is `admin`, `gestor` or `atendimento`; `leitura` gets
`403 role_forbidden`. The audit actor is `user:<id>` (panel) or
`service:<service id>`.

| Route | Body |
| --- | --- |
| `POST /internal/v1/scheduling/bookings` | `contract_version`, `tenant_id`, `idempotency_key`, `resource_id`, `client_ref`, `service_id?`, `starts_at`, `ends_at`, `status?` (`scheduled` default, `tentative`, `confirmed`), `buffer_min?` (only without `service_id`) |
| `POST /internal/v1/scheduling/bookings/{id}/cancel` | `contract_version`, `tenant_id`, `expected_version` |
| `POST /internal/v1/scheduling/bookings/{id}/reschedule` | `contract_version`, `tenant_id`, `expected_version`, `starts_at`, `ends_at` |

Answer: `{"contract_version":"v1","tenant_id":"…","booking":{…},"replayed":true?}`.
Create answers `201`, or `200` with `replayed: true` when the same
`idempotency_key` arrives again with the same body. Cancelling an already
cancelled booking answers `200` with it unchanged.

Guarantees, enforced in PostgreSQL inside one transaction: no overlap on a
resource (including the service buffer, `blocked_until`), no overlapping active
bookings for one client when the tenant sets `client_exclusive`, optimistic
version on cancel/reschedule, one audit row (`sched_booking_log`) per change,
writes only inside the resource's opening rules and outside holidays and
exceptions, and no native write on a mirrored tenant.

| HTTP | `error.code` | When |
| --- | --- | --- |
| 400 | `invalid_request` / `unsupported_contract_version` | malformed body, unknown field, bad status, end before start, booking already closed |
| 403 | `role_forbidden` | panel person with role `leitura` |
| 404 | `not_found` | resource, service or booking unknown |
| 409 | `agenda_read_only` | mirrored tenant |
| 409 | `slot_taken` | resource busy (buffer included) |
| 409 | `client_busy` | client already booked at that time |
| 409 | `version_conflict` | `expected_version` is stale |
| 409 | `idempotency_mismatch` | key reused with another body |
| 422 | `outside_opening_hours` | outside the weekly rules, on a holiday or exception |
| 503 | `scheduling_unavailable` | agenda disabled or database unavailable |
