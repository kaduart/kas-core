# Painel KAS no seu PC (modo real)

Sobe o KAS Core (Go) com Postgres e uma carga de demonstração, para o painel
`web/` entrar com login de verdade e mostrar a receita calculada pelo Go.
Nada aqui fala com Render, Atlas ou o CRM da clínica.

## O que vem carregado

| Empresa | Módulos | Dados |
|---|---|---|
| Escola Exemplo (`escola-demo`) | Attribution + Relatórios | CSVs de `testdata/attribution/escola-demo`: fecha **R$ 6.470,00** |
| Clínica Aurora (`clinica-aurora`) | Attribution + Relatórios | amostra fictícia (`amostra-fono`) |
| Odonto Sorriso (`odonto-sorriso`) | Attribution ativo, Relatórios **pausado** | sem dados (mostra "falta integrar") |
| Fono Inova (`fono-inova`) | Attribution + Relatórios | **vazia de propósito**: recebe o banco da clínica no teste |

## Passo a passo (PowerShell, na pasta do repositório)

1. Crie o arquivo de acesso a partir do exemplo e preencha e-mail, nome e senha (10+ caracteres):

```
copy deploy\painel-local\.env.example deploy\painel-local\.env
```

2. Suba o KAS Core e a carga (a primeira vez demora uns minutos):

```
docker compose -f deploy/painel-local/docker-compose.yml up --build -d
```

3. Ligue o painel no modo real:

```
copy web\.env.local.example web\.env.local
```

4. Rode o painel e abra http://localhost:3100:

```
cd web; npm run dev
```

Para voltar ao modo demonstração, apague `web\.env.local` e reinicie o `npm run dev`.

## Carregar de novo

Pode rodar quantas vezes quiser; nada duplica (cada lote tem id fixo):

```
docker compose -f deploy/painel-local/docker-compose.yml run --rm seed
```

## Zerar tudo

Apaga o banco local (só deste painel):

```
docker compose -f deploy/painel-local/docker-compose.yml down -v
```

## Como funciona

- O navegador só fala com o Next.js. O Next.js fala com o KAS Core com a
  credencial do serviço `kas-web`, que nunca vai para o navegador.
- A pessoa entra com e-mail e senha guardados no KAS Core (senha com PBKDF2,
  sessão de 12 horas, trava de 15 minutos após 5 erros). O token da sessão fica
  num cookie `httpOnly`.
- A empresa sempre vem da sessão. Mudar o cookie de empresa para uma que a
  pessoa não tem não abre nada: o painel volta para uma empresa permitida.
- A porta 8088 fica só no `127.0.0.1` do PC, em HTTP, por um repasse (`porta`)
  que existe só aqui. Em produção o KAS escuta com TLS.
