"use client";

import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Bell, Menu, X } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";
import { useTenant } from "@/lib/tenant/TenantProvider";
import { BrandMark, BrandName } from "./BrandMark";
import { Sidebar } from "./Sidebar";
import { Topbar } from "./Topbar";

export function AppShell({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const reduce = useReducedMotion();
  const { tenant } = useTenant();

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  return (
    <div className="flex min-h-dvh">
      <div className="sticky top-0 hidden h-dvh shrink-0 lg:block">
        <Sidebar />
      </div>

      <AnimatePresence>
        {open ? (
          <>
            <motion.button
              key="scrim"
              aria-label="Fechar menu"
              className="fixed inset-0 z-40 bg-[rgba(10,25,18,.45)] lg:hidden"
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              onClick={() => setOpen(false)}
            />
            <motion.div
              key="drawer"
              role="dialog"
              aria-modal="true"
              aria-label="Menu"
              className="fixed inset-y-0 left-0 z-50 shadow-[20px_0_50px_rgba(0,0,0,.3)] lg:hidden"
              initial={reduce ? false : { x: "-100%" }}
              animate={{ x: 0 }}
              exit={reduce ? { opacity: 0 } : { x: "-100%" }}
              transition={{ type: "tween", duration: 0.25, ease: [0.2, 0.8, 0.2, 1] }}
            >
              <button onClick={() => setOpen(false)} aria-label="Fechar menu" className="absolute right-3 top-3 z-10 grid size-11 place-items-center rounded-xl text-white hover:bg-white/10">
                <X className="size-5" aria-hidden />
              </button>
              <Sidebar onNavigate={() => setOpen(false)} />
            </motion.div>
          </>
        ) : null}
      </AnimatePresence>

      <div className="min-w-0 flex-1">
        <div className="sticky top-0 z-30 flex items-center gap-2.5 bg-brand-deep px-3.5 py-2.5 text-white lg:hidden">
          <button onClick={() => setOpen(true)} aria-label="Abrir menu" className="grid size-11 place-items-center rounded-xl bg-white/12">
            <Menu className="size-[22px]" aria-hidden />
          </button>
          <BrandMark icon={tenant.brand.icon} size={30} />
          <BrandName name1={tenant.brand.name1} name2={tenant.brand.name2} className="truncate text-xl tracking-tight" />
          <span className="flex-1" />
          <button aria-label="Notificações, 1 nova" className="relative grid size-11 place-items-center rounded-xl bg-white/12">
            <Bell className="size-5" aria-hidden />
            <span className="absolute right-2.5 top-2 size-2.5 rounded-full bg-[#e5324b]" />
          </button>
        </div>
        <main className="mx-auto max-w-[1440px] px-3.5 pb-8 pt-4 lg:px-6">
          <Topbar />
          {children}
        </main>
      </div>
    </div>
  );
}
