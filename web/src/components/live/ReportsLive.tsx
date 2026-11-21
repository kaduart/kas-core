"use client";

import { useState } from "react";
import { FileSpreadsheet, FileText } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/ui/page-header";
import { cents, fileUrl, useRevenue } from "@/lib/kas/api";
import { fmtDate } from "@/lib/kas/period";
import type { Period } from "@/lib/kas/types";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { PeriodTabs } from "./LiveDashboard";

/** Relatórios do modo real: arquivos gerados pelo KAS Core com a marca da empresa. */
export function ReportsLive() {
  const { tenant, access } = useTenant();
  const [period, setPeriod] = useState<Period>("30d");
  const { data } = useRevenue(tenant.id, period, access("attribution").allowed);
  const range = data ? `${fmtDate(data.periodo.de)} a ${fmtDate(data.periodo.ate)}` : "";

  return (
    <>
      <PageHeader icon={FileText} title="Relatórios" description="Arquivos gerados pelo KAS Core, com a marca da sua empresa." actions={<PeriodTabs value={period} onChange={setPeriod} />} />
      <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
        <Card className="flex flex-col gap-2">
          <h3 className="text-[17px] font-bold">Receita por campanha</h3>
          <p className="text-[13.5px] text-[#2a3a33]">
            Quanto cada campanha trouxe, separado em comprovada, provável e sem vínculo, com estornos descontados.
          </p>
          <small className="text-[12.5px] text-muted">
            {data ? `${range} · total líquido ${cents(data.total_liquido_centavos)}` : "Período selecionado"}
          </small>
          <div className="mt-1 flex flex-wrap gap-2">
            <Button asChild size="sm" variant="soft">
              <a href={fileUrl("report-csv", period)} download>
                <FileSpreadsheet aria-hidden /> Baixar CSV
              </a>
            </Button>
            <Button asChild size="sm" variant="outline">
              <a href={fileUrl("report-html", period)} download>
                <FileText aria-hidden /> Baixar relatório (HTML)
              </a>
            </Button>
          </div>
          <p className="text-[12.5px] text-muted">O HTML abre no navegador e pode ser salvo em PDF pelo Imprimir.</p>
        </Card>
      </div>
    </>
  );
}
