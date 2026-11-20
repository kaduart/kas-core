# KAS Core — piloto local

Projeto independente em Go, mantido fora dos repositórios do CRM. Procedimentos de operação (subir, migrar, limpar, rotacionar token, tratar `dead`, restaurar backup) estão no [RUNBOOK.md](RUNBOOK.md). Esta etapa contém três módulos reutilizáveis: avaliação de configuração por tenant, registro de auditoria e publicação de eventos com Outbox (fases 1 a 3), com capacidades separadas por serviço. Usa a biblioteca padrão Go (mais o driver `pgx` para o armazenamento opcional em PostgreSQL) e um único executável. Nenhum dos módulos está integrado ao CRM ou pronto para produção.

O valor fictício `logging.max_meta_keys` foi inspirado em um logger do legado, mas é somente dado de exemplo declarado em `config.example.json`; o núcleo não depende do logger nem de caminhos ou entidades do CRM. Todos os tenants, atores e recursos usados nos exemplos são fictícios.

## Executar localmente (WSL)

No Ubuntu WSL, entre na pasta do projeto e use somente tokens fictícios descartáveis:

```bash
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
export KAS_CALLER_FONO_NODE_TOKEN='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
export KAS_CALLER_DEMO_NODE_B_TOKEN='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
go run ./cmd/kas-core -config ./config.example.json -audit-data-dir ./data -addr 127.0.0.1:8088
```

Configuração, incluindo o registro central `tenant_registry`, fica em memória e é carregada no início. Cada registro define `id` e `status` (`active` ou `inactive`). IDs duplicados, status inválido e referências de configuração ou serviços a tenants não cadastrados impedem a inicialização. Tanto a avaliação quanto a auditoria exigem tenant cadastrado, ativo e permitido para o serviço autenticado. Alterações no registro exigem reinício; não há endpoint administrativo. Inativar um tenant bloqueia novas operações e preserva eventos históricos. O arquivo da auditoria fica em `./data/audit.jsonl`, separado do código-fonte. O processo mantém `./data/audit.lock`; uma segunda instância usando o mesmo diretório falha ao iniciar. Se o processo terminar abruptamente, o lock pode ficar obsoleto e bloquear a abertura. Confirme que o processo anterior parou antes de remover manualmente `audit.lock`; o programa não o apaga automaticamente.

O arquivo JSON Lines é validado integralmente na abertura. Um registro parcial, inválido ou acima do limite bloqueia o início. Não apague nem edite o arquivo automaticamente para contornar isso; preserve-o e investigue antes de qualquer reparo manual.

## Operação: contêiner, métricas e rotação de credenciais

- **Docker (desenvolvimento).** O `Dockerfile` tem dois estágios: compila um binário estático (`CGO_ENABLED=0`) e o copia para `gcr.io/distroless/static-debian12:nonroot`, que roda como usuário `65532`, sem shell. O `docker-compose.yml` sobe PostgreSQL com TLS e dois papéis, aplica as migrações, aplica `deploy/grants.sql` (só `INSERT`/`SELECT`, mais `UPDATE` nas entregas) e inicia a KAS com o papel restrito: `./scripts/dev-certs.sh && docker compose up --build`. Senhas e tokens padrão são fictícios e só para desenvolvimento; as portas ficam presas a `127.0.0.1`. **Não é uma implantação de produção.**
- **Rotação sem indisponibilidade.** Cada serviço aceita até dois tokens ao mesmo tempo: `token_env` e, opcionalmente, `token_env_next` (precisam ser diferentes). A troca exige editar a configuração e reiniciar duas vezes (começar a aceitar o novo; depois remover o antigo). `kas_service_token_checks_total{slot}` mostra se os clientes já migraram. Passo a passo no [RUNBOOK.md](RUNBOOK.md).
- **Métricas.** `GET /metrics` no formato Prometheus: `kas_http_requests_total` (rota e status), `kas_http_request_duration_seconds` (latência por rota), `kas_http_requests_in_flight`, `kas_outbox_deliveries` (por estado: pending, in_flight, delivered, dead), `kas_outbox_oldest_due_pending_age_seconds`, `kas_outbox_stats_up`, `kas_service_token_checks_total`, `kas_services_rotating_tokens` e `kas_process_start_time_seconds`. **Nenhum rótulo contém tenant, serviço ou token**; rotas fora da lista viram `other`. Só é servido sem token em listener de loopback; fora dele precisa de `-metrics-token-env` (senão fica desligado). Responde mesmo com o limite de concorrência esgotado ou durante o encerramento. As contagens da Outbox vêm do banco com cache de 10 s.

