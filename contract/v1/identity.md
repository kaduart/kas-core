# Contrato v1: login de pessoas (identity)

Decisão do Kadu em 20/11/2026: os usuários do painel ficam no próprio KAS Core.
Isso substitui a regra D4 ("a KAS autentica só sistemas"). Os sistemas continuam
se autenticando com o próprio token; o painel é um sistema com a capacidade
`identity:sessions` e fala com a KAS em nome da pessoa.

## Quem chama

- Cabeçalhos do serviço: `X-KAS-Service` e `Authorization: Bearer <token>`.
- Capacidade: `identity:sessions` (não pertence a nenhum módulo).
- A sessão mostra **só** as empresas em que a pessoa tem papel **e** que o
  serviço pode atender. Um painel white-label de uma empresa nunca vê as outras.
- A sessão fica presa ao serviço que a criou.

## Rotas

| Rota | O que faz | Sucesso |
|---|---|---|
| `POST /internal/v1/identity/sessions` | entra com e-mail e senha | `201` com `session_token` |
| `GET /internal/v1/identity/session` | lê a sessão (`X-KAS-Session`) | `200` |
| `DELETE /internal/v1/identity/session` | sai (idempotente) | `204` |

Corpo do login: `{"contract_version":"v1","email":"...","password":"..."}`.

Resposta:

```json
{
  "contract_version": "v1",
  "session_token": "kss_…",
  "expires_at": "2026-11-21T12:00:00Z",
  "user": {"id": "usr_…", "email": "ana@escola.exemplo", "name": "Ana Souza"},
  "memberships": [{"tenant_id": "escola-demo", "role": "admin"}]
}
```

`session_token` só aparece no login. Papéis: `admin`, `gestor`, `atendimento`, `leitura`.

## Erros

| Código | HTTP | Quando |
|---|---|---|
| `invalid_credentials` | 401 | e-mail ou senha errados (a mesma resposta para e-mail que não existe) |
| `login_locked` | 429 | 5 senhas erradas seguidas: bloqueio de 15 min (`Retry-After`) |
| `no_tenant_access` | 403 | a pessoa não tem papel em nenhuma empresa deste painel |
| `session_invalid` | 401 | sessão ausente, vencida (12 h), encerrada ou de outro serviço |
| `identity_unavailable` | 503 | `-identity-store` desligado ou banco fora |

## Segurança

- Senha: PBKDF2-SHA256, 600 mil iterações, sal aleatório; mínimo de 10 caracteres.
- Token de sessão: 32 bytes aleatórios; o banco guarda só o SHA-256.
- Trocar a senha encerra todas as sessões da pessoa.
- E-mail não vai para log nem para evento.

## Operação

```
kas-core -events-store postgres -identity-store postgres
```

Criar usuário (a senha vem de variável de ambiente, nunca de flag):

```
KAS_IDENTITY_NEW_PASSWORD=... kas-core -identity-add-user ana@escola.exemplo \
  -identity-user-name "Ana Souza" -identity-memberships escola-demo:admin
```

Trocar senha: `-identity-set-password <e-mail>` com a mesma variável.
