/**
 * Contrato de leitura do painel. Espelha o que o KAS Core (Go) vai expor;
 * o front só exibe, não calcula receita nem decide permissão.
 */
export type Period = "7d" | "30d" | "90d";
export type RevenueKind = "received" | "contracted" | "invoiced";

export interface Kpi {
  key: "leads" | "appointments" | "attendances" | "revenue";
  label: string;
  value: number;
  /** Variação contra o mês anterior, em pontos percentuais. */
  deltaPct: number;
  money?: boolean;
}

export interface FunnelStage {
  label: string;
  value: number;
}

export interface CampaignRevenue {
  channel: string;
  color: string;
  amount: number;
}

export interface IntegrationHealth {
  name: string;
  short: string;
  color: string;
  status: "operational" | "degraded" | "down";
  lastSync: string;
}

export interface Activity {
  kind: "payment" | "appointment" | "process";
  title: string;
  detail: string;
  amount?: number;
  when: string;
}

export interface DashboardData {
  kpis: Kpi[];
  funnel: FunnelStage[];
  campaigns: CampaignRevenue[];
  integrations: IntegrationHealth[];
  revenue: Record<RevenueKind, { month: string; value: number }[]>;
  activities: Activity[];
}
