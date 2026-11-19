# Ambiente integrado CRM + KAS (isolado e descartável)

Itens 7 e 9 da matriz "comando original × entregue". Um comando sobe tudo, roda os cenários e apaga tudo no fim.

## O que sobe

| Peça | O que é | Real ou simulado |
|---|---|---|
| MongoDB 7 (replica set de 1 nó) | `127.0.0.1:27317`, banco `isolado_crm` | descartável (Docker) |
| Redis 7 | `127.0.0.1:6579` | descartável (Docker) |
| Reaper do CRM | `config/cronManager.js` + `crons/eventReaper.cron.js` | **código real do CRM** (`scripts/isolado/crmReaper.mjs`) |
| Worker do CRM | `claimEvent`/`renewLease`/`completeOwned` de `eventOwnership.js` | **código real**; o processo é morto com `kill -9` para deixar posses abandonadas |
| OutboxDispatcher do CRM | `infrastructure/outbox/outboxPattern.js` | **código real**, publicando em BullMQ |
| alertService do CRM | `infrastructure/alerts/alertService.js` | real, mandando para um webhook **simulado** (`alert-sink.mjs`) |
| kas-event-reaper | executor `-protocol v2` | binário real |
| kas-outbox-recovery | executor | binário real |
| Central de alertas da KAS | embutida nos dois binários | real, saída em log estruturado |
| Observador | change stream do banco (`scripts/isolado/observer.mjs`) | prova independente dos logs |

Nada lê `.env`. Os processos do CRM sobem com `env -i` e só as variáveis do ambiente isolado. `scripts/isolado/_guard.mjs` recusa MongoDB/Redis fora de loopback, URI `+srv` e banco sem prefixo `isolado_`. Nenhum dado real; nenhum WhatsApp, e-mail, Atlas ou Render.

## Como rodar

Requisitos: Docker, Go 1.25+, Node 20+, e os dois repositórios lado a lado com `npm ci` feito no CRM.

```
deploy/integrado/run.sh
```

Para manter contêineres e logs:

```
KEEP=1 deploy/integrado/run.sh
```

Se o CRM estiver em outra pasta:

```
CRM_DIR=/caminho/fono-inova-crm-back deploy/integrado/run.sh
```

Leva cerca de 6 minutos. Termina com `TUDO OK` (código 0) ou com a lista do que falhou (código 1).

## Cenários e o que cada um prova

**Item 7 (cortes: reaper, recuperação de failed, alertas)**

| # | Cenário | Verificação |
|---|---|---|
| 7.1 | Worker real morre com 150 eventos; Node e KAS disputam a posse | 150/150 recuperados uma vez; o observador confirma que cada recuperação foi gravada na mesma transação que a confirmação da posse |
| 7.2 | Entra um segundo executor KAS (3 candidatos) | 120/120 uma vez, nenhuma escrita fora da posse |
| 7.3 | MongoDB pausado 8 s com eventos presos | 100/100 depois da volta; nenhum processo cai |
| 7.4 | O dono atual leva `kill -9` (não devolve a posse) | outro assume depois do prazo; 80/80 |
| 7.5 | Evento na 3ª recuperação; depois o canal de alerta cai | alerta crítico chega ao webhook simulado; sem canal, o reaper segue e grava o `dead_letter` |
| 7.6 | Worker que perdeu a posse tenta concluir com token antigo | recusado em todos |
| 7.7 | Outbox com falhas + 5 eventos sem fila; Redis pausado; um recuperador leva `kill -9` | 60/60 republicados; exatamente 180 jobs (60 × 3 filas); os 5 sem fila desistidos com alerta |

**Item 9 (reversão Node → Go → Node)**

1. Node dono (`EVENT_REAPER_OWNER=node`, `EVENT_REAPER_SHARED_LEASE=on`).
2. O Go sobe **antes** de o Node sair (erro de operação simulado): o observador mostra 0 escritas do Go enquanto o Node segura a posse.
3. Node reinicia com `EVENT_REAPER_OWNER=kas` (SIGTERM devolve a posse): não inicia o reaper; o Go assume e recupera.
4. O Go **trava com a posse** (SIGSTOP) e o Node volta como dono: o Node espera o prazo vencer e assume.
5. O Go antigo acorda (SIGCONT): 0 escritas; os ciclos dele são recusados (`not_leader`).
6. Nenhum evento com posse vencida esquecido em `processing`; na reversão inteira, todas as 190 recuperações foram gravadas dentro da posse e nenhuma duas vezes.

## Resultado da rodada de 19/11/2026

Todas as verificações passaram (`TUDO OK`). Observador da reversão inteira: 190 recuperações, 190 cercadas, 0 fora da posse, 0 duplicadas; donos em sequência `crm-node → kas-rev → crm-node`.

## O que este ambiente não prova

- Redis pausado não fez o dispatcher do CRM marcar `failed` (o cliente espera a volta); as falhas do 7.7 foram semeadas no formato que o dispatcher grava. A pausa provou só que nada se perde nem duplica quando o Redis volta.
- A perda de posse **no meio** de um ciclo do Go (fence dentro da transação) é coberta pelos testes de unidade da KAS e pelo teste do CRM `reaperLease.mongo.nodetest.mjs`; aqui o Go acordado só tenta renovar e é recusado antes de escrever.
- Canal real de alertas, hospedagem da KAS e credenciais de produção não fazem parte (decisões de ativação).
