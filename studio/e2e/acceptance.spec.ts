import { expect, test, type Page } from '@playwright/test'

// Critério de aceite do piloto do editor:
// abrir o fluxo de retenção, mudar o prazo de uma empresa, validar e publicar
// a nova versão, e acompanhar uma execução com o CRM simulado. Uma instância
// iniciada antes da mudança mantém o prazo original.
const shot = (page: Page, name: string) => page.screenshot({ path: `test-results/shots/${name}.png` })

async function startExecution(page: Page, ref: string) {
  await page.getByTestId('tab-run').click()
  await page.getByTestId('start-ref').fill(ref)
  await page.getByTestId('start').click()
  await expect(page.getByTestId('instance-detail')).toContainText(ref)
}

test('editar prazo, publicar e acompanhar execução sem mudar instância antiga', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('tenant').selectOption('demo-empresa-a')

  // 1. Abrir o fluxo de retenção da empresa A (na stack nova: versão 1, 2 dias úteis).
  await expect(page.getByTestId('node-negociar')).toBeVisible()
  const deadlineText = (await page.getByTestId('deadline-negociar').textContent())!
  const days = Number(/(\d+) dias? út/.exec(deadlineText)![1])
  const oldVersion = Number(await page.getByTestId('version-select').inputValue())
  const newDays = days + 1
  const run = Date.now().toString(36)
  const before = `contrato-antes-${run}`
  const after = `contrato-depois-${run}`
  await shot(page, '01-fluxo-aberto')

  // 2. Uma execução iniciada ANTES da mudança.
  await startExecution(page, before)
  await expect(page.getByTestId('instance-version')).toHaveText(`versão ${oldVersion}`)
  const dueBefore = (await page.getByTestId('due-negociar').textContent())!.trim()

  // 3. Mudar o prazo da empresa A para 3 dias úteis, validar e publicar.
  await page.getByTestId('tab-design').click()
  await page.getByTestId('node-negociar').click()
  await page.getByTestId('deadline-days').fill(String(newDays))
  await page.getByTestId('deadline-days').press('Enter')
  await expect(page.getByTestId('changes')).toContainText('negociar.deadline.business_days')
  await expect(page.getByTestId('deadline-negociar')).toContainText(`${newDays} dias úteis`)
  // Arrastar uma etapa também conta como alteração do desenho.
  const box = (await page.getByTestId('node-revisar').boundingBox())!
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
  await page.mouse.down()
  await page.mouse.move(box.x + box.width / 2 + 40, box.y + box.height / 2 + 60, { steps: 8 })
  await page.mouse.up()
  await expect(page.getByTestId('changes')).toContainText('posições no desenho')
  await expect(page.getByTestId('deadline-negociar')).toContainText(`${newDays} dias úteis`)
  await expect(page.getByTestId('publish')).toBeDisabled() // só depois de validar
  await page.getByTestId('validate').click()
  await expect(page.getByTestId('notice')).toContainText('Definição válida')
  await shot(page, '02-prazo-alterado-validado')
  await page.getByTestId('publish').click()
  await expect(page.getByTestId('notice')).toContainText(`Versão ${oldVersion + 1} publicada`)
  await expect(page.getByTestId('version-select')).toHaveValue(String(oldVersion + 1))

  // A empresa B não muda.
  await page.getByTestId('tenant').selectOption('demo-empresa-b')
  await expect(page.getByTestId('deadline-negociar')).toContainText('5 dias úteis')
  await page.getByTestId('tenant').selectOption('demo-empresa-a')
  await expect(page.getByTestId('deadline-negociar')).toContainText(`${newDays} dias úteis`)

  // 4. Execução nova usa a versão 2; a antiga continua na 1 com o prazo original.
  await startExecution(page, after)
  await expect(page.getByTestId('instance-version')).toHaveText(`versão ${oldVersion + 1}`)
  const dueAfter = (await page.getByTestId('due-negociar').textContent())!.trim()
  expect(dueAfter).not.toBe(dueBefore)
  await page.getByTestId(`instance-${before}`).click()
  await expect(page.getByTestId('instance-version')).toHaveText(`versão ${oldVersion}`)
  await expect(page.getByTestId('due-negociar')).toHaveText(dueBefore)
  await shot(page, '03-instancia-antiga-prazo-original')

  // 5. Acompanhar a execução nova até o CRM simulado confirmar o cancelamento.
  await page.getByTestId(`instance-${after}`).click()
  await expect(page.getByTestId('instance-version')).toHaveText(`versão ${oldVersion + 1}`)
  await page.getByTestId('resolve-negociar-cliente_cancela').click()
  await expect(page.getByTestId('instance-status')).toHaveText('concluída: cancelado', { timeout: 30_000 })
  await expect(page.getByTestId('command-pedir_cancelamento')).toContainText('confirmed')
  await expect(page.getByTestId('history')).toContainText('Comando enviado ao CRM')
  await shot(page, '04-execucao-concluida-pelo-crm-simulado')
})
