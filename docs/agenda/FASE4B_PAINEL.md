# Agenda — Fase 4B: tela no painel Next.js

Status: **Implementada e validada localmente** contra a API real do KAS Core (Go + PostgreSQL local, tenant fictício
`clinica-aurora`). Sem mock: no modo demonstração (sem KAS ligado) a tela explica que precisa do KAS em vez de inventar
horários. Nada foi publicado.

## O que a tela faz

| Função | Como |
|---|---|
| Visão diária e semanal | Abas Dia/Semana; grade proporcional ao tempo, no fuso da empresa (vem do Go), linha de "agora" |
| Escolher profissional/sala | Seletor com os recursos ativos (`GET /scheduling/catalog`) |
| Serviço | Seletor de serviço (duração e intervalo do Go) ou horário padrão de 40 min |
| Horários livres | Blocos tracejados na grade e botões no painel lateral (`GET /scheduling/availability`) |
| Agendar | Clique no horário livre → código do cliente (sem nome/telefone) + situação → Agendar |
| Remarcar | Clique no agendamento → Remarcar → escolhe horário livre com a mesma duração → Mover |
| Cancelar | Clique no agendamento → Cancelar → confirmação em linha (sem modal) |
| Intervalo do serviço | Faixa listrada depois do atendimento mostra o buffer que bloqueia o profissional |
| Carregando / erro / vazio | Esqueleto, cartão de erro com "Tentar de novo", dia sem atendimento com atalhos |
| Permissões | `leitura` e agenda espelhada → aviso "Somente leitura", sem botões de escrita; o Go recusa de novo (403/409) |
| Marca da empresa | Cores e logo do tenant (white-label) aplicados pelo tema existente |
| Celular | Filtros empilhados, semana vira lista por dia, sem rolagem lateral (largura 390 px medida) |

## Segurança no BFF (`web/src/app/api/kas/agenda/*`)

- A empresa vem sempre da sessão; o navegador nunca envia `tenant_id` nem vê o token do serviço.
- Corpo validado com zod em modo estrito; código do cliente segue o mesmo padrão do Go.
- Escritas exigem `Origin` igual ao `Host` (barra CSRF); 403 `origin_forbidden` caso contrário.
- Chave de idempotência gerada por tentativa no navegador (`web-<uuid>`): reenviar não duplica.
- Toda escrita, certa ou recusada, recarrega a agenda (depois de `slot_taken`/`version_conflict` a tela mostra o estado real).

## Correção encontrada

`MODULE_KEYS` do painel não tinha `scheduling`. Desde a Fase 1 o Go devolve `scheduling` em `GET /internal/v1/modules`,
e o zod do painel recusaria a resposta (`contract_mismatch`) no modo real. Corrigido nesta fase.

## Evidência

| Verificação | Resultado |
|---|---|
| `tsc --noEmit`, `eslint .` | sem erros |
| `vitest run` | 45 testes (6 novos de fuso/datas, 3 de geometria da grade, 6 de BFF/cliente) |
| Detector do impeccable em `src/components/agenda` | 0 achados |
| Ponta a ponta (Playwright, KAS local real) | Maria (atendimento) agenda às 17:00 (validação do código vazio ok), remarca para 18:20, cancela; Leo (leitura) vê 0 botões de horário livre e o POST direto ao BFF volta `403 role_forbidden`; celular 390 px sem rolagem lateral |
| `kas-scheduling-seed` | 30 agendamentos na 1ª carga, 0 na 2ª (idempotente); recusa `fono-inova`, banco remoto e nome com "prod" |

Prints: pasta do projeto `agenda-fase4b/` (fora do repositório).

## Pendente

- Kadu validar no PC com `deploy/painel-local` (o compose foi atualizado mas não foi executado aqui; aqui rodou o mesmo binário direto).
- CI do GitHub não roda os testes do `web/` (só os do Go e do `node-integration`); validação do painel é local.
- Arrastar e soltar para remarcar, lista de espera e confirmação por WhatsApp ficam para depois (não pedidos nesta fase).
