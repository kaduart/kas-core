# Plano de extração de uma capacidade do CRM para a KAS Core

Data: 2026-11-18. Estado: **proposta; nada implementado no CRM, nada hospedado.** Este plano trata de **substituir uma responsabilidade técnica hoje executada em Node por Go**. Espelhar eventos para a KAS sem substituir nada é um **experimento de integração**, não uma migração, e **não é redução de custo**.

Fontes lidas, somente leitura: CRM em `C:\Users\User\Documents\projetos\crm\back` (commit `7c080d1c`) e a API do Render (listagem de serviços, sem variáveis de ambiente). Nenhum `.env` foi aberto. Onde algo não foi verificado, está dito.

## 1. O que roda de fato (e onde está o custo)

O `ecosystem.config.cjs` do CRM descreve 7 processos PM2 (API, cinco workers, watchdog), mas **não é o que está no Render**. A conta tem:

| Serviço | Plano | Executa | Observação |
|---|---|---|---|
| `fono-inova-crm-back` | starter | `node server.js` (API; os workers dependem de `ENABLE_WORKERS`, não verificado no painel) | build instala o Chrome (`puppeteer`) |
| `crm-worker` | standard + disco 5 GB | `workers/entrypoints/whatsapp-only.js` | `whatsapp-web.js` + Chrome, heap 768 MB |
| `fono-inova-crm-api` | standard | `node server.js` | **suspenso** |
| `recupera-api`, `conecta-api` | free | outros projetos | não são o CRM |
| `redis-fono-inova` | Key Value starter | filas BullMQ | `recupera-redis` é free |

**Consequência para a economia:** os dois itens pagos que pesam são o worker do WhatsApp (Chrome) e a API (Chrome para PDF). Nenhum deles é substituível por Go: dependem de `whatsapp-web.js` e `puppeteer`. Portanto **nenhuma migração para Go, no estado atual do código, desliga um serviço pago do Render.** Valores em dólar não foram obtidos (a documentação do Render lida não traz preços): conferir no painel.

### 1.1 Fatos verificados sobre onde o Outbox roda e quando para

Verificados no código do CRM e no Render (somente leitura):

- O Start Command do `crm-worker` é `workers/entrypoints/whatsapp-only.js`. Esse entrypoint sobe os grupos `scheduling`, `billing`, `clinical` e `reconciliation` (`workers/entrypoints/whatsapp-only.js:138-143`).
- O grupo `reconciliation` inicia `startOutboxDispatcher` em `workers/registry.js:223-224` quando `ENABLE_RECONCILIATION_OUTBOX` não é `false`. O padrão é **ligado** (`isEnabled` assume `true` sem a variável). Isso depende das variáveis do painel do Render, que não li.
- O web (`node server.js`) só sobe workers se `ENABLE_WORKERS=true` (`server.js:819`). O valor no painel não foi verificado.
- O `crm-worker` é **suspenso às 19:10 (BRT, seg-sex) e retomado seg-sex** por `back/scripts/render-worker-toggle.js`. O comentário do script diz que a retomada é às **07:50**; o levantamento recebido diz 07:30. O horário real está no agendador, que o script cita como "Cron Jobs `crm-worker-suspend` / `crm-worker-resume`" e que **não aparece** na lista de serviços do Render que li (5 serviços, nenhum cron job). Onde o agendador roda continua não localizado.
- Consequência: enquanto o worker está parado (noites e fins de semana) o dispatcher, as filas e os lembretes também param. Eventos se acumulam em `pending`; isso é parada programada, não falha.

**Ajuste na métrica de idade do evento mais antigo.** A métrica `oldest due pending age` deve **descontar as janelas em que o worker está programado para estar parado** (noites e fins de semana), senão gera alerta falso toda manhã. A janela vem de configuração da KAS (dias e horas, fuso `America/Sao_Paulo`), não de leitura do Render, e a métrica passa a medir o tempo de espera **dentro das horas de operação**. Alertas de `failed` e `dead` não dependem de janela.

## 2. Candidatas avaliadas

| Candidata | Onde está no Node | Veredicto |
|---|---|---|
| Dispatcher do Outbox publicando no BullMQ | `infrastructure/outbox/outboxPattern.js` (239 linhas), iniciado em `workers/registry.js` (grupos `outbox` e `reconciliation`) | **Não migrar agora.** O destino é o BullMQ; os consumidores são workers Node que usam o protocolo Redis/Lua do BullMQ. Reimplementar a escrita em Go seria copiar um protocolo interno sem suporte oficial. Também roda dentro de processos existentes: não libera serviço. |
| Gravação de auditoria (`recordAudit`) | `services/auditLogService.js:207`, 25 chamadas em 18 arquivos | **Não é o primeiro corte** (ver 2.1). |
| Reaper de eventos travados | `crons/eventReaper.cron.js` (158 linhas) | Opera sobre `EventStore` (Mongo). Útil, mas pequeno e sem efeito em custo. |
| Crons de anúncios (`adsConversion`, `metaAdsSync`) | `crons/` | Carregam identificadores e dados de contato com hash: fora até haver política de dados. |
| WhatsApp, PDF, regras clínicas, financeiro | vários | Ficam no CRM. |

