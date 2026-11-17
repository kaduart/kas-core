# KAS Core — registro de validação local

Este arquivo registra resultados locais e comandos reproduzíveis. Ele não representa aprovação do GitHub Actions nem prontidão para produção.

## Estado do código testado

- Base: commit `302fbc800672797078b5e35b3d6d4795f5f52c97`.
- Os quatro resultados de baseline abaixo foram informados pelo usuário para esse commit, antes das alterações desta branch.
- A validação pós-alterações foi executada no Ubuntu WSL na branch local `kas-core/validacao-core`, com as mudanças de capacidades `config` e `audit`, autorização por tenant, configuração de exemplo e handlers HTTP. O código e os testes correspondem ao commit `761aa4c2ef23a4836a79e9ea169f9c0d82bce54c`; os comandos foram executados na árvore local idêntica antes desse commit. As alterações posteriores neste roteiro são apenas documentação.

## Resultados pós-alterações (Ubuntu WSL)

| Comando | Resultado |
|---|---|
| `go test ./...` | Aprovado; pacotes `audit`, `configstore` e `httpapi` passaram. `cmd/kas-core` e `strictjson` não têm testes próprios. |
| `go vet ./...` | Aprovado; terminou sem diagnósticos e a sequência prosseguiu. |
| `CGO_ENABLED=1 go test -race ./...` | Aprovado; detector de corrida executou nos pacotes Go com testes. |
| `npm test` | Aprovado: 11 testes, 0 falhas — 8 do cliente Node, 1 integração Go–Node, 1 auditoria HTTP/persistência após reinício e 1 matriz HTTP de tenants. |

O `npm test` completo foi repetido isoladamente para capturar a saída final depois que a primeira saída conjunta terminou antes de mostrar o resumo Node. A execução final terminou com código 0. Todos os dados e tokens usados são fictícios.

O OpenAPI foi carregado com o parser YAML já disponível no WSL; a sintaxe, os quatro caminhos e o enum de capacidades `config`/`audit` passaram. Esta validação confere YAML e estrutura básica, não substitui uma execução do workflow no GitHub Actions.

## O que os testes de capacidades cobrem

- Serviço com `config` e `audit`: acesso autorizado aos dois endpoints.
- Serviço somente com `config`: configuração permitida e auditoria negada com `capability_forbidden`.
- Serviço somente com `audit`: auditoria permitida e configuração negada com `capability_forbidden`.
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
