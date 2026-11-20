# KAS Web (painel SaaS)

Front do KAS Core em **Next.js (App Router) + TypeScript + Tailwind CSS v4**. Primeira entrega: o painel aprovado
(layout "Dashboard KAS Core com Sidebar Verde") com **white-label por empresa** (nome, cores e ícone).

O Go (kas-core) continua sendo o cérebro: receita, permissões, módulos e tenant são decididos lá. O front só exibe.

## Rodar local

```bash
cd web
npm install
npm run dev        # http://localhost:3100
```

Verificações (as mesmas do CI `.github/workflows/web.yml`):

```bash
npm run lint && npm run typecheck && npm test && npm run build
```

## Modo demonstração x modo real

Quem decide o que cada empresa pode usar é o **KAS Core (Go)**. O painel só mostra essa decisão; as APIs do Go recusam de novo
(`403 module_not_contracted` / `module_paused`).

| | Demonstração | Real |
|---|---|---|
| Liga quando | faltam as variáveis abaixo | `KAS_API_URL`, `KAS_SERVICE_ID` e `KAS_SERVICE_TOKEN` definidos no servidor |
| Empresa | cookie `kas_tenant` ou domínio; troca livre no topo | **só pelo domínio**; domínio desconhecido dá erro, sem empresa padrão |
| Módulos | simulados (`src/lib/kas/demo.ts`), liga/desliga no catálogo via cookie | `GET /internal/v1/modules?tenant_id=` (contrato v1, kas-core #26); liga/desliga não existe na tela |
| CRM, Omnichannel, Amanda IA | abrem como **prévia** com aviso | bloqueados: estão `planned` no catálogo do Go |

- `src/lib/session.ts` monta a sessão no servidor a cada requisição. As credenciais do serviço ficam só no servidor (`src/lib/kas/core-client.ts`).
- `src/lib/kas/contract.ts` espelha o contrato v1 e tem `moduleAccess`: ativo, ativo com integração pendente, pausado, não contratado, planejado.
- `ModuleGate` é componente de servidor: a tela bloqueada nem chega ao navegador.
- Saúde (Agenda, Financeiro clínico, Prontuário, Estoque) é **extensão vertical**: aparece só para empresas do segmento saúde, fora do catálogo comum.
- Pendente: login de pessoas (hoje o usuário é fixo) e marca vinda do configstore.

Empresas de demonstração: Fono Inova, Clínica Aurora, Odonto Sorriso (Attribution com integração pendente e Relatórios pausado)
e **Escola Exemplo** (empresa 2, educação, só Attribution + Relatórios, sem o CRM da Fono Inova).

## Como funciona o white-label

- `src/lib/tenant/presets.ts`: empresas de demonstração e domínios.
- `src/lib/tenant/color.ts` deriva a paleta inteira (escuro, fundo do menu, tom claro, texto sobre destaque) a partir de **duas cores**.
  O formulário de marca recusa cor principal clara demais para o texto branco do menu (contraste AA).
- A marca personalizada é salva em cookie `kas_brand_<tenant>` para o servidor já renderizar com as cores certas.
  Na integração, passa a vir do configstore do KAS Core.

## Dados

`src/lib/kas/fixtures.ts` tem dados fictícios. `src/lib/kas/api.ts` é o único ponto de leitura (TanStack Query):
na integração, troca-se `fetchDashboard` pela chamada ao KAS Core, sem mexer nas telas. O contrato está em `src/lib/kas/types.ts`.

## Stack

Next.js 16 · React 19 · Tailwind v4 · shadcn/ui (Radix + cva) · Motion (gaveta do celular) · Recharts · TanStack Query ·
React Hook Form + Zod · sonner (avisos) · lucide-react. React Flow continua no `studio/`.

## Telas

| Rota | Tela | O que dá para fazer na demonstração |
|---|---|---|
| `/dashboard` | Painel | período, canais do donut, abas de receita |
| `/attribution` | Attribution | período, tabela de campanhas com retorno |
| `/crm` | CRM | busca, avançar lead de etapa (colunas viram carrossel no celular) |
| `/omnichannel` | Omnichannel | abrir conversa, responder (some do "sem resposta") |
| `/process-studio` | Process Studio | pausar/ativar fluxo; editor visual segue no `studio/` |
| `/amanda-ia` | Amanda IA | classificações e o que a IA fez |
| `/relatorios` | Relatórios | baixar CSV/PDF (simulado) |
| `/modulos` | Catálogo | estado de cada módulo; na demonstração, liga/desliga respeitando dependências (Relatórios precisa de Attribution) |
| `/configuracoes` | Marca | nome, cores e ícone |

Telas de módulo ficam atrás de `ModuleGate` (servidor): não contratado, pausado e planejado têm mensagens próprias; integração pendente e prévia mostram aviso no topo.

## Componentes de interface

`src/components/ui/` segue o padrão shadcn/ui (cva + Radix + `cn` com tailwind-merge), adaptado aos tokens da marca.
O `components.json` já está pronto; nesta máquina o registro do shadcn estava bloqueado pela rede, então os
componentes foram escritos no mesmo formato. Para novos: `npx shadcn@latest add <componente>`.

## Próximos passos

1. Login e sessão de pessoas pelo KAS Core; troca de empresa só entre as permitidas.
2. Attribution e Relatórios lendo os contratos reais (`/internal/v1/reports/revenue`, kas-core #28).
3. Demonstração da Escola Exemplo com dados isolados, CSV idempotente e receita conciliada.
4. Só depois: extensão Saúde (ERP de clínicas).