### 2.1 Por que não a auditoria primeiro

A hipótese de começar pela entrega de auditoria tem um defeito real a corrigir: `recordAudit` engole o erro (o `catch` só faz `console.error('AUDIT_FAIL', …)`), e o código registra um caso em produção (comentário de 2026-09-18: auditoria de agendamentos do app `agenda` perdida em silêncio). Mas, como **corte para a KAS**, ela esbarra em fatos do código:

1. O registro tem `before`, `after`, `diff` e `metadata` como `Mixed` (`models/AuditLog.js`): objetos arbitrários de entidades do CRM, ou seja, potencialmente dado clínico e pessoal. O contrato de auditoria da KAS **não aceita payload livre** (só identificadores, ator opaco, ação, recurso, resultado). Mover o envelope exigiria ampliar o contrato ou deixar `before/after` no CRM, o que mantém o CRM como dono do dado e reduz o ganho.
2. A coleção do CRM tem retenção de 1 ano por índice TTL (`expireAfterSeconds: 31536000`); a KAS não tem retenção de auditoria hoje.
3. São 25 chamadas em linha, em rotas e comandos de domínio: o corte tocaria muito código do CRM de uma vez.
4. O defeito de perda silenciosa pode ser corrigido no próprio CRM sem Go. Isso é melhoria do CRM, não migração.

## 3. Recomendação: o primeiro corte

**Responsabilidade: o ciclo de vida do Outbox do CRM** (recuperação de eventos `failed`, retenção e métricas), hoje **incompleto no Node**, a ser executado pela KAS em Go. É a única candidata que (a) tem arquivos e contrato bem delimitados, (b) não envolve dado clínico, (c) reaproveita o que a KAS já tem provado, e (d) corrige falhas **verificadas no código**.

### Fatos verificados no código

- `publishPendingEvents` seleciona só `status: 'pending'` com `scheduledAt <= agora` (`outboxPattern.js:90-91`). Quando a publicação falha, grava `status: 'failed'` com um `scheduledAt` de backoff (linhas 165-168); o tipo desconhecido também vira `failed` (linha 111). **Nada seleciona `failed` de novo**, e nenhum código nas pastas pesquisadas (`workers`, `services`, `crons`, `jobs`, `infrastructure`, `scripts`, `routes`, `server.js`) recoloca um evento em `pending`. O backoff gravado é decorativo: uma falha transitória do Redis ao publicar pode deixar o evento parado para sempre. Os eventos de tipo desconhecido também ficam em `failed` sem alerta.
- `cleanupOutbox` (`outboxPattern.js:206`, retenção de 7 dias para `published`) é exportado, mas **ninguém o chama** nas pastas pesquisadas, e `OutboxModel.js` não tem índice TTL. A coleção cresce sem limite.
- `eventReaper.cron.js` trata o `EventStore`, não o `Outbox`.
- Há teste unitário de `publishPendingEvents` (`tests/unit/outboxPattern.test.js`) que cobre evento desconhecido e evento sem fila; o caso "falha transitória e reenvio" não está coberto.

Limite desta verificação: busca restrita às pastas citadas; não executei o CRM nem consultei um Mongo de produção, então **não sei quantos eventos `failed` ou quantos documentos existem hoje**. A primeira etapa (sombra) mede isso.

### O que passa para Go, e o que não passa

| Passa para Go (KAS) | Continua no CRM |
|---|---|
| Descobrir eventos `failed` elegíveis (tentativas e idade), aplicar backoff e devolvê-los a `pending` | `saveToOutbox` (gravação transacional junto ao domínio) |
| Classificar `dead` (tipo desconhecido, tentativas esgotadas) e alertar | O dispatcher que publica no BullMQ (`publishPendingEvents`) |
| Retenção: apagar `published` além do prazo, em lotes | `eventToQueueMap` e os consumidores BullMQ |
| Métricas (`pending/failed/dead`, idade do mais antigo) | Todas as regras clínicas e financeiras e os payloads de domínio |

A KAS **não lê nem copia o `payload`**: só `eventId`, `eventType`, `status`, `attempts`, `scheduledAt`, `createdAt`, `publishedAt`. Isso mantém regra e dado clínico no CRM.

### Dependências e contratos

- **Nova dependência na KAS:** o driver oficial do MongoDB para Go. A KAS hoje só fala com PostgreSQL. É uma ampliação deliberada e isolada em um pacote.
- **Contrato com o legado:** o esquema do `OutboxModel.js` (campos acima, índices `{status, scheduledAt}`). Qualquer mudança nele precisa de coordenação.
- **Consumidores afetados:** o dispatcher Node (que volta a enxergar os `pending` devolvidos). Nenhum consumidor BullMQ muda.
- **Acesso:** usuário Mongo próprio da KAS, **só leitura** na etapa de sombra; escrita restrita a `Outbox` (atualizar `status/scheduledAt/attempts` e apagar `published` antigos) só após autorização.

