# KAS Core — registro de validação local

Este arquivo registra resultados locais e comandos reproduzíveis. Ele não representa aprovação do GitHub Actions nem prontidão para produção.

## Estado do código testado

- Base: commit `302fbc800672797078b5e35b3d6d4795f5f52c97`.
- Os quatro resultados de baseline abaixo foram informados pelo usuário para esse commit, antes das alterações desta branch.
- A validação pós-alterações foi executada no Ubuntu WSL na branch local `kas-core/validacao-core`, sobre a árvore derivada de `0f6b7fd26fbf024c3f41af5a1b6d62962073c153`, já com as capacidades `config:read` e `audit:write`, autorização por tenant, configuração de exemplo e handlers HTTP. Os resultados abaixo correspondem aos arquivos de código e testes registrados nesta entrega. Esta é validação local; não implica aprovação do GitHub Actions.

## Resultados pós-alterações (Ubuntu WSL)

| Comando | Resultado |
|---|---|
| `go test ./...` | Aprovado; pacotes `audit`, `configstore` e `httpapi` passaram. `cmd/kas-core` e `strictjson` não têm testes próprios. |
| `go vet ./...` | Aprovado; terminou sem diagnósticos e a sequência prosseguiu. |
| `CGO_ENABLED=1 go test -race ./...` | Aprovado; detector de corrida executou nos pacotes Go com testes. |
| `npm test` | Aprovado: 11 testes, 0 falhas — 8 do cliente Node, 1 integração Go–Node, 1 auditoria HTTP/persistência após reinício e 1 matriz HTTP de tenants. |

O `npm test` completo foi repetido isoladamente para capturar a saída final depois que a primeira saída conjunta terminou antes de mostrar o resumo Node. A execução final terminou com código 0. Todos os dados e tokens usados são fictícios.

O OpenAPI foi carregado com o parser YAML já disponível no WSL; a sintaxe, os quatro caminhos e o enum de capacidades `config:read`/`audit:write` passaram. Esta validação confere YAML e estrutura básica, não substitui uma execução do workflow no GitHub Actions.

## O que os testes de capacidades cobrem

- Serviço com `config:read` e `audit:write`: acesso autorizado aos dois endpoints.
- Serviço somente com `config:read`: configuração permitida e auditoria negada com `capability_forbidden`.
- Serviço somente com `audit:write`: auditoria permitida e configuração negada com `capability_forbidden`.
- Serviço sem capacidades declaradas: ambos os endpoints negados por padrão.
- Acesso cruzado continua negado mesmo quando a capacidade da operação existe.
- Capacidade desconhecida ou duplicada rejeita a configuração na inicialização.
- Métodos HTTP incompatíveis recebem `405`, cabeçalho `Allow` e erro JSON.

## Comandos para reproduzir

```bash
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
go test ./...
go vet ./...
CGO_ENABLED=1 go test -race ./...
cd node-integration
npm test
```

`npm test` compila o executável Linux no diretório ignorado `.tools/` e executa a suíte unitária do Node e os testes entre processos. Não precisa de serviços externos nem usa credenciais reais.

## Validação remota

O workflow [`.github/workflows/ci.yml`](.github/workflows/ci.yml) configura os quatro checks em `ubuntu-latest` para push à branch `kas-core/validacao-core`, pull requests para `main` e execução manual. Até existir uma execução registrada pelo GitHub Actions, estes resultados permanecem exclusivamente locais no WSL.

## Limites preservados

- O Node continua como demonstração isolada, sem integração com o CRM.
- A configuração e o registro de tenants são lidos no início; mudanças exigem reinício.
- A auditoria é JSON Lines local, de processo único, e confirma após sincronização. Não oferece imutabilidade, proteção contra adulteração ou entrega exatamente uma vez; retentativas podem duplicar eventos.
- O piloto não inclui banco, painel, busca/edição/exclusão por API, retenção automatizada, backup, replicação ou recuperação automatizada, e não está pronto para produção.

## Rodada PostgreSQL (auditoria) — resultados no Ubuntu WSL

