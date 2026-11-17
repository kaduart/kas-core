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
- `event_id` não é único: reenvio grava nova linha. Decidir se o contrato deve rejeitar ou ignorar duplicatas.
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
