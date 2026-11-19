#!/usr/bin/env bash
# Lê o log da sombra (JSON Lines) e imprime só a mensagem fixa e o TIPO dos erros, nunca o texto
# do motivo (que pode citar host). Uso: deploy/sombra/tipo-erro.sh run/reaper.log
set -uo pipefail
f=${1:-run/reaper.log}
[ -f "$f" ] || { echo "erro: sem log"; exit 0; }
grep '"level":"ERROR"' "$f" | head -5 | while IFS= read -r l; do
  msg=$(printf '%s' "$l" | sed -n 's/.*"msg":"\([^"]*\)".*/\1/p')
  case "$l" in
    *"no such host"*|*"lookup _mongodb._tcp"*) t="host do cluster não encontrado no DNS (endereço errado)";;
    *"error parsing uri"*|*"unescaped"*|*"invalid port"*|*"scheme must be"*) t="URI com formato inválido";;
    *"uthentication failed"*|*"AuthenticationFailed"*|*"auth error"*) t="usuário ou senha recusados";;
    *"server selection"*|*"timed out"*|*"timeout"*|*"connection refused"*) t="sem conexão com o cluster (rede ou lista de IPs)";;
    *"not authorized"*|*"Unauthorized"*) t="permissão negada pelo servidor";;
    *) t="outro";;
  esac
  case "$msg" in
    "mongo connect failed"|"shadow_refused_not_read_only"|"MongoDB URI variable is not set"|"invalid MongoDB URI"|"cannot open report file") ;;
    *) msg="(mensagem fora da lista)";;
  esac
  echo "erro: $msg | tipo: $t"
done
exit 0
