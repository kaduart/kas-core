import { describe, expect, it } from "vitest";
import { createBody, rescheduleBody, slotsQuery } from "./agenda-input";
import { sameOrigin } from "./bff";
import { CoreError, cancelAgendaBooking, createAgendaBooking, getAgendaCatalog } from "./core-client";

const cfg = { baseUrl: "http://127.0.0.1:8088", serviceId: "kas-web", token: "t" };
const booking = {
  id: "bk-1",
  resource_id: "prof-ana",
  client_ref: "cli-1",
  starts_at: "2026-11-30T12:00:00Z",
  ends_at: "2026-11-30T12:40:00Z",
  status: "scheduled",
  buffer_min: 0,
  blocked_until: "2026-11-30T12:40:00Z",
  version: 1,
  updated_at: "2026-11-21T00:00:00Z",
};

function fake(status: number, body: unknown, seen: { url?: string; init?: RequestInit }[] = []) {
  return (async (url: string, init?: RequestInit) => {
    seen.push({ url, init });
    return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  }) as unknown as typeof fetch;
}

describe("Agenda no BFF", () => {
  it("o código do cliente não aceita nome, e-mail nem telefone com espaço", () => {
    const base = { chave: "web-1", recurso: "prof-ana", inicio: "2026-11-30T12:00:00Z", fim: "2026-11-30T12:40:00Z" };
    expect(createBody.safeParse({ ...base, cliente: "cli-1042" }).success).toBe(true);
    for (const cliente of ["Maria Silva", "maria@exemplo.com", "(11) 9999-0000", ""]) expect(createBody.safeParse({ ...base, cliente }).success).toBe(false);
    expect(createBody.safeParse({ ...base, cliente: "cli-1", tenant_id: "outra" }).success).toBe(false);
  });
  it("horários exigem fuso explícito e versão positiva", () => {
    expect(rescheduleBody.safeParse({ versao: 1, inicio: "2026-11-30T09:00:00", fim: "2026-11-30T09:40:00" }).success).toBe(false);
    expect(rescheduleBody.safeParse({ versao: 0, inicio: "2026-11-30T09:00:00-03:00", fim: "2026-11-30T09:40:00-03:00" }).success).toBe(false);
    expect(slotsQuery.safeParse({ recurso: "prof-ana", de: "2026-11-30", ate: "2026-11-30", servico: "s", duracao: "40" }).success).toBe(false);
  });
  it("escrita só vem do próprio painel (Origin = Host)", () => {
    const req = (origin?: string) => new Request("http://localhost:3100/api/kas/agenda/agendamentos", { method: "POST", headers: { host: "localhost:3100", ...(origin ? { origin } : {}) } });
    expect(sameOrigin(req("http://localhost:3100"))).toBe(true);
    expect(sameOrigin(req("https://malicioso.exemplo"))).toBe(false);
    expect(sameOrigin(req())).toBe(false);
  });
});

describe("Agenda no cliente do KAS Core", () => {
  it("envia a sessão, o contrato v1 e a empresa da sessão", async () => {
    const seen: { url?: string; init?: RequestInit }[] = [];
    const out = await createAgendaBooking(
      cfg,
      "clinica-aurora",
      "sess",
      { idempotencyKey: "web-1", resourceId: "prof-ana", clientRef: "cli-1", startsAt: booking.starts_at, endsAt: booking.ends_at },
      fake(201, { contract_version: "v1", tenant_id: "clinica-aurora", booking }, seen),
    );
    expect(out.booking.id).toBe("bk-1");
    const h = new Headers(seen[0].init!.headers);
    expect(h.get("X-KAS-Session")).toBe("sess");
    expect(JSON.parse(String(seen[0].init!.body))).toMatchObject({ contract_version: "v1", tenant_id: "clinica-aurora", idempotency_key: "web-1", client_ref: "cli-1" });
  });
  it("resposta de outra empresa é recusada", async () => {
    await expect(getAgendaCatalog(cfg, "clinica-aurora", "s", fake(200, { contract_version: "v1", tenant_id: "outra", zone: "UTC", read_only: false, resources: [], services: [] }))).rejects.toMatchObject({
      code: "tenant_mismatch",
    });
  });
  it("repassa o código de erro do Go", async () => {
    const err = await cancelAgendaBooking(cfg, "clinica-aurora", "s", "bk-1", 1, fake(409, { error: { code: "agenda_read_only" } })).catch((e) => e);
    expect(err).toBeInstanceOf(CoreError);
    expect(err).toMatchObject({ status: 409, code: "agenda_read_only" });
  });
});
