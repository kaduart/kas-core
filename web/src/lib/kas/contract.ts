/**
 * Espelho do contrato v1 do catálogo de módulos do KAS Core
 * (contract/v1/modules.md e GET /internal/v1/modules, PR kas-core #26).
 * O front só representa estas decisões; quem as aplica são as APIs do Go.
 */
import { z } from "zod";

export const MODULE_KEYS = ["attribution", "reports", "process", "crm", "omnichannel", "ai"] as const;
export type ModuleKey = (typeof MODULE_KEYS)[number];

export const CONTRACT_STATES = ["active", "paused", "absent", "planned"] as const;
export type ContractState = (typeof CONTRACT_STATES)[number];

/** Módulos que o KAS Core permite contratar hoje. crm, omnichannel e ai estão "planned" no catálogo do Go. */
export const ACTIVATABLE: ModuleKey[] = ["attribution", "reports", "process"];
export const DEPENDS_ON: Partial<Record<ModuleKey, ModuleKey[]>> = { reports: ["attribution"] };

export const moduleStateSchema = z.object({
  key: z.enum(MODULE_KEYS),
  contract: z.enum(CONTRACT_STATES),
  depends_on: z.array(z.string()).optional(),
  integration: z.object({ ready: z.boolean(), missing: z.array(z.string()) }).optional(),
  caller_capabilities: z.array(z.string()).optional(),
});
export type ModuleState = z.infer<typeof moduleStateSchema>;

export const modulesResponseSchema = z.object({
  contract_version: z.literal("v1"),
  tenant_id: z.string(),
  config_version: z.string(),
  modules: z.array(moduleStateSchema),
});
export type ModulesResponse = z.infer<typeof modulesResponseSchema>;

export type SessionMode = "demo" | "live";

/** O que a tela pode fazer com um módulo, derivado do contrato. */
export type ModuleAccess =
  | { allowed: true; preview?: false; missing?: string[] }
  | { allowed: true; preview: true }
  | { allowed: false; reason: "not_contracted" | "paused" | "planned" };

export function moduleAccess(mode: SessionMode, modules: ModuleState[], key: ModuleKey): ModuleAccess {
  const m = modules.find((x) => x.key === key);
  if (!m || m.contract === "absent") return { allowed: false, reason: "not_contracted" };
  if (m.contract === "paused") return { allowed: false, reason: "paused" };
  if (m.contract === "planned") return mode === "demo" ? { allowed: true, preview: true } : { allowed: false, reason: "planned" };
  if (m.integration && !m.integration.ready) return { allowed: true, missing: m.integration.missing };
  return { allowed: true };
}

/** Nomes legíveis das pendências de integração que o Go devolve em `integration.missing`. */
export const MISSING_LABEL: Record<string, string> = {
  receipts: "recebimentos (pagamentos)",
  ad_spend: "investimento em anúncios",
  leads: "leads",
};
