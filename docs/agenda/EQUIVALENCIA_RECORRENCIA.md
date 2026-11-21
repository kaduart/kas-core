# Equivalência com as regras de agenda do CRM (fase 2)

Gerado por `KAS_EQUIV_REPORT=docs/agenda/EQUIVALENCIA_RECORRENCIA_TABELA.md go test ./internal/legacy/crmagenda -run Equivalence`.
Teste: `internal/legacy/crmagenda/equivalence_test.go`. Modelos do CRM transcritos **por leitura** em
`internal/legacy/crmagenda/crmref_test.go`. O CRM não foi executado nem alterado.

## Os 3 geradores de recorrência do CRM (lidos)

| Gerador | Onde | Regra de datas |
|---|---|---|
| Pacote (particular) | front `TherapyPackageFormModal.tsx` ~660-770 + back `packageController.v2.js` `adjustScheduleSkipHolidays` ~108-170 | semanas a partir da segunda da semana de início; semanal ou quinzenal; sábado/domingo descartados; feriado empurrado 7/14 dias até uma DATA livre |
| Plano de convênio | back `services/schedule/generateInsurancePlanSessions.js` ~205-290 | início = maior entre início do plano e hoje; semanas a partir do domingo; corta pelo saldo da guia; pula feriado, cancelamento histórico e avaliação; folga de só 1 semana |
| Contrato de liminar | back `services/schedule/generateLiminarSessions.js` ~117-200 | por horário, N semanas sem feriado/cancelado; para na validade do contrato; não recria o que existe; conflito só por hora exata |

Também lidos: `middleware/conflictDetection.js` (`calculateAvailableSlots`, `checkSlotOverlap`),
`config/feriadosBR-dynamic.js` (`isTimeBlockedByHoliday`), `constants/appointmentStatus.js`.

## Regra genérica (KAS core) × regra de saúde (módulo)

| Fica no motor genérico | Fica no módulo saúde (Fono Inova) |
|---|---|
| semanal/quinzenal ancorado na semana de início | saldo da guia de convênio (autorizadas − usadas − reservadas) |
| dias e horário por dia (`time_by_weekday`) | crédito e limites por especialidade da liminar |
| feriado: pular e estender (`holiday_policy=skip`) | quais datas pular: cancelamento histórico, avaliação (`skip_dates`) |
| `count`, `until` ou os dois (o que vier primeiro) | início mínimo (plano × hoje × pedido) |
| conflito por sobreposição parcial (recurso e cliente) | valor da sessão, faturamento, origem do agendamento |
| exceções de período (férias, meio feriado) | status do profissional (ativo, férias, afastado) |
| intervalo após a sessão, por serviço | mapeamento dos status do CRM (no importador) |

## Ajustes feitos no KAS nesta fase (por causa da equivalência)

1. Quinzenal com vários dias passou a ancorar na semana da data de início (os dias ficam na mesma quinzena, como no CRM). Antes, a segunda caía na semana seguinte à quarta de início (cenário R05).
2. `time_by_weekday`: horário diferente por dia na mesma série (R02, R03, I15).
3. `skip_dates`: o módulo informa datas a pular; o motor só pula e estende (I18, L22).
4. `count` e `until` juntos (L23).

## Defeitos do CRM encontrados (só registro; o CRM está congelado)

- **R07, R10**: data empurrada por feriado pode cair em outro feriado (ex.: 25/12 vai para 01/01).
- **R13**: com 2 sessões no mesmo dia, o ajuste compara só a data e separa os horários em semanas diferentes.
- **I17**: plano de convênio com 2 ou mais datas puladas gera menos sessões que o saldo (folga fixa de 1 semana).
- **I20**: datas do convênio dependem do fuso do servidor (`toISOString` de meia-noite local); igual ao achado de `computeDateTimes`.
- **L25**: conflito da liminar só por hora exata; 08:20 não choca com 08:00–08:40.

## Resultado