Base: branch `kas-core/validacao-core`, commit `5236b37` + alterações desta rodada (ainda sem commit no momento deste registro). PostgreSQL 16.15 local, banco descartável `kas_core_test`, credenciais fictícias.

**O que mudou:** novo `internal/audit/postgres.go` e migração `internal/audit/migrations/0001_audit_events.sql`; flags `-audit-store` e `-audit-database-url-env` em `cmd/kas-core/main.go`; novo `internal/audit/postgres_test.go`; serviço PostgreSQL com credenciais fictícias em `.github/workflows/ci.yml`; `go.mod` (diretiva `go` subiu de 1.22 para 1.25.0 por exigência do driver `pgx/v5`) e novo `go.sum`; README.

| Comando | Resultado |
|---|---|
| `go test ./...` sem `KAS_TEST_DATABASE_URL` | Aprovado; os 5 testes/subtestes de PostgreSQL ficaram como SKIP |
| `go test ./...` com banco local | Aprovado, incluindo 6 testes de PostgreSQL |
| `go vet ./...` | Aprovado, sem diagnósticos; `gofmt -l internal cmd` sem saída |
| `CGO_ENABLED=1 go test -race ./...` com banco local | Aprovado |
| `npm test` | 11 testes, 0 falhas (8 + 1 + 1 + 1). Esses testes usam o armazenamento JSON Lines, não o PostgreSQL |
| Teste manual do binário com `-audit-store postgres` | `201` para tenant autorizado; `403` para acesso cruzado; 1 linha gravada em `audit_events`; SIGTERM encerrou com código 0; senha ausente do log |

Os testes de PostgreSQL cobrem: campos preservados (incluindo nanossegundos), migração repetida e iniciada por 6 aberturas simultâneas, 60 gravações concorrentes, evento inválido, contexto cancelado, armazenamento fechado, falha de banco sem vazar detalhes e erro de conexão sem vazar a senha.

**Não validado:** o workflow do GitHub Actions com o novo serviço PostgreSQL (nenhuma execução remota foi feita nesta rodada); desempenho; queda e retomada do servidor PostgreSQL com o processo em execução; PostgreSQL com TLS; versões do PostgreSQL diferentes da 16.

**Limitações e dúvidas de negócio**
- A configuração continua só em arquivo. Persistir no banco exige decisão sobre edição, versionamento e atualização, hoje fora do escopo.
- ~~`event_id` não é único: reenvio grava nova linha.~~ Superado na rodada de idempotência abaixo.
- Sem retenção, particionamento, backup ou consulta de eventos.
- Quem acessa o banco pode alterar ou apagar linhas; nenhuma proteção contra adulteração é prometida.
- Não é pronto para produção.

**Roteiro curto de validação (WSL, ~5 min)**
```bash
sudo service postgresql start
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
export KAS_TEST_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go test -count=1 ./... && go vet ./... && CGO_ENABLED=1 go test -count=1 -race ./...
(cd node-integration && npm test)
```
Para rodar o servidor: veja a seção "Armazenamento PostgreSQL" no README. Se um schema de teste ficar para trás após uma falha, remova os schemas `kas_test_*` no banco descartável.

## Rodada de idempotência, TLS e prontidão — resultados no Ubuntu WSL

Base: commit `d14d3fe` + alterações desta rodada. PostgreSQL 16.15 local descartável, credenciais fictícias.

**Decisões do usuário implementadas:** `pgx/v5` mantido; identidade `(tenant_id, service_id, event_id)` com índice único (migração `0002`); reenvio idêntico → `200` com o evento original sem nova linha; mesmo `event_id` com conteúdo diferente → `409 event_conflict`; mesma regra no JSON Lines (índice em memória); TLS obrigatório fora de loopback; `/readyz` verifica a auditoria; usuário de runtime com só `INSERT`/`SELECT`, separado do de migração; configuração continua em arquivo.

**Para tornar o usuário de runtime utilizável** (não só documentado) acrescentei `-audit-migrate-only`, `-audit-migration-database-url-env` e `-audit-auto-migrate=false` (modo que só verifica migrações, sem DDL). Sem isso a KAS não iniciaria com um usuário sem direito de DDL.

