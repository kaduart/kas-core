# kas-outbox-recovery: recuperação de eventos `failed` do Outbox do CRM

> Sombra em produção (só leitura, separada da sombra do reaper): ver [SOMBRA.md](SOMBRA.md).

Peças 1 e 2 do roadmap ("Outbox em sombra" e "Recuperação de `failed` com escrita atrás de flag"). Tudo foi feito localmente, com dados fictícios. Nada aqui foi ligado em produção.

## 1. A responsabilidade e o que muda no Node

| | Antes (CRM hoje) | Com a KAS |
|---|---|---|
| Quem publica | `publishPendingEvents` (Node) | continua no Node, sem mudança |
| Evento que falha ao publicar | vira `failed` e nunca mais é selecionado (o dispatcher só lê `pending`) | a KAS devolve para `pending`, e o Node publica de novo |
| Evento sem fila (`UNKNOWN_EVENT_TYPE`) | `failed` para sempre, sem aviso | marcado como desistido e com alerta |
| Evento que falhou 5 vezes | (não chega lá: para na 1ª) | marcado como desistido e com alerta |

**O que sai do Node:** nada. O Node não faz essa recuperação hoje; ela passa a existir em Go. Por isso **nenhum caminho do Node é desligado** neste corte. O dispatcher, `ENABLE_OUTBOX_DISPATCHER` e `ENABLE_RECONCILIATION_OUTBOX` continuam como estão.

**Regra clínica:** nenhuma. O pacote não sabe o que o evento significa, não lê o `payload` e não conhece modelo clínico.

## 2. Separação: política genérica e adaptador legado

- `internal/outboxrecovery` é a política pura e o runner.
  - A entrada é reduzida a `status`, `attempts`, o instante de backoff, uma classificação "erro permanente" e a contabilidade própria.
  - Não conhece MongoDB, BullMQ nem o CRM.
  - Pode servir a outro outbox, de outro cliente.
- `internal/legacy/crmoutbox` é o único lugar que conhece os nomes de campo do `OutboxModel.js` (coleção `outboxes`).
  - Lê só `_id`, `status`, `attempts`, `scheduledAt`, `eventType` e `createdAt` (estes dois só para o relatório da sombra: tipo e idade, nunca para decidir), os 64 primeiros caracteres de `lastError` (cortados no servidor, só para classificar) e `kasRecovery`.
  - Um teste verifica que todo `find` enviado ao Mongo usa exatamente essa projeção.
- `cmd/kas-outbox-recovery` é o binário.
  - Modo `shadow` é o padrão.
  - Para escrever, é preciso `-mode executor -executor-confirm-db <db>`.
  - Um host que não seja loopback é recusado.

### Política (ordem fixa, coberta por teste)

1. Status diferente de `failed`: nada.
2. Já desistido pela KAS: nada.
3. `attempts` ilegível (null, texto, fracionário) ou contabilidade ilegível: nada, só relatório.
4. `lastError` com prefixo permanente (`UNKNOWN_EVENT_TYPE`): desiste e alerta.
5. `attempts >= 5`: desiste e alerta.
6. A KAS já recuperou a entrada 5 vezes: desiste e alerta. Isso protege contra um dispatcher que falhe sem contar.
7. O backoff do próprio Node (`scheduledAt`) ainda não venceu: espera.
8. Nos demais casos: recupera.

### Escrita

- **Recuperar:** um `updateOne` condicional nos valores observados (`_id`, `status: failed`, `attempts`, `scheduledAt`, `kasRecovery.count`, sem `exhaustedAt`).
  - Grava `status: pending`, `scheduledAt: agora`, `$inc kasRecovery.count` e `kasRecovery.lastRecoveredAt`/`lastFromAttempts`.
  - `scheduledAt` é regravado porque um valor ausente deixaria a entrada presa em `pending`.
- **Desistir:** o mesmo filtro condicional.
  - O status continua `failed` (o enum do CRM não tem `dead`).
  - Grava só `kasRecovery.exhaustedAt`, `reason` e `exhaustedAtAttempts`.
  - Nada no CRM lê `kasRecovery`.
- **Sem lease nem líder:** a condição sobre os valores observados basta.
  - Dois recuperadores, recuperador e dispatcher, ou recuperador reiniciado nunca aplicam a mesma ocorrência de falha duas vezes.
  - Quem perde a corrida conta `lostRaces` e segue.

## 3. Garantias e o que não é prometido

- **Pelo menos uma vez, não exatamente uma vez.**
  - Ao republicar, o Node chama `queue.add` com `jobId = eventId__fila`.
  - O BullMQ ignora um jobId que ainda existe no Redis. Se o job já tiver sido removido (`removeOnComplete`), o consumidor recebe de novo.
  - No oráculo, 288 de 1.965 jobs foram re-adicionados depois de uma publicação parcial em várias filas, e todos foram absorvidos pelo jobId. Em produção isso depende da retenção de jobs; os consumidores precisam deduplicar por `eventId`.
