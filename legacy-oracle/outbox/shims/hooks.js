// Module hooks for the oracle. The vendored files on disk are not modified
// (oracle.js checks their hashes); at load time:
//  - the dispatcher's two heavy CRM imports are redirected to the shims;
//  - the dispatcher gets a module-scoped `Date` bound to the oracle clock.
//    Only the dispatcher sees it: the MongoDB driver keeps the real clock, so
//    its heartbeats and timeouts are not disturbed by the simulated time.
const redirect = {
  '../queue/queueConfig.js': new URL('./queueConfig.js', import.meta.url).href,
  '../events/eventPublisher.js': new URL('./eventPublisher.js', import.meta.url).href
};
const isDispatcher = (url) => url && url.endsWith('/infrastructure/outbox/outboxPattern.js');

export async function resolve(specifier, context, next) {
  if (redirect[specifier] && isDispatcher(context.parentURL)) {
    return { url: redirect[specifier], shortCircuit: true };
  }
  return next(specifier, context);
}

export async function load(url, context, next) {
  const r = await next(url, context);
  if (!isDispatcher(url)) return r;
  const src = typeof r.source === 'string' ? r.source : Buffer.from(r.source).toString('utf8');
  // Same line count is kept by putting the binding on the first line.
  return { ...r, source: 'const Date = globalThis.__oracleDate; ' + src, shortCircuit: true };
}
