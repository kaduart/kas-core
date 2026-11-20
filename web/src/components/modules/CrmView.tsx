"use client";

import { useMemo, useState } from "react";
import { ArrowRight, Search, UsersRound } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { PageHeader } from "@/components/ui/page-header";
import { brl } from "@/lib/kas/api";
import { LEADS, STAGES, type Lead } from "@/lib/kas/modules-fixtures";

export function CrmView() {
  const [leads, setLeads] = useState<Lead[]>(LEADS);
  const [q, setQ] = useState("");
  const shown = useMemo(() => {
    const t = q.trim().toLowerCase();
    return t ? leads.filter((l) => `${l.name} ${l.interest} ${l.source}`.toLowerCase().includes(t)) : leads;
  }, [leads, q]);

  const advance = (l: Lead) => {
    const i = STAGES.findIndex((s) => s.key === l.stage);
    const next = STAGES[i + 1];
    if (!next) return;
    setLeads((all) => all.map((x) => (x.id === l.id ? { ...x, stage: next.key, since: "agora" } : x)));
    toast.success(`${l.name} foi para ${next.label}.`);
  };

  return (
    <>
      <PageHeader icon={UsersRound} title="CRM" description="Cada contato, da primeira mensagem ao pacote fechado." />
      <label className="relative mb-4 block max-w-md">
        <Search className="pointer-events-none absolute left-3.5 top-1/2 size-[18px] -translate-y-1/2 text-muted" aria-hidden />
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Buscar por nome, interesse ou origem" className="pl-10" aria-label="Buscar lead" />
      </label>
      <div className="-mr-3.5 flex snap-x snap-mandatory gap-3 overflow-x-auto pb-2 pr-3.5 lg:mr-0 lg:grid lg:grid-cols-5 lg:overflow-visible lg:pr-0">
        {STAGES.map((s, si) => {
          const col = shown.filter((l) => l.stage === s.key);
          const total = col.reduce((a, l) => a + l.value, 0);
          return (
            <section key={s.key} aria-label={s.label} className="flex w-[78vw] max-w-[320px] shrink-0 snap-start flex-col rounded-[22px] bg-[#e9eeeb] p-2.5 lg:w-auto lg:max-w-none">
              <header className="flex items-center gap-2 px-1.5 pb-2 pt-1">
                <b className="text-[15px]">{s.label}</b>
                <Badge tone="neutral">{col.length}</Badge>
                <span className="ml-auto text-[12.5px] text-muted">{brl(total)}</span>
              </header>
              <ul className="flex flex-col gap-2">
                {col.map((l) => (
                  <li key={l.id} className="rounded-2xl bg-white p-3 shadow-[0_1px_2px_rgba(15,31,23,.06)]">
                    <div className="flex items-start gap-2">
                      <b className="flex-1 text-[14.5px] leading-tight">{l.name}</b>
                      <small className="whitespace-nowrap text-[12px] text-muted">{l.since}</small>
                    </div>
                    <p className="mt-1 text-[13px] leading-snug text-[#2a3a33]">{l.interest}</p>
                    <div className="mt-2 flex items-center gap-2">
                      <Badge tone="info">{l.source}</Badge>
                      {si < STAGES.length - 1 ? (
                        <Button size="sm" variant="ghost" className="ml-auto px-2" onClick={() => advance(l)} aria-label={`Mover ${l.name} para ${STAGES[si + 1].label}`}>
                          Avançar <ArrowRight aria-hidden />
                        </Button>
                      ) : (
                        <Badge tone="solid" className="ml-auto">fechado</Badge>
                      )}
                    </div>
                  </li>
                ))}
                {col.length === 0 ? <li className="px-2 py-4 text-center text-[13px] text-muted">Nenhum contato nesta etapa.</li> : null}
              </ul>
            </section>
          );
        })}
      </div>
    </>
  );
}
