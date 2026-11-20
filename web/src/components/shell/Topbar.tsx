"use client";

import { Bell, ChevronDown, CircleHelp, Puzzle, Search } from "lucide-react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/cn";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { TenantMenu } from "./TenantMenu";

function greeting(d = new Date()) {
  const h = d.getHours();
  return h < 12 ? "Bom dia" : h < 18 ? "Boa tarde" : "Boa noite";
}

const round =
  "relative hidden size-[52px] shrink-0 place-items-center rounded-full bg-white shadow-[0_1px_3px_rgba(15,31,23,.06)] transition-colors hover:bg-brand-tint md:grid";

export function Topbar() {
  const { tenant, user } = useTenant();
  const userName = user.name;
  const role = user.role;
  const isDash = usePathname() === "/dashboard";
  const first = userName.split(" ")[0];
  const initials = userName.split(" ").map((p) => p[0]).slice(0, 2).join("");
  return (
    <header className={cn("mb-[18px] flex-wrap items-center gap-3 md:flex md:gap-4", isDash ? "flex" : "hidden")}>
      <div className={cn("min-w-0", !isDash && "hidden")}>
        <h1 className="text-2xl font-extrabold tracking-tight md:text-[28px]" suppressHydrationWarning>
          {greeting()}, {first}
        </h1>
        <p className="mt-0.5 text-[15px] text-[#2a3a33] md:text-[17px]">
          Visão geral da <b className="font-semibold">{tenant.name}</b>
        </p>
      </div>
      <span className="hidden flex-1 md:block" />

      <TenantMenu>
        <button className="flex w-full items-center gap-3 rounded-[18px] bg-white px-[18px] py-2.5 text-left shadow-[0_1px_3px_rgba(15,31,23,.06)] md:w-auto md:min-w-[270px]">
          <Puzzle className="size-[30px] shrink-0 fill-brand text-brand" aria-hidden />
          <span className="min-w-0 flex-1">
            <b className="block truncate text-[17px] font-semibold">{tenant.name}</b>
            <small className="block truncate text-[13px] text-muted">{tenant.segment}</small>
          </span>
          <ChevronDown className="size-5" aria-hidden />
        </button>
      </TenantMenu>

      <button className={round} aria-label="Buscar">
        <Search className="size-6" aria-hidden />
      </button>
      <button className={round} aria-label="Notificações, 1 nova">
        <Bell className="size-6" aria-hidden />
        <span className="absolute right-2.5 top-2 grid size-3.5 place-items-center rounded-full border-2 border-white bg-[#e5324b] text-[8px] font-bold text-white">1</span>
      </button>
      <button className={round} aria-label="Ajuda">
        <CircleHelp className="size-6" aria-hidden />
      </button>
      <Link href="/configuracoes" className="hidden items-center gap-3 p-1 md:flex">
        <span className="grid size-[54px] place-items-center rounded-full bg-brand-dark text-[17px] font-bold text-white">{initials}</span>
        <span>
          <b className="block text-base">{userName}</b>
          <small className="text-sm text-muted">{role}</small>
        </span>
        <ChevronDown className="size-5" aria-hidden />
      </Link>
    </header>
  );
}
