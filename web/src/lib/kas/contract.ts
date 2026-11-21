/**
 * Espelho do contrato v1 do catálogo de módulos do KAS Core
 * (contract/v1/modules.md e GET /internal/v1/modules, PR kas-core #26).
 * O front só representa estas decisões; quem as aplica são as APIs do Go.
 */
import { z } from "zod";

export const MODULE_KEYS = ["scheduling", "attribution", "reports", "process", "crm", "omnichannel", "ai"] as const;
export type ModuleKey = (typeof MODULE_KEYS)[number];

export const CONTRACT_STATES = ["active", "paused", "absent", "planned"] as const;
export type ContractState = (typeof CONTRACT_STATES)[number];

/** Módulos que o KAS Core permite contratar hoje. crm, omnichannel e ai estão "planned" no catálogo do Go. */
export const ACTIVATABLE: ModuleKey[] = ["scheduling", "attribution", "reports", "process"];
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

/* ---------- Agenda (GET/POST /internal/v1/scheduling/*, fases 1 e 4A) ---------- */

export const BOOKING_STATUSES = ["tentative", "scheduled", "confirmed", "completed", "canceled", "no_show"] as const;
export type BookingStatus = (typeof BOOKING_STATUSES)[number];
export const ACTIVE_STATUSES: BookingStatus[] = ["tentative", "scheduled", "confirmed"];
export const STATUS_LABEL: Record<BookingStatus, string> = {
  tentative: "Pré-agendado",
  scheduled: "Agendado",
  confirmed: "Confirmado",
  completed: "Realizado",
  canceled: "Cancelado",
  no_show: "Faltou",
};

/** Papéis que podem mudar a agenda. O Go confere de novo (403 role_forbidden). */
export const WRITER_ROLES: Role[] = ["admin", "gestor", "atendimento"];

/** Código opaco: sem espaço nem @, o que mantém nome e e-mail fora por construção (mesma regra do Go). */
export const REF_PATTERN = /^[A-Za-z0-9._:-]{1,128}$/;

export const resourceSchema = z.object({
  id: z.string(),
  kind: z.string(),
  label: z.string(),
  tags: z.array(z.string()).nullish(),
  capacity: z.number().int(),
  active: z.boolean(),
});
export type AgendaResource = z.infer<typeof resourceSchema>;

export const serviceSchema = z.object({ id: z.string(), label: z.string(), duration_min: z.number().int(), buffer_min: z.number().int() });
export type AgendaService = z.infer<typeof serviceSchema>;

export const agendaCatalogSchema = z.object({
  contract_version: z.literal("v1"),
  tenant_id: z.string(),
  zone: z.string(),
  read_only: z.boolean(),
  resources: z.array(resourceSchema),
  services: z.array(serviceSchema),
});
export type AgendaCatalog = z.infer<typeof agendaCatalogSchema>;

export const bookingSchema = z.object({
  id: z.string(),
  resource_id: z.string(),
  client_ref: z.string(),
  service_id: z.string().optional(),
  starts_at: z.string(),
  ends_at: z.string(),
  status: z.enum(BOOKING_STATUSES),
  buffer_min: z.number().int(),
  blocked_until: z.string(),
  version: z.number().int(),
  updated_at: z.string(),
});
export type AgendaBooking = z.infer<typeof bookingSchema>;

export const bookingsResponseSchema = z.object({
  contract_version: z.literal("v1"),
  tenant_id: z.string(),
  bookings: z.array(bookingSchema).nullish().transform((v) => v ?? []),
});

export const slotsResponseSchema = z.object({
  contract_version: z.literal("v1"),
  tenant_id: z.string(),
  resource_id: z.string(),
  slots: z
    .array(z.object({ starts_at: z.string(), ends_at: z.string(), remaining: z.number().int() }))
    .nullish()
    .transform((v) => v ?? []),
});
export type AgendaSlot = z.infer<typeof slotsResponseSchema>["slots"][number];

export const bookingWriteResponseSchema = z.object({
  contract_version: z.literal("v1"),
  tenant_id: z.string(),
  booking: bookingSchema,
  replayed: z.boolean().optional(),
});
export type BookingWriteResponse = z.infer<typeof bookingWriteResponseSchema>;
