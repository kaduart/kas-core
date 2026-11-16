# KAS Config contract v1

## Endpoint

`POST /internal/v1/config/evaluate`

Authentication headers:

- `X-KAS-Service: <service-id>`
- `Authorization: Bearer <64-character hexadecimal token>`

The service ID maps to a token environment-variable name and an explicit tenant allowlist in the server's JSON configuration. The token is loaded at startup from that environment variable. The same file contains `tenant_registry`, the central list of tenant IDs and their `active`/`inactive` status. A request succeeds only when its tenant is registered, active and in the authenticated service's allowlist. Tenant IDs and statuses cannot be changed through request bodies. The file is loaded once at startup; registry changes require a restart.

## Request

```json
{
  "contract_version": "v1",
  "tenant_id": "tenant-demo-a",
  "key": "logging.max_meta_keys",
  "expected_type": "integer"
}
```

The JSON config declares key types under `key_schema`. Supported types are `integer`, `boolean` and `string`. Integer rules may define inclusive `min` and `max`. Values are checked against the declared type and range when the configuration loads. Requests for undeclared keys or a mismatched `expected_type` are rejected.

## Success response

```json
{
  "contract_version": "v1",
  "config_version": "local-demo-2026-11-16",
  "tenant_id": "tenant-demo-a",
  "key": "logging.max_meta_keys",
  "type": "integer",
  "value": 10
}
```

`value` is JSON typed according to `type`. `config_version` identifies the loaded config snapshot and is shared across the response for that process.

## Errors

Every error response has this shape:

```json
{
  "contract_version": "v1",
  "error": { "code": "tenant_forbidden", "message": "service is not authorized for this tenant" }
}
```

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | `invalid_request` | Invalid JSON, unknown field, invalid contract version or missing/invalid request fields |
| 401 | `unauthorized` | Unknown service or invalid/missing service credential |
| 403 | `tenant_forbidden` | Authenticated service is not authorized for the requested tenant |
| 403 | `tenant_inactive` | Tenant is registered but inactive; existing audit history is retained |
| 404 | `tenant_not_found` | Requested tenant is absent from `tenant_registry` |
| 404 | `config_not_found` | Tenant has no configured value for the key |
| 413 | `payload_too_large` | Request body exceeds the size limit |
| 415 | `unsupported_media_type` | Content type is not JSON |
| 422 | `unknown_key` | Key is absent from `key_schema` |
| 422 | `type_mismatch` | Requested type differs from the declared key type |
| 503 | `capacity_exceeded` | Concurrent request limit was reached |
| 500 | `internal_error` | Unexpected evaluation failure |

Health endpoints `GET /healthz` and `GET /readyz` return a small `v1` status response and do not expose configuration values. The example config and tenant data are fictitious; credentials are supplied only via environment variables.
