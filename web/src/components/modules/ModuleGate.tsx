"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { Lock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { MODULES } from "@/lib/modules";
import { useTenant } from "@/lib/tenant/TenantProvider";
import type { ModuleKey } from "@/lib/tenant/types";

/** Mostra a tela só se o módulo estiver ativo para a empresa. A decisão real vem do KAS Core. */
export function ModuleGate({ module, children }: { module: ModuleKey; children: ReactNode }) {
  const { tenant } = useTenant();
  if (tenant.modules.includes(module)) return <>{children}</>;
  const info = MODULES.find((m) => m.key === module);
  return (
    <section className="max-w-2xl rounded-[22px] bg-white p-6 shadow-[0_1px_3px_rgba(15,31,23,.05)]">
      <span className="mb-3 grid size-12 place-items-center rounded-2xl bg-brand-tint text-brand-dark">
        <Lock className="size-6" aria-hidden />
      </span>
      <h2 className="text-xl font-bold">{info?.label} não está ativo para {tenant.name}</h2>
      <p className="mt-1 text-muted">Ative o módulo no catálogo para liberar esta tela.</p>
      <Button asChild className="mt-4">
        <Link href="/modulos">Abrir catálogo de módulos</Link>
      </Button>
    </section>
  );
}
