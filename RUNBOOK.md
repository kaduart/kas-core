# KAS Core — runbook operacional

Procedimentos para subir, migrar, limpar, rotacionar credenciais, tratar entregas `dead` e restaurar backup. Todos os exemplos usam nomes, senhas e tokens **fictícios**. Este documento não declara o sistema pronto para produção: veja os limites no fim.

Convenções: `kas-core` é o executável (ou a imagem `kas-core:dev`). A conexão com o banco vem sempre de **variáveis de ambiente**, nunca de flags nem do arquivo de configuração. Quem opera precisa de dois papéis no PostgreSQL:

| Papel | Quem usa | Privilégios |
|---|---|---|
| migração (dono do esquema) | `-*-migrate-only`, `-events-purge`, tratamento manual de `dead` | DDL e `DELETE` |
| runtime | o servidor | auditoria: `INSERT`/`SELECT`; `outbox_events`: `INSERT`/`SELECT`; `outbox_deliveries`: `INSERT`/`SELECT`/`UPDATE`; nunca `DELETE`, `TRUNCATE` ou DDL (`deploy/grants.sql`) |

## 1. Subir

### Ambiente local com Docker (desenvolvimento)

Requer Docker com Compose v2 e `openssl`.

```bash
./scripts/dev-certs.sh          # uma vez: certificado autoassinado (30 dias)
docker compose up --build       # db -> migrate -> grants -> kas
```

O compose sobe o PostgreSQL com TLS e dois papéis, aplica as migrações e as concessões e só então inicia a KAS com o papel de runtime (`-audit-auto-migrate=false`, `-events-auto-migrate=false`). Ele escuta em `https://127.0.0.1:8088` (autoassinado: use `curl -k`) e o banco em `127.0.0.1:5432`. Tokens e senhas padrão são fictícios e só valem aqui; para trocá-los copie `.env.example` para `.env`.

Verificação:

```bash
./scripts/compose-smoke.sh      # publica, reenvia, faz claim e ack, lê /metrics
curl -ks https://127.0.0.1:8088/readyz
docker compose down -v          # apaga também o volume do banco
```

### Binário (WSL ou servidor)

```bash
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o kas-core ./cmd/kas-core
export KAS_CALLER_FONO_NODE_TOKEN=...        # 64 caracteres hexadecimais por serviço
export KAS_AUDIT_DATABASE_URL='postgres://kas_runtime:SENHA@db.example.com:5432/kas_core?sslmode=verify-full'
export KAS_EVENTS_DATABASE_URL="$KAS_AUDIT_DATABASE_URL"
export KAS_METRICS_TOKEN=...                 # 32 ou mais caracteres
./kas-core -config /etc/kas/config.json -addr 0.0.0.0:8088 \
  -tls-cert /etc/kas/server.crt -tls-key /etc/kas/server.key \
  -audit-store postgres -audit-auto-migrate=false \
  -events-store postgres -events-auto-migrate=false \
  -metrics-token-env KAS_METRICS_TOKEN
```

Regras que a KAS impõe ao iniciar:

- Listener fora de loopback exige `-tls-cert` e `-tls-key`.
- Banco fora de loopback exige TLS: `sslmode=disable`, `allow` e `prefer` são recusados. Use `verify-full` (o `require` criptografa, mas não autentica o servidor).
- `/metrics` só é servido sem token em listener loopback; fora dele precisa de `-metrics-token-env`.
- Configuração, tipos de evento, consumidores e tokens são lidos **só na inicialização**: qualquer mudança exige reinício.
- Com `-audit-store jsonl` (padrão) os dados ficam em `-audit-data-dir`, com `audit.lock`. Se o processo morrer, confirme que ele parou antes de remover o lock manualmente.

Saúde: `GET /healthz` (vivo) e `GET /readyz` (também checa a auditoria e os eventos; 503 sem detalhes se o armazenamento falhar). Encerrar com `SIGTERM` espera as requisições em andamento.

