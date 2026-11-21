"use client";

import { useState } from "react";
import Link from "next/link";
import { ArrowRight, BadgeCheck, CircleHelp, Database, Scale, Unlink } from "lucide-react";
import { CampaignCard } from "@/components/dashboard/CampaignCard";
import { Card, CardHead } from "@/components/dashboard/Card";
import { ModulesRow } from "@/components/dashboard/ModulesRow";
import { PromoCard } from "@/components/dashboard/PromoCard";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cents, num, revenueErrorText, useRevenue } from "@/lib/kas/api";
import { fmtDate } from "@/lib/kas/period";
import type { Period } from "@/lib/kas/types";
import { cn } from "@/lib/cn";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { byOrigin, InvariantSeal, lineName, LevelBadge } from "./revenue-ui";

export function PeriodTabs({ value, onChange }: { value: Period; onChange: (p: Period) => void }) {
  return (
    <Tabs value={value} onValueChange={(v) => onChange(v as Period)}>
      <TabsList aria-label="Período">
        <TabsTrigger value="7d">7 dias</TabsTrigger>
        <TabsTrigger value="30d">30 dias</TabsTrigger>
        <TabsTrigger value="90d">90 dias</TabsTrigger>
      </TabsList>
    </Tabs>
  );
}

/** Painel do modo real: só números que o KAS Core calculou. Nada inventado, nada de variação sem base. */
export function LiveDashboard() {
  const { tenant, access } = useTenant();
  const [period, setPeriod] = useState<Period>("30d");
  const canRevenue = access("attribution").allowed;
  const { data, error, isError, isFetching, refetch } = useRevenue(tenant.id, period, canRevenue);

  const total = data?.total_liquido_centavos ?? 0;
  const lvl = (k: string) => data?.por_nivel_centavos[k] ?? 0;
  const share = (v: number) => (total > 0 ? `${Math.round((v / total) * 100)}% do total` : "—");
  const tiles = [
    { key: "net", label: "Receita líquida", value: total, sub: data ? `${fmtDate(data.periodo.de)} a ${fmtDate(data.periodo.ate)}` : "", icon: Database, hot: true },
    { key: "comprovada", label: "Comprovada", value: lvl("comprovada"), sub: share(lvl("comprovada")), icon: BadgeCheck },
    { key: "provavel", label: "Provável", value: lvl("provavel"), sub: share(lvl("provavel")), icon: Scale },
    { key: "sem_vinculo", label: "Sem vínculo", value: lvl("sem_vinculo"), sub: share(lvl("sem_vinculo")), icon: Unlink },
  ];

  if (!canRevenue) {
    return (
      <>
        <ModulesRow />
        <Card label="Receita">
          <p className="font-semibold">Receita por campanha não está contratada para {tenant.name}.</p>
          <p className="mt-1 text-sm text-muted">Ative o módulo Attribution no catálogo para ver de onde vem cada real recebido.</p>
          <Link href="/modulos" className="mt-4 inline-flex min-h-11 items-center gap-2 rounded-xl bg-brand px-4 font-semibold text-white">
            Ver catálogo <ArrowRight className="size-4" aria-hidden />
          </Link>
        </Card>
      </>
    );
  }

  return (
    <>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <span className="text-[13.5px] text-muted">Dados do KAS Core{isFetching ? " · atualizando…" : ""}</span>
        <span className="ml-auto">
          <PeriodTabs value={period} onChange={setPeriod} />
        </span>
      </div>

      {isError ? (
        <div role="alert" className="mb-[18px] rounded-[22px] bg-white p-6">
          <p className="font-semibold">{revenueErrorText(error)}</p>
          <button onClick={() => refetch()} className="mt-4 min-h-11 rounded-xl bg-brand px-4 font-semibold text-white">
            Tentar de novo
          </button>
        </div>
      ) : null}

      <div className="mb-[18px] grid grid-cols-2 gap-2.5 md:gap-4 xl:grid-cols-4" aria-busy={!data}>
        {tiles.map((k) => {
          const Icon = k.icon;
          return (
            <div
              key={k.key}
              className={cn(
                "flex flex-col items-start gap-2 rounded-[22px] p-3.5 shadow-[0_1px_3px_rgba(15,31,23,.05)] md:flex-row md:items-center md:gap-4 md:px-[18px] md:py-4",
                k.hot ? "text-white" : "bg-white",
              )}
              style={k.hot ? { background: "linear-gradient(135deg, var(--brand) 0%, var(--brand-dark) 100%)" } : undefined}
            >
              <span className={cn("grid size-[42px] shrink-0 place-items-center rounded-full md:size-14", k.hot ? "bg-white/15" : "bg-brand-tint text-brand-dark")}>
                <Icon className="size-[22px] md:size-[26px]" strokeWidth={1.8} aria-hidden />
              </span>
              <div className="min-w-0 flex-1">
                <small className="block text-[13.5px] font-semibold">{k.label}</small>
                {data ? (
                  <strong className="block text-[22px] font-extrabold leading-tight tracking-tight tabular-nums md:text-[30px]">{cents(k.value)}</strong>
                ) : (
                  <span className="my-1.5 block h-7 w-28 animate-pulse rounded-lg bg-current/15" />
                )}
                <span className={cn("block text-[13px]", k.hot ? "text-white/85" : "text-muted")}>{k.sub || " "}</span>
              </div>
            </div>
          );
        })}
      </div>

      {data ? (
        <div className="mb-[18px] grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-[1.12fr_1fr_.98fr]">
          {byOrigin(data).length ? (
            <CampaignCard data={byOrigin(data)} />
          ) : (
            <Card label="Receita por Campanha">
              <CardHead icon={Database} title="Receita por Campanha" />
              <p className="text-[14.5px] text-muted">Nenhum pagamento neste período. Importe os recebimentos ou escolha um período maior.</p>
            </Card>
          )}
          <Card label="Campanhas que mais trouxeram receita">
            <CardHead icon={BadgeCheck} title="Top campanhas" />
            {data.linhas.length ? (
              <ol className="flex flex-col">
                {[...data.linhas]
                  .sort((a, b) => b.liquido_centavos - a.liquido_centavos)
                  .slice(0, 5)
                  .map((l) => (
                    <li key={`${l.nivel}|${l.origem}|${l.campanha}|${l.ad_id}`} className="flex min-h-[52px] items-center gap-3 border-b border-line last:border-0">
                      <span className="min-w-0 flex-1">
                        <b className="block truncate text-[14.5px] font-semibold">{lineName(l)}</b>
                        <small className="text-[12.5px] text-muted">
                          {num(l.clientes)} {l.clientes === 1 ? "cliente" : "clientes"}
                        </small>
                      </span>
                      <LevelBadge level={l.nivel} />
                      <b className="w-[92px] text-right text-[14.5px] tabular-nums">{cents(l.liquido_centavos)}</b>
                    </li>
                  ))}
              </ol>
            ) : (
              <p className="text-[14.5px] text-muted">Nenhum pagamento ligado a campanha neste período.</p>
            )}
            <Link href="/attribution" className="mt-3 inline-flex min-h-11 items-center gap-1.5 text-[14px] font-semibold text-brand-dark">
              Ver todas as campanhas <ArrowRight className="size-4" aria-hidden />
            </Link>
          </Card>
          <div className="md:col-span-2 2xl:col-span-1">
            <Card label="Como o KAS conta" className="h-full">
              <CardHead icon={CircleHelp} title="Como o KAS conta" />
              <InvariantSeal r={data} />
              <dl className="mt-3 grid gap-2 text-[13.5px]">
                <div>
                  <dt className="inline font-semibold">Comprovada: </dt>
                  <dd className="inline text-muted">clique do anúncio ligado só a este cliente.</dd>
                </div>
                <div>
                  <dt className="inline font-semibold">Provável: </dt>
                  <dd className="inline text-muted">só campanha, ou contato usado por mais de um cliente.</dd>
                </div>
                <div>
                  <dt className="inline font-semibold">Sem vínculo: </dt>
                  <dd className="inline text-muted">
                    {num(data.sem_vinculo.lancamentos)} {data.sem_vinculo.lancamentos === 1 ? "lançamento" : "lançamentos"} sem origem de anúncio.
                  </dd>
                </div>
              </dl>
              <p className="mt-3 text-[12.5px] text-muted">Estornos já descontados. Valores em reais, sem arredondar.</p>
            </Card>
          </div>
        </div>
      ) : null}

      <ModulesRow />
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 2xl:grid-cols-[2.06fr_1fr]">
        <Card label="Funil e atividades">
          <CardHead icon={Database} title="Funil e atividades" small />
          <p className="text-[14.5px] text-muted">
            Leads, agendamentos e o histórico do dia aparecem aqui quando o CRM da empresa estiver ligado ao KAS Core. Até lá, o painel mostra só o que já é dado real.
          </p>
        </Card>
        <PromoCard />
      </div>
    </>
  );
}
