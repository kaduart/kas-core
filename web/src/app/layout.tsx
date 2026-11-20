import type { Metadata, Viewport } from "next";
import { Inter } from "next/font/google";
import { getSession } from "@/lib/session";
import { Providers } from "./providers";
import "./globals.css";

const inter = Inter({ subsets: ["latin"], variable: "--font-inter", display: "swap" });

export const metadata: Metadata = {
  title: "KAS Core",
  description: "Plataforma de Gestão e Crescimento",
};

export const viewport: Viewport = { width: "device-width", initialScale: 1, themeColor: "#064023" };

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const session = await getSession();
  return (
    <html lang="pt-BR" className={inter.variable}>
      <body>
        <Providers session={session}>{children}</Providers>
      </body>
    </html>
  );
}
