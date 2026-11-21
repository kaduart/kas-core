# Benchmark da Agenda — fase 2 (21/11/2026, madrugada)

Todos os números abaixo foram medidos nesta máquina, nesta data, com os comandos
indicados. Nada foi estimado. **Não existe medição equivalente do CRM (Node +
MongoDB) na mesma máquina e com os mesmos dados**, então este documento não
compara linguagens nem bancos e não atribui ao Go nenhum ganho. Uma comparação
justa exigiria rodar a rota de horários do CRM sobre a mesma massa, no mesmo
hardware, com o mesmo cliente de carga (fica como pendência).

## Ambiente

| Item | Valor |
|---|---|
| CPU | Intel Xeon @ 2.10GHz, 4 vCPUs (contêiner na nuvem, compartilhado) |
| RAM | 16 GB |
| Kernel | Linux 6.18 |
| Go | 1.25.0 |
| PostgreSQL | 16.16 em Docker 29.10 (127.0.0.1), `max_connections=100`, `shared_buffers=128MB` |
| Pool do KAS | 8 conexões (padrão de produção do `pgdb.Open`) |
| Cliente e servidor HTTP | mesmo processo Go, TCP real em loopback (`httptest.NewServer`) |
| Massa (fictícia) | 1 tenant (`clinica-aurora`), 20 profissionais, 30 dias úteis, 6.000 agendamentos (10 de 15 horários ocupados por dia) |

Limitações do ambiente: o cliente de carga divide as 4 vCPUs com o servidor e o
Postgres; a CPU do processo Go soma cliente + servidor. Máquina de nuvem
compartilhada: repetições variam alguns %. Rodado 3 vezes (as 2 primeiras
tinham defeitos no próprio teste de escrita, corrigidos); os números são da 3ª.

## Comandos

```
KAS_TEST_DATABASE_URL=postgres://kas:...@127.0.0.1:55432/kas_core_test?sslmode=disable \
KAS_BENCH=1 KAS_BENCH_PG_CONTAINER=pg-sched KAS_BENCH_OUT=resultado.json \
go test ./internal/httpapi -run TestSchedulingLoad -count=1 -timeout 30m -v

go test ./internal/scheduling -run '^$' -bench . -benchmem -count 3
```

Dados brutos: `docs/agenda/benchmark-fase2-2026-11-21.json`.

## 1. Cálculo puro (sem banco, sem HTTP)

| Operação | Tempo por operação | Memória |
|---|---|---|
| Horários livres, 1 profissional, 1 semana, 50 ocupados | ~63–66 µs | 12 KB, 37 alocações |
| Horários livres, 1 mês, passo 10 min, intervalo 10 min, 310 ocupados | ~3,1–3,7 ms | 105 KB |
| Conflitos de 20 candidatos contra 310 agendamentos | ~122–138 µs | 50 KB |
| Série de 150 sessões (seg/qua/sex, 1 ano, 7 feriados) | ~41–46 µs | 22 KB |

Leitura: o cálculo de um mês com passo de 10 min é o ponto mais caro (cresce com
horários × agendamentos). Otimização possível (filtrar agendamentos por dia) fica
registrada; não foi feita porque o caso real da tela é de 1 a 7 dias.

## 2. Requisição completa (HTTP + autenticação + Postgres)

`availability_5_days` = GET de horários livres de 1 profissional por 5 dias.
`conflicts_check` = POST de checagem de 1 candidato. Cada nível roda
max(400, 40 × simultâneos) pedidos.

### Com o limite padrão do servidor (`-max-concurrent 32`)

| Simultâneos | Cenário | OK | Recusados 503 | OK/s | p50 | p95 | p99 |
|---|---|---|---|---|---|---|---|
| 1 | horários | 400 | 0 | 559 | 1,7 ms | 2,7 ms | 3,4 ms |
| 10 | horários | 400 | 0 | 1.753 | 5,2 ms | 9,2 ms | 12,7 ms |
| 25 | horários | 1.000 | 0 | 1.754 | 13,9 ms | 17,1 ms | 21,1 ms |
| 50 | horários | 64 | 1.936 | — | — | — | — |
| 100 | horários | 98 | 3.902 | — | — | — | — |
| 1 | conflitos | 400 | 0 | 542 | 1,8 ms | 2,5 ms | 3,2 ms |
| 10 | conflitos | 400 | 0 | 1.674 | 5,8 ms | 8,1 ms | 8,9 ms |
| 25 | conflitos | 1.000 | 0 | 1.875 | 13,1 ms | 16,4 ms | 18,1 ms |
| 50 | conflitos | 93 | 1.907 | — | — | — | — |
| 100 | conflitos | 122 | 3.878 | — | — | — | — |

Acima de 32 pedidos ao mesmo tempo o servidor recusa na hora com 503
`capacity_exceeded` (proteção já existente). Os clientes do teste repetem sem
espera, por isso a maioria dos pedidos vira 503: isso mede a proteção, não a
capacidade. Para 50 e 100 simultâneos reais, o limite precisa subir (próxima tabela).

### Com o limite em 128

