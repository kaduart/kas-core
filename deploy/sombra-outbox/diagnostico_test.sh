#!/usr/bin/env bash
# Os diagnósticos públicos da sombra do Outbox nunca imprimem senha nem host.
set -euo pipefail
D="$(dirname "$0")"
SENHA=S3nhaSecreta987; HOST=cluster0.zz9.mongodb.net
out=$(URI="mongodb+srv://kas_outbox_shadow:$SENHA@$HOST/?readPreference=secondaryPreferred" "$D/forma-uri.sh")
echo "$out"
! grep -q "$SENHA" <<<"$out" && ! grep -q "$HOST" <<<"$out" || { echo "FALHOU: forma-uri vazou valor"; exit 1; }
grep -q 'usuário=kas_outbox_shadow' <<<"$out" && grep -q 'senha presente' <<<"$out"
t=$(mktemp); printf '{"level":"ERROR","msg":"mongo connect failed","reason":"auth error: AuthenticationFailed on %s"}\n' "$HOST" > "$t"
out=$("$D/../sombra/tipo-erro.sh" "$t"); echo "$out"
! grep -q "$HOST" <<<"$out" || { echo "FALHOU: tipo-erro vazou host"; exit 1; }
grep -q 'usuário ou senha recusados' <<<"$out"
echo "diagnóstico OK"
