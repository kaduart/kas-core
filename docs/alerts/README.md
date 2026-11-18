# Central de alertas (corte KAS)

## O que sai do Node

Hoje o CRM avisa por `infrastructure/alerts/alertService.js` (`sendAlert`). O comparativo abaixo mostra três problemas dele:

1. **A chave é tipo + nível.** Dois eventos diferentes do mesmo tipo em 5 minutos: o segundo é descartado e não volta.
2. **O cooldown é gravado antes do envio.** Se o webhook falha, o alerta é marcado como enviado (`sent: true`) e se perde. Os próximos 5 minutos ficam em silêncio.
3. **O estado vive em memória, por processo.** Cada processo tem o seu cooldown.

Para os cortes da KAS (recuperação do Outbox e reaper do Event Store), a central Go assume os avisos. O `alertService` do Node continua atendendo o resto do CRM. Nada dele foi alterado.

## Capacidade genérica × adaptador do CRM

| Peça | Pacote | Sabe do CRM? |
|---|---|---|
| Central: dedup, cooldown com agregação, escalonamento por severidade, nova tentativa de entrega | `internal/alerts` | não |
| Destinos: `SimulatedSink` (memória, testes) e `LogSink` (log estruturado) | `internal/alerts` | não |
| Adaptador: Outbox abandonado, pendentes atrasados (com o horário de suspensão do crm-worker), erro e divergência do reaper, textos em pt-BR | `internal/legacy/crmalerts` | sim |
| Ligação nos comandos | `cmd/kas-outbox-recovery`, `cmd/kas-event-reaper` | via adaptador |

Regras da central:
- **Dedup:** a mesma chave (origem, tipo, assunto) repetida dentro de 1 h é contada, não reenviada.
- **Cooldown:** cada grupo (origem, tipo) envia no máximo 1 notificação a cada 15 min. O que chega no meio é **agregado** e sai na próxima notificação, com a lista de assuntos (até 20) e a contagem do resto.
- **Escalonamento:** um alerta mais grave que o último notificado fura o cooldown.
- **Falha de entrega:** o alerta continua pendente e o cooldown não começa. A próxima chamada tenta de novo.

**Pendentes atrasados à noite:** das 19:10 às 07:30 nos dias úteis e no fim de semana, o crm-worker (que roda o dispatcher) fica suspenso. Nesse horário, pendentes atrasados são esperados e viram `info`, não `warning`. De dia, acima de 500 viram `critical`.

## Comparação Node × Go (mesmo cenário fictício)

Cenário: cinco eventos do Outbox abandonados (A a E), uma repetição de A e o webhook fora no minuto 36. Rodado com o `alertService.js` da main do CRM sem modificação (`legacy-oracle/alerts`, conferido por hash), com `fetch` simulado e relógio injetado.

```
00min alerta A                | Node: entregue                                     | Go: delivered
01min alerta B                | Node: descartado (cooldown)                        | Go: deferred
02min alerta A                | Node: descartado (cooldown)                        | Go: duplicate
03min alerta C                | Node: descartado (cooldown)                        | Go: deferred
20min flush                   | Node: (nada)                                       | Go: 2 notificações até aqui
36min alerta D (webhook fora) | Node: marcado como enviado, webhook falhou: perdido | Go: delivery_failed
37min alerta E                | Node: descartado (cooldown)                        | Go: delivered
Node: 1 notificação, eventos avisados [A]
Go:   3 notificações, eventos avisados [A B C D E]
```

Como reproduzir:

```
scripts/validate-alerts.sh
```

## O que falta para ativar

- **Canal monitorado.** Hoje a central entrega no log (`LogSink`). Um destino real (WhatsApp da equipe, e-mail ou Slack) precisa de decisão e credencial do Kadu. Isso **não** foi feito nesta rodada.
- **Uma instância por grupo.** Com dois processos de recuperação, cada um tem a sua central. O reaper já tem posse por lease; a recuperação do Outbox roda como processo único.
- Os demais bloqueios dos cortes: hospedagem e TLS, usuários Mongo restritos, 5 dias de sombra e OK para escrita.
