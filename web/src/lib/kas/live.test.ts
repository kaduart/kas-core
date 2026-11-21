import { describe, expect, it } from "vitest";
import { CoreError, getRevenue, login } from "./core-client";
import { loginErrorMessage, safeNext } from "./login-errors";
import { periodRange } from "./period";
import { pickLiveTenant } from "@/lib/session";

const cfg = { baseUrl: "http://kas.test", serviceId: "kas-web", token: "t" };
const json = (status: number, body: unknown) => async () => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("período em São Paulo", () => {
  it("30 dias contam o dia de hoje no fuso de Brasília", () => {
    // 21/11 00:30 UTC ainda é 20/11 em São Paulo
    const r = periodRange("30d", new Date("2026-11-21T00:30:00Z"));
    expect(r).toEqual({ fromDate: "2026-10-22", toDate: "2026-11-20", from: "2026-10-22T00:00:00-03:00", to: "2026-11-20T23:59:59-03:00" });
  });
});

describe("login", () => {
  it("devolve o token e as empresas", async () => {
    const s = await login(cfg, "a@b.c", "x", json(201, {
      contract_version: "v1", session_token: "kss_x", expires_at: "2026-11-21T12:00:00Z",
      user: { id: "usr_1", email: "a@b.c", name: "Ana" }, memberships: [{ tenant_id: "escola-demo", role: "gestor" }],
    }) as unknown as typeof fetch);
    expect(s.session_token).toBe("kss_x");
    expect(s.memberships[0].role).toBe("gestor");
  });
  it("senha errada vira mensagem que não revela se o e-mail existe", async () => {
    const err = await login(cfg, "a@b.c", "x", json(401, { error: { code: "invalid_credentials" } }) as unknown as typeof fetch).catch((e) => e);
    expect(err).toBeInstanceOf(CoreError);
    expect(loginErrorMessage(err)).toMatch(/não conferem/);
  });
  it("servidor fora do ar não quebra a tela", async () => {
    const err = await login(cfg, "a@b.c", "x", (async () => { throw new TypeError("fetch failed"); }) as unknown as typeof fetch).catch((e) => e);
    expect(err.code).toBe("core_unreachable");
    expect(loginErrorMessage(err)).toMatch(/não respondeu/);
  });
  it("só redireciona para dentro do painel", () => {
    expect(safeNext("/attribution")).toBe("/attribution");
    for (const bad of ["//evil.com", "/\\evil.com", "https://evil.com", "/login", null]) expect(safeNext(bad)).toBe("/dashboard");
  });
});

describe("empresa da sessão", () => {
  const allowed = ["clinica-aurora", "escola-demo"];
  it("em localhost vale a escolha salva, se a pessoa tiver acesso", () => {
    expect(pickLiveTenant(allowed, "localhost:3100", "escola-demo")).toBe("escola-demo");
    expect(pickLiveTenant(allowed, "localhost:3100", "fono-inova")).toBe("clinica-aurora");
  });
  it("domínio próprio de outra empresa não abre", () => {
    expect(pickLiveTenant(allowed, "painel.escolaexemplo.com.br", "clinica-aurora")).toBe("escola-demo");
    expect(pickLiveTenant(allowed, "painel.odontosorriso.com.br", undefined)).toBeNull();
  });
});

describe("receita", () => {
  it("manda a sessão da pessoa junto com a credencial do serviço", async () => {
    let seen: Headers | undefined;
    const spy = (async (_u: string, init: RequestInit) => {
      seen = new Headers(init.headers);
      return new Response("{}", { status: 401, headers: { "Content-Type": "application/json" } });
    }) as unknown as typeof fetch;
    await getRevenue(cfg, "escola-demo", "kss_abc", {}, spy).catch(() => null);
    expect(seen?.get("X-KAS-Session")).toBe("kss_abc");
    expect(seen?.get("X-KAS-Service")).toBe("kas-web");
  });
  it("recusa resposta de outra empresa", async () => {
    const body = { contract_version: "v1", tenant_id: "outra", moeda: "BRL", total_liquido_centavos: 0, por_nivel_centavos: {}, linhas: [],
      sem_vinculo: { recebido_centavos: 0, estornos_centavos: 0, liquido_centavos: 0, clientes: 0, lancamentos: 0 },
      invariante: { soma_niveis_centavos: 0, total_liquido_centavos: 0, fecha: true } };
    const err = await getRevenue(cfg, "escola-demo", "kss_x", {}, json(200, body) as unknown as typeof fetch).catch((e) => e);
    expect(err.code).toBe("tenant_mismatch");
  });
});
