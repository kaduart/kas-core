// Imprime o mapa evento -> filas do dispatcher do CRM (eventToQueueMap de
// infrastructure/events/eventPublisher.js), normalizado e ordenado, em JSON.
// Uso: CRM_DIR=../fono-inova-crm-back node deploy/sombra-outbox/mapa-filas.mjs
// A sombra do Outbox embute este mapa (internal/legacy/crmoutbox/queues.json);
// a CI compara o embutido com o do CRM para o "destino proposto" não envelhecer.
import { pathToFileURL } from 'node:url';
import { join } from 'node:path';

const crm = process.env.CRM_DIR;
if (!crm) { console.error('CRM_DIR é obrigatório'); process.exit(2); }
process.env.NODE_ENV = 'test';
// O módulo cria a conexão Redis de forma preguiçosa; nada é publicado aqui.
const log = console.log; console.log = console.warn = console.info = () => {};
const m = await import(pathToFileURL(join(crm, 'infrastructure', 'events', 'eventPublisher.js')).href);
const out = {};
for (const k of Object.keys(m.eventToQueueMap).sort()) {
  const v = m.eventToQueueMap[k];
  out[k] = Array.isArray(v) ? [...v] : [v];
}
log(JSON.stringify(out, null, 2));
process.exit(0);
