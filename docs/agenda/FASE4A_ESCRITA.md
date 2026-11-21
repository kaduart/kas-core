# Agenda — Fase 4A: criar, cancelar e remarcar (tenant fictício)

Status: **Implementada e validada em ambiente local e CI** (tenant fictício `clinica-aurora`).
Nada foi ligado em produção. A Agenda da Fono Inova continua **somente leitura**.

## O que mudou

| Camada | Mudança |
|---|---|
| `configstore` | Nova capacidade `scheduling:write` no módulo `scheduling`; permitida a serviços de painel (sempre presa à sessão da pessoa). |
| `scheduling.Store` | `ErrReadOnly` (tenant espelhado não aceita escrita nativa), `ErrClosed` (fora do horário de atendimento), `RequireOpen` em `NewBooking`, `RescheduleOpen`, cancelamento repetido idempotente. |
| `httpapi` | 3 rotas de escrita (`scheduling_write.go`), checagem de papel, mapeamento de erros, log `scheduling_booking_written` sem dado pessoal. |

## Contrato

Todas as rotas: `Content-Type: application/json`, corpo estrito (campo desconhecido = 400), `contract_version: "v1"`.
O tenant vem da credencial (e, no painel, da sessão); `tenant_id` só escolhe entre os tenants já permitidos.

### Criar

`POST /internal/v1/scheduling/bookings`

```json
{"contract_version":"v1","tenant_id":"clinica-aurora","idempotency_key":"web-7f3a",
 "resource_id":"prof-ana","client_ref":"cli-123","service_id":"avaliacao",
 "starts_at":"2026-11-30T09:00:00-03:00","ends_at":"2026-11-30T09:40:00-03:00","status":"scheduled"}
```

- `service_id` opcional; com ele, o buffer do serviço é lido na mesma transação. Sem ele, `buffer_min` (0–240).
- `status`: `scheduled` (padrão), `tentative` ou `confirmed`.
- 201 criado; 200 com `"replayed": true` quando a mesma chave chega de novo com o mesmo corpo.

### Cancelar

`POST /internal/v1/scheduling/bookings/{id}/cancel` → `{"contract_version":"v1","tenant_id":"…","expected_version":2}`

Cancelar algo já cancelado devolve o agendamento sem mudança (resposta perdida não vira erro).

### Remarcar

`POST /internal/v1/scheduling/bookings/{id}/reschedule` → `{"contract_version":"v1","tenant_id":"…","expected_version":2,"starts_at":"…","ends_at":"…"}`

### Resposta

```json
{"contract_version":"v1","tenant_id":"clinica-aurora","booking":{"id":"…","resource_id":"prof-ana","client_ref":"cli-123",
 "starts_at":"…","ends_at":"…","status":"scheduled","buffer_min":20,"blocked_until":"…","version":1,"updated_at":"…"}}
```

### Erros

| HTTP | código | quando |
|---|---|---|
| 400 | `invalid_request` | corpo inválido, status não permitido, fim antes do início, agendamento já encerrado |
| 401 | `session_required` / `session_invalid` | painel sem sessão válida |
| 403 | `tenant_forbidden` | pessoa sem vínculo no tenant |
| 403 | `role_forbidden` | papel `leitura` |
| 403 | (autorização) | serviço sem `scheduling:write` ou tenant não servido |
| 404 | `not_found` | recurso, serviço ou agendamento inexistente |
| 409 | `agenda_read_only` | tenant com Agenda espelhada (ex.: Fono Inova) |
| 409 | `slot_taken` | profissional/recurso ocupado (inclui buffer) |
| 409 | `client_busy` | cliente já tem horário sobreposto |
| 409 | `version_conflict` | `expected_version` desatualizada |
| 409 | `idempotency_mismatch` | mesma chave com outro corpo |
| 422 | `outside_opening_hours` | fora da regra semanal, em feriado ou em exceção |
| 503 | `scheduling_unavailable` / `capacity_exceeded` | Agenda desligada ou sobrecarga |

## Quem pode escrever

- Serviço com `scheduling:write` para o tenant → ator `service:<id>`.
- Painel (`identity:sessions`) → só pessoa com papel `admin`, `gestor` ou `atendimento` no tenant → ator `user:<id>`.
- `leitura` → 403 `role_forbidden`.

## Proteção da Agenda da Fono Inova

A trava fica no **banco/store**, não só na rota: se o tenant tem linhas em `sched_mirror_cursor` ou
`sched_mirror_items`, `CreateBooking`, `Reschedule` e `Cancel` devolvem `ErrReadOnly` dentro da transação.
Vale para qualquer chamador, mesmo um serviço com `scheduling:write`. O espelho continua escrevendo
pelo `MirrorApply`, que é o único caminho de escrita desse tenant.

Limitação declarada: um tenant que **ainda não recebeu** nenhuma importação não é reconhecido como
espelhado. Por isso a configuração de produção não deve conceder `scheduling:write` a nenhum serviço da
Fono Inova antes da decisão de migração (pendência de configuração, não de código).

## Concorrência (o que os testes provam)

`internal/httpapi/scheduling_write_test.go`, PostgreSQL real, 24 requisições simultâneas por cenário:

1. 24 clientes no mesmo horário → 1 × 201, 23 × `slot_taken`.
2. A mesma requisição 24 vezes (retentativas) → 1 × 201, 23 × 200 `replayed`, 1 agendamento só.
3. 24 remarcações da mesma versão → 1 × 200, 23 × `version_conflict`.
4. Cancelar × remarcar a mesma versão ao mesmo tempo → um vence, versão final 3.
5. Dois agendamentos diferentes disputando um horário livre → 1 vence.
6. Ao final: nenhuma sobreposição ativa (contando buffer) e `soma(version) = linhas de auditoria`.

Regressão C1–C11 da Fase 1 (`internal/scheduling`) continua passando sem alteração.

## Testes

| Teste | Cobre |
|---|---|
| `TestSchedulingWriteRoutesAuthorizeTenantAndRole` | sem sessão, `leitura`, pessoa de outro tenant, serviço sem capacidade, serviço de outro tenant, token errado; nada gravado nas recusas; ator na auditoria |
| `TestSchedulingWriteRefusedOnMirroredAgenda` | criar, cancelar e remarcar no tenant espelhado → 409, agenda e auditoria intactas |
| `TestSchedulingCreateRulesBufferHoursAndIdempotency` | buffer do serviço, cliente ocupado, antes/depois do horário, serviço/recurso inexistente, status inválido, replay, troca de corpo com mesma chave |
| `TestSchedulingCancelAndReschedule` | versão otimista, fora do horário, cancelamento repetido, remarcar cancelado, horário liberado, trilha `create,reschedule,cancel,create` |
| `TestSchedulingConcurrentWrites` | os 6 cenários acima |
| `TestSchedulingDisabled` | rotas de escrita respondem 503 com a Agenda desligada; nenhuma outra rota de escrita existe |
| `TestTenantsAreIsolated` (crmagenda) | escrita nativa recusada em tenant espelhado; espelho não sobrescreve agendamento nativo |
