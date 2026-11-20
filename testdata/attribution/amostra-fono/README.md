# Amostra fictícia da Fono no formato genérico do KAS Attribution

Tradução da amostra `services/attribution/fixtures/receitaFicticia.js` do CRM (PR #99) para as quatro entradas genéricas. Todos os nomes, telefones e ids são inventados.

- Cada AdConversion de aquisição vira um contato com `acquisition=true`. A `unknown` (Gabi) não vira contato.
- Cada LeadAttribution do telefone vira um contato sem `acquisition`, ligado a um cliente daquele telefone.
- Só entram créditos de caixa (`payment_received`, `package_purchase`, `insurance_received`) e o estorno ligado a pagamento. O CRM já exclui `revenue_recognition`, `package_consumed`, `insurance_billed` e a reversão sem pagamento.

Resultado esperado, igual ao do CRM: líquido R$ 1.290 = comprovada 480 + provável 570 + sem vínculo 240.
