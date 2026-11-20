import { describe, expect, it } from "vitest";
import { contrast, isHex, mix, themeTokens } from "./color";
import { resolveTenant, TENANTS } from "./presets";

describe("cores da marca", () => {
  it("mistura nos extremos devolve as próprias cores", () => {
    expect(mix("#0E8F4E", "#000000", 0)).toBe("#0e8f4e");
    expect(mix("#0E8F4E", "#000000", 1)).toBe("#000000");
  });

  it("aceita hex curto e rejeita texto inválido", () => {
    expect(isHex("#fff")).toBe(true);
    expect(isHex("verde")).toBe(false);
    expect(() => mix("verde", "#000", 0.5)).toThrow();
  });

  it("texto branco no menu de todas as empresas de demonstração passa AA", () => {
    for (const t of TENANTS) {
      const tk = themeTokens(t.brand.primary, t.brand.accent);
      expect(contrast(tk["--brand-deep"], "#ffffff")).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("texto sobre o destaque escolhe a cor legível", () => {
    expect(themeTokens("#0E8F4E", "#C6F03C")["--on-accent"]).not.toBe("#ffffff");
  });
});

describe("resolução da empresa", () => {
  it("sessão vence o domínio", () => {
    expect(resolveTenant({ cookie: "odonto-sorriso", host: "painel.clinicaaurora.com.br" }).id).toBe("odonto-sorriso");
  });
  it("sem sessão, usa o domínio", () => {
    expect(resolveTenant({ host: "painel.clinicaaurora.com.br:443" }).id).toBe("clinica-aurora");
  });
  it("domínio desconhecido cai no padrão KAS", () => {
    expect(resolveTenant({ host: "x.example" }).id).toBe("fono-inova");
  });
});

import { parseBrandCookie, serializeBrandCookie } from "./brand-cookie";

describe("cookie da marca", () => {
  it("ida e volta preserva a marca", () => {
    const b = TENANTS[1].brand;
    expect(parseBrandCookie(serializeBrandCookie(b))).toEqual(b);
  });
  it("rejeita cor ou ícone inválido", () => {
    expect(parseBrandCookie(serializeBrandCookie({ ...TENANTS[0].brand, primary: "url(x)" }))).toBeNull();
    expect(parseBrandCookie(encodeURIComponent(JSON.stringify({ ...TENANTS[0].brand, icon: "bomba" })))).toBeNull();
    expect(parseBrandCookie("%%%")).toBeNull();
  });
});

import { MODULES, NAV_TOP } from "@/lib/modules";

describe("módulos por empresa", () => {
  it("todo módulo tem rota no menu", () => {
    for (const m of MODULES) expect(NAV_TOP.some((n) => n.href === `/${m.slug}`)).toBe(true);
  });
  it("empresas de demonstração só usam módulos conhecidos", () => {
    const keys = MODULES.map((m) => m.key);
    for (const t of TENANTS) for (const k of t.modules) expect(keys).toContain(k);
  });
});
