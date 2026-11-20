import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

const badgeVariants = cva("inline-flex items-center gap-1 whitespace-nowrap rounded-full px-2.5 py-0.5 text-[12.5px] font-semibold", {
  variants: {
    tone: {
      brand: "bg-brand-tint text-brand-ink",
      neutral: "bg-[#eef1ef] text-[#45544d]",
      warn: "bg-[#fdebd3] text-[#8a4b00]",
      danger: "bg-[#f8e3dd] text-[#a3402b]",
      info: "bg-[#e1e9fb] text-[#2c4683]",
      solid: "bg-brand text-white",
    },
  },
  defaultVariants: { tone: "brand" },
});

export function Badge({ className, tone, ...props }: ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return <span data-slot="badge" className={cn(badgeVariants({ tone }), className)} {...props} />;
}
