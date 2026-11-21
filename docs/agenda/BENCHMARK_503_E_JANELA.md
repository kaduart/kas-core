# Agenda: causa dos 503, matriz de pool e extração por janela (21/11/2026)

Tudo local, com dados fictícios: PostgreSQL e MongoDB em Docker, no loopback. Nada foi feito no Atlas, no CRM nem em produção.
A máquina tem 4 vCPU, e cliente, servidor e banco rodam nela ao mesmo tempo. Os números servem para comparar configurações entre si, não para prever a produção.

## 1. A causa exata dos HTTP 503

O 503 vem do limitador do próprio servidor KAS, em `internal/httpapi/handler.go` (`serve()`):

```go
if h.active.Add(1) > h.limit { // 503 capacity_exceeded na hora
```

- O limite padrão é `-max-concurrent=32`.
- Não existe fila: o pedido número 33 é recusado na hora, sem `Retry-After`.
- O pool do PostgreSQL ficava fixo em no máximo 8 conexões. Com mais de 8 pedidos simultâneos, os pedidos esperam conexão segurando a vaga do limitador, e as vagas acabam mais rápido.

O benchmark da fase 2 também exagerou o número. Ele tinha um orçamento fixo de pedidos: o pedido recusado volta em microssegundos e gasta o orçamento. Por isso apareceu "97% recusados".

**Regra confirmada pela matriz:** só houve 503 quando a concorrência passou do limite do servidor (50 ou 100 clientes com limite 32, e 100 clientes com limite 64). Com o limite maior ou igual à concorrência, foram **0 recusas em todas as 48 combinações**, com qualquer pool.

## 2. A matriz

São 4 pools (4, 8, 16, 32) × 3 limites (32, 64, 128) × 4 concorrências (10, 25, 50, 100) × 2 tipos de cliente, com 3 segundos por combinação.

Os dois tipos de cliente:

- **Imediato:** diante de um 503, manda o próximo pedido na hora. É o pior caso.
- **Backoff:** espera 50 ms × tentativa e repete até 5 vezes.

Os dados completos estão em `benchmark-matriz-pool-2026-11-21.json`. O teste é o `TestSchedulingPoolMatrix`, com `KAS_BENCH=1 KAS_BENCH_MATRIX=1`.

| Pool | Limite | Clientes | Cliente | ok/s | p50 | p99 | Recusa | Falha p/ usuário |
|---|---|---|---|---|---|---|---|---|
| 8 (atual) | 32 (atual) | 50 | imediato | 554 | 51 ms | 296 ms | 93% | 93% |
| 8 (atual) | 32 (atual) | 50 | backoff | 871 | 34 ms | 362 ms | 18% | 0,8% |
| 8 | 128 | 50 | imediato | 1.626 | 30 ms | 47 ms | 0% | 0% |
| 16 | 64 | 50 | imediato | 2.104 | 23 ms | 36 ms | 0% | 0% |
| 16 | 128 | 100 | imediato | 1.981 | 50 ms | 66 ms | 0% | 0% |
| 32 | 128 | 100 | imediato | 1.626 | 59 ms | 101 ms | 0% | 0% |
| 4 | 128 | 100 | imediato | 1.386 | 71 ms | 94 ms | 0% | 0% |

O que os números mostram:

- **Quem decide a recusa é o limite, não o pool.**
- **O pool decide a vazão.** Com 50 clientes, o pool de 16 rendeu cerca de 30% mais que o de 8. O pool de 32 não rendeu mais que o de 16 nesta máquina, porque a CPU é dividida com o banco.
- **O backoff no cliente reduz a falha para o usuário**, de 93% para 0,1–5%, mas aumenta o p99 (de 180 a 530 ms). Ele ajuda, mas não substitui um limite correto.
- **Memória:**
  - heap do Go entre 5 e 22 MB;
  - PostgreSQL entre 210 e 270 MB, crescendo com o pool (213 MB com pool 4, 267 MB com pool 32).

**Configuração sugerida para a produção:** pool 16 e `-max-concurrent=128`.

- O limite fica bem acima da concorrência esperada de uma clínica.
- O pool de 16 cabe no limite de conexões de um PostgreSQL pequeno.

Mudança no código para permitir isso: `internal/pgdb` agora respeita um `pool_max_conns` explícito na URL, entre 2 e 32. Sem o parâmetro, continua no máximo 8, como antes.

## 3. A extração por janela de datas (modo `window`)

Comando:

```
kas-scheduling-mirror -mode window -from AAAA-MM-DD -to AAAA-MM-DD
```

Cada rodada:

1. Lê os agendamentos da janela pelo índice `{date:-1}`, que o CRM já tem.
2. Grava no espelho em lotes.
3. Trata as exclusões com duas verificações, como o `reconcile`.