**Arquivos alterados:** `internal/audit/{event,service,jsonl,postgres}.go`, `internal/audit/migrations/0002_audit_events_identity.sql` (novo), `internal/httpapi/handler.go`, `cmd/kas-core/main.go`, testes `internal/audit/{idempotency,postgres}_test.go` e `internal/httpapi/handler_test.go`, `contract/v1/{audit-events,config-evaluate}.md`, `openapi.yaml`, README e este arquivo.

| Comando | Resultado |
|---|---|
| `gofmt -l internal cmd` | Sem saída |
| `go vet ./...` | Aprovado |
| `go test ./...` sem `KAS_TEST_DATABASE_URL` | Aprovado; testes que precisam de banco ficaram como SKIP, e os de política de TLS rodaram |
| `go test ./...` com banco local | Aprovado; 27 testes de PASS e 1 SKIP no pacote `audit` |
| `CGO_ENABLED=1 go test -race ./...` com banco local | Aprovado |
| `npm test` | 11 testes, 0 falhas |
| Binário ponta a ponta (`-audit-migrate-only`, servidor com `-audit-auto-migrate=false`) | `/readyz` 200; primeiro envio 201; reenvio idêntico 200 com o mesmo `received_at`; conteúdo alterado 409; 1 linha no banco; SIGTERM com código 0; senha ausente dos logs; DSN remoto com `sslmode=disable` recusado com código 2; nenhum processo restante |

**O 1 SKIP** é `TestPostgresRuntimeRoleWithInsertSelectOnly`: o usuário de teste `kas` não pode criar papéis. Eu o executei separadamente concedendo `CREATEROLE` ao `kas` apenas durante o teste, no banco local descartável, e revoguei em seguida (`rolcreaterole = false`; nenhum papel `kas_rt_*` nem schema de teste restante). Resultado: PASS — o papel só com `INSERT`/`SELECT` abriu em modo verificado, gravou, detectou duplicata e **não** conseguiu `UPDATE`, `DELETE`, `DROP` nem criar tabela. Nas execuções comuns ele é ignorado. No CI o usuário do serviço PostgreSQL é superusuário, então o teste deve rodar lá (não confirmado).

Durante o primeiro desses testes um papel de teste ficou órfão por erro de ordem de limpeza; corrigi o teste, removi o papel e repeti o ciclo até a limpeza funcionar sozinha.

**Não validado:** o resultado do GitHub Actions deste commit (ver o resumo de entrega); queda e retomada do PostgreSQL com o servidor em execução; conexão TLS real com um PostgreSQL remoto (a política foi testada sobre a configuração analisada, sem servidor com TLS); desempenho; outras versões do PostgreSQL.

**Limitações**
- Reenvio só é "idêntico" se todos os campos forem iguais como enviados; o mesmo instante escrito em outro formato em `occurred_at` conta como conteúdo diferente (409).
- O índice em memória do JSON Lines cresce com o arquivo.
- Se um banco já tiver identidades duplicadas, a migração `0002` falha e nada é apagado; é preciso resolver manualmente.
- `sslmode=require` criptografa sem autenticar o servidor; `verify-full` é o recomendado.
- Sem retenção, backup, particionamento ou consulta. Quem administra o banco pode alterar linhas. Não é pronto para produção.

## Rodada Outbox (migrações compartilhadas, fases 1 e 2) — resultados no Ubuntu WSL

Base: commit `7870f1f` + alterações desta rodada. PostgreSQL 16.15 local descartável, credenciais fictícias. Sem webhook, sem chamada externa, sem identidade humana.

**Entregue**
1. **Migrações compartilhadas:** `internal/pgmigrate` (executor com tabela de controle própria por módulo, trava consultiva, transação por migração, verificação sem DDL) e `internal/pgdb` (abertura de pool com a política de TLS). A auditoria passou a usá-los mantendo a tabela `audit_schema_migrations` e a chave de trava, então bancos já migrados continuam compatíveis. `internal/pgtest` isola um schema por teste.
2. **Fase 1:** `POST /internal/v1/events` com a capacidade `events:publish`; tipos de evento e consumidores declarados na configuração (`event_types`, `event_consumers`); idempotência `201` / `200` / `409` por `(tenant_id, service_id, event_id)`; evento e entregas gravados na mesma transação.
3. **Fase 2:** `events.Worker` com `FOR UPDATE SKIP LOCKED`, prazo de bloqueio, backoff exponencial com jitter, estado `dead`, tratamento de pânico e de timeout, e impedimento de sobrescrita por trabalhador atrasado.
4. `/readyz` verifica o armazenamento de eventos quando habilitado. Flags novas: `-events-store`, `-events-database-url-env`, `-events-auto-migrate`, `-events-migrate-only`, `-events-migration-database-url-env`.

