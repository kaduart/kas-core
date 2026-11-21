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
    vertical: "saude",
    domains: ["app.kascore.com.br", "localhost"],
    demoModules: { attribution: "active", reports: "active", process: "active" },
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
    vertical: "saude",
    domains: ["painel.clinicaaurora.com.br"],
    demoModules: { attribution: "active", reports: "active" },
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
    vertical: "saude",
    domains: ["painel.odontosorriso.com.br"],
    demoModules: { attribution: "active", reports: "paused" },
    demoMissing: { attribution: ["receipts"] },
    brand: {
      name1: "Sorriso",
      name2: " Pro",
      tagline: "Gestão da sua clínica",
      primary: "#6B3FD1",
      accent: "#FFC94D",
      icon: "tooth",
    },
  },
  {
    // Mesmo tenant do piloto do kas-core (deploy/piloto-escola): segunda empresa, sem CRM da Fono Inova.
    id: "escola-demo",
    name: "Escola Exemplo",
    segment: "Escola de idiomas",
    vertical: "educacao",
    domains: ["painel.escolaexemplo.com.br"],
    demoModules: { attribution: "active", reports: "active" },
    brand: {
      name1: "Exemplo",
      name2: " Painel",
      tagline: "Matrículas e marketing em um lugar",
      primary: "#C2410C",
      accent: "#FDE68A",
      icon: "leaf",
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

/** Modo real: só o domínio decide a empresa. Domínio desconhecido não cai em empresa nenhuma. */
export function tenantByHost(host?: string | null): Tenant | undefined {
  const h = (host ?? "").split(":")[0].toLowerCase();
  return TENANTS.find((t) => t.domains.includes(h));
}

/** Empresa pelo id. Uma empresa real que ainda não tem cadastro de marca aqui aparece com a marca padrão. */
export function tenantFromId(id: string): Tenant {
  return (
    findTenant(id) ?? { id, name: id, segment: "", vertical: "servicos", domains: [], brand: { ...KAS_DEFAULT_BRAND }, demoModules: {} }
  );
}

const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]", "::1"]);
export function isLocalHost(host?: string | null): boolean {
  const h = (host ?? "").toLowerCase().replace(/:\d+$/, "");
  return LOCAL_HOSTS.has(h);
}