## 2. Migrar

Migrações vêm embutidas no executável e têm uma tabela de controle por módulo (`audit_schema_migrations`, `events_schema_migrations`, `process_schema_migrations`). Não há reversão automática.

```bash
export KAS_AUDIT_MIGRATION_DATABASE_URL='postgres://kas_migrator:SENHA@db.example.com:5432/kas_core?sslmode=verify-full'
export KAS_EVENTS_MIGRATION_DATABASE_URL="$KAS_AUDIT_MIGRATION_DATABASE_URL"
./kas-core -audit-store postgres -audit-migrate-only -events-store postgres -events-migrate-only
# Process Studio (só se for ligar o motor; roda depois das migrações de eventos, mesmo papel e banco)
./kas-core -events-store postgres -process-store postgres -process-migrate-only
psql "$KAS_AUDIT_MIGRATION_DATABASE_URL" -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=kas_runtime -f deploy/grants.sql
```

Ordem numa atualização: (1) backup (seção 7); (2) migrar com o papel de migração; (3) rodar `deploy/grants.sql` (repetir é seguro, e é necessário quando uma migração cria tabela); (4) subir a nova versão com o papel de runtime. O servidor em modo verificado **recusa iniciar** se houver migração pendente e diz para rodar a migração.

Se uma migração falhar, ela é desfeita por inteiro e as anteriores permanecem. A migração `0002` da auditoria falha, sem apagar nada, se já houver identidades duplicadas; resolva-as manualmente e rode de novo.

## 3. Limpeza de retenção

Política: entregues, 30 dias; `dead`, 90 dias; eventos sem nenhuma entrega e recebidos há mais de 30 dias. Entregas `pending` e `in_flight` nunca são apagadas. **Nada é apagado automaticamente**: a limpeza só roda quando você executa.

```bash
# 1. simulação: só conta (em simulação, o número de eventos é um limite inferior)
./kas-core -events-store postgres -events-purge -events-purge-dry-run
# 2. execução real, em lotes de 1000
./kas-core -events-store postgres -events-purge
```

Usa `KAS_EVENTS_MIGRATION_DATABASE_URL` (papel de migração, porque precisa de `DELETE`). Saída em log: `delivered_deliveries`, `dead_deliveries`, `events` e os cortes de data. Termina com código 0; código 1 em falha parcial (o que já foi apagado continua apagado e repetir é seguro); código 2 se faltar a variável ou o `-events-store postgres`.

Agendamento (cron ou tarefa do sistema) é decisão sua e **não está configurado**. Sem limpeza as tabelas crescem sem limite. Acompanhe `kas_outbox_deliveries{state="delivered"}`.

A auditoria não tem limpeza: ela só cresce. Não há retenção da auditoria nesta etapa.

## 4. Rotacionar token de serviço

Cada serviço aceita **até dois tokens ao mesmo tempo**: `token_env` e, opcionalmente, `token_env_next`. A rotação não tem indisponibilidade, mas **exige reinício** da KAS duas vezes.

1. Gere um token novo (64 hexadecimais) e guarde-o num cofre: `openssl rand -hex 32`.
2. No `config.json`, acrescente `"token_env_next": "KAS_CALLER_FONO_NODE_TOKEN_NEXT"` ao serviço e defina essa variável com o token novo. O token novo **precisa ser diferente** do atual, senão a KAS recusa iniciar. Reinicie. O log mostra `service token rotation is active` com os serviços afetados; agora os dois tokens valem.
3. Troque o token no cliente (o serviço que chama a KAS) e reimplante o cliente.
4. Confirme a migração: em `/metrics`, `kas_service_token_checks_total{slot="next"}` cresce e `slot="primary"` para de crescer. O contador soma todos os serviços e não identifica token nem serviço; se mais de um serviço estiver em rotação, espere os dois pararem de usar o primário.
5. Promova: no `config.json`, remova `token_env_next` e aponte `token_env` para a variável do token novo (ou ponha o valor novo na variável antiga). Reinicie. O token antigo deixa de valer. `kas_services_rotating_tokens` volta a `0`.
6. Revogue o token antigo nos cofres.