**Arquivos novos:** `internal/{pgdb,pgmigrate,pgtest}/*`, `internal/events/{event,service,postgres,worker}.go` e `migrations/0001_outbox.sql`, testes de cada pacote, `internal/httpapi/events_test.go`, `contract/v1/events.md`. **Alterados:** `internal/audit/postgres.go` (e seus testes), `internal/configstore/store.go` (+ testes), `internal/httpapi/handler.go`, `cmd/kas-core/main.go`, `config.example.json`, `openapi.yaml`, README, contratos de auditoria e configuração.

| Comando | Resultado |
|---|---|
| `gofmt -l internal cmd` | Sem saída |
| `go vet ./...` | Aprovado |
| `go test ./...` sem `KAS_TEST_DATABASE_URL` | Aprovado; os testes que exigem banco ficam como SKIP |
| `go test ./...` com banco local | Aprovado: 95 PASS e 2 SKIP (ver abaixo) |
| `CGO_ENABLED=1 go test -race ./...` com banco local | Aprovado |
| `npm test` | 11 testes, 0 falhas |
| OpenAPI | YAML válido; os 5 caminhos e a capacidade `events:publish` presentes; nenhuma referência `$ref` quebrada |
| Binário ponta a ponta | Padrão (`-events-store none`) devolve `503 events_unavailable`; modo verificado em schema vazio sai com código 2; `-events-migrate-only` sai com 0; DSN remoto sem TLS sai com 2; `/readyz` 200; 201 → 200 (mesmo `received_at`) → 409; payload aninhado `400 invalid_event`; tipo não declarado `422`; acesso cruzado `403`; evento do tenant B (sem consumidor) grava 201 e **nenhuma** entrega; SIGTERM com código 0; senha e payload ausentes dos logs; nenhum processo restante |

**Os 2 SKIP** (`TestPostgresRuntimeRoleWithInsertSelectOnly`, `TestRuntimeRoleHasLeastPrivilege`) exigem que o usuário de teste crie papéis. Executei os dois concedendo `CREATEROLE` ao `kas` só durante o teste, no banco local descartável, e revoguei depois (`rolcreaterole = false`, nenhum papel `kas_rt_*`/`kas_ev_*` nem schema `kas_*` restante). Os dois passaram: o papel de eventos publica, detecta duplicata, pega e conclui entregas, e **não** consegue `UPDATE`/`DELETE` em `outbox_events`, `DELETE`/`TRUNCATE`/`DROP` em `outbox_deliveries`, criar tabelas nem alterar a tabela de migrações. No primeiro ciclo houve uma falha de limpeza (`tuple concurrently updated`) por uma concessão no nível do banco usada em paralelo pelos dois pacotes; removi essa concessão dos testes, limpei o papel órfão e repeti. No CI o usuário do serviço PostgreSQL é superusuário, então esses testes devem rodar lá (não confirmado).