## 4. Validação local de equivalência (dados fictícios)

1. **Simulador do legado:** um Mongo local descartável com o esquema do `OutboxModel`, preenchido por um gerador fictício (`eventType` do mapa real, `payload` com identificadores sintéticos). Pré-requisito a confirmar: um `mongod` local (não verificado aqui; o CRM tem scripts `espelho:mongo`).
2. **Cenários com falha injetada:** Redis indisponível durante a publicação; processo reiniciado no meio de um lote; dois recuperadores simultâneos; evento de tipo desconhecido; relógio adiantado.
3. **Oráculo:** implementação de referência do comportamento desejado (em Node, nos testes) comparada, por `eventId`, com o resultado do Go. Divergência aceitável: **zero** em 10.000 eventos com as falhas acima.
4. **Idempotência:** o `jobId` do BullMQ (`eventId__fila`) já impede publicação duplicada; o teste confirma que devolver o mesmo evento a `pending` duas vezes não gera dois jobs.

## 5. Transição e reversão

1. **Sombra (somente leitura):** a KAS calcula o que recuperaria e apagaria e publica métricas. **Não escreve.** Compara com a contagem real do Outbox.
2. **Escrita limitada, atrás de flag:** só a recuperação de `failed` (reversível: o evento volta a `pending`, que o Node já sabe tratar). Autorização explícita antes.
3. **Retenção:** primeiro simulação, depois apagamento com lote pequeno e limite diário. Irreversível, por isso por último.
4. **Reversão:** desligar a KAS (ou a flag) devolve ao comportamento atual do Node; o que ela já recuperou continua válido. O apagamento não se desfaz, então exige backup verificado do Mongo antes. Aplicar a flag no CRM exige redeploy se for lida no carregamento (`config/featureFlags.js`; **não verifiquei** onde a flag seria lida). Ligar a KAS não exige mexer no CRM.

## 5.1 Hospedagem da KAS (restrições registradas)

- Um web service **free** do Render **não recebe tráfego pela rede privada**; só atende pela internet pública.
- Por isso o teste de conectividade, quando houver, será **somente público: com TLS, com token de serviço, sem banco e sem dado real**. Auditoria durável e Outbox da KAS em PostgreSQL ficam pendentes até existir uma economia medida que justifique um banco (o PostgreSQL free expira em 30 dias e não tem backup).
- Nenhuma conta extra será criada para contornar limites do plano.

## 6. O que pode ser desativado depois de validado

- **Nada dos serviços pagos do Render.** O ganho é: eventos que hoje podem ficar parados para sempre passam a ser entregues, e a coleção `Outbox` deixa de crescer sem limite (custo de armazenamento do Mongo não medido).
- Dependência Node: nenhuma sai. O dispatcher e o BullMQ continuam.
- **Primeiros alvos de economia, antes de qualquer corte em Go (medições recebidas, não verificadas por mim):**
  1. **Estouro de memória do `crm-worker`: o processo passou de 2 GB em 13/11 (OOM).** Entender o que consome memória (Chrome do WhatsApp, filas, vazamento) e dimensionar o plano com base em dado, não em palpite.
  2. **Banda de saída: 2,46 GB de 5 GB já no dia 18 do mês.** Identificar o que gera tráfego antes que a franquia acabe e que passe a gerar cobrança.
- **Economia real** só virá de reduzir recursos medidos: por exemplo, rebaixar o plano do `crm-worker` ou da API se a memória real permitir (decisão de configuração, não de linguagem), ou remover o serviço suspenso. Isso deve ser medido no painel antes de qualquer promessa.

## 7. Critérios de aceite

- Em simulador local: **0 eventos perdidos e 0 divergências** em 10.000 eventos com falhas injetadas; nenhuma duplicação no BullMQ simulado.
- A KAS não lê `payload` (conferido por teste e revisão de consulta); nenhum dado pessoal nos logs.
- Em sombra real (quando autorizada): contagem de `failed`/`pending`/`published` da KAS igual à do Mongo, e relatório do que seria recuperado revisado por você.
- Reversão ensaiada: desligar a KAS no meio de um lote não deixa evento preso.
- Sem afirmar latência ou ausência de impacto no CRM antes de medir: a leitura roda fora da API e com limite de consultas por minuto.

## 8. Fora do escopo desta rodada

Alterar o CRM, hospedar a KAS, migrar dados, ligar produção. Hospedagem será reavaliada quando houver uma substituição concreta para medir.

## 9. Trabalho independente na KAS que segue

Cliente Mongo e o pacote do ciclo de vida do Outbox com testes contra um Mongo local; simulador do legado; gerador de eventos fictícios; métricas no `/metrics` já existente. Nada disso precisa do CRM nem de hospedagem.