## Armazenamento PostgreSQL da auditoria (opcional)

Por padrão a auditoria continua em JSON Lines. Com `-audit-store postgres`, os eventos vão para a tabela `audit_events` em PostgreSQL. A string de conexão vem de uma variável de ambiente (padrão `KAS_AUDIT_DATABASE_URL`, configurável por `-audit-database-url-env`) e nunca de flag, arquivo de configuração, log ou mensagem de erro.

```bash
export KAS_AUDIT_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go run ./cmd/kas-core -config ./config.example.json -audit-store postgres -addr 127.0.0.1:8088
```

**Migrações.** O executor é o pacote compartilhado `internal/pgmigrate`; cada módulo (auditoria, eventos) tem os próprios arquivos SQL embutidos no executável e a própria tabela de controle, de modo que os módulos continuam independentes. Para a auditoria (`internal/audit/migrations`: `0001` cria a tabela, `0002` cria o índice único de identidade). Rodam de forma idempotente, serializadas por trava consultiva, e cada uma é registrada em `audit_schema_migrations`. Não há reversão automática. Se um banco criado só com a `0001` já tiver identidades duplicadas, a `0002` falha, é desfeita por inteiro e **nenhuma linha é apagada**; resolva as duplicatas manualmente no banco descartável e rode de novo.

**Idempotência.** A identidade de um evento é `(tenant_id, service_id, event_id)`. Reenvio idêntico (todos os campos, comparados exatamente como enviados, inclusive o texto de `occurred_at`) não grava nova linha e devolve `200` com o evento original e o `received_at` original. Mesma identidade com conteúdo diferente devolve `409 event_conflict` e não grava. O mesmo `event_id` em outro tenant ou serviço é outro evento. Isso é deduplicação por identidade, não promessa de entrega exatamente uma vez.

O JSON Lines aplica a mesma regra, com um índice em memória reconstruído a partir do arquivo inteiro na abertura (a memória cresce com o arquivo). Arquivos antigos com duplicatas continuam abrindo: vale o primeiro registro e nada no arquivo é alterado.

**TLS.** Para qualquer host que não seja loopback (`localhost`, `127.0.0.0/8`, `::1` ou socket Unix), a KAS recusa iniciar se a conexão puder ocorrer sem TLS: `sslmode=disable`, `allow` e `prefer` são rejeitados. Use `require`, `verify-ca` ou, de preferência, `verify-full` (o `require` criptografa, mas não autentica o servidor). Em loopback, `sslmode=disable` continua permitido para desenvolvimento.

**Usuários do banco (menor privilégio).** Use dois usuários:

- *Migração* (dono do esquema): cria e altera tabelas. Roda `-audit-migrate-only`, que lê `KAS_AUDIT_MIGRATION_DATABASE_URL` (ajustável por `-audit-migration-database-url-env`), aplica as migrações pendentes e encerra, sem exigir configuração nem tokens.
- *Runtime* (o processo da KAS): somente `INSERT` e `SELECT` em `audit_events` e `SELECT` em `audit_schema_migrations`, mais `USAGE` no schema e `CONNECT` no banco. Sem `UPDATE`, `DELETE`, `TRUNCATE` ou DDL. Inicie com `-audit-auto-migrate=false`: a KAS só verifica que todas as migrações embutidas já foram aplicadas e falha se houver alguma pendente.

```sql
-- como o usuário de migração (nomes e senha fictícios)
GRANT CONNECT ON DATABASE kas_core_test TO kas_runtime;
GRANT USAGE ON SCHEMA public TO kas_runtime;
GRANT INSERT, SELECT ON audit_events TO kas_runtime;
GRANT SELECT ON audit_schema_migrations TO kas_runtime;
```

Um teste de integração cria um papel com esses privilégios e confirma que ele insere, detecta duplicatas, e não consegue `UPDATE`, `DELETE`, `DROP` nem criar tabelas (ele é ignorado se o usuário de teste não puder criar papéis). Mesmo assim, quem administra o banco pode alterar linhas: não há promessa de imutabilidade nem de proteção contra adulteração.

**Gravação durável e prontidão.** `201` só é devolvido depois que o PostgreSQL confirma o `INSERT` (commit, conforme o `synchronous_commit` do servidor). Falha de banco devolve o erro genérico `audit_unavailable`, sem detalhes internos. `GET /healthz` é apenas liveness. `GET /readyz` também verifica o armazenamento de auditoria (ping ao PostgreSQL com limite de 3 s; no JSON Lines, falha se uma escrita falhou ou o arquivo foi fechado) e devolve `503 audit_unavailable` quando não está pronto.

