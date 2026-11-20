import type { Metadata, Viewport } from "next";
import { Inter } from "next/font/google";
import { cookies, headers } from "next/headers";
import { brandCookieName, parseBrandCookie } from "@/lib/tenant/brand-cookie";
import { resolveTenant, TENANT_COOKIE, TENANTS } from "@/lib/tenant/presets";
import type { TenantBrand } from "@/lib/tenant/types";
import { Providers } from "./providers";
import "./globals.css";

const inter = Inter({ subsets: ["latin"], variable: "--font-inter", display: "swap" });

export const metadata: Metadata = {
  title: "KAS Core",
  description: "Plataforma de Gestão e Crescimento",
};

export const viewport: Viewport = { width: "device-width", initialScale: 1, themeColor: "#064023" };

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const [jar, h] = await Promise.all([cookies(), headers()]);
  const tenant = resolveTenant({ cookie: jar.get(TENANT_COOKIE)?.value, host: h.get("host") });
  const brands: Record<string, TenantBrand> = {};
  for (const t of TENANTS) {
    const b = parseBrandCookie(jar.get(brandCookieName(t.id))?.value);
    if (b) brands[t.id] = b;
  }
  return (
    <html lang="pt-BR" className={inter.variable}>
      <body>
        <Providers initialTenantId={tenant.id} initialBrands={brands}>{children}</Providers>
      </body>
    </html>
  );
}
