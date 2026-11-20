import { isHex } from "./color";
import type { BrandIcon, TenantBrand } from "./types";

/**
 * Marca personalizada guardada em cookie para o servidor já renderizar com as cores
 * da empresa (sem "piscar" o tema padrão). Na integração, a fonte passa a ser o KAS Core.
 */
export const brandCookieName = (tenantId: string) => `kas_brand_${tenantId}`;

const ICONS: BrandIcon[] = ["x", "leaf", "heart", "tooth"];

export function parseBrandCookie(raw: string | undefined | null): TenantBrand | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(decodeURIComponent(raw)) as Partial<TenantBrand>;
    if (
      typeof v.name1 === "string" &&
      typeof v.name2 === "string" &&
      typeof v.tagline === "string" &&
      typeof v.primary === "string" &&
      isHex(v.primary) &&
      typeof v.accent === "string" &&
      isHex(v.accent) &&
      ICONS.includes(v.icon as BrandIcon)
    ) {
      return {
        name1: v.name1.slice(0, 14),
        name2: v.name2.slice(0, 14),
        tagline: v.tagline.slice(0, 40),
        primary: v.primary,
        accent: v.accent,
        icon: v.icon as BrandIcon,
      };
    }
  } catch {
    /* cookie corrompido: usa a marca padrão */
  }
  return null;
}

export function serializeBrandCookie(brand: TenantBrand): string {
  return encodeURIComponent(JSON.stringify(brand));
}