**Outros limites.** Os textos originais de `occurred_at` e `received_at` ficam em colunas `*_raw` (nanossegundos preservados); colunas `timestamptz` servem para consulta. Máximo de 8 conexões, 10 s para abrir e 3 s por gravação, sem ajuste por configuração. A **configuração continua em arquivo**, carregada na inicialização. Os testes de integração com PostgreSQL só rodam se `KAS_TEST_DATABASE_URL` apontar para um banco local descartável (cada teste cria e remove seu próprio schema); sem a variável, são ignorados.

## Módulo de eventos (Outbox) — fases 1 a 3

Contrato `v1` em [contract/v1/events.md](contract/v1/events.md) e no [openapi.yaml](openapi.yaml). Publicação durável de eventos com idempotência e entrega a consumidores com bloqueio por prazo, retentativas e estado `dead`, sobre PostgreSQL. **A entrega é pelo menos uma vez, sem ordenação**: os consumidores precisam ser idempotentes, deduplicando por `(tenant_id, service_id, event_id)`. Não há promessa de entrega exatamente uma vez.

```bash
export KAS_EVENTS_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go run ./cmd/kas-core -config ./config.example.json -events-store postgres -addr 127.0.0.1:8088
```

- **Fase 1 — publicação.** `POST /internal/v1/events`, com a capacidade `events:publish` e as mesmas regras de tenant. O tipo de evento precisa estar declarado em `event_types` na configuração. O `payload` é um objeto JSON de até 16 KiB que leva **só identificadores**: o contrato proíbe dado clínico ou pessoal. A KAS aplica um formato restritivo (strings em formato de identificador, inteiros de 64 bits e booleanos, sem aninhamento, listas, `null` nem texto livre), mas **não consegue detectar** dado pessoal escrito em formato de identificador. Primeira publicação devolve `201`; reenvio idêntico devolve `200` com o evento original e não grava nada; mesma identidade com conteúdo diferente devolve `409 event_conflict`. O evento e uma entrega por consumidor correspondente (tipo **e** tenant) são gravados na **mesma transação**.
- **Fase 2 — trabalhadores.** O pacote `internal/events` tem um `Worker` que pega entregas com `FOR UPDATE SKIP LOCKED` e prazo de bloqueio (padrão 60 s), executa handlers em processo, aplica backoff exponencial com jitter (base 2 s, teto 5 min, 5 tentativas) e move para `dead` após esgotar as tentativas ou em falha permanente. Se um trabalhador cair, o prazo expira e outro refaz a entrega. Um trabalhador atrasado não sobrescreve o resultado. **Os handlers em processo existem só em testes**: o executável não registra nenhum. Não há webhook nem chamada externa.
- **Fase 3 — consumo por pull.** Um consumidor é um serviço autenticado que busca e confirma entregas, sem chamadas da KAS para fora. Exige a capacidade `events:consume` e que o consumidor esteja vinculado ao serviço por `"service"` em `event_consumers`. O escopo é a interseção entre os tipos e tenants do consumidor e os tenants do próprio serviço (tenants inativos ficam de fora), e a requisição não consegue ampliá-lo. `POST /internal/v1/events/deliveries/claim` entrega até 50 entregas (padrão 10) com prazo de bloqueio de 5 a 300 s (padrão 60 s) e um `lease_token` secreto por claim. `.../ack` confirma (um ack repetido também responde `200`). `.../nack` devolve a entrega com backoff exponencial e jitter; ao esgotar as 5 tentativas, ou com `"permanent": true`, ela vira `dead`. `ack` e `nack` só valem com o `lease_token`, o `attempt` e o consumidor corretos; fora disso, `409 lease_lost` e nada muda. Consumidor novo na configuração **não** recebe eventos já publicados (sem reprocessamento). Continua pelo menos uma vez: o consumidor precisa ser idempotente.
- **Retenção e limpeza.** Entregues, 30 dias; `dead`, 90 dias; um evento sai quando não tem mais entrega e foi recebido há mais de 30 dias. Nada é apagado sozinho: rode `-events-purge` (com `-events-purge-dry-run` para só contar), que usa `KAS_EVENTS_MIGRATION_DATABASE_URL`, apaga em lotes de 1000 e encerra. Precisa do papel de migração (dono do esquema), porque o papel de runtime não tem `DELETE`. Entregas `pending` e `in_flight` nunca são apagadas. Sem rodar a limpeza, as tabelas crescem sem limite.
- **Papéis do banco.** Migração: `-events-migrate-only` com `KAS_EVENTS_MIGRATION_DATABASE_URL` (dono do esquema). Runtime: `INSERT` e `SELECT` em `outbox_events` (nunca `UPDATE` ou `DELETE`); `INSERT`, `SELECT` e `UPDATE` em `outbox_deliveries` (nunca `DELETE`); `SELECT` em `events_schema_migrations`; mais `USAGE` no schema e `CONNECT` no banco. Inicie com `-events-auto-migrate=false`. A limpeza (`-events-purge`) usa o papel de migração, nunca o de runtime. A conexão exige TLS fora de loopback, como na auditoria.
- `GET /readyz` também verifica o armazenamento de eventos quando ele está habilitado.
- A configuração continua em arquivo: tipos de evento, consumidores e vínculos com serviços só mudam com reinício, e consumidores adicionados depois não recebem entregas de eventos já publicados.

