# KAS Process Studio (piloto do editor)

Editor visual dos fluxos do motor (`internal/process`), com visual inspirado em BPMN, feito em React Flow. Ele edita o mesmo JSON `kas.process/v1` que o motor valida e não carrega nenhuma regra de negócio.

## O que faz

- **Desenho:** abre o fluxo de cada empresa e mostra todas as versões. Na etapa, edita papéis, prazo (dias úteis com corte, ou duração), tolerância, caminhos e status de negócio. Também dá para arrastar as etapas. Antes de publicar, o editor lista as alterações. **Publicar só fica liberado depois de Validar.** Cada publicação gera uma versão nova, e execuções já iniciadas continuam na versão delas.
- **Execuções:** inicia uma execução e acompanha a etapa atual no desenho, as tarefas com prazo (horário de Brasília), os comandos ao CRM e o histórico. As tarefas abertas são resolvidas com um ator fictício.

## Segurança do piloto

- O navegador nunca recebe token. O servidor do Studio (`server/kasProxy.ts`) guarda os tokens de serviço e faz o papel do servidor do CRM:
  - carimba `occurred_at` e `tenant_id`;
  - só alcança `/internal/v1/process/*` de leitura, validação, publicação, início e resolução;
  - não consome eventos e não reporta resultado de comando.
- A KAS precisa estar em loopback. Os servidores do Studio escutam só em `127.0.0.1`.

## Rodar localmente

No Windows (WSL), siga o [roteiro passo a passo](ROTEIRO-WINDOWS.md).

Pré-requisitos: Go 1.25+, Node 22, `psql`, e Docker (ou um Postgres local passado em `PG_ADMIN_URL`).

```bash
studio/scripts/stack.sh start          # Postgres descartável + KAS real + CRM simulado por empresa + versão 1 dos fluxos
cd studio && npm ci
set -a; source .stack/env; set +a
npm run dev                             # http://127.0.0.1:5174
../studio/scripts/stack.sh stop
```

## Testes

- `npm test`: unitários (modelo do desenho e proxy).
- `npm run e2e`: critério de aceite no navegador, com a stack local de pé. O teste:
  1. abre o fluxo de retenção;
  2. muda o prazo da empresa A;
  3. valida e publica a versão nova;
  4. confere que a empresa B não mudou;
  5. confere que a execução iniciada antes continua com o prazo original;
  6. acompanha uma execução até o CRM simulado confirmar.

  As capturas de tela ficam em `test-results/shots/`.
- No CI, o job `studio` roda tudo isso e publica as capturas como artefato.
