"use client";

import * as T from "@radix-ui/react-tabs";
import type { ComponentProps } from "react";
import { cn } from "@/lib/cn";

export const Tabs = T.Root;
export function TabsList({ className, ...p }: ComponentProps<typeof T.List>) {
  return <T.List className={cn("inline-flex rounded-xl bg-[#f1f3f2] p-[3px]", className)} {...p} />;
}
export function TabsTrigger({ className, ...p }: ComponentProps<typeof T.Trigger>) {
  return (
    <T.Trigger
      className={cn("min-h-9 rounded-[10px] px-3 text-[13px] font-medium data-[state=active]:bg-brand-dark data-[state=active]:text-white", className)}
      {...p}
    />
  );
}
export const TabsContent = T.Content;
