/** Simulação do catálogo do KAS Core para o modo demonstração. Puro: serve no servidor, no navegador e nos testes. */
import { CONTRACT_STATES, MODULE_KEYS, type ContractState, type ModuleKey, type ModulesResponse } from "./contract";
import type { Tenant } from "@/lib/tenant/types";

export const demoModulesCookie = (tenantId: string) => `kas_demo_modules_${tenantId}`;

const PLANNED: ModuleKey[] = ["crm", "omnichannel", "ai"];

export function parseDemoModulesCookie(raw: string | undefined | null): Partial<Record<ModuleKey, ContractState>> {
  if (!raw) return {};
  try {
    const v = JSON.parse(decodeURIComponent(raw)) as Record<string, unknown>;
    const out: Partial<Record<ModuleKey, ContractState>> = {};
    for (const k of MODULE_KEYS) {
      const st = v[k];
      if (typeof st === "string" && (CONTRACT_STATES as readonly string[]).includes(st) && st !== "planned" && !PLANNED.includes(k)) out[k] = st as ContractState;
    }
    return out;
  } catch {
    return {};
  }
}

/** Monta, no modo demonstração, a mesma resposta que GET /internal/v1/modules daria. */
export function demoModules(tenant: Tenant, override: Partial<Record<ModuleKey, ContractState>> = {}): ModulesResponse {
  return {
    contract_version: "v1",
    tenant_id: tenant.id,
    config_version: "demo",
    modules: MODULE_KEYS.map((key) => {
      const contract: ContractState = PLANNED.includes(key) ? "planned" : (override[key] ?? tenant.demoModules[key] ?? "absent");
      const missing = tenant.demoMissing?.[key];
      return {
        key,
        contract,
        ...(key === "reports" ? { depends_on: ["attribution"] } : {}),
        ...(contract === "active" && key === "attribution" ? { integration: { ready: !missing?.length, missing: missing ?? [] } } : {}),
      };
    }),
  };
}

