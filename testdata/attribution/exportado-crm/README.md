# Amostra fictícia exportada pelo próprio CRM

Gerada pelo exportador do CRM (`services/attribution/kasExport.js`, PR do fono-inova-crm-back) a partir da mesma amostra fictícia da receita por campanha (`services/attribution/fixtures/receitaFicticia.js`). Diferente de `amostra-fono` (traduzida à mão), aqui os ids são os do CRM (ObjectId) e as linhas saem exatamente como o exportador gera.

O teste `TestCRMExportMatchesCRM` confere que o KAS chega ao mesmo resultado do CRM: líquido R$ 1.290 = comprovada 480 + provável 570 + sem vínculo 240.
