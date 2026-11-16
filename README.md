# KAS Core — piloto local

Projeto independente em Go, mantido fora dos repositórios do CRM. Esta etapa contém dois módulos reutilizáveis: avaliação de configuração por tenant e registro de auditoria. Usa a biblioteca padrão Go e um único executável. Nenhum dos módulos está integrado ao CRM ou pronto para produção.

O valor fictício `logging.max_meta_keys` foi inspirado em um logger do legado, mas é somente dado de exemplo declarado em `config.example.json`; o núcleo não depende do logger nem de caminhos ou entidades do CRM. Todos os tenants, atores e recursos usados nos exemplos são fictícios.

## Executar localmente (PowerShell)

Defina tokens descartáveis de teste, não credenciais reais:

```powershell
$env:KAS_CALLER_FONO_NODE_TOKEN = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
$env:KAS_CALLER_DEMO_NODE_B_TOKEN = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
go run ./cmd/kas-core -config ./config.example.json -audit-data-dir ./data -addr 127.0.0.1:8088
```

Configuração, incluindo o registro central `tenant_registry`, fica em memória e é carregada no início. Cada registro define `id` e `status` (`active` ou `inactive`). IDs duplicados, status inválido e referências de configuração ou serviços a tenants não cadastrados impedem a inicialização. Tanto a avaliação quanto a auditoria exigem tenant cadastrado, ativo e permitido para o serviço autenticado. Alterações no registro exigem reinício; não há endpoint administrativo. Inativar um tenant bloqueia novas operações e preserva eventos históricos. O arquivo da auditoria fica em `./data/audit.jsonl`, separado do código-fonte. O processo mantém `./data/audit.lock`; uma segunda instância usando o mesmo diretório falha ao iniciar. Se o processo terminar abruptamente, o lock pode ficar obsoleto e bloquear a abertura. Confirme que o processo anterior parou antes de remover manualmente `audit.lock`; o programa não o apaga automaticamente.

O arquivo JSON Lines é validado integralmente na abertura. Um registro parcial, inválido ou acima do limite bloqueia o início. Não apague nem edite o arquivo automaticamente para contornar isso; preserve-o e investigue antes de qualquer reparo manual.

## Módulo de configuração

Contrato `v1`: `POST /internal/v1/config/evaluate`, descrito em [contract/v1/config-evaluate.md](contract/v1/config-evaluate.md). O esquema JSON declara chaves, tipo (`integer`, `boolean`, `string`) e faixa opcional de inteiros. O serviço autenticado precisa estar autorizado para o tenant solicitado. A configuração é somente leitura durante a vida do processo.

As respostas de autorização incluem `tenant_not_found` (404) para tenant ausente do registro e `tenant_inactive` (403) para tenant desativado; `tenant_forbidden` (403) continua indicando que o serviço autenticado não tem vínculo com o tenant.

## Módulo de auditoria

Contrato `v1`: `POST /internal/v1/audit/events`, descrito em [contract/v1/audit-events.md](contract/v1/audit-events.md). A gravação retorna `201` somente depois de escrever e sincronizar o evento. `service_id` no corpo precisa corresponder ao serviço autenticado. `actor` é uma declaração do serviço emissor, não uma identidade de usuário verificada pela KAS Core.

Os campos são estruturados e não há campo de payload arbitrário. Não envie tokens, dados pessoais, clínicos ou financeiros. Logs do servidor contêm somente resultado HTTP, código de erro fixo e duração, sem corpo ou credenciais.

O armazenamento é local, de processo único e append-only via API. Mutex protege escritas concorrentes dentro do processo; um lock de arquivo exclusivo bloqueia outra instância. A cada registro há `fsync`/`Sync` antes da resposta de sucesso. Isso não oferece proteção contra adulteração, imutabilidade, replicação ou entrega exatamente uma vez. Repetir uma chamada após falha ou perda da resposta pode gerar duplicatas. Não há busca, edição, exclusão por API, retenção automática, rotação ou arquivo remoto.

## Correlação HTTP

Ambos os endpoints de negócio aceitam o cabeçalho opcional `X-Correlation-ID`. O valor deve ter de 1 a 64 caracteres ASCII: letra ou número inicial, seguido apenas por letras, números, ponto, sublinhado ou hífen. Se ausente, a KAS gera um identificador aleatório de 32 caracteres hexadecimais. Se inválido ou repetido, a resposta é `400 invalid_correlation_id` e recebe um novo ID válido. O ID é propagado no `context.Context`, devolvido no cabeçalho de resposta e incluído nos logs estruturados, sem usar tokens ou corpos. Ele é apenas diagnóstico, nunca autorização ou seleção de tenant. Na auditoria, o cabeçalho HTTP permanece independente de `correlation_id` declarado no evento.

## Testes reproduzíveis

No diretório do projeto:

```powershell
go test ./...
cd node-integration
npm test
```

`npm test` compila o executável localmente e executa testes do cliente Node, a integração Go–Node já existente e um teste HTTP real da auditoria. O teste da auditoria valida envio autorizado, negação de acesso cruzado, bloqueio da segunda instância, reinício com preservação dos eventos e nova gravação. Usa somente tenants e tokens fictícios. Não precisa de banco ou serviços externos; loopback local pode exigir permissão do ambiente de execução.

O teste com detector de corrida (`go test -race ./...`) continua pendente neste ambiente: a compilação requer CGO e não há compilador C (`gcc`) disponível. Não foi instalado nenhum compilador.

## Limites

O diretório de dados local é um piloto, não uma solução de produção. É responsabilidade operacional manter backups, controlar permissões e espaço em disco, tratar locks obsoletos e definir recuperação de arquivo inválido. Não há canário ou certificação multi-tenant. O cliente Node permanece uma demonstração experimental isolada, sem conexão com o CRM.
