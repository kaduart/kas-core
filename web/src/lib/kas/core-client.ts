import { identitySessionSchema, modulesResponseSchema, revenueResponseSchema, type IdentitySession, type ModulesResponse, type RevenueResponse } from "./contract";

/**
 * Cliente do KAS Core usado SÓ no servidor do Next.js (BFF). As credenciais do
 * serviço nunca vão para o navegador. Segue o contrato D4: a KAS autentica
 * sistemas; o Next.js é o sistema que fala com ela e registra a pessoa no ator.
 */
export interface CoreConfig {
  baseUrl: string;
  serviceId: string;
  token: string;
}

export function coreConfigFromEnv(env: NodeJS.ProcessEnv = process.env): CoreConfig | null {
  const baseUrl = env.KAS_API_URL?.trim();
  const serviceId = env.KAS_SERVICE_ID?.trim();
  const token = env.KAS_SERVICE_TOKEN?.trim();
  if (!baseUrl || !serviceId || !token) return null;
  return { baseUrl: baseUrl.replace(/\/+$/, ""), serviceId, token };
}

export class CoreError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`KAS Core respondeu ${status} ${code}`);
  }
}

type FetchImpl = typeof fetch;

async function errorCode(res: Response): Promise<string> {
  try {
    return ((await res.json()) as { error?: { code?: string } }).error?.code ?? "unknown";
  } catch {
    return "unknown";
  }
}

/** Chamada autenticada como serviço. Erros de rede viram CoreError(503, "core_unreachable"). */
async function coreFetch(cfg: CoreConfig, path: string, init: RequestInit = {}, fetchImpl: FetchImpl = fetch): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("X-KAS-Service", cfg.serviceId);
  headers.set("Authorization", `Bearer ${cfg.token}`);
  if (!headers.has("Accept")) headers.set("Accept", "application/json");
  let res: Response;
  try {
    res = await fetchImpl(`${cfg.baseUrl}${path}`, { ...init, headers, cache: "no-store", signal: init.signal ?? AbortSignal.timeout(10_000) });
  } catch {
    throw new CoreError(503, "core_unreachable");
  }
  if (!res.ok) throw new CoreError(res.status, await errorCode(res));
  return res;
}

export async function getModules(cfg: CoreConfig, tenantId: string, fetchImpl: FetchImpl = fetch): Promise<ModulesResponse> {
  const res = await coreFetch(cfg, `/internal/v1/modules?tenant_id=${encodeURIComponent(tenantId)}`, {}, fetchImpl);
  const parsed = modulesResponseSchema.safeParse(await res.json());
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  if (parsed.data.tenant_id !== tenantId) throw new CoreError(502, "tenant_mismatch");
  return parsed.data;
}

/* ---------- Identidade: a pessoa entra no painel; o Go guarda usuário, senha e sessão ---------- */

const SESSION_HEADER = "X-KAS-Session";

function parseIdentity(body: unknown): IdentitySession {
  const parsed = identitySessionSchema.safeParse(body);
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  return parsed.data;
}

export async function login(cfg: CoreConfig, email: string, password: string, fetchImpl: FetchImpl = fetch): Promise<IdentitySession & { session_token: string }> {
  const res = await coreFetch(
    cfg,
    "/internal/v1/identity/sessions",
    { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ contract_version: "v1", email, password }) },
    fetchImpl,
  );
  const s = parseIdentity(await res.json());
  if (!s.session_token) throw new CoreError(502, "contract_mismatch");
  return { ...s, session_token: s.session_token };
}

export async function getIdentitySession(cfg: CoreConfig, sessionToken: string, fetchImpl: FetchImpl = fetch): Promise<IdentitySession> {
  const res = await coreFetch(cfg, "/internal/v1/identity/session", { headers: { [SESSION_HEADER]: sessionToken } }, fetchImpl);
  return parseIdentity(await res.json());
}

export async function logout(cfg: CoreConfig, sessionToken: string, fetchImpl: FetchImpl = fetch): Promise<void> {
  await coreFetch(cfg, "/internal/v1/identity/session", { method: "DELETE", headers: { [SESSION_HEADER]: sessionToken } }, fetchImpl);
}

/* ---------- Receita (Attribution #28) e relatórios (Reports) ---------- */

export interface RevenueQuery {
  from?: string;
  to?: string;
  unit?: string;
}

function revenueParams(tenantId: string, q: RevenueQuery, format?: string): string {
  const p = new URLSearchParams({ tenant_id: tenantId });
  if (q.from) p.set("from", q.from);
  if (q.to) p.set("to", q.to);
  if (q.unit) p.set("unit", q.unit);
  if (format) p.set("format", format);
  return p.toString();
}

export async function getRevenue(cfg: CoreConfig, tenantId: string, q: RevenueQuery = {}, fetchImpl: FetchImpl = fetch): Promise<RevenueResponse> {
  const res = await coreFetch(cfg, `/internal/v1/attribution/revenue?${revenueParams(tenantId, q)}`, {}, fetchImpl);
  const parsed = revenueResponseSchema.safeParse(await res.json());
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  if (parsed.data.tenant_id !== tenantId) throw new CoreError(502, "tenant_mismatch");
  return parsed.data;
}

export type RevenueFile = "attribution-csv" | "report-csv" | "report-html";

/** Arquivo pronto do Go (CSV da receita ou relatório HTML com a marca da empresa), repassado como veio. */
export async function getRevenueFile(cfg: CoreConfig, tenantId: string, kind: RevenueFile, q: RevenueQuery = {}, fetchImpl: FetchImpl = fetch): Promise<Response> {
  const path = kind === "attribution-csv" ? "/internal/v1/attribution/revenue" : "/internal/v1/reports/revenue";
  const format = kind === "report-html" ? "html" : "csv";
  return coreFetch(cfg, `${path}?${revenueParams(tenantId, q, format)}`, { headers: { Accept: "*/*" } }, fetchImpl);
}
