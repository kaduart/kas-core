# Execução autônoma noturna — Agenda (21/11/2026)

Branch: `kas-core/agenda-fase2` (criada de `a170fbc`, fim da fase 1).
Autorização: mensagem do Kadu de 21/11 00h14 BRT e ampliação de 00h16 BRT.
Limites: sem CRM, sem Atlas/produção, sem dados reais, sem PR/merge/deploy,
sem Financeiro, sem recursos pagos. Tudo com dados fictícios, em bancos locais
descartáveis (Postgres 16 e MongoDB em Docker, 127.0.0.1).

Classificação usada: **Implementada e validada** · **Implementada, aguardando
validação** · **Planejada, não implementada** · **Bloqueada**.

## Marco 1 — importador `kas-scheduling-mirror` (fase 2, prioridade 1)

O que foi feito:
- Migração `0002_mirror.sql` (+ rollback `0002_mirror.down.sql`): tabelas
  `sched_mirror_items`, `sched_mirror_cursor`, `sched_mirror_missing`,
  `sched_mirror_runs`. Nenhuma coluna com dado pessoal. Agendamentos espelhados
  ficam em `sched_bookings` com `exclusive=false` (o CRM pode ter sobreposições:
  são medidas, não recusadas).
- `internal/scheduling/mirror.go`: gravação em lote idempotente com o cursor na
  mesma transação, atualização fora de ordem ignorada, nunca sobrescreve um
  agendamento nativo do KAS, exclusão física só na 2ª verificação (com intervalo
  mínimo), trava por (tenant, origem), registro de execuções só com contagens,
  resumo por profissional e dia para comparar.
- `internal/legacy/crmagenda`: único lugar que conhece os campos do CRM. Lê só
  9 campos (projeção fechada), troca todo id por HMAC-SHA256 com chave do KAS,
  mapeia status pela regra de slot do CRM (só cancelado e concluído liberam),
  dia do CRM (meio-dia UTC) + hora no fuso da clínica.
- `cmd/kas-scheduling-mirror`: modos `full`, `incremental`, `reconcile`,
  `verify`. Segredos só por variável de ambiente. Recusa Mongo/Postgres fora de
  loopback, `mongodb+srv` e banco com "prod" no nome.
- `scripts/validate-agenda-mirror.sh`: validação ponta a ponta com 200
  agendamentos fictícios.

Testes (todos contra Postgres e MongoDB reais, sem mock):
17 de integração em `internal/legacy/crmagenda/importer_test.go` (carga
idempotente, linhas inválidas contadas, incremental, fora de ordem, exclusão em
2 verificações, intervalo mínimo, apagar e recriar, exclusão lógica, reinício
após queda na carga completa e no incremental, queda do Postgres, queda do
Mongo, importadores concorrentes, isolamento de tenant, sobreposições da
origem, divergência detectada, logs sem ids), 5 unitários de mapeamento, 1 de
configuração do comando, rollback com dados espelhados.

Resultado: `go test ./...` 28/28 pacotes ok; script ponta a ponta OK
(full 200, rerun 0 alterações, incremental 5, exclusão após 2 verificações,
verify igual).

Classificação: **Implementada e validada** (com dados fictícios). Cópia de dados
reais para `fono_espelho`: **Bloqueada** (precisa de autorização separada).

CI do marco 1: commit `60feada`, run 44861034563, 6/6 verde.

## Marco 2 — equivalência com as regras do CRM (fase 2, prioridade 2)

- Lidos (sem executar nem alterar o CRM) os 3 geradores de recorrência: pacote
  (formulário do front + ajuste de feriados do back), plano de convênio e
  contrato de liminar; mais o cálculo de horários livres, feriados parciais e
  status que bloqueiam.
- 32 cenários fictícios em `internal/legacy/crmagenda/equivalence_test.go`:
  20 iguais, 4 iguais depois que o módulo saúde aplica a regra clínica,
  6 defeitos do CRM (o KAS acerta de propósito), 2 conceitos que só o KAS tem
  (intervalo e férias por período). Tabela completa e separação genérico × saúde
  em `docs/agenda/EQUIVALENCIA_RECORRENCIA.md`.
- Ajustes no motor genérico: quinzenal ancorado na semana de início,
  `time_by_weekday`, `skip_dates`, `count` + `until` juntos.

Classificação: **Implementada e validada** (contra modelos transcritos por
leitura). A comparação com dados reais do CRM é a fase 3A/3B: **Bloqueada** até
autorização de cópia.

CI do marco 2: commit `7044b80`, run 44861541754.

## Marco 3 — benchmark (fase 2, prioridade 3) e robustez (prioridade 4)

- `internal/httpapi/scheduling_bench_test.go` (só roda com `KAS_BENCH=1`) e
  `internal/scheduling/compute_bench_test.go`. Resultados e limitações em
  `docs/agenda/BENCHMARK_FASE2.md`; dados brutos em JSON.
- Resumo: 1 a 25 simultâneos sem erro com o limite padrão (32); 50 e 100 exigem
  subir `-max-concurrent` (com 128: 0 erros, p99 30 ms em 50 e 67 ms em 100). Teto
  ~1.900 leituras/s nesta máquina; gargalo pool de 8 conexões + CPU. Gravação no
  mesmo profissional em fila (~700/s), espalhada ~1.300/s. Postgres congelado 3 s:
  0 erros.
