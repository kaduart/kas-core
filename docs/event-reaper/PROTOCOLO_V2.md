# Protocolo v2: reaper com posse de execução

Par do CRM: branch `feat/eventstore-posse` (`docs/EVENTSTORE_OWNERSHIP.md` no CRM). Oráculo: `legacy-oracle/eventreaper-v2`, cópia sem modificação dos arquivos do CRM em `701d456`, com SHA-256 conferido a cada execução.

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
- `leaseExpiresAt` ausente e `createdAt` mais antigo que 15 min (eventos de código anterior à posse).

Decisão: `recoveryCount + 1 >= 3` vai para `dead_letter` (`error.code = RECOVERY_LIMIT_EXCEEDED`) com alerta. Senão a regra antiga de `attempts` (`< 3` vai para `pending`, senão `failed`), com `error.code = RECOVERED_STUCK_PROCESSING`.

Escrita: condicional ao `status`, ao token, ao prazo e ao `recoveryCount` observados, comparando o valor bruto (ausente casa só ausente; `null` casa só `null`). Remove token, dono e prazo. Se o worker renovar, concluir ou falhar no meio, nada é gravado (`lost_race`).

## Por que 3 recuperações

O maior `maxStalledCount` dos workers do CRM é 2 (o padrão do BullMQ é 1). Depois disso o BullMQ já não reentrega o job. Uma 3ª posse abandonada indica um evento que derruba o worker (por exemplo, falta de memória) e não deve circular para sempre.

## Como rodar

```
export KAS_TEST_ORACLE_V2_DIR=$PWD/legacy-oracle/eventreaper-v2
(cd legacy-oracle/eventreaper-v2 && npm ci)
go test ./internal/legacy/crmeventstore -run V2 -v
```

O executor v2 só roda contra banco descartável nesta fase, com as mesmas travas do v1.
