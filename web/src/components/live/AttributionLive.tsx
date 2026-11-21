"use client";

import { useState } from "react";
import { ChartNoAxesColumnIncreasing, Download } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader, Stat } from "@/components/ui/page-header";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { REASON_LABEL, sourceLabel } from "@/lib/kas/contract";
import { cents, fileUrl, num, revenueErrorText, useRevenue } from "@/lib/kas/api";
import { fmtDate } from "@/lib/kas/period";
import type { Period } from "@/lib/kas/types";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { PeriodTabs } from "./LiveDashboard";
import { InvariantSeal, LevelBadge, lineName, originColor, UNLINKED_COLOR } from "./revenue-ui";

/** Attribution no modo real: as linhas exatamente como o KAS Core calculou. */
export function AttributionLive() {
  const { tenant } = useTenant();
  const [period, setPeriod] = useState<Period>("30d");
  const { data, error, isError, refetch } = useRevenue(tenant.id, period);
  const lvl = (k: string) => data?.por_nivel_centavos[k] ?? 0;
  const origins = data ? [...new Set(data.linhas.map((l) => l.origem))] : [];
  const reasons = data ? Object.entries(data.sem_vinculo.motivos_centavos ?? {}).filter(([, v]) => v !== 0) : [];

  return (
    <>
      <PageHeader
        icon={ChartNoAxesColumnIncreasing}
        title="Attribution"
        description={data ? `De onde veio cada real recebido, de ${fmtDate(data.periodo.de)} a ${fmtDate(data.periodo.ate)}.` : "De onde veio cada real recebido."}
        actions={<PeriodTabs value={period} onChange={setPeriod} />}
      />

      {isError ? (
        <Card role="alert" className="mb-4">
          <p className="font-semibold">{revenueErrorText(error)}</p>
          <Button className="mt-3" onClick={() => refetch()}>Tentar de novo</Button>
        </Card>
      ) : null}

      <div className="mb-4 grid grid-cols-2 gap-2.5 md:gap-4 xl:grid-cols-4">
        <Stat label="Receita líquida" value={data ? cents(data.total_liquido_centavos) : "…"} hint="recebido menos estornos" />
        <Stat label="Comprovada" value={data ? cents(lvl("comprovada")) : "…"} hint="clique ligado só a este cliente" />
        <Stat label="Provável" value={data ? cents(lvl("provavel")) : "…"} hint="sinal mais fraco" />
        <Stat label="Sem vínculo" value={data ? cents(lvl("sem_vinculo")) : "…"} hint="sem origem de anúncio" />
      </div>

      {data ? (
        <div className="mb-4">
          <InvariantSeal r={data} />
        </div>
      ) : null}

      <Card className="mb-4">
        <CardHeader>
          <CardTitle>Campanhas</CardTitle>
          <Button asChild variant="outline" size="sm" className="ml-auto">
            <a href={fileUrl("attribution-csv", period)} download>
              <Download aria-hidden /> Exportar CSV
            </a>
          </Button>
        </CardHeader>
        {!data ? (
          <div className="h-40 animate-pulse rounded-xl bg-canvas" aria-busy="true" aria-label="Carregando campanhas" />
        ) : data.linhas.length === 0 ? (
          <p className="py-6 text-[14.5px] text-muted">Nenhum pagamento ligado a campanha neste período.</p>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Campanha</TH>
                <TH>Nível</TH>
                <TH className="text-right">Clientes</TH>
                <TH className="text-right">Recebido</TH>
                <TH className="text-right">Estornos</TH>
                <TH className="text-right">Líquido</TH>
              </TR>
            </THead>
            <TBody>
              {[...data.linhas]
                .sort((a, b) => b.liquido_centavos - a.liquido_centavos)
                .map((l) => (
                  <TR key={`${l.nivel}|${l.origem}|${l.campanha}|${l.ad_id}`}>
                    <TD>
                      <span className="flex items-center gap-2.5">
                        <span className="size-2.5 shrink-0 rounded-full" style={{ background: originColor(l.origem, origins.indexOf(l.origem)) }} aria-hidden />
                        <span>
                          <b className="block font-semibold">{lineName(l)}</b>
                          <small className="text-muted">
                            {sourceLabel(l.origem)}
                            {l.ad_id && l.campanha ? ` · anúncio ${l.ad_id}` : ""}
                          </small>
                        </span>
                      </span>
                    </TD>
                    <TD>
                      <LevelBadge level={l.nivel} />
                    </TD>
                    <TD className="text-right tabular-nums">{num(l.clientes)}</TD>
                    <TD className="text-right tabular-nums">{cents(l.recebido_centavos)}</TD>
                    <TD className="text-right tabular-nums">{l.estornos_centavos ? `− ${cents(l.estornos_centavos)}` : "—"}</TD>
                    <TD className="text-right font-semibold tabular-nums">{cents(l.liquido_centavos)}</TD>
                  </TR>
                ))}
              {data.sem_vinculo.lancamentos > 0 ? (
                <TR>
                  <TD>
                    <span className="flex items-center gap-2.5">
                      <span className="size-2.5 shrink-0 rounded-full" style={{ background: UNLINKED_COLOR }} aria-hidden />
                      <span>
                        <b className="block font-semibold">Sem vínculo</b>
                        <small className="text-muted">pagamentos sem origem de anúncio</small>
                      </span>
                    </span>
                  </TD>
                  <TD>
                    <LevelBadge level="sem_vinculo" />
                  </TD>
                  <TD className="text-right tabular-nums">{num(data.sem_vinculo.clientes)}</TD>
                  <TD className="text-right tabular-nums">{cents(data.sem_vinculo.recebido_centavos)}</TD>
                  <TD className="text-right tabular-nums">{data.sem_vinculo.estornos_centavos ? `− ${cents(data.sem_vinculo.estornos_centavos)}` : "—"}</TD>
                  <TD className="text-right font-semibold tabular-nums">{cents(data.sem_vinculo.liquido_centavos)}</TD>
                </TR>
              ) : null}
            </TBody>
          </Table>
        )}
      </Card>

      {reasons.length ? (
        <Card>
          <CardHeader>
            <CardTitle>Por que ficou sem vínculo</CardTitle>
          </CardHeader>
          <ul className="flex flex-col">
            {reasons
              .sort((a, b) => b[1] - a[1])
              .map(([k, v]) => (
                <li key={k} className="flex min-h-11 items-center gap-3 border-b border-line text-[14.5px] last:border-0">
                  <span className="flex-1">{REASON_LABEL[k] ?? k}</span>
                  <b className="tabular-nums">{cents(v)}</b>
                </li>
              ))}
          </ul>
        </Card>
      ) : null}
    </>
  );
}
