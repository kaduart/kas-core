"use client";

import { createContext, useCallback, useContext, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { brandCookieName, serializeBrandCookie } from "./brand-cookie";
import { themeTokens } from "./color";
import { findTenant, KAS_DEFAULT_BRAND, TENANT_COOKIE, TENANTS } from "./presets";
import type { ModuleKey, Tenant, TenantBrand } from "./types";

interface TenantContextValue {
  tenant: Tenant;
  tenants: Tenant[];
  switchTenant: (id: string) => void;
  /** Aplica a marca na hora (pré-visualização). */
  previewBrand: (brand: TenantBrand) => void;
  /** Grava a marca. Nesta entrega fica em cookie; na integração vai para o KAS Core. */
  saveBrand: (brand: TenantBrand) => void;
  resetBrand: () => TenantBrand;
  /** Liga ou desliga um módulo para a empresa atual. Demonstração: na integração, quem decide é o KAS Core. */
  setModuleActive: (key: ModuleKey, on: boolean) => void;
}

const TenantContext = createContext<TenantContextValue | null>(null);

function writeCookie(name: string, value: string | null) {
  const base = `${name}=${value ?? ""}; path=/; samesite=lax; max-age=`;
  document.cookie = base + (value === null ? "0" : "31536000");
}

export function TenantProvider({
  initialTenantId,
  initialBrands = {},
  children,
}: {
  initialTenantId: string;
  /** Marcas personalizadas lidas no servidor, por empresa. */
  initialBrands?: Record<string, TenantBrand>;
  children: ReactNode;
}) {
  const router = useRouter();
  const [tenantId, setTenantId] = useState(initialTenantId);
  const [overrides, setOverrides] = useState<Record<string, TenantBrand>>(initialBrands);

  const [moduleOverrides, setModuleOverrides] = useState<Record<string, ModuleKey[]>>({});
  const base = findTenant(tenantId) ?? TENANTS[0];
  const tenant = useMemo<Tenant>(
    () => ({ ...base, brand: overrides[base.id] ?? base.brand, modules: moduleOverrides[base.id] ?? base.modules }),
    [base, overrides, moduleOverrides],
  );

  const switchTenant = useCallback(
    (id: string) => {
      if (!findTenant(id)) return;
      writeCookie(TENANT_COOKIE, encodeURIComponent(id));
      setTenantId(id);
      router.refresh();
    },
    [router],
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

  const setModuleActive = useCallback(
    (key: ModuleKey, on: boolean) =>
      setModuleOverrides((o) => {
        const current = o[tenantId] ?? (findTenant(tenantId) ?? TENANTS[0]).modules;
        const next = on ? Array.from(new Set([...current, key])) : current.filter((k) => k !== key);
        return { ...o, [tenantId]: next };
      }),
    [tenantId],
  );

  const value = useMemo(
    () => ({ tenant, tenants: TENANTS, switchTenant, previewBrand, saveBrand, resetBrand, setModuleActive }),
    [tenant, switchTenant, previewBrand, saveBrand, resetBrand, setModuleActive],
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
