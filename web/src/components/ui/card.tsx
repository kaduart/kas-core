import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export function Card({ className, ...props }: ComponentProps<"section">) {
  return <section data-slot="card" className={cn("rounded-[22px] bg-white px-5 py-[18px] shadow-[0_1px_3px_rgba(15,31,23,.05)]", className)} {...props} />;
}

export function CardHeader({ className, ...props }: ComponentProps<"div">) {
  return <div className={cn("mb-3.5 flex flex-wrap items-center gap-2.5", className)} {...props} />;
}

export function CardTitle({ className, ...props }: ComponentProps<"h2">) {
  return <h2 className={cn("text-lg font-bold tracking-tight md:text-[21px]", className)} {...props} />;
}
