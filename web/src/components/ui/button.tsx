import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export const buttonVariants = cva(
  "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-xl font-semibold transition-[background,transform,opacity] active:scale-[.97] disabled:pointer-events-none disabled:opacity-50 [&_svg]:size-4 [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default: "bg-brand text-white hover:bg-brand-dark",
        accent: "bg-accent text-on-accent hover:brightness-95",
        outline: "border border-[#d5ddd8] bg-white text-ink hover:bg-canvas",
        ghost: "text-ink hover:bg-brand-tint",
        soft: "bg-brand-tint text-brand-ink hover:brightness-95",
      },
      size: {
        default: "min-h-11 px-4 text-sm",
        sm: "min-h-9 px-3 text-[13px]",
        lg: "min-h-12 px-5 text-[15px]",
        icon: "size-11",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);

export function Button({ className, variant, size, asChild, ...props }: ComponentProps<"button"> & VariantProps<typeof buttonVariants> & { asChild?: boolean }) {
  const Comp = asChild ? Slot : "button";
  return <Comp data-slot="button" className={cn(buttonVariants({ variant, size }), className)} {...props} />;
}
