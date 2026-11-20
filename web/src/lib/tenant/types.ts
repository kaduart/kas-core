import type { ContractState, ModuleKey } from "@/lib/kas/contract";

export type { ModuleKey } from "@/lib/kas/contract";

/** Ícones de marca disponíveis no seletor. No produto, a empresa também poderá enviar o próprio logo. */
export type BrandIcon = "x" | "leaf" | "heart" | "tooth";

/** Segmento da empresa. Define quais extensões verticais aparecem; o catálogo de módulos KAS é o mesmo para todos. */
export type Vertical = "saude" | "educacao" | "servicos" | "varejo";

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

export interface Tenant {
  /** Mesmo id de tenant do KAS Core. */
  id: string;
  /** Nome da empresa exibido no seletor e na saudação. */
  name: string;
  /** Segmento, ex.: "Clínica Multidisciplinar". */
  segment: string;
  vertical: Vertical;
  /** Domínios que abrem esta empresa (white-label por domínio). */
  domains: string[];
  brand: TenantBrand;
  /**
   * Só para o modo demonstração: estado inicial dos módulos. No modo real,
   * o estado vem de GET /internal/v1/modules e este campo é ignorado.
   */
  demoModules: Partial<Record<ModuleKey, ContractState>>;
  /** Só demonstração: pendências de integração simuladas. */
  demoMissing?: Partial<Record<ModuleKey, string[]>>;
}
