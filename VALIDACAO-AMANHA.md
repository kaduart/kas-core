# KAS Core — roteiro de validação no WSL

Este roteiro cobre as alterações preparadas em `main`. A validação deve ser feita no Ubuntu WSL depois que o PC estiver ligado. **Os resultados anteriores são anteriores a estas alterações e não as aprovam.**

## Alterações desta revisão

- O cliente Node agora encaminha `X-Correlation-ID` somente quando o valor satisfaz o mesmo formato e limite de 64 caracteres do endpoint Go. IDs inválidos ou longos são omitidos e o serviço gera um ID próprio; não há truncamento que altere silenciosamente a correlação.
- A comparação de inteiros do cliente de demonstração exige inteiros seguros de JavaScript. Valores fora da precisão exata são ignorados, em vez de produzir uma comparação potencialmente errada com o `int64` do Go.
- O contrato de configuração agora documenta `payload_too_large` para HTTP 413, igual à resposta real do handler.
- O `npm test` inclui um teste HTTP entre processos com tenants fictícios ativo, inativo e desconhecido para configuração e auditoria, além de acesso cruzado. Ele confirma que somente a gravação autorizada chega ao arquivo de auditoria.

## Exemplos fictícios cobertos

| Caso | Configuração | Auditoria |
|---|---:|---:|
| `tenant-demo-b` ativo e autorizado para `demo-node-b` | 200 | 201 |
| `tenant-demo-a` inativo | 403 `tenant_inactive` | 403 `tenant_inactive` |
| `tenant-unknown` não registrado | 404 `tenant_not_found` | 404 `tenant_not_found` |
| `fono-node-local` tenta acessar `tenant-demo-b` | 403 `tenant_forbidden` | 403 `tenant_forbidden` |

Os atores, serviços, tenants e tokens dos testes são fictícios. Não use credenciais reais.

## Comandos

No Ubuntu WSL:

```bash
cd /mnt/c/Users/User/Documents/projetos/kas-core-pilot
go test ./...
CGO_ENABLED=1 go test -race ./...
go vet ./...
cd node-integration
npm test
```

`npm test` compila o executável Linux local e executa os testes do cliente Node, da integração Go–Node, da auditoria HTTP com reinício e da matriz de tenants. Os arquivos de teste e dados temporários ficam sob `.tools/` e são removidos pelos testes quando terminam.

## Estado dos resultados

- Antes destas alterações, os resultados informados para `go test ./...`, `CGO_ENABLED=1 go test -race ./...` e `npm test` foram aprovados no WSL. A matriz HTTP também havia passado em execução temporária.
- Após estas alterações: **pendente executar novamente** os quatro comandos acima. Não considerar a validação concluída até registrar os resultados desta revisão.
- A política do Windows permanece ativa. Os testes entre processos devem rodar no WSL, não tentando executar o binário Linux pelo PowerShell.

## Limites para revisão

- O cliente Node continua sendo apenas uma demonstração isolada; não foi integrado ao CRM.
- A configuração é carregada na inicialização; mudanças no registro de tenants exigem reinício.
- A auditoria continua em JSON Lines local, de processo único, com confirmação após `Sync`. Isso não prova imutabilidade, proteção contra adulteração ou entrega exatamente uma vez.
- Não há banco, painel, API de busca/edição/exclusão, retenção ou recuperação automatizada. O piloto não está pronto para produção.
