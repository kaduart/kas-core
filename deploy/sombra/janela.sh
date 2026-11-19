#!/usr/bin/env bash
# Duração (segundos) de uma execução AGENDADA da sombra: termina MARGEM antes do próximo horário
# da agenda (00:05, 06:05, 12:05, 18:05 UTC). Como o fim é ancorado no próximo horário, e não no
# início, uma execução atrasada pelo GitHub termina mais cedo e nunca se sobrepõe à seguinte.
#   deploy/sombra/janela.sh [agora_epoch]     -> imprime segundos (0 = não vale a pena rodar)
set -euo pipefail
NOW=${1:-$(date -u +%s)}
PERIOD=21600   # 6 h
OFFSET=300     # minuto 5
MARGIN=${MARGEM:-480}  # 8 min antes do próximo horário
MAX=20700      # 345 min (limite do job: 358)
MIN=600        # menos de 10 min: não roda
next=$(( ( (NOW - OFFSET) / PERIOD + 1 ) * PERIOD + OFFSET ))
secs=$(( next - MARGIN - NOW ))
(( secs > MAX )) && secs=$MAX
(( secs < MIN )) && secs=0
echo "$secs"
