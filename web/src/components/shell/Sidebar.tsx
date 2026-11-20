"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { ChevronDown, UserRound } from "lucide-react";
import { cn } from "@/lib/cn";
import { NAV_BOTTOM, NAV_TOP, type NavItem } from "@/lib/modules";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { BrandMark, BrandName } from "./BrandMark";
import { TenantMenu } from "./TenantMenu";

function NavLink({ item, active, onNavigate }: { item: NavItem; active: boolean; onNavigate?: () => void }) {
  const Icon = item.icon;
  return (
    <Link
      href={item.href}
      onClick={onNavigate}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex min-h-[50px] items-center gap-4 rounded-l-[28px] pl-[22px] pr-5 text-[16.5px] transition-colors",
        active ? "nav-active font-bold" : "font-medium text-white hover:bg-white/10",
      )}
    >
      <Icon className="size-[22px] shrink-0" strokeWidth={1.8} aria-hidden />
      {item.label}
      {item.badge ? (
        <span className="relative z-[1] ml-auto mr-1.5 grid h-7 min-w-7 place-items-center rounded-full bg-brand px-2 text-[13px] font-bold text-white ring-2 ring-white/15">
          {item.badge}
        </span>
      ) : null}
    </Link>
  );
}

export function Sidebar({ onNavigate }: { onNavigate?: () => void }) {
  const pathname = usePathname();
  const { tenant } = useTenant();
  const top = NAV_TOP.filter((n) => !n.module || tenant.modules.includes(n.module));
  const isActive = (href: string) => pathname === href || pathname.startsWith(href + "/");

  return (
    <aside
      aria-label="Menu principal"
      className="relative flex h-full w-[278px] flex-col overflow-hidden pb-[22px] pl-[14px] pt-7 text-white"
      style={{ background: "linear-gradient(180deg, var(--brand-deep) 0%, var(--brand-dark) 55%, var(--brand-deep) 100%)" }}
    >
      <div className="flex items-center gap-3 pb-[30px] pl-[18px] pr-4">
        <BrandMark icon={tenant.brand.icon} />
        <div className="min-w-0">
          <BrandName name1={tenant.brand.name1} name2={tenant.brand.name2} className="block truncate leading-none tracking-tight"
            style={{ fontSize: (tenant.brand.name1 + tenant.brand.name2).length > 11 ? 22 : 28 }} />
          <small className="mt-1 block whitespace-nowrap text-[10px] font-semibold opacity-90">{tenant.brand.tagline}</small>
        </div>
      </div>

      <nav className="relative z-[1] flex flex-col gap-1.5">
        {top.map((item) => (
          <NavLink key={item.href} item={item} active={isActive(item.href)} onNavigate={onNavigate} />
        ))}
        <div className="my-[26px] ml-2 mr-[22px] h-px bg-white/20" />
        {NAV_BOTTOM.map((item) => (
          <NavLink key={item.href} item={item} active={isActive(item.href)} onNavigate={onNavigate} />
        ))}
      </nav>

      <TenantMenu align="start" side="top">
        <button className="relative z-[1] ml-1.5 mr-[18px] mt-auto flex items-center gap-3 rounded-2xl border border-white/15 bg-white/10 px-3.5 py-3 text-left">
          <span className="grid size-9 shrink-0 place-items-center rounded-full bg-brand">
            <UserRound className="size-[18px]" aria-hidden />
          </span>
          <span className="flex-1 truncate text-base font-bold">{tenant.name}</span>
          <ChevronDown className="size-[18px]" aria-hidden />
        </button>
      </TenantMenu>
    </aside>
  );
}