Rollback: antes do passo 5, basta remover `token_env_next` e reiniciar; o token antigo continua válido. Se um token vazar, inclua o passo 5 imediatamente depois de trocar o cliente.

Tokens nunca vão para logs, rótulos de métricas ou mensagens de erro. O token do `/metrics` (`-metrics-token-env`) é separado: troque-o alterando a variável e reiniciando.

## 5. Tratar entregas `dead`

Uma entrega vira `dead` quando um consumidor pede falha permanente (`consumer_permanent` / `handler_permanent`), quando esgota as tentativas (`max_attempts`) ou quando o bloqueio expira sem tentativas restantes (`lease_expired`). Ela é mantida, e nada a reprocessa sozinho. Alerta sugerido: `kas_outbox_deliveries{state="dead"} > 0`.

A API não lista nem reenfileira (decisão de escopo), então isto é **manual, com o papel de migração**. Os exemplos não mostram o payload.

```sql
-- panorama: quantas, por causa
SELECT last_error_code, count(*), min(dead_at), max(dead_at)
  FROM outbox_deliveries WHERE status = 'dead' GROUP BY 1 ORDER BY 2 DESC;

-- quais (identificadores, sem payload)
SELECT d.id AS delivery_id, d.consumer_id, d.attempts, d.last_error_code, d.dead_at,
       e.tenant_id, e.service_id, e.event_id, e.event_type
  FROM outbox_deliveries d JOIN outbox_events e ON e.id = d.event_pk
 WHERE d.status = 'dead' ORDER BY d.dead_at DESC LIMIT 100;
```

Decisão por causa:

- **`handler_permanent` / `consumer_permanent`**: o consumidor rejeitou o evento. Descubra o motivo no consumidor. Só reenfileire depois de corrigi-lo.
- **`max_attempts`**: falha repetida. Corrija a causa (consumidor fora do ar, bug) e reenfileire.
- **`lease_expired`**: o consumidor sumiu no meio do processamento. O evento pode já ter sido tratado: confira antes, porque reenfileirar entrega de novo.

Reenfileirar uma entrega (zera tentativas e volta a ser devida agora):

```sql
BEGIN;
UPDATE outbox_deliveries
   SET status = 'pending', attempts = 0, next_attempt_at = now(),
       locked_until = NULL, locked_by = NULL, last_error_code = NULL, dead_at = NULL
 WHERE id = 123 AND status = 'dead'          -- ajuste o filtro; confirme a contagem antes do COMMIT
RETURNING id, consumer_id;
COMMIT;
```

A entrega é **pelo menos uma vez**: reenfileirar pode entregar de novo algo que o consumidor já processou, então o consumidor precisa deduplicar por `(tenant_id, service_id, event_id)`. Para descartar de vez, deixe-a como `dead` e use a limpeza (90 dias). Não apague linhas à mão fora de uma transação revisada.

## 6. Métricas

`GET /metrics` (formato Prometheus 0.0.4; com `Authorization: Bearer <token do -metrics-token-env>`). Fica fora do limite de concorrência e do período de encerramento para continuar respondendo quando o serviço está ocupado. **Nenhum rótulo contém tenant, serviço ou token**: as rotas são de uma lista fixa (outras viram `other`).

| Métrica | Rótulos | Uso |
|---|---|---|
| `kas_http_requests_total` | `route`, `status` | taxa de erros (5xx), 401/403 incomuns, 503 de capacidade |
| `kas_http_request_duration_seconds` | `route` (+`le`) | latência por rota |
| `kas_http_requests_in_flight` | — | saturação (o limite é `-max-concurrent`) |
| `kas_outbox_deliveries` | `state` = pending, in_flight, delivered, dead | fila e `dead` |
| `kas_outbox_oldest_due_pending_age_seconds` | — | consumidores parados |
| `kas_outbox_stats_up` | — | 0 se a leitura do banco falhou |
| `kas_service_token_checks_total` | `slot` = primary, next | andamento da rotação |
| `kas_services_rotating_tokens` | — | rotação em aberto |
| `kas_process_start_time_seconds` | — | reinícios |

