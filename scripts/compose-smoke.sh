#!/bin/bash
# Smoke test for a running KAS Core (the compose stack or a local binary).
# Publishes an event, claims and acknowledges it, and checks /metrics. Uses only
# fictitious data. The server certificate is self-signed in development, so
# curl runs with -k.
#
#   BASE_URL=https://127.0.0.1:8088 ./scripts/compose-smoke.sh
set -euo pipefail

BASE_URL="${BASE_URL:-https://127.0.0.1:8088}"
FONO_TOKEN="${KAS_DEV_FONO_TOKEN:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
METRICS_TOKEN="${KAS_DEV_METRICS_TOKEN:-cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc}"
WAIT_SECONDS="${WAIT_SECONDS:-90}"

fail() { echo "SMOKE FAILED: $*" >&2; exit 1; }
req() { # method path auth-token body -> prints "BODY\nHTTP=code"
  local method="$1" path="$2" token="$3" body="${4:-}"
  local args=(-ks -w '\nHTTP=%{http_code}' -X "$method" "$BASE_URL$path")
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  if [[ -n "$body" ]]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  curl "${args[@]}"
}
status() { sed -n 's/^HTTP=//p' <<<"$1"; }
svc_req() { # path body
  local method=POST path="$1" body="$2"
  curl -ks -w '\nHTTP=%{http_code}' -X POST "$BASE_URL$path" -H "X-KAS-Service: fono-node-local" \
    -H "Authorization: Bearer $FONO_TOKEN" -H 'Content-Type: application/json' -d "$body"
}

echo "waiting for $BASE_URL/readyz (up to ${WAIT_SECONDS}s)"
for ((i = 0; i < WAIT_SECONDS; i++)); do
  if [[ "$(curl -ks -o /dev/null -w '%{http_code}' "$BASE_URL/readyz" || true)" == "200" ]]; then ready=1; break; fi
  sleep 1
done
[[ "${ready:-0}" == 1 ]] || fail "readyz did not return 200"

id="evt.smoke-$(date +%s)-$RANDOM"
event="{\"contract_version\":\"v1\",\"event_id\":\"$id\",\"tenant_id\":\"tenant-demo-a\",\"service_id\":\"fono-node-local\",\"event_type\":\"settings.updated\",\"correlation_id\":\"corr.smoke\",\"occurred_at\":\"2026-11-17T12:00:00Z\",\"payload\":{\"version\":1}}"

out="$(svc_req /internal/v1/events "$event")"
[[ "$(status "$out")" == 201 ]] || fail "publish returned $(status "$out"): $out"
out="$(svc_req /internal/v1/events "$event")"
[[ "$(status "$out")" == 200 ]] || fail "identical resend returned $(status "$out"), want 200"

out="$(svc_req /internal/v1/events/deliveries/claim '{"contract_version":"v1","consumer_id":"consumer-demo","max_deliveries":50,"lease_seconds":30}')"
[[ "$(status "$out")" == 200 ]] || fail "claim returned $(status "$out"): $out"
grep -q "\"event_id\":\"$id\"" <<<"$out" || fail "claim did not return the published event"
delivery_id="$(grep -o '"delivery_id":[0-9]*' <<<"$out" | head -1 | cut -d: -f2)"
attempt="$(grep -o '"attempt":[0-9]*' <<<"$out" | head -1 | cut -d: -f2)"
lease_token="$(grep -o '"lease_token":"[0-9a-f]*"' <<<"$out" | head -1 | cut -d'"' -f4)"
[[ -n "$delivery_id" && -n "$attempt" && -n "$lease_token" ]] || fail "could not read the delivery from the claim"

out="$(svc_req /internal/v1/events/deliveries/ack "{\"contract_version\":\"v1\",\"consumer_id\":\"consumer-demo\",\"delivery_id\":$delivery_id,\"attempt\":$attempt,\"lease_token\":\"$lease_token\"}")"
[[ "$(status "$out")" == 200 ]] || fail "ack returned $(status "$out"): $out"

out="$(req GET /metrics "")"
[[ "$(status "$out")" == 401 ]] || fail "/metrics without a token returned $(status "$out"), want 401"
out="$(req GET /metrics "$METRICS_TOKEN")"
[[ "$(status "$out")" == 200 ]] || fail "/metrics returned $(status "$out")"
for want in 'kas_http_requests_total{route="/internal/v1/events",status="201"}' \
            'kas_http_request_duration_seconds_count' \
            'kas_outbox_deliveries{state="delivered"}' 'kas_outbox_stats_up 1'; do
  grep -qF "$want" <<<"$out" || fail "/metrics is missing $want"
done
for secret in "tenant-demo" "$FONO_TOKEN" "$METRICS_TOKEN" "fono-node-local" "consumer-demo" "$id"; do
  if grep -qF "$secret" <<<"$out"; then fail "/metrics leaks '$secret'"; fi
done
echo "smoke ok: published, replayed (200), claimed, acked, metrics scraped"
