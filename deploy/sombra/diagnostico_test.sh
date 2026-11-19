#!/usr/bin/env bash
# Os diagnósticos públicos nunca imprimem senha nem host.
set -euo pipefail
D="$(dirname "$0")"
SENHA=S3nhaSecreta987; HOST=cluster0.zz9.mongodb.net
out=$(URI="mongodb+srv://kas_reaper_shadow:$SENHA@$HOST/?readPreference=secondaryPreferred" "$D/forma-uri.sh")
echo "$out"
! grep -q "$SENHA" <<<"$out" && ! grep -q "$HOST" <<<"$out" || { echo "FALHOU: forma-uri vazou valor"; exit 1; }
grep -q 'esquema=mongodb+srv' <<<"$out" && grep -q 'senha presente' <<<"$out"
t=$(mktemp); printf '{"level":"ERROR","msg":"mongo connect failed","reason":"error parsing uri: lookup _mongodb._tcp.%s on 8.8.8.8:53: no such host"}\n' "$HOST" > "$t"
out=$("$D/tipo-erro.sh" "$t"); echo "$out"
! grep -q "$HOST" <<<"$out" || { echo "FALHOU: tipo-erro vazou host"; exit 1; }
grep -q 'host do cluster não encontrado' <<<"$out"
printf '{"level":"ERROR","msg":"%s","reason":"x"}\n' "$SENHA" > "$t"
out=$("$D/tipo-erro.sh" "$t"); ! grep -q "$SENHA" <<<"$out" || { echo "FALHOU: msg desconhecida vazou"; exit 1; }
echo "diagnóstico OK"
