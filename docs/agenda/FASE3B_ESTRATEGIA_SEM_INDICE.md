# Agenda — Fase 3B: extração sem o índice `{updatedAt:1,_id:1}`

Status: **Planejada, não implementada.** Nada aqui foi executado contra o Atlas.
Pedido do Kadu (21/11, 08h54): "resolver a estratégia de consultas sem índice no Atlas", sem cópia de dados reais por enquanto.

## O problema

A carga incremental lê o que mudou desde a última rodada (`updatedAt > cursor`).
O modelo `Appointment` do CRM não declara índice em `updatedAt`. Sem esse índice, cada rodada incremental vira uma **varredura da coleção inteira** no Atlas.
Rodando a cada poucos minutos, isso pesa justamente no banco que atende a clínica.

Índices que o modelo já declara e que servem para outra coisa:

- `{date:-1}`
- `{doctor:1,date:1}`
- `_id`

O que existe de fato no Atlas só se confirma com `-mode plan` lá, e isso também precisa de autorização.

## Opções

| | Como funciona | Carga no Atlas | Mudança no Atlas | Atraso | Risco |
|---|---|---|---|---|---|
| **A. Janela de datas** | Cada rodada relê os agendamentos de uma janela (por exemplo, de 7 dias atrás a 120 dias à frente) pelo índice de data e reconcilia | Proporcional à janela (centenas a poucos milhares de documentos), com índice | Nenhuma | Minutos | Mudança fora da janela não entra, mas o passado distante não afeta horário livre |
| **B. Change stream** | Assina as mudanças da coleção pelo oplog; uma carga inicial e depois só os eventos | Muito baixa depois da carga inicial | Nenhuma no esquema; o usuário precisa da permissão `changeStream` | Segundos | Processo contínuo; se o oplog girar antes de retomar, precisa recarregar |
| **C. Criar o índice** | Incremental como está no código | Baixa | Índice novo (build em segundo plano, espaço extra) | Minutos | Mudança de produção; o build consome recursos uma vez |
| **D. Ler do espelho local** | Lê de `fono_espelho`, não do Atlas | Zero no dia a dia | Nenhuma | Horas ou dias (idade da cópia) | Depende da cópia real, que você ainda não autorizou |

## Recomendação

**A primeiro, B depois se for preciso tempo real, C só se A e B não bastarem.**

A usa só índices que já existem e não muda nada no Atlas. A reconciliação por janela já está implementada e testada: é o `-mode reconcile`, com exclusão só depois de duas verificações. Falta só agendar.

## Proteções em qualquer opção

1. **Antes de cada rodada:** rodar `-mode plan` no destino. Se aparecer `COLLSCAN` ou índice sem limites, a rodada não começa. Isso já está implementado.
2. **Leitura do secundário:** `readPreference=secondaryPreferred`, que já é o padrão do `kas-scheduling-mirror`.
3. **Limite de tempo por consulta:** um `maxTimeMS` em cada consulta. Ainda não está implementado; seria uma mudança pequena no `Reader.find`.
4. **Pausa entre lotes** na carga completa, para não disputar recursos com o horário de atendimento. Ainda não está implementada.
5. **Carga completa uma única vez**, fora do horário da clínica (por exemplo, domingo de madrugada), paginada por `_id`, que usa índice.
6. **Usuário Atlas exclusivo e só leitura**, restrito à coleção `appointments` e à projeção de campos do `crmagenda.Projection`. Nunca o `claude_ajustes`.
7. **Interrupção:** um arquivo ou flag que para a rodada no próximo lote, sem perder o cursor.

## O que precisa de autorização (em ordem)

1. Rodar `-mode plan` contra o Atlas. Só pede planos e não lê documentos, mas é acesso ao Atlas.
2. Criar o usuário Atlas só leitura da opção A.
3. A carga completa real, que é a cópia de dados reais. **Você ainda não autorizou.**
4. Só se for o caso: o índice da opção C, ou a permissão de change stream da opção B.

## Como medir antes de decidir (sem dado real)

No Mongo local, com 100 mil agendamentos fictícios, comparar A, B e C em três números:

- documentos examinados por rodada;
- tempo por rodada;
- atraso de propagação.

O `-mode plan` mostra os documentos examinados sem precisar de dado real.
