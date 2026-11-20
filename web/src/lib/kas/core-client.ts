import { modulesResponseSchema, type ModulesResponse } from "./contract";

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

export async function getModules(cfg: CoreConfig, tenantId: string, fetchImpl: typeof fetch = fetch): Promise<ModulesResponse> {
  const url = `${cfg.baseUrl}/internal/v1/modules?tenant_id=${encodeURIComponent(tenantId)}`;
  const res = await fetchImpl(url, {
    headers: { "X-KAS-Service": cfg.serviceId, Authorization: `Bearer ${cfg.token}`, Accept: "application/json" },
    cache: "no-store",
  });
  if (!res.ok) {
    let code = "unknown";
    try {
      code = ((await res.json()) as { error?: { code?: string } }).error?.code ?? code;
    } catch {
      /* corpo não é JSON */
    }
    throw new CoreError(res.status, code);
  }
  const parsed = modulesResponseSchema.safeParse(await res.json());
  if (!parsed.success) throw new CoreError(502, "contract_mismatch");
  if (parsed.data.tenant_id !== tenantId) throw new CoreError(502, "tenant_mismatch");
  return parsed.data;
}
