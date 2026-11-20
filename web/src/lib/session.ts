import { cookies, headers } from "next/headers";
import { cache } from "react";
import type { ModulesResponse, SessionMode } from "@/lib/kas/contract";
import { CoreError, coreConfigFromEnv, getModules } from "@/lib/kas/core-client";
import { demoModules, demoModulesCookie, parseDemoModulesCookie } from "@/lib/kas/demo";
import { brandCookieName, parseBrandCookie } from "@/lib/tenant/brand-cookie";
import { findTenant, resolveTenant, TENANT_COOKIE, tenantByHost, TENANTS } from "@/lib/tenant/presets";
import type { Tenant, TenantBrand } from "@/lib/tenant/types";

/** Sessão serializável que o servidor entrega para as telas. */
export interface Session {
  mode: SessionMode;
  tenantId: string;
  /** Empresas que este usuário pode abrir. No modo real vem do login (pendente). */
  allowedTenantIds: string[];
  user: { name: string; role: string };
  modules: ModulesResponse;
  /** Marcas personalizadas por empresa (modo demonstração). */
  brands: Record<string, TenantBrand>;
}

/**
 * Resolve a sessão de cada requisição.
 * - Modo real (KAS_API_URL, KAS_SERVICE_ID e KAS_SERVICE_TOKEN definidos): empresa pelo domínio,
 *   módulos do KAS Core. Cookie de troca de empresa e liga/desliga são ignorados.
 * - Modo demonstração (sem essas variáveis): empresas e módulos fictícios, controláveis na tela.
 */
export const getSession = cache(async (): Promise<Session> => {
  const [jar, h] = await Promise.all([cookies(), headers()]);
  const cfg = coreConfigFromEnv();
  const mode: SessionMode = cfg ? "live" : "demo";

  const tenant = mode === "demo" ? resolveTenant({ cookie: jar.get(TENANT_COOKIE)?.value, host: h.get("host") }) : tenantByHost(h.get("host"));
  if (!tenant) throw new CoreError(404, "unknown_domain");

  const brands: Record<string, TenantBrand> = {};
  for (const t of TENANTS) {
    const b = parseBrandCookie(jar.get(brandCookieName(t.id))?.value);
    if (b) brands[t.id] = b;
  }

  const modules = cfg ? await getModules(cfg, tenant.id) : demoModules(tenant, parseDemoModulesCookie(jar.get(demoModulesCookie(tenant.id))?.value));

  return {
    mode,
    tenantId: tenant.id,
    allowedTenantIds: mode === "demo" ? TENANTS.map((t) => t.id) : [tenant.id],
    user: { name: "Ricardo Maia", role: mode === "demo" ? "Administrador (demonstração)" : "Administrador" },
    modules,
    brands: mode === "demo" ? brands : {},
  };
});

export function sessionTenant(s: Session): Tenant {
  return findTenant(s.tenantId) ?? TENANTS[0];
}