| # | Área | Cenário | Resultado | CRM | KAS | Observação |
|---|---|---|---|---|---|---|
| R01 | recorrência | pacote semanal, 1 dia, 8 sessões | igual | 2027-03-01 08:00, 2027-03-08 08:00, 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00, 2027-04-05 08:00, 2027-04-12 08:00, 2027-04-19 08:00 | 2027-03-01 08:00, 2027-03-08 08:00, 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00, 2027-04-05 08:00, 2027-04-12 08:00, 2027-04-19 08:00 |  |
| R02 | recorrência | pacote semanal, seg 08:00 e qua 14:00, início na segunda | igual | 2027-03-01 08:00, 2027-03-03 14:00, 2027-03-08 08:00, 2027-03-10 14:00, 2027-03-15 08:00, 2027-03-17 14:00, 2027-03-22 08:00 | 2027-03-01 08:00, 2027-03-03 14:00, 2027-03-08 08:00, 2027-03-10 14:00, 2027-03-15 08:00, 2027-03-17 14:00, 2027-03-22 08:00 | horário por dia (time_by_weekday) |
| R03 | recorrência | pacote semanal seg+qua começando numa quarta | igual | 2027-03-03 14:00, 2027-03-08 08:00, 2027-03-10 14:00, 2027-03-15 08:00, 2027-03-17 14:00 | 2027-03-03 14:00, 2027-03-08 08:00, 2027-03-10 14:00, 2027-03-15 08:00, 2027-03-17 14:00 | a segunda da semana 0 fica fora |
| R04 | recorrência | pacote quinzenal, 1 dia, 6 sessões | igual | 2027-03-02 10:00, 2027-03-16 10:00, 2027-03-30 10:00, 2027-04-13 10:00, 2027-04-27 10:00, 2027-05-11 10:00 | 2027-03-02 10:00, 2027-03-16 10:00, 2027-03-30 10:00, 2027-04-13 10:00, 2027-04-27 10:00, 2027-05-11 10:00 |  |
| R05 | recorrência | pacote quinzenal seg+qua começando numa quarta | igual | 2027-03-03 14:00, 2027-03-15 08:00, 2027-03-17 14:00, 2027-03-29 08:00, 2027-03-31 14:00, 2027-04-12 08:00 | 2027-03-03 14:00, 2027-03-15 08:00, 2027-03-17 14:00, 2027-03-29 08:00, 2027-03-31 14:00, 2027-04-12 08:00 | corrigido no KAS nesta fase: antes a segunda caía na semana ímpar; agora os dois dias ficam na mesma quinzena, como no CRM |
| R06 | feriados | pacote semanal com 1 feriado no meio | igual | 2027-03-01 09:00, 2027-03-08 09:00, 2027-03-22 09:00, 2027-03-29 09:00, 2027-04-05 09:00, 2027-04-12 09:00 | 2027-03-01 09:00, 2027-03-08 09:00, 2027-03-22 09:00, 2027-03-29 09:00, 2027-04-05 09:00, 2027-04-12 09:00 | CRM empurra para depois do fim; KAS pula e estende: mesmas datas |
| R07 | feriados | pacote: data empurrada cai em outro feriado | defeito_crm | 2027-03-01 09:00, 2027-03-15 09:00, 2027-03-22 09:00, 2027-03-29 09:00 | 2027-03-01 09:00, 2027-03-15 09:00, 2027-03-22 09:00, 2027-04-05 09:00 | CRM não confere feriado na data nova e marca sessão no feriado; KAS pula também esse |
| R08 | feriados | pacote quinzenal com feriado | igual | 2027-03-02 10:00, 2027-03-30 10:00, 2027-04-13 10:00, 2027-04-27 10:00 | 2027-03-02 10:00, 2027-03-30 10:00, 2027-04-13 10:00, 2027-04-27 10:00 | salto de 14 dias nos dois |
| R09 | virada | pacote semanal atravessando mês e ano | igual | 2026-12-21 15:00, 2026-12-28 15:00, 2027-01-04 15:00, 2027-01-11 15:00, 2027-01-18 15:00 | 2026-12-21 15:00, 2026-12-28 15:00, 2027-01-04 15:00, 2027-01-11 15:00, 2027-01-18 15:00 |  |
| R10 | virada | sextas com Natal e Ano Novo seguidos | defeito_crm | 2026-12-18 16:00, 2027-01-01 16:00, 2027-01-08 16:00, 2027-01-15 16:00 | 2026-12-18 16:00, 2027-01-08 16:00, 2027-01-15 16:00, 2027-01-22 16:00 | 25/12 é empurrado para 01/01, outro feriado; KAS pula os dois |
| R11 | disponibilidade semanal | pacote com sábado escolhido | regra_clinica | 2027-03-01 08:00, 2027-03-08 08:00, 2027-03-15 08:00, 2027-03-22 08:00 | 2027-03-01 08:00, 2027-03-08 08:00, 2027-03-15 08:00, 2027-03-22 08:00 | o formulário do CRM descarta sábado e domingo em silêncio (a clínica não abre); no KAS quem fecha é a regra semanal da clínica: o módulo só manda dias úteis |
| R12 | recorrência | pacote com 2 sessões no mesmo dia, total ímpar | igual | 2027-03-01 08:00, 2027-03-01 09:00, 2027-03-08 08:00, 2027-03-08 09:00, 2027-03-15 08:00 | 2027-03-01 08:00, 2027-03-01 09:00, 2027-03-08 08:00, 2027-03-08 09:00, 2027-03-15 08:00 | KAS: duas séries (08:00 com 3, 09:00 com 2) |
| R13 | feriados | 2 sessões no mesmo dia caindo num feriado | defeito_crm | 2027-03-01 08:00, 2027-03-01 09:00, 2027-03-15 08:00, 2027-03-15 09:00, 2027-03-22 08:00, 2027-03-29 09:00 | 2027-03-01 08:00, 2027-03-01 09:00, 2027-03-15 08:00, 2027-03-15 09:00, 2027-03-22 08:00, 2027-03-22 09:00 | o CRM compara só a data: a 08:00 vai para a data livre e a 09:00 é empurrada mais uma semana; KAS mantém as duas juntas |
| I14 | recorrência | convênio, 1 horário semanal, saldo 6 | igual | 2027-03-04 10:00, 2027-03-11 10:00, 2027-03-18 10:00, 2027-03-25 10:00, 2027-04-01 10:00, 2027-04-08 10:00 | 2027-03-04 10:00, 2027-03-11 10:00, 2027-03-18 10:00, 2027-03-25 10:00, 2027-04-01 10:00, 2027-04-08 10:00 | início = maior entre início do plano e hoje |
| I15 | recorrência | convênio, sexta e quarta cadastradas fora de ordem, saldo ímpar | igual | 2027-03-03 09:00, 2027-03-05 11:00, 2027-03-10 09:00, 2027-03-12 11:00, 2027-03-17 09:00 | 2027-03-03 09:00, 2027-03-05 11:00, 2027-03-10 09:00, 2027-03-12 11:00, 2027-03-17 09:00 | o corte do saldo cai no último dia da semana |
| I16 | feriados | convênio com 1 feriado | igual | 2027-03-01 08:00, 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00 | 2027-03-01 08:00, 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00 |  |
| I17 | feriados | convênio, 1 horário, 2 feriados no período | defeito_crm | 2027-03-01 08:00, 2027-03-22 08:00, 2027-03-29 08:00 | 2027-03-01 08:00, 2027-03-22 08:00, 2027-03-29 08:00, 2027-04-05 08:00 | a folga de 1 semana (weeksNeeded + 1) não cobre 2 feriados: o CRM gera 3 de 4 sessões autorizadas; KAS gera as 4 |
| I18 | estados internos | convênio pula um cancelamento histórico (avaliação em outro horário não pula) | regra_clinica | 2027-03-01 08:00, 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00 | 2027-03-01 08:00, 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00 | o módulo saúde escolhe as datas a pular (skip_dates); o motor genérico só pula |
| I19 | estados internos | saldo da guia (10 autorizadas, 3 usadas, 2 reservadas) | regra_clinica | 2027-03-02 13:00, 2027-03-09 13:00, 2027-03-16 13:00, 2027-03-23 13:00, 2027-03-30 13:00 | 2027-03-02 13:00, 2027-03-09 13:00, 2027-03-16 13:00, 2027-03-23 13:00, 2027-03-30 13:00 | saldo é regra do convênio: o módulo calcula 5 e passa count=5 |
| I20 | fuso horário | convênio gerado num servidor com fuso positivo | defeito_crm | 2027-02-28 08:00, 2027-03-07 08:00, 2027-03-14 08:00 | 2027-03-01 08:00, 2027-03-08 08:00, 2027-03-15 08:00 | toISOString de meia-noite local volta um dia em fuso UTC+; o KAS usa o fuso da clínica, não o do servidor |
| L21 | recorrência | liminar, 2 horários, 4 semanas cada | igual | 2027-03-01 08:00, 2027-03-04 15:00, 2027-03-08 08:00, 2027-03-11 15:00, 2027-03-15 08:00, 2027-03-18 15:00, 2027-03-22 08:00, 2027-03-25 15:00 | 2027-03-01 08:00, 2027-03-04 15:00, 2027-03-08 08:00, 2027-03-11 15:00, 2027-03-15 08:00, 2027-03-18 15:00, 2027-03-22 08:00, 2027-03-25 15:00 | KAS: uma série por horário |
| L22 | feriados | liminar com feriado e cancelamento | igual | 2027-03-01 08:00, 2027-03-22 08:00, 2027-03-29 08:00, 2027-04-05 08:00 | 2027-03-01 08:00, 2027-03-22 08:00, 2027-03-29 08:00, 2027-04-05 08:00 | os dois pulam e estendem |
| L23 | recorrência | liminar que vence antes de completar as semanas | igual | 2027-03-03 10:00, 2027-03-10 10:00, 2027-03-17 10:00, 2027-03-24 10:00 | 2027-03-03 10:00, 2027-03-10 10:00, 2027-03-17 10:00, 2027-03-24 10:00 | KAS: count e until juntos (o que vier primeiro) |
| L24 | cancelamento e remarcação | liminar regerada não duplica o que já existe | regra_clinica | 2027-03-01 08:00, 2027-03-15 08:00 | 2027-03-01 08:00, 2027-03-15 08:00 | no KAS a não duplicação é da gravação (chave de idempotência por ocorrência, fase 4A); a lista calculada é a mesma |
| L25 | conflito de recurso | liminar 08:20 contra sessão 08:00-08:40 do mesmo profissional | defeito_crm | conflito=false | conflito=true | o CRM compara só a hora exata e não vê o choque; o KAS vê sobreposição parcial |
| L26 | recorrência | liminar com início pedido no futuro e plano antigo | igual | 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00 | 2027-03-15 08:00, 2027-03-22 08:00, 2027-03-29 08:00 | início = maior entre plano, hoje e o pedido |
| A27 | disponibilidade semanal | horários do profissional com um ocupado | igual | 08:00, 09:20, 10:00 | 08:00, 09:20, 10:00 |  |
| A28 | estados internos | pré-agendado e falta bloqueiam; cancelado e concluído liberam | igual | 08:40, 10:00 | 08:40, 10:00 | pelo mapeamento de status do importador |
| A29 | feriados | feriado parcial (Quarta de Cinzas, só manhã) | igual | 13:00, 15:00 | 13:00, 15:00 | no KAS é uma exceção de meio período |
| A30 | férias e folgas | férias do profissional numa semana | diferenca_kas | 08:00, 09:00 |  | no cálculo de horários do CRM não há férias por período (o status "ferias" do profissional não é lido ali); o KAS fecha o período por exceção |
| A31 | intervalos | intervalo de 10 min depois da sessão | diferenca_kas | 08:40, 09:30 | 09:30 | o CRM não tem intervalo; no KAS é configurado por serviço (com 0, os dois coincidem: ver A27) |
| A32 | conflito de cliente | paciente já tem sessão com outro profissional no mesmo horário | igual | conflito=true | conflito=true | convênio confere sobreposição do paciente; KAS com client_exclusive |

Totais: igual 20 · regra clínica 4 · defeito do CRM 6 · conceito extra do KAS 2
