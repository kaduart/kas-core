"use client";

import Link from "next/link";
import { useState } from "react";
import { CircleGauge, Plus } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { MODULES } from "@/lib/modules";
import type { ModuleAccess } from "@/lib/kas/contract";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { cn } from "@/lib/cn";

function StatusPill({ access }: { access: ModuleAccess }) {
  if (access.allowed && !access.preview && access.missing?.length) return <Badge tone="warn">Falta integrar</Badge>;
  return <Badge>Ativo</Badge>;
}

export function ModulesRow() {
  const { access } = useTenant();
  const mods = MODULES.map((m) => ({ ...m, access: access(m.key) })).filter((m) => m.access.allowed && !m.access.preview);
  const [sel, setSel] = useState(mods[0]?.key);
  return (
    <section aria-label="Seus Módulos" className="mb-[18px]">
      <div className="mb-3 flex items-center gap-2.5">
        <CircleGauge className="size-[22px] text-brand-dark" aria-hidden />
        <h2 className="text-[21px] font-bold tracking-tight">Seus Módulos</h2>
      </div>
      <div className="-mr-3.5 flex snap-x snap-mandatory gap-3 overflow-x-auto pb-1.5 pr-3.5 lg:mr-0 lg:grid lg:grid-cols-[repeat(4,minmax(0,1fr))] lg:overflow-visible lg:pr-0 2xl:grid-cols-[repeat(6,minmax(0,1fr))_minmax(0,.62fr)]">
        {mods.map((m) => {
          const Icon = m.icon;
          const on = sel === m.key;
          return (
            <Link
              key={m.key}
              href={`/${m.slug}`}
              onMouseEnter={() => setSel(m.key)}
              onFocus={() => setSel(m.key)}
              className={cn(
                "flex min-h-[126px] shrink-0 basis-[62%] snap-start flex-col gap-1 rounded-[18px] border-2 bg-white px-4 py-3.5 shadow-[0_1px_3px_rgba(15,31,23,.05)] transition hover:-translate-y-0.5 sm:basis-[38%] lg:basis-auto",
                on ? "border-accent" : "border-transparent",
              )}
              style={on ? { background: "color-mix(in srgb, var(--accent) 10%, #fff)" } : undefined}
            >
              <span className="mb-1.5 flex items-center justify-between text-brand-dark">
                <Icon className="size-[22px]" strokeWidth={1.8} aria-hidden />
                <StatusPill access={m.access} />
              </span>
              <b className="text-[17.5px] font-bold">{m.label}</b>
              <span className="text-[13.5px] leading-snug text-muted">{m.description}</span>
            </Link>
          );
        })}
        <Link
          href="/modulos"
          className="flex min-h-[126px] shrink-0 basis-[40%] snap-start flex-col items-center justify-center gap-1 rounded-[18px] border-[1.5px] border-[#d5ddd8] text-center text-brand-dark sm:basis-[24%] lg:basis-auto"
        >
          <Plus className="size-[26px]" aria-hidden />
          <b className="text-[15.5px]">Adicionar Módulo</b>
        </Link>
      </div>
    </section>
  );
}
