# Protocolo v2: reaper com posse de execução

Par do CRM: branch `feat/eventstore-posse` (`docs/EVENTSTORE_OWNERSHIP.md` no CRM). Oráculo: `legacy-oracle/eventreaper-v2`, cópia sem modificação dos arquivos do CRM em `a84a644`, com SHA-256 conferido a cada execução.

## Campos no documento do Event Store

| Campo | Quem grava | Uso |
|---|---|---|
| `processingToken` | worker, ao reivindicar (UUID por execução) | identifica a execução dona |
| `processingOwner` | worker | só diagnóstico |
| `leaseExpiresAt` | worker, renovado a cada `EVENT_LEASE_MS/3` (padrão 60 s) | o reaper só retoma depois que vence |
| `recoveryCount` | reaper | quantas vezes a posse foi abandonada |

## Regra do reaper v2

Elegível se `status: processing` e:
- `leaseExpiresAt <= agora`; ou
- sem posse (`processingToken` e `leaseExpiresAt` nulos ou ausentes, ou seja, reivindicado pelo código anterior à posse), **somente se a cerca já passou** (`-legacy-fence`, igual a `EVENT_LEGACY_FENCE_AT` do CRM) e com `createdAt` anterior a min(agora − 15 min, cerca). Sem cerca, esses eventos nunca são liberados: a execução antiga pode estar rodando.

Decisão: `recoveryCount + 1 >= 3` vai para `dead_letter` (`error.code = RECOVERY_LIMIT_EXCEEDED`) com alerta. Senão a regra antiga de `attempts` (`< 3` vai para `pending`, senão `failed`), com `error.code = RECOVERED_STUCK_PROCESSING`.

Escrita: condicional ao `status`, ao token, ao prazo e ao `recoveryCount` observados, comparando o valor bruto (ausente casa só ausente; `null` casa só `null`). Remove token, dono e prazo. Se o worker renovar, concluir ou falhar no meio, nada é gravado (`lost_race`).

## Por que 3 recuperações

Por job, o BullMQ executa no máximo `maxStalledCount + 1` vezes antes de declarar o job travado. O maior `maxStalledCount` do CRM é 2 (o padrão é 1), então 3 execuções abandonadas esgotam o que a própria fila permitiria. No CRM, a retomada direta por uma nova entrega também conta, então o limite vale mesmo sem o reaper. Isso fica no CRM: a KAS só faz a parte do reaper.

## Como rodar

```
export KAS_TEST_ORACLE_V2_DIR=$PWD/legacy-oracle/eventreaper-v2
(cd legacy-oracle/eventreaper-v2 && npm ci)
go test ./internal/legacy/crmeventstore -run V2 -v
```

O teste roda três cenários de cerca (vencida, ausente e futura). Em cada um, o resultado tem que ser idêntico ao do reaper do CRM, documento por documento.

O executor v2 só roda contra banco descartável nesta fase, com as mesmas travas do v1. Runbook: README, seção 5.
