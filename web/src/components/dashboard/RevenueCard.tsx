"use client";

import { useState } from "react";
import { LayoutGrid } from "lucide-react";
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import { brl } from "@/lib/kas/api";
import type { DashboardData, RevenueKind } from "@/lib/kas/types";
import { cn } from "@/lib/cn";
import { Card, CardHead } from "./Card";

const TABS: { k: RevenueKind; l: string }[] = [
  { k: "received", l: "Recebida" },
  { k: "contracted", l: "Contratada" },
  { k: "invoiced", l: "Faturada" },
];

export function RevenueCard({ revenue }: { revenue: DashboardData["revenue"] }) {
  const [kind, setKind] = useState<RevenueKind>("received");
  const data = revenue[kind];
  return (
    <Card label="Evolução da Receita">
      <CardHead icon={LayoutGrid} title="Evolução da Receita" small>
        <div role="tablist" className="inline-flex rounded-xl bg-[#f1f3f2] p-[3px]">
          {TABS.map((t) => (
            <button
              key={t.k}
              role="tab"
              aria-selected={kind === t.k}
              onClick={() => setKind(t.k)}
              className={cn("min-h-8 rounded-[10px] px-3 text-[12.5px] font-medium", kind === t.k && "bg-brand-dark text-white")}
            >
              {t.l}
            </button>
          ))}
        </div>
      </CardHead>
      <div className="h-[190px]">
        <ResponsiveContainer width="100%" height="100%">
          <BarChart data={data} margin={{ top: 6, right: 4, left: 4, bottom: 0 }} barCategoryGap="22%">
            <CartesianGrid vertical={false} stroke="#eef2f0" />
            <XAxis dataKey="month" tickLine={false} axisLine={{ stroke: "#e6ebe8" }} tick={{ fontSize: 13, fill: "#2a3a33" }} />
            <YAxis tickLine={false} axisLine={false} width={56} tick={{ fontSize: 12.5, fill: "#2a3a33" }} tickFormatter={(v: number) => `R$ ${Math.round(v / 1000)}k`} />
            <Tooltip cursor={{ fill: "rgba(0,0,0,.04)" }} formatter={(v) => brl(Number(v))} labelStyle={{ fontWeight: 600 }} contentStyle={{ borderRadius: 12, border: "1px solid #e6ebe8" }} />
            <Bar dataKey="value" name="Receita" radius={[7, 7, 0, 0]} maxBarSize={46}>
              {data.map((d, i) => (
                <Cell key={d.month} fill={i === data.length - 1 ? "var(--brand)" : "color-mix(in srgb, var(--accent) 70%, var(--brand))"} />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>
    </Card>
  );
}
