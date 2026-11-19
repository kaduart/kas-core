# Ambiente integrado CRM + KAS (isolado e descartável)

Itens 7 e 9 da matriz "comando original × entregue", mais os casos da guarda do Node (G1–G5). **A KAS roda sempre com `-require-crm-node-lease`** (padrão do binário), como no procedimento de troca. Um comando sobe tudo, roda os cenários e apaga tudo no fim. É a **integração dos cortes implementados**, não uma validação da API inteira (ver "Escopo").

## Escopo: integração dos cortes, não da API

Este ambiente integra **só os cortes implementados**: o reaper do Event Store (Node e KAS), a posse de execução de eventos, a recuperação de Outbox `failed` e os alertas. **Não valida a API do CRM.** Nenhuma rota HTTP é chamada, nenhum controller, fila de negócio (agendamento, pagamento, WhatsApp) ou worker de domínio roda. O "worker do CRM" é um simulador que usa só as funções reais de posse (`claimEvent`/`renewLease`/`completeOwned`).

| Fluxo | Atravessa processos reais | Semeado ou simulado |
|---|---|---|
| 7.1, 7.2, 7.4 Recuperação de eventos presos | Processo do reaper do CRM (código real do cron) + 1 ou 2 binários `kas-event-reaper` + processo do worker do CRM morto com `kill -9` + MongoDB real (replica set) | Os eventos nascem por `insertMany` (`pending`, dados fictícios). A posse abandonada é real, criada pelo worker simulador antes do `kill -9` |
| 7.3 Banco indisponível | Os mesmos processos acima com o contêiner do MongoDB pausado 8 s (`docker pause`) | A falha é provocada (pausa do contêiner), não uma queda de rede real |
| 7.5 Alerta de `dead_letter` | Reaper real do CRM + `alertService` real mandando HTTP para um processo local | **Semeado:** o evento entra direto com `recoveryCount: 2` (não passou por duas recuperações reais). **Simulado:** o canal é `alert-sink.mjs`, não Slack/e-mail; a queda do canal é o processo dele morto |
| 7.6 Conclusão tardia com token antigo | Processo do worker simulador com as funções reais de posse + MongoDB | Tokens antigos são os gravados no cenário anterior |
| 7.7 Recuperação de Outbox `failed` | `OutboxDispatcher` real do CRM publicando no Redis real (BullMQ) + 2 binários `kas-outbox-recovery` (um morto com `kill -9`) + Redis pausado 6 s | **Semeado:** as 60 falhas entram direto como `failed` no formato que o dispatcher grava (`lastError: 'Connection is closed.'`). A pausa do Redis não fez o dispatcher marcar `failed`, então a falha real de publicação não foi reproduzida. Os 5 eventos sem fila também são semeados. Nenhum consumidor de fila roda |
| 9 Reversão Node → Go → Node | Reaper real do CRM reiniciado com outra configuração (SIGTERM) + binário Go com a guarda (SIGSTOP/SIGCONT) + conferência `preflight-posse.mjs` + observador por change stream | Os eventos de cada fase são semeados e abandonados pelo worker simulador. "Go sobe antes do Node sair" é um erro de operação encenado de propósito |
| G1–G5 Guarda do Node | Reaper real do CRM com a variável ausente, `off`, `on` cedente e revertido para `off` no meio; binário Go com a guarda; Node morto com `kill -9` | Configurações erradas encenadas de propósito; eventos semeados e abandonados pelo worker simulador |

O que isso prova: as peças trocadas (reaper, posse, recuperação de failed, alertas) convivem sem escrita fora da posse e sem duplicata, com processos reais morrendo e voltando. O que não prova: comportamento da API, das telas, dos workers de domínio, de carga real ou do Render.

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

Leva cerca de 8 minutos. Roda no CI do kas-core (job `Integrated CRM + KAS`), com o CRM da branch indicada em `CRM_REF`. Termina com `TUDO OK` (código 0) ou com a lista do que falhou (código 1).

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
3. A conferência só leitura dá `COMPATÍVEL`. O Node reinicia com `EVENT_REAPER_OWNER=kas` **mantendo** `on` (SIGTERM devolve a posse): fica em modo cedente (marca `yield`, não recupera); o Go com a guarda assume e recupera.
4. O Go **trava com a posse** (SIGSTOP) e o Node volta como dono: o Node espera o prazo vencer e assume.
5. O Go antigo acorda (SIGCONT): 0 escritas; os ciclos dele são recusados (`not_leader`).
6. Nenhum evento com posse vencida esquecido em `processing`; na reversão inteira, todas as 190 recuperações foram gravadas dentro da posse e nenhuma duas vezes.

**Guarda do Node (G1–G5), KAS sempre com `-require-crm-node-lease`**

| # | Cenário | Verificação |
|---|---|---|
| G1 | Node com `EVENT_REAPER_SHARED_LEASE` ausente | conferência acusa `INCOMPATÍVEL`; a KAS registra `incompatible_config`, não segura a posse e não grava nada; o Node segue recuperando 40/40 |
| G2 | Node com `off` | idem |
| G3 | KAS com `-lease-collection` diferente do Node | conferência acusa a divergência; a KAS recusa e não cria posse própria; o Node recupera 40/40 cercado na posse padrão |
| G4 | KAS executando (Node cedente); o Node é revertido para `off` por engano | a KAS detecta a marca velha e devolve a posse; enquanto a posse dela vivia, o Node `off` pulou o ciclo; depois o Node retoma 40/40; a KAS não grava mais nada e segue de pé |
| G5 | Node cedente leva `kill -9` com a KAS executando | a KAS para sozinha; sem Node, ninguém recupera (limite conhecido); o Node volta e recupera 20/20 |

## Resultado da rodada de 19/11/2026 (guarda desligada, versão anterior do roteiro)

Todas as verificações passaram (`TUDO OK`). Observador da reversão inteira: 190 recuperações, 190 cercadas, 0 fora da posse, 0 duplicadas; donos em sequência `crm-node → kas-rev → crm-node`.

## O que este ambiente não prova

- Redis pausado não fez o dispatcher do CRM marcar `failed` (o cliente espera a volta); as falhas do 7.7 foram semeadas no formato que o dispatcher grava. A pausa provou só que nada se perde nem duplica quando o Redis volta.
- A perda de posse ou de compatibilidade **no meio** de um ciclo do Go (fence dentro da transação) é coberta pelos testes de unidade da KAS (`FenceRefusesMidCycleWhenMarkerGoesStale`); aqui o Go é recusado no ciclo seguinte ou na próxima escrita, sem forçar o instante exato.
- Canal real de alertas, hospedagem da KAS e credenciais de produção não fazem parte (decisões de ativação).
