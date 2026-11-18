// Stand-in for the CRM utils/logger.js: silent, same shape.
const noop = () => {};
export function createContextLogger() {
  return { info: noop, warn: noop, error: noop, debug: noop };
}
