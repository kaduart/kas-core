#!/usr/bin/env bash
# Central de alertas: testes com race detector e comparação com o alertService
# do CRM (Node, sem modificação) no mesmo cenário fictício. Sem rede, sem banco.
#   scripts/validate-alerts.sh
set -euo pipefail
cd "$(dirname "$0")/.."
echo "== testes (race)"
go test -race -count=1 ./internal/alerts/ ./internal/legacy/crmalerts/
echo "== comparação Node x Go"
KAS_TEST_ORACLE_ALERTS_DIR="$PWD/legacy-oracle/alerts" go test -count=1 ./internal/legacy/crmalerts -run Oracle -v | grep -E "min |Node:|Go:|PASS|FAIL"
echo "== ok"
