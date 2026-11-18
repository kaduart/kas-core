# Plano da Fase 1, revisão 2: primeira capacidade do CRM migrada para a KAS Core

Data: 18/11/2026. Status: proposta revisada. Nada foi implementado, hospedado ou migrado.
Fontes: CRM local `back/` (HEAD `7c080d1c`) e KAS `origin/kas-core/operacional` (HEAD `7f4bb16`).
A revisão 1 (espelhar `APPOINTMENT_*` com envio sem garantia) foi **descartada**. Os motivos estão na seção 9.

---

## 1. Capacidade escolhida: registro de auditoria de ações (envelope)

### 1.1 O que existe hoje no Node
| Arquivo | O que faz |
|---|---|
| `back/services/auditLogService.js` → `recordAudit()` | Grava direto na coleção `auditlogs`. É *best-effort*: o `try/catch` engole o erro e só registra em log. |
| `back/models/AuditLog.js` | Campos `userId`, `actorRole`, `action`, `entityType`, `entityId`, `before`, `after`, `diff`, `source`, `correlationId`, `severity` e `metadata`. TTL de 1 ano. |
| `back/config/featureFlags.js` (`AUDIT.ENABLED` ← `FF_AUDIT_ENABLED`) | Liga e desliga a auditoria. É lido **no carregamento do módulo**. |
| Chamadas em `back/services/appointment/commands/*.js` (create, update, cancel, confirm, delete, expire, clinicalStatus, etc.) | Exemplo: `createAppointmentCommand.js:350` chama `recordAudit` **depois do commit**, em `Promise.all` junto com o socket. |
| `getAppointmentAuditTrail` / `getInsurancePlanAuditTrail` | Leitura da trilha, usada pela interface. |

**Problemas confirmados no próprio código:**
- O cabeçalho de `auditLogService.js` tem um TODO técnico: *"deve evoluir para garantia de entrega em caso de falha de processo"*.
- Um comentário de correção datado de 2026-09-18 registra que a auditoria de agendamentos criados pelo app `agenda` foi **perdida em silêncio em produção** (o erro de cast era engolido pelo best-effort).

### 1.2 O que passa para Go, e o que fica no Node
| Responsabilidade | Destino |
|---|---|
| **Registro durável, idempotente e isolado por tenant do envelope da ação**: quem (pseudônimo), ação, recurso (pseudônimo), resultado, quando ocorreu, quando foi recebido e correlação | **KAS Core** (já existe: `POST /internal/v1/audit/events`) |
| `before`, `after` e `diff` (campos como `paymentAmount`, `clinicalStatus`, `notes`, `insuranceGuide`) | **Ficam no CRM.** São dados clínicos e financeiros. |
| `inferSeverity`, `pickAuditableFields` e as regras de quais campos auditar | **Ficam no CRM.** São regras do domínio. |
| Interface que mostra a trilha do agendamento | **Fica no CRM** até existir uma leitura na KAS (decisão D3). |

**Por que esta capacidade, e não outras:**
- **Auditoria:** o módulo da KAS é o mais maduro e o legado tem uma falha real de garantia de entrega. Não envolve regra clínica, desde que o diff fique fora.
- **Eventos/Outbox:** o CRM já tem Outbox, BullMQ e projeções acoplados aos workers. Substituir isso é um passo grande demais para o primeiro marco.
- **Feature flags:** hoje são variáveis de ambiente lidas na inicialização. Ganho baixo.

---

## 2. Evidências das capacidades atuais da KAS

