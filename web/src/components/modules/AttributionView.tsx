"use client";

import { useState } from "react";
import { ChartNoAxesColumnIncreasing, Download } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader, Stat } from "@/components/ui/page-header";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { brl, num } from "@/lib/kas/api";
import { ATTRIBUTION, ATTRIBUTION_SCHOOL } from "@/lib/kas/modules-fixtures";
import { useTenant } from "@/lib/tenant/TenantProvider";

const F: Record<string, number> = { "7d": 0.24, "30d": 1, "90d": 2.7 };

export function AttributionView() {
  const [period, setPeriod] = useState("30d");
  const f = F[period];
  const { tenant } = useTenant();
  const school = tenant.vertical === "educacao";
  const rows = (school ? ATTRIBUTION_SCHOOL : ATTRIBUTION).map((r) => ({ ...r, leads: Math.round(r.leads * f), scheduled: Math.round(r.scheduled * f), revenue: Math.round(r.revenue * f), spend: Math.round(r.spend * f) }));
  const tot = rows.reduce((a, r) => ({ leads: a.leads + r.leads, revenue: a.revenue + r.revenue, spend: a.spend + r.spend }), { leads: 0, revenue: 0, spend: 0 });

  return (
    <>
      <PageHeader
        icon={ChartNoAxesColumnIncreasing}
        title="Attribution"
        description="De onde vem cada real recebido: canal, campanha e custo."
        actions={
          <Tabs value={period} onValueChange={setPeriod}>
            <TabsList aria-label="Período">
              <TabsTrigger value="7d">7 dias</TabsTrigger>
              <TabsTrigger value="30d">30 dias</TabsTrigger>
              <TabsTrigger value="90d">90 dias</TabsTrigger>
            </TabsList>
          </Tabs>
        }
      />
      <div className="mb-4 grid grid-cols-2 gap-2.5 md:gap-4 xl:grid-cols-4">
        <Stat label="Receita atribuída" value={brl(tot.revenue)} hint="pagamentos com origem conhecida" />
        <Stat label="Leads" value={num(tot.leads)} />
        <Stat label="Custo por lead" value={brl(tot.leads ? tot.spend / tot.leads : 0, true)} hint="só canais pagos" />
        <Stat label="Retorno sobre o investimento" value={`${(tot.spend ? tot.revenue / tot.spend : 0).toFixed(1).replace(".", ",")}x`} hint="receita ÷ investimento" />
      </div>
      <Card>
        <CardHeader>
          <CardTitle>Campanhas</CardTitle>
          <Button variant="outline" size="sm" className="ml-auto" onClick={() => toast.success("Exportação CSV gerada (demonstração).")}>
            <Download aria-hidden /> Exportar CSV
          </Button>
        </CardHeader>
        <Table>
          <THead>
            <TR>
              <TH>Campanha</TH>
              <TH className="text-right">Leads</TH>
              <TH className="text-right">{school ? "Visitas" : "Agendados"}</TH>
              <TH className="text-right">Investimento</TH>
              <TH className="text-right">Receita</TH>
              <TH className="text-right">Retorno</TH>
            </TR>
          </THead>
          <TBody>
            {rows.map((r) => {
              const roas = r.spend ? r.revenue / r.spend : null;
              return (
                <TR key={r.campaign}>
                  <TD>
                    <span className="flex items-center gap-2.5">
                      <span className="size-2.5 shrink-0 rounded-full" style={{ background: r.color }} aria-hidden />
                      <span>
                        <b className="block font-semibold">{r.campaign}</b>
                        <small className="text-muted">{r.channel}</small>
                      </span>
                    </span>
                  </TD>
                  <TD className="text-right tabular-nums">{num(r.leads)}</TD>
                  <TD className="text-right tabular-nums">{num(r.scheduled)}</TD>
                  <TD className="text-right tabular-nums">{r.spend ? brl(r.spend) : "—"}</TD>
                  <TD className="text-right font-semibold tabular-nums">{brl(r.revenue)}</TD>
                  <TD className="text-right">
                    {roas === null ? <Badge tone="neutral">orgânico</Badge> : <Badge tone={roas >= 4 ? "brand" : roas >= 2 ? "warn" : "danger"}>{roas.toFixed(1).replace(".", ",")}x</Badge>}
                  </TD>
                </TR>
              );
            })}
          </TBody>
        </Table>
      </Card>
    </>
  );
}
