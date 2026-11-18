# Reaper do EventStore: primeiro corte da migração do CRM para a KAS

Data: 18/11/2026. Branch: `kas-core/event-reaper`. Legado de referência: `kaduart/fono-inova-crm-back` no commit `10d7e75a` (18/11/2026, já com o PR #72).

**Estado:** código, testes e validação local prontos. **Nada roda em produção.** Nesta rodada não houve conexão ao Mongo de produção, criação de usuários nem mudança no CRM ou no Render. O executor só foi exercitado em bancos descartáveis.

## 1. Qual responsabilidade sai do Node

| Sai do Node (quando ativado) | Continua no Node |
|---|---|
| A rotina `crons/eventReaper.cron.js`: a cada 5 min, achar eventos do `eventstore` presos em `processing` e devolvê-los para `pending` ou `failed` | O modelo `models/EventStore.js` e a coleção `eventstore` (o CRM continua dono do dado) |
| O registro `startCron('eventReaper', ...)` em `config/cronManager.js:55-56` | `processWithGuarantees`, `markEventProcessed`, `eventExists`, os workers BullMQ e todas as regras de negócio |

A KAS lê só `eventId`, `eventType`, `status`, `createdAt`, `attempts` (e o futuro `processingToken`), e escreve só `status` e `error`. Ela nunca lê `payload`, `metadata` nem campos de agregado. Um teste olha os comandos enviados ao Mongo para garantir isso.

## 2. Comportamento efetivo do reaper Node (mapeado em `10d7e75`)

| Aspecto | O que o código faz | Onde |
|---|---|---|
| Onde roda | Na API (`node server.js`, 24h), porque `ENABLE_CRONS !== 'false'` chama `startAllCrons()`. `workers/entrypoints/cron-worker.js` também chamaria, mas não está no Render (inventário de 18/11). | `server.js:931-932`, `config/cronManager.js:52-56` |
| Intervalo | Primeira execução 2 min após subir; depois `setInterval` de 5 min | `eventReaper.cron.js:138-144` |
| Seleção | `find({status:'processing', createdAt:{$lt: agora-15min}}).sort({createdAt:1}).limit(100).lean()` | `eventReaper.cron.js:24-30` |
| Decisão | `attempts < 3` vai para `pending`; senão vai para `failed`. Comparação JS sobre o valor cru (lean). | `eventReaper.cron.js:46-47` |
| Escrita | `updateOne({eventId}, {$set:{status, error:{message, recoveredAt, recoveredBy}}})`. O filtro tem só o `eventId`, e `attempts` não é incrementado. | `eventReaper.cron.js:49-61` |
| O que fica gravado | `error` vira **só** `{message}`. O Mongoose (modo strict) descarta `recoveredAt`/`recoveredBy`, e `stack`/`code` anteriores se perdem. Não grava `updatedAt` (o schema só tem `createdAt`). | confirmado pelo oráculo; `models/EventStore.js:101-105,117` |
| Erros | Cada evento tem seu `try/catch`: um erro não para o lote. Erro de consulta só gera log. | `eventReaper.cron.js:86-95,119-121` |
| Concorrência | `isRunning` impede sobreposição **no mesmo processo**. Não há trava entre processos. `cronManager` é singleton por processo. | `eventReaper.cron.js:102-107`, `cronManager.js:9-18` |
| Quem coloca em `processing` | `processWithGuarantees` faz `updateOne({eventId}, {$set:{status:'processing'}})`, sem marca de tempo e sem dono | `infrastructure/events/eventStoreService.js:414-417` |
| Quem conclui | `markEventProcessed` faz `doc.save()` por `_id`, **sem condição** | `eventStoreService.js:135-160`, `models/EventStore.js:238-243` |
| Por que importa | `eventExists` considera `processing` como já feito; os workers pulam o evento. Um `processing` preso bloqueia o reprocessamento até o reaper agir. | `models/EventStore.js:174-183`, ex.: `workers/createAppointmentWorker.js:63` |
| O que **não** acontece | Nada lê `pending` do `eventstore` para reprocessar (`getPendingEvents` não tem chamador). Voltar para `pending` só desbloqueia: quem reexecuta é o BullMQ. | `eventStoreService.js:126` |

Achados laterais, sem efeito nesta rodada: `infrastructure/workers/watchdog.js` e `watchdogBullmq.js` também mexem em `processing`, mas não são importados por ninguém (código morto) e filtram por `updatedAt`, que não existe no schema. Pelo mesmo motivo, `eventMonitor.js:501` conta `processing` antigos por `updatedAt` e sempre retorna 0.

## 3. Arquitetura em Go

```
cmd/kas-event-reaper            binário: flags, modos, guardas, /metrics opcional
internal/eventreaper            política pura (Decide), Runner (ciclo, sombra, executor), Stats
internal/legacy/crmeventstore   adaptador Mongo do legado: consulta, escrita condicional, lease com fencing
internal/mongotest              banco Mongo descartável para testes (só loopback)
legacy-oracle/eventreaper       o reaper Node SEM MODIFICAÇÃO, usado como oráculo nos testes
```

A política não conhece Mongo nem CRM. O adaptador é o único lugar com nomes de campo do legado. Não há modelo clínico em lugar nenhum.

**Transição atômica.** Cada escrita roda numa transação que (1) confirma o lease do executor e (2) faz um `updateOne` condicional: `eventId` + `status:'processing'` + `createdAt < corte` + `attempts` igual ao observado + `processingToken` (igual ou ausente). Se alguém mudou o evento entre a leitura e a escrita, o resultado é `lost_race` e nada é gravado.

**Lease com fencing.** Há um único documento em `kas_event_reaper_lease`, com um token aleatório que muda a cada troca de dono. O relógio é o do servidor (`$$NOW`). Toda escrita grava no documento do lease dentro da mesma transação, então um executor antigo cujo lease expirou e foi tomado não consegue mais escrever (`ErrLeaseLost`).

## 4. Modos

| | Sombra (`-mode shadow`, padrão) | Executor (`-mode executor`) |
|---|---|---|
| Escreve? | Nunca (validado por hash do banco) | Sim, com escrita condicional e lease |
| Saída | Previsões por `eventId`. No ciclo seguinte compara com o banco: `agree`, `still_processing`, `diverge`, `gone` | `applied`, `lost_race`, `lease_lost`, `error` |
| Leitura | `secondaryPreferred` | primário (transação) |
| Guardas | — | exige `-executor-confirm-db` igual a `-db`; recusa host que não seja loopback, a menos que se passe `-allow-remote-executor` (**não autorizado nesta fase**) |

A URI vem só de variável de ambiente (`-mongo-uri-env`, padrão `KAS_REAPER_MONGO_URI`), nunca de argumento. O relatório (`-report`, JSON Lines) e o `/metrics` (`-metrics-addr`, só loopback) carregam apenas identificadores e estados.

## 5. Como ativar, desativar e reverter (etapas futuras, cada uma com OK do Kadu)

Nada disto foi feito. A ordem foi pensada para **nunca haver dois executores ativos**.

**Pré-requisitos**
1. Um usuário Mongo próprio da KAS: só leitura para a sombra; depois, escrita só em `eventstore` (campos `status` e `error`) e na coleção `kas_event_reaper_lease`.
2. Um host sempre ligado para o executor (Portão G do roadmap). A sombra pode rodar do PC em horário comercial.
3. **Uma mudança pequena no CRM:** hoje a única forma de desligar o reaper Node é `ENABLE_CRONS=false`, que derruba **todos** os crons, inclusive o `workerScheduler`. É preciso uma chave só para ele, por exemplo `EVENT_REAPER_OWNER=node|kas` lida em `cronManager.js` antes de `startCron('eventReaper', ...)`. Proposta opcional, mais forte: o cron Node também pula a execução se houver lease vivo em `kas_event_reaper_lease`.

**Etapa A, sombra em produção (só leitura).** Rodar `-mode shadow` ao lado do cron Node por alguns dias. Critério: os `shadow_checks` dão `agree` e todo `diverge` é explicado (ver DV-1).

**Etapa B, troca de dono**
1. Publicar o CRM com a chave nova ainda em `node`. Não muda nada.
2. Trocar para `kas` e reiniciar a API. Confirmar nos logs que não aparece mais `Inicializando Event Reaper Cron` nem `[EventReaper]`.
3. Só então subir `kas-event-reaper -mode executor`. Uma lacuna de alguns minutos é inofensiva, porque o limite do reaper é 15 min.

**Reversão**
1. Parar o executor da KAS (SIGTERM). Ele libera o lease ao sair.
2. Conferir que nenhum `kas-event-reaper` está rodando e que o lease está expirado.
3. Trocar a chave para `node` e reiniciar a API.

O que a KAS já gravou continua válido: são as mesmas transições que o Node faria.

## 6. Como rodar a validação local

```bash
# testes unitários (sem banco)
go test ./internal/eventreaper/ ./cmd/kas-event-reaper/

# integração + oráculo (Mongo descartável em loopback, replica set de 1 nó)
docker run -d --name kasmongo -p 127.0.0.1:27017:27017 mongo:7 --replSet rs0 --bind_ip_all
docker exec kasmongo mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"127.0.0.1:27017"}]})'
(cd legacy-oracle/eventreaper && npm install)
KAS_TEST_MONGO_URI='mongodb://127.0.0.1:27017/?directConnection=true' \
KAS_TEST_ORACLE_DIR=$PWD/legacy-oracle/eventreaper go test ./internal/legacy/... -v

# ponta a ponta: sombra, dois executores, pausa do banco, kill -9 do líder
scripts/validate-event-reaper.sh
```

## 7. Evidências desta rodada (18/11/2026)

Os resultados abaixo estão em `EVIDENCIAS.md`.

- **Oráculo:** 10.000 eventos fictícios, com todas as formas de `attempts`, idades em volta do limite e todos os status. O reaper Node sem modificação e o executor Go rodaram em cópias idênticas, deram **0 divergências documento a documento** e os mesmos totais (879 `pending`, 746 `failed`, 18 ciclos). Um teste de mutação (trocar 3 por 4) faz o oráculo falhar.
- **Concorrência:** dois executores sem lease, em 1.000 eventos, moveram cada evento exatamente uma vez (só a escrita condicional os protegeu). Dois executores com lease, em 600 eventos, deram o mesmo resultado.
- **Posse:** um ex-dono com lease expirado e tomado não escreve (`ErrLeaseLost`).
- **Reinício:** processo cortado após 37 escritas; o próximo terminou os 213 restantes sem repetir nenhum.
- **Falha do banco:** com o banco inalcançável, o ciclo é reportado como erro, sem travar. No ponta a ponta, o Mongo pausado por 8 s gerou 4 ciclos com erro, e os executores voltaram sozinhos.
- **Ponta a ponta:** sombra não alterou o hash do banco; 2.314 elegíveis, 2.314 aplicados, 0 duplicados; kill -9 do líder e o outro assumiu.
- **Suíte:** `go test ./...`, `go vet ./...`, `staticcheck` e `go test -race ./...` verdes (com Postgres e Mongo descartáveis), além do `npm test` do node-integration.
