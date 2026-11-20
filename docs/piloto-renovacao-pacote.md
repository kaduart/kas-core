# Piloto renovação de pacote (Process Studio, Frente 3)

Piloto que reaproveita o motor (`internal/process`) e o editor (`studio/`) já aceitos. Tudo aqui é fictício: a clínica é `demo-clinica`, as famílias e os pacotes são referências inventadas, e o CRM é o simulado (`cmd/kas-crm-sim`). Nada fala com Render, Atlas, CRM real ou WhatsApp.

## Regra de conclusão

A execução só termina como **`renovado`** quando o CRM confirma o **identificador de um pacote NOVO criado para aquele pedido**. Promessa da família, mensagem enviada ou o "renovou" da secretaria **não** fecham: eles só levam ao comando `crm.package.create_renewal`, e a conclusão depende da confirmação do CRM com `result_ref` (o id do pacote novo).

## Fluxo `renovacao-pacote`

Definição: [`studio/samples/renovacao-pacote.json`](../studio/samples/renovacao-pacote.json). O Studio abre essa mesma definição (`studio/scripts/stack.sh start` publica para `demo-clinica`) e os testes usam o mesmo arquivo.

| Etapa | Tipo | O que faz |
|---|---|---|
| `falar_com_familia` | tarefa humana, papel `secretaria` | prazo de **2 dias úteis** com corte às 18:00 (calendário do tenant, com feriados); respostas `renovou`, `pausa_combinada`, `vai_sair` |
| `decidir` | decisão | `renovou` → comando; `pausa_combinada` → `pausado`; `vai_sair` → `nao_renovou` |
| `criar_pacote_novo` | comando ao CRM `crm.package.create_renewal` | `require_result_ref: true`; espera 1 h, até 3 consultas com a **mesma** `command_key`; confirmado → `renovado`; recusado ou sem resposta → supervisão |
| `revisar_supervisao` | tarefa humana, papel `supervisao` | recebe prazo vencido, recusa e falta de resposta; não consegue encerrar enquanto o comando estiver incerto (`409 command_unknown`) |
| `fim_renovado` / `fim_pausado` / `fim_nao_renovou` | fim | resultados `renovado`, `pausado`, `nao_renovou` |

O CRM inicia a execução (`process:run`) com `business_ref` = id do pacote que está acabando e `start_key` único por pacote (por exemplo `renovacao-<id do pacote>`).

## O que mudou no motor (aditivo)

- `POST /internal/v1/process/commands/{key}/result` aceita `result_ref` opcional: identificador opaco do que o CRM criou, até 128 caracteres `[A-Za-z0-9._:-]`, nunca dado pessoal, só com `confirmed`. Fica gravado (migração `0002_command_result_ref.sql`, coluna nova em `process_commands`), aparece em `commands[].result_ref` da instância e no histórico (`command_result.detail.result_ref`).
- Idempotência: reenvio idêntico (mesmo status e mesmo `result_ref`) = `200 duplicate`; outro `result_ref` (ou a falta dele) para a mesma chave já respondida = `409 idempotency_conflict`.
- Etapa `external_command` pode declarar `require_result_ref: true`: confirmação sem `result_ref` é recusada com `422 result_ref_required` e nada muda (o comando segue `requested` e as consultas continuam).
- CRM simulado: `-drop-first-result-refs` perde de propósito a **primeira** confirmação de cada comando daqueles `business_ref` (aceita prefixo com `*`), depois de aplicar o efeito e gravar o recibo. Um `crm.package.create_renewal` confirmado cria **um** pacote por `command_key` (`pacote-novo-N`) e o informa como `result_ref`; consultas e reentregas respondem com o mesmo pacote, a partir do recibo.

## Testes

