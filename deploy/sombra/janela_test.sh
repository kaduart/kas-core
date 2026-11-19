#!/usr/bin/env bash
# Casos da janela da agenda (sem rede, sem banco).
set -euo pipefail
J="$(dirname "$0")/janela.sh"
ep() { date -u -d "$1" +%s; }
fail=0
check() { local got; got=$("$J" "$(ep "$1")"); [ "$got" = "$2" ] && echo "OK: $1 -> $got s" || { echo "FALHOU: $1 -> $got (esperado $2)"; fail=1; }; }
check '2026-11-20 06:05:00' 20700   # no horário: 345 min (limitado)
check '2026-11-20 06:20:00' 20220   # 15 min de atraso: termina 11:57
check '2026-11-20 06:40:00' 19020   # 35 min de atraso: termina 11:57, 8 min antes de 12:05
check '2026-11-20 11:40:00' 1020    # muito atrasada: só 17 min
check '2026-11-20 11:50:00' 0       # 7 min: não roda
check '2026-11-20 23:59:00' 0       # 6 min antes de 00:05: não roda
exit $fail
