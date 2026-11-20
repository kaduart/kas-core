#!/usr/bin/env bash
# Decide a janela do resumo da sombra do reaper e imprime linhas KEY=valor (para $GITHUB_ENV).
# Entrada (ambiente): SINCE, UNTIL, MIN_DAYS (vindos do disparo manual; vazios = automático),
# JANELA_INICIO, JANELA_FIM (janela oficial), NOW (epoch, opcional; padrão = agora).
#   manual com since/until -> usa o que veio (MIN_DAYS vazio vira 0)
#   antes da janela        -> FASE=aquecimento, sem janela, MIN_DAYS=0
#   durante                -> FASE=parcial, SINCE=início, UNTIL=agora (arredondado a 5 min), MIN_DAYS=0
#   depois do fim          -> FASE=veredito, janela inteira, MIN_DAYS=5
set -euo pipefail
NOW=${NOW:-$(date -u +%s)}
if [ -n "${SINCE:-}" ] || [ -n "${UNTIL:-}" ]; then
  [ -n "${SINCE:-}" ] && [ -n "${UNTIL:-}" ] || { echo "since e until vão juntos" >&2; exit 1; }
  echo "FASE=manual"; echo "SINCE=$SINCE"; echo "UNTIL=$UNTIL"; echo "MIN_DAYS=${MIN_DAYS:-0}"; exit 0
fi
s=$(date -u -d "$JANELA_INICIO" +%s); e=$(date -u -d "$JANELA_FIM" +%s)
(( s < e )) || { echo "janela oficial inválida" >&2; exit 1; }
if (( NOW < s )); then
  echo "FASE=aquecimento (antes da janela oficial; não conta para o veredito)"
  echo "SINCE="; echo "UNTIL="; echo "MIN_DAYS=${MIN_DAYS:-0}"
elif (( NOW < e )); then
  u=$(( NOW / 300 * 300 ))
  echo "FASE=parcial da janela oficial"
  echo "SINCE=$JANELA_INICIO"; echo "UNTIL=$(date -u -d "@$u" +%Y-%m-%dT%H:%M:%SZ)"; echo "MIN_DAYS=${MIN_DAYS:-0}"
else
  echo "FASE=veredito (janela oficial completa)"
  echo "SINCE=$JANELA_INICIO"; echo "UNTIL=$JANELA_FIM"; echo "MIN_DAYS=${MIN_DAYS:-5}"
fi