**Decisões conservadoras que tomei (revisar se discordar)**
- **Formato do payload mais estrito que "objeto JSON":** só strings em formato de identificador, inteiros de 64 bits e booleanos, até 32 chaves, sem aninhamento, listas, `null` nem texto livre. É a forma técnica de reforçar "só identificadores". Não detecta dado pessoal escrito como identificador (por exemplo, um número de documento). Relaxar é uma mudança pequena em `CanonicalPayload`.
- Payload acima de 16 KiB devolve `413 payload_too_large`; formato inválido devolve `400 invalid_event`. Tipo não declarado devolve `422 unknown_event_type`.
- Reenvio idêntico devolve `200` (mesma convenção da auditoria), comparando o conteúdo exatamente como enviado.
- Consumidores declarados na configuração com lista obrigatória de tipos **e** tenants; assinatura vale no momento da publicação (consumidor novo não recebe eventos antigos).
- O executável **não inicia trabalhadores** nem registra handlers: eles existem só nos testes, conforme combinado. Eventos publicados pelo binário ficam `pending`.
- Eventos exigem PostgreSQL; o padrão é desligado (`-events-store none`).
- Tempo de `next_attempt_at` e dos bloqueios vem do relógio do processo (não do banco); diferença de relógio entre instâncias afeta os prazos.
- Padrões do trabalhador: bloqueio 60 s, timeout do handler 30 s, 5 tentativas, backoff 2 s a 5 min, lote 10.

**Não validado**
- O resultado do GitHub Actions deste commit (ver o resumo de entrega).
- Queda e retomada do PostgreSQL com o servidor ou o trabalhador em execução; TLS real contra um PostgreSQL remoto; desempenho e volume; relógios desalinhados entre instâncias.
- Nenhuma limpeza de retenção foi implementada ou testada.

**Dúvidas registradas (sem bloquear o trabalho)**
- Fase 3 (consumo por pull): o consumidor precisará de um endpoint de busca e confirmação. Isso exige decidir autenticação do consumidor, formato de lote e como ficar dentro do veto a "busca/edição por API".
- Quem fará a limpeza de retenção? Precisará de um papel com `DELETE`, separado do runtime.
- Consumidor adicionado depois deve receber os eventos já publicados (reprocessamento)? Hoje não.
- Limite de crescimento das tabelas sem limpeza automática.

**Roteiro curto de validação (WSL, ~8 min)**
```bash
sudo service postgresql start
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
export KAS_TEST_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go test -count=1 ./... && go vet ./... && CGO_ENABLED=1 go test -count=1 -race ./...
(cd node-integration && npm test)
```
Para publicar um evento de verdade, veja a seção "Módulo de eventos (Outbox)" no README e o exemplo em `contract/v1/events.md`.

## Rodada Outbox fase 3 (consumo por pull e limpeza) — resultados no Ubuntu WSL

Base: commit `b1360b6` + alterações desta rodada. PostgreSQL 16.15 local descartável, credenciais fictícias. Sem webhook, sem chamada externa, sem identidade humana.

**Entregue**
- Capacidade `events:consume`. Um consumidor é vinculado a um serviço por `"service"` em `event_consumers`; o serviço autentica com o token de serviço. O escopo é a interseção dos tipos e tenants do consumidor com os tenants do serviço (inativos excluídos) e a requisição não consegue ampliá-lo.
- `POST /internal/v1/events/deliveries/claim` (lote de 1 a 50, padrão 10; prazo de 5 a 300 s, padrão 60 s; `lease_token` por claim), `.../ack` (idempotente em repetição) e `.../nack` (backoff com jitter; esgotadas as 5 tentativas, ou `"permanent": true`, vira `dead`). `ack`/`nack` só valem com consumidor, `delivery_id`, `attempt` e `lease_token` corretos; senão `409 lease_lost`.
- `-events-purge` (e `-events-purge-dry-run`) com o papel de migração: entregues após 30 dias, `dead` após 90 dias, e eventos sem entrega recebidos há mais de 30 dias; lotes de 1000; nunca roda sozinho; não toca em `pending`/`in_flight`.
- Sem reprocessamento: consumidor novo só recebe eventos publicados depois de configurado. Payload continua estrito.

**Arquivos novos:** `internal/events/{pull,purge}.go`, `pull_test.go`, `internal/httpapi/events_pull_test.go`. **Alterados:** `internal/events/postgres.go`, `internal/configstore/store.go` (+ testes), `internal/httpapi/handler.go`, `cmd/kas-core/main.go`, `config.example.json`, `internal/events/worker.go` (só o jitter padrão), `contract/v1/events.md`, `openapi.yaml`, README e contratos de auditoria e configuração.

