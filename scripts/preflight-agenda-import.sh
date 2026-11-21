#!/usr/bin/env bash
# Pré-voo da importação real da Agenda (fase 3B). NÃO importa nada e NÃO lê
# documentos: só confere a configuração e pede ao MongoDB os planos das
# consultas do espelho. Hoje o CLI só aceita MongoDB em loopback (cópia local);
# apontar para o Atlas exige uma autorização nova e uma mudança de código.
#
# Uso:
#   KAS_MIRROR_MONGO_URI='mongodb://127.0.0.1:27018/?directConnection=true' \
#   KAS_MIRROR_KEY='(32+ bytes, guardado fora do git)' \
#   scripts/preflight-agenda-import.sh fono_espelho
#
# Sai com 0 só quando todas as consultas de appointments usam índice.
set -euo pipefail
DB="${1:?informe o banco da cópia local, ex.: fono_espelho}"
fail=0
ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FALHA %s\n' "$1"; fail=1; }

echo "== configuração"
[[ -n "${KAS_MIRROR_MONGO_URI:-}" ]] || { bad "KAS_MIRROR_MONGO_URI vazio"; exit 2; }
case "$KAS_MIRROR_MONGO_URI" in
  mongodb+srv://*) bad "URI +srv (Atlas): fora do escopo autorizado" ;;
  mongodb://127.0.0.1*|mongodb://localhost*|mongodb://\[::1\]*) ok "MongoDB em loopback (cópia local)" ;;
  *) bad "MongoDB fora do loopback: fora do escopo autorizado" ;;
esac
if [[ "${#KAS_MIRROR_KEY}" -ge 32 ]]; then ok "chave HMAC com ${#KAS_MIRROR_KEY} bytes"; else bad "KAS_MIRROR_KEY ausente ou curta (mínimo 32 bytes)"; fi
[[ "$DB" != *prod* ]] && ok "nome do banco não parece produção" || bad "nome do banco parece produção"
[[ $fail -eq 0 ]] || { echo "pré-voo interrompido"; exit 2; }

echo "== planos das consultas (sem ler documentos)"
plans=$(go run ./cmd/kas-scheduling-mirror -mode plan -mongo-db "$DB")
read -r -d '' CHECK <<'PY' || true
import json, sys
fail = 0
for p in json.load(sys.stdin):
    if p["collection"] == "appointments" and p["collection_scan"]:
        fail = 1
        print("  FALHA %s: leitura da coleção inteira; índice sugerido: %s" % (p["name"], p.get("advice", "?")))
    else:
        idx = " (índice %s)" % p["index"] if p.get("index") else ""
        print("  ok    %s: %s%s" % (p["name"], " > ".join(p["stages"]), idx))
sys.exit(fail)
PY
echo "$plans" | python3 -c "$CHECK" || fail=1

if [[ $fail -eq 0 ]]; then echo "pré-voo OK"; else echo "pré-voo com pendências (ver docs/agenda/FASE3B_IMPORTACAO_REAL.md)"; exit 1; fi
