# Sombra do reaper KAS contra produção (só leitura)

Data: 19/11/2026. Estado: **pacote pronto, sombra desligada.** Nada foi criado no Atlas, no Render ou nos segredos do GitHub. Nenhuma escrita em produção, nenhum custo.

## 1. O que a sombra faz e o que nunca faz

- A cada 5 min, lê no `eventstore` os eventos `processing` elegíveis (a mesma consulta indexada do Node: `{status, createdAt}`, limite 100) e **prevê** o que o reaper Node fará com cada um.
- No ciclo seguinte, relê só esses `eventId` e compara: `agree`, `still_processing`, `diverge` ou `gone`.
- **Nunca escreve.** Três barreiras independentes:
  1. código: no modo sombra o Runner não tem lease nem chama `Apply`;
  2. servidor: o usuário Mongo só tem `find` no `eventstore`; antes do primeiro ciclo a sombra pede `connectionStatus` com privilégios e **recusa iniciar (saída 2)** se houver qualquer ação de escrita ou se a conexão não estiver autenticada (`-require-read-only on`);
  3. teste isolado: hash do banco e oplog inalterados enquanto só a sombra roda (`deploy/sombra/validate.sh`, cenário S3).
- **Não afeta o Node.** O Node só pula o próprio ciclo quando existe um lease KAS vivo, e a sombra nunca cria lease. Sombra parada, travada (SIGSTOP), morta (kill -9) ou sem banco: o Node segue recuperando normalmente (cenários S5 e S6).
- Lê com `secondaryPreferred`, então a carga cai na réplica secundária quando houver.
- Lê só `eventId`, `eventType`, `status`, `createdAt`, `attempts`, `processingToken` e campos de recuperação. Não lê `payload`.

## 2. Onde executar (recursos levantados)

| Opção | Custo | Disponibilidade | Veredito |
|---|---|---|---|
| **GitHub Actions no `kaduart/kas-core`** (repo público) | R$ 0 (minutos ilimitados em repo público) | Execuções de ~5h45 em fila, cobertura quase contínua; não depende de PC ligado | **Recomendado** |
| KADU-PC (Windows, local) | R$ 0 | Depende do PC ligado e acordado 5 dias; risco de cobertura < 90% | Reserva |
| Render (workspace atual) | Worker extra pago; web free não recebe tráfego privado e dorme | — | Não usar (custo/regra "nada no Render sem OK") |
| Atlas, outras nuvens | Pago ou exige conta nova | — | Não usar |

Rede: a lista de acesso do projeto Atlas de produção já tem a faixa aberta usada pelo Render (conferido em leitura em 19/11), e o workflow de backup já chega ao cluster a partir do Actions. **Não é preciso mudar rede.** A segurança fica no usuário só leitura com senha forte.

## 3. Configuração

Workflows (só rodam depois do merge na `main`, porque agenda do Actions usa a branch padrão):

- `.github/workflows/sombra-reaper.yml`: de hora em hora (minuto 7); grupo de concorrência enfileira a próxima execução, então sempre há uma rodando. Cada execução: compila da `main`, roda `kas-event-reaper -mode shadow -protocol v2 -require-read-only on -warmup 30s -interval 5m`, sonda o `/health` do CRM a cada 60 s, publica o resumo (só contagens) e guarda o relatório completo **criptografado (AES256)** como artifact por 14 dias.
- `.github/workflows/sombra-reaper-resumo.yml`: todo dia às 09:00 (BRT) junta as execuções dos últimos dias e avalia os critérios. Manual com `days` e `min_days`.

Execuções canceladas na fila são normais: o Actions mantém só uma pendente por grupo.

Segredos do repositório (Settings → Secrets and variables → Actions → Secrets):

```
KAS_REAPER_SHADOW_MONGO_URI
```

```
SOMBRA_REPORT_PASSPHRASE
```

Variáveis do repositório (aba Variables):

