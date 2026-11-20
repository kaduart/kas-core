import type { BrandIcon } from "@/lib/tenant/types";

/** Ícone da marca. Usa a cor de destaque da empresa; o traço claro fica branco. */
export function BrandMark({ icon, size = 46 }: { icon: BrandIcon; size?: number }) {
  const common = { width: size, height: size, viewBox: "0 0 48 48", "aria-hidden": true } as const;
  switch (icon) {
    case "leaf":
      return (
        <svg {...common}>
          <path d="M8 40C8 18 22 8 42 8c0 20-10 34-32 34" fill="var(--accent)" />
          <path d="M8 40 30 18" stroke="#fff" strokeWidth="4" strokeLinecap="round" />
        </svg>
      );
    case "heart":
      return (
        <svg {...common}>
          <path d="M24 41S6 30 6 17a9 9 0 0 1 18-3 9 9 0 0 1 18 3c0 13-18 24-18 24z" fill="var(--accent)" />
          <path d="M13 22h7l3-6 4 12 3-6h6" stroke="#fff" strokeWidth="3.5" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "tooth":
      return (
        <svg {...common}>
          <path
            d="M14 6c-6 0-9 5-8 11 1 7 4 9 5 15 1 5 2 10 5 10s3-8 5-12c1-2 5-2 6 0 2 4 2 12 5 12s4-5 5-10c1-6 4-8 5-15 1-6-2-11-8-11-4 0-6 2-10 2s-6-2-10-2z"
            fill="var(--accent)"
          />
          <path d="M17 13c2 0 4 1 7 1" stroke="#fff" strokeWidth="3" strokeLinecap="round" />
        </svg>
      );
    default:
      return (
        <svg {...common}>
          <path d="M9 7 L39 41" stroke="var(--accent)" strokeWidth="10" strokeLinecap="round" />
          <path d="M39 7 L9 41" stroke="#fff" strokeWidth="10" strokeLinecap="round" />
        </svg>
      );
  }
}

export function BrandName({ name1, name2, className, style }: { name1: string; name2: string; className?: string; style?: React.CSSProperties }) {
  return (
    <span className={className} style={style}>
      <span className="font-bold">{name1}</span>
      <span className="font-normal">{name2}</span>
    </span>
  );
}
