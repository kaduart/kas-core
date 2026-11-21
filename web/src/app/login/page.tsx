import type { Metadata } from "next";
import type { CSSProperties } from "react";
import { headers } from "next/headers";
import { redirect } from "next/navigation";
import { LogIn } from "lucide-react";
import { BrandMark, BrandName } from "@/components/shell/BrandMark";
import { coreConfigFromEnv } from "@/lib/kas/core-client";
import { getSession } from "@/lib/session";
import { themeTokens } from "@/lib/tenant/color";
import { isLocalHost, KAS_DEFAULT_BRAND, tenantByHost } from "@/lib/tenant/presets";
import { LoginForm } from "./LoginForm";

export const metadata: Metadata = { title: "Entrar · KAS Core" };
// Depende das variáveis do servidor e do domínio de cada requisição.
export const dynamic = "force-dynamic";

export default async function LoginPage({ searchParams }: { searchParams: Promise<{ next?: string }> }) {
  // Modo demonstração não tem login.
  if (!coreConfigFromEnv()) redirect("/dashboard");
  if (await getSession().catch(() => null)) redirect("/dashboard");

  const host = (await headers()).get("host");
  const tenant = isLocalHost(host) ? undefined : tenantByHost(host);
  const brand = tenant?.brand ?? KAS_DEFAULT_BRAND;
  const style = themeTokens(brand.primary, brand.accent) as unknown as CSSProperties;
  const { next } = await searchParams;

  return (
    <div style={style} className="flex min-h-dvh flex-col bg-canvas lg:flex-row">
      {/* Painel da marca: mesmo verde e o mesmo recorte curvado do menu lateral. */}
      <aside
        className="relative flex shrink-0 flex-col overflow-hidden px-6 pb-12 pt-8 text-white lg:w-[min(44vw,560px)] lg:py-10 lg:pl-12 lg:pr-0"
        style={{ background: "linear-gradient(180deg, var(--brand-deep) 0%, var(--brand-dark) 55%, var(--brand-deep) 100%)" }}
      >
        <div className="flex items-center gap-3">
          <BrandMark icon={brand.icon} size={44} />
          <div className="min-w-0">
            <BrandName name1={brand.name1} name2={brand.name2} className="block truncate text-[26px] leading-none tracking-tight" />
            <small className="mt-1 block text-[11px] font-semibold opacity-90">{brand.tagline}</small>
          </div>
        </div>

        <div className="mt-10 lg:mt-auto">
          <p className="max-w-[17ch] text-[30px] font-extrabold leading-[1.08] tracking-tight text-balance lg:text-[44px]">
            {tenant ? `Painel da ${tenant.name}` : "Tudo da sua empresa em um painel só."}
          </p>
          <p className="mt-3 max-w-[38ch] text-[15px] text-white/80 lg:text-[16.5px]">
            Leads, receita por campanha e relatórios, com o acesso que o seu administrador liberou.
          </p>
        </div>

        <div className="relative z-[1] mt-auto hidden lg:block">
          <div className="nav-active flex min-h-[54px] items-center gap-4 rounded-l-[28px] pl-[22px] text-[17px] font-bold">
            <LogIn className="size-[22px]" strokeWidth={1.9} aria-hidden />
            Entrar
          </div>
        </div>
        <span className="mb-auto hidden lg:block" />
      </aside>

      <main className="relative -mt-7 flex flex-1 items-start justify-center rounded-t-[28px] bg-canvas px-5 pb-10 pt-9 lg:mt-0 lg:items-center lg:rounded-none lg:px-10">
        <div className="w-full max-w-[400px]">
          <h1 className="text-[26px] font-extrabold tracking-tight md:text-[30px]">Entrar no painel</h1>
          <p className="mt-1 text-[15px] text-muted">Use o e-mail e a senha que o administrador da sua empresa cadastrou.</p>
          <LoginForm next={next} />
          <p className="mt-6 text-[13.5px] text-muted">
            Esqueceu a senha? Peça ao administrador da sua empresa para trocar. Por segurança, o painel não envia senha por e-mail.
          </p>
        </div>
      </main>
    </div>
  );
}