| Comando | Resultado |
|---|---|
| `gofmt -l internal cmd` | Sem saída |
| `go vet ./...` | Aprovado |
| `go test ./...` sem `KAS_TEST_DATABASE_URL` | Aprovado; testes de banco ficam SKIP |
| `go test ./...` com banco local | Aprovado: 114 PASS e 2 SKIP (ver abaixo) |
| `CGO_ENABLED=1 go test -race ./...` com banco local | Aprovado |
| `npm test` | 11 testes, 0 falhas |
| OpenAPI | YAML válido; 8 caminhos; `events:consume` no enum; nenhuma `$ref` quebrada |
| Binário ponta a ponta | publicar 201; claim 200; ack com token alheio 409 `lease_lost`; ack 200; ack repetido 200; nack 200 `pending`; claim logo após o nack devolve lista vazia (backoff); após ~3 s volta como tentativa 2; nack permanente `dead`; serviço sem `events:consume` 403; lote de 51 retorna 400; estados finais `delivered:1` e `dead:2:consumer_permanent`; SIGTERM com código 0; tokens, senha e payload ausentes do log; purge em simulação conta 1 entregue e 1 `dead` sem apagar; purge real apaga 1 + 1 e os 2 eventos; sem a variável de migração ou com `-events-store none` sai com código 2; nenhum processo restante |

**Os 2 SKIP** são os testes de papel com menor privilégio (precisam criar papéis). Rodei os dois concedendo `CREATEROLE` ao `kas` só durante o teste, no banco local descartável, e revoguei (`rolcreaterole = false`, nenhum papel ou schema de teste restante). Passaram, agora incluindo claim e ack por pull sob o papel restrito (só `INSERT`/`SELECT` em `outbox_events`; `INSERT`/`SELECT`/`UPDATE` em `outbox_deliveries`; sem `DELETE`, `TRUNCATE`, DDL).

**Decisões conservadoras (revisar se discordar)**
- Consumidores desconhecidos, em processo e de outro serviço respondem o mesmo `403 consumer_forbidden`, para não revelar quais consumidores existem.
- `nack` não é idempotente (repetir dá `409`); `ack` é. Não há campo de texto livre no `nack`; só códigos curtos.
- `max_attempts` e backoff do pull usam os padrões fixos (5; 2 s a 5 min), sem flag. O bloqueio é por claim (um token por lote), não por entrega.
- A limpeza apaga eventos sem entrega com mais de 30 dias de recebidos, inclusive os que nunca tiveram consumidor. Em simulação, a contagem de eventos é um limite inferior.
- A limpeza usa uma conexão com limite de 30 minutos e lotes de 1000; não há flag para mudar os prazos de retenção (30/90 dias fixos).
- Tenant inativo deixa de ser entregue ao consumidor pull (as entregas ficam `pending` até o tenant voltar a ficar ativo). Mudar de status exige reinício.

**Não validado**
- O resultado do GitHub Actions deste commit (ver o resumo de entrega).
- Queda e retomada do PostgreSQL com o servidor em execução; TLS real contra PostgreSQL remoto; desempenho e volume (a limpeza foi testada com 2500 linhas, não com milhões); relógios desalinhados entre instâncias; um consumidor pull real em outro processo (os testes usam a API HTTP por `curl` e testes Go).
- A limpeza não foi executada contra um banco com dados reais, nem agendada.

**Dúvidas registradas**
- Quem agenda a limpeza (cron, tarefa do sistema) e com que frequência? Hoje é manual.
- Um consumidor pull precisa de renovação do prazo (heartbeat) para tratar lotes longos? Hoje só dá para reivindicar de novo após expirar; o prazo máximo é de 300 s.
- Deve haver uma forma de listar ou reenfileirar entregas `dead`? Está fora do escopo (veto a edição por API); hoje só por SQL com o papel de migração.
- Reprocessar eventos antigos para um consumidor novo continua fora do escopo.

**Roteiro curto de validação (WSL, ~10 min)**
```bash
sudo service postgresql start
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
export KAS_TEST_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go test -count=1 ./... && go vet ./... && CGO_ENABLED=1 go test -count=1 -race ./...
(cd node-integration && npm test)
```
Para o fluxo manual (publicar, claim, ack/nack, purge), veja `contract/v1/events.md` e a seção do README.