### Leitura da auditoria (`audit:read`)

`GET /internal/v1/audit?entity_type=&entity_id=&since=&limit=&cursor=` devolve registros de auditoria de **um tenant**. Exige a capacidade `audit:read`, **separada** de `audit:write` (quem escreve não lê, e vice-versa). O tenant vem da autenticação do serviço: um serviço autorizado para um único tenant não manda nada e só lê esse tenant; um serviço autorizado para vários precisa enviar `tenant_id`, que é autorizado como qualquer outra requisição (só escolhe um dos seus próprios tenants ativos). Registros de outro tenant nunca são devolvidos, e o cursor guarda só uma posição, nunca um tenant.

A ordem é estável por `recorded_at` (o `received_at` atribuído pela KAS, em microssegundos) e depois pelo id do registro (identidade do PostgreSQL ou número da linha no JSON Lines). O `limit` vai de 1 a 200 (padrão 50); valores inválidos, parâmetros desconhecidos ou repetidos e cursor malformado dão `400`. Funciona nos dois armazenamentos. No PostgreSQL a migração `0003` acrescenta `entity_type`/`entity_id` e os índices de leitura; o papel de runtime só precisa de `SELECT` (que já tem). O contrato de escrita ganhou os campos **opcionais** `entity_type` e `entity_id` (juntos ou nenhum, só identificadores); quem não os envia continua funcionando. Detalhes e limites (inclusive o caso de um registro confirmado depois com `recorded_at` anterior) em [contract/internal/v1/audit-events.md](contract/internal/v1/audit-events.md) e no [openapi.yaml](openapi.yaml).

## Process Studio — motor (piloto)

Contrato em [contract/v1/process.md](contract/v1/process.md). Desligado por padrão (`-process-store none`). Com `-process-store postgres`, usa o **mesmo banco** do Outbox (`KAS_EVENTS_DATABASE_URL`, exige `-events-store postgres`), porque a mudança de etapa e o comando ao CRM são gravados na mesma transação. Um trabalhador de timers roda dentro do executável (`-process-timer-interval`, padrão 1 s) com `FOR UPDATE SKIP LOCKED`; vários executáveis podem rodar juntos sem disparar o mesmo timer duas vezes.

```bash
export KAS_EVENTS_DATABASE_URL='postgres://kas:kas_teste_local@localhost:5432/kas_core_test?sslmode=disable'
go run ./cmd/kas-core -config ./config.example.json -events-store postgres -process-store postgres -addr 127.0.0.1:8088
```

- Módulo `internal/process`; calendário de dias úteis em `internal/calendar`.
- Testes de aceite T1–T23 em `internal/process/acceptance_test.go`, com um CRM simulado que usa o pull real. Os tenants dos testes são fictícios (`demo-empresa-a`, 2 dias úteis; `demo-empresa-b`, 5 dias úteis).
- Papel de runtime: rode `deploy/grants-process.sql` depois de `deploy/grants.sql` (histórico, definições e eventos recebidos são só `INSERT`/`SELECT`).
- Editor visual (piloto) em [`studio/`](studio/README.md).
- Teste integrado com o executável real e o CRM simulado: `deploy/process-e2e/run.sh` (só termina com `TUDO OK`). O CRM simulado é `cmd/kas-crm-sim` e só aceita KAS em loopback.

## Módulo de configuração

Contrato `v1`: `POST /internal/v1/config/evaluate`, descrito em [contract/v1/config-evaluate.md](contract/v1/config-evaluate.md) e em [openapi.yaml](openapi.yaml). O esquema JSON declara chaves, tipo (`integer`, `boolean`, `string`) e faixa opcional de inteiros. O serviço autenticado precisa estar autorizado para o tenant solicitado e possuir a capacidade `config:read`. A configuração é somente leitura durante a vida do processo.

