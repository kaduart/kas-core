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

/* ---------- Identidade (contract/v1/identity.md) ---------- */

export const ROLES = ["admin", "gestor", "atendimento", "leitura"] as const;
export type Role = (typeof ROLES)[number];
export const ROLE_LABEL: Record<Role, string> = {
  admin: "Administrador",
  gestor: "Gestor",
  atendimento: "Atendimento",
  leitura: "Somente leitura",
};

export const identitySessionSchema = z.object({
  contract_version: z.literal("v1"),
  session_token: z.string().optional(),
  expires_at: z.string(),
  user: z.object({ id: z.string(), email: z.string(), name: z.string() }),
  memberships: z.array(z.object({ tenant_id: z.string(), role: z.enum(ROLES) })),
});
export type IdentitySession = z.infer<typeof identitySessionSchema>;

/* ---------- Receita por campanha (GET /internal/v1/attribution/revenue, #28) ---------- */

export const REVENUE_LEVELS = ["comprovada", "provavel", "sem_vinculo"] as const;
export type RevenueLevel = (typeof REVENUE_LEVELS)[number];

const cents = z.number().int();
export const revenueLineSchema = z.object({
  nivel: z.enum(REVENUE_LEVELS),
  origem: z.string(),
  campanha: z.string().optional(),
  ad_id: z.string().optional(),
  recebido_centavos: cents,
  estornos_centavos: cents,
  liquido_centavos: cents,
  clientes: z.number().int(),
  lancamentos: z.number().int(),
  motivos_por_lancamento: z.record(z.string(), z.number().int()).nullish(),
});
export type RevenueLine = z.infer<typeof revenueLineSchema>;

export const revenueResponseSchema = z.object({
  contract_version: z.literal("v1"),
  tenant_id: z.string(),
  moeda: z.literal("BRL"),
  total_liquido_centavos: cents,
  por_nivel_centavos: z.record(z.string(), cents),
  linhas: z.array(revenueLineSchema).nullish().transform((v) => v ?? []),
  sem_vinculo: z.object({
    recebido_centavos: cents,
    estornos_centavos: cents,
    liquido_centavos: cents,
    clientes: z.number().int(),
    lancamentos: z.number().int(),
    motivos_centavos: z.record(z.string(), cents).nullish(),
  }),
  invariante: z.object({ soma_niveis_centavos: cents, total_liquido_centavos: cents, fecha: z.boolean() }),
});
export type RevenueResponse = z.infer<typeof revenueResponseSchema>;

/** Nomes legíveis das origens de anúncio que o KAS grava. */
export const SOURCE_LABEL: Record<string, string> = {
  google_ads: "Google Ads",
  meta_ads: "Meta Ads",
  tiktok_ads: "TikTok Ads",
  organic: "Orgânico",
  site: "Site",
  indicacao: "Indicação",
};
export const sourceLabel = (s: string) => SOURCE_LABEL[s] ?? s.replace(/[_-]+/g, " ").replace(/^\w/, (c) => c.toUpperCase());

/** Motivos da conciliação (sem vínculo / provável), em português simples. */
export const REASON_LABEL: Record<string, string> = {
  sinal_forte_contato_exclusivo: "clique do anúncio ligado só a este cliente",
  sinal_forte_em_contato_compartilhado: "clique do anúncio em contato compartilhado",
  so_sinal_fraco: "só campanha ou anúncio, sem clique",
  lancamento_sem_cliente: "pagamento sem cliente",
  contato_da_campanha_depois_do_recebimento: "contato da campanha depois do pagamento",
  sem_origem_capturada: "cliente sem origem registrada",
  estorno_acima_do_saldo: "estorno maior que o valor pago",
};
