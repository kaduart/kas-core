const HEX = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i;

export function isHex(value: string): boolean {
  return HEX.test(value.trim());
}

function toRgb(hex: string): [number, number, number] {
  const m = hex.trim().match(HEX);
  if (!m) throw new Error(`cor inválida: ${hex}`);
  let h = m[1];
  if (h.length === 3) h = h.split("").map((c) => c + c).join("");
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)];
}

/** Mistura linear entre duas cores. t=0 devolve a, t=1 devolve b. */
export function mix(a: string, b: string, t: number): string {
  const A = toRgb(a);
  const B = toRgb(b);
  const k = Math.min(1, Math.max(0, t));
  return (
    "#" +
    A.map((v, i) => Math.round(v + (B[i] - v) * k).toString(16).padStart(2, "0")).join("")
  );
}

function luminance(hex: string): number {
  const [r, g, b] = toRgb(hex).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

export interface ThemeTokens {
  "--brand": string;
  "--brand-dark": string;
  "--brand-deep": string;
  "--brand-tint": string;
  "--brand-ink": string;
  "--accent": string;
  "--on-accent": string;
}

/**
 * Deriva a paleta completa a partir das duas cores escolhidas pela empresa.
 * Mantém o mesmo layout para todo tenant; muda só a identidade.
 */
export function themeTokens(primary: string, accent: string): ThemeTokens {
  const deep = mix(primary, "#000000", 0.55);
  return {
    "--brand": primary,
    "--brand-dark": mix(primary, "#000000", 0.32),
    "--brand-deep": deep,
    "--brand-tint": mix(primary, "#ffffff", 0.86),
    "--brand-ink": mix(primary, "#000000", 0.4),
    "--accent": accent,
    "--on-accent": contrast(accent, "#ffffff") >= 3 ? "#ffffff" : deep,
  };
}
