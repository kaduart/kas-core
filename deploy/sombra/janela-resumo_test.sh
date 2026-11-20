#!/usr/bin/env bash
# Testes de deploy/sombra/janela-resumo.sh (sem rede).
set -euo pipefail
d=$(dirname "$0"); f=0
run() { env -i PATH="$PATH" JANELA_INICIO=2026-11-23T00:00:00-03:00 JANELA_FIM=2026-11-28T00:00:00-03:00 "$@" "$d/janela-resumo.sh"; }
check() { if printf '%s\n' "$1" | grep -qx -- "$2"; then echo "ok: $3"; else echo "FALHOU: $3 (esperado $2)"; printf '%s\n' "$1"; f=1; fi; }
ep() { date -u -d "$1" +%s; }
o=$(run NOW=$(ep 2026-11-20T12:00:00Z));       check "$o" "SINCE=" "antes: sem janela"; check "$o" "MIN_DAYS=0" "antes: min 0"
o=$(run NOW=$(ep 2026-11-25T12:03:10Z));       check "$o" "SINCE=2026-11-23T00:00:00-03:00" "durante: início oficial"
check "$o" "UNTIL=2026-11-25T12:00:00Z" "durante: até agora arredondado"; check "$o" "MIN_DAYS=0" "durante: min 0"
o=$(run NOW=$(ep 2026-11-30T13:00:00Z));       check "$o" "UNTIL=2026-11-28T00:00:00-03:00" "depois: fim oficial"; check "$o" "MIN_DAYS=5" "depois: min 5"
o=$(run NOW=$(ep 2026-11-28T03:00:00Z));       check "$o" "MIN_DAYS=5" "fim exato (exclusivo) já é veredito"
o=$(run NOW=$(ep 2026-11-30T13:00:00Z) MIN_DAYS=0); check "$o" "MIN_DAYS=0" "min_days manual respeitado"
o=$(run SINCE=2026-11-24T00:00:00-03:00 UNTIL=2026-11-26T00:00:00-03:00); check "$o" "FASE=manual" "since/until manuais"
check "$o" "MIN_DAYS=0" "manual sem min_days = 0"
if run SINCE=2026-11-24T00:00:00-03:00 >/dev/null 2>&1; then echo "FALHOU: since sem until aceito"; f=1; else echo "ok: since sem until recusado"; fi
exit $f
