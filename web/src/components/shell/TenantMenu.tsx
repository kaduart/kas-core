"use client";

import * as Dropdown from "@radix-ui/react-dropdown-menu";
import { Check } from "lucide-react";
import type { ReactNode } from "react";
import { useTenant } from "@/lib/tenant/TenantProvider";

/** Troca de empresa. Na integração, só lista empresas às quais o usuário tem acesso (decidido no Go). */
export function TenantMenu({ children, align = "end", side = "bottom" }: { children: ReactNode; align?: "start" | "end"; side?: "top" | "bottom" }) {
  const { tenant, tenants, switchTenant } = useTenant();
  return (
    <Dropdown.Root>
      <Dropdown.Trigger asChild>{children}</Dropdown.Trigger>
      <Dropdown.Portal>
        <Dropdown.Content
          align={align}
          side={side}
          sideOffset={8}
          className="z-50 min-w-[280px] rounded-2xl bg-white p-1.5 text-ink shadow-[0_18px_40px_rgba(15,31,23,.16)]"
        >
          <Dropdown.Label className="px-3 pb-1 pt-2 text-xs font-semibold text-muted">Empresas</Dropdown.Label>
          {tenants.map((t) => (
            <Dropdown.Item
              key={t.id}
              onSelect={() => switchTenant(t.id)}
              className="flex min-h-12 cursor-pointer items-center gap-3 rounded-xl px-3 outline-none data-[highlighted]:bg-canvas"
            >
              <span className="size-7 shrink-0 rounded-[9px]" style={{ background: t.brand.primary }} aria-hidden />
              <span className="flex-1">
                <b className="block text-[15px] font-semibold">{t.name}</b>
                <small className="text-[13px] text-muted">{t.segment}</small>
              </span>
              {t.id === tenant.id ? <Check className="size-4 text-brand" aria-label="atual" /> : null}
            </Dropdown.Item>
          ))}
        </Dropdown.Content>
      </Dropdown.Portal>
    </Dropdown.Root>
  );
}