Todas estão na branch `kas-core/operacional`. Os commits até `b1360b6` já estão em `main` (merge do PR #2, `39bb58d`). De `6462c3d` em diante, os commits só existem na branch e dependem do PR pendente.

| Capacidade | Commits | Arquivos | Testes que comprovam |
|---|---|---|---|
| Auditoria em PostgreSQL | `d14d3fe` | `internal/audit/postgres.go`, `migrations/0001_audit_events.sql` | `TestPostgresAppendPreservesEventFields`, `TestPostgresConcurrentAppendsAreAllStored` |
| Deduplicação (`event_id` por tenant e serviço; 200 em reenvio idêntico, 409 em conflito) | `7870f1f` | `migrations/0002_audit_events_identity.sql`, `internal/audit/postgres.go` | `TestPostgresIdenticalResendIsDuplicateAndConflictIsRejected`, `TestPostgresConcurrentIdenticalSubmissionsStoreOneRow`, `TestAuditIdenticalResendReturnsOriginalEventWithOK`, `TestAuditConflictingEventReturns409` |
| Migrações (compartilhadas, idempotentes, concorrentes, papéis separados) | `f72d5d0`, `7870f1f` | `internal/pgmigrate/migrate.go`, `deploy/grants.sql` | `TestApplyIsIdempotentOrderedAndVerifiable`, `TestConcurrentApplyRunsEachMigrationOnce`, `TestPostgresRuntimeRoleWithInsertSelectOnly`, `TestPostgresVerifiedOpenRequiresAppliedMigrations` |
| TLS obrigatório no banco fora de loopback | `7870f1f`, `f72d5d0` | `internal/pgdb/pgdb.go` | `TestTransportPolicy`, `TestOpenPostgresRejectsPlaintextRemoteBeforeConnecting` |
| Claim, ack e nack (pull) | `6462c3d` | `internal/events/pull.go` | `TestAckRequiresTheLeaseTokenConsumerAndAttempt`, `TestExpiredPullLeaseIsReclaimedAndTheLateConsumerIsRejected`, `TestNackAppliesBackoffThenBuriesAtMaxAttempts` |
| Métricas | `0a911da` | `internal/metrics/metrics.go`, `internal/httpapi/metrics.go` | `TestMetricsCountRequestsByRouteAndStatusWithoutTenantOrToken`, `TestSeriesAreCappedAgainstCardinalityGrowth`, `TestMetricsOnANonLoopbackListenerNeedAToken` |
| Rotação de token | `3fd7b28` | `internal/configstore/store.go` | `TestRotationAcceptsBothTokensAndRejectsOthers` |
| Encerramento limpo | `5236b37` | `cmd/kas-core/main.go` | `TestSIGTERMRequestsGracefulShutdown`, `TestDrainWaitsForAuditWriteAndRejectsNewRequests` |
| TLS no listener HTTP | piloto (`2cfaca8`/`302fbc8`) | `cmd/kas-core/main.go` (flags `-tls-cert` e `-tls-key`; recusa listener fora de loopback sem TLS) | Sem teste unitário específico. **Lacuna:** acrescentar teste. |

**Grau de verificação:**
- Eu executei a suíte só até `0f6b7fd`.
- Os resultados de `d14d3fe` em diante vêm dos relatos do agente executor: Actions runs 7, 8, 9, 11 e 12, lidos na página pública, sem os logs dos passos.
- **Antes da fase B,** rodar a suíte no commit exato que for usado e guardar o link do run.

O que a fase 1 **não** usa da KAS: o módulo de eventos e o pull. O destino é só a auditoria.

---

## 3. Entrega confiável CRM → KAS

### 3.1 Princípios
- **Não muda** o significado de `published` no Outbox do CRM, e **não usa** o Outbox do CRM.
- **Não bloqueia** o comando principal: no caminho do usuário entra apenas uma gravação local no Mongo. O envio HTTP acontece em outro processo.
- **Estado de entrega próprio**, com retentativas limitadas e recuperação depois de queda.

### 3.2 Componentes (no CRM, numa branch, após autorização)
1. **Coleção nova `kas_audit_deliveries`:**

| Campo | Conteúdo |
|---|---|
| `eventId` | UUID, com índice único |
| `envelope` | Já pseudonimizado (seção 3.4) |
| `contentHash` | Hash do envelope |
| `status` | `pending`, `in_flight`, `delivered`, `conflict` ou `dead` |
| `attempts` | Número de tentativas |
| `nextAttemptAt` | Próxima tentativa |
| `leaseUntil` / `leaseOwner` | Prazo e dono do bloqueio |
| `lastErrorCode` | Só o código, sem mensagem livre |
| `kasStatus` | Resposta da KAS: 201, 200 ou 409 |
| `createdAt` / `deliveredAt` | Datas |

2. **Em `recordAudit()`:** gerar o `eventId`, gravar `AuditLog` (como hoje, com `metadata.kasEventId`) **e** `kas_audit_deliveries` **numa mesma transação Mongo curta**. Se a transação falhar, o comportamento atual se mantém: só log, sem quebrar o comando.
   - **Limite herdado, que não muda nesta fase:** `recordAudit` roda depois do commit do comando. Uma queda entre o commit e a auditoria perde o registro, hoje e depois. Fechar isso exige gravar dentro da transação do comando (decisão D4, fase posterior).
3. **Relay em `crm-worker`** (processo separado do `crm-api`):
   - Laço com `findOneAndUpdate` atômico: `pending` com `nextAttemptAt` vencido, ou `in_flight` com `leaseUntil` vencido, passa para `in_flight` com novo prazo.
   - Envia `POST /internal/v1/audit/events` com timeout de 3 s (o valor final sai da medição).
   - **201 ou 200** → `delivered`. O 200 é reenvio idêntico; a KAS já deduplica.
   - **409** → `conflict`. É terminal e dispara alerta, porque significa o mesmo id com conteúdo diferente, ou seja, um bug.
   - **400, 401, 403 ou 422** → `dead`. Também terminal, com alerta. É erro de configuração ou contrato.
   - **5xx, timeout ou erro de rede** → volta para `pending`, com backoff exponencial e jitter (2 s até um teto de 10 min), **no máximo 10 tentativas**, depois `dead`.
   - No máximo N envios em paralelo (N=4). O relay para sozinho se a fila passar de um limite configurado e alerta.
4. **Recuperação:**
   - Queda do relay → o prazo vence e outro ciclo reivindica.
   - Timeout depois de a KAS já ter gravado → o reenvio recebe 200 com o mesmo evento. Isso é seguro graças à deduplicação da seção 2.
   - Registros `dead` são reenfileirados manualmente por um script com confirmação. Nada é apagado automaticamente.

### 3.3 Garantias desta etapa
- **Pelo menos uma vez** entre `kas_audit_deliveries` e a KAS, com **exatamente um registro final** na KAS por `eventId`, graças ao índice único em `(tenant_id, service_id, event_id)`.
- **Sem garantia** para o trecho entre o commit do comando e `recordAudit`. Esse é o limite herdado de 3.2.2.
- **Sem garantia de ordem.** A KAS guarda `occurred_at` e `received_at`.

### 3.4 Payload mínimo e pseudonimização
Identificadores de agendamento e de usuário podem ser vinculados a pessoas, por isso **não vão em claro**.

| Campo KAS | Valor |
|---|---|
| `event_id` | UUID gerado no CRM |
| `tenant_id` | Fixo por ambiente (`KAS_TENANT_ID`) |
| `service_id` | Serviço autenticado do CRM |
| `actor` | `user.<hmac>`, `service.<nome>` ou `system.crm`, onde `<hmac>` = HMAC-SHA256(`userId`, chave do tenant) truncado em 32 hex |
| `action` | Tabela fixa de mapeamento. Exemplo: `appointment_created` → `appointment.created`. O padrão da KAS não aceita `_` no primeiro segmento. Ações em português (`cancelamento`, `preagendamento_confirmado`…) recebem nomes canônicos. Ação fora da tabela não é enviada: conta numa métrica e gera alerta. |
| `resource` | `appointment:<hmac(entityId)>` |
| `result` | `success`. Hoje `recordAudit` só é chamado depois de escrita bem-sucedida; a confirmar caso a caso. |
| `correlation_id` | `correlationId` do CRM, validado. Se for inválido, recebe um gerado. |
| `occurred_at` | `createdAt` do AuditLog |

- A chave HMAC fica **só no CRM**, como segredo do Render. A KAS nunca vê o id real.
- A reconciliação funciona porque o CRM consegue recalcular o HMAC.
- Rotacionar a chave quebra a ligação com os registros antigos. Por isso a chave é versionada (`k1`) dentro do pseudônimo.

---

## 4. Reconciliação e testes

### 4.1 Reconciliação por `event_id` (não por contagem)
- **Fonte A:** `kas_audit_deliveries` no CRM, com `eventId` e `contentHash`.
- **Fonte B:** `audit_events` na KAS, com `event_id` e o hash recalculado dos campos.
  - Hoje não existe API de leitura (foi vetada antes). Na staging, a exportação é feita por SQL com o papel de migração.
  - Na fase D, depende da decisão D3.
- **Script de reconciliação** com 4 classes:
  - `faltando_na_kas`: delivered em A, ausente em B;
  - `sobrando_na_kas`: está em B sem estar em A;
  - `hash_divergente`;
  - `pendente_envelhecido`: pending/in_flight há mais de 15 min.
- **Meta para avançar:** zero nas quatro classes por 7 dias seguidos. Toda divergência é investigada pelo `event_id`.
- **Comparação extra com o legado:** cada `AuditLog` criado no período com `metadata.kasEventId` tem uma linha em A. `AuditLog` sem `kasEventId` com a flag ligada é anomalia.

### 4.2 Testes obrigatórios antes da staging
Rodam contra a KAS real e o Mongo local, com dados fictícios.

| Cenário | Esperado |
|---|---|
| KAS fora do ar por 10 min | O comando do CRM conclui. A fila cresce, drena quando a KAS volta e a reconciliação dá zero. |
| Reinício do relay com itens `in_flight` | Os prazos vencem, os itens são reprocessados, nada se perde nem duplica na KAS. |
| Mesmo `eventId` enviado 2 vezes | Uma linha na KAS e um 200 no segundo envio. |
| Timeout depois de persistir (proxy de teste que atrasa a resposta além do timeout do cliente) | O reenvio recebe 200 e há uma linha só. |
| Mesmo `eventId` com conteúdo diferente | 409, `conflict` e alerta. |
| Banco da KAS fora (503) | Retentativas e depois `dead`, sem impacto no comando. |
| Token errado ou revogado | 401, `dead` e alerta. |
| Rotação de token | Sem falhas de entrega durante a troca. |
| Falha da transação local `AuditLog` + delivery | O comando conclui e o erro aparece em log e métrica. |
| Carga: N comandos por segundo (N vem da medição de produção) | Latência adicional de `recordAudit` **medida**, não presumida. |

### 4.3 Medições (nenhum número é prometido antes de medir)
- **Linha de base antes da mudança:** p50, p95 e p99 de `recordAudit` e do comando `createAppointment` em produção, por 7 dias.
- **Depois da mudança, em staging:** os mesmos percentis mais o custo da transação extra.
- **Critério de aceite:** o p95 não piora mais que X ms, com X definido por você depois de ver a linha de base.

---

## 5. Etapas e o que cada uma garante

| Etapa | O que é | Garantias | Descartável? |
|---|---|---|---|
| **A. Experimento de conectividade** | Um script fora do CRM manda eventos **sintéticos** (tenant `tenant-staging-probe`) do mesmo ambiente de rede até a KAS na staging. | Nenhuma de negócio. Prova só rede, TLS, autenticação, `/readyz` e `/metrics`. | **Sim**, e é apagado no fim. Nenhum dado do CRM é usado. |
| **B. Entrega confiável em staging** | Seção 3 numa cópia de staging do CRM, com dados fictícios. | Pelo menos uma vez, um registro final por `event_id`, recuperação depois de queda. | Não. É o código da migração. |
| **C. Sombra em produção** | Flag ligada em produção. O CRM continua gravando `AuditLog` como hoje, e a KAS recebe em paralelo. | As de B, mais a reconciliação diária por `event_id`. **O legado continua sendo a fonte oficial.** | Pode ser desligada (seção 7). |
| **D. Substituição** | Só com a sua autorização, depois de C (seção 8). | A KAS passa a ser o registro oficial do envelope. | Tem reversão documentada. |

---

## 6. Transporte autenticado e protegido

**Não desligar TLS.** A ideia da revisão 1, a "flag de rede privada sem TLS", foi **retirada**.

1. **TLS na própria KAS:** ela já aceita `-tls-cert` e `-tls-key` e recusa listener fora de loopback sem TLS (`cmd/kas-core/main.go`).
2. **Certificado:**
   - Gerar uma CA interna pequena só para KAS e CRM. A CA vai para o CRM e o certificado e a chave para a KAS, como Secret Files do Render.
   - No CRM, usar um `https.Agent` com `ca` fixa e verificação de hostname ligada.
   - Validade de 90 dias, com rotação documentada no RUNBOOK.
   - Uma alternativa é expor a KAS como web service com TLS gerenciado pelo Render. Isso aumenta a superfície pública, então só entra com sua decisão (D2).
3. **Autenticação:** token Bearer por serviço com a capacidade `audit:write` e `X-KAS-Service`. Já existe, com rotação em dois slots (`3fd7b28`).
4. **Banco da KAS:** `sslmode=verify-full` com a CA do provedor. A política atual já recusa conexão sem TLS fora de loopback.
5. **Ainda não implementado, para avaliar depois:** mTLS e restrição por IP de origem.
6. **A testar na etapa A:** verificação de hostname, certificado expirado recusado e token antigo recusado depois da rotação.

---

## 7. Reversão (procedimento real)

As flags do CRM são lidas **no carregamento do módulo** (`back/config/featureFlags.js` usa `process.env`). Portanto **mudar uma flag exige reinício do serviço**.

| Flag nova | Serviço | Efeito |
|---|---|---|
| `KAS_AUDIT_ENQUEUE_ENABLED` | `crm-api` e workers que chamam `recordAudit` | Controla a gravação em `kas_audit_deliveries` |
| `KAS_AUDIT_RELAY_ENABLED` | `crm-worker` | Liga e desliga o envio para a KAS |

**Procedimento:**
1. No painel do Render, abrir o serviço e ir em Environment.
2. Mudar a flag para `false` e salvar.
3. O Render faz um novo deploy com a variável. Pelo comportamento padrão do Render, alteração de env dispara redeploy, mas **isso precisa ser confirmado na conta** antes da etapa C.
4. Confirmar nos logs a linha de inicialização com o estado das flags. Essa linha é um item a implementar.
5. Tempo esperado: o de um deploy do serviço, a medir na staging. **Não é instantâneo.**

**Ordem de reversão:**
1. Desligar o relay (`crm-worker`), que para o tráfego para a KAS.
2. Se preciso, desligar o enqueue (`crm-api`).

O `AuditLog` legado nunca deixa de ser gravado nas etapas A a C, então a reversão não perde auditoria. Os itens que ficaram em `kas_audit_deliveries` são preservados e podem ser drenados mais tarde ou descartados por decisão sua.

**Melhoria opcional:** reler a flag do relay em tempo de execução (Redis ou configuração da KAS) para desligar sem deploy. Fica para depois.

---

## 8. Primeiro marco de migração

> **A KAS executa o registro durável e idempotente do envelope de auditoria das ações de agendamento.**

1. **Sombra (etapa C):** o Node continua sendo a fonte oficial e a KAS recebe em paralelo. Para avançar: 7 a 14 dias com reconciliação zero nas 4 classes, nenhum `dead` sem explicação e a latência medida dentro do limite que você definir.
2. **Substituição (etapa D), só com autorização explícita:**
   - O envelope oficial passa a ser o da KAS.
   - O `AuditLog` do Node mantém apenas `before`, `after` e `diff`, ligados por `kasEventId`.
   - A trilha mostrada na interface lê o envelope da KAS, o que **depende de D3** (endpoint de leitura).
   - Até D3 existir, o Node continua gravando o envelope completo. Na prática a substituição fica restrita à garantia de entrega, e o registro oficial continua no Node.
3. **Reversão de D:** religar a leitura e gravação do envelope no Node, com a flag e o deploy da seção 7. O `AuditLog` nunca deixou de ter o envelope durante a transição.

---

## 9. O que mudou em relação à revisão 1
- **Antes:** espelhar `APPOINTMENT_CREATED/CANCELLED`, sem migrar responsabilidade nenhuma. **Agora:** uma responsabilidade real (auditoria) com marco de substituição.
- **Antes:** envio sem retentativa. **Agora:** estado de entrega próprio, retentativas limitadas e recuperação.
- **Antes:** paridade por contagem diária. **Agora:** reconciliação por `event_id` e hash.
- **Antes:** TLS desligado na rede privada. **Agora:** TLS com CA interna, ou TLS gerenciado se você decidir assim.
- **Antes:** "o CRM nem percebe" e "menos de 5 ms". **Agora:** nenhuma promessa; os números vêm das medições.
- **Antes:** ID de agendamento em claro. **Agora:** pseudonimizado com HMAC.

## 10. Decisões suas antes de implementar
| # | Decisão | Minha recomendação |
|---|---|---|
| D1 | Autorizar mudança no CRM: `recordAudit`, coleção nova e relay no `crm-worker` | Sim, numa branch com PR |
| D2 | Transporte: CA interna em serviço privado, ou web service com TLS gerenciado pelo Render | CA interna |
| D3 | Endpoint de leitura de auditoria na KAS (escopo por tenant e recurso, capacidade `audit:read`) | Sim, mas só antes da etapa D |
| D4 | Levar `recordAudit` para dentro da transação do comando | Depois da etapa C |
| D5 | Hospedagem de staging (Render) e o limite X ms de latência | Definir depois da linha de base |

---

## 11. Custo adicional zero e o que poderá ser desligado (adendo de 18/11, 10h)

**Requisito:** nenhum recurso pago novo nesta fase. A KAS continua **local**. As etapas B, C e D desta revisão ficam **suspensas** até existir uma substituição medida. Valem agora só o desenvolvimento local e os simuladores.

### 11.1 Validação local, sem hospedagem
- **Simulador do CRM** (`node-integration/sim/`, só no repositório da KAS):
  - reproduz as chamadas de `recordAudit` dos commands de agendamento com dados fictícios;
  - inclui a coleção `kas_audit_deliveries` num Mongo local descartável;
  - inclui o relay;
  - roda a mesma bateria de testes da seção 4.2 contra a KAS local (Go + PostgreSQL do WSL).
- **Equivalência:**
  - Para cada chamada simulada, o envelope que o Node gravaria em `AuditLog` (calculado pela mesma função de seleção, copiada como fixture) precisa corresponder 1:1, por `event_id` e hash, ao registro na KAS.
  - A meta é zero divergências em 10 mil eventos, com as falhas injetadas da seção 4.2.

### 11.2 O que sai do Node depois da migração validada (etapa D)
| Hoje no Node | Depois |
|---|---|
| Gravação do envelope em `auditlogs` (Mongo) em todos os commands | Sai do Node. O `AuditLog` fica só com `before`, `after`, `diff` e `kasEventId`. |
| Índices de envelope em `AuditLog` (`action`, `source`, `severity`, `userId` e os de correlação) | Podem ser removidos depois que a leitura migrar (D3). |
| Tratamento "engole erro" de `recordAudit` | É substituído pela entrega com estado e alerta. |

### 11.3 Economia: o que é honesto dizer
- **Este primeiro corte não reduz custo.** O ganho é **confiabilidade**: fecha uma perda silenciosa que o próprio código registra, com garantia de entrega e reconciliação. Hospedar KAS e PostgreSQL acrescentaria custo antes de retirar qualquer coisa.
- **Onde pode haver economia de verdade, mais adiante:** o `render.yaml` do repositório descreve um `fono-inova-crm-api` (standard) e 5 workers (`scheduling`, `billing`, `clinical`, `reconciliation` no starter, e `whatsapp` no standard), todos com Redis/BullMQ.
  - **Atenção:** uma auditoria anterior apontou que esse arquivo descreve uma arquitetura futura. **Os serviços realmente ativos e os valores precisam ser conferidos no painel do Render.**
  - Só haverá economia se um **serviço inteiro** for desligado. Os candidatos são o `crm-worker-reconciliation` ou o dispatcher do Outbox (`startOutboxDispatcher`, que hoje faz polling do Mongo a cada 1 s, iniciado em `workers/registry.js:69` e `:224`). Ou então se o Redis puder ter o plano reduzido.
  - Isso é o **segundo corte**: o dispatcher do Outbox e a entrega dos eventos na KAS. Fica para depois que o primeiro provar o caminho.
- **Não comparar "Node contra Go" como economia.** Só conta o recurso que deixar de ser cobrado.
