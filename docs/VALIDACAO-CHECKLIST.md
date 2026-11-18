# Checklist de validação da KAS Core (cortes do CRM)

Atualizado em 18/11/2026. Nenhum corte está ligado em produção. O checklist do lado do CRM fica em `fono-inova-crm-back/docs/VALIDACAO-CHECKLIST.md`.

| Corte | O que sai do Node | PR | Como reproduzir | Evidência | Estado |
|---|---|---|---|---|---|
| Reaper do Event Store (v1 e v2 com posse) | `crons/eventReaper.cron.js`, por flag, depois da sombra | #9, #10 (mergeados) | `scripts/validate-event-reaper.sh` | `docs/event-reaper/EVIDENCIAS.md`; oráculos v1 e v2 | código pronto, não ativado |
| Recuperação de `failed` do Outbox | nada (o Node não fazia isso) | #11 | `scripts/validate-outbox-recovery.sh` | `docs/outbox-recovery/README.md` §4; oráculo: 1.039 esquecidos hoje → 0 | PR aberto |

Fora desta rodada, por instrução:

- a retenção do Outbox, que apaga dados;
- a entrada de webhooks e pagamentos;
- o dispatcher do Outbox, porque reproduziria o protocolo do BullMQ.

Para ativar qualquer corte, faltam:

- o portão G (hospedagem) e o TLS;
- usuários Mongo restritos;
- 5 dias de sombra em produção;
- um canal de alerta monitorado;
- OK explícito para escrita.
