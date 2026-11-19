# Sombra da recuperação do Outbox (entradas `failed`) em produção, só leitura

Preparada em 19/11/2026. **Desligada.** Não roda em produção até: (1) o Kadu criar papel, usuário, ambiente e segredos próprios (seção 5), (2) um teste manual de 15 min ser avaliado, e (3) um OK separado para ligar a agenda.

## 1. O que esta sombra cobre (e o que não cobre)

Ela cobre **entradas do Outbox transacional do CRM (`outboxes`) com `status: failed`**. Uma entrada do Outbox é um **evento de domínio** que o CRM gravou na mesma transação da alteração, para o dispatcher (`publishPendingEvents`) publicar nas filas BullMQ. Ela **não é uma mensagem enviada**. Quando a publicação falha, a entrada vira `failed` e o dispatcher nunca mais a seleciona, porque só lê `pending`.

A recuperação (kas-core #11) devolveria essas entradas para `pending`, e o próprio dispatcher do Node publicaria de novo. **A sombra não faz isso.** Ela só lê e registra o que a recuperação faria.

### Tipos de evento que chegam ao Outbox (inventário do código do CRM, `main` 529ca0f)

Levantado pelos `saveToOutbox(...)` do CRM. O destino é o `eventToQueueMap`, embutido em `internal/legacy/crmoutbox/queues.json`. A CI compara esse arquivo com o CRM a cada execução.

| Tipo | Quem grava | Destino (filas) | Pode enviar a uma pessoa? |
|---|---|---|---|
| APPOINTMENT_CREATED | createAppointmentService | notification, patient-projection, clinical-orchestrator | **sim** (notification) |
| APPOINTMENT_SCHEDULED | domínio de agendamento | notification, patient-projection, clinical-orchestrator | **sim** |
| APPOINTMENT_RESCHEDULED | domínio de agendamento | notification, patient-projection | **sim** |
| APPOINTMENT_UPDATED | comandos de agendamento | notification, patient-projection, appointment-integration | **sim** |
| APPOINTMENT_CANCELLED | cancelAppointmentCommand, packageSettlementClosure | sync-medical, patient-projection, clinical-orchestrator, package-projection | não |
| APPOINTMENT_DELETED | deleteAppointmentCommand | sync-medical, patient-projection, package-projection | não |
| APPOINTMENT_CONFIRMED | confirmPreAgendamentoCommand | **sem rota** (UNKNOWN_EVENT_TYPE) | não |
| SESSION_COMPLETED / SESSION_SCHEDULED / SESSION_CANCELLED / SESSION_NO_SHOW | completeSessionService.v2 e domínio clínico | package-projection, patient-projection, clinical-session, … | não |
| PACKAGE_CREATED / PACKAGE_UPDATED / PACKAGE_DELETED | comandos de pacote | package-projection, package-validation, patient-projection | não |
| PAYMENT_STATUS_CHANGED | paymentStatusService | patient-projection, package-projection | não |
| PATIENT_REGISTERED / PATIENT_UPDATED / PATIENT_PHONE_CHANGED / PATIENT_DATA_CONFIRMED / PATIENT_VIEW_REBUILD_REQUESTED | domínio clínico | patient-projection | não |
| PATIENT_DELETE_REQUESTED | domínio clínico | patient-processing | não |
| INSURANCE_GUIDE_CREATED / INSURANCE_PAYMENT_AUTO_SETTLED / LIMINAR_CONTRACT_CREATED | convênio e liminar | patient-projection (e balance-update) | não |
| INSURANCE_BATCH_PROCESSING / _COMPLETED / _PARTIAL_SUCCESS / INSURANCE_ITEM_RETRYING | insuranceOrchestratorWorker | **sem rota** (UNKNOWN_EVENT_TYPE) | não |

"Pode enviar a uma pessoa" significa que a fila de destino tem um worker que pode mandar WhatsApp ou e-mail: `notification`, `whatsapp-notification`, `followup-processing`, `whatsapp-auto-reply` ou `lead-orchestrator-v2` (`internal/legacy/crmoutbox/queues.go`). Isso **não** quer dizer que vai enviar: depende do worker e das proteções do CRM (outbound_deliveries). Quer dizer que recuperar a entrada **chega** a um caminho de envio. A tabela da produção sai do relatório, que conta o que realmente existe no `outboxes`.

## 2. O que a sombra registra

A cada 5 min, para cada entrada `failed` ainda não desistida:
- **elegibilidade**, na política do #11 sem mudança: `recover` (`retry`), `exhaust` (`permanent_error`, `attempts_exhausted`, `recovery_limit`) ou `none` (`backoff_not_elapsed`, `malformed_entry`);
- **tentativas** (`attempts` do dispatcher) e recuperações anteriores;
- **idade** (`agora − createdAt`);
- **destino proposto** (filas do `eventToQueueMap`, ou "sem rota");
- um **censo** do Outbox (contagem por status e `pending` atrasadas).

Lê só `_id`, `status`, `attempts`, `scheduledAt`, `eventType`, `createdAt`, os 64 primeiros caracteres de `lastError` (só para classificar) e `kasRecovery`. **Nunca lê `payload`**, `eventId`, `correlationId` ou `aggregate*`. Um teste confere a projeção de todo `find`.

Público (resumo da execução): só contagens, tipos de evento e nomes de fila. Relatório completo (com o id de cada entrada) e log: criptografados com AES256, artifact de 14 dias.

## 3. Garantias de "só observa"

| Camada | Garantia |
|---|---|
| Servidor | Papel `kasOutboxShadowRead` = **só `find` em `fono_inova_prod.outboxes`**. Não lê `eventstore`, `patients` nem nada mais. |
| Binário | `-require-read-only on` prova pelo servidor (`connectionStatus`) que o usuário só tem ações de leitura e recusa iniciar caso contrário (código 2). Com `-require-read-only on`, o modo executor, `-unmark` e `-ensure-indexes` são recusados antes de conectar. |
| Código | Em modo `shadow`, o runner não chama `Recover`/`Exhaust`. O resumo reprova (O1) qualquer recuperação, desistência ou disputa registrada. |
| Fila | A sombra não conhece Redis/BullMQ: não tem como publicar. |
| Notificação | Alertas da sombra vão só para o log criptografado; não há canal de entrega configurado. |

## 4. Separação da sombra do reaper

| | Sombra do reaper | Sombra do Outbox |
|---|---|---|
| Workflow | `sombra-reaper.yml` / `-resumo` | `sombra-outbox.yml` / `-resumo` |
| Ambiente GitHub | `sombra` | `sombra-outbox` |
| Usuário Atlas | `kas_reaper_shadow` | `kas_outbox_shadow` |
| Papel | `kasReaperShadowRead` (find em `eventstore`) | `kasOutboxShadowRead` (find em `outboxes`) |
| Segredos | `KAS_REAPER_SHADOW_MONGO_URI`, `SOMBRA_REPORT_PASSPHRASE` | `KAS_OUTBOX_SHADOW_MONGO_URI`, `SOMBRA_OUTBOX_REPORT_PASSPHRASE` |
| Agenda | `SOMBRA_AGENDA_ATIVA` (xx:05) | `SOMBRA_OUTBOX_AGENDA_ATIVA` (xx:35) |
| Artifact | `sombra-reaper-<run>` | `sombra-outbox-<run>` |

Nada da sombra do reaper foi alterado por este pacote: nem workflow, nem binário, nem scripts. O usuário atual **não** foi ampliado.

## 5. Pré-condições (Kadu, ~15 min, só depois do merge)

1. Atlas, projeto Producao → Database Access → Custom Roles → criar `kasOutboxShadowRead`: ação **find**, banco `fono_inova_prod`, coleção `outboxes` (igual a `deploy/sombra-outbox/atlas-role.json`).
2. Database Users → criar `kas_outbox_shadow`, senha gerada, **só** o papel `kasOutboxShadowRead`. Nenhum papel embutido.
3. GitHub → Settings → Environments → criar `sombra-outbox` com regra de branch `main`, e os segredos:
   - `KAS_OUTBOX_SHADOW_MONGO_URI` = `mongodb+srv://kas_outbox_shadow:SENHA@producao.rj6hivl.mongodb.net/?readPreference=secondaryPreferred`
   - `SOMBRA_OUTBOX_REPORT_PASSPHRASE` = uma frase nova (não reaproveitar a do reaper)
4. **Não** criar `SOMBRA_OUTBOX_AGENDA_ATIVA`. Actions → `sombra-outbox` → Run workflow, `main`, 15 min. Esperado: "usuário só leitura comprovado pelo servidor", saída 124 e O1 verde.
5. Avaliar. Só com OK separado criar `SOMBRA_OUTBOX_AGENDA_ATIVA=true`.

Parar: apagar a variável. Revogar: apagar o usuário no Atlas.

## 6. Critérios (kas-outbox-shadow-summary)

| | Critério | Passa quando |
|---|---|---|
| O1 | Nenhuma escrita | todas as execuções em modo sombra, com só leitura comprovado pelo servidor; 0 recuperações, desistências ou disputas |
| O2 | Erros sob controle | ≤ 1% dos ciclos com erro; no máximo 3 seguidos |
| O3 | Sem impacto no CRM | ciclo p95 ≤ 2 s; /health do CRM ≥ 99% de 200 |
| O4 | Período e cobertura | `-min-days` (5 no veredito); ≥ 90% dos intervalos de 5 min com ciclo sem erro; com `-since`/`-until`, medido contra a janela inteira |
| O5 | Há o que avaliar | **zero entradas `failed` no período = INCONCLUSIVO** (código 4) |

Códigos de saída: 0 passa; 1 reprova; 3 período curto; 4 inconclusivo; 2 uso errado.

## 7. Validação isolada (`deploy/sombra-outbox/validate.sh`, job `sombra-outbox` na CI)

Mongo 7 com autenticação e o mesmo papel do Atlas, Redis 7 e o **dispatcher real do CRM** (`scripts/isolado/crmDispatcher.mjs`). **Dados fictícios. Os números abaixo são do teste e não dizem nada sobre a produção.**

| | Cenário | Resultado esperado (teste fictício) |
|---|---|---|
| T1 | Servidor nega ao usuário da sombra insert, update, delete e createIndex no `outboxes`, e leitura de `eventstore` e `patients` | negado; leitura do `outboxes` permitida |
| T2 | Usuário readWrite, sem autenticação, ou executor com `-require-read-only on` | recusa (código 2); usuário da sombra confirmado com `[find]` |
| T3 | Só a sombra, 27 entradas failed fictícias | dbHash igual; 0 escritas no oplog; sem payload no relatório; resumo: recuperaria 18 (12 com destino `notification`), desistiria de 7 (4 por tentativas, 3 sem rota), aguardaria 2 |
| T4 | Dispatcher real do CRM ao lado, 40 pending | 40/40 publicadas; 0 jobs no Redis para failed; as 27 failed byte a byte iguais; nenhuma anotação `kasRecovery` |
| T5 | Sombra travada (SIGSTOP) e morta (kill -9) | dispatcher publica 30/30 e segue de pé |
| T6 | Banco pausado | ciclo com erro registrado e retomada sozinha; SIGTERM não vira erro |

## 8. Achados para a etapa seguinte (executor), não para a sombra

- **Sem limite de idade na política.** Hoje a recuperação devolveria a `pending` uma entrada `failed` de qualquer idade. Para tipos cujo destino inclui `notification` (APPOINTMENT_CREATED, _SCHEDULED, _RESCHEDULED, _UPDATED), isso pode levar a um aviso atrasado (por exemplo, sobre um agendamento de dias atrás). O relatório já separa "recuperaria com destino que pode enviar a uma pessoa" **por idade**. A decisão de um limite (por exemplo, desistir acima de X horas para esses tipos) fica para antes do executor, com base nos números reais da sombra.
- **Tipos sem rota** (APPOINTMENT_CONFIRMED, INSURANCE_BATCH_*, INSURANCE_ITEM_RETRYING) nunca serão publicados. A política já desiste deles. A correção é no CRM (mapear a fila ou parar de gravar).
- **Publicação parcial.** O dispatcher publica fila por fila; uma falha no meio deixa jobs já criados. O jobId `eventId__fila` evita duplicar enquanto o job existir no Redis, mas não depois que ele é removido.