## Rodada operacional (branch `kas-core/operacional`) — resultados no Ubuntu WSL

Base: `main` em `39bb58d` (já tem as fases 1 e 2 do Outbox) **+ o commit da fase 3** (consumo por pull e `-events-purge`), trazido com `cherry-pick` (`6462c3d`, mesmo conteúdo de `fcfce3a`) porque a fase 3 ainda não está em `main` e o runbook precisa da limpeza. Se a fase 3 for mergeada antes, o `cherry-pick` fica como o mesmo patch e não conflita. Sem deploy, produção, merge, CRM ou identidade humana.

**Entregue**
1. **Dockerfile multi-stage:** compila com `CGO_ENABLED=0` (binário estático) e copia para `gcr.io/distroless/static-debian12:nonroot` (usuário `65532`, sem shell). `.dockerignore`.
2. **`docker-compose.yml` (só desenvolvimento):** PostgreSQL 16 com TLS e dois papéis (`kas_migrator`, `kas_runtime`), serviço `migrate`, serviço `grants` (`deploy/grants.sql`) e `kas` com o papel restrito e `-audit-auto-migrate=false`. TLS com certificado autoassinado gerado por `scripts/dev-certs.sh` (a KAS exige TLS fora de loopback, inclusive para o banco). Portas presas a `127.0.0.1`, `read_only`, `cap_drop: ALL`. Senhas e tokens padrão são fictícios.
3. **Rotação de credenciais:** `token_env_next` opcional por serviço; os dois tokens valem ao mesmo tempo, comparados sem parar no primeiro acerto; precisam ser diferentes e em variáveis diferentes. Vale para a autorização normal e a de consumidores pull. Log de início lista os serviços em rotação.
4. **`GET /metrics`:** formato Prometheus 0.0.4, sem dependência nova. `kas_http_requests_total{route,status}`, `kas_http_request_duration_seconds{route}`, `kas_http_requests_in_flight`, `kas_outbox_deliveries{state}`, `kas_outbox_oldest_due_pending_age_seconds`, `kas_outbox_stats_up`, `kas_service_token_checks_total{slot}`, `kas_services_rotating_tokens`, `kas_process_start_time_seconds`. Rotas de uma lista fixa (o resto vira `other`); nenhum rótulo com tenant, serviço ou token.
5. **`RUNBOOK.md`:** subir, migrar, limpeza, rotacionar token, tratar `dead`, métricas, backup e restauração.
6. **CI:** novo job `docker` (build da imagem, usuário não-root, binário inicia, `docker compose config`, pilha completa e `scripts/compose-smoke.sh`).

**Arquivos novos:** `Dockerfile`, `.dockerignore`, `.gitattributes`, `.env.example`, `docker-compose.yml`, `RUNBOOK.md`, `deploy/grants.sql`, `deploy/dev/initdb/10-roles.sh`, `scripts/{dev-certs,compose-smoke}.sh`, `internal/metrics/*`, `internal/httpapi/{metrics,metrics_test}.go`, `internal/events/{metrics,metrics_test}.go`. **Alterados:** `internal/configstore/store.go` (+ testes), `internal/httpapi/handler.go`, `cmd/kas-core/main.go` (+ teste), `.github/workflows/ci.yml`, `.gitignore`, `openapi.yaml`, contrato de configuração, README.

| Comando | Resultado |
|---|---|
| `gofmt -l internal cmd` | Sem saída |
| `go vet ./...` | Aprovado |
| `go test ./...` sem `KAS_TEST_DATABASE_URL` | Aprovado; testes de banco ficam SKIP |
| `go test ./...` com banco local | Aprovado: 135 PASS e 2 SKIP |
| `CGO_ENABLED=1 go test -race ./...` com banco local | Aprovado |
| `npm test` | 11 testes, 0 falhas |
| Build estático (`CGO_ENABLED=0`) | `ELF 64-bit ... statically linked` |
| YAML (OpenAPI, compose, CI) e sintaxe dos scripts | Válidos; 9 caminhos no OpenAPI, nenhuma `$ref` quebrada; scripts sem CRLF e com bit de execução |
| **Simulação do runbook em dois bancos descartáveis (40 verificações)** | 40 aprovadas, 0 falhas: migrar e `grants.sql` (e repetir), subir com o papel restrito em TLS e modo verificado, `compose-smoke.sh`, rotação (os dois tokens valem; token desconhecido 401; promoção; token antigo 401), `dead` (listar, reenfileirar, voltar a ser entregue), limpeza (simulação e real), `pg_dump`/`pg_restore`, migrar após restaurar, grants após restaurar, reenvio idêntico 200 e evento novo 201 no banco restaurado; bancos e papel removidos no fim |

