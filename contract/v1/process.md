# Contrato v1 — Process Studio (motor, piloto)

Status: **piloto**. O executável só liga estas rotas com `-process-store postgres` (padrão `none`, que responde `503 process_unavailable`). Especificação aprovada: spec v2 do KAS Process Studio.

O núcleo não carrega regra de negócio. Definições, papéis, `business_status` e tipos de comando são dados enviados pelo serviço do tenant. O `business_status` é opaco: quem interpreta é o CRM.

## Autenticação e tenant

Iguais às demais rotas internas: `X-KAS-Service` + `Authorization: Bearer <token>`. O tenant é resolvido a partir do serviço autenticado. `tenant_id` no corpo (ou na query, nos `GET`) só é obrigatório quando o serviço atende mais de um tenant, e nunca amplia o escopo. Todo corpo leva `"contract_version": "v1"`, é JSON estrito (campos desconhecidos e chaves duplicadas são recusados) e tem limite de 8 KiB (72 KiB para definições).

`occurred_at` é sempre o instante registrado pelo **servidor autenticado do CRM**, nunca pelo navegador. Mais de 30 s no futuro gera `400 clock_skew`. Instantes são normalizados para microssegundos (precisão do PostgreSQL), então um reenvio idêntico compara igual.

## Capacidades

| Capacidade | Rotas |
|---|---|
| `process:define` | `GET /internal/v1/process/definitions`, `GET /internal/v1/process/definitions/{key}?version=`, `POST /internal/v1/process/definitions:validate`, `POST /internal/v1/process/definitions`, `PUT /internal/v1/process/roles/{role}/members` |
| `process:run` | `GET /internal/v1/process/instances?process_key=&status=`, `POST /internal/v1/process/instances`, `GET /internal/v1/process/instances/{id}` |
| `process:task` | `GET /internal/v1/process/tasks?role=&status=`, `POST /internal/v1/process/tasks/{id}/resolve` |
| `process:command` | `POST /internal/v1/process/commands/{key}/result`, `POST /internal/v1/process/commands/{key}/query` |
| `process:event` | `POST /internal/v1/process/events` |

## Idempotência

Toda escrita tem chave própria: `publish_id`, `change_id`, `start_key`, `submission_key`, a `command_key` do comando, e `event_id`. Primeira escrita: `201` (resolve e resultado de comando: `200`). Reenvio idêntico: `200` com `"duplicate": true`. Mesma chave com conteúdo diferente: `409 idempotency_conflict`.

## Comandos ao CRM (Outbox pull)

A KAS não chama nada para fora. Ao entrar numa etapa `external_command`, a mudança de etapa e o evento `process.command.requested` são gravados **na mesma transação** (`events.PublishTx`). O CRM consome pelo pull já existente (`events:consume`) e segue esta ordem:

1. aplica o efeito e grava o recibo da `command_key` na mesma transação do CRM;
2. chama `POST /internal/v1/process/commands/{key}/result` com `confirmed` ou `rejected`;
3. só depois de `200` dá `ack` na entrega.

Sem resposta no prazo, a KAS publica `process.command.status_query` com a **mesma** chave. Uma revisão humana também reusa a chave enquanto o comando estiver `requested` ou `unknown`; geração nova só depois de `rejected`. Ids de evento: `cmd:<key>:r<tentativa>` (pedido) e `cmd:<key>:q<tentativa>.<n>` (consulta). Esperas: `process.wait.opened` com id `wait:<wait_key>`.

### Identificador do resultado (`result_ref`)

O corpo do resultado aceita `result_ref` opcional: o identificador de negócio do que o CRM criou para aquele comando (por exemplo, o id do pacote novo). É opaco, até 128 caracteres `[A-Za-z0-9._:-]`, **nunca dado pessoal**, e só vale com `confirmed` (com `rejected` é `400`). Fica gravado no comando (`commands[].result_ref` em `GET /instances/{id}`) e no histórico (`command_result.detail.result_ref`); a resposta `200` o devolve. Reenvio com o mesmo status e o mesmo `result_ref` = `200 duplicate`; outro `result_ref`, ou a falta dele, para uma chave já respondida = `409 idempotency_conflict`. Ao responder uma `process.command.status_query`, o CRM usa a mesma rota e o mesmo `result_ref` do recibo.

Uma etapa `external_command` pode declarar `"require_result_ref": true`: aí uma confirmação sem `result_ref` é recusada (`422 result_ref_required`), nada muda e o comando continua `requested` (as consultas seguem). Piloto: [docs/piloto-renovacao-pacote.md](../../docs/piloto-renovacao-pacote.md).

Os três tipos `process.command.requested`, `process.command.status_query` e `process.wait.opened` precisam estar em `event_types`, e o consumidor do CRM precisa estar em `event_consumers` vinculado ao serviço.

## Erros específicos

| HTTP | código | quando |
|---|---|---|
| 400 | `clock_skew` | `occurred_at` adiante do relógio do servidor |
| 403 | `role_not_allowed` | o papel informado não pode resolver a tarefa |
| 403 | `actor_not_in_role` | o ator não tinha o papel em `occurred_at` |
| 404 | `not_found` | recurso inexistente **para este tenant** |
| 409 | `task_closed` | tarefa já fechada |
| 409 | `command_unknown` | resolução levaria ao fim com comando ainda incerto; consulte o CRM antes |
| 409 | `command_not_pending` | consulta de comando que não está `requested`/`unknown` |
| 409 | `instance_active` | já existe instância em andamento para o `business_ref` |
| 409 | `wait_type_mismatch` | evento com `wait_key` de outra espera |
| 422 | `invalid_definition` | definição inválida (na publicação) |
| 422 | `result_ref_required` | confirmação sem `result_ref` numa etapa com `require_result_ref` |
| 503 | `process_unavailable` | motor desligado ou banco indisponível |

## Prazos

Hora de vencimento (`due_at`) é diferente da hora de execução: o timer roda em `cutoff_at` (= `due_at` + tolerância, padrão 5 min) + 30 s de assentamento e decide pelas respostas **já gravadas**. Resposta é no prazo se `occurred_at ≤ due_at`, recebida até `cutoff_at` e sem atraso de relato acima de 10 min. Resposta tardia é registrada para revisão (`record_only`). Prazo em dias úteis usa o calendário do tenant (fuso e feriados na definição), com corte de horário.
