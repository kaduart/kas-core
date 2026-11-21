"use client";

import { useState } from "react";
import { useDashboard } from "@/lib/kas/api";
import type { Period } from "@/lib/kas/types";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { LiveDashboard } from "@/components/live/LiveDashboard";
import { ActivityCard } from "./ActivityCard";
import { CampaignCard } from "./CampaignCard";
import { FunnelCard } from "./FunnelCard";
import { IntegrationsCard } from "./IntegrationsCard";
import { KpiRow } from "./KpiRow";
import { ModulesRow } from "./ModulesRow";
import { PromoCard } from "./PromoCard";
import { RevenueCard } from "./RevenueCard";

export function DashboardView() {
  const { mode } = useTenant();
  return mode === "live" ? <LiveDashboard /> : <DemoDashboard />;
}

function DemoDashboard() {
  const { tenant } = useTenant();
  const [period, setPeriod] = useState<Period>("30d");
  const { data, isError, refetch } = useDashboard(tenant.id, period, tenant.vertical);

  if (isError) {
    return (
      <div role="alert" className="rounded-[22px] bg-white p-6">
        <p className="font-semibold">Não foi possível carregar o painel.</p>
        <p className="mt-1 text-sm text-muted">Verifique a conexão com o KAS Core e tente de novo.</p>
        <button onClick={() => refetch()} className="mt-4 min-h-11 rounded-xl bg-brand px-4 font-semibold text-white">
          Tentar de novo
        </button>
      </div>
    );
  }
  if (!data) {
    return <div className="h-[60vh] animate-pulse rounded-[22px] bg-white/70" aria-busy="true" aria-label="Carregando painel" />;
  }

  return (
    <>
      <KpiRow kpis={data.kpis} />
      <div className="mb-[18px] grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-[1fr_1.12fr_.98fr]">
        <FunnelCard stages={data.funnel} period={period} onPeriod={setPeriod} />
        <CampaignCard data={data.campaigns} />
        <div className="md:col-span-2 2xl:col-span-1">
          <IntegrationsCard items={data.integrations} />
        </div>
      </div>
      <ModulesRow />
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-[1fr_1.03fr_1.03fr]">
        <RevenueCard revenue={data.revenue} />
        <ActivityCard items={data.activities} />
        <div className="md:col-span-2 2xl:col-span-1">
          <PromoCard />
        </div>
      </div>
    </>
  );
}
