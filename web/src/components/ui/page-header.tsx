import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

export function PageHeader({ icon: Icon, title, description, actions }: { icon: LucideIcon; title: string; description: string; actions?: ReactNode }) {
  return (
    <div className="mb-4 flex flex-wrap items-end gap-3">
      <div className="flex min-w-0 items-center gap-3">
        <span className="grid size-12 shrink-0 place-items-center rounded-2xl bg-brand-tint text-brand-dark">
          <Icon className="size-6" strokeWidth={1.8} aria-hidden />
        </span>
        <div className="min-w-0">
          <h2 className="text-[22px] font-extrabold tracking-tight md:text-[26px]">{title}</h2>
          <p className="text-[14px] text-muted">{description}</p>
        </div>
      </div>
      {actions ? <div className="ml-auto flex flex-wrap gap-2">{actions}</div> : null}
    </div>
  );
}

export function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-[20px] bg-white px-4 py-3.5 shadow-[0_1px_3px_rgba(15,31,23,.05)]">
      <small className="block text-[13px] font-semibold text-muted">{label}</small>
      <b className="block text-[24px] font-extrabold tracking-tight tabular-nums md:text-[28px]">{value}</b>
      {hint ? <span className="text-[12.5px] text-muted">{hint}</span> : null}
    </div>
  );
}
