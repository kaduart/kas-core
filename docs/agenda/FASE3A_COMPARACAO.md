# Fase 3A — motor de comparação KAS × CRM (com fixtures)

Código: `internal/legacy/crmagenda/compare.go` (motor) e `calendar.go` (regras de
horário do CRM portadas: horários por dia da semana do profissional, feriados
fixos e da Páscoa com meio período, intervalo de 40 min, expediente 08:00–18:00).
Histórico: migração `0003_mirror_compare.sql` (só contagens). Comando:
`kas-scheduling-mirror -mode sync-availability | compare [-format md] | history`.

## O que compara, por profissional e dia

1. **Agendamentos**: o espelho tem exatamente as linhas da origem? (faltando,
   sobrando, desatualizada, profissional não espelhado)
2. **Horários livres**: o que o CRM mostraria (regra dele sobre os dados dele) ×
   o que o KAS calcula sobre o espelho.
3. **Status**: tabela CRM → KAS de todos os status do período.
4. **Conflitos**: pares sobrepostos na origem.
5. **Recorrência**: padrões semanais (profissional, dia da semana, horário) com
   suas quantidades, dos dois lados.

## Classificação

| Resultado | Quando |
|---|---|
| igual | mesmos agendamentos e mesmos horários livres |
| erro de importação | o espelho não bate com a origem; a diferença de horário é consequência |
| regra clínica | mesmos agendamentos, horários diferentes por motivo conhecido: calendário de feriados, exceção no KAS (férias), status do profissional |
| divergência real | mesmos agendamentos, horários diferentes, sem motivo conhecido |

## Testes (Mongo + Postgres reais, dados fictícios)

- igual depois da importação (15 de 15 profissional-dia; padrões semanais iguais)
- erros de importação separados (linha alterada e linha nova não importadas) e
  voltando a igual depois do incremental
- regras clínicas: férias (exceção no KAS + status "ferias" no CRM) e terça de
  Carnaval só de manhã no CRM
- divergência real (horário mudado à mão no KAS) e profissional não espelhado
- histórico em ordem; relatório e logs sem ids do CRM
- janelas inválidas recusadas; calendário de feriados do CRM conferido
- ponta a ponta no script: março/2027 com 200 agendamentos, 75 profissional-dia:
  72 iguais, 3 regra clínica (Sexta-feira Santa: feriado no CRM, não cadastrado no
  KAS), 0 importação, 0 divergência real, 113 de 113 padrões iguais

## Achados no calendário do CRM (só registro)

- Não tem 20/11 (Consciência Negra, feriado nacional desde 2024).
- Chama de "Quarta-feira de Cinzas" a terça de Carnaval (Páscoa − 47) e bloqueia
  só a manhã; a quarta de Cinzas verdadeira (Páscoa − 46) não bloqueia nada.
- A ocupação do CRM também conta a coleção de sessões (`Session`); o espelho lê só
  `appointments`. Sessão sem agendamento correspondente vai aparecer como
  divergência na comparação com dados reais: precisa de decisão na fase 3B.

## Limites

Só fixtures. Comparar com a agenda real exige a cópia protegida (fase 3B), que
está **bloqueada** até autorização.
