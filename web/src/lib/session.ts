import { cookies, headers } from "next/headers";
import { redirect } from "next/navigation";
import { cache } from "react";
import { ROLE_LABEL, type ModulesResponse, type Role, type SessionMode } from "@/lib/kas/contract";
import { CoreError, coreConfigFromEnv, getIdentitySession, getModules } from "@/lib/kas/core-client";
import { demoModules, demoModulesCookie, parseDemoModulesCookie } from "@/lib/kas/demo";
import { brandCookieName, parseBrandCookie } from "@/lib/tenant/brand-cookie";
import { isLocalHost, resolveTenant, TENANT_COOKIE, tenantByHost, tenantFromId, TENANTS } from "@/lib/tenant/presets";
import type { Tenant, TenantBrand } from "@/lib/tenant/types";

/** Cookie httpOnly com o token de sessão emitido pelo KAS Core. O navegador nunca vê a credencial do serviço. */
export const SESSION_COOKIE = "kas_session";

/** Sessão serializável que o servidor entrega para as telas. */
export interface Session {
  mode: SessionMode;
  tenantId: string;
  /** Empresas que esta pessoa pode abrir (no modo real, as do login). */
  allowedTenantIds: string[];
  /** role é o rótulo exibido; roleKey é o papel do contrato (só no modo real). */
  user: { name: string; role: string; roleKey?: Role; email?: string };
  modules: ModulesResponse;
  /** Marcas personalizadas por empresa (modo demonstração). */
  brands: Record<string, TenantBrand>;
}

/** Só no servidor: o token da sessão, que o Go exige em toda leitura de dados. Nunca vai para o navegador. */
export async function sessionToken(): Promise<string | undefined> {
  return (await cookies()).get(SESSION_COOKIE)?.value;
}

/**
 * Escolhe a empresa do modo real entre as que a pessoa tem acesso.
 * Domínio próprio de uma empresa manda; em localhost vale a escolha salva (cookie) ou a primeira.
 * Retorna null se o domínio é de uma empresa a que a pessoa não tem acesso.
 */
export function pickLiveTenant(allowed: string[], host: string | null, cookie: string | undefined): string | null {
  if (allowed.length === 0) return null;
  if (!isLocalHost(host)) {
    const byHost = tenantByHost(host);
    if (byHost) return allowed.includes(byHost.id) ? byHost.id : null;
  }
  if (cookie && allowed.includes(cookie)) return cookie;
  return allowed[0];
}

/**
 * Resolve a sessão de cada requisição.
 * - Modo real (KAS_API_URL, KAS_SERVICE_ID e KAS_SERVICE_TOKEN definidos): a pessoa entra com
 *   e-mail e senha guardados no KAS Core; empresas e papel vêm do login; módulos do catálogo do Go.
 *   Sem sessão válida devolve null e o painel manda para /login.
 * - Modo demonstração (sem essas variáveis): empresas e módulos fictícios, sem login.
 */
export const getSession = cache(async (): Promise<Session | null> => {
  const [jar, h] = await Promise.all([cookies(), headers()]);
  const cfg = coreConfigFromEnv();

  if (!cfg) {
    const tenant = resolveTenant({ cookie: jar.get(TENANT_COOKIE)?.value, host: h.get("host") });
    const brands: Record<string, TenantBrand> = {};
    for (const t of TENANTS) {
      const b = parseBrandCookie(jar.get(brandCookieName(t.id))?.value);
      if (b) brands[t.id] = b;
    }
    return {
      mode: "demo",
      tenantId: tenant.id,
      allowedTenantIds: TENANTS.map((t) => t.id),
      user: { name: "Ricardo Maia", role: "Administrador (demonstração)" },
      modules: demoModules(tenant, parseDemoModulesCookie(jar.get(demoModulesCookie(tenant.id))?.value)),
      brands,
    };
  }

  const token = jar.get(SESSION_COOKIE)?.value;
  if (!token) return null;
  let identity;
  try {
    identity = await getIdentitySession(cfg, token);
  } catch (e) {
    if (e instanceof CoreError && e.status === 401) return null;
    throw e;
  }

  const allowed = identity.memberships.map((m) => m.tenant_id);
  const tenantId = pickLiveTenant(allowed, h.get("host"), jar.get(TENANT_COOKIE)?.value);
  if (!tenantId) throw new CoreError(403, "no_tenant_access");
  const role = identity.memberships.find((m) => m.tenant_id === tenantId)!.role;

  return {
    mode: "live",
    tenantId,
    allowedTenantIds: allowed,
    user: { name: identity.user.name || identity.user.email, role: ROLE_LABEL[role], roleKey: role, email: identity.user.email },
    modules: await getModules(cfg, tenantId, token),
    brands: {},
  };
});

export function sessionTenant(s: Session): Tenant {
  return tenantFromId(s.tenantId);
}

/** Para telas do painel: sem sessão (modo real), vai para o login. */
export async function requireSession(): Promise<Session> {
  const s = await getSession();
  if (!s) redirect("/login");
  return s;
}
