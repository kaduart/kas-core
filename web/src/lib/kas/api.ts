"use client";

import { useQuery } from "@tanstack/react-query";
import { dashboardFixture } from "./fixtures";
import type { DashboardData, Period } from "./types";

/**
 * Ponto único de leitura do painel. Hoje devolve dados fictícios; na integração
 * passa a chamar o KAS Core (Go), ex.: GET /internal/v1/reports/dashboard?period=30d,
 * com o tenant vindo da sessão. Nada de regra de negócio aqui.
 */
async function fetchDashboard(tenantId: string, period: Period): Promise<DashboardData> {
  return dashboardFixture(tenantId, period);
}

export function useDashboard(tenantId: string, period: Period) {
  return useQuery({
    queryKey: ["dashboard", tenantId, period],
    queryFn: () => fetchDashboard(tenantId, period),
    staleTime: 60_000,
    placeholderData: (prev) => prev,
  });
}

export const brl = (v: number, cents = false) =>
  v.toLocaleString("pt-BR", { style: "currency", currency: "BRL", minimumFractionDigits: cents ? 2 : 0, maximumFractionDigits: cents ? 2 : 0 });
export const num = (v: number) => v.toLocaleString("pt-BR");
