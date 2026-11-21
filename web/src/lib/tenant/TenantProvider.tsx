"use client";

import { createContext, useCallback, useContext, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { moduleAccess, type ContractState, type ModuleAccess, type ModuleKey, type ModuleState, type SessionMode } from "@/lib/kas/contract";
import { demoModulesCookie } from "@/lib/kas/demo";
import type { Session } from "@/lib/session";
import { brandCookieName, serializeBrandCookie } from "./brand-cookie";
import { themeTokens } from "./color";
import { KAS_DEFAULT_BRAND, TENANT_COOKIE, tenantFromId } from "./presets";
import type { Tenant, TenantBrand } from "./types";

interface TenantContextValue {
  mode: SessionMode;
  tenant: Tenant;
  /** Empresas que a sessão pode abrir. No modo real, as do login. */
  tenants: Tenant[];
  user: Session["user"];
  /** Estado de cada módulo como o servidor decidiu (contrato v1 do KAS Core). */
  modules: ModuleState[];
  access: (key: ModuleKey) => ModuleAccess;
  /** Troca de empresa entre as permitidas (no modo real, as do login). */
  switchTenant: (id: string) => void;
  /** Só no modo demonstração: simula o contrato do módulo e pede ao servidor para recalcular. */
  setDemoModule: (key: ModuleKey, state: ContractState) => void;
  previewBrand: (brand: TenantBrand) => void;
  saveBrand: (brand: TenantBrand) => void;
  resetBrand: () => TenantBrand;
}

const TenantContext = createContext<TenantContextValue | null>(null);

function writeCookie(name: string, value: string | null) {
  const base = `${name}=${value ?? ""}; path=/; samesite=lax; max-age=`;
  document.cookie = base + (value === null ? "0" : "31536000");
}

export function TenantProvider({ session, children }: { session: Session; children: ReactNode }) {
  const router = useRouter();
  const [overrides, setOverrides] = useState<Record<string, TenantBrand>>(session.brands);
  const tenantId = session.tenantId;
  const base = useMemo(() => tenantFromId(tenantId), [tenantId]);
  const tenant = useMemo<Tenant>(() => ({ ...base, brand: overrides[base.id] ?? base.brand }), [base, overrides]);
  const tenants = useMemo(() => session.allowedTenantIds.map(tenantFromId), [session.allowedTenantIds]);
  const modules = session.modules.modules;

  const access = useCallback((key: ModuleKey) => moduleAccess(session.mode, modules, key), [session.mode, modules]);

  const switchTenant = useCallback(
    (id: string) => {
      if (!session.allowedTenantIds.includes(id)) return;
      writeCookie(TENANT_COOKIE, encodeURIComponent(id));
      router.refresh();
    },
    [router, session.allowedTenantIds],
  );

  const setDemoModule = useCallback(
    (key: ModuleKey, state: ContractState) => {
      if (session.mode !== "demo") return;
      const current: Record<string, ContractState> = {};
      for (const m of modules) if (m.contract !== "planned") current[m.key] = m.contract;
      current[key] = state;
      writeCookie(demoModulesCookie(tenantId), encodeURIComponent(JSON.stringify(current)));
      router.refresh();
    },
    [router, session.mode, modules, tenantId],
  );

  const previewBrand = useCallback((brand: TenantBrand) => setOverrides((o) => ({ ...o, [tenantId]: brand })), [tenantId]);

  const saveBrand = useCallback(
    (brand: TenantBrand) => {
      setOverrides((o) => ({ ...o, [tenantId]: brand }));
      writeCookie(brandCookieName(tenantId), serializeBrandCookie(brand));
    },
    [tenantId],
  );

  const resetBrand = useCallback(() => {
    const brand = { ...KAS_DEFAULT_BRAND };
    setOverrides((o) => ({ ...o, [tenantId]: brand }));
    writeCookie(brandCookieName(tenantId), null);
    return brand;
  }, [tenantId]);

  const value = useMemo(
    () => ({ mode: session.mode, tenant, tenants, user: session.user, modules, access, switchTenant, setDemoModule, previewBrand, saveBrand, resetBrand }),
    [session.mode, session.user, tenant, tenants, modules, access, switchTenant, setDemoModule, previewBrand, saveBrand, resetBrand],
  );

  const style = themeTokens(tenant.brand.primary, tenant.brand.accent) as unknown as CSSProperties;

  return (
    <TenantContext.Provider value={value}>
      <div style={style} className="contents">
        {children}
      </div>
    </TenantContext.Provider>
  );
}

export function useTenant(): TenantContextValue {
  const ctx = useContext(TenantContext);
  if (!ctx) throw new Error("useTenant precisa estar dentro de <TenantProvider>");
  return ctx;
}
