import { ChevronRight, ShieldCheck } from "lucide-react";
import type { IntegrationHealth } from "@/lib/kas/types";
import { Card, CardHead } from "./Card";

const STATUS: Record<IntegrationHealth["status"], { label: string; cls: string }> = {
  operational: { label: "Operacional", cls: "bg-brand-tint text-brand-ink" },
  degraded: { label: "Instável", cls: "bg-[#fdebd3] text-[#8a4b00]" },
  down: { label: "Fora do ar", cls: "bg-[#f8e3dd] text-[#a3402b]" },
};

export function IntegrationsCard({ items }: { items: IntegrationHealth[] }) {
  return (
    <Card label="Saúde das Integrações">
      <CardHead icon={ShieldCheck} title="Saúde das Integrações" />
      <ul className="flex flex-col">
        {items.map((g) => {
          const st = STATUS[g.status];
          return (
            <li key={g.name}>
              <button className="flex min-h-11 w-full items-center gap-3 rounded-[10px] px-1 text-left hover:bg-[#f4f7f5]" title={`Última sincronização ${g.lastSync}`}>
                <span className="grid size-6 shrink-0 place-items-center rounded-[7px] text-[11px] font-extrabold text-white" style={{ background: g.color }} aria-hidden>
                  {g.short}
                </span>
                <span className="flex-1 text-[14.5px]">{g.name}</span>
                <span className={`rounded-full px-3 py-0.5 text-[13px] font-semibold ${st.cls}`}>{st.label}</span>
                <ChevronRight className="size-[18px]" aria-hidden />
              </button>
            </li>
          );
        })}
      </ul>
    </Card>
  );
}
