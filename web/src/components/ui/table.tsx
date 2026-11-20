import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export function Table({ className, ...props }: ComponentProps<"table">) {
  return (
    <div className="-mx-1 overflow-x-auto px-1">
      <table data-slot="table" className={cn("w-full min-w-[640px] border-collapse text-sm", className)} {...props} />
    </div>
  );
}
export const THead = ({ className, ...p }: ComponentProps<"thead">) => <thead className={cn("text-left text-[12.5px] text-muted", className)} {...p} />;
export const TBody = (p: ComponentProps<"tbody">) => <tbody {...p} />;
export const TR = ({ className, ...p }: ComponentProps<"tr">) => <tr className={cn("border-b border-line last:border-0", className)} {...p} />;
export const TH = ({ className, ...p }: ComponentProps<"th">) => <th className={cn("px-2 py-2.5 font-semibold", className)} {...p} />;
export const TD = ({ className, ...p }: ComponentProps<"td">) => <td className={cn("px-2 py-3 align-middle", className)} {...p} />;
