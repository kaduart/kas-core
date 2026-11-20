# Roteiro: experimentar o editor no Windows (WSL)

Tudo roda no seu computador com dados fictícios e um CRM simulado. Nada toca o CRM de produção, o Render ou o Atlas.

## Antes (uma vez só)

- WSL com Ubuntu, Go 1.25+, Node 22 e Docker Desktop com a integração WSL ligada.
- Cliente do Postgres no Ubuntu:

```bash
sudo apt-get install -y postgresql-client
```

## Subir

Abra o terminal do Ubuntu (WSL) na pasta do `kas-core` e rode um comando por vez.

```bash
git fetch origin kas-core/process-studio-editor
```

```bash
git checkout kas-core/process-studio-editor
```

```bash
studio/scripts/stack.sh start
```

Esse comando sobe:

- um Postgres descartável no Docker;
- a KAS real com o motor ligado;
- um CRM simulado por empresa;
- a versão 1 do fluxo de retenção, com 2 dias úteis na `demo-empresa-a` e 5 na `demo-empresa-b`.

```bash
cd studio
```

```bash
npm ci
```

```bash
set -a; source .stack/env; set +a
```

```bash
npm run dev
```

Abra no navegador do Windows:

```
http://127.0.0.1:5174
```

## Demonstração (uns 3 minutos)

1. Em **Empresa**, escolha `demo-empresa-a`. Na aba **Desenho**, o fluxo `retencao-cancelamento` abre com a etapa `negociar` em **2 dias úteis**.
2. Na aba **Execuções**, inicie a referência `contrato-antes`. Ela fica na **versão 1**. Anote o "vence" da tarefa `negociar`.
3. Volte para **Desenho** e clique em `negociar`. Mude **Dias úteis** para `3` e aperte Enter. A caixa **Alterações não publicadas** mostra `2 → 3`.
4. Clique em **Validar**. Quando aparecer "Definição válida", clique em **Publicar nova versão**. Aparece "Versão 2 publicada".
5. Troque a empresa para `demo-empresa-b`. O prazo continua em **5 dias úteis**, ou seja, a outra empresa não mudou.
6. Volte para `demo-empresa-a`. Em **Execuções**, inicie a referência `contrato-depois`. Ela fica na **versão 2**, com o "vence" um dia útil depois.
7. Clique em `contrato-antes`. Ela continua na **versão 1**, com o **prazo original**.
8. Clique em `contrato-depois` e depois em `cliente_cancela`. O desenho destaca a etapa atual. O CRM simulado confirma em instantes, a execução termina como **concluída: cancelado** e o **Histórico** mostra cada passo.

## Encerrar

Pare o servidor com `Ctrl+C` e rode:

```bash
scripts/stack.sh stop
```

## Opcional: ver o teste automático fazendo tudo isso

Com a stack de pé e o `.stack/env` carregado, rode um comando por vez:

```bash
npx playwright install chromium
```

```bash
npx playwright test --headed
```

## Sem Docker

Se você tiver um Postgres local, passe o endereço de administrador antes do `start`:

```bash
PG_ADMIN_URL='postgres://USUARIO@127.0.0.1:5432/postgres?sslmode=disable' studio/scripts/stack.sh start
```
