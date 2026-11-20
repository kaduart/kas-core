# Amostra fictícia: estornos pelo saldo do recebimento

Gerada pelo exportador real do CRM (`services/attribution/fixtures/estornosFicticios.js` →
`toKasInputs`), com dados inventados. Regra `estorno-v3-saldo-por-recebimento`.
Período do relatório: 01/11 a 30/11/2026 (o histórico de outubro também está aqui, porque o
KAS guarda tudo e o relatório filtra por data).

| Caso | O que acontece | Esperado em novembro |
|---|---|---|
| 1 | Pagamento com R$ 100 antes do anúncio e R$ 300 depois; estorno exato dos R$ 100 e depois estorno geral de R$ 200 | gg-caso-1 = R$ 100; sem vínculo desse pagamento = R$ 0 |
| 2 | Estorno de R$ 50 só com o pagamento, antes de todo recebimento (R$ 500 recebidos depois) | gg-caso-2 = R$ 500; sem vínculo −R$ 50 (`estorno_antes_do_recebimento`) |
| 3 | Em outubro: R$ 200 antes do anúncio, R$ 200 depois e estorno exato dos primeiros R$ 200. Em novembro: estorno geral de R$ 100 | gg-caso-3 = −R$ 100 (o saldo da primeira parcela já era zero) |

Total: R$ 450 = comprovada R$ 500 + sem vínculo −R$ 50. O CRM dá o mesmo resultado
(`tests/unit/revenueAttribution.estornos.test.js`).
