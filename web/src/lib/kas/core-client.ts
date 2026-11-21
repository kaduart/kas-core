import {
  agendaCatalogSchema,
  bookingWriteResponseSchema,
  bookingsResponseSchema,
  identitySessionSchema,
  modulesResponseSchema,
  revenueResponseSchema,
  slotsResponseSchema,
  type AgendaBooking,
  type AgendaCatalog,
  type AgendaSlot,
  type BookingWriteResponse,
  type IdentitySession,
  type ModulesResponse,
  type RevenueResponse,
} from "./contract";

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

const SESSION_HEADER = "X-KAS-Session";

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

/**
 * O painel é um serviço que faz login de pessoas: o Go exige a sessão da pessoa em toda
 * leitura de dados e só responde pelas empresas dela (o tenant_id sozinho não abre nada).
 */
export async function getModules(cfg: CoreConfig, tenantId: string, sessionToken: string, fetchImpl: FetchImpl = fetch): Promise<ModulesResponse> {
  const res = await coreFetch(cfg, `/internal/v1/modules?tenant_id=${encodeURIComponent(tenantId)}`, { headers: { [SESSION_HEADER]: sessionToken } }, fetchImpl);
  const parsed = modulesResponseSchema.safeParse(await res.json());
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  if (parsed.data.tenant_id !== tenantId) throw new CoreError(502, "tenant_mismatch");
  return parsed.data;
}

/* ---------- Identidade: a pessoa entra no painel; o Go guarda usuário, senha e sessão ---------- */

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

export async function getRevenue(cfg: CoreConfig, tenantId: string, sessionToken: string, q: RevenueQuery = {}, fetchImpl: FetchImpl = fetch): Promise<RevenueResponse> {
  const res = await coreFetch(cfg, `/internal/v1/attribution/revenue?${revenueParams(tenantId, q)}`, { headers: { [SESSION_HEADER]: sessionToken } }, fetchImpl);
  const parsed = revenueResponseSchema.safeParse(await res.json());
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  if (parsed.data.tenant_id !== tenantId) throw new CoreError(502, "tenant_mismatch");
  return parsed.data;
}

export type RevenueFile = "attribution-csv" | "report-csv" | "report-html";

/** Arquivo pronto do Go (CSV da receita ou relatório HTML com a marca da empresa), repassado como veio. */
export async function getRevenueFile(cfg: CoreConfig, tenantId: string, sessionToken: string, kind: RevenueFile, q: RevenueQuery = {}, fetchImpl: FetchImpl = fetch): Promise<Response> {
  const path = kind === "attribution-csv" ? "/internal/v1/attribution/revenue" : "/internal/v1/reports/revenue";
  const format = kind === "report-html" ? "html" : "csv";
  return coreFetch(cfg, `${path}?${revenueParams(tenantId, q, format)}`, { headers: { Accept: "*/*", [SESSION_HEADER]: sessionToken } }, fetchImpl);
}

/* ---------- Agenda (fases 1 e 4A). A empresa vem da sessão; o Go confere papel e trava de agenda espelhada ---------- */

function parseFor<T>(schema: { safeParse(v: unknown): { success: true; data: T } | { success: false } }, body: unknown, tenantId: string): T {
  const parsed = schema.safeParse(body);
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  if ((parsed.data as { tenant_id: string }).tenant_id !== tenantId) throw new CoreError(502, "tenant_mismatch");
  return parsed.data;
}

const sessionHeaders = (sessionToken: string, json = false): Record<string, string> =>
  json ? { [SESSION_HEADER]: sessionToken, "Content-Type": "application/json" } : { [SESSION_HEADER]: sessionToken };

export async function getAgendaCatalog(cfg: CoreConfig, tenantId: string, sessionToken: string, fetchImpl: FetchImpl = fetch): Promise<AgendaCatalog> {
  const res = await coreFetch(cfg, `/internal/v1/scheduling/catalog?tenant_id=${encodeURIComponent(tenantId)}`, { headers: sessionHeaders(sessionToken) }, fetchImpl);
  return parseFor(agendaCatalogSchema, await res.json(), tenantId);
}

