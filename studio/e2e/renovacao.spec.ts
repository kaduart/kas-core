import { expect, test, type Page } from '@playwright/test'

// Piloto de renovação de pacote (clínica fictícia demo-clinica):
// o editor abre o fluxo, a secretaria registra "renovou", o CRM simulado cria o
// pacote novo e PERDE a primeira confirmação (stack.sh: refs pacote-demo-perde-*),
// a KAS consulta a mesma command_key e só então a execução termina "renovado",
// com o identificador do pacote novo no comando e no histórico.
const shot = (page: Page, name: string) => page.screenshot({ path: `test-results/shots/${name}.png` })

test('renovação: confirmação perdida, KAS consulta o mesmo pedido, um pacote só', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('tenant').selectOption('demo-clinica')

  // 1. O fluxo abre no editor, com a tarefa da secretaria em 2 dias úteis.
  await expect(page.getByTestId('node-falar_com_familia')).toBeVisible()
  await expect(page.getByTestId('deadline-falar_com_familia')).toContainText('2 dias úteis')
  await expect(page.getByTestId('node-criar_pacote_novo')).toBeVisible()
  await shot(page, '10-renovacao-fluxo-aberto')

  // 2. Para a demonstração, a espera pela resposta do CRM cai para 3 s (no piloto: 1 h).
  const oldVersion = Number(await page.getByTestId('version-select').inputValue())
  await page.getByTestId('node-criar_pacote_novo').click()
  await expect(page.getByTestId('field-require-result-ref')).toBeChecked()
  await page.getByTestId('field-reply-timeout').fill('PT3S')
  await page.getByTestId('field-reply-timeout').press('Enter')
  await expect(page.getByTestId('changes')).toContainText('criar_pacote_novo.reply_timeout')
  await page.getByTestId('validate').click()
  await expect(page.getByTestId('notice')).toContainText('Definição válida')
  await page.getByTestId('publish').click()
  await expect(page.getByTestId('notice')).toContainText(`Versão ${oldVersion + 1} publicada`)

  // 3. O CRM inicia pela referência do pacote que está acabando; a secretaria registra "renovou".
  const ref = `pacote-demo-perde-${Date.now().toString(36)}`
  await page.getByTestId('tab-run').click()
  await page.getByTestId('start-ref').fill(ref)
  await page.getByTestId('start').click()
  await expect(page.getByTestId('instance-detail')).toContainText(ref)
  await page.getByTestId('resolve-falar_com_familia-renovou').click()

  // 4. Só termina quando o CRM confirma o pacote novo, depois da consulta de status.
  await expect(page.getByTestId('instance-status')).toHaveText('concluída: renovado', { timeout: 45_000 })
  const command = page.getByTestId('command-criar_pacote_novo')
  await expect(command).toContainText('confirmed')
  await expect(command).toContainText('consultas 1')
  await expect(page.getByTestId('result-ref-criar_pacote_novo')).toContainText(/pacote-novo-\d+/)
  const history = page.getByTestId('history')
  await expect(history).toContainText('Comando enviado ao CRM')
  await expect(history).toContainText('Consulta de status ao CRM')
  await expect(history).toContainText('Resultado do CRM registrado')
  await shot(page, '11-renovacao-confirmada-depois-da-consulta')
})