| Simultâneos | Cenário | OK | Erros | OK/s | p50 | p95 | p99 | máx |
|---|---|---|---|---|---|---|---|---|
| 1 | horários | 400 | 0 | 631 | 1,5 ms | 2,3 ms | 2,7 ms | 3,1 ms |
| 10 | horários | 400 | 0 | 1.764 | 5,5 ms | 7,5 ms | 8,2 ms | 9,4 ms |
| 25 | horários | 1.000 | 0 | 2.001 | 12,2 ms | 15,5 ms | 18,0 ms | 20,1 ms |
| 50 | horários | 2.000 | 0 | 1.937 | 25,6 ms | 29,2 ms | 30,3 ms | 33,2 ms |
| 100 | horários | 4.000 | 0 | 1.894 | 50,9 ms | 64,0 ms | 66,7 ms | 72,9 ms |
| 1 | conflitos | 400 | 0 | 432 | 1,8 ms | 2,8 ms | 3,2 ms | 182 ms* |
| 10 | conflitos | 400 | 0 | 1.691 | 5,8 ms | 8,2 ms | 9,5 ms | 11,2 ms |
| 25 | conflitos | 1.000 | 0 | 1.611 | 15,1 ms | 20,5 ms | 23,0 ms | 25,2 ms |
| 50 | conflitos | 2.000 | 0 | 1.789 | 28,1 ms | 33,1 ms | 34,7 ms | 36,7 ms |
| 100 | conflitos | 4.000 | 0 | 1.733 | 55,9 ms | 72,2 ms | 75,5 ms | 84,0 ms |

\* um pedido isolado de 182 ms (pausa da máquina compartilhada; p99 3,2 ms).

Leitura: a vazão para de crescer por volta de ~1.800–2.000 pedidos/s a partir de
25 simultâneos; daí em diante a latência cresce linearmente (fila). O gargalo é o
pool de 8 conexões ao Postgres somado às 4 vCPUs divididas com o cliente: com 100
simultâneos, cada pedido espera ~12 outros por conexão. Isso é efeito do banco e
do pool, não da linguagem.

CPU e memória (nível de 100 simultâneos, limite 128): processo Go (cliente +
servidor) 3,2 s de CPU em 2,1 s de parede, heap em uso ≤ 13 MB, memória total do
runtime ≤ 30 MB; Postgres 1,5 s (horários) a 3,9 s (conflitos) de CPU, memória do
contêiner ~306 MB estável em todos os níveis.

## 3. Gravação e espera de trava

Gravação direta no store (ainda não há rota HTTP de escrita: fase 4A). Cada
pedido reserva um horário livre diferente. "1 profissional" = todos esperam a
trava do mesmo recurso. "20 profissionais" = carga espalhada.

| Simultâneos | 1 profissional p50 / p99 / máx | OK/s | 20 profissionais p50 / p99 / máx | OK/s |
|---|---|---|---|---|
| 1 | 2,5 / 4,3 / 4,7 ms | 379 | 2,5 / 4,1 / 4,1 ms | 391 |
| 10 | 16,2 / 23,4 / 24,8 ms | 578 | 6,2 / 8,3 / 8,8 ms | 1.535 |
| 25 | 32,6 / 38,3 / 39,7 ms | 725 | 16,2 / 19,7 / 19,9 ms | 1.460 |
| 50 | 66,3 / 72,8 / 74,1 ms | 698 | 34,6 / 44,4 / 45,2 ms | 1.331 |
| 100 | 73,8 / 140,1 / 141,1 ms | 699 | 77,6 / 84,2 / 85,5 ms | 1.265 |

Leitura: num mesmo profissional as gravações são em fila (trava por recurso, de
propósito) e o teto fica em ~700 gravações/s; espalhado chega a ~1.300–1.500/s.
A espera da trava é a diferença entre as duas colunas (ex.: 50 simultâneos, ~32 ms
a mais no p50). Nenhum erro, nenhuma reserva duplicada. Na fase 1, com 50 pedidos
disputando o MESMO horário, 1 foi aceito e 49 recusados com 409 em 20/20 rodadas.

## 4. Comportamento sob falha

Postgres congelado (`docker pause`) por 3 s no meio de uma rodada de 50
simultâneos (6.000 pedidos de horários, limite 128):

- 0 erros, 0 recusas: os pedidos em voo esperaram (máx 3.079 ms) e terminaram.
- O primeiro pedido OK depois de descongelar chegou em menos de 1 ms.
- p50 da rodada 26,8 ms (igual à rodada sem falha); vazão 948/s, menor por causa
  dos 3 s parados.

Não testado ainda (registrado): queda mais longa que o tempo-limite de 15 s do
banco, perda de conexão TCP (em vez de congelamento) e reinício do Postgres.
Os testes do importador cobrem conexão fechada no meio da carga (recupera ao
rodar de novo, sem duplicar).

## 5. O que este benchmark NÃO prova

- Nada sobre o CRM atual: não houve medição do CRM.
- Nada sobre produção: hardware, rede e banco serão outros.
- Nada sobre o painel: só API e banco.
