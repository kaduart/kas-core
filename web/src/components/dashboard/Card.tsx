import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "@/lib/cn";

export function Card({ className, children, label }: { className?: string; children: ReactNode; label?: string }) {
  return (
    <section aria-label={label} className={cn("rounded-[22px] bg-white px-5 py-[18px] shadow-[0_1px_3px_rgba(15,31,23,.05)]", className)}>
      {children}
    </section>
  );
}

export function CardHead({ icon: Icon, title, children, small }: { icon: LucideIcon; title: string; children?: ReactNode; small?: boolean }) {
  return (
    <div className="mb-3.5 flex flex-wrap items-center gap-2.5">
      <Icon className="size-[22px] text-brand-dark" strokeWidth={1.9} aria-hidden />
      <h2 className={cn("font-bold tracking-tight", small ? "text-lg" : "text-[21px]")}>{title}</h2>
      {children ? <div className="ml-auto">{children}</div> : null}
    </div>
  );
}