As contagens da outbox são lidas no banco no máximo a cada 10 s e dependem do tamanho da tabela: mantenha a limpeza em dia.

## 7. Backup e restauração

O estado está no PostgreSQL (auditoria e Outbox) e, se usar `-audit-store jsonl`, em `-audit-data-dir`. A configuração e os tokens ficam fora do banco: guarde-os à parte (o arquivo de configuração não tem segredos; os tokens ficam no cofre).

### Backup

```bash
pg_dump --format=custom --no-owner --no-acl --file=kas-$(date +%Y%m%d-%H%M).dump "$KAS_AUDIT_MIGRATION_DATABASE_URL"
```

`--no-owner --no-acl` deixa de fora donos e concessões: o destino não precisa ter os mesmos papéis, e as concessões do runtime são reaplicadas por `deploy/grants.sql` (passo 4 da restauração). Guarde o arquivo cifrado e fora do servidor do banco. Para JSON Lines, copie `audit.jsonl` com a KAS parada (ou após `SIGTERM`), nunca no meio de uma escrita. Um backup que nunca foi restaurado não é um backup: faça o teste abaixo periodicamente.

### Restaurar

1. **Pare a KAS** (`SIGTERM`) para que nada escreva durante a restauração.
2. Com um papel que possa criar bancos (administrador do PostgreSQL), crie o banco de destino vazio com o papel de migração como dono e restaure:
   ```bash
   createdb --owner=kas_migrator kas_core_restored
   pg_restore --no-owner --role=kas_migrator --dbname=kas_core_restored kas-AAAAMMDD-HHMM.dump
   ```
3. Confira as tabelas de controle: `SELECT name FROM audit_schema_migrations; SELECT name FROM events_schema_migrations;`.
4. Aplique as migrações da versão que vai rodar (`-audit-migrate-only -events-migrate-only`) e depois `deploy/grants.sql` (as concessões não vêm no dump, por causa de `--no-acl`).
5. Aponte `KAS_AUDIT_DATABASE_URL` e `KAS_EVENTS_DATABASE_URL` para o banco restaurado e inicie a KAS. Confirme `GET /readyz` = 200 e `kas_outbox_stats_up 1`.

Consequências a esperar:

- **Perda desde o último backup.** Eventos e auditoria posteriores ao dump somem. Emissores que guardam o evento podem reenviá-lo com o mesmo corpo: a identidade `(tenant_id, service_id, event_id)` torna isso seguro (`201` se for novo, `200` se já existir).
- **Entregas `in_flight` expiram** e voltam como nova tentativa; entregas já confirmadas depois do backup reaparecem como `pending` e serão entregues de novo (consumidores idempotentes).
- Sequências de identidade continuam no valor do dump; não há reaproveitamento de IDs já usados.

### Teste de restauração (faça periodicamente)

Restaure para um banco descartável, inicie a KAS com `-audit-store postgres -events-store postgres` contra ele e rode `./scripts/compose-smoke.sh` (`BASE_URL` apontando para essa instância). Compare `SELECT count(*) FROM outbox_events` e `audit_events` entre o banco de origem e o restaurado.

## Limites

Este runbook cobre a preparação operacional de um piloto: não há alta disponibilidade, replicação, rotação automática de tokens, recarga de configuração sem reinício, agendador de limpeza, retenção da auditoria, alertas prontos nem hospedagem definida. A auditoria e a Outbox não são imutáveis nem resistentes a adulteração: quem administra o banco pode alterar linhas. A entrega é pelo menos uma vez e sem ordenação. O Docker Compose é só para desenvolvimento.
