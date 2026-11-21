"use client";

import { useQuery } from "@tanstack/react-query";
import type { Vertical } from "@/lib/tenant/types";
import { dashboardFixture } from "./fixtures";
import type { DashboardData, Period } from "./types";

/**
 * Ponto único de leitura do painel. Hoje devolve dados fictícios; na integração
 * passa a chamar o KAS Core (Go), ex.: GET /internal/v1/reports/dashboard?period=30d,
 * com o tenant vindo da sessão. Nada de regra de negócio aqui.
 */
async function fetchDashboard(tenantId: string, period: Period, vertical: Vertical): Promise<DashboardData> {
  return dashboardFixture(tenantId, period, vertical);
}

export function useDashboard(tenantId: string, period: Period, vertical: Vertical = "saude") {
  return useQuery({
    queryKey: ["dashboard", tenantId, period],
    queryFn: () => fetchDashboard(tenantId, period, vertical),
    staleTime: 60_000,
    placeholderData: (prev) => prev,
  });
}

export const brl = (v: number, cents = false) =>
  v.toLocaleString("pt-BR", { style: "currency", currency: "BRL", minimumFractionDigits: cents ? 2 : 0, maximumFractionDigits: cents ? 2 : 0 });
export const num = (v: number) => v.toLocaleString("pt-BR");

/* ---------- Modo real: receita do KAS Core via BFF (/api/kas) ---------- */

export type LiveRevenue = import("./contract").RevenueResponse & { periodo: { de: string; ate: string } };

export class BffError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`${status} ${code}`);
  }
}

async function fetchRevenue(period: Period): Promise<LiveRevenue> {
  const res = await fetch(`/api/kas/receita?periodo=${period}`, { cache: "no-store" });
  if (res.status === 401) {
    // Recarga completa de propósito: limpa o estado do painel ao perder a sessão.
    // eslint-disable-next-line @next/next/no-location-assign-relative-destination
    window.location.assign("/login");
    throw new BffError(401, "session_invalid");
  }
  if (!res.ok) {
    const code = ((await res.json().catch(() => ({}))) as { error?: { code?: string } }).error?.code ?? "unknown";
    throw new BffError(res.status, code);
  }
  return res.json();
}

export function useRevenue(tenantId: string, period: Period, enabled = true) {
  return useQuery({
    queryKey: ["revenue", tenantId, period],
    queryFn: () => fetchRevenue(period),
    staleTime: 60_000,
    placeholderData: (prev) => prev,
    enabled,
    retry: (n, e) => !(e instanceof BffError && e.status < 500) && n < 2,
  });
}

/** Link de download que passa pelo BFF (a credencial do serviço fica no servidor). */
export const fileUrl = (kind: "attribution-csv" | "report-csv" | "report-html", period: Period) => `/api/kas/arquivo?tipo=${kind}&periodo=${period}`;

export const cents = (c: number) => brl(c / 100, true);

/** Mensagem curta para o erro de leitura da receita. */
export function revenueErrorText(e: unknown): string {
  if (e instanceof BffError) {
    if (e.code === "module_paused") return "O módulo está pausado para esta empresa. Os dados continuam guardados.";
    if (e.code === "module_not_contracted" || e.code === "tenant_forbidden") return "Esta empresa não tem acesso a este módulo.";
    if (e.status === 502 || e.code === "core_unreachable") return "O KAS Core não respondeu. Tente de novo em alguns minutos.";
  }
  return "Não foi possível carregar a receita agora.";
}
