"use client";

import { ChevronDown, Hexagon } from "lucide-react";
import * as Dropdown from "@radix-ui/react-dropdown-menu";
import { mix } from "@/lib/tenant/color";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { themeTokens } from "@/lib/tenant/color";
import { num } from "@/lib/kas/api";
import type { FunnelStage, Period } from "@/lib/kas/types";
import { Card, CardHead } from "./Card";

export const PERIOD_LABEL: Record<Period, string> = { "7d": "Últimos 7 dias", "30d": "Últimos 30 dias", "90d": "Últimos 90 dias" };

export function PeriodPicker({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  return (
    <Dropdown.Root>
      <Dropdown.Trigger className="inline-flex min-h-[38px] items-center gap-2 rounded-xl bg-[#f1f3f2] px-3 text-[13.5px] font-medium">
        {PERIOD_LABEL[value]}
        <ChevronDown className="size-4" aria-hidden />
      </Dropdown.Trigger>
      <Dropdown.Portal>
        <Dropdown.Content align="end" sideOffset={6} className="z-50 min-w-44 rounded-xl bg-white p-1 shadow-[0_14px_30px_rgba(15,31,23,.14)]">
          {(Object.keys(PERIOD_LABEL) as Period[]).map((p) => (
            <Dropdown.Item key={p} onSelect={() => onChange(p)} className="flex min-h-10 cursor-pointer items-center rounded-lg px-3 text-sm outline-none data-[highlighted]:bg-canvas">
              {PERIOD_LABEL[p]}
            </Dropdown.Item>
          ))}
        </Dropdown.Content>
      </Dropdown.Portal>
    </Dropdown.Root>
  );
}

export function FunnelCard({ stages, period, onPeriod }: { stages: FunnelStage[]; period: Period; onPeriod: (p: Period) => void }) {
  const { tenant } = useTenant();
  const tk = themeTokens(tenant.brand.primary, tenant.brand.accent);
  return (
    <Card label="Funil Comercial">
      <CardHead icon={Hexagon} title="Funil Comercial">
        <PeriodPicker value={period} onChange={onPeriod} />
      </CardHead>
      <ol className="flex flex-col gap-1.5">
        {stages.map((s, i) => {
          const next = stages[i + 1];
          const light = i >= 3;
          return (
            <li key={s.label} className="flex items-center gap-3.5">
              <div className="flex flex-[0_0_74%] justify-center">
                <div
                  className="funnel-bar flex h-[31px] items-center pr-3.5 text-[14.5px] font-semibold transition-[width] duration-500"
                  style={{ width: `${100 - i * 9}%`, background: mix(tk["--brand-deep"], tenant.brand.accent, i / 4), color: light ? tk["--brand-deep"] : "#fff" }}
                >
                  <span className="flex-1 text-center">{s.label}</span>
                  <span className="tabular-nums">{num(s.value)}</span>
                </div>
              </div>
              {next ? (
                <span className="rounded-full bg-brand-tint px-3 py-1 text-sm font-bold text-brand-ink" title={`${s.label} → ${next.label}`}>
                  {s.value ? Math.round((next.value / s.value) * 100) : 0}%
                </span>
              ) : null}
            </li>
          );
        })}
      </ol>
    </Card>
  );
}
