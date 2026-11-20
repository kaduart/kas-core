"use client";

import { useState } from "react";
import { ChartColumnBig, ChevronDown } from "lucide-react";
import { Cell, Pie, PieChart, ResponsiveContainer } from "recharts";
import { brl } from "@/lib/kas/api";
import type { CampaignRevenue } from "@/lib/kas/types";
import { cn } from "@/lib/cn";
import { Card, CardHead } from "./Card";

export function CampaignCard({ data }: { data: CampaignRevenue[] }) {
  const [off, setOff] = useState<Record<string, boolean>>({});
  const [mode, setMode] = useState<"brl" | "pct">("brl");
  const active = data.filter((d) => !off[d.channel]);
  const total = active.reduce((a, d) => a + d.amount, 0);
  const pct = (v: number) => (total ? (v / total) * 100 : 0);

  const toggle = (ch: string) => {
    const next = { ...off, [ch]: !off[ch] };
    if (data.every((d) => next[d.channel])) return;
    setOff(next);
  };

  return (
    <Card label="Receita por Campanha">
      <CardHead icon={ChartColumnBig} title="Receita por Campanha">
        <button
          onClick={() => setMode(mode === "brl" ? "pct" : "brl")}
          className="inline-flex min-h-[38px] items-center gap-2 rounded-xl bg-[#f1f3f2] px-3 text-[13.5px] font-medium"
        >
          Atribuição ({mode === "brl" ? "R$" : "%"})
          <ChevronDown className="size-4" aria-hidden />
        </button>
      </CardHead>
      <div className="flex flex-wrap items-center gap-[18px]">
        <div className="relative mx-auto size-[170px] shrink-0 md:mx-0 md:size-[180px] 2xl:size-[164px]">
          <ResponsiveContainer width="100%" height="100%">
            <PieChart>
              <Pie data={active} dataKey="amount" nameKey="channel" innerRadius="72%" outerRadius="98%" startAngle={90} endAngle={-270} stroke="#fff" strokeWidth={2} isAnimationActive>
                {active.map((d) => (
                  <Cell key={d.channel} fill={d.color} />
                ))}
              </Pie>
            </PieChart>
          </ResponsiveContainer>
          <div className="pointer-events-none absolute inset-0 flex flex-col items-center justify-center">
            <b className="text-[21px] font-extrabold tracking-tight">{brl(total)}</b>
            <small className="text-[13px] text-muted">{active.length === data.length ? "Total recebido" : "Canais selecionados"}</small>
          </div>
        </div>
        <ul className="min-w-[210px] flex-1 rounded-[14px] bg-[#f7f9f8] px-3 py-1.5">
          {data.map((d) => (
            <li key={d.channel}>
              <button
                onClick={() => toggle(d.channel)}
                aria-pressed={!off[d.channel]}
                className={cn("grid min-h-[31px] w-full grid-cols-[12px_minmax(0,1fr)_36px_auto] whitespace-nowrap items-center gap-2 rounded-lg px-1 text-left text-sm hover:bg-[#eef2f0]", off[d.channel] && "opacity-40")}
              >
                <span className="size-3 rounded-full" style={{ background: d.color }} aria-hidden />
                <span className="truncate" title={d.channel}>{d.channel}</span>
                <span className="text-right tabular-nums">{off[d.channel] ? "0%" : `${Math.round(pct(d.amount))}%`}</span>
                <span className="text-right font-semibold tabular-nums">
                  {mode === "brl" ? brl(d.amount) : `${pct(off[d.channel] ? 0 : d.amount).toFixed(1).replace(".", ",")}%`}
                </span>
              </button>
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}
