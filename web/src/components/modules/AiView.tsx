"use client";

import { Bot } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader, Stat } from "@/components/ui/page-header";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { AI_CLASSES, AI_RECENT } from "@/lib/kas/modules-fixtures";

export function AiView() {
  const total = AI_CLASSES.reduce((a, c) => a + c.n, 0);
  const max = Math.max(...AI_CLASSES.map((c) => c.n));
  return (
    <>
      <PageHeader icon={Bot} title="Amanda IA" description="Atende no WhatsApp, classifica cada contato e chama a equipe quando precisa." />
      <div className="mb-4 grid grid-cols-2 gap-2.5 md:gap-4 xl:grid-cols-4">
        <Stat label="Conversas atendidas" value={String(total)} hint="últimos 30 dias" />
        <Stat label="Resolvidas sem a equipe" value="71%" />
        <Stat label="Passadas para a equipe" value="68" />
        <Stat label="Primeira resposta" value="8 s" hint="tempo médio" />
      </div>
      <div className="grid gap-4 xl:grid-cols-[1fr_1.4fr]">
        <Card>
          <CardHeader>
            <CardTitle>O que as pessoas querem</CardTitle>
          </CardHeader>
          <ul className="flex flex-col gap-3">
            {AI_CLASSES.map((c) => (
              <li key={c.label}>
                <div className="mb-1 flex justify-between text-[14px]">
                  <span>{c.label}</span>
                  <b className="tabular-nums">{c.n}</b>
                </div>
                <div className="h-2.5 overflow-hidden rounded-full bg-[#eef1ef]">
                  <div className="h-full rounded-full bg-brand" style={{ width: `${(c.n / max) * 100}%` }} />
                </div>
              </li>
            ))}
          </ul>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>Últimas classificações</CardTitle>
          </CardHeader>
          <Table>
            <THead>
              <TR>
                <TH>Contato</TH>
                <TH>Assunto</TH>
                <TH>O que a Amanda fez</TH>
              </TR>
            </THead>
            <TBody>
              {AI_RECENT.map((r) => (
                <TR key={r.who}>
                  <TD>
                    <b className="block font-semibold">{r.who}</b>
                    <small className="text-muted">“{r.said}”</small>
                  </TD>
                  <TD>
                    <Badge tone="info">{r.cls}</Badge>
                  </TD>
                  <TD>
                    {r.action}
                    {r.handoff ? <Badge tone="warn" className="ml-2">equipe</Badge> : null}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </Card>
      </div>
    </>
  );
}
