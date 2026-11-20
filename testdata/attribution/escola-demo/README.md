# Segunda empresa fictícia: escola-demo

Escola inventada, usada para provar que o KAS Attribution atende outra empresa sem código próprio. Todos os nomes, e-mails e ids são inventados (domínio `exemplo.test`).

O que a amostra exercita, diferente da Fono:

- clientes identificados por **e-mail** (não telefone), com **unidades** (centro, norte);
- **mesma família** com dois responsáveis (resp-04 e resp-05, mesmo e-mail em caixa diferente): sinal forte em contato compartilhado vira "provável";
- **um pagamento com dois recebimentos** (matrícula + mensalidade, `pay-a1`) e um estorno só com `payment_ref`: rateio 19.000 + 24.000 centavos;
- **contato depois do recebimento** (resp-07) e **estorno ligado ao recebimento** (`receipt_ref`);
- **cliente sem contato** (resp-06): sem vínculo, "sem origem capturada";
- **linha cancelada** (`mat-99`, `voided=sim`): não entra no relatório e não é apagada.

Resultado esperado (centavos): líquido 647.000 = comprovada 267.000 + provável 285.000 + sem vínculo 95.000. Unidade centro: 362.000.