**Os 2 SKIP** são os testes de papel com menor privilégio, inalterados nesta rodada (precisam criar papéis; validados antes com `CREATEROLE` temporário). Esta rodada não mexeu em privilégios. Na simulação do runbook, o servidor rodou o tempo todo com um papel de runtime criado só com `deploy/grants.sql`, inclusive lendo as estatísticas da Outbox (`kas_outbox_stats_up 1`) e publicando, reivindicando e confirmando eventos.

**A simulação encontrou um erro no meu rascunho do runbook**, já corrigido: `pg_dump` inclui as concessões (ACL) por padrão; o procedimento agora usa `--no-owner --no-acl` e reaplica `deploy/grants.sql` depois de restaurar.

**Não validado**
- **Docker:** não há Docker neste computador. A imagem, o `docker-compose.yml` (TLS do PostgreSQL por `entrypoint` com cópia da chave, serviços `migrate`/`grants` como tarefas únicas, `depends_on: service_completed_successfully`, `read_only`, usuário `65532` lendo o certificado) **só foram validados por análise do YAML e por reproduzir cada passo com o binário e o PostgreSQL locais**. A prova real é o job `docker` do Actions (ver o resumo de entrega); se ele falhar, o compose precisa de ajuste.
- Imagens base referenciadas por tag (`golang:1.25`, `postgres:16`, distroless `nonroot`), não por digest.
- Prometheus real raspando `/metrics` (o formato foi testado por testes e `curl`, não por um Prometheus).
- Queda e retomada do PostgreSQL com o servidor rodando; TLS contra PostgreSQL remoto com `verify-full`; volume; escalonamento.

**Decisões conservadoras (revisar se discordar)**
- `/metrics` sem token só em listener de loopback; fora dele fica **desligado** (404) a menos que `-metrics-token-env` esteja definido. É servido antes do limitador de concorrência e durante o encerramento.
- `kas_service_token_checks_total{slot}` agrega todos os serviços; durante uma rotação de mais de um serviço não dá para saber qual já migrou (o rótulo por serviço foi evitado de propósito).
- Rotação exige reinício duas vezes (sem recarga dinâmica, como o resto da configuração).
- O compose usa `sslmode=require` com certificado autoassinado (criptografa, não autentica); o runbook recomenda `verify-full` fora do desenvolvimento.
- A chave TLS de desenvolvimento fica legível por todos (`0644`) para os contêineres sem privilégio lerem pelo bind mount; o script avisa que nunca deve ser usada fora do desenvolvimento.
- Sem `HEALTHCHECK` na imagem (distroless não tem shell nem `curl`); use `/readyz` por fora.

**Dúvidas registradas**
- Onde e como a KAS será hospedada de verdade (a imagem e o compose são só preparação)? Orquestrador, rede, cofre de segredos e TLS definitivo dependem disso.
- Quem agenda a limpeza e quem recebe o alerta de `dead` e de `kas_outbox_stats_up 0`?
- Quer rótulo por serviço em `kas_service_token_checks_total`? Aumenta a cardinalidade e expõe nomes de serviços.
- Fixar as imagens base por digest e escanear vulnerabilidades no CI?

**Roteiro curto de validação (WSL, ~10 min)**
```bash
sudo service postgresql start
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
export KAS_TEST_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go test -count=1 ./... && go vet ./... && CGO_ENABLED=1 go test -count=1 -race ./...
(cd node-integration && npm test)
```
Com Docker instalado: `./scripts/dev-certs.sh && docker compose up --build` e, em outro terminal, `./scripts/compose-smoke.sh`.
