import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export function Input({ className, ...props }: ComponentProps<"input">) {
  return (
    <input
      data-slot="input"
      className={cn("min-h-11 w-full rounded-xl border border-[#d5ddd8] bg-white px-3.5 text-[15px] text-ink placeholder:text-muted", className)}
      {...props}
    />
  );
}