```
SOMBRA_REAPER_ATIVA
```
`true` liga, `false` (ou apagar) desliga.

```
SOMBRA_LEGACY_FENCE
```
Mesmo valor de `EVENT_LEGACY_FENCE_AT` da API no Render; vazio se a API não tiver essa variável.

Opcionais, com padrão: `SOMBRA_DB` (`fono_inova_prod`) e `SOMBRA_CRM_HEALTH_URL` (`https://fono-inova-crm-back.onrender.com/health`).

## 4. Permissão mínima no Mongo (Atlas)

Papel em `deploy/sombra/atlas-role.json`: só `FIND` em `fono_inova_prod.eventstore`. Nada de `listCollections`, nada em outras coleções, nada de escrita.

Passo a passo (Kadu, no painel Atlas, projeto de produção):

1. Database Access → Custom Roles → Add New Custom Role. Nome:
   ```
   kasReaperShadowRead
   ```
   Ação `find` (Collection Actions → Query and Write Actions → find), banco `fono_inova_prod`, coleção `eventstore`.
2. Database Users → Add New Database User → Password. Usuário:
   ```
   kas_reaper_shadow
   ```
   Senha gerada pelo Atlas. Em Database User Privileges, marcar **só** o papel customizado `kasReaperShadowRead`. Restringir ao cluster de produção.
3. Montar a URI no formato `mongodb+srv://kas_reaper_shadow:<senha>@<host>/?retryWrites=false&readPreference=secondaryPreferred` e colar direto no segredo `KAS_REAPER_SHADOW_MONGO_URI`. Não enviar a URI por chat.

Limite conhecido: o Atlas não restringe campos, então esse usuário consegue ler o `payload`. O código projeta só os campos da seção 1, e a credencial fica só no segredo do GitHub. Endurecimento opcional (decisão do Kadu, porque cria um objeto em produção): uma view projetada sem `payload` e o papel apontando para ela.

Para desfazer: apagar o usuário `kas_reaper_shadow` e o papel. Nada mais foi criado.

## 5. Coleta e leitura dos resultados

- Cada execução: aba **Summary** do run mostra a tabela C1–C7 e as classes de divergência, só contagens, sem `eventId`.
- Resumo diário: run do `sombra-reaper-resumo`.
- Detalhe (com `eventId`, para investigar): baixar o artifact e abrir localmente:
  ```
  gpg -d sombra-<run>.tar.gz.gpg | tar xz
  ```
  Depois:
  ```
  go run ./cmd/kas-shadow-summary -with-ids run/report.jsonl
  ```
- Formato do relatório (`report.jsonl`): linha `start` (modo, protocolo, banco, classe do host, `read_only` comprovado), uma linha `cycle` por ciclo (previsões, conferências, erros, duração e `proc` com RSS, heap, goroutines e CPU) e linha `stop`.

## 6. Critérios objetivos (avaliados por `kas-shadow-summary`)

| | Critério | Passa quando |
|---|---|---|
| C1 | Nenhuma escrita pela sombra | Toda execução em modo sombra, com só leitura comprovado pelo servidor e 0 resultados de escrita |
| C2 | Divergências classificadas | `decision_mismatch` = 0 e `unclassified` = 0 |
| C3 | Nada parado sem explicação | `node_not_acting` = 0 |
| C4 | Erros registrados e dentro do limite | Ciclos com erro ≤ 1% e no máximo 3 seguidos |
| C5 | Sem impacto perceptível no CRM | Ciclo p95 ≤ 2 s, ≤ 100 candidatos por ciclo, `/health` com ≥ 99% de respostas 200 |
| C6 | Consumo registrado | RSS máximo ≤ 128 MB; CPU total registrada |
| C7 | Período e cobertura | ≥ 5 dias e ≥ 90% dos ciclos esperados |

Saída do resumo: 0 aprovado, 1 algum critério falhou, 3 período ainda curto.

Classes de divergência (um `diverge` sempre recebe uma):

