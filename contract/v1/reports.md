# KAS Reports (contract v1)

The revenue report ready to hand to a company: a printable HTML page with the
company's name and color, or a CSV file. It reads the KAS Attribution data, so
the module `reports` depends on `attribution` (contract/v1/modules.md). Spec:
"KAS por módulos", section 8.6.

## Route

`GET /internal/v1/reports/revenue` — capability `reports:read`, module `reports`
active.

Parameters are the same as `GET /internal/v1/attribution/revenue`
(contract/v1/attribution.md): `tenant_id`, `from`, `to`, `unit`, `model`, plus
`format=html` (default) or `format=csv`.

- **HTML**: one self-contained page (no script, no external file; header
  `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`), with
  the net revenue, the three levels, the line "Conferido" when the levels add up
  to the total, campaigns, "sem vínculo" by reason, and the rule versions. It
  prints cleanly and follows the reader's light or dark theme.
- **CSV**: identical to the Attribution CSV (one row per line, a `sem_vinculo`
  row and a `total` row).

## Branding

Two optional configuration keys per tenant, declared as `string` in
`key_schema`:

| Key | Use | Default |
| --- | --- | --- |
| `branding.name` | company name in the title (up to 80 characters) | the tenant id |
| `branding.color` | accent color, `#RRGGBB` | `#1f6f5c` |

A value that does not fit is ignored and the default is used. No company has
code of its own: a new company is a configuration change.

## Errors

`400 invalid_request` (bad parameter or format), `400 model_unsupported`,
`403 tenant_forbidden | capability_forbidden | module_not_contracted |
module_paused`, `503 attribution_unavailable`.

## Pilot

`deploy/piloto-escola/run.sh` runs the second fictional company (escola-demo)
end to end with the real executables; the same seven steps are covered by
`TestEscolaDemoPilot`.
