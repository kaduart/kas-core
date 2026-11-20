import { CalendarCheck, Database, UsersRound, UserRoundCheck, type LucideIcon } from "lucide-react";
import { brl, num } from "@/lib/kas/api";
import type { Kpi } from "@/lib/kas/types";
import { cn } from "@/lib/cn";

const ICON: Record<Kpi["key"], LucideIcon> = { leads: UsersRound, appointments: CalendarCheck, attendances: UserRoundCheck, revenue: Database };

export function KpiRow({ kpis }: { kpis: Kpi[] }) {
  return (
    <div className="mb-[18px] grid grid-cols-2 gap-2.5 md:gap-4 xl:grid-cols-4">
      {kpis.map((k) => {
        const Icon = ICON[k.key];
        const hot = k.key === "revenue";
        return (
          <div
            key={k.key}
            className={cn(
              "flex flex-col items-start gap-2 rounded-[22px] p-3.5 shadow-[0_1px_3px_rgba(15,31,23,.05)] md:flex-row md:items-center md:gap-4 md:px-[18px] md:py-4",
              hot ? "text-white" : "bg-white",
            )}
            style={hot ? { background: "linear-gradient(135deg, var(--brand) 0%, var(--brand-dark) 100%)" } : undefined}
          >
            <span className={cn("grid size-[42px] shrink-0 place-items-center rounded-full md:size-14", hot ? "bg-white/15" : "bg-brand-tint text-brand-dark")}>
              <Icon className="size-[22px] md:size-[26px]" strokeWidth={1.8} aria-hidden />
            </span>
            <div className="min-w-0 flex-1">
              <small className="block text-[13.5px] font-semibold">{k.label}</small>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                <strong className="text-[24px] font-extrabold leading-tight tracking-tight tabular-nums md:text-[33px]">
                  {k.money ? brl(k.value) : num(k.value)}
                </strong>
                <span className={cn("rounded-full px-2 py-0.5 text-[13.5px] font-bold", hot ? "bg-white/20" : "bg-brand-tint text-brand-ink")}>
                  ↑ +{k.deltaPct}%
                </span>
              </div>
              <span className={cn("block text-[13px]", hot ? "text-white/85" : "text-muted")}>vs. mês anterior</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}