| Classe | Significado | Conta como falha? |
|---|---|---|
| `concluded_by_worker` | Um worker concluiu o evento entre a previsão e a conferência (DV-1 em `DIVERGENCIAS.md`) | Não |
| `same_result_other_writer` | Estado final igual ao previsto, mas a mensagem gravada não é a do reaper (ex.: o worker falhou o evento) | Não |
| `changed_by_other_writer` | Outro escritor (worker, retry manual) levou o evento para outro estado | Não |
| `decision_mismatch` | O reaper Node gravou (mensagem dele) um destino diferente do previsto | **Sim** |
| `unclassified` | Nenhuma regra explica | **Sim** |
| `node_not_acting` | Evento elegível em `processing` por 4 conferências seguidas (~20 min) | **Sim** (C3) |
| `gone` | Evento apagado (ex.: limpeza) | Não |

A classe vem de quem escreveu a mensagem observada (`observed_reason`: `reaper`, `other`, `none`); o texto da mensagem nunca é copiado para o relatório.

## 7. Roteiro dos cinco dias

**Dia 0 (pré-condições, ~15 min do Kadu)**
1. Merge do PR desta branch na `main`.
2. Criar papel e usuário (seção 4) e os dois segredos.
3. Copiar `EVENT_LEGACY_FENCE_AT` da API para `SOMBRA_LEGACY_FENCE` (ou deixar vazio).
4. Teste curto: Actions → `sombra-reaper` → Run workflow com `minutes` = 15, depois de pôr `SOMBRA_REAPER_ATIVA=true`. Esperado: log com "usuário só leitura comprovado pelo servidor", C1 verde, saída 124.
5. Se o teste passar, deixar a variável em `true`. A agenda horária assume.

**Dias 1 a 5**
- 09:00: o resumo diário roda sozinho. Olhar só C1–C6 e as classes; C7 fica "período curto" até o dia 5.
- Qualquer `decision_mismatch`, `unclassified` ou `node_not_acting`: baixar o artifact, rodar com `-with-ids`, explicar o caso em `DIVERGENCIAS.md`. Uma divergência explicada e sem defeito da KAS vira nova regra de classe; um defeito real reinicia a contagem dos 5 dias depois da correção.
- Execução vermelha por erro de conexão isolado não reinicia nada; conta em C4.

**Dia 5 (veredito)**
- Run manual do `sombra-reaper-resumo` com `days` = 6 e `min_days` = 5. Verde em C1–C7 = sombra aprovada. Isso **não** ativa escrita: a troca de responsabilidade (executor) é outra etapa, com OK separado.

**Parar a qualquer momento**
```
SOMBRA_REAPER_ATIVA=false
```
A execução em andamento termina sozinha; para cortar na hora, cancelar o run. Para revogar o acesso, apagar o usuário no Atlas.

## 8. Testes isolados que sustentam o pacote

`deploy/sombra/validate.sh` sobe um Mongo 7 com autenticação ativa e o mesmo papel do Atlas, roda o reaper Node real do CRM e verifica:

| | Cenário | Resultado esperado |
|---|---|---|
| S1 | Servidor nega insert, update, escrita no lease e leitura de `patients` ao usuário da sombra | negado; leitura do `eventstore` permitida |
| S2 | Sombra com usuário readWrite ou sem autenticação | recusa (saída 2); usuário da sombra confirmado com `[find]` |
| S3 | Só a sombra rodando | dbHash igual, 0 escritas no oplog, 200 previsões |
| S4 | Reaper Node real agindo | 90 `agree`, 0 `decision_mismatch`, resumo aprovado |
| S5 | Sombra com SIGSTOP e kill -9 | Node recupera 60/60; nenhum documento de lease KAS |
| S6 | Banco pausado | ciclos com erro registrados, depois recupera; SIGTERM não gera erro falso |

Roda localmente (`CRM_DIR=<checkout do CRM com npm ci> deploy/sombra/validate.sh`) e no CI (job `sombra`).
