import Link from "next/link";
import { ArrowRight, CalendarCheck, CircleDollarSign, List, Workflow, type LucideIcon } from "lucide-react";
import { brl } from "@/lib/kas/api";
import type { Activity } from "@/lib/kas/types";
import { Card, CardHead } from "./Card";

const ICON: Record<Activity["kind"], LucideIcon> = { payment: CircleDollarSign, appointment: CalendarCheck, process: Workflow };

export function ActivityCard({ items }: { items: Activity[] }) {
  return (
    <Card label="Atividades Recentes">
      <CardHead icon={List} title="Atividades Recentes" small>
        <Link href="/relatorios" className="inline-flex min-h-9 items-center gap-1 text-[13.5px] font-semibold text-ink">
          Ver todas <ArrowRight className="size-4" aria-hidden />
        </Link>
      </CardHead>
      <ul className="flex flex-col">
        {items.map((a) => {
          const Icon = ICON[a.kind];
          return (
            <li key={a.title + a.when} className="flex items-center gap-3 py-[7px]">
              <span className="grid size-10 shrink-0 place-items-center rounded-full bg-brand-tint text-brand-dark">
                <Icon className="size-5" strokeWidth={1.9} aria-hidden />
              </span>
              <span className="min-w-0">
                <b className="block text-[14.5px]">{a.title}</b>
                <small className="block truncate text-[13.5px] text-[#2a3a33]">{a.detail}</small>
              </span>
              <span className="ml-auto whitespace-nowrap text-right text-[13.5px] text-[#2a3a33]">
                {a.amount ? <b className="block text-[14.5px]">{brl(a.amount, true)}</b> : null}
                {a.when}
              </span>
            </li>
          );
        })}
      </ul>
    </Card>
  );
}
