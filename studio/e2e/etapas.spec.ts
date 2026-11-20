import { expect, test, type Page } from '@playwright/test'

// Criar e apagar etapas no editor: a etapa nova nasce com o formato mínimo do
// motor, é ligada pelo painel, valida e publica; apagar limpa os caminhos que
// apontavam para ela e o Validar mostra o que ficou faltando. Empresa B, dados
// fictícios.
const shot = (page: Page, name: string) => page.screenshot({ path: `test-results/shots/${name}.png` })

test('criar etapa, ligar, publicar; apagar etapa limpa os caminhos', async ({ page }) => {
  await page.goto('/')
  await page.getByTestId('tenant').selectOption('demo-empresa-b')
  await expect(page.getByTestId('node-negociar')).toBeVisible()
  const oldVersion = Number(await page.getByTestId('version-select').inputValue())
  const name = `fim_arquivado_${Date.now().toString(36)}`

  // 1. Criar uma etapa de fim ao lado de pedir_cancelamento.
  await page.getByTestId('node-pedir_cancelamento').click()
  await page.getByTestId('add-step').click()
  await page.getByTestId('new-step-name').fill('negociar')
  await expect(page.getByTestId('new-step-problem')).toContainText('Já existe')
  await expect(page.getByTestId('new-step-create')).toBeDisabled()
  await page.getByTestId('new-step-name').fill(name)
  await page.getByTestId('new-step-type').selectOption('end')
  await page.getByTestId('new-step-create').click()
  await expect(page.getByTestId(`node-${name}`)).toBeVisible()
  await expect(page.getByTestId('changes')).toContainText(`${name}.type`)

  // Sem ninguém apontando para ela, o motor recusa (etapa inalcançável).
  await page.getByTestId('validate').click()
  await expect(page.getByTestId('notice')).toContainText('Definição inválida')
  await expect(page.getByTestId('notice')).toContainText(`${name} is unreachable`)

  // 2. Ligar: sem resposta do CRM vai para a etapa nova.
  await page.getByTestId('node-pedir_cancelamento').click()
  await page.getByTestId('field-on_no_reply').selectOption(name)
  await page.getByTestId('validate').click()
  await expect(page.getByTestId('notice')).toContainText('Definição válida')
  await shot(page, '10-etapa-criada-ligada')
  await page.getByTestId('publish').click()
  await expect(page.getByTestId('notice')).toContainText(`Versão ${oldVersion + 1} publicada`)
  await expect(page.getByTestId(`node-${name}`)).toBeVisible()

  // 3. A etapa de início não pode ser apagada.
  await page.getByTestId('node-negociar').click()
  await expect(page.getByTestId('remove-blocked')).toBeVisible()
  await expect(page.getByTestId('remove-step')).toHaveCount(0)

  // 4. Apagar a etapa nova: o editor avisa quem aponta para ela antes de apagar.
  await page.getByTestId(`node-${name}`).click()
  await page.getByTestId('remove-step').click()
  await expect(page.getByTestId('remove-confirm')).toContainText('pedir_cancelamento.on_no_reply')
  await page.getByTestId('remove-step-confirm').click()
  await expect(page.getByTestId(`node-${name}`)).toHaveCount(0)
  await expect(page.getByTestId('notice')).toContainText(`Etapa ${name} apagada`)
  await expect(page.getByTestId('notice')).toContainText('pedir_cancelamento.on_no_reply')
  await page.getByTestId('validate').click()
  await expect(page.getByTestId('notice')).toContainText('on_no_reply is required')
  await shot(page, '11-etapa-apagada-validar')

  // 5. Descartar volta ao publicado.
  await page.getByTestId('discard').click()
  await expect(page.getByTestId(`node-${name}`)).toBeVisible()
  await expect(page.getByTestId('changes')).toContainText('Nenhuma')
})
