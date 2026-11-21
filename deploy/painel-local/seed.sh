#!/bin/sh
# Carga do painel local. Pode rodar quantas vezes quiser: nada duplica.
#  1. cria (ou atualiza) a pessoa que entra no painel, com acesso às 4 empresas;
#  2. importa a Escola Exemplo (escola-demo) e a amostra fictícia na Clínica Aurora;
#     o lote tem id fixo, então a segunda carga só repete a resposta original.
# A Fono Inova fica sem dados de propósito: ela recebe o banco real da clínica depois.
set -eu
KAS=http://127.0.0.1:8089

i=0
until wget -q -O /dev/null "$KAS/readyz" 2>/dev/null; do
  i=$((i + 1)); [ "$i" -gt 90 ] && { echo "KAS Core não ficou pronto"; exit 1; }
  sleep 1
done

echo "== pessoa do painel: $KAS_PAINEL_EMAIL"
kas-core -config /etc/kas/config.json -events-store postgres -identity-store postgres \
  -identity-add-user "$KAS_PAINEL_EMAIL" -identity-user-name "$KAS_PAINEL_NOME" \
  -identity-memberships "fono-inova:admin,clinica-aurora:admin,odonto-sorriso:admin,escola-demo:admin"

load() { # SERVICE PASTA
  for kind in customers contacts receipts refunds; do
    kas-attribution-import -kas "$KAS" -service "$1" -token-env KAS_IMPORT_TOKEN -kind "$kind" \
      -file "/seed/attribution/$2/$kind.csv" -batch-id "painel-local-$2-$kind" -actor user.painel-local
  done
}
echo "== Escola Exemplo (escola-demo)"; load escola-sis escola-demo
echo "== Clínica Aurora (amostra fictícia)"; load aurora-sis amostra-fono
echo "== carga concluída"