- `internal/process/renewal_pilot_test.go`: R1 caminho feliz com id do pacote no histórico; R2 confirmação perdida → consulta com a mesma chave → mesmo pacote, exatamente um; R3 reenvio idêntico 200 / outro `result_ref` 409 / sem `result_ref` 422 / formato inválido 400; R4 prazo de 2 dias úteis → revisão da `supervisao`; R5 `vai_sair` e `pausa_combinada` sem comando; R6 sem resposta alguma → `unknown` → supervisão, que não consegue encerrar.
- `internal/httpapi/process_result_ref_test.go` e `internal/crmsim/sim_renewal_test.go`: o mesmo pela API HTTP e pelo CRM simulado real.
- `deploy/process-e2e/run.sh`, cenário 6: executável real + CRM simulado real (só termina com `TUDO OK`).
- `studio/e2e/renovacao.spec.ts`: no navegador, abre o fluxo, publica uma versão com espera curta e acompanha a execução até `renovado` depois da consulta.

## Roteiro de demonstração (local, fictício)

Pré-requisitos: Go 1.25+, Node 22, `psql`, `jq` e Docker (ou um Postgres local em `PG_ADMIN_URL`).

1. Suba a stack descartável: `studio/scripts/stack.sh start` (KAS real em loopback, CRM simulado por empresa, `demo-clinica` com `renovacao-pacote` v1).
2. Abra o Studio: `cd studio && npm ci && set -a && source .stack/env && set +a && npm run dev` → <http://127.0.0.1:5174>, empresa **demo-clinica**. O fluxo aparece com a tarefa da secretaria em 2 dias úteis.
3. Na etapa **criar_pacote_novo**, troque "Espera pela resposta" para `PT10S` (só para a demo; no piloto é `PT1H`), clique **Validar** e depois **Publicar** (versão 2).
4. Em **Execuções**, inicie a referência `pacote-demo-perde-1` (o CRM simulado da stack perde a primeira confirmação de toda referência que começa com `pacote-demo-perde-`).
5. Na tarefa `falar_com_familia`, clique **renovou**. A execução **não** termina: fica em `criar_pacote_novo`, comando `requested`. O CRM já criou o pacote (`studio/.stack/sim-clinica.json`, campo `packages`) e a confirmação se perdeu (`dropped`).
6. Depois de ~10 s, a KAS publica a consulta de status com a mesma chave; o CRM responde do recibo com o mesmo pacote. A execução fica **concluída: renovado**, e o comando mostra "identificador no CRM: `pacote-novo-1`" e "consultas 1".
7. Confira o histórico: comando enviado → timer (sem resposta) → consulta de status → resultado registrado com `pacote-novo-1` → concluída. Em `sim-clinica.json` existe **um** pacote e o efeito rodou uma vez.
8. Para encerrar: `studio/scripts/stack.sh stop`. Sem navegador, o mesmo roteiro roda inteiro em `deploy/process-e2e/run.sh` (cenário 6).

## Decisões pendentes do dono

1. **Qual registro do CRM real prova a renovação.** Proposta: um `Package` novo criado com vínculo ao id do pedido (a `command_key`) e ao pacote que acabou (`business_ref`); o id desse pacote volta como `result_ref`. Falta confirmar o modelo e o campo onde o CRM grava o recibo da `command_key` na mesma transação do pacote.
2. **Quem são `secretaria` e `supervisao`** (membros e quem os mantém via `PUT /roles/{role}/members`).
3. **Prazos:** 2 dias úteis com corte às 18:00, espera de 1 h pela resposta do CRM e 3 consultas são valores de piloto; confirmar, e confirmar o calendário de feriados da clínica.
4. **O que a supervisão faz** quando o CRM recusa ou não responde (hoje: decide de novo entre renovou/pausa/vai_sair, e não consegue encerrar com comando incerto).
5. **Hospedagem e ativação** (`-process-store postgres` em algum ambiente, CRM real consumindo o Outbox) seguem como decisões separadas; este PR não liga nada fora do ambiente isolado.
