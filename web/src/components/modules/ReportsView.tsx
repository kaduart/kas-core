"use client";

import { FileSpreadsheet, FileText } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/ui/page-header";
import { REPORTS, REPORTS_SCHOOL } from "@/lib/kas/modules-fixtures";
import { useTenant } from "@/lib/tenant/TenantProvider";

export function ReportsView() {
  const { tenant } = useTenant();
  const reports = tenant.vertical === "educacao" ? REPORTS_SCHOOL : REPORTS;
  return (
    <>
      <PageHeader icon={FileText} title="Relatórios" description="Relatórios prontos para baixar ou mandar para a equipe." />
      <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
        {reports.map((r) => (
          <Card key={r.id} className="flex flex-col gap-2">
            <h3 className="text-[17px] font-bold">{r.name}</h3>
            <p className="text-[13.5px] text-[#2a3a33]">{r.desc}</p>
            <small className="text-[12.5px] text-muted">Atualizado {r.updated}</small>
            <div className="mt-1 flex flex-wrap gap-2">
              <Button size="sm" variant="soft" onClick={() => toast.success(`${r.name}: CSV gerado (demonstração).`)}>
                <FileSpreadsheet aria-hidden /> Baixar CSV
              </Button>
              <Button size="sm" variant="outline" onClick={() => toast.success(`${r.name}: PDF gerado (demonstração).`)}>
                <FileText aria-hidden /> Baixar PDF
              </Button>
            </div>
          </Card>
        ))}
      </div>
    </>
  );
}
