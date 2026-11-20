"use client";

import Link from "next/link";
import { Boxes } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { PageHeader } from "@/components/ui/page-header";
import { Switch } from "@/components/ui/switch";
import { ACTIVATABLE, DEPENDS_ON, MISSING_LABEL, type ModuleKey, type ModuleState } from "@/lib/kas/contract";
import { MODULES, VERTICAL_EXTENSIONS } from "@/lib/modules";
import { useTenant } from "@/lib/tenant/TenantProvider";

const STATE_LABEL: Record<ModuleState["contract"], { text: string; tone: "brand" | "neutral" | "warn" | "info" }> = {
  active: { text: "Contratado", tone: "brand" },
  paused: { text: "Pausado", tone: "warn" },
  absent: { text: "Não contratado", tone: "neutral" },
  planned: { text: "Planejado", tone: "info" },
};

/** Quem depende deste módulo e está ativo (ex.: Relatórios depende de Attribution). */
function activeDependents(key: ModuleKey, modules: ModuleState[]) {
  return modules.filter((m) => m.contract === "active" && DEPENDS_ON[m.key]?.includes(key));
}

export function CatalogView() {
  const { tenant, mode, modules, setDemoModule } = useTenant();
  const vertical = VERTICAL_EXTENSIONS[tenant.vertical];
  const byKey = (k: ModuleKey) => modules.find((m) => m.key === k);
  const label = (k: ModuleKey) => MODULES.find((m) => m.key === k)?.label ?? k;

  return (
    <>
      <PageHeader
        icon={Boxes}
        title="Módulos"
        description={mode === "demo" ? `Simule o plano da ${tenant.name}. O menu e o painel acompanham.` : `O que está contratado para a ${tenant.name}.`}
      />

      <div className="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        {MODULES.map((m) => {
          const Icon = m.icon;
          const st = byKey(m.key);
          const contract = st?.contract ?? "absent";
          const state = STATE_LABEL[contract];
          const on = contract === "active";
          const canToggle = mode === "demo" && ACTIVATABLE.includes(m.key);
          const deps = DEPENDS_ON[m.key] ?? [];
          const depsOff = deps.filter((d) => byKey(d)?.contract !== "active");
          const blockers = activeDependents(m.key, modules);
          const missing = on && st?.integration && !st.integration.ready ? st.integration.missing : [];

          let hint: string | null = null;
          if (!on && depsOff.length) hint = `Precisa de ${depsOff.map(label).join(" e ")} ativo.`;
          else if (on && blockers.length) hint = `${blockers.map((b) => label(b.key)).join(" e ")} usa este módulo.`;
          else if (missing.length) hint = `Falta conectar ${missing.map((k) => MISSING_LABEL[k] ?? k).join(" e ")}.`;
          else if (contract === "planned") hint = "Ainda não pode ser contratado.";
          const previewHref = mode === "demo" && contract === "planned" ? `/${m.slug}` : null;

          const disabled = (!on && depsOff.length > 0) || (on && blockers.length > 0);

          return (
            <Card key={m.key} className={contract === "planned" ? "flex items-start gap-3 opacity-80" : "flex items-start gap-3"}>
              <span className="grid size-11 shrink-0 place-items-center rounded-2xl bg-brand-tint text-brand-dark">
                <Icon className="size-[22px]" aria-hidden />
              </span>
              <div className="min-w-0 flex-1">
                <b className="block text-[16px]">{m.label}</b>
                <p className="text-[13px] text-muted">{m.description}</p>
                <div className="mt-2 flex flex-wrap items-center gap-1.5">
                  <Badge tone={state.tone}>{state.text}</Badge>
                  {missing.length ? <Badge tone="warn">Falta integrar</Badge> : null}
                </div>
                {hint ? <p className="mt-1.5 text-[12.5px] text-muted">{hint}</p> : null}
                {previewHref ? (
                  <Link href={previewHref} className="mt-1.5 inline-block text-[13px] font-semibold text-brand-dark underline underline-offset-2">
                    Ver prévia da tela
                  </Link>
                ) : null}
              </div>
              {canToggle ? (
                <Switch
                  checked={on}
                  disabled={disabled}
                  onCheckedChange={(v) => {
                    setDemoModule(m.key, v ? "active" : "absent");
                    toast(v ? `${m.label} ligado para ${tenant.name}.` : `${m.label} desligado para ${tenant.name}.`);
                  }}
                  aria-label={`Ligar ${m.label}`}
                />
              ) : null}
            </Card>
          );
        })}
      </div>

      {vertical ? (
        <>
          <h3 className="mb-1 text-[15px] font-bold">{vertical.title}</h3>
          <p className="mb-2.5 text-[13px] text-muted">Produtos só para o segmento da {tenant.name}, fora do catálogo comum. Ainda em construção.</p>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
            {vertical.items.map((m) => {
              const Icon = m.icon;
              return (
                <Card key={m.label} className="flex flex-col gap-2 border-[1.5px] border-dashed border-[#d5ddd8] bg-transparent shadow-none">
                  <span className="flex items-center justify-between">
                    <Icon className="size-[22px] text-brand-dark" aria-hidden />
                    <Badge tone="neutral">Em breve</Badge>
                  </span>
                  <b className="text-[16px]">{m.label}</b>
                  <p className="text-[13px] text-muted">{m.description}</p>
                </Card>
              );
            })}
          </div>
        </>
      ) : null}

      <p className="mt-4 text-[12.5px] text-muted">
        {mode === "demo"
          ? "Modo demonstração: a escolha fica salva neste navegador. Na operação real, quem contrata, pausa e libera é o KAS Core."
          : "Para mudar o plano, fale com a equipe KAS."}
      </p>
    </>
  );
}
