import type { DashboardData, Period } from "./types";

const FACTOR: Record<Period, number> = { "7d": 0.24, "30d": 1, "90d": 2.7 };

/** Dados fictícios da demonstração. Serão trocados pela chamada ao KAS Core. */
export function dashboardFixture(tenantId: string, period: Period): DashboardData {
  const f = FACTOR[period] * (tenantId === "fono-inova" ? 1 : tenantId === "clinica-aurora" ? 0.62 : 0.81);
  const n = (v: number) => Math.round(v * f);
  const months = ["Jun", "Jul", "Ago", "Set", "Out", "Nov"];
  const series = (vals: number[]) => vals.map((v, i) => ({ month: months[i], value: Math.round(v * 1000 * f) }));
  const campaigns = [
    { channel: "Google Ads", color: "#0E9F55", amount: n(22982) },
    { channel: "Meta Ads", color: "#F0216F", amount: n(17510) },
    { channel: "TikTok Ads", color: "#FF8A1F", amount: n(6019) },
    { channel: "Orgânico", color: "#2468F2", amount: n(4921) },
    { channel: "Outros", color: "#C9CED1", amount: n(3288) },
  ];
  const received = campaigns.reduce((a, c) => a + c.amount, 0);
  return {
    kpis: [
      { key: "leads", label: "Leads", value: n(482), deltaPct: 18 },
      { key: "appointments", label: "Agendamentos", value: n(236), deltaPct: 22 },
      { key: "attendances", label: "Atendimentos", value: n(198), deltaPct: 11 },
      { key: "revenue", label: "Receita Recebida", value: received, deltaPct: 27, money: true },
    ],
    funnel: [
      { label: "Leads", value: n(482) },
      { label: "Qualificados", value: n(338) },
      { label: "Agendados", value: n(236) },
      { label: "Compareceram", value: n(198) },
      { label: "Convertidos", value: n(152) },
    ],
    campaigns,
    integrations: [
      { name: "Meta (CAPI)", short: "M", color: "#1877F2", status: "operational", lastSync: "há 2 min" },
      { name: "Google Ads", short: "G", color: "#2468F2", status: "operational", lastSync: "há 4 min" },
      { name: "TikTok Ads", short: "T", color: "#111111", status: "operational", lastSync: "há 6 min" },
      { name: "GA4 (Analytics)", short: "GA", color: "#E37400", status: "operational", lastSync: "há 3 min" },
      { name: "WhatsApp (Amanda)", short: "W", color: "#128C4A", status: "operational", lastSync: "agora" },
    ],
    revenue: {
      received: series([12, 15, 21, 25, 32, 47]),
      contracted: series([18, 22, 26, 31, 38, 52]),
      invoiced: series([14, 17, 23, 27, 35, 50]),
    },
    activities: [
      { kind: "payment", title: "Pagamento recebido", detail: "Paciente #5487 · Google Ads", amount: 720, when: "há 12 min" },
      { kind: "appointment", title: "Novo agendamento", detail: "Lead #7321 · Meta Ads", when: "há 28 min" },
      { kind: "process", title: "Processo atualizado", detail: "Retenção · Process Studio", when: "há 1 h" },
    ],
  };
}
