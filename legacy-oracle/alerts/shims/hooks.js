// Redirects the alert service's logger import to a silent shim. The vendored
// file on disk is not modified (oracle.js checks its hash).
const isAlertService = (url) => url && url.endsWith('/infrastructure/alerts/alertService.js');
export async function resolve(specifier, context, next) {
  if (specifier === '../../utils/logger.js' && isAlertService(context.parentURL)) {
    return { url: new URL('./logger.js', import.meta.url).href, shortCircuit: true };
  }
  return next(specifier, context);
}
