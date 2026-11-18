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

Regras da central (testadas em `internal/alerts/*_test.go`):
- **Dedup:** a mesma chave (origem, tipo, assunto) repetida dentro de 1 h é contada, não reenviada.
- **Cooldown:** cada grupo (origem, tipo) envia no máximo 1 notificação a cada 15 min. O que chega no meio é **agregado** e sai na próxima notificação, com a lista de assuntos (até 20) e a contagem do resto.
- **Escalonamento:** um alerta mais grave que o último notificado fura o cooldown.
- **Falha de entrega:** o alerta continua pendente e o cooldown não começa. A próxima chamada tenta de novo.
- **Tenant:** a chave e o grupo incluem o tenant. Repetição, cooldown ou destino fora de uma clínica não seguram outra.
- **Reinício:** com um `Store` (`MemoryStore` nos testes, `mongostore` num Mongo), dedup, cooldowns e alertas não entregues sobrevivem. A gravação vem depois da entrega: se o processo cair entre as duas, aquela notificação sai de novo depois do reinício. A garantia é **pelo menos uma vez**: pode repetir, mas não perde. Nos comandos atuais a central roda **sem** store, só em memória, porque eles estão em modo sombra e não gravam nada. Ligar o `mongostore` é parte da ativação.
- **Volume limitado:** 1.000 eventos distintos em 1 hora, num mesmo grupo, viram no máximo 5 notificações durante a hora, mais o resumo final, e os 1.000 aparecem na contagem.

**Pendentes atrasados à noite:** das 19:10 às 07:30 nos dias úteis e no fim de semana, o crm-worker (que roda o dispatcher) fica suspenso. Nesse horário, pendentes atrasados são esperados e viram `info`, não `warning`. De dia, acima de 500 viram `critical`.

## Comparação Node × Go (mesmo cenário fictício)

Cenário: cinco eventos do Outbox abandonados (A a E), uma repetição de A e o webhook fora no minuto 36. Rodado com o `alertService.js` da main do CRM sem modificação (`legacy-oracle/alerts`, conferido por hash), com `fetch` simulado e relógio injetado.

```
00min alerta A                | Node: entregue                                      | Go: delivered
01min alerta B                | Node: descartado (cooldown)                         | Go: deferred
02min alerta A                | Node: descartado (cooldown)                         | Go: duplicate
03min alerta C                | Node: descartado (cooldown)                         | Go: deferred
20min flush                   | Node: (nada)                                        | Go: 2 notificações até aqui
36min alerta D (webhook fora) | Node: marcado como enviado, webhook falhou: perdido | Go: delivery_failed
37min alerta E                | Node: descartado (cooldown)                         | Go: delivered
Node: 1 notificação, eventos avisados [A]
Go:   3 notificações, eventos avisados [A B C D E]
```

### Como ler "1 de 5" contra "5 de 5"

**Mais mensagens não é o critério.** O que se mede é se cada evento distinto chega a alguém, com atraso e volume limitados.

- **Volume:** os dois ficam baixos (Node 1, Go 3). O Go não manda uma mensagem por evento. B e C saíram juntos depois do cooldown, D e E também, e a repetição de A não gerou mensagem. No teste de volume, 1.000 eventos viram no máximo 6 notificações.
- **Cobertura:** o Node avisou só A. B, C e E foram descartados pelo cooldown porque a chave dele é tipo + nível. D foi marcado como enviado com o webhook fora. Ninguém ficaria sabendo de 4 dos 5 eventos. O Go avisou os 5.
- **Atraso:** o Go segura B e C até o fim do cooldown, cerca de 15 a 20 minutos. Não é instantâneo, por escolha. Só um alerta mais grave fura essa espera.
- **Repetição:** depois de um reinício, o Go pode repetir uma notificação (pelo menos uma vez). O Node, ao reiniciar, perde o cooldown e pode mandar de novo também, mas sem guardar pendências.

Como reproduzir:

```
scripts/validate-alerts.sh
```

## O que falta para ativar

- **Canal monitorado.** Hoje a central entrega no log (`LogSink`). Um destino real (WhatsApp da equipe, e-mail ou Slack) precisa de decisão e credencial do Kadu. Isso **não** foi feito nesta rodada.
- **Uma instância por grupo.** Com dois processos de recuperação, cada um tem a sua central. O reaper já tem posse por lease; a recuperação do Outbox roda como processo único.
- Os demais bloqueios dos cortes: hospedagem e TLS, usuários Mongo restritos, 5 dias de sombra e OK para escrita.
