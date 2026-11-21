import { CircleCheck, CircleAlert } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { sourceLabel, type RevenueLevel, type RevenueLine, type RevenueResponse } from "@/lib/kas/contract";
import { cents } from "@/lib/kas/api";

/** Cores por origem, as mesmas do painel de demonstração. */
const ORIGIN_COLOR: Record<string, string> = {
  google_ads: "#0E9F55",
  meta_ads: "#F0216F",
  tiktok_ads: "#FF8A1F",
  organic: "#2468F2",
};
const EXTRA = ["#7A4FE0", "#14A3B8", "#C08A00", "#5B6B63"];
export const UNLINKED_COLOR = "#C9CED1";

export function originColor(origin: string, i = 0): string {
  return ORIGIN_COLOR[origin] ?? EXTRA[i % EXTRA.length];
}

export const LEVEL_LABEL: Record<RevenueLevel, string> = { comprovada: "Comprovada", provavel: "Provável", sem_vinculo: "Sem vínculo" };
export const LEVEL_HINT: Record<RevenueLevel, string> = {
  comprovada: "clique do anúncio ligado só a este cliente",
  provavel: "sinal mais fraco ou contato compartilhado",
  sem_vinculo: "pagamento sem origem de anúncio",
};

export function LevelBadge({ level }: { level: RevenueLevel }) {
  return <Badge tone={level === "comprovada" ? "brand" : level === "provavel" ? "warn" : "neutral"}>{LEVEL_LABEL[level]}</Badge>;
}

/** Receita líquida por origem (comprovada + provável), mais a fatia "Sem vínculo". Valores em reais. */
export function byOrigin(r: RevenueResponse): { channel: string; color: string; amount: number }[] {
  const acc = new Map<string, number>();
  for (const l of r.linhas) acc.set(l.origem, (acc.get(l.origem) ?? 0) + l.liquido_centavos);
  const rows = [...acc.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([o, c], i) => ({ channel: sourceLabel(o), color: originColor(o, i), amount: c / 100 }));
  if (r.sem_vinculo.liquido_centavos !== 0) rows.push({ channel: "Sem vínculo", color: UNLINKED_COLOR, amount: r.sem_vinculo.liquido_centavos / 100 });
  return rows.filter((x) => x.amount > 0);
}

export const lineName = (l: RevenueLine) => l.campanha || (l.ad_id ? `Anúncio ${l.ad_id}` : "Sem campanha");

/** Selo da prova de fechamento: comprovada + provável + sem vínculo = total líquido. */
export function InvariantSeal({ r }: { r: RevenueResponse }) {
  const ok = r.invariante.fecha;
  return (
    <p className={`flex items-start gap-2 rounded-[14px] px-3 py-2.5 text-[13.5px] ${ok ? "bg-brand-tint text-brand-ink" : "bg-[#f8e3dd] text-[#8f3420]"}`}>
      {ok ? <CircleCheck className="mt-px size-[18px] shrink-0" aria-hidden /> : <CircleAlert className="mt-px size-[18px] shrink-0" aria-hidden />}
      <span>
        {ok ? (
          <>
            <b className="font-semibold">A conta fecha.</b> Comprovada + provável + sem vínculo = {cents(r.total_liquido_centavos)}, centavo por centavo.
          </>
        ) : (
          <>
            <b className="font-semibold">A conta não fecha</b> ({cents(r.invariante.soma_niveis_centavos)} contra {cents(r.total_liquido_centavos)}). Avise o suporte KAS.
          </>
        )}
      </span>
    </p>
  );
}
