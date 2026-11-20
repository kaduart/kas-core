import Link from "next/link";
import type { ReactNode } from "react";
import { Eye, Lock, PauseCircle, PlugZap } from "lucide-react";
import { Button } from "@/components/ui/button";
import { MISSING_LABEL, moduleAccess, type ModuleKey } from "@/lib/kas/contract";
import { MODULES } from "@/lib/modules";
import { getSession, sessionTenant } from "@/lib/session";

const panel = "max-w-2xl rounded-[22px] bg-white p-6 shadow-[0_1px_3px_rgba(15,31,23,.05)]";

/**
 * Decide no servidor se a tela do módulo abre. Quem decide é o contrato devolvido pelo KAS Core
 * (ou a simulação no modo demonstração); o navegador só recebe o resultado.
 * Isto é apresentação: as APIs do Go recusam de novo (403 module_not_contracted / module_paused).
 */
export async function ModuleGate({ module, children }: { module: ModuleKey; children: ReactNode }) {
  const session = await getSession();
  const tenant = sessionTenant(session);
  const access = moduleAccess(session.mode, session.modules.modules, module);
  const label = MODULES.find((m) => m.key === module)?.label ?? module;

  if (!access.allowed) {
    const copy = {
      not_contracted: {
        icon: Lock,
        title: `${label} não faz parte do plano da ${tenant.name}`,
        body: session.mode === "demo" ? "Ligue o módulo no catálogo para ver esta tela." : "Para contratar, fale com a equipe KAS.",
      },
      paused: {
        icon: PauseCircle,
        title: `${label} está pausado para a ${tenant.name}`,
        body: "Os dados continuam guardados. Enquanto estiver pausado, nada novo entra e nada é exibido.",
      },
      planned: {
        icon: Lock,
        title: `${label} ainda não está disponível`,
        body: "O módulo está planejado no KAS Core e ainda não pode ser contratado.",
      },
    }[access.reason];
    const Icon = copy.icon;
    return (
      <section className={panel}>
        <span className="mb-3 grid size-12 place-items-center rounded-2xl bg-brand-tint text-brand-dark">
          <Icon className="size-6" aria-hidden />
        </span>
        <h2 className="text-xl font-bold">{copy.title}</h2>
        <p className="mt-1 text-muted">{copy.body}</p>
        <Button asChild className="mt-4">
          <Link href="/modulos">Ver módulos</Link>
        </Button>
      </section>
    );
  }

  const banner = access.preview
    ? { icon: Eye, tone: "border-dashed border-[#c9d3cd] bg-white/60 text-[#45544d]", text: "Prévia de interface. Este módulo está planejado no KAS Core e os dados são fictícios." }
    : access.missing?.length
      ? {
          icon: PlugZap,
          tone: "border-[#f2d5a8] bg-[#fff7ea] text-[#7a4a00]",
          text: `Falta conectar ${access.missing.map((k) => MISSING_LABEL[k] ?? k).join(" e ")}. Os números abaixo ficam incompletos até lá.`,
        }
      : null;

  return (
    <>
      {banner ? (
        <p role="status" className={`mb-4 flex items-start gap-2.5 rounded-2xl border px-4 py-3 text-[14px] leading-snug ${banner.tone}`}>
          <banner.icon className="mt-0.5 size-[18px] shrink-0" aria-hidden />
          {banner.text}
        </p>
      ) : null}
      {children}
    </>
  );
}
