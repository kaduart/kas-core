import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { BrandMark } from "@/components/shell/BrandMark";

/** Card da própria KAS no painel do cliente: sempre leva a marca KAS, mesmo em white-label. */
export function PromoCard() {
  return (
    <section
      aria-label="Catálogo de módulos"
      className="relative min-h-[200px] overflow-hidden rounded-[22px] px-6 py-5 text-white"
      style={{ background: "linear-gradient(135deg, var(--brand-dark) 0%, var(--brand-deep) 100%)" }}
    >
      <div className="mb-3 flex items-center gap-2 text-xl">
        <BrandMark icon="x" size={26} />
        <span className="font-bold">KAS</span>
        <span className="-ml-1.5">Core</span>
      </div>
      <h3 className="relative z-[1] max-w-[240px] text-[21px] font-bold leading-tight">Expanda sua operação com novos módulos</h3>
      <p className="relative z-[1] mb-3.5 mt-2 max-w-[240px] text-[13px] opacity-85">Ative apenas o que sua empresa precisa e evolua no seu ritmo.</p>
      <Link
        href="/modulos"
        className="relative z-[1] inline-flex min-h-11 items-center gap-2 rounded-full bg-accent px-5 text-sm font-bold text-on-accent transition active:scale-95"
      >
        Ver catálogo de módulos <ArrowRight className="size-4" aria-hidden />
      </Link>
      <div className="absolute right-5 top-7 h-[150px] w-[130px] opacity-35 sm:opacity-100" aria-hidden>
        {[86, 50, 14].map((top, i) => (
          <span
            key={top}
            className={i === 2 ? "kas-float absolute left-2.5 h-16 w-[110px]" : "absolute left-2.5 h-16 w-[110px]"}
            style={{
              top,
              opacity: 0.85 + i * 0.07,
              background: "linear-gradient(160deg, color-mix(in srgb, var(--accent) 55%, #fff), var(--brand))",
              clipPath: "polygon(50% 0,100% 30%,100% 70%,50% 100%,0 70%,0 30%)",
              filter: "drop-shadow(0 10px 18px rgba(0,0,0,.25))",
            }}
          />
        ))}
      </div>
    </section>
  );
}
