"use client";

import { Button } from "@/components/ui/button";

/** Falha ao falar com o KAS Core (fora do ar, contrato diferente, empresa errada). */
export default function PainelError({ error, reset }: { error: Error & { digest?: string }; reset: () => void }) {
  return (
    <section role="alert" className="max-w-2xl rounded-[22px] bg-white p-6 shadow-[0_1px_3px_rgba(15,31,23,.05)]">
      <h2 className="text-xl font-bold">Esta tela não carregou</h2>
      <p className="mt-1 text-muted">O painel não conseguiu confirmar os dados com o KAS Core. Tente de novo; se continuar, avise a equipe KAS com o código abaixo.</p>
      {error.digest ? <code className="mt-3 block text-[13px] text-muted">{error.digest}</code> : null}
      <Button className="mt-4" onClick={reset}>Tentar de novo</Button>
    </section>
  );
}