As respostas de autorização incluem `tenant_not_found` (404) para tenant ausente do registro e `tenant_inactive` (403) para tenant desativado; `tenant_forbidden` (403) continua indicando que o serviço autenticado não tem vínculo com o tenant.

## Módulo de auditoria

Contrato `v1`: `POST /internal/v1/audit/events`, descrito em [contract/internal/v1/audit-events.md](contract/internal/v1/audit-events.md) e em [openapi.yaml](openapi.yaml). O serviço autenticado precisa estar autorizado para o tenant e possuir a capacidade `audit:write`. A gravação retorna `201` somente depois de escrever e sincronizar o evento (ou confirmar o `INSERT` no PostgreSQL). Reenvio idêntico devolve `200` sem gravar de novo; mesmo `event_id` com conteúdo diferente devolve `409 event_conflict` (veja a seção PostgreSQL abaixo). `service_id` no corpo precisa corresponder ao serviço autenticado. `actor` é uma declaração do serviço emissor, não uma identidade de usuário verificada pela KAS Core.

Os campos são estruturados e não há campo de payload arbitrário. Não envie tokens, dados pessoais, clínicos ou financeiros. Logs do servidor contêm somente resultado HTTP, código de erro fixo e duração, sem corpo ou credenciais.

O armazenamento é local, de processo único e append-only via API. Mutex protege escritas concorrentes dentro do processo; um lock de arquivo exclusivo bloqueia outra instância. A cada registro há `fsync`/`Sync` antes da resposta de sucesso. Isso não oferece proteção contra adulteração, imutabilidade, replicação ou entrega exatamente uma vez. Repetir uma chamada após falha ou perda da resposta pode gerar duplicatas. Não há busca, edição, exclusão por API, retenção automática, rotação ou arquivo remoto.

## Correlação HTTP

Ambos os endpoints de negócio aceitam o cabeçalho opcional `X-Correlation-ID`. O valor deve ter de 1 a 64 caracteres ASCII: letra ou número inicial, seguido apenas por letras, números, ponto, sublinhado ou hífen. Se ausente, a KAS gera um identificador aleatório de 32 caracteres hexadecimais. Se inválido ou repetido, a resposta é `400 invalid_correlation_id` e recebe um novo ID válido. O ID é propagado no `context.Context`, devolvido no cabeçalho de resposta e incluído nos logs estruturados, sem usar tokens ou corpos. Ele é apenas diagnóstico, nunca autorização ou seleção de tenant. Na auditoria, o cabeçalho HTTP permanece independente de `correlation_id` declarado no evento.

## Testes reproduzíveis

No Ubuntu WSL, a partir do diretório do projeto:

```bash
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
go test ./...
go vet ./...
CGO_ENABLED=1 go test -race ./...
cd node-integration
npm test
```

`npm test` compila o executável localmente e executa testes do cliente Node, a integração Go–Node já existente e um teste HTTP real da auditoria. O teste da auditoria valida envio autorizado, negação de acesso cruzado, bloqueio da segunda instância, reinício com preservação dos eventos e nova gravação. Usa somente tenants e tokens fictícios. Não precisa de banco ou serviços externos; loopback local pode exigir permissão do ambiente de execução.

Para validar o código atual no WSL, execute os comandos de [VALIDACAO-AMANHA.md](VALIDACAO-AMANHA.md). O detector de corrida precisa de `CGO_ENABLED=1` e de um compilador C. Resultados de testes anteriores a uma alteração não validam essa alteração; o roteiro registra o que ainda deve ser executado.

O workflow [KAS Core CI](.github/workflows/ci.yml) executa os mesmos quatro checks em `ubuntu-latest` no push à branch `kas-core/validacao-core`, em pull requests para `main` e sob acionamento manual. Usa apenas tokens fictícios escritos nos testes; não lê segredos do repositório, não acessa produção e não faz deploy.

Serviços recebem capacidades na configuração de inicialização: `config:read`, `audit:write`, `audit:read`, `events:publish`, `events:consume` ou combinações. Campo ausente/vazio concede nenhuma capacidade; nomes desconhecidos ou repetidos impedem a inicialização. Para manter o funcionamento do exemplo, `config.example.json` declara explicitamente ambas para os dois serviços fictícios. A autorização por tenant continua obrigatória em conjunto com a capacidade.

## Limites

O diretório de dados local é um piloto, não uma solução de produção. É responsabilidade operacional manter backups, controlar permissões e espaço em disco, tratar locks obsoletos e definir recuperação de arquivo inválido. Não há canário ou certificação multi-tenant. O cliente Node permanece uma demonstração experimental isolada, sem conexão com o CRM.
