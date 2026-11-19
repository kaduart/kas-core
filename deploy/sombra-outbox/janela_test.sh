#!/usr/bin/env bash
# Casos da janela da agenda da sombra do Outbox (sem rede, sem banco).
set -euo pipefail
J="$(dirname "$0")/janela.sh"
ep() { date -u -d "$1" +%s; }
fail=0
check() { local got; got=$("$J" "$(ep "$1")"); [ "$got" = "$2" ] && echo "OK: $1 -> $got s" || { echo "FALHOU: $1 -> $got (esperado $2)"; fail=1; }; }
check '2026-11-20 06:35:00' 20700   # no horário: 345 min (limitado)
check '2026-11-20 06:50:00' 20220   # 15 min de atraso: termina 12:27, 8 min antes de 12:35
check '2026-11-20 12:10:00' 1020    # muito atrasada: só 17 min
check '2026-11-20 12:20:00' 0       # 7 min: não roda
exit $fail