- **Achado O-1 (CRM, não corrigido aqui):** `workers/registry.js` liga o dispatcher em dois grupos (`outbox` e `reconciliation`), ambos ligados por padrão. Se os dois grupos rodarem no mesmo processo, há dois dispatchers lendo os mesmos `pending`, o que é mais uma fonte de publicação duplicada. Confirmar no Render quais grupos o worker sobe.
- **Achado O-2:** `cleanupOutbox` nunca é chamado. Hoje há 52% de `failed` esquecidos no corpus fictício. A retenção ficou fora desta rodada por instrução.

## 4. Evidências

Os comandos estão em `scripts/validate-outbox-recovery.sh`. O script sobe o próprio Mongo descartável na porta 27118 e o remove no fim.

| Cenário | Teste | Resultado local (18/11) |
|---|---|---|
| Política, ordem dos casos | `internal/outboxrecovery/policy_test.go` | 13 casos ok |
| Sombra não escreve e não lê payload (monitor de comandos) | `TestShadowWritesNothingAndNeverReadsPayload` | ok |
| Executor igual a uma leitura independente, 3.000 entradas com tipos estranhos; repetição não faz nada | `TestExecutorMatchesAnIndependentReading` | ok |
| 6 recuperadores simultâneos, 2.000 entradas | `TestConcurrentRecoverersApplyEachFailureOnce` | cada falha aplicada 1 vez (9.700 corridas perdidas, inofensivas) |
| Corrida com o dispatcher entre a leitura e a escrita | `TestLosesTheRaceAgainstTheDispatcher` | nada escrito em quem mudou |
| Queda no meio do lote (4 quedas) e reinício | `TestRestartAfterACrashConverges` | estado final idêntico à execução sem queda |
| Reversão | `TestUnmarkReversesExhaustOnly` | campos do dispatcher restaurados |
| Censo da sombra igual a contagem direta | `TestCensusMatchesDirectCounts` | ok |
| Guardas do binário | `cmd/kas-outbox-recovery/main_test.go` | 8 recusas |
| **Node x Go**: dispatcher original do CRM (`f560f63`, hash conferido) com e sem a KAS, 2.000 entradas com plano de falhas | `TestOracleCRMDispatcherWithAndWithoutRecovery` | ver abaixo |
| Ponta a ponta com binário, kill -9, 3 executores e reversão | `scripts/validate-outbox-recovery.sh` | todos os cenários passaram |

Resultado do oráculo, em que cada entrada é comparada com uma previsão independente feita a partir do plano de falhas:

| | Publicadas | Desistidas com alerta | `failed` esquecidas | Jobs |
|---|---|---|---|---|
| CRM hoje | 961 | 0 | **1.039** | 1.012 |
| CRM + KAS (rodadas, 2 recuperadores) | 1.614 | 386 | **0** | 1.965 |
| CRM + KAS rodando ao mesmo tempo | 1.614 | 386 | **0** | 1.965 |

O oráculo troca só duas importações do dispatcher, `queueConfig` e `eventPublisher`, por stand-ins:

- uma fila em memória com a regra de jobId do BullMQ;
- um mapa fictício de eventos para filas.

O relógio é um `Date` com escopo de módulo, injetado só no dispatcher. A primeira versão trocava o `Date` global, e o driver do Mongo derrubava a conexão de forma intermitente; isso foi corrigido. O arquivo em disco não é modificado.

## 5. Reversão

1. **Desligar:** pare o processo. O CRM volta exatamente ao comportamento de hoje, porque nunca seleciona `failed`.
2. **Desfazer as marcas:** `kas-outbox-recovery -db <db> -executor-confirm-db <db> -unmark all`, ou `-unmark <reason>` para um motivo só.
3. **O que não se desfaz:** uma entrada recuperada que já foi republicada. Isso é intencional, e é o motivo de exigir deduplicação no consumidor.

## 6. Bloqueios para ativar

- Portão G: onde a KAS roda 24h, e D2 (TLS).
- Um usuário Mongo só leitura para a sombra, e outro com `update` só em `outboxes` para o executor.
- Rodar a sombra por 5 dias úteis contra produção e revisar o relatório.
- Um canal de alerta monitorado. Hoje o alerta vai só para o log, em nível ERROR.
- Confirmar O-1, ou seja, quantos dispatchers sobem no worker.
- Confirmar que os consumidores das filas do Outbox deduplicam por `eventId`.
- O índice `{status:1, _id:1}` é opcional: o CRM já declara `{status:1, scheduledAt:1}`. `autoIndex` está desligado em produção, então qualquer índice precisa de `-ensure-indexes` com OK.
- OK explícito para escrita em produção.
