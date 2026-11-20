"use client";

import { Boxes } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/ui/page-header";
import { Switch } from "@/components/ui/switch";
import { MODULES, PLANNED } from "@/lib/modules";
import { useTenant } from "@/lib/tenant/TenantProvider";

export function CatalogView() {
  const { tenant, setModuleActive } = useTenant();
  return (
    <>
      <PageHeader icon={Boxes} title="Módulos" description={`Ative só o que a ${tenant.name} precisa. O menu e o painel acompanham.`} />
      <h3 className="mb-2.5 text-[15px] font-bold text-muted">Disponíveis</h3>
      <div className="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {MODULES.map((m) => {
          const Icon = m.icon;
          const on = tenant.modules.includes(m.key);
          return (
            <Card key={m.key} className="flex items-start gap-3">
              <span className="grid size-11 shrink-0 place-items-center rounded-2xl bg-brand-tint text-brand-dark">
                <Icon className="size-[22px]" aria-hidden />
              </span>
              <div className="min-w-0 flex-1">
                <b className="block text-[16px]">{m.label}</b>
                <p className="text-[13px] text-muted">{m.description}</p>
                <Badge tone={on ? "brand" : "neutral"} className="mt-2">
                  {on ? "Ativo" : "Desligado"}
                </Badge>
              </div>
              <Switch
                checked={on}
                onCheckedChange={(v) => {
                  setModuleActive(m.key, v);
                  toast(v ? `${m.label} ativado para ${tenant.name}.` : `${m.label} desligado para ${tenant.name}.`);
                }}
                aria-label={`Ativar ${m.label}`}
              />
            </Card>
          );
        })}
      </div>
      <h3 className="mb-2.5 text-[15px] font-bold text-muted">ERP de clínicas, em construção</h3>
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {PLANNED.map((m) => {
          const Icon = m.icon;
          return (
            <Card key={m.label} className="flex flex-col gap-2 border-[1.5px] border-dashed border-[#d5ddd8] bg-transparent shadow-none">
              <span className="flex items-center justify-between">
                <Icon className="size-[22px] text-brand-dark" aria-hidden />
                <Badge tone="warn">Em breve</Badge>
              </span>
              <b className="text-[16px]">{m.label}</b>
              <p className="text-[13px] text-muted">{m.description}</p>
            </Card>
          );
        })}
      </div>
      <p className="mt-4 text-[12.5px] text-muted">Nesta demonstração a escolha vale até recarregar a página. Na integração, quem liga e desliga é o KAS Core, por empresa.</p>
    </>
  );
}
