# Reaper do EventStore: divergências e defeitos conhecidos do legado

Regra desta migração: o comportamento do Node é preservado. Um defeito do legado não é corrigido em silêncio. Cada item abaixo tem um teste que o fixa.

## Divergências intencionais da versão Go

**DV-1 foi aceita pelo Kadu em 18/11/2026 como correção intencional:** o reaper não deve sobrescrever um evento que o worker já concluiu. DV-2 a DV-4 são consequências operacionais (contagem, limite de tempo, trava entre reapers) e não mudam o documento gravado.

| Id | Node | Go | Por quê | Teste |
|---|---|---|---|---|
| DV-1 | `updateOne({eventId})` sem condição: se o worker concluir entre a leitura e a escrita, o reaper sobrescreve `processed` com `pending` | Escrita condicional: só aplica se ainda estiver `processing` com os mesmos `attempts`; senão conta `lost_race` | O pedido exige transição atômica, e a sobrescrita faz um evento concluído parecer não concluído (`eventExists` passa a responder falso) | `TestCompletionBetweenReadAndWriteIsNotOverwritten` (mostra também a sobrescrita do legado) |
| DV-2 | Um `updateOne` que não casa nada conta como sucesso | Conta como `lost_race`, separado | Visibilidade | idem |
| DV-3 | Sem limite de tempo: um banco que não responde trava o ciclo, e `isRunning` fica verdadeiro para sempre | `-cycle-timeout` (padrão 2 min) | Robustez | `scripts/validate-event-reaper.sh` (pausa de 8 s) |
| DV-4 | Trava só por processo (`isRunning`) | Lease com fencing entre processos e máquinas | Evitar dois executores | `TestFormerLeaseHolderIsFencedOut`, `TestOnlyOneLiveLeaseHolder` |

**Alcance da equivalência.** A afirmação "0 divergências com o Node" vale **somente sem disputa entre escritores**, isto é, quando nenhum worker conclui, falha ou reivindica o evento entre a leitura e a escrita do reaper. Foi isso que o oráculo comparou em 10.000 eventos. No cenário de conclusão concorrente, o comportamento é **diferente por decisão** (DV-1): o Node grava `pending` por cima de `processed`, e a versão Go deixa `processed`. Esse cenário não entra na equivalência; ele é coberto por `TestCompletionBetweenReadAndWriteIsNotOverwritten`.

## Trava entre reapers não é posse da execução

São duas coisas diferentes, e esta entrega só resolve a primeira:

| | O que garante | O que **não** garante |
|---|---|---|
| **Lease do executor** (`kas_event_reaper_lease`) | No máximo um reaper KAS grava por vez; um reaper antigo cujo lease foi tomado não grava (fencing) | Nada sobre os workers. Não impede que um worker continue rodando nem que conclua depois. |
| **Escrita condicional do reaper** | Cada transição `processing → pending/failed` acontece no máximo uma vez por estado observado e não apaga uma conclusão já gravada | Não impede que um worker que estava rodando grave `processed` **depois** do resgate (o legado conclui sem condição; ver F-1) |
| **Posse da execução pelo worker** (F-1) | Ainda **não existe** no legado | — |

Os testes que dizem "cada evento foi movido exatamente uma vez" falam das **transições do reaper**, não do processamento. **Esta entrega não promete processamento exatamente uma vez**: o BullMQ reentrega jobs, e um worker resgatado pode ter executado efeitos externos antes de perder a posse.

## Defeitos do legado preservados (registrados, não corrigidos)

| Id | Defeito | Efeito | Teste |
|---|---|---|---|
| L-1 | A idade é medida por `createdAt`, não pelo início do processamento (que não é registrado) | Um evento criado há mais de 15 min que um worker acabou de pegar (retentativa do BullMQ) é tratado como preso **enquanto ainda roda**. Isso pode levar a execução duplicada. | `TestKnownDefectL1AgeComesFromCreatedAtNotFromProcessingStart` |
| L-2 | O reaper não incrementa `attempts` | Um evento cujo worker morre toda vez alterna entre `processing` e `pending` para sempre e nunca vira `failed` | `TestKnownDefectL2ReaperNeverIncrementsAttempts` |
| L-3 | Comparação JS sobre o valor cru | `attempts` ausente vai para `failed`; `null` vai para `pending`; string `"2"` vai para `pending`; `"abc"` vai para `failed` | `TestDecideCharacterisesLegacyRule`, oráculo |
| L-4 | `error` é substituído inteiro | Perdem-se `stack` e `code` da falha anterior; `recoveredAt`/`recoveredBy` nunca chegam ao banco (descartados pelo Mongoose) | `TestApplyWritesExactlyWhatTheLegacyReaperStores`, oráculo |
| L-5 | Um lote de 100 a cada 5 min | No máximo 1.200 eventos por hora; um acúmulo grande demora | `TestBatchLimitAndOrderFollowTheLegacyQuery` |
| L-6 | Ordenação só por `createdAt` | Empates saem em ordem indefinida. O resultado final não muda. | — |

## F-1: impedir que um consumidor antigo conclua depois de perder a posse

**Hoje é impossível sem mudar o CRM.** O legado não tem dono nem token na execução: `processWithGuarantees` grava `status:'processing'` sem marca, e `markEventProcessed` salva por `_id` sem condição. Se o reaper devolve o evento para `pending` e o worker antigo termina depois, a conclusão dele vence. O teste `TestFencedClaimProtocolStopsAStaleConsumer` demonstra isso.

O lado da KAS já está pronto para o protocolo cercado. Se um documento tiver `processingToken`, o reaper exige o mesmo token na escrita e o remove. O mesmo teste mostra um consumidor com token sendo barrado.

**Proposta para uma etapa futura (mudança no CRM, com aprovação):**
1. Ao reivindicar, gravar `processingToken` (aleatório) e `processingStartedAt` com `updateOne({eventId, status:{$ne:'processing'}}, ...)`.
2. Concluir e falhar com `updateOne({eventId, processingToken: meuToken}, ...)`. Se não casar, a execução perdeu a posse e não grava.
3. O reaper passa a medir a idade por `processingStartedAt`, o que corrige L-1. Essa troca seria uma mudança de comportamento explícita, ligada por flag.