A rodada não usa `updatedAt` e não move o cursor do incremental.

### Testes com dados fictícios (`window_test.go`)

- Uma mudança de status dentro da janela entra.
- Um agendamento novo dentro da janela entra.
- Um agendamento fora da janela não entra.
- Rodar de novo sem mudanças não altera nada.
- Uma exclusão só vale na segunda verificação.
- Janela invertida ou maior que 400 dias é recusada.

### Escala

O teste usa 102.200 agendamentos fictícios, com só os índices que o CRM declara. Ele está em `benchmark-janela-2026-11-21.json` e roda com `KAS_BENCH=1 KAS_BENCH_WINDOW=1`.

| Consulta no Mongo | Plano | Documentos examinados | Devolvidos | Tempo |
|---|---|---|---|---|
| Janela (7 dias atrás, 120 à frente) | IXSCAN `date_-1` | 18.060 | 18.060 | 36 ms |
| Incremental por `updatedAt`, sem índice | **COLLSCAN** | **102.200** | 140 | 56 ms |
| Incremental por `updatedAt`, com índice (opção C) | IXSCAN | 140 | 140 | 2 ms |

- **No Mongo, a janela só examina o que devolve.** O incremental sem índice varre a coleção inteira a cada rodada.
- **O custo está no lado do KAS.** No PostgreSQL, a primeira rodada levou 102 s (18 mil linhas novas), e uma rodada sem mudanças levou 30 s. A gravação confere linha por linha.

Ajustes antes de agendar a janela:

1. Rodar a janela curta com frequência (por exemplo, 7 dias atrás e 30 à frente, a cada 10 minutos) e a janela longa, de 120 dias, uma vez por noite.
2. Melhoria no KAS (ainda não feita): pular, antes de gravar, as linhas cujo `updatedAt` não mudou. Isso deve levar a rodada sem mudanças de 30 s para poucos segundos. Não depende do Atlas.

## 4. Atualização de 21/11, 10h30: gravação mais rápida e teste de saturação

### Gravação no espelho (`MirrorApply`)

Antes, cada linha fazia três consultas. Agora o lote inteiro faz uma consulta só para saber o dono e a versão já gravada de cada linha. Fora da carga completa, a linha com a mesma versão (`updatedAt`) é pulada. A mesma janela de 102 mil fictícios (`benchmark-janela-otimizada-2026-11-21.json`):

| | Antes | Depois |
|---|---|---|
| Primeira rodada (18.060 linhas novas) | 102 s | 38 s |
| Rodada sem mudanças | 30 s | **1,5 s** |

Todos os testes do espelho continuam passando, inclusive os de exclusão, versão antiga e dono diferente.

Limite conhecido: uma linha alterada na origem sem mudar o `updatedAt` não é regravada fora da carga completa. O Mongoose do CRM atualiza o `updatedAt` em toda gravação, mas um script que grave direto no banco pode não atualizar. A carga completa (`-mode full`) regrava tudo.

### Saturação e estabilidade com pool 16 e limite 128 (`benchmark-saturacao-2026-11-21.json`)

O teste roda em janelas de 10 s, em 4 fases, por 4 min 50 s, só na leitura de horários livres. O teste é o `TestSchedulingSoak`, com `KAS_BENCH=1 KAS_BENCH_SOAK=1`.

| Fase | Clientes | ok/s | p99 | Recusas | Outros erros |
|---|---|---|---|---|---|
| Estável (12 janelas) | 50 | 1.480–1.832 | 43–52 ms | 0 | 0 |
| No limite (6) | 128 | 1.634–1.863 | 89–107 ms | 0 | 0 |
| Acima do limite (3) | 200 | 939–1.027 | 171–185 ms | ~93% (esperado) | 0 |
| Recuperação (6) | 50 | 1.758–2.020 | 39–46 ms | 0 | 0 |

- **Nenhuma deriva** em 30 janelas. O p99 da fase estável não cresce com o tempo. Depois da sobrecarga, a latência volta ao nível normal.
- **Memória:** o heap do Go ficou entre 10 e 26 MB. Subiu durante a sobrecarga e voltou a ficar entre 15 e 20 MB.
- **Postgres:** 18 conexões o tempo todo (o pool de 16 mais as do teste), entre 241 e 242 MB, sem vazamento de conexão.
- **Goroutines:** o número sobe com a quantidade de clientes (155, 389 e 605), porque o cliente HTTP do teste mantém as conexões abertas. Elas não crescem dentro de uma mesma fase.
- **Fora do teste:** escrita concorrente (reservas) e rede real entre o KAS e o banco. Aqui tudo roda na mesma máquina, com 4 vCPU.
