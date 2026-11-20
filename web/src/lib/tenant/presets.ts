import type { Tenant } from "./types";

/**
 * Empresas de demonstração. Na integração real, estes dados vêm do KAS Core
 * (configstore por tenant), e não deste arquivo.
 */
export const TENANTS: Tenant[] = [
  {
    id: "fono-inova",
    name: "Fono Inova",
    segment: "Clínica Multidisciplinar",
    domains: ["app.kascore.com.br", "localhost"],
    modules: ["attribution", "crm", "omnichannel", "process", "ai", "reports"],
    brand: {
      name1: "KAS",
      name2: " Core",
      tagline: "Plataforma de Gestão e Crescimento",
      primary: "#0E8F4E",
      accent: "#C6F03C",
      icon: "x",
    },
  },
  {
    id: "clinica-aurora",
    name: "Clínica Aurora",
    segment: "Fisioterapia e Pilates",
    domains: ["painel.clinicaaurora.com.br"],
    modules: ["attribution", "crm", "reports"],
    brand: {
      name1: "Aurora",
      name2: " Gestão",
      tagline: "Cuidado que acompanha você",
      primary: "#1F5FD1",
      accent: "#7FE3FF",
      icon: "heart",
    },
  },
  {
    id: "odonto-sorriso",
    name: "Odonto Sorriso",
    segment: "Clínica Odontológica",
    domains: ["painel.odontosorriso.com.br"],
    modules: ["attribution", "crm", "omnichannel", "reports"],
    brand: {
      name1: "Sorriso",
      name2: " Pro",
      tagline: "Gestão da sua clínica",
      primary: "#6B3FD1",
      accent: "#FFC94D",
      icon: "tooth",
    },
  },
];

export const DEFAULT_TENANT_ID = TENANTS[0].id;
export const KAS_DEFAULT_BRAND = TENANTS[0].brand;
export const TENANT_COOKIE = "kas_tenant";

export function findTenant(id: string | undefined | null): Tenant | undefined {
  return TENANTS.find((t) => t.id === id);
}

/** Resolve a empresa pela sessão (cookie) e, se não houver, pelo domínio. */
export function resolveTenant(opts: { cookie?: string | null; host?: string | null }): Tenant {
  const byCookie = findTenant(opts.cookie);
  if (byCookie) return byCookie;
  const host = (opts.host ?? "").split(":")[0].toLowerCase();
  const byHost = TENANTS.find((t) => t.domains.includes(host));
  return byHost ?? TENANTS[0];
}
