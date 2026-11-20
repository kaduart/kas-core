import { describe, expect, it } from "vitest";
import { moduleAccess, type ModuleState } from "./contract";
import { CoreError, coreConfigFromEnv, getModules } from "./core-client";
import { demoModules, parseDemoModulesCookie } from "./demo";
import { findTenant, tenantByHost } from "@/lib/tenant/presets";

const mods: ModuleState[] = [
  { key: "attribution", contract: "active", integration: { ready: false, missing: ["receipts"] } },
  { key: "reports", contract: "paused", depends_on: ["attribution"] },
  { key: "process", contract: "absent" },
  { key: "crm", contract: "planned" },
];

describe("moduleAccess (decisão vem do contrato)", () => {
  it("ativo com integração pendente libera e avisa o que falta", () => {
    expect(moduleAccess("live", mods, "attribution")).toEqual({ allowed: true, missing: ["receipts"] });
  });
  it("pausado e ausente bloqueiam nos dois modos", () => {
    for (const mode of ["demo", "live"] as const) {
      expect(moduleAccess(mode, mods, "reports")).toEqual({ allowed: false, reason: "paused" });
      expect(moduleAccess(mode, mods, "process")).toEqual({ allowed: false, reason: "not_contracted" });
      expect(moduleAccess(mode, mods, "ai")).toEqual({ allowed: false, reason: "not_contracted" });
    }
  });
  it("planejado só vira prévia na demonstração", () => {
    expect(moduleAccess("demo", mods, "crm")).toEqual({ allowed: true, preview: true });
    expect(moduleAccess("live", mods, "crm")).toEqual({ allowed: false, reason: "planned" });
  });
});

describe("modo demonstração", () => {
  it("cookie não consegue ligar módulo planejado nem estado inválido", () => {
    const raw = encodeURIComponent(JSON.stringify({ crm: "active", ai: "active", process: "active", reports: "hack" }));
    expect(parseDemoModulesCookie(raw)).toEqual({ process: "active" });
    expect(parseDemoModulesCookie("%%%")).toEqual({});
  });
  it("Escola Exemplo tem Attribution e Relatórios, sem CRM", () => {
    const r = demoModules(findTenant("escola-demo")!);
    const by = Object.fromEntries(r.modules.map((m) => [m.key, m.contract]));
    expect(by).toMatchObject({ attribution: "active", reports: "active", process: "absent", crm: "planned" });
    expect(r.modules.find((m) => m.key === "reports")?.depends_on).toEqual(["attribution"]);
  });
});

describe("modo real", () => {
  it("só liga com as três variáveis", () => {
    expect(coreConfigFromEnv({ KAS_API_URL: "http://x" } as unknown as NodeJS.ProcessEnv)).toBeNull();
    expect(coreConfigFromEnv({ KAS_API_URL: "http://x/", KAS_SERVICE_ID: "web", KAS_SERVICE_TOKEN: "t" } as unknown as NodeJS.ProcessEnv)?.baseUrl).toBe("http://x");
  });
  it("domínio desconhecido não cai em empresa nenhuma", () => {
    expect(tenantByHost("evil.example.com")).toBeUndefined();
    expect(tenantByHost("painel.escolaexemplo.com.br:443")?.id).toBe("escola-demo");
  });

  const cfg = { baseUrl: "http://kas", serviceId: "web", token: "t" };
  const ok = (body: unknown, status = 200) => (async () => new Response(JSON.stringify(body), { status })) as unknown as typeof fetch;

  it("valida o contrato e o tenant da resposta", async () => {
    const body = { contract_version: "v1", tenant_id: "escola-demo", config_version: "3", modules: [{ key: "attribution", contract: "active" }] };
    await expect(getModules(cfg, "escola-demo", ok(body))).resolves.toMatchObject({ tenant_id: "escola-demo" });
    await expect(getModules(cfg, "outra", ok(body))).rejects.toMatchObject({ code: "tenant_mismatch" });
    await expect(getModules(cfg, "escola-demo", ok({ ...body, contract_version: "v2" }))).rejects.toMatchObject({ code: "contract_mismatch" });
  });
  it("propaga o código de erro do Go", async () => {
    const err = await getModules(cfg, "x", ok({ error: { code: "unauthorized" } }, 401)).catch((e) => e);
    expect(err).toBeInstanceOf(CoreError);
    expect(err).toMatchObject({ status: 401, code: "unauthorized" });
  });
});

import { dashboardFixture } from "./fixtures";
import { ATTRIBUTION_SCHOOL } from "./modules-fixtures";

describe("Escola Exemplo: receita conciliada", () => {
  it("Attribution fecha com o painel em receita, leads e visitas (30 dias)", () => {
    const d = dashboardFixture("escola-demo", "30d", "educacao");
    const sum = (k: "revenue" | "leads" | "scheduled") => ATTRIBUTION_SCHOOL.reduce((a, r) => a + r[k], 0);
    expect(sum("revenue")).toBe(d.campaigns.reduce((a, c) => a + c.amount, 0));
    expect(sum("leads")).toBe(d.kpis.find((k) => k.key === "leads")!.value);
    expect(sum("scheduled")).toBe(d.kpis.find((k) => k.key === "appointments")!.value);
    for (const c of d.campaigns) expect(ATTRIBUTION_SCHOOL.filter((r) => r.channel === c.channel).reduce((a, r) => a + r.revenue, 0)).toBe(c.amount);
  });
});
