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

## Como funciona o white-label

- `src/lib/tenant/presets.ts`: empresas de demonstração (Fono Inova, Clínica Aurora, Odonto Sorriso), domínios e módulos ativos.
- A empresa é resolvida no servidor pelo cookie de sessão `kas_tenant` e, sem ele, pelo domínio (`resolveTenant`).
- `src/lib/tenant/color.ts` deriva a paleta inteira (escuro, fundo do menu, tom claro, texto sobre destaque) a partir de **duas cores**.
  O formulário de marca recusa cor principal clara demais para o texto branco do menu (contraste AA).
- A marca personalizada é salva em cookie `kas_brand_<tenant>` para o servidor já renderizar com as cores certas.
  Na integração, passa a vir do configstore do KAS Core.
- O menu mostra só os módulos ativos da empresa.

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
| `/modulos` | Catálogo | ligar/desligar módulo da empresa; menu e telas acompanham. Módulos do ERP de clínicas aparecem como "Em breve" |
| `/configuracoes` | Marca | nome, cores e ícone |

Telas de módulo ficam atrás de `ModuleGate`: se a empresa não tem o módulo, aparece o convite para ativar.

## Componentes de interface

`src/components/ui/` segue o padrão shadcn/ui (cva + Radix + `cn` com tailwind-merge), adaptado aos tokens da marca.
O `components.json` já está pronto; nesta máquina o registro do shadcn estava bloqueado pela rede, então os
componentes foram escritos no mesmo formato. Para novos: `npx shadcn@latest add <componente>`.

## Próximos passos

1. Módulos do ERP de clínicas: Agenda, Financeiro, Prontuário e Estoque.
2. Login e sessão vindos do KAS Core; troca de empresa só entre as permitidas.
3. Upload de logo próprio da empresa.
4. Ligar `fetchDashboard` ao endpoint de relatórios do KAS Core.
