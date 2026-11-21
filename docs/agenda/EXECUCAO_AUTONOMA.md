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
