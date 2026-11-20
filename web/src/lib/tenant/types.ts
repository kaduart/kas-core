/** Ícones de marca disponíveis no seletor. No produto, a empresa também poderá enviar o próprio logo. */
export type BrandIcon = "x" | "leaf" | "heart" | "tooth";

/** Identidade visual de uma empresa (tenant). Só apresentação: regras e permissões ficam no KAS Core (Go). */
export interface TenantBrand {
  /** Parte em negrito do nome do sistema, ex.: "KAS". */
  name1: string;
  /** Parte leve do nome do sistema, ex.: " Core". */
  name2: string;
  /** Frase abaixo do nome. */
  tagline: string;
  /** Cor principal (hex #rrggbb). */
  primary: string;
  /** Cor de destaque (hex #rrggbb). */
  accent: string;
  icon: BrandIcon;
}

export type ModuleKey = "attribution" | "crm" | "omnichannel" | "process" | "ai" | "reports";

export interface Tenant {
  id: string;
  /** Nome da empresa exibido no seletor e na saudação. */
  name: string;
  /** Segmento, ex.: "Clínica Multidisciplinar". */
  segment: string;
  /** Domínios que abrem esta empresa (white-label por domínio). */
  domains: string[];
  /** Módulos ativos para esta empresa. */
  modules: ModuleKey[];
  brand: TenantBrand;
}