- Sem comparação Node × Go: não há medição equivalente do CRM (declarado).
- Robustez nova: log de execuções confere com o que aconteceu (execução que caiu
  não grava linha), nenhuma consulta reescreve `tenant_id`, tenant inválido
  recusado, mesmo id em dois tenants fica separado.

Classificação: benchmark **Implementada e validada** (local); robustez
**Implementada e validada** (Postgres e Mongo reais).

## Marco 4 — fase 3A: motor de comparação

Detalhes em `docs/agenda/FASE3A_COMPARACAO.md`. Compara por profissional e dia
(agendamentos, horários livres, status, conflitos, padrões semanais), classifica
em igual / erro de importação / regra clínica / divergência real e guarda o
histórico. 6 testes de integração + script ponta a ponta.

Classificação: **Implementada e validada com fixtures**. Com dados reais:
**Bloqueada** (depende da cópia autorizada).

CI do marco 4: commit `cb7eab9`, run 44862655066 (verde).

## Marco 5 — fase 4A: escrita na Agenda (tenant fictício)

Detalhes em `docs/agenda/FASE4A_ESCRITA.md`. Rotas `POST /internal/v1/scheduling/bookings`,
`/bookings/{id}/cancel` e `/bookings/{id}/reschedule`, com capability `scheduling:write`,
papéis admin, gestor e atendimento, e ator `user:<id>` ou `service:<id>` na auditoria.
Tenant espelhado de um sistema externo (ex.: Fono Inova) recusa escrita com
`agenda_read_only`; fora do expediente, `outside_opening_hours`.

Concorrência medida (24 pedidos paralelos por cenário, Postgres local):
mesmo horário 1 criado + 23 `slot_taken`; mesmo `Idempotency-Key` 1 criado + 23
replays com o mesmo id; remarcar da mesma versão 1 sucesso + 23 `version_conflict`;
soma das versões = linhas de auditoria.

Classificação: **Implementada e validada** (local, dados fictícios). CI: commit
`616a01c`, run 44863428798 (verde).

## Marco 6 — fase 4B: tela Agenda no painel

Detalhes em `docs/agenda/FASE4B_PAINEL.md`. Catálogo (`GET /internal/v1/scheduling/catalog`),
seed fictício (`cmd/kas-scheduling-seed`, só clinica-aurora, odonto-sorriso e
escola-demo, só loopback), BFF Next.js com checagem de origem, tela dia e semana,
painel lateral para criar, remarcar e cancelar, modo só leitura.

Validado com a API real local (sem mocks): Playwright criou, validou erro, remarcou,
cancelou, mostrou a semana, celular 390 px sem rolagem lateral, usuário de leitura
sem botões e com POST recusado (403 `role_forbidden`). Seed: 30 agendamentos na 1ª
vez, 0 na 2ª. tsc, eslint, vitest (45) e impeccable detect limpos.

Classificação: tela e API **Implementada e validada** (local). O compose do
`deploy/painel-local` com a Agenda: **Implementada, aguardando validação** (não
subido com Docker Compose nesta noite; testado com os binários). CI: commit
`c45c26e`, run 44864560962 (verde).

## Marco 7 — fase 3B: importação real protegida (preparação)

Detalhes em `docs/agenda/FASE3B_IMPORTACAO_REAL.md`. Novo `-mode plan` no
`kas-scheduling-mirror`: pede ao MongoDB só o plano de cada consulta (explain
queryPlanner), sem ler documentos, e acusa varredura de coleção ou de índice sem
limites. `scripts/preflight-agenda-import.sh` confere URI local, chave e nome do
banco antes do plano.

Bug achado e corrigido: a leitura por janela ordenava por `_id` e o MongoDB
percorria o índice `_id` inteiro em vez do índice de data; agora ordena em Go.
Achado no CRM (só leitura do código): `Appointment` não tem índice
`{updatedAt:1,_id:1}`, então a carga incremental varreria a coleção. Criar esse
índice é mudança no CRM/Atlas: **Bloqueada**.

Classificação: pré-voo **Implementada e validada** (Mongo local com e sem
índices). Importação real: **Bloqueada** (Atlas, cópia de dados reais e chave
HMAC exigem autorização). CI: commit `30cac52`, run 44865249059.

## Frente paralela — Jornada do Terapeuta (auditoria)

Relatório técnico em Claude Doc ("KAS Health — Jornada do Terapeuta"). Só leitura
do código do CRM; nada implementado.

## Matriz final por fase

| Fase | Estado | Evidência | Próxima autorização |
|---|---|---|---|
| 2 Importador + equivalência + benchmark | Implementada e validada (fictício) | Marcos 1–3, CI verde | Cópia real para o espelho local |
| 3A Comparação | Implementada e validada com fixtures | Marco 4, CI verde | Mesma cópia real |
| 4A Escrita | Implementada e validada (local) | Marco 5, CI verde | Nenhuma para seguir local |
| 4B Painel | Implementada e validada (local); compose aguardando validação | Marco 6, CI verde, Playwright | Kadu testar no KADU-PC |
| 3B Importação real | Pré-voo validado; importação Bloqueada | Marco 7 | Atlas, índice no CRM, chave HMAC |
