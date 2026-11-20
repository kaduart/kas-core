import Link from "next/link";
import { notFound } from "next/navigation";
import { MODULES } from "@/lib/modules";

const EXTRA: Record<string, { label: string; description: string }> = {
  modulos: { label: "Módulos", description: "Catálogo para ativar ou desativar módulos por empresa." },
};

export function generateStaticParams() {
  return [...MODULES.map((m) => ({ modulo: m.slug })), { modulo: "modulos" }];
}

export default async function ModuloPage({ params }: { params: Promise<{ modulo: string }> }) {
  const { modulo } = await params;
  const info = MODULES.find((m) => m.slug === modulo) ?? EXTRA[modulo];
  if (!info) notFound();
  return (
    <section className="max-w-3xl rounded-[22px] bg-white p-6 shadow-[0_1px_3px_rgba(15,31,23,.05)]">
      <h2 className="text-2xl font-bold tracking-tight">{info.label}</h2>
      <p className="mt-2 text-muted">{info.description}</p>
      <p className="mt-1 text-sm text-muted">Esta tela entra na próxima entrega, depois do painel.</p>
      <Link href="/dashboard" className="mt-5 inline-flex min-h-11 items-center rounded-xl bg-brand px-4 font-semibold text-white">
        Voltar ao Dashboard
      </Link>
    </section>
  );
}