/** Agendamentos que tocam [from, to) (RFC 3339), de um recurso ou de todos. */
export async function listAgendaBookings(
  cfg: CoreConfig,
  tenantId: string,
  sessionToken: string,
  q: { resourceId?: string; from: string; to: string },
  fetchImpl: FetchImpl = fetch,
): Promise<AgendaBooking[]> {
  const p = new URLSearchParams({ tenant_id: tenantId, from: q.from, to: q.to });
  if (q.resourceId) p.set("resource_id", q.resourceId);
  const res = await coreFetch(cfg, `/internal/v1/scheduling/bookings?${p}`, { headers: sessionHeaders(sessionToken) }, fetchImpl);
  return parseFor(bookingsResponseSchema, await res.json(), tenantId).bookings;
}

/** Horários livres entre duas datas locais (YYYY-MM-DD, inclusivas), por serviço ou duração. */
export async function getAgendaSlots(
  cfg: CoreConfig,
  tenantId: string,
  sessionToken: string,
  q: { resourceId: string; from: string; to: string; serviceId?: string; durationMin?: number },
  fetchImpl: FetchImpl = fetch,
): Promise<AgendaSlot[]> {
  const p = new URLSearchParams({ tenant_id: tenantId, resource_id: q.resourceId, from: q.from, to: q.to });
  if (q.serviceId) p.set("service_id", q.serviceId);
  else p.set("duration_min", String(q.durationMin ?? 40));
  const res = await coreFetch(cfg, `/internal/v1/scheduling/availability?${p}`, { headers: sessionHeaders(sessionToken) }, fetchImpl);
  return parseFor(slotsResponseSchema, await res.json(), tenantId).slots;
}

export interface CreateBookingInput {
  idempotencyKey: string;
  resourceId: string;
  clientRef: string;
  serviceId?: string;
  startsAt: string;
  endsAt: string;
  status?: "tentative" | "scheduled" | "confirmed";
}

export async function createAgendaBooking(cfg: CoreConfig, tenantId: string, sessionToken: string, b: CreateBookingInput, fetchImpl: FetchImpl = fetch): Promise<BookingWriteResponse> {
  const body = {
    contract_version: "v1",
    tenant_id: tenantId,
    idempotency_key: b.idempotencyKey,
    resource_id: b.resourceId,
    client_ref: b.clientRef,
    ...(b.serviceId ? { service_id: b.serviceId } : {}),
    starts_at: b.startsAt,
    ends_at: b.endsAt,
    ...(b.status ? { status: b.status } : {}),
  };
  const res = await coreFetch(cfg, "/internal/v1/scheduling/bookings", { method: "POST", headers: sessionHeaders(sessionToken, true), body: JSON.stringify(body) }, fetchImpl);
  return parseFor(bookingWriteResponseSchema, await res.json(), tenantId);
}

export async function cancelAgendaBooking(cfg: CoreConfig, tenantId: string, sessionToken: string, id: string, expectedVersion: number, fetchImpl: FetchImpl = fetch): Promise<BookingWriteResponse> {
  const res = await coreFetch(
    cfg,
    `/internal/v1/scheduling/bookings/${encodeURIComponent(id)}/cancel`,
    { method: "POST", headers: sessionHeaders(sessionToken, true), body: JSON.stringify({ contract_version: "v1", tenant_id: tenantId, expected_version: expectedVersion }) },
    fetchImpl,
  );
  return parseFor(bookingWriteResponseSchema, await res.json(), tenantId);
}

export async function rescheduleAgendaBooking(
  cfg: CoreConfig,
  tenantId: string,
  sessionToken: string,
  id: string,
  m: { expectedVersion: number; startsAt: string; endsAt: string },
  fetchImpl: FetchImpl = fetch,
): Promise<BookingWriteResponse> {
  const res = await coreFetch(
    cfg,
    `/internal/v1/scheduling/bookings/${encodeURIComponent(id)}/reschedule`,
    {
      method: "POST",
      headers: sessionHeaders(sessionToken, true),
      body: JSON.stringify({ contract_version: "v1", tenant_id: tenantId, expected_version: m.expectedVersion, starts_at: m.startsAt, ends_at: m.endsAt }),
    },
    fetchImpl,
  );
  return parseFor(bookingWriteResponseSchema, await res.json(), tenantId);
}
