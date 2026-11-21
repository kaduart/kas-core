# Agenda — Fase 3B: preparo da importação real protegida

Status: **Planejada e ferramentas prontas; execução real Bloqueada** (depende de autorização para ler o Atlas ou para
copiar dados reais para a réplica local). Nada aqui conectou ao Atlas, copiou dado real ou acessou produção.

## 1. O que já existe e foi validado com dados fictícios

| Peça | Onde | Prova |
|---|---|---|
| Leitura mínima (só 9 campos de `appointments`, 3 de `doctors`) | `crmagenda.Projection`, `DoctorProjection` | teste de projeção e varredura de colunas pessoais |
| Ids trocados por HMAC (`ag-`, `pr-`, `cl-`) | `crmagenda.Mapper` | teste "nenhum id do CRM no relatório nem nos logs" |
| Carga completa, incremental, reconciliação em 2 checagens, verificação | `kas-scheduling-mirror` | `scripts/validate-agenda-mirror.sh` + 17 testes |
| Comparação KAS × CRM com histórico | `-mode compare`, `-mode history` | 6 testes (Fase 3A) |
| Trava de escrita: tenant espelhado é só leitura | `scheduling.ErrReadOnly` | testes da Fase 4A |
| **Novo: checagem de índices sem ler documentos** | `-mode plan`, `scripts/preflight-agenda-import.sh` | `TestPlansFlagMissingIndexes` |

## 2. Achado desta fase: índices

O `explain` (só plano, sem documentos) das consultas do espelho mostra o que cada uma precisa:

| Consulta | Índice necessário | No modelo do CRM (`models/Appointment.js`, lido) |
|---|---|---|
| Carga completa por `_id` | `_id` (padrão) | existe |
| Incremental por `updatedAt` | `{ updatedAt: 1, _id: 1 }` | **não declarado**: hoje seria leitura da coleção inteira a cada rodada |
| Janela por data (reconciliação/comparação) | `{ date: 1 }` ou `{ date: -1 }` | declarado `{ date: -1 }` |
| Profissionais | nenhum (coleção pequena) | — |

Correção feita no código: a leitura por janela ordenava por `_id`, o que fazia o MongoDB percorrer o índice `_id`
inteiro mesmo com índice de data. Agora a consulta vai sem ordenação e o Go ordena (achado pelo próprio `-mode plan`).

O modelo declara índices, mas o que existe de fato no Atlas só se sabe com `listIndexes` ou `-mode plan` lá (pendente de
autorização). Criar o índice `{ updatedAt: 1, _id: 1 }` no Atlas é **mudança em produção** (build em segundo plano,
espaço extra): precisa de OK explícito. Alternativa sem índice novo: só carga completa + reconciliação por janela de data
(sem incremental), com custo maior por rodada.

## 3. Checklist de segurança do Atlas (para quando houver autorização)

- [ ] Usuário **novo e dedicado** (ex.: `kas_agenda_leitura`), nunca `claude_ajustes` nem `consultaNovRead`.
- [ ] Papel customizado só com `find` em `appointments` e `doctors` do banco da clínica. Sem `listCollections` em outros bancos, sem escrita.
- [ ] Lista de IP com só o endereço de onde a importação roda, removida ao final.
- [ ] TLS obrigatório (padrão do Atlas); leitura em secundário (`secondaryPreferred`, já configurado no CLI).
- [ ] Senha gerada e guardada só no cofre/variável do ambiente; nunca no git, chat ou log.
- [ ] Janela de execução combinada fora do horário de pico da clínica.
- [ ] Revogar o usuário e a regra de IP ao terminar; registrar data e responsável.
- [ ] Antes de ler qualquer documento: rodar `-mode plan` e anexar o resultado.

## 4. Chave HMAC (`KAS_MIRROR_KEY`)

- 32 bytes ou mais, gerada uma vez por tenant, guardada fora do git.
- Quem tem a chave consegue ligar `cl-…` ao paciente no CRM; sem ela, o espelho não identifica ninguém.
- Trocar a chave muda todos os ids: exige recarga completa do espelho (o rollback abaixo serve).
- Apagar a chave torna o espelho anônimo de forma irreversível (útil no fim do piloto, se for o caso).

## 5. Passo a passo previsto (não executado)

1. Autorização escrita do Kadu para a opção escolhida (A: ler o Atlas com usuário dedicado; B: copiar os dados para a réplica local `fono_espelho` e importar dali).
2. `scripts/preflight-agenda-import.sh <banco>` → precisa terminar com "pré-voo OK".
3. `-mode full` com `-migrate` num PostgreSQL de teste; `-mode verify` na janela de 30 dias.
4. `-mode sync-availability` e `-mode compare` de 4 semanas; ler o relatório `-format md`.
5. Critério para seguir: 0 `import_error` e toda `real_divergence` explicada; regras clínicas listadas com motivo.
6. Só depois: incremental agendado (com o índice) e comparação diária com histórico.

Para B, o CLI já funciona (loopback). Para A, o CLI **recusa de propósito** URI `+srv` e host remoto; abrir isso é uma
mudança de código pequena, com flag explícita, que só deve ser feita junto com a autorização.

## 6. Rollback

| Situação | Ação | Efeito |
|---|---|---|
| Importação parcial ou errada | `DELETE` das linhas do tenant em `sched_mirror_items`, `sched_mirror_missing`, `sched_mirror_cursor`, `sched_mirror_runs`, `sched_mirror_comparisons` e dos `sched_bookings` com chave `mirror:%` | volta ao estado antes da importação; o CRM não é tocado |
| Desistir do espelho | `scheduling.Rollback()` (migrações `.down` 0003 → 0002 → 0001) | remove as tabelas da Agenda |
| Suspeita de vazamento | destruir a chave HMAC | refs viram anônimas para sempre |

O CRM só é lido: nenhum passo de rollback precisa mexer nele.

## 7. Decisão pendente: coleção `Session`

A ocupação que o CRM mostra usa também a coleção `Session` (sessões de pacote), que o espelho **não** lê hoje. Opções:

| Opção | Prós | Contras |
|---|---|---|
| A. Ignorar `Session` (hoje) | menos dados lidos | comparação marca divergência onde só há `Session` |
| B. Ler `Session` com projeção mínima (profissional, data, hora, status) | ocupação igual à do CRM | mais uma coleção autorizada; mapear status de sessão |
| C. Esperar a migração de pacotes para a KAS | sem leitura extra | comparação incompleta até lá |

Recomendação: **B**, com a mesma regra de projeção e HMAC, depois de 28/11.

## 8. O que falta para declarar a fase concluída

- Autorização (A ou B) e execução do passo a passo com dados reais.
- `-mode plan` no ambiente real.
- Decisão sobre o índice `{ updatedAt: 1, _id: 1 }` e sobre a coleção `Session`.
