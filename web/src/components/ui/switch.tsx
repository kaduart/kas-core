"use client";

import * as S from "@radix-ui/react-switch";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export function Switch({ className, ...p }: ComponentProps<typeof S.Root>) {
  return (
    <S.Root
      className={cn(
        "relative inline-flex h-7 w-12 shrink-0 cursor-pointer items-center rounded-full bg-[#cfd8d3] transition-colors data-[state=checked]:bg-brand disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...p}
    >
      <S.Thumb className="block size-[22px] translate-x-[3px] rounded-full bg-white shadow transition-transform data-[state=checked]:translate-x-[23px]" />
    </S.Root>
  );
}
