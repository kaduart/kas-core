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

Next.js 16 · React 19 · Tailwind v4 · Radix (menus) · Motion (gaveta do celular) · Recharts · TanStack Query ·
React Hook Form + Zod · lucide-react. shadcn/ui entra quando as telas de módulo precisarem de mais componentes;
nesta entrega os poucos componentes foram feitos direto sobre Radix. React Flow continua no `studio/`.

## Próximos passos

1. Telas dos módulos (hoje mostram um aviso).
2. Login e sessão vindos do KAS Core; troca de empresa só entre as permitidas.
3. Upload de logo próprio da empresa.
4. Ligar `fetchDashboard` ao endpoint de relatórios do KAS Core.
