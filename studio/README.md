# KAS Process Studio (piloto do editor)

Editor visual dos fluxos do motor (`internal/process`), com visual inspirado em BPMN, feito em React Flow. Ele edita o mesmo JSON `kas.process/v1` que o motor valida e não carrega nenhuma regra de negócio.

## O que faz

- **Desenho:** abre o fluxo de cada empresa e mostra todas as versões. Na etapa, edita papéis, prazo (dias úteis com corte, ou duração), tolerância, caminhos e status de negócio. Também dá para arrastar as etapas. Antes de publicar, o editor lista as alterações. **Publicar só fica liberado depois de Validar.** Cada publicação gera uma versão nova, e execuções já iniciadas continuam na versão delas.
- **Execuções:** inicia uma execução e acompanha a etapa atual no desenho, as tarefas com prazo (horário de Brasília), os comandos ao CRM e o histórico. As tarefas abertas são resolvidas com um ator fictício.

## Segurança do piloto

- O navegador nunca recebe token. O servidor do Studio (`server/kasProxy.ts`) guarda os tokens de serviço e faz o papel do servidor do CRM:
  - carimba `occurred_at` e `tenant_id`;
  - só alcança `/internal/v1/process/*` de leitura, validação, publicação, início e resolução;
  - não consome eventos e não reporta resultado de comando.
- A KAS precisa estar em loopback. Os servidores do Studio escutam só em `127.0.0.1`.

## Rodar localmente

Roteiro do dia a dia no Windows (WSL com Ubuntu). A primeira instalação está no [roteiro passo a passo](ROTEIRO-WINDOWS.md). Tudo roda no seu PC, com dados fictícios e CRM simulado. Nada toca produção, Render ou Atlas.

### 1. Abra o Docker Desktop

Depois de reiniciar o PC, o Docker Desktop **não abre sozinho**. Abra pelo menu Iniciar e espere aparecer **Engine running** no canto de baixo.

Se aparecer erro de WSL ("Falha catastrófica" ou `E_UNEXPECTED`), rode no PowerShell e abra o Docker de novo:

```
wsl --shutdown
```

### 2. Entre no Ubuntu

No PowerShell:

```
wsl -d Ubuntu
```

### 3. Vá para a pasta `studio`

Todos os comandos a seguir rodam **dentro de `studio`**, não na raiz do repositório.

```
cd ~/kas-core/studio
```

### 4. Suba a stack de teste

```
scripts/stack.sh start
```

Esse comando sobe um Postgres descartável no Docker, a KAS e um CRM simulado por empresa, e publica a versão 1 do fluxo de retenção (`demo-empresa-a` com 2 dias úteis, `demo-empresa-b` com 5).

**O banco começa vazio a cada `start`.** Isso é proposital: reiniciar o PC ou rodar `start` de novo apaga as versões e execuções anteriores e recria só a versão 1. Termina com `Stack pronta`.

### 5. Instale as dependências (só na primeira vez ou depois de atualizar o código)

```
npm ci
```

### 6. Carregue os tokens da stack

Sem esse passo, o editor abre sem nenhuma empresa. Rode de novo sempre que fizer um `start` novo.

```
set -a; source .stack/env; set +a
```

### 7. Suba o editor

```
npm run dev
```

O terminal fica preso mostrando `Local: http://127.0.0.1:5174`. Isso é normal: deixe essa janela aberta.

### 8. Abra no navegador do Windows

Cole o endereço abaixo no **Chrome**, não no terminal:

```
http://127.0.0.1:5174
```

### 9. Desligar

No terminal do editor, aperte `Ctrl+C`. Depois:

```
scripts/stack.sh stop
```

### Problemas comuns

| O que aparece | O que fazer |
| --- | --- |
| `Cannot connect to the Docker daemon` | Abra o Docker Desktop e espere **Engine running** (passo 1). |
| `No such file or directory` no `stack.sh` | Você está fora de `studio`. Rode `cd ~/kas-core/studio` (passo 3). |
| Editor abre sem empresa ou com erro `tenant_not_configured` | Pare com `Ctrl+C`, rode o passo 6 e depois o passo 7. |
| `kas_unreachable` no editor | A stack caiu (por exemplo, depois de reiniciar). Rode os passos 4, 6 e 7. |
| Sumiram as versões e execuções | Normal depois de reiniciar ou de um `start` novo (passo 4). |
| `command not found` ao colar a URL | A URL vai no navegador, não no terminal (passo 8). |

Pré-requisitos: Go 1.25+, Node 20 ou 22, `psql` (`sudo apt-get install -y postgresql-client`) e Docker Desktop com a integração WSL ligada para o Ubuntu (ou um Postgres local passado em `PG_ADMIN_URL`).

## Testes

- `npm test`: unitários (modelo do desenho e proxy).
- `npm run e2e`: critério de aceite no navegador, com a stack local de pé. O teste:
  1. abre o fluxo de retenção;
  2. muda o prazo da empresa A;
  3. valida e publica a versão nova;
  4. confere que a empresa B não mudou;
  5. confere que a execução iniciada antes continua com o prazo original;
  6. acompanha uma execução até o CRM simulado confirmar.

  As capturas de tela ficam em `test-results/shots/`.
- No CI, o job `studio` roda tudo isso e publica as capturas como artefato.
