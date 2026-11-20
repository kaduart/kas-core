"use client";

import { useState } from "react";
import { Workflow } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { PageHeader, Stat } from "@/components/ui/page-header";
import { Switch } from "@/components/ui/switch";
import { PROCESSES } from "@/lib/kas/modules-fixtures";

export function ProcessView() {
  const [rows, setRows] = useState(PROCESSES);
  const running = rows.reduce((a, r) => a + (r.active ? r.running : 0), 0);
  const late = rows.reduce((a, r) => a + (r.active ? r.late : 0), 0);
  const soon = rows.reduce((a, r) => a + (r.active ? r.dueSoon : 0), 0);
  return (
    <>
      <PageHeader
        icon={Workflow}
        title="Process Studio"
        description="Fluxos com etapas, responsáveis e prazos, executados pela KAS."
        actions={<Button onClick={() => toast("O editor visual (React Flow) abre aqui na integração com o studio/.")}>Novo fluxo</Button>}
      />
      <div className="mb-4 grid grid-cols-3 gap-2.5 md:gap-4">
        <Stat label="Em andamento" value={String(running)} />
        <Stat label="Prazo vencendo" value={String(soon)} hint="nas próximas 24 h" />
        <Stat label="Atrasados" value={String(late)} />
      </div>
      <div className="grid gap-3 md:grid-cols-2">
        {rows.map((r) => (
          <Card key={r.id} className="flex flex-col gap-3">
            <div className="flex items-start gap-3">
              <div className="min-w-0 flex-1">
                <h3 className="text-[17px] font-bold">{r.name}</h3>
                <p className="text-[13px] text-muted">{r.steps} etapas</p>
              </div>
              <label className="flex items-center gap-2 text-[13px] font-medium">
                {r.active ? "Ativo" : "Pausado"}
                <Switch
                  checked={r.active}
                  onCheckedChange={(v) => {
                    setRows((all) => all.map((x) => (x.id === r.id ? { ...x, active: v } : x)));
                    toast(v ? `${r.name} ativado.` : `${r.name} pausado.`);
                  }}
                  aria-label={`Ativar ${r.name}`}
                />
              </label>
            </div>
            <div className="flex flex-wrap gap-2">
              <Badge tone="info">{r.running} em andamento</Badge>
              {r.dueSoon ? <Badge tone="warn">{r.dueSoon} vencendo</Badge> : null}
              {r.late ? <Badge tone="danger">{r.late} atrasado</Badge> : null}
              {!r.running && !r.late ? <Badge tone="neutral">sem execuções</Badge> : null}
            </div>
            <Button variant="outline" size="sm" className="self-start" onClick={() => toast("Abriria o fluxo no editor visual.")}>
              Abrir no editor
            </Button>
          </Card>
        ))}
      </div>
    </>
  );
}
